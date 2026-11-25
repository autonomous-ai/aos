package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"go.autonomous.ai/os/system/lib/otamirror"
)

// configPath is the bootstrap worker's own config file (a var so tests can redirect it).
var configPath = "/root/config/bootstrap.json"

// MetadataURLEnv overrides metadata_url for this process only; it is never written back.
const MetadataURLEnv = "OTA_METADATA_URL"

// BootstrapVersion is injected at build time via ldflags.
var BootstrapVersion = "dev"

// Config holds bootstrap OTA worker configuration (stored in bootstrap.json).
type Config struct {
	HttpPort int `json:"httpPort" yaml:"httpPort" validate:"required"`

	MetadataURL string `json:"metadata_url" yaml:"metadataURL"`
	// SigningPublicKey is the base64 Ed25519 key authorizing OTA metadata; provisioned
	// locally, never accepted from the feed.
	SigningPublicKey string `json:"signing_public_key" yaml:"signingPublicKey"`
	// RollbackVersions maps component -> rolled-back version; only that exact target is skipped.
	RollbackVersions map[string]string `json:"rollback_versions,omitempty" yaml:"rollbackVersions"`
	PollInterval     string            `json:"poll_interval" yaml:"pollInterval"` // e.g. "1h", "10m"
	StateFile        string            `json:"state_file" yaml:"stateFile"`

	// metadataFromEnv marks MetadataURL as the OTA_METADATA_URL override.
	metadataFromEnv bool
}

// MetadataFromEnv reports whether MetadataURL came from OTA_METADATA_URL.
func (c *Config) MetadataFromEnv() bool { return c.metadataFromEnv }

// Default returns operational defaults; MetadataURL is empty until provisioning.
func Default() Config {
	return Config{
		HttpPort:     8080,
		MetadataURL:  "",
		PollInterval: "5m",
		StateFile:    "/root/bootstrap/state.json",
	}
}

// LoadOrDefault overlays bootstrap.json onto Default(); missing/corrupt file yields defaults.
// OTA_METADATA_URL, when set, replaces metadata_url in memory. Otherwise a
// metadata_url in the OTA bucket is moved to the CDN that serves the same
// bucket, and the new value is written back once so every reader follows.
func LoadOrDefault() *Config {
	cfg := Default()
	data, err := os.ReadFile(configPath)
	if err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			cfg = Default()
		}
	}
	if env := strings.TrimSpace(os.Getenv(MetadataURLEnv)); env != "" {
		cfg.MetadataURL = env
		cfg.metadataFromEnv = true
		return &cfg
	}
	if err == nil {
		if moved := otamirror.ToCDN(strings.TrimSpace(cfg.MetadataURL)); moved != strings.TrimSpace(cfg.MetadataURL) {
			if werr := rewriteMetadataURL(moved); werr != nil {
				slog.Warn("metadata_url moved to the CDN in memory only", "component", "bootstrap", "error", werr)
			} else {
				slog.Info("metadata_url moved to the CDN", "component", "bootstrap", "url", moved)
			}
			cfg.MetadataURL = moved
		}
	}
	return &cfg
}

// rewriteMetadataURL replaces only metadata_url in bootstrap.json, keeping every
// other key (including ones this binary does not know), via an atomic rename.
func rewriteMetadataURL(metadataURL string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	value, err := json.Marshal(metadataURL)
	if err != nil {
		return fmt.Errorf("encode metadata_url: %w", err)
	}
	raw["metadata_url"] = value
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(configPath); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(configPath), ".bootstrap.json.new-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, configPath); err != nil {
		return fmt.Errorf("install config: %w", err)
	}
	return nil
}

// Save writes the config to /root/config/bootstrap.json.
func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("write config %s: %w", configPath, err)
	}
	return nil
}
