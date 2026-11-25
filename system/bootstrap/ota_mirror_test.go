package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go.autonomous.ai/os/system/bootstrap/config"
	"go.autonomous.ai/os/system/lib/otamirror"
)

// mirrorServers starts a fake CDN and a fake bucket host and points otaMirror at them.
func mirrorServers(t *testing.T, cdn, storage http.HandlerFunc) (cdnURL, storageURL string) {
	t.Helper()
	c := httptest.NewServer(cdn)
	s := httptest.NewServer(storage)
	t.Cleanup(c.Close)
	t.Cleanup(s.Close)
	old := otaMirror
	otaMirror = otamirror.Mapping{CDN: c.URL, Storage: s.URL + "/bucket"}
	t.Cleanup(func() { otaMirror = old })
	return c.URL, s.URL + "/bucket"
}

func TestMetadataFetchPrefersCDN(t *testing.T) {
	var storageHits atomic.Int32
	_, storage := mirrorServers(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/os/ota/metadata.json" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"from":"cdn"}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			storageHits.Add(1)
			_, _ = w.Write([]byte(`{"from":"storage"}`))
		})
	b := &Bootstrap{cfg: &config.Config{MetadataURL: storage + "/os/ota/metadata.json"}, client: http.DefaultClient}
	payload, _, err := b.fetchMetadataPayload(context.Background())
	if err != nil || string(payload) != `{"from":"cdn"}` {
		t.Fatalf("payload %s err %v", payload, err)
	}
	if storageHits.Load() != 0 {
		t.Fatalf("bucket host was contacted although the CDN answered")
	}
}

func TestMetadataFetchFallsBackToBucket(t *testing.T) {
	cdn, _ := mirrorServers(t,
		func(w http.ResponseWriter, r *http.Request) { http.Error(w, "blocked", http.StatusBadGateway) },
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/bucket/os/ota/metadata.json" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"from":"storage"}`))
		})
	// A device whose stored URL is already on the CDN still falls back.
	b := &Bootstrap{cfg: &config.Config{MetadataURL: cdn + "/os/ota/metadata.json"}, client: http.DefaultClient}
	payload, _, err := b.fetchMetadataPayload(context.Background())
	if err != nil || string(payload) != `{"from":"storage"}` {
		t.Fatalf("payload %s err %v", payload, err)
	}
}

func TestMetadataFetchReportsEveryHost(t *testing.T) {
	_, storage := mirrorServers(t,
		func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", http.StatusNotFound) },
		func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", http.StatusForbidden) })
	b := &Bootstrap{cfg: &config.Config{MetadataURL: storage + "/os/ota/metadata.json"}, client: http.DefaultClient}
	_, _, err := b.fetchMetadataPayload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdaterFetchFallsBackOnEmptyCDNBody(t *testing.T) {
	_, storage := mirrorServers(t,
		func(w http.ResponseWriter, r *http.Request) {}, // 200, empty body
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Cache-Control") != "no-cache" {
				t.Errorf("updater fetch must bypass caches")
			}
			_, _ = w.Write([]byte("#!/bin/bash\n"))
		})
	b := &Bootstrap{cfg: &config.Config{}, client: http.DefaultClient}
	data, err := b.fetchUpdater(context.Background(), storage+"/os/software-update")
	if err != nil || string(data) != "#!/bin/bash\n" {
		t.Fatalf("data %q err %v", data, err)
	}
}
