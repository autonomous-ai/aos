package mqtt

import (
	"net/url"
	"strings"
	"sync"
	"time"
)

// Transport is how the client reaches the broker.
type Transport string

const (
	// TransportTCP is the primary transport: plain MQTT or MQTT over TLS.
	TransportTCP Transport = "tcp"
	// TransportWSS is the fallback: MQTT over a secure WebSocket on a URL the
	// backend hands out, with the same credentials and channels as TCP.
	TransportWSS Transport = "wss"
)

// NormalizeWSURL validates the WebSocket URL from the backend. Only wss with a
// host is accepted, so credentials are never sent over a plain WebSocket. It
// returns the trimmed URL and whether it is usable.
func NormalizeWSURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "wss") || u.Hostname() == "" {
		return "", false
	}
	return raw, true
}

// Policy tunes when the client leaves TCP for WSS and when it tries TCP again.
type Policy struct {
	// MinFailures is the number of failed connection attempts in a row, none of
	// them reaching the broker's CONNACK, before TCP is given up on. A session
	// that connected and then dropped does not count.
	MinFailures int
	// ProbeInterval is how often a device on WSS checks whether TCP works again.
	ProbeInterval time.Duration
}

// DefaultPolicy: three failed connects in a row move the client to WSS, and it
// re-checks TCP every 10 minutes. If WSS itself fails twice as many times in a
// row, the client returns to TCP.
var DefaultPolicy = Policy{
	MinFailures:   3,
	ProbeInterval: 10 * time.Minute,
}

// Status is a snapshot of the transport state.
type Status struct {
	Transport Transport
	// WSURLKnown is true when a usable WSS URL is held.
	WSURLKnown bool
	Connected  bool
	// Connects counts successful connections (the first one included);
	// reconnects are Connects-1 once connected at least once.
	Connects int
	// Switches counts moves between TCP and WSS.
	Switches int
}

// Selector is the transport fallback state machine. It is pure: time comes from
// an injectable clock and it does no I/O, so the client drives it and tests
// advance a fake clock. Safe for concurrent use.
type Selector struct {
	mu     sync.Mutex
	now    func() time.Time
	policy Policy

	wsURL string // normalized wss URL, "" when none
	cur   Transport

	connected bool
	failures  int       // failed attempts in a row since the last success
	wssSince  time.Time // when TCP was last given up on or last probed
	connects  int
	switches  int
}

// NewSelector starts on TCP. wsURL is the wss fallback URL ("" or invalid: none).
func NewSelector(p Policy, wsURL string, now func() time.Time) *Selector {
	if now == nil {
		now = time.Now
	}
	u, _ := NormalizeWSURL(wsURL)
	return &Selector{now: now, policy: p, wsURL: u, cur: TransportTCP}
}

// SetWSURL replaces the wss URL in place. It never moves a working connection:
// the new URL is used the next time the client falls back to WSS.
func (s *Selector) SetWSURL(raw string) {
	u, _ := NormalizeWSURL(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wsURL = u
}

// WSURL returns the wss URL to use for the WSS transport, "" when none.
func (s *Selector) WSURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wsURL
}

// Reset returns to TCP with fresh counters; call it when the broker config
// changed and the client restarts. The wss URL is kept.
func (s *Selector) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = TransportTCP
	s.connected = false
	s.failures = 0
	s.wssSince = time.Time{}
	s.connects = 0
	s.switches = 0
}

// Current is the transport the next connection attempt should use.
func (s *Selector) Current() Transport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Status returns a snapshot.
func (s *Selector) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Transport: s.cur, WSURLKnown: s.wsURL != "", Connected: s.connected, Connects: s.connects, Switches: s.switches}
}

// Up records a successful connection.
func (s *Selector) Up() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = true
	s.connects++
	s.failures = 0
}

// Down records a lost session. It is not a failed connect: the count of failures
// in a row starts from zero, as for a first connect.
func (s *Selector) Down() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
	s.failures = 0
}

// Failure records a failed connection attempt (one that never reached CONNACK)
// and reports whether the client must switch to Current(). On TCP with a WSS URL
// held, TCP is dropped after MinFailures failures in a row. On WSS, twice as many
// failures in a row send the client back to TCP, so a bad WSS URL cannot strand it.
func (s *Selector) Failure() (switched bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.connected = false
	s.failures++
	switch s.cur {
	case TransportTCP:
		if s.wsURL != "" && s.failures >= s.policy.MinFailures {
			s.moveLocked(TransportWSS, now)
			return true
		}
	case TransportWSS:
		if s.failures >= 2*s.policy.MinFailures {
			s.moveLocked(TransportTCP, now)
			return true
		}
	}
	return false
}

// ProbeDue reports whether a device on WSS should check TCP again.
func (s *Selector) ProbeDue() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur == TransportWSS && s.now().Sub(s.wssSince) >= s.policy.ProbeInterval
}

// ProbeDone records the result of a TCP probe and reports whether the client
// must switch back to TCP. A failed probe restarts the interval.
func (s *Selector) ProbeDone(ok bool) (switched bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.cur != TransportWSS {
		return false
	}
	if !ok {
		s.wssSince = now
		return false
	}
	s.moveLocked(TransportTCP, now)
	return true
}

// moveLocked changes transport and restarts the failure count for it.
func (s *Selector) moveLocked(t Transport, now time.Time) {
	s.cur = t
	s.switches++
	s.failures = 0
	s.connected = false
	if t == TransportWSS {
		s.wssSince = now
	}
}
