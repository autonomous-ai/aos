package openclaw

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"go.autonomous.ai/os/system/lib/otamirror"
)

func hookServers(t *testing.T, cdn, storage http.HandlerFunc) string {
	t.Helper()
	c := httptest.NewServer(cdn)
	s := httptest.NewServer(storage)
	t.Cleanup(c.Close)
	t.Cleanup(s.Close)
	old := otamirror.Default
	otamirror.Default = otamirror.Mapping{CDN: c.URL, Storage: s.URL + "/bucket"}
	t.Cleanup(func() { otamirror.Default = old })
	return c.URL
}

const hookHandler = "export default async function handler() {}\n"

func TestHookDownloadSkipsHTMLFromCDN(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"html content type": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("Sign in to the network"))
		},
		"html body": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("  <!DOCTYPE html><html></html>"))
		},
		"empty body": func(w http.ResponseWriter, r *http.Request) {},
		"error":      func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", http.StatusBadGateway) },
	}
	for name, cdn := range cases {
		t.Run(name, func(t *testing.T) {
			base := hookServers(t, cdn, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bucket/os/hooks/turn-gate/handler.ts" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "video/mp2t")
				_, _ = w.Write([]byte(hookHandler))
			})
			dst := filepath.Join(t.TempDir(), "handler.ts")
			if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			changed, err := downloadFile(base+"/os/hooks/turn-gate/handler.ts", dst)
			if err != nil || !changed {
				t.Fatalf("changed %v err %v", changed, err)
			}
			if data, _ := os.ReadFile(dst); string(data) != hookHandler {
				t.Fatalf("handler.ts = %q", data)
			}
		})
	}
}

func TestHookDownloadKeepsFileWhenNoHostServesIt(t *testing.T) {
	html := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html></html>"))
	}
	base := hookServers(t, html, html)
	dst := filepath.Join(t.TempDir(), "handler.ts")
	if err := os.WriteFile(dst, []byte(hookHandler), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := downloadFile(base+"/os/hooks/turn-gate/handler.ts", dst); err == nil {
		t.Fatal("expected an error")
	}
	if data, _ := os.ReadFile(dst); string(data) != hookHandler {
		t.Fatalf("handler.ts was overwritten: %q", data)
	}
}
