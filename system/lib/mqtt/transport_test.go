package mqtt

import (
	"crypto/tls"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

const testWSURL = "wss://ws.example/mqtt"

func newTestSelector(ws bool) (*Selector, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	url := ""
	if ws {
		url = testWSURL
	}
	return NewSelector(DefaultPolicy, url, clk.now), clk
}

// failN records n failed connects and returns whether the last one asked to switch.
func failN(s *Selector, n int) (switched bool) {
	for i := 0; i < n; i++ {
		switched = s.Failure()
	}
	return switched
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

func TestDefaultPolicy(t *testing.T) {
	if DefaultPolicy.MinFailures != 3 || DefaultPolicy.ProbeInterval != 10*time.Minute {
		t.Fatalf("DefaultPolicy = %+v", DefaultPolicy)
	}
	if DefaultKeepAlive != 60 || DefaultConnectTimeout != 10*time.Second {
		t.Fatalf("keepalive %d, connect timeout %v", DefaultKeepAlive, DefaultConnectTimeout)
	}
}

func TestSelectorStaysOnTCPWithoutWSURL(t *testing.T) {
	s, _ := newTestSelector(false)
	if failN(s, 50) {
		t.Fatal("no ws url: must never switch")
	}
	if s.Current() != TransportTCP {
		t.Fatalf("current = %s", s.Current())
	}
}

func TestSelectorThreeFailuresInARowSwitchWithoutWaiting(t *testing.T) {
	s, clk := newTestSelector(true)
	// No outage clause: three failures right away are enough.
	if failN(s, 2) {
		t.Fatal("two failures must not switch")
	}
	clk.advance(time.Second)
	if !s.Failure() {
		t.Fatal("third failure in a row must switch to WSS")
	}
	if s.Current() != TransportWSS {
		t.Fatalf("current = %s", s.Current())
	}
	if st := s.Status(); st.Switches != 1 || st.Connected {
		t.Fatalf("status = %+v", st)
	}
}

func TestSelectorSuccessResetsFailureCount(t *testing.T) {
	s, _ := newTestSelector(true)
	failN(s, 2)
	s.Up()
	if failN(s, 2) {
		t.Fatal("failures before a success must not count")
	}
	if !s.Failure() {
		t.Fatal("three in a row after the success must switch")
	}
}

func TestSelectorDroppedSessionIsNotAFailure(t *testing.T) {
	s, _ := newTestSelector(true)
	failN(s, 2)
	s.Up()
	s.Down() // a session that connected and dropped
	if failN(s, 2) {
		t.Fatal("a drop restarts the count from zero")
	}
	if !s.Failure() {
		t.Fatal("three failed reconnects after the drop must switch")
	}
}

func TestSelectorProbeBackToTCP(t *testing.T) {
	s, clk := newTestSelector(true)
	if !failN(s, 3) {
		t.Fatal("setup: expected WSS")
	}
	s.Up()
	if s.ProbeDue() {
		t.Fatal("probe due immediately after the switch")
	}
	clk.advance(9*time.Minute + 59*time.Second)
	if s.ProbeDue() {
		t.Fatal("probe due before 10 min")
	}
	clk.advance(time.Second)
	if !s.ProbeDue() {
		t.Fatal("probe must be due at 10 min")
	}
	// Failed probe: stay on WSS and wait another interval.
	if s.ProbeDone(false) {
		t.Fatal("failed probe must not switch")
	}
	if s.ProbeDue() {
		t.Fatal("failed probe must restart the interval")
	}
	clk.advance(10 * time.Minute)
	if !s.ProbeDue() {
		t.Fatal("probe due again after another 10 min")
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
	failN(s, 3) // -> WSS
	clk.advance(10 * time.Minute)
	if !s.ProbeDone(true) {
		t.Fatal("expected switch back")
	}
	// TCP accepts the probe but then keeps failing: back to WSS by the same rule.
	if failN(s, 2) {
		t.Fatal("too early")
	}
	if !s.Failure() || s.Current() != TransportWSS {
		t.Fatalf("expected WSS again, current = %s", s.Current())
	}
}

func TestSelectorBadWSSReturnsToTCP(t *testing.T) {
	s, _ := newTestSelector(true)
	failN(s, 3) // -> WSS
	if failN(s, 5) {
		t.Fatal("WSS gets twice the failures before it is dropped")
	}
	if !s.Failure() || s.Current() != TransportTCP {
		t.Fatalf("expected TCP, current = %s", s.Current())
	}
}

func TestSelectorNewWSURLNeverMovesTheConnection(t *testing.T) {
	s, clk := newTestSelector(true)
	s.Up() // working on TCP
	s.SetWSURL("wss://other.example/mqtt")
	if st := s.Status(); st.Transport != TransportTCP || !st.Connected || st.Connects != 1 || st.Switches != 0 {
		t.Fatalf("status = %+v", st)
	}
	if s.WSURL() != "wss://other.example/mqtt" {
		t.Fatalf("url = %q", s.WSURL())
	}
	// Now on WSS: a new URL keeps the session on WSS and does not reset to TCP.
	s.Down()
	failN(s, 3)
	s.Up()
	s.SetWSURL("wss://third.example/mqtt")
	if st := s.Status(); st.Transport != TransportWSS || !st.Connected {
		t.Fatalf("status = %+v", st)
	}
	clk.advance(time.Minute)
	if s.ProbeDue() {
		t.Fatal("a url change must not make a probe due")
	}
	// An unusable URL clears the fallback for the next time.
	s.SetWSURL("ws://insecure.example/mqtt")
	if s.WSURL() != "" || s.Status().WSURLKnown {
		t.Fatal("a non-wss url must leave no fallback")
	}
}

func TestSelectorURLRemovedWhileFailingStaysOnTCP(t *testing.T) {
	s, _ := newTestSelector(true)
	failN(s, 2)
	s.SetWSURL("")
	if s.Failure() {
		t.Fatal("no url: must not switch")
	}
}

func TestSelectorResetKeepsURL(t *testing.T) {
	s, _ := newTestSelector(true)
	failN(s, 3)
	s.Reset()
	if st := s.Status(); st.Transport != TransportTCP || !st.WSURLKnown || st.Switches != 0 || st.Connects != 0 {
		t.Fatalf("status after reset = %+v", st)
	}
}

func TestFactoryShareSelectorAndURLUpdate(t *testing.T) {
	f, err := ProvideFactory(Config{Endpoint: "broker.example", Port: 1883, WSURL: testWSURL})
	if err != nil {
		t.Fatal(err)
	}
	c := f.GetClient("a")
	if c.opts.WSURL != testWSURL || c.sel != f.sel || c.fallback {
		t.Fatalf("plain client: %+v", c)
	}
	if fc := f.GetFallbackClient("b"); !fc.fallback || fc.sel != f.sel {
		t.Fatal("fallback client must drive the shared selector")
	}
	// A URL update reaches the shared state in place: no reset, no restart.
	f.sel.Up()
	f.SetWSURL("wss://other.example/mqtt")
	if f.sel.WSURL() != "wss://other.example/mqtt" {
		t.Fatal("url not updated")
	}
	if st := f.Status(); st.Connects != 1 || st.Transport != TransportTCP || !st.Connected {
		t.Fatalf("status = %+v", st)
	}
	// An invalid URL means no fallback.
	f.UpdateConfig(Config{Endpoint: "broker.example", Port: 1883, WSURL: "ws://insecure.example/mqtt"})
	if f.Status().WSURLKnown {
		t.Fatal("a ws:// URL must not enable the fallback")
	}
	// A config update (client restart) starts on TCP.
	f.UpdateConfig(Config{Endpoint: "broker.example", Port: 1883, WSURL: "wss://other.example/mqtt"})
	if st := f.Status(); !st.WSURLKnown || st.Transport != TransportTCP || st.Connects != 0 {
		t.Fatalf("status = %+v", st)
	}
}
