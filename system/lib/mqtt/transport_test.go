package mqtt

import (
	"crypto/tls"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func newTestSelector(ws bool) (*Selector, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	return NewSelector(DefaultPolicy, ws, clk.now), clk
}

func TestNormalizeWSURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"wss://broker.example/mqtt", "wss://broker.example/mqtt", true},
		{"  wss://broker.example:443/mqtt ", "wss://broker.example:443/mqtt", true},
		{"WSS://broker.example/mqtt", "WSS://broker.example/mqtt", true},
		{"", "", false},
		{"ws://broker.example/mqtt", "", false}, // never a plain WebSocket
		{"https://broker.example/mqtt", "", false},
		{"mqtt://broker.example:1883", "", false},
		{"wss:///mqtt", "", false},
		{"broker.example/mqtt", "", false},
		{"wss://%zz", "", false},
	}
	for _, tt := range tests {
		got, ok := NormalizeWSURL(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("NormalizeWSURL(%q) = %q, %t; want %q, %t", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestServerURLForWSS(t *testing.T) {
	o := Options{Endpoint: "broker.example", Port: 1883, WSURL: " wss://ws.example/mqtt "}
	u, err := o.ServerURLFor(TransportWSS)
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "wss://ws.example/mqtt" {
		t.Fatalf("wss url = %q", u)
	}
	u, err = o.ServerURLFor(TransportTCP)
	if err != nil || u.String() != "mqtt://broker.example:1883" {
		t.Fatalf("tcp url = %v, %v", u, err)
	}
	for _, bad := range []string{"", "ws://ws.example/mqtt", "nonsense"} {
		o.WSURL = bad
		if _, err := o.ServerURLFor(TransportWSS); err == nil {
			t.Fatalf("WSURL %q must be rejected", bad)
		}
	}
}

func TestTLSConfigForWSS(t *testing.T) {
	// WSS is always TLS and checks the WSS host, even when the TCP side is plain.
	o := Options{Endpoint: "broker.example", Port: 1883, WSURL: "wss://ws.example:443/mqtt"}
	if o.TLSConfigFor(TransportTCP) != nil {
		t.Fatal("plain TCP must stay without TLS")
	}
	cfg := o.TLSConfigFor(TransportWSS)
	if cfg == nil {
		t.Fatal("wss needs a TLS config")
	}
	if cfg.ServerName != "ws.example" || cfg.MinVersion != tls.VersionTLS12 || cfg.InsecureSkipVerify {
		t.Fatalf("unexpected TLS config: %+v", cfg)
	}
	o.TLS = true
	if cfg := o.TLSConfigFor(TransportTCP); cfg == nil || cfg.ServerName != "broker.example" {
		t.Fatalf("tls tcp config = %+v", cfg)
	}
}

func TestSelectorStaysOnTCPWithoutWSURL(t *testing.T) {
	s, clk := newTestSelector(false)
	s.Started()
	for i := 0; i < 20; i++ {
		clk.advance(30 * time.Second)
		if s.Failure() {
			t.Fatal("no ws url: must never switch")
		}
	}
	if s.Current() != TransportTCP {
		t.Fatalf("current = %s", s.Current())
	}
}

func TestSelectorSwitchesToWSSAfterOutage(t *testing.T) {
	s, clk := newTestSelector(true)
	s.Started()

	// Failures alone are not enough: a broker restart must not leave TCP.
	for i := 0; i < 5; i++ {
		clk.advance(2 * time.Second)
		if s.Failure() {
			t.Fatalf("switched after only %ds of outage", (i+1)*2)
		}
	}
	// Time alone is not enough either: one slow failure does not count as "failing".
	s2, clk2 := newTestSelector(true)
	s2.Started()
	clk2.advance(5 * time.Minute)
	if s2.Failure() {
		t.Fatal("one failure must not switch, however long it took")
	}
	if s2.Current() != TransportTCP {
		t.Fatalf("one failure switched transport: %s", s2.Current())
	}

	// 60 s down and at least two failures: give up on TCP.
	clk.advance(60 * time.Second)
	if !s.Failure() {
		t.Fatal("expected switch to WSS")
	}
	if s.Current() != TransportWSS {
		t.Fatalf("current = %s", s.Current())
	}
	if st := s.Status(); st.Switches != 1 || st.Connected {
		t.Fatalf("status = %+v", st)
	}
}

func TestSelectorBlackholedTCP(t *testing.T) {
	// TCP black-holed: each attempt takes the 30 s connect timeout plus ~7 s of
	// backoff, so the second failure lands at about 75 s and decides.
	s, clk := newTestSelector(true)
	s.Started()
	clk.advance(35 * time.Second)
	if s.Failure() {
		t.Fatal("first failure must not switch")
	}
	clk.advance(40 * time.Second)
	if !s.Failure() {
		t.Fatal("second failure after 75 s must switch")
	}
}

func TestSelectorUpResetsOutage(t *testing.T) {
	s, clk := newTestSelector(true)
	s.Started()
	clk.advance(50 * time.Second)
	s.Failure()
	s.Up()
	st := s.Status()
	if !st.Connected || st.Connects != 1 {
		t.Fatalf("status = %+v", st)
	}
	// Drop later: the outage clock restarts, so old failures do not count.
	clk.advance(time.Hour)
	s.Down()
	clk.advance(30 * time.Second)
	if s.Failure() {
		t.Fatal("outage of 30 s with one failure must not switch")
	}
	// A dropped session that never comes back still times out like a first connect.
	clk.advance(40 * time.Second)
	if !s.Failure() {
		t.Fatal("70 s outage with two failures must switch")
	}
}

func TestSelectorProbeBackToTCP(t *testing.T) {
	s, clk := newTestSelector(true)
	s.Started()
	clk.advance(30 * time.Second)
	s.Failure()
	clk.advance(40 * time.Second)
	if !s.Failure() {
		t.Fatal("setup: expected WSS")
	}
	s.Up()
	if s.ProbeDue() {
		t.Fatal("probe due immediately after the switch")
	}
	clk.advance(29 * time.Minute)
	if s.ProbeDue() {
		t.Fatal("probe due before 30 min")
	}
	clk.advance(time.Minute)
	if !s.ProbeDue() {
		t.Fatal("probe must be due after 30 min")
	}
	// Failed probe: stay on WSS and wait another interval.
	if s.ProbeDone(false) {
		t.Fatal("failed probe must not switch")
	}
	if s.ProbeDue() {
		t.Fatal("failed probe must restart the interval")
	}
	clk.advance(30 * time.Minute)
	if !s.ProbeDue() {
		t.Fatal("probe due again after another 30 min")
	}
	if !s.ProbeDone(true) {
		t.Fatal("working TCP must switch back")
	}
	if s.Current() != TransportTCP {
		t.Fatalf("current = %s", s.Current())
	}
	if s.ProbeDue() {
		t.Fatal("no probing while on TCP")
	}
	if st := s.Status(); st.Switches != 2 || st.Connects != 1 {
		t.Fatalf("status = %+v", st)
	}
}

func TestSelectorTCPFailsAgainAfterSwitchBack(t *testing.T) {
	s, clk := newTestSelector(true)
	s.Started()
	clk.advance(30 * time.Second)
	s.Failure()
	clk.advance(40 * time.Second)
	s.Failure() // -> WSS
	clk.advance(30 * time.Minute)
	if !s.ProbeDone(true) {
		t.Fatal("expected switch back")
	}
	// TCP accepts the probe but then keeps failing: back to WSS after the same rule.
	clk.advance(30 * time.Second)
	if s.Failure() {
		t.Fatal("too early")
	}
	clk.advance(40 * time.Second)
	if !s.Failure() || s.Current() != TransportWSS {
		t.Fatalf("expected WSS again, current = %s", s.Current())
	}
}

func TestSelectorBadWSSReturnsToTCP(t *testing.T) {
	s, clk := newTestSelector(true)
	s.Started()
	clk.advance(30 * time.Second)
	s.Failure()
	clk.advance(40 * time.Second)
	s.Failure() // -> WSS
	clk.advance(60 * time.Second)
	if s.Failure() {
		t.Fatal("WSS must get 2x the outage before it is dropped")
	}
	clk.advance(70 * time.Second)
	if !s.Failure() || s.Current() != TransportTCP {
		t.Fatalf("expected TCP, current = %s", s.Current())
	}
}

func TestSelectorReset(t *testing.T) {
	s, clk := newTestSelector(true)
	s.Started()
	clk.advance(30 * time.Second)
	s.Failure()
	clk.advance(40 * time.Second)
	s.Failure()
	s.Reset(false)
	if st := s.Status(); st.Transport != TransportTCP || st.WSURLKnown || st.Switches != 0 || st.Connects != 0 {
		t.Fatalf("status after reset = %+v", st)
	}
}

func TestFactoryShareSelectorAndReset(t *testing.T) {
	f, err := ProvideFactory(Config{Endpoint: "broker.example", Port: 1883, WSURL: "wss://ws.example/mqtt"})
	if err != nil {
		t.Fatal(err)
	}
	c := f.GetClient("a")
	if c.opts.WSURL != "wss://ws.example/mqtt" || c.sel != f.sel || c.fallback {
		t.Fatalf("plain client: %+v", c)
	}
	if fc := f.GetFallbackClient("b"); !fc.fallback || fc.sel != f.sel {
		t.Fatal("fallback client must drive the shared selector")
	}
	if !f.Status().WSURLKnown {
		t.Fatal("ws url must be known")
	}
	// An invalid URL means no fallback.
	f.UpdateConfig(Config{Endpoint: "broker.example", Port: 1883, WSURL: "ws://insecure.example/mqtt"})
	if f.Status().WSURLKnown {
		t.Fatal("a ws:// URL must not enable the fallback")
	}
	f.UpdateConfig(Config{Endpoint: "broker.example", Port: 1883, WSURL: "wss://other.example/mqtt"})
	if st := f.Status(); !st.WSURLKnown || st.Transport != TransportTCP {
		t.Fatalf("status = %+v", st)
	}
}
