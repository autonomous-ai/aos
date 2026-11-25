package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.autonomous.ai/os/system/lib/otamirror"
)

var bucketMetadataURL = otamirror.Default.Storage + "/os/ota/metadata.json"

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bootstrap.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := configPath
	configPath = path
	t.Cleanup(func() { configPath = old })
	return path
}

func TestLoadMovesBucketMetadataURLToCDNOnce(t *testing.T) {
	t.Setenv(MetadataURLEnv, "")
	path := writeConfig(t, `{"httpPort":8080,"metadata_url":"`+bucketMetadataURL+`","signing_public_key":"k","future_key":{"a":1}}`)

	cfg := LoadOrDefault()
	if cfg.MetadataURL != "https://cdn.autonomous.ai/os/ota/metadata.json" {
		t.Fatalf("MetadataURL = %q", cfg.MetadataURL)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["metadata_url"] != "https://cdn.autonomous.ai/os/ota/metadata.json" {
		t.Fatalf("stored metadata_url = %v", raw["metadata_url"])
	}
	// Keys this binary does not model survive the rewrite.
	if raw["future_key"] == nil || raw["signing_public_key"] != "k" {
		t.Fatalf("rewrite dropped keys: %s", data)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err %v", info.Mode(), err)
	}
	before := string(data)
	_ = LoadOrDefault()
	after, _ := os.ReadFile(path)
	if string(after) != before {
		t.Fatalf("second load rewrote the file")
	}
}

func TestLoadKeepsOtherMetadataURLs(t *testing.T) {
	t.Setenv(MetadataURLEnv, "")
	for _, url := range []string{
		"https://storage.googleapis.com/ota-test-bucket/os/ota/metadata.json",
		"https://cdn.autonomous.ai/os/ota/metadata.json",
		"",
	} {
		body, _ := json.Marshal(map[string]any{"metadata_url": url, "httpPort": 8080})
		path := writeConfig(t, string(body))
		cfg := LoadOrDefault()
		if cfg.MetadataURL != url {
			t.Fatalf("MetadataURL = %q, want %q", cfg.MetadataURL, url)
		}
		data, _ := os.ReadFile(path)
		if string(data) != string(body) {
			t.Fatalf("file changed for %q: %s", url, data)
		}
	}
}

func TestEnvOverrideWinsAndIsNotPersisted(t *testing.T) {
	path := writeConfig(t, `{"metadata_url":"`+bucketMetadataURL+`"}`)
	t.Setenv(MetadataURLEnv, " https://example.test/os/ota/metadata.json ")
	cfg := LoadOrDefault()
	if cfg.MetadataURL != "https://example.test/os/ota/metadata.json" || !cfg.MetadataFromEnv() {
		t.Fatalf("cfg = %q fromEnv=%v", cfg.MetadataURL, cfg.MetadataFromEnv())
	}
	data, _ := os.ReadFile(path)
	if string(data) != `{"metadata_url":"`+bucketMetadataURL+`"}` {
		t.Fatalf("env override touched the file: %s", data)
	}
}

func TestEnvOverrideWithoutConfigFile(t *testing.T) {
	old := configPath
	configPath = filepath.Join(t.TempDir(), "missing.json")
	t.Cleanup(func() { configPath = old })
	t.Setenv(MetadataURLEnv, "https://example.test/m.json")
	if cfg := LoadOrDefault(); cfg.MetadataURL != "https://example.test/m.json" || cfg.PollInterval != "5m" {
		t.Fatalf("cfg = %+v", cfg)
	}
}
