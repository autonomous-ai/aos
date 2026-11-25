package config

import "testing"

func TestRealtimeEffective(t *testing.T) {
	off := false
	on := true
	cases := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{"default outside China", &Config{}, true},
		{"default in China (gemini)", &Config{Region: "CN"}, false},
		{"explicit gemini in China", &Config{Region: "cn", Realtime: &RealtimeConfig{Enabled: &on, Provider: "gemini"}}, false},
		{"other provider in China", &Config{Region: "CN", Realtime: &RealtimeConfig{Enabled: &on, Provider: "openai"}}, true},
		{"disabled outside China", &Config{Realtime: &RealtimeConfig{Enabled: &off}}, false},
		{"other region keeps gemini", &Config{Region: "US"}, true},
	}
	for _, tc := range cases {
		if got := tc.cfg.RealtimeEffective(); got != tc.want {
			t.Errorf("%s: RealtimeEffective = %v, want %v", tc.name, got, tc.want)
		}
	}
	// The saved setting itself is never changed by region.
	if !(&Config{Region: "CN"}).RealtimeEnabled() {
		t.Error("RealtimeEnabled must stay the saved setting in China")
	}
}
