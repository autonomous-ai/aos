package server

import (
	"testing"

	"go.autonomous.ai/os/system/server/config"
)

// A new WSS fallback URL must restart the MQTT client; the same URL must not.
func TestMQTTConfigSignatureTracksWSURL(t *testing.T) {
	t.Setenv("MQTT_TLS", "")
	mk := func(ws string) *config.Config {
		return &config.Config{MQTTEndpoint: "b.example", MQTTPort: 1883, MQTTUsername: "u", FAChannel: "fa", MQTTWSURL: ws}
	}
	sigNone := mqttConfigSignature(mk(""))
	sigWS := mqttConfigSignature(mk("wss://b.example/mqtt"))
	if sigWS == sigNone {
		t.Fatal("adding a ws url must change the signature")
	}
	if mqttConfigSignature(mk("wss://b.example/mqtt")) != sigWS {
		t.Fatal("same config must give the same signature")
	}
	if mqttConfigSignature(mk("wss://c.example/mqtt")) == sigWS {
		t.Fatal("a different ws url must change the signature")
	}
	if mqttConfigSignature(mk("")) != sigNone {
		t.Fatal("clearing the ws url must return to the original signature")
	}
}
