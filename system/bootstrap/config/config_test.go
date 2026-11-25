package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
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
	old, oldLock := configPath, LockPath
	configPath = path
	LockPath = filepath.Join(t.TempDir(), "software-update.lock")
	t.Cleanup(func() { configPath, LockPath = old, oldLock })
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

func holdLock(t *testing.T) func() {
	t.Helper()
	file, err := os.OpenFile(LockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }
}

func TestMetadataURLRewriteWaitsForRunningUpdate(t *testing.T) {
	t.Setenv(MetadataURLEnv, "")
	body := `{"metadata_url":"` + bucketMetadataURL + `"}`
	path := writeConfig(t, body)
	release := holdLock(t)
	cfg := LoadOrDefault()
	if cfg.MetadataURL != "https://cdn.autonomous.ai/os/ota/metadata.json" {
		t.Fatalf("MetadataURL = %q", cfg.MetadataURL)
	}
	if data, _ := os.ReadFile(path); string(data) != body {
		t.Fatalf("file rewritten while an update held the lock: %s", data)
	}
	release()
	_ = LoadOrDefault()
	var raw map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &raw); err != nil || raw["metadata_url"] != "https://cdn.autonomous.ai/os/ota/metadata.json" {
		t.Fatalf("not rewritten after the lock was released: %s", data)
	}
}

func TestSaveKeepsKeysWrittenByOthers(t *testing.T) {
	t.Setenv(MetadataURLEnv, "")
	path := writeConfig(t, `{"httpPort":8080,"metadata_url":"https://cdn.autonomous.ai/os/ota/metadata.json","future_key":1}`)
	cfg := LoadOrDefault()
	// software-update records a rollback after this process loaded the file.
	if err := os.WriteFile(path, []byte(`{"httpPort":8080,"metadata_url":"https://cdn.autonomous.ai/os/ota/metadata.json","future_key":1,"rollback_versions":{"hal":"0.1.2"}}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	cfg.PollInterval = "10m"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	rollback, _ := raw["rollback_versions"].(map[string]any)
	if raw["poll_interval"] != "10m" || raw["future_key"] == nil || rollback["hal"] != "0.1.2" {
		t.Fatalf("Save dropped keys: %s", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode())
	}
}

func TestSaveRefusesWhileUpdateRuns(t *testing.T) {
	path := writeConfig(t, `{"future_key":1}`)
	release := holdLock(t)
	defer release()
	cfg := Default()
	if err := cfg.Save(); err == nil {
		t.Fatal("Save wrote while an update held the lock")
	}
	if data, _ := os.ReadFile(path); string(data) != `{"future_key":1}` {
		t.Fatalf("file changed: %s", data)
	}
}

func TestSaveCreatesMissingFile(t *testing.T) {
	path := writeConfig(t, "")
	_ = os.Remove(path)
	cfg := Default()
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat %v err %v", info, err)
	}
}
