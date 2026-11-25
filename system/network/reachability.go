package network

import (
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"go.autonomous.ai/os/system/lib/urlnorm"
)

// Internet reachability.
//
// ICMP (icmpTargets) is the primary probe, but some networks (corporate, hotel,
// some mesh routers) drop ICMP while the web works. With ICMP alone those
// networks failed setup right after the WiFi join — the device tore down its
// hotspot, never reached the cloud, and the customer was dropped back on the
// WiFi form with no explanation. A TLS handshake with the device's own cloud
// API host is the fallback: it is the host setup actually needs next.

const (
	// cloudProbeTimeout bounds the TLS fallback so a dead network still fails
	// fast inside setup's 60s window, while a slow first lookup or a far
	// server still passes.
	cloudProbeTimeout = 5 * time.Second
	// monitorCloudProbeTimeout bounds the network monitor's TLS fallback.
	monitorCloudProbeTimeout = 4 * time.Second
	// defaultCloudProbeAddr is used before config.json carries an Autonomous
	// base URL (a fresh device joins WiFi before setup saves it).
	defaultCloudProbeAddr = "campaign-api.autonomous.ai:443"
)

// icmpTargets are pinged in order. The first is the probe every device has
// always used; the second is a public resolver that answers on networks where
// the first is blocked. It is only pinged when the first has failed or has not
// replied within icmpHedgeDelay, so a network where the first answers sees
// exactly the old traffic.
var icmpTargets = []string{"8.8.8.8", "223.5.5.5"}

// icmpHedgeDelay is how long pingAny waits on one target before it also pings
// the next one (a variable so tests can shorten it).
var icmpHedgeDelay = time.Second

// pingOnce sends one ICMP echo to target and returns ping's output (a variable
// so tests can replace it).
var pingOnce = func(target, timeoutSec string) ([]byte, error) {
	return exec.Command("ping", "-c", "1", "-W", timeoutSec, target).CombinedOutput()
}

// pingAny pings targets in order and returns the output of the first reply.
// The next target is started when the current one fails or after
// icmpHedgeDelay, whichever comes first, so the total wait is at most one ping
// timeout plus the hedge delays. When none replies it returns the first
// target's output and error.
func pingAny(targets []string, timeoutSec string) ([]byte, error) {
	if len(targets) == 0 {
		return nil, errors.New("no ping targets")
	}
	type result struct {
		err error
		out []byte
		idx int
	}
	results := make(chan result, len(targets))
	start := func(i int) {
		go func() {
			out, err := pingOnce(targets[i], timeoutSec)
			results <- result{err: err, out: out, idx: i}
		}()
	}
	next, pending := 1, 1
	start(0)
	hedge := time.NewTimer(icmpHedgeDelay)
	defer hedge.Stop()
	var firstOut []byte
	var firstErr error
	for pending > 0 {
		select {
		case r := <-results:
			pending--
			if r.err == nil {
				return r.out, nil
			}
			if r.idx == 0 {
				firstOut, firstErr = r.out, r.err
			}
			if next < len(targets) {
				start(next)
				next++
				pending++
				hedge.Reset(icmpHedgeDelay)
			}
		case <-hedge.C:
			if next < len(targets) {
				start(next)
				next++
				pending++
				hedge.Reset(icmpHedgeDelay)
			}
		}
	}
	return firstOut, firstErr
}

// Probes, as variables so tests can replace them.
var (
	// icmpPing pings icmpTargets (see pingAny) and returns the output of the
	// first that replies.
	icmpPing = func(timeoutSec string) ([]byte, error) {
		return pingAny(icmpTargets, timeoutSec)
	}
	// tlsReach completes a TLS handshake with addr and closes it.
	tlsReach = func(addr string, timeout time.Duration) error {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return err
		}
		// Certificate checks are skipped on purpose: this only proves the host
		// is reachable and sends nothing, and it must work before NTP has fixed
		// the clock — devices have no RTC, so right after the WiFi join every
		// certificate still looks "not yet valid".
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", addr,
			&tls.Config{ServerName: host, InsecureSkipVerify: true}) //nolint:gosec // reachability probe only, see above
		if err != nil {
			return err
		}
		return conn.Close()
	}
)

// cloudProbeAddr is host:port of the device's Autonomous cloud API, or the
// default before setup has saved one. An owner's own LLM provider is never
// probed: reachability is about our cloud.
func (s *Service) cloudProbeAddr() string {
	base := strings.TrimSpace(s.config.LLMBaseURL)
	if base == "" || !urlnorm.IsAutonomousHost(base) {
		return defaultCloudProbeAddr
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return defaultCloudProbeAddr
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// reachableOverTLS is the fallback probe used when ICMP fails.
func (s *Service) reachableOverTLS() (string, error) {
	return s.reachableOverTLSWithin(cloudProbeTimeout)
}

// reachableOverTLSWithin is reachableOverTLS with its own timeout.
func (s *Service) reachableOverTLSWithin(timeout time.Duration) (string, error) {
	addr := s.cloudProbeAddr()
	return addr, tlsReach(addr, timeout)
}
