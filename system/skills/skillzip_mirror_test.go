package skills

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"go.autonomous.ai/os/system/lib/otamirror"
)

func TestSkillDownloadsFallBackFromCDNToStorage(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusBadGateway)
	}))
	defer cdn.Close()
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bucket/os/ota/metadata.json":
			_, _ = w.Write([]byte(`{"skills":{"voice":{"version":"abc"}}}`))
		case "/bucket/os/skills/voice.zip":
			_, _ = w.Write([]byte("zip"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer storage.Close()
	old := otamirror.Default
	otamirror.Default = otamirror.Mapping{CDN: cdn.URL, Storage: storage.URL + "/bucket"}
	defer func() { otamirror.Default = old }()

	// The device stores the CDN URL; the storage host answers when the CDN fails.
	versions, err := FetchSkillVersions(cdn.URL + "/os/ota/metadata.json")
	if err != nil || versions["voice"] != "abc" {
		t.Fatalf("versions %v err %v", versions, err)
	}
	path, err := DownloadToTempFile(cdn.URL+"/os/skills/voice.zip", "skill-*.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if data, _ := os.ReadFile(path); string(data) != "zip" {
		t.Fatalf("data %q", data)
	}
}

func TestSkillVersionsSkipHTMLFromCDN(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>Sign in to the network</body></html>"))
	}))
	defer cdn.Close()
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"skills":{"voice":{"version":"abc"}}}`))
	}))
	defer storage.Close()
	old := otamirror.Default
	otamirror.Default = otamirror.Mapping{CDN: cdn.URL, Storage: storage.URL + "/bucket"}
	defer func() { otamirror.Default = old }()

	versions, err := FetchSkillVersions(cdn.URL + "/os/ota/metadata.json")
	if err != nil || versions["voice"] != "abc" {
		t.Fatalf("versions %v err %v", versions, err)
	}
}
