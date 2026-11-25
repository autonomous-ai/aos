package mqtt

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/autopaho/queue/memory"
	"github.com/eclipse/paho.golang/paho"
)

// ErrNotConnected is returned by Publish when the client is not connected.
var ErrNotConnected = errors.New("mqtt: not connected")

// MessageHandler is called when a message is received on a subscribed topic.
type MessageHandler func(topic string, payload []byte)

// MQTT is an MQTT client with auto-connect and reconnect.
type MQTT struct {
	opts       Options
	conn       *autopaho.ConnectionManager
	connCtx    context.Context
	connCancel context.CancelFunc
	mu         sync.Mutex

	// sel holds the transport state (shared through the Factory); nil means TCP only.
	sel *Selector
	// fallback makes this client drive sel: switch to WSS when TCP keeps failing
	// and back when it recovers. Without it the client uses sel.Current() as is.
	fallback bool
	// switchCh wakes the supervisor when sel asked for another transport.
	switchCh chan struct{}
	// connCancelCur stops the current connection manager only (connCancel stops the client).
	connCancelCur context.CancelFunc
	gen           int // bumped on each (re)start so stale callbacks are ignored
	transport     Transport
	done          chan struct{}
	// Test hooks; zero values mean production behaviour.
	probeTCP    func(ctx context.Context) error // checks the TCP endpoint accepts connections
	backoff     autopaho.Backoff                // reconnect backoff
	tlsOverride *tls.Config                     // replaces the TLS settings of every transport

	// subscriptions are re-applied on each connection up
	subscriptions map[string]subEntry
	handlers      map[string]MessageHandler
}

type subEntry struct {
	topic string
	qos   byte
}

// supervisePoll is how often a client on WSS checks whether a TCP probe is due.
var supervisePoll = time.Minute

// ProvideClient creates an MQTT client from config; Endpoint is required.
func ProvideClient(opts Options) *MQTT {
	if opts.KeepAlive == 0 {
		opts.KeepAlive = DefaultKeepAlive
	}
	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = DefaultConnectTimeout
	}
	if opts.ClientID == "" {
		opts.ClientID = defaultClientID()
	}
	return &MQTT{
		opts:          opts,
		subscriptions: make(map[string]subEntry),
		handlers:      make(map[string]MessageHandler),
	}
}

func defaultClientID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "os-mqtt-0"
	}
	return "os-mqtt-" + hex.EncodeToString(b)
}

// Connect starts auto-reconnect and returns once the initial handshake completes or ctx expires.
func (c *MQTT) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.conn != nil {
		c.mu.Unlock()
		return nil // already connected
	}
	// Connection lifetime is decoupled from ctx: ctx only bounds the handshake wait,
	// since callers often defer cancel() yet reuse the client. Only Close (or a failed
	// handshake below) cancels connCtx.
	c.connCtx, c.connCancel = context.WithCancel(context.Background())
	connCtx := c.connCtx
	connCancel := c.connCancel
	if c.sel == nil {
		c.sel = NewSelector(DefaultPolicy, "", nil)
	}
	c.switchCh = make(chan struct{}, 1)
	c.done = make(chan struct{})
	done := c.done
	c.mu.Unlock()
	go func() {
		<-connCtx.Done()
		close(done)
	}()

	up := make(chan struct{})
	var upOnce sync.Once
	if err := c.start(connCtx, c.sel.Current(), func() { upOnce.Do(func() { close(up) }) }); err != nil {
		connCancel()
		return err
	}
	if c.fallback {
		go c.supervise(connCtx, func() { upOnce.Do(func() { close(up) }) })
	}

	// ctx bounds only this handshake wait; connCtx is unaffected.
	select {
	case <-up:
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		connCancel()
		return ctx.Err()
	}
}

// start opens a connection manager on transport t. onUp runs on every successful
// connection (it is how Connect learns the first handshake finished).
func (c *MQTT) start(parent context.Context, t Transport, onUp func()) error {
	opts := c.opts
	if u := c.sel.WSURL(); u != "" {
		opts.WSURL = u // the current URL, which may have changed since the client was made
	}
	serverURL, err := opts.ServerURLFor(t)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.gen++
	gen := c.gen
	c.mu.Unlock()
	current := func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.gen == gen
	}

	cfg := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{serverURL},
		TlsCfg:                        c.tlsFor(t),
		KeepAlive:                     c.opts.KeepAlive,
		CleanStartOnInitialConnection: true,
		SessionExpiryInterval:         60,
		ConnectTimeout:                c.opts.ConnectTimeout,
		ReconnectBackoff:              c.reconnectBackoff(),
		Queue:                         memory.New(),
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			if !current() {
				return
			}
			c.sel.Up()
			c.mu.Lock()
			subs := c.copySubscriptions()
			c.mu.Unlock()
			st := c.sel.Status()
			slog.Info("mqtt connected", "component", "mqtt", "broker", c.opts.Endpoint, "transport", string(t),
				"subs", len(subs), "connects", st.Connects, "switches", st.Switches)
			for _, s := range subs {
				if _, err := cm.Subscribe(context.Background(), &paho.Subscribe{
					Subscriptions: []paho.SubscribeOptions{{Topic: s.topic, QoS: s.qos}},
				}); err != nil {
					slog.Error("subscribe failed", "component", "mqtt", "topic", s.topic, "error", err)
				} else {
					slog.Info("subscribed ok", "component", "mqtt", "topic", s.topic)
				}
			}
			if onUp != nil {
				onUp()
			}
		},
		OnConnectionDown: func() bool { // keep reconnecting
			if c.fallback && current() {
				c.sel.Down()
			}
			return true
		},
		OnConnectError: func(err error) {
			slog.Error("connect error", "component", "mqtt", "transport", string(t), "error", err)
			// A broker that answers CONNACK with a refusal is reachable: another
			// transport would not help, so only connects that never got there count.
			var refused *autopaho.ConnackError
			if errors.As(err, &refused) {
				return
			}
			if c.fallback && current() && c.sel.Failure() {
				select {
				case c.switchCh <- struct{}{}:
				default:
				}
			}
		},
		ClientConfig: paho.ClientConfig{
			ClientID: c.opts.ClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					c.mu.Lock()
					fn := c.handlers[pr.Packet.Topic]
					c.mu.Unlock()
					if fn != nil {
						fn(pr.Packet.Topic, pr.Packet.Payload)
					}
					return true, nil
				},
			},
		},
	}
	if c.opts.Username != "" {
		cfg.ConnectUsername = c.opts.Username
		cfg.ConnectPassword = []byte(c.opts.Password)
	}

	connCtx, cancel := context.WithCancel(parent)
	conn, err := autopaho.NewConnection(connCtx, cfg)
	if err != nil {
		cancel()
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.connCancelCur = cancel
	c.transport = t
	c.mu.Unlock()
	return nil
}

func (c *MQTT) tlsFor(t Transport) *tls.Config {
	if c.tlsOverride != nil {
		return c.tlsOverride
	}
	opts := c.opts
	if u := c.sel.WSURL(); u != "" {
		opts.WSURL = u
	}
	return opts.TLSConfigFor(t)
}

func (c *MQTT) reconnectBackoff() autopaho.Backoff {
	if c.backoff != nil {
		return c.backoff
	}
	return autopaho.DefaultExponentialBackoff()
}

// supervise moves the connection between transports as the Selector decides:
// to WSS when TCP keeps failing, back to TCP when a probe shows it works again.
// It runs until the client is closed.
func (c *MQTT) supervise(parent context.Context, onUp func()) {
	tick := time.NewTicker(supervisePoll)
	defer tick.Stop()
	for {
		select {
		case <-parent.Done():
			return
		case <-c.switchCh:
		case <-tick.C:
			if !c.sel.ProbeDue() {
				continue
			}
			probe := c.probeTCP
			if probe == nil {
				probe = c.dialTCP
			}
			pctx, cancel := context.WithTimeout(parent, 10*time.Second)
			err := probe(pctx)
			cancel()
			if parent.Err() != nil {
				return
			}
			if !c.sel.ProbeDone(err == nil) {
				slog.Info("mqtt tcp probe failed, staying on wss", "component", "mqtt")
				continue
			}
		}
		t := c.sel.Current()
		c.mu.Lock()
		same := c.transport == t
		c.mu.Unlock()
		if same {
			continue
		}
		slog.Warn("mqtt switching transport", "component", "mqtt", "to", string(t), "broker", c.opts.Endpoint)
		c.stopCurrent()
		if parent.Err() != nil {
			return
		}
		if err := c.start(parent, t, onUp); err != nil {
			slog.Error("mqtt transport switch failed", "component", "mqtt", "transport", string(t), "error", err)
		}
	}
}

// stopCurrent shuts down the current connection manager and waits for it, so two
// sessions never share one client ID.
func (c *MQTT) stopCurrent() {
	c.mu.Lock()
	conn := c.conn
	cancel := c.connCancelCur
	c.gen++ // callbacks of the old manager are stale from here on
	c.conn = nil
	c.connCancelCur = nil
	c.mu.Unlock()
	if conn == nil {
		return
	}
	ctx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	_ = conn.Disconnect(ctx) // cancels the manager and waits for it to exit
	if cancel != nil {
		cancel()
	}
}

// dialTCP checks that the broker's TCP endpoint accepts a connection (and the TLS
// handshake when TLS is on). It does not log in or disturb the live WSS session.
func (c *MQTT) dialTCP(ctx context.Context) error {
	u, err := c.opts.ServerURL()
	if err != nil {
		return err
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return err
	}
	defer conn.Close()
	if cfg := c.opts.TLSConfig(); cfg != nil {
		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (c *MQTT) copySubscriptions() []subEntry {
	var out []subEntry
	for _, s := range c.subscriptions {
		out = append(out, s)
	}
	return out
}

// Close disconnects and stops the client.
func (c *MQTT) Close() error {
	c.mu.Lock()
	conn := c.conn
	cancel := c.connCancel
	c.conn = nil
	c.connCancel = nil
	c.connCancelCur = nil
	c.gen++
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Disconnect(ctx)
	}
	return nil
}

// Subscribe registers a topic handler; subscriptions are re-applied on reconnect.
func (c *MQTT) Subscribe(topic string, qos byte, handler MessageHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscriptions[topic] = subEntry{topic: topic, qos: qos}
	if handler != nil {
		c.handlers[topic] = handler
	}
}

// Publish sends a message and returns when sent or ctx is cancelled.
func (c *MQTT) Publish(ctx context.Context, topic string, qos byte, payload []byte) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	_, err := conn.Publish(ctx, &paho.Publish{
		Topic:   topic,
		QoS:     qos,
		Payload: payload,
	})
	return err
}

// Done returns a channel that is closed when the client has been stopped.
func (c *MQTT) Done() <-chan struct{} {
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return done
}
