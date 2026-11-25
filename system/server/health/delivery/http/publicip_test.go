package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func resetPublicIPCache(t *testing.T, url string) {
	t.Helper()
	oldURL := publicIPURL
	publicIPURL = url
	publicIPCache.ip = ""
	publicIPCache.fetchedAt = time.Time{}
	t.Cleanup(func() {
		publicIPURL = oldURL
		publicIPCache.ip = ""
		publicIPCache.fetchedAt = time.Time{}
	})
}

func TestGetPublicIPTimesOutAndRemembersFailure(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	resetPublicIPCache(t, srv.URL)

	start := time.Now()
	if ip := getPublicIP(); ip != "" {
		t.Fatalf("want empty IP on timeout, got %q", ip)
	}
	if elapsed := time.Since(start); elapsed > publicIPTimeout+time.Second {
		t.Fatalf("lookup took %v, want about %v", elapsed, publicIPTimeout)
	}
	start = time.Now()
	if ip := getPublicIP(); ip != "" {
		t.Fatalf("want empty IP from failure cache, got %q", ip)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("cached failure took %v, want immediate", elapsed)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("lookup calls = %d, want 1", got)
	}
}

func TestGetPublicIPCachesSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("203.0.113.7\n"))
	}))
	defer srv.Close()
	resetPublicIPCache(t, srv.URL)

	for i := 0; i < 2; i++ {
		if ip := getPublicIP(); ip != "203.0.113.7" {
			t.Fatalf("call %d: got %q", i, ip)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("lookup calls = %d, want 1", got)
	}
}
