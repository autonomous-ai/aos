package domain

import "testing"

func TestProvidersForRegion(t *testing.T) {
	if got := ProvidersForRegion(false); len(got) != len(ListProviders) {
		t.Fatalf("outside China: got %d providers, want %d", len(got), len(ListProviders))
	}
	cn := ProvidersForRegion(true)
	if len(cn) != len(ListProviders)-4 {
		t.Fatalf("China: got %d providers, want %d", len(cn), len(ListProviders)-4)
	}
	for _, p := range cn {
		switch p.Key {
		case "google", "google-vertex", "google-antigravity", "google-gemini-cli":
			t.Errorf("China list still has %s", p.Key)
		}
	}
}
