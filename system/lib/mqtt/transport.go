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
	// MinFailures is the number of failed connection attempts in one outage
	// before TCP is given up on.
	MinFailures int
	// MinOutage is how long the connection must have been down, without one
	// success, before TCP is given up on. Both thresholds must be met, so a
	// short broker restart does not move the device off TCP.
	MinOutage time.Duration
	// ProbeInterval is how often a device on WSS checks whether TCP works again.
	ProbeInterval time.Duration
}

// DefaultPolicy: two failed attempts over at least 60 s of outage, TCP re-check
// every 30 minutes.
var DefaultPolicy = Policy{
	MinFailures:   2,
	MinOutage:     60 * time.Second,
	ProbeInterval: 30 * time.Minute,
}

// Status is a snapshot of the transport state.
type Status struct {
	Transport Transport
	// WSURLKnown is true when a usable WSS URL is configured.
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

	wsKnown bool
	cur     Transport

	connected   bool
	failures    int       // failed attempts in the current outage
	outageSince time.Time // zero while connected
	wssSince    time.Time // when TCP was last given up on or last probed
	connects    int
	switches    int
}

// NewSelector starts on TCP. wsKnown says whether a WSS URL is available.
func NewSelector(p Policy, wsKnown bool, now func() time.Time) *Selector {
	if now == nil {
		now = time.Now
	}
	return &Selector{now: now, policy: p, wsKnown: wsKnown, cur: TransportTCP}
}

// Reset returns to TCP with fresh counters; call it when the broker config changed.
func (s *Selector) Reset(wsKnown bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wsKnown = wsKnown
	s.cur = TransportTCP
	s.connected = false
	s.failures = 0
	s.outageSince = time.Time{}
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
	return Status{Transport: s.cur, WSURLKnown: s.wsKnown, Connected: s.connected, Connects: s.connects, Switches: s.switches}
}

// Started marks the beginning of a connection effort, so an outage is timed
// from the first attempt and not from the first failure.
func (s *Selector) Started() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected && s.outageSince.IsZero() {
		s.outageSince = s.now()
	}
}

// Up records a successful connection.
func (s *Selector) Up() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = true
	s.connects++
	s.failures = 0
	s.outageSince = time.Time{}
}

// Down records a lost connection; the outage clock starts now.
func (s *Selector) Down() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
	if s.outageSince.IsZero() {
		s.outageSince = s.now()
	}
}

// Failure records a failed connection attempt and reports whether the client
// must switch to Current(). On TCP with a WSS URL known, TCP is dropped after
// MinFailures failures over MinOutage. On WSS, when WSS itself stays down for
// twice that, the client goes back to TCP so a bad WSS URL cannot strand it.
func (s *Selector) Failure() (switched bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.connected = false
	if s.outageSince.IsZero() {
		s.outageSince = now
	}
	s.failures++
	outage := now.Sub(s.outageSince)
	switch s.cur {
	case TransportTCP:
		if s.wsKnown && s.failures >= s.policy.MinFailures && outage >= s.policy.MinOutage {
			s.moveLocked(TransportWSS, now)
			return true
		}
	case TransportWSS:
		if s.failures >= s.policy.MinFailures && outage >= 2*s.policy.MinOutage {
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

// moveLocked changes transport and restarts the outage accounting for it.
func (s *Selector) moveLocked(t Transport, now time.Time) {
	s.cur = t
	s.switches++
	s.failures = 0
	s.outageSince = now
	s.connected = false
	if t == TransportWSS {
		s.wssSince = now
	}
}
