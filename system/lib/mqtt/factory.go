package mqtt

// Factory creates MQTT clients sharing one config; each gets a unique client ID.
// The clients share one transport Selector, so a short-lived client uses WSS
// while the long-lived one has fallen back to it.
type Factory struct {
	config Config
	sel    *Selector
}

// ProvideFactory creates a Factory from config.
func ProvideFactory(cfg Config) (*Factory, error) {
	return &Factory{config: cfg, sel: NewSelector(DefaultPolicy, cfg.WSURL, nil)}, nil
}

// UpdateConfig refreshes the connection config; call before restartMQTT to pick up new credentials.
// The transport state restarts on TCP.
func (f *Factory) UpdateConfig(cfg Config) {
	f.config = cfg
	f.sel.SetWSURL(cfg.WSURL)
	f.sel.Reset()
}

// SetWSURL updates the wss fallback URL without touching any live connection:
// the new URL is used the next time a client falls back to WSS.
func (f *Factory) SetWSURL(raw string) {
	f.sel.SetWSURL(raw)
}

// Status reports the current transport and connect/switch counts.
func (f *Factory) Status() Status {
	return f.sel.Status()
}

func (f *Factory) options(clientID string) Options {
	return Options{
		Endpoint: f.config.Endpoint,
		Port:     f.config.Port,
		Username: f.config.Username,
		Password: f.config.Password,
		ClientID: clientID,
		TLS:      f.config.TLS,
		WSURL:    f.sel.WSURL(),
	}
}

// GetClient returns a new client with a unique ID; call Connect, Publish, then Close.
// It connects with whichever transport is current and never changes it.
func (f *Factory) GetClient(clientID string) *MQTT {
	c := ProvideClient(f.options(clientID))
	c.sel = f.sel
	return c
}

// GetFallbackClient is GetClient for the long-lived client: it drives the
// transport state, moving to WSS when TCP keeps failing and back to TCP when it
// recovers.
func (f *Factory) GetFallbackClient(clientID string) *MQTT {
	c := f.GetClient(clientID)
	c.fallback = true
	return c
}
