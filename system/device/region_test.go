package device

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.autonomous.ai/os/system/domain"
	"go.autonomous.ai/os/system/server/config"
)

// regionTestService returns a Service whose config saves into a temp dir.
func regionTestService(t *testing.T, cfg *config.Config) *Service {
	t.Helper()
	t.Chdir(t.TempDir())
	return &Service{config: cfg}
}

func TestApplyPingRegionStoresClearsAndWritesOnlyOnChange(t *testing.T) {
	s := regionTestService(t, &config.Config{})
	var tzCalls []string
	setTZ := func(tz string) error { tzCalls = append(tzCalls, tz); return nil }
	cfgFile := filepath.Join("config", "config.json")

	// Nothing stored and nothing sent: no write.
	s.applyPingRegion("", setTZ)
	if _, err := os.Stat(cfgFile); err == nil {
		t.Fatal("config written for an unchanged empty region")
	}

	// A region is stored (normalised) and persisted.
	s.applyPingRegion("cn", setTZ)
	if s.config.Region != "CN" || !s.config.InChina() {
		t.Fatalf("region = %q, want CN", s.config.Region)
	}
	if _, err := os.Stat(cfgFile); err != nil {
		t.Fatalf("config not saved: %v", err)
	}

	// Unchanged: no rewrite.
	if err := os.Remove(cfgFile); err != nil {
		t.Fatal(err)
	}
	s.applyPingRegion("CN", setTZ)
	if _, err := os.Stat(cfgFile); err == nil {
		t.Fatal("unchanged region rewrote config")
	}

	// The backend leaves the field out when the device has no region: clear it.
	s.applyPingRegion("", setTZ)
	if s.config.Region != "" || s.config.InChina() {
		t.Fatalf("absent region did not clear: %q", s.config.Region)
	}
	if _, err := os.Stat(cfgFile); err != nil {
		t.Fatal("clearing the region must be saved")
	}
	// A region that is not letters reads as none.
	s.applyPingRegion("CN", setTZ)
	s.applyPingRegion("zz-1", setTZ)
	if s.config.Region != "" {
		t.Fatalf("strange region stored: %q", s.config.Region)
	}

	// Asia/Shanghai was applied on each entry into China while no zone was set; leaving never undoes it.
	if len(tzCalls) != 2 || tzCalls[0] != "Asia/Shanghai" {
		t.Fatalf("timezone calls = %v", tzCalls)
	}
}

func TestApplyPingRegionNotifiesSubscribers(t *testing.T) {
	s := regionTestService(t, &config.Config{})
	var got []RegionChange
	s.OnRegionChange(func(ch RegionChange) { got = append(got, ch) })
	noTZ := func(string) error { return nil }

	s.applyPingRegion("", noTZ)   // unchanged
	s.applyPingRegion("CN", noTZ) // enter
	s.applyPingRegion("CN", noTZ) // unchanged
	s.applyPingRegion("US", noTZ) // leave China
	s.applyPingRegion("", noTZ)   // US -> none

	want := []RegionChange{
		{Region: "CN", Previous: "", InChina: true, WasInChina: false},
		{Region: "US", Previous: "CN", InChina: false, WasInChina: true},
		{Region: "", Previous: "US", InChina: false, WasInChina: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d notifications %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("notification %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Entering or leaving China restarts HAL and refreshes the agent text; other
// changes do neither.
func TestApplyRegionEffects(t *testing.T) {
	gw := &onboardCounter{}
	restarts := make(chan struct{}, 4)
	s := &Service{config: &config.Config{}, agentGateway: gw}
	s.halRestartCommand = func(context.Context) error { restarts <- struct{}{}; return nil }
	t.Chdir(t.TempDir())

	s.applyRegionEffects(RegionChange{Region: "US", InChina: false, WasInChina: false})
	select {
	case <-restarts:
		t.Fatal("a non-China change restarted HAL")
	case <-time.After(100 * time.Millisecond):
	}

	s.applyRegionEffects(RegionChange{Region: "CN", InChina: true, WasInChina: false})
	select {
	case <-restarts:
	case <-time.After(2 * time.Second):
		t.Fatal("entering China did not restart HAL")
	}
	deadline := time.Now().Add(2 * time.Second)
	for gw.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if gw.count() != 1 {
		t.Fatalf("onboarding refreshes = %d, want 1", gw.count())
	}
}

type onboardCounter struct {
	domain.AgentGateway
	mu sync.Mutex
	n  int
}

func (g *onboardCounter) EnsureOnboarding() error { g.mu.Lock(); g.n++; g.mu.Unlock(); return nil }
func (g *onboardCounter) count() int              { g.mu.Lock(); defer g.mu.Unlock(); return g.n }

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
