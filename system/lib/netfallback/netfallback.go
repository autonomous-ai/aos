// Package netfallback sets the device's fallback time and DNS servers for the
// region the cloud places it in, so time sync and name lookups keep working in
// mainland China while every other device keeps the image's settings.
//
// While the region is CN:
//   - systemd-timesyncd gets a drop-in whose FallbackNTP lists Debian's pool,
//     then servers that answer in China (FallbackNTP, never NTP=, so a server
//     the network hands out still comes first); chrony gets the same servers
//     as extra sources when it reads a sources directory.
//   - openresolv appends resolvers that answer in China after the router's
//     (name_servers_append, never name_servers=, which goes before the
//     router's), and device-sta-mode lists them after the DHCP lease's.
//
// In any other region, or with no region, the drop-in and the sources file
// are removed and the image's DNS lines are put back, but only where ours are
// the ones in place. Nothing changes before the device is set up, so an image
// built for China keeps its settings until the cloud has answered.
//
// Every step is idempotent and best-effort; outside China a line changed by
// hand is left alone.
package netfallback

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// RegionCN is the region code the cloud reports for mainland China.
const RegionCN = "CN"

const (
	// timesyncdDropIn is written while in China only.
	timesyncdDropIn = "# Fallback time servers that answer in mainland China, after the Debian pool.\n" +
		"[Time]\nFallbackNTP=0.debian.pool.ntp.org 1.debian.pool.ntp.org ntp.aliyun.com ntp.tencent.com cn.pool.ntp.org\n"
	// chronySources is written while in China only.
	chronySources = "# Time servers that answer in mainland China.\n" +
		"server ntp.aliyun.com iburst\nserver ntp.tencent.com iburst\npool cn.pool.ntp.org iburst\n"

	// defaultResolvconfLine is the image's static fallback; chinaResolvconfLine
	// takes its place in China, after the router's servers.
	defaultResolvconfLine = `name_servers="1.1.1.1 8.8.8.8"`
	chinaResolvconfLine   = `name_servers_append="223.5.5.5 119.29.29.29 1.1.1.1 8.8.8.8"`

	// defaultStaDNS / chinaStaDNS are the fallback lines device-sta-mode writes
	// after the DHCP lease's DNS servers when it rebuilds resolv.conf. The
	// whole assignment is matched so neither is a substring of the other.
	defaultStaDNS = `DNS_LINES="${DNS_LINES}nameserver 1.1.1.1\nnameserver 8.8.8.8\n"`
	chinaStaDNS   = `DNS_LINES="${DNS_LINES}nameserver 223.5.5.5\nnameserver 119.29.29.29\nnameserver 1.1.1.1\nnameserver 8.8.8.8\n"`

	commandTimeout = 10 * time.Second
)

// Paths, the command runner and the region poll interval, as variables so
// tests can redirect them.
var (
	timesyncdDropInPath = "/etc/systemd/timesyncd.conf.d/90-autonomous-fallback-ntp.conf"
	chronyConf          = "/etc/chrony/chrony.conf"
	chronySourcesDir    = "/etc/chrony/sources.d"
	chronySourcesName   = "autonomous-fallback.sources"
	resolvconfConf      = "/etc/resolvconf.conf"
	staModeScript       = "/usr/local/bin/device-sta-mode"

	regionCheckInterval = 5 * time.Minute

	// enabled is false off Linux (simulator), where nothing is touched.
	enabled = runtime.GOOS == "linux"

	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
)

// State reports the region the cloud last reported ("" for none) and whether
// the device is set up. Before setup the region is not known yet.
type State func() (region string, setUp bool)

// Applier applies the settings for the current state, doing work only when
// the China/not-China decision changed since the last call.
type Applier struct {
	state   State
	applied *bool
	mu      sync.Mutex
}

// NewApplier returns an Applier reading state.
func NewApplier(state State) *Applier { return &Applier{state: state} }

// Apply reads the state and applies it. Safe to call from any goroutine, e.g.
// right after the region changes.
func (a *Applier) Apply(ctx context.Context) {
	if !enabled {
		return
	}
	region, setUp := a.state()
	if !setUp {
		return
	}
	china := strings.EqualFold(strings.TrimSpace(region), RegionCN)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.applied != nil && *a.applied == china {
		return
	}
	apply(ctx, china)
	a.applied = &china
}

// Run applies the state now and every regionCheckInterval until ctx ends.
func (a *Applier) Run(ctx context.Context) {
	ticker := time.NewTicker(regionCheckInterval)
	defer ticker.Stop()
	for {
		a.Apply(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func apply(ctx context.Context, china bool) {
	changed, err := ensureTimesyncd(ctx, china)
	logStep("timesyncd", changed, err)
	changed, err = ensureChrony(ctx, china)
	logStep("chrony", changed, err)
	changed, err = ensureResolvconf(ctx, china)
	logStep("resolvconf", changed, err)
	changed, err = ensureStaModeScript(china)
	logStep("device-sta-mode", changed, err)
}

func logStep(name string, changed bool, err error) {
	if err != nil {
		slog.Warn("network fallback step failed", "component", "netfallback", "step", name, "error", err)
		return
	}
	if changed {
		slog.Info("network fallback updated", "component", "netfallback", "step", name)
	}
}

// ensureTimesyncd writes the drop-in in China and removes it elsewhere, then
// restarts a running timesyncd after a change.
func ensureTimesyncd(ctx context.Context, china bool) (bool, error) {
	changed, err := putOrRemove(timesyncdDropInPath, timesyncdDropIn, china)
	if err != nil || !changed {
		return changed, err
	}
	cctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	// try-restart leaves a stopped timesyncd (e.g. on chrony images) stopped.
	if out, err := runCmd(cctx, "systemctl", "try-restart", "systemd-timesyncd"); err != nil {
		slog.Warn("systemd-timesyncd restart failed", "component", "netfallback", "error", err, "output", strings.TrimSpace(string(out)))
	}
	return true, nil
}

// ensureChrony writes the extra sources in China when chrony reads a sources
// directory, and removes them elsewhere, then reloads chrony's sources.
func ensureChrony(ctx context.Context, china bool) (bool, error) {
	if china {
		conf, err := os.ReadFile(chronyConf)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		if !strings.Contains(string(conf), "sourcedir "+chronySourcesDir) {
			return false, nil
		}
	}
	changed, err := putOrRemove(filepath.Join(chronySourcesDir, chronySourcesName), chronySources, china)
	if err != nil || !changed {
		return changed, err
	}
	cctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if out, err := runCmd(cctx, "chronyc", "reload", "sources"); err != nil && !errors.Is(err, exec.ErrNotFound) {
		slog.Warn("chronyc reload sources failed", "component", "netfallback", "error", err, "output", strings.TrimSpace(string(out)))
	}
	return true, nil
}

// putOrRemove writes content to path when want is true (unless it already
// holds it) and removes path otherwise; it reports whether anything changed.
func putOrRemove(path, content string, want bool) (bool, error) {
	if !want {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
	if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
		return false, nil
	}
	return true, writeFileAtomic(path, []byte(content), 0o644)
}

// ensureResolvconf puts chinaResolvconfLine in place of openresolv's
// name_servers lines in China. Elsewhere it puts the image's line back, but
// only where chinaResolvconfLine is the one in place. resolv.conf is
// regenerated after a change.
func ensureResolvconf(ctx context.Context, china bool) (bool, error) {
	data, err := os.ReadFile(resolvconfConf)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	now := string(data)
	line := chinaResolvconfLine
	if !china {
		if !hasLine(now, chinaResolvconfLine) {
			return false, nil
		}
		line = defaultResolvconfLine
	}
	fixed := resolvconfFixed(now, line)
	if fixed == now {
		return false, nil
	}
	if err := writeFileAtomic(resolvconfConf, []byte(fixed), 0o644); err != nil {
		return false, err
	}
	cctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if out, err := runCmd(cctx, "resolvconf", "-u"); err != nil && !errors.Is(err, exec.ErrNotFound) {
		slog.Warn("resolvconf -u failed", "component", "netfallback", "error", err, "output", strings.TrimSpace(string(out)))
	}
	return true, nil
}

// resolvconfFixed returns now with line in place of the first name_servers*
// line and any other one dropped, or line last when there is none; every
// other line is kept.
func resolvconfFixed(now, line string) string {
	var b strings.Builder
	placed := false
	for _, kept := range strings.SplitAfter(now, "\n") {
		if kept == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(kept), "name_servers") {
			if !placed {
				b.WriteString(line + "\n")
				placed = true
			}
			continue
		}
		b.WriteString(kept)
		if !strings.HasSuffix(kept, "\n") {
			b.WriteString("\n")
		}
	}
	if !placed {
		b.WriteString(line + "\n")
	}
	return b.String()
}

func hasLine(text, line string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

// ensureStaModeScript switches the fallback DNS lines device-sta-mode writes
// after the DHCP lease's DNS servers. It takes effect on the next WiFi join.
func ensureStaModeScript(china bool) (bool, error) {
	from, to := []byte(chinaStaDNS), []byte(defaultStaDNS)
	if china {
		from, to = to, from
	}
	data, err := os.ReadFile(staModeScript)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if bytes.Contains(data, to) || bytes.Count(data, from) != 1 {
		return false, nil
	}
	info, err := os.Stat(staModeScript)
	if err != nil {
		return false, err
	}
	updated := bytes.Replace(data, from, to, 1)
	return true, writeFileAtomic(staModeScript, updated, info.Mode().Perm())
}

// writeFileAtomic writes data to a temp file beside path and renames it in.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
