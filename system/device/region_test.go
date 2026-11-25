package device

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.autonomous.ai/os/system/server/config"
)

// regionTestService returns a Service whose config saves into a temp dir.
func regionTestService(t *testing.T, cfg *config.Config) *Service {
	t.Helper()
	t.Chdir(t.TempDir())
	return &Service{config: cfg}
}

func TestApplyPingRegionStoresAndKeeps(t *testing.T) {
	s := regionTestService(t, &config.Config{})
	var tzCalls []string
	setTZ := func(tz string) error { tzCalls = append(tzCalls, tz); return nil }

	// Absent field: nothing stored, nothing written.
	s.applyPingRegion("", setTZ)
	if s.config.Region != "" || len(tzCalls) != 0 {
		t.Fatalf("empty region changed state: region=%q tz=%v", s.config.Region, tzCalls)
	}
	if _, err := os.Stat(filepath.Join("config", "config.json")); err == nil {
		t.Fatal("config written for an absent region")
	}

	// Present: stored (normalised) and persisted.
	s.applyPingRegion("cn", setTZ)
	if s.config.Region != "CN" || !s.config.InChina() {
		t.Fatalf("region = %q, want CN", s.config.Region)
	}
	if _, err := os.Stat(filepath.Join("config", "config.json")); err != nil {
		t.Fatalf("config not saved: %v", err)
	}

	// Absent field after a stored value must not clear it.
	s.applyPingRegion("", setTZ)
	if s.config.Region != "CN" {
		t.Fatalf("absent field cleared region: %q", s.config.Region)
	}

	// Unchanged: no rewrite.
	if err := os.Remove(filepath.Join("config", "config.json")); err != nil {
		t.Fatal(err)
	}
	s.applyPingRegion("CN", setTZ)
	if _, err := os.Stat(filepath.Join("config", "config.json")); err == nil {
		t.Fatal("unchanged region rewrote config")
	}
	if len(tzCalls) != 1 || tzCalls[0] != "Asia/Shanghai" {
		t.Fatalf("timezone calls = %v, want one Asia/Shanghai", tzCalls)
	}
}

func TestApplyPingRegionNeverOverridesOwnerTimezone(t *testing.T) {
	s := regionTestService(t, &config.Config{Timezone: "Asia/Ho_Chi_Minh"})
	called := false
	s.applyPingRegion("CN", func(string) error { called = true; return nil })
	if called {
		t.Fatal("owner-chosen timezone was overridden")
	}
	if !s.config.InChina() {
		t.Fatal("region not stored")
	}
}

func TestApplyPingRegionOtherRegionLeavesTimezone(t *testing.T) {
	s := regionTestService(t, &config.Config{})
	called := false
	s.applyPingRegion("US", func(string) error { called = true; return nil })
	if called || s.config.InChina() || s.config.Region != "US" {
		t.Fatalf("US region: called=%v region=%q", called, s.config.Region)
	}
}

func TestApplyPingRegionTimezoneFailureKeepsRegion(t *testing.T) {
	s := regionTestService(t, &config.Config{})
	s.applyPingRegion("CN", func(string) error { return errors.New("no tzdata") })
	if !s.config.InChina() {
		t.Fatal("region lost after a timezone failure")
	}
}

func TestConfigInChina(t *testing.T) {
	cases := map[string]bool{"CN": true, "cn": true, " Cn ": true, "": false, "US": false, "HK": false}
	for region, want := range cases {
		if got := (&config.Config{Region: region}).InChina(); got != want {
			t.Errorf("InChina(%q) = %v, want %v", region, got, want)
		}
	}
	var nilCfg *config.Config
	if nilCfg.InChina() {
		t.Error("nil config must not be in China")
	}
}
