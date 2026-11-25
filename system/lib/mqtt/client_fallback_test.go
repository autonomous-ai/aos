package mqtt

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// connack is an MQTT 5 CONNACK: success, no session, no properties.
var connack = []byte{0x20, 0x03, 0x00, 0x00, 0x00}

// startWSBroker is a minimal MQTT-over-WSS broker that accepts every CONNECT.
func startWSBroker(t *testing.T) (srv *httptest.Server, accepted *atomic.Int32, tlsCfg *tls.Config) {
	t.Helper()
	accepted = &atomic.Int32{}
	up := websocket.Upgrader{Subprotocols: []string{"mqtt"}}
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		if _, _, err := ws.ReadMessage(); err != nil { // CONNECT
			return
		}
		accepted.Add(1)
		if err := ws.WriteMessage(websocket.BinaryMessage, connack); err != nil {
			return
		}
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, accepted, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// startTCPBroker listens on addr and accepts every CONNECT.
func startTCPBroker(t *testing.T, addr string) (ln net.Listener, accepted *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	accepted = &atomic.Int32{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 4096)
				if _, err := conn.Read(buf); err != nil { // CONNECT
					return
				}
				accepted.Add(1)
				if _, err := conn.Write(connack); err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln, accepted
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fastBackoff(int) time.Duration { return 20 * time.Millisecond }

// TestFallbackToWSSAndBack runs the real client against a dead TCP port and a
// working WSS broker: it must end up on WSS, then return to TCP once TCP works.
func TestFallbackToWSSAndBack(t *testing.T) {
	oldPoll := supervisePoll
	supervisePoll = 30 * time.Millisecond
	t.Cleanup(func() { supervisePoll = oldPoll })

	srv, wsAccepted, tlsCfg := startWSBroker(t)
	tcpAddr := freeAddr(t)
	host, port, _ := net.SplitHostPort(tcpAddr)
	var portNum int
	for _, ch := range port {
		portNum = portNum*10 + int(ch-'0')
	}

	sel := NewSelector(Policy{MinFailures: 3, ProbeInterval: time.Hour}, "wss://"+strings.TrimPrefix(srv.URL, "https://")+"/mqtt", time.Now)
	c := ProvideClient(Options{
		Endpoint:       host,
		Port:           portNum,
		ClientID:       "test-client",
		ConnectTimeout: 2 * time.Second,
	})
	c.sel = sel
	c.fallback = true
	c.backoff = fastBackoff
	c.tlsOverride = tlsCfg
	t.Cleanup(func() { c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := sel.Current(); got != TransportWSS {
		t.Fatalf("transport = %s, want wss", got)
	}
	if wsAccepted.Load() != 1 {
		t.Fatalf("wss broker saw %d sessions, want 1", wsAccepted.Load())
	}
	if st := sel.Status(); !st.Connected || st.Switches != 1 {
		t.Fatalf("status = %+v", st)
	}
	if err := c.Publish(ctx, "t", 0, []byte("x")); err != nil {
		t.Fatalf("publish over wss: %v", err)
	}

	// A new WSS URL while connected must not touch the session.
	sel.SetWSURL("wss://elsewhere.example/mqtt")
	time.Sleep(150 * time.Millisecond)
	if wsAccepted.Load() != 1 || sel.Current() != TransportWSS || !sel.Status().Connected || sel.Status().Switches != 1 {
		t.Fatalf("url change moved the connection: sessions=%d status=%+v", wsAccepted.Load(), sel.Status())
	}
	sel.SetWSURL("wss://" + strings.TrimPrefix(srv.URL, "https://") + "/mqtt")

	// TCP still dead: a due probe fails and the client stays on WSS.
	sel.mu.Lock()
	sel.probeEvery = 0
	sel.mu.Unlock()
	time.Sleep(200 * time.Millisecond)
	if sel.Current() != TransportWSS {
		t.Fatal("must stay on wss while the TCP probe fails")
	}

	// TCP comes back: the next probe moves the client back and it reconnects over TCP.
	_, tcpAccepted := startTCPBroker(t, tcpAddr)
	waitFor(t, "switch back to tcp", func() bool { return tcpAccepted.Load() == 1 && sel.Status().Connected })
	if got := sel.Current(); got != TransportTCP {
		t.Fatalf("transport = %s, want tcp", got)
	}
	if st := sel.Status(); st.Switches != 2 {
		t.Fatalf("status = %+v", st)
	}
	if err := c.Publish(ctx, "t", 0, []byte("x")); err != nil {
		t.Fatalf("publish over tcp: %v", err)
	}
}

// TestNoFallbackWithoutWSURL checks the default path is untouched: with no wss
// URL the client keeps retrying TCP and never changes transport.
func TestNoFallbackWithoutWSURL(t *testing.T) {
	tcpAddr := freeAddr(t)
	host, port, _ := net.SplitHostPort(tcpAddr)
	var portNum int
	for _, ch := range port {
		portNum = portNum*10 + int(ch-'0')
	}
	f, _ := ProvideFactory(Config{Endpoint: host, Port: portNum})
	f.sel.policy = Policy{MinFailures: 1, ProbeInterval: time.Hour}
	c := f.GetFallbackClient("x")
	c.backoff = fastBackoff
	c.opts.ConnectTimeout = time.Second
	t.Cleanup(func() { c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if err := c.Connect(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connect err = %v, want deadline exceeded", err)
	}
	if f.Status().Transport != TransportTCP || f.Status().Switches != 0 {
		t.Fatalf("status = %+v", f.Status())
	}
}

// A one-shot client shares the selector but must not change its state: a
// publisher connecting must not reset the long-lived client's failure count or
// mark it connected.
func TestOneShotClientLeavesSelectorAlone(t *testing.T) {
	addr := freeAddr(t)
	host, port, _ := net.SplitHostPort(addr)
	var portNum int
	for _, ch := range port {
		portNum = portNum*10 + int(ch-'0')
	}
	_, accepted := startTCPBroker(t, addr)
	f, _ := ProvideFactory(Config{Endpoint: host, Port: portNum, WSURL: "wss://ws.example/mqtt"})
	f.sel.Failure()
	f.sel.Failure() // two failures of the long-lived client so far

	c := f.GetClient("one-shot")
	t.Cleanup(func() { c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if accepted.Load() != 1 {
		t.Fatalf("broker saw %d sessions", accepted.Load())
	}
	if st := f.Status(); st.Connected || st.Connects != 0 {
		t.Fatalf("one-shot client changed the shared state: %+v", st)
	}
	// The third failure still switches: the count was not reset.
	if !f.sel.Failure() {
		t.Fatal("the failure count was reset by the one-shot client")
	}
}
