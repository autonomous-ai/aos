package device

import (
	"errors"
	"testing"

	"go.autonomous.ai/os/system/beclient"
	"go.autonomous.ai/os/system/domain"
	"go.autonomous.ai/os/system/server/config"
)

// skillLister is a minimal AgentGateway stand-in: only ListSkills is exercised
// by the ping path, so the rest of the interface is embedded unimplemented.
type skillLister struct {
	domain.AgentGateway
	list []domain.InstalledSkill
	err  error
}

func (g *skillLister) ListSkills() ([]domain.InstalledSkill, error) { return g.list, g.err }

func TestInstalledSkillsForPing(t *testing.T) {
	s := &Service{agentGateway: &skillLister{list: []domain.InstalledSkill{
		{
			Name:        "music",
			Description: "Play music.",
			// Files must NOT ride the ping — it fires every 15s.
			Files: []domain.SkillNode{{Name: "SKILL.md", Path: "music/SKILL.md"}},
		},
		{Name: "voice"},
	}}}

	got := s.installedSkillsForPing()
	if len(got) != 2 {
		t.Fatalf("want 2 skills, got %d: %+v", len(got), got)
	}
	if got[0].Name != "music" || got[0].Description != "Play music." {
		t.Errorf("got[0] = %+v", got[0])
	}
	// A skill with no description reports the name alone (description omitempty).
	if got[1].Name != "voice" || got[1].Description != "" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

// A skill listing failure must not break the ping; the field is omitted.
func TestInstalledSkillsForPingIsBestEffort(t *testing.T) {
	cases := []struct {
		label string
		gw    domain.AgentGateway
	}{
		{"unsupported runtime", &skillLister{err: domain.ErrNotSupportedByRuntime}},
		{"filesystem error", &skillLister{err: errors.New("permission denied")}},
		{"empty list", &skillLister{list: []domain.InstalledSkill{}}},
		{"nil gateway", nil},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			s := &Service{agentGateway: tc.gw}
			if got := s.installedSkillsForPing(); got != nil {
				t.Errorf("want nil (field omitted), got %+v", got)
			}
		})
	}
}

func TestApplyMQTTWSURL(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		name        string
		stored      string
		resp        *beclient.PingResponse
		wantURL     string
		wantChanged bool
	}{
		{"absent keeps stored", "wss://a.example/mqtt", &beclient.PingResponse{}, "wss://a.example/mqtt", false},
		{"nil response", "wss://a.example/mqtt", nil, "wss://a.example/mqtt", false},
		{"set from empty", "", &beclient.PingResponse{MQTTWSURL: str("wss://a.example/mqtt")}, "wss://a.example/mqtt", true},
		{"same value", "wss://a.example/mqtt", &beclient.PingResponse{MQTTWSURL: str(" wss://a.example/mqtt ")}, "wss://a.example/mqtt", false},
		{"new value", "wss://a.example/mqtt", &beclient.PingResponse{MQTTWSURL: str("wss://b.example/mqtt")}, "wss://b.example/mqtt", true},
		{"empty clears", "wss://a.example/mqtt", &beclient.PingResponse{MQTTWSURL: str("")}, "", true},
		{"empty on empty", "", &beclient.PingResponse{MQTTWSURL: str("")}, "", false},
		{"plain ws ignored", "wss://a.example/mqtt", &beclient.PingResponse{MQTTWSURL: str("ws://b.example/mqtt")}, "wss://a.example/mqtt", false},
		{"garbage ignored", "", &beclient.PingResponse{MQTTWSURL: str("not a url")}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &config.Config{MQTTWSURL: tt.stored}
			if got := applyMQTTWSURL(tt.resp, c); got != tt.wantChanged {
				t.Fatalf("changed = %t, want %t", got, tt.wantChanged)
			}
			if c.MQTTWSURL != tt.wantURL {
				t.Fatalf("stored = %q, want %q", c.MQTTWSURL, tt.wantURL)
			}
		})
	}
}
