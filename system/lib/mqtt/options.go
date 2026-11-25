// Package mqtt provides an MQTT client with auto-connect and reconnect (Eclipse Paho autopaho).
package mqtt

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// DefaultKeepAlive is the default keepalive in seconds. A short value makes a
// dead broker link visible within about a minute, so the client reconnects (and
// can fall back to WSS) instead of sitting on a half-open socket.
const DefaultKeepAlive = 60

// DefaultConnectTimeout is the default time one connection attempt may take,
// from dialling to the broker's CONNACK.
const DefaultConnectTimeout = 10 * time.Second

// DefaultPort is the default MQTT port when Port is 0.
const DefaultPort = 1883

// Options configures the MQTT client. Endpoint is required to enable MQTT.
type Options struct {
	// Endpoint is the broker host (domain or IP only); empty disables MQTT.
	Endpoint string
	// Port is the broker port (e.g. 1883, 8883). 0 uses DefaultPort.
	Port int
	// ClientID defaults to a generated value when empty.
	ClientID string
	Username string
	Password string
	// KeepAlive is in seconds; 0 uses DefaultKeepAlive.
	KeepAlive uint16
	// ConnectTimeout bounds the initial connection; 0 uses DefaultConnectTimeout.
	ConnectTimeout time.Duration
	// TLS connects over TLS (scheme ssl) using the system root CAs with hostname
	// verification. When set, the client never falls back to a plain connection.
	TLS bool
	// WSURL is the optional wss:// URL used for the WSS transport; the client
	// reads the current value from its Selector, which can change while running.
	WSURL string
}

// ServerURL returns mqtt://host:port (ssl://host:port when TLS is set) for Paho.
func (o *Options) ServerURL() (*url.URL, error) {
	host := strings.TrimSpace(o.Endpoint)
	if host == "" {
		return nil, fmt.Errorf("mqtt: endpoint is required")
	}
	port := o.Port
	if port == 0 {
		port = DefaultPort
	}
	scheme := "mqtt"
	if o.TLS {
		scheme = "ssl"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", host, port),
	}
	return u, nil
}

// ServerURLFor returns the URL for a transport: the TCP URL for TransportTCP,
// the configured wss:// URL for TransportWSS.
func (o *Options) ServerURLFor(t Transport) (*url.URL, error) {
	if t != TransportWSS {
		return o.ServerURL()
	}
	raw, ok := NormalizeWSURL(o.WSURL)
	if !ok {
		return nil, fmt.Errorf("mqtt: no usable wss url")
	}
	return url.Parse(raw)
}

// TLSConfigFor returns the TLS settings for a transport. WSS always uses TLS
// (system root CAs, TLS 1.2 minimum) and verifies the WSS URL's host name.
func (o *Options) TLSConfigFor(t Transport) *tls.Config {
	if t != TransportWSS {
		return o.TLSConfig()
	}
	raw, _ := NormalizeWSURL(o.WSURL)
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
}

// TLSConfig returns the TLS settings for a TLS connection, or nil for a plain one.
// Certificates are checked against the system root CAs and the broker host name,
// and TLS 1.2 is the minimum version.
func (o *Options) TLSConfig() *tls.Config {
	if !o.TLS {
		return nil
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: strings.TrimSpace(o.Endpoint),
	}
}
