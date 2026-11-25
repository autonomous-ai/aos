package server

import (
	"testing"

	"go.autonomous.ai/os/system/server/config"
)

// The WSS fallback URL is not part of the signature: a new URL must not
// restart a working MQTT connection. Real broker changes still do.
func TestMQTTConfigSignatureIgnoresWSURL(t *testing.T) {
	t.Setenv("MQTT_TLS", "")
	mk := func(endpoint, ws string) *config.Config {
		return &config.Config{MQTTEndpoint: endpoint, MQTTPort: 1883, MQTTUsername: "u", FAChannel: "fa", MQTTWSURL: ws}
	}
	base := mqttConfigSignature(mk("b.example", ""))
	for _, ws := range []string{"wss://b.example/mqtt", "wss://c.example/mqtt"} {
		if mqttConfigSignature(mk("b.example", ws)) != base {
			t.Fatalf("ws url %q changed the signature", ws)
		}
	}
	if mqttConfigSignature(mk("other.example", "")) == base {
		t.Fatal("a different broker must change the signature")
	}
}
