package beclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.autonomous.ai/os/system/server/config"
)

func TestPingPayloadIncludesWakeWordState(t *testing.T) {
	for _, want := range []bool{true, false} {
		payload, err := json.Marshal(PingPayload{WakeWordEnabled: want})
		if err != nil {
			t.Fatalf("marshal ping payload: %v", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatalf("unmarshal ping payload: %v", err)
		}

		raw, ok := fields["wakeword_enabled"]
		if !ok {
			t.Fatal("wakeword_enabled must be present even when disabled")
		}
		var got bool
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal wakeword_enabled: %v", err)
		}
		if got != want {
			t.Fatalf("wakeword_enabled = %t, want %t", got, want)
		}
	}
}

func TestPingUsesBackendOverride(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := &config.Config{
		LLMBaseURL:     "http://127.0.0.1:1/v1", // local model server, must not be hit
		LLMAPIKey:      "ollama",
		BackendBaseURL: srv.URL + "/api/v1/ai/v1",
		BackendAPIKey:  "backend-key",
		MQTTEndpoint:   "mqtt.example",
	}
	if _, err := New(cfg).Ping(cfg.BackendKey(), PingPayload{}); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if gotPath != "/api/v1/ai/ping" {
		t.Fatalf("path = %q, want /api/v1/ai/ping", gotPath)
	}
	if gotAuth != "Bearer backend-key" {
		t.Fatalf("auth = %q, want backend key", gotAuth)
	}
}

func TestBackendFallsBackToLLM(t *testing.T) {
	cfg := &config.Config{LLMBaseURL: "https://x/v1", LLMAPIKey: "k"}
	if cfg.BackendBase() != "https://x/v1" || cfg.BackendKey() != "k" {
		t.Fatalf("empty backend fields must reuse llm_base_url / llm_api_key")
	}
}

func TestPingIncludesVoiceInputMode(t *testing.T) {
	for _, mode := range []string{"automatic", "tap_to_talk"} {
		data, err := json.Marshal(PingPayload{VoiceInputMode: mode})
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["voice_input_mode"] != mode {
			t.Fatalf("mode=%v", fields["voice_input_mode"])
		}
	}
}

func TestPingBodyMQTTTLS(t *testing.T) {
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := &config.Config{
		BackendBaseURL: srv.URL + "/api/v1/ai/v1",
		BackendAPIKey:  "k",
		MQTTEndpoint:   "mqtt.example",
	}
	c := New(cfg)

	if _, err := c.Ping(cfg.BackendKey(), PingPayload{Status: "online"}); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, ok := body["mqtt_tls"]; ok {
		t.Fatalf("mqtt_tls must be absent when the switch is off: %v", body)
	}
	if string(body["status"]) != `"online"` {
		t.Fatalf("other fields must be unchanged: %v", body)
	}

	if _, err := c.Ping(cfg.BackendKey(), PingPayload{Status: "online", MQTTTLS: true}); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if string(body["mqtt_tls"]) != "true" {
		t.Fatalf("mqtt_tls = %s, want true", body["mqtt_tls"])
	}
}

func TestPingResponseRegion(t *testing.T) {
	cases := map[string]string{
		`{"status":"ok","region":"CN"}`:        "CN",
		`{"status":"ok","region":"cn"}`:        "CN",
		`{"status":"ok"}`:                      "",
		`{"status":"ok","region":""}`:          "",
		`{"status":"ok","region":null}`:        "",
		`{"status":"ok","region":5}`:           "",
		`{"status":"ok","region":["CN"]}`:      "",
		`{"status":"ok","region":"zz-1"}`:      "",
		`{"status":"ok","region":"C N"}`:       "",
		`{"status":"ok","region":"../x"}`:      "",
		`{"status":"ok","region":"ABCDEFGHI"}`: "",
	}
	for body, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		cfg := &config.Config{BackendBaseURL: srv.URL + "/api/v1/ai/v1", BackendAPIKey: "k"}
		resp, err := New(cfg).Ping(cfg.BackendKey(), PingPayload{})
		srv.Close()
		if err != nil || resp == nil {
			t.Fatalf("%s: ping resp=%v err=%v", body, resp, err)
		}
		if resp.Region != want {
			t.Errorf("%s: region = %q, want %q", body, resp.Region, want)
		}
	}
}

// A strange region must not lose the rest of the reply.
func TestPingResponseStrangeRegionKeepsReply(t *testing.T) {
	for _, strange := range []string{`5`, `null`, `["CN"]`, `"zz-1"`, `{}`} {
		var resp PingResponse
		body := `{"status":"ok","device_id":"d-1","mqtt":{"mqtt_server":"b.example"},"region":` + strange + `}`
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("%s: %v", strange, err)
		}
		if resp.Region != "" || resp.DeviceID != "d-1" || !resp.HasMQTT() {
			t.Errorf("%s: region=%q device=%q mqtt=%v", strange, resp.Region, resp.DeviceID, resp.HasMQTT())
		}
	}
}
