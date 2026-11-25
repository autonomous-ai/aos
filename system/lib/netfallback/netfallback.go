// Package netfallback keeps the device's time and name lookups working on
// networks where the usual public NTP and DNS servers are unreachable.
//
//   - NTP, every device: extra fallback NTP servers are added after the ones the
//     time service already uses, so they are only asked when those fail.
//   - DNS, by region: while the cloud places the device in mainland China, the
//     static fallback DNS servers list the resolvers that answer there first;
//     in any other region the image's default list is kept (or put back).
//
// Every step is idempotent and best-effort. A file that is missing, or whose
// line was changed by hand, is left alone.
package netfallback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ExtraNTPServers are added after the time service's existing fallback servers.
var ExtraNTPServers = []string{"ntp.aliyun.com", "ntp.tencent.com", "cn.pool.ntp.org"}

const (
	// RegionCN is the region code the cloud reports for mainland China.
	RegionCN = "CN"

	// defaultResolvconfLine / chinaResolvconfLine are the static fallback DNS
	// servers openresolv adds to resolv.conf. The default line is the one the
	// images have always written; the China line puts resolvers that answer
	// there ahead of it.
	defaultResolvconfLine = `name_servers="1.1.1.1 8.8.8.8"`
	chinaResolvconfLine   = `name_servers="223.5.5.5 119.29.29.29 1.1.1.1 8.8.8.8"`

	// defaultStaDNS / chinaStaDNS are the fallback lines device-sta-mode writes
	// after the DHCP lease's DNS servers when it rebuilds resolv.conf.
	// The whole assignment is matched so neither is a substring of the other.
	defaultStaDNS = `DNS_LINES="${DNS_LINES}nameserver 1.1.1.1\nnameserver 8.8.8.8\n"`
	chinaStaDNS   = `DNS_LINES="${DNS_LINES}nameserver 223.5.5.5\nnameserver 119.29.29.29\nnameserver 1.1.1.1\nnameserver 8.8.8.8\n"`

	timesyncdDropInName = "90-autonomous-fallback-ntp.conf"
	chronySourcesName   = "autonomous-fallback.sources"

	commandTimeout = 10 * time.Second
)

// Paths, the command runner and the region poll interval, as variables so
// tests can redirect them.
var (
	timesyncdDropInDir = "/etc/systemd/timesyncd.conf.d"
	timesyncdBinaries  = []string{"/lib/systemd/systemd-timesyncd", "/usr/lib/systemd/systemd-timesyncd"}
	chronyConf         = "/etc/chrony/chrony.conf"
	chronySourcesDir   = "/etc/chrony/sources.d"
	resolvconfConf     = "/etc/resolvconf.conf"
	staModeScript      = "/usr/local/bin/device-sta-mode"

	regionCheckInterval = 5 * time.Minute

	runCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
)

// Run adds the NTP fallback once, then keeps the DNS fallback in line with the
// region until ctx ends. region returns the region the cloud last reported, or
// "" while none is known; DNS is left alone until one is. Does nothing off
// Linux (simulator).
func Run(ctx context.Context, region func() string) {
	if runtime.GOOS != "linux" {
		return
	}
	changed, err := ensureTimesyncd(ctx)
	logStep("timesyncd", changed, err)
	changed, err = ensureChrony(ctx)
	logStep("chrony", changed, err)

	ticker := time.NewTicker(regionCheckInterval)
	defer ticker.Stop()
	applied := ""
	for {
		applied = applyRegion(ctx, region(), applied)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// applyRegion sets the DNS fallback for region when it is known and differs
// from the one applied last, and returns the region now applied.
func applyRegion(ctx context.Context, region, applied string) string {
	r := strings.ToUpper(strings.TrimSpace(region))
	if r == "" || r == applied {
		return applied
	}
	china := r == RegionCN
	changed, err := ensureResolvconf(ctx, china)
	logStep("resolvconf", changed, err)
	changed, err = ensureStaModeScript(china)
	logStep("device-sta-mode", changed, err)
	return r
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

// ensureTimesyncd adds ExtraNTPServers to systemd-timesyncd's fallback list,
// keeping the servers it already uses first.
func ensureTimesyncd(ctx context.Context) (bool, error) {
	if !anyExists(timesyncdBinaries) {
		return false, nil
	}
	cctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := runCmd(cctx, "timedatectl", "show-timesync", "-p", "FallbackNTPServers", "--value")
	if err != nil {
		// Without the current list we would replace defaults we cannot see.
		return false, fmt.Errorf("read fallback NTP servers: %w", err)
	}
	current := strings.Fields(string(out))
	if len(current) == 0 {
		return false, errors.New("timesyncd reports no fallback NTP servers")
	}
	merged, changed := appendMissing(current, ExtraNTPServers)
	if !changed {
		return false, nil
	}
	content := "# Extra fallback NTP servers for networks where the default ones are\n" +
		"# unreachable. The defaults stay first.\n" +
		"[Time]\nFallbackNTP=" + strings.Join(merged, " ") + "\n"
	if err := writeFileAtomic(filepath.Join(timesyncdDropInDir, timesyncdDropInName), []byte(content), 0o644); err != nil {
		return false, err
	}
	// try-restart leaves a stopped timesyncd (e.g. on chrony images) stopped.
	if out, err := runCmd(cctx, "systemctl", "try-restart", "systemd-timesyncd"); err != nil {
		slog.Warn("systemd-timesyncd restart failed", "component", "netfallback", "error", err, "output", strings.TrimSpace(string(out)))
	}
	return true, nil
}

// ensureChrony adds ExtraNTPServers as extra chrony sources when chrony reads
// a sources directory.
func ensureChrony(ctx context.Context) (bool, error) {
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
	var b strings.Builder
	b.WriteString("# Extra NTP sources for networks where the default ones are unreachable.\n")
	for _, s := range ExtraNTPServers {
		kind := "server"
		if strings.Contains(s, "pool.") {
			kind = "pool"
		}
		fmt.Fprintf(&b, "%s %s iburst\n", kind, s)
	}
	path := filepath.Join(chronySourcesDir, chronySourcesName)
	if existing, err := os.ReadFile(path); err == nil && string(existing) == b.String() {
		return false, nil
	}
	if err := writeFileAtomic(path, []byte(b.String()), 0o644); err != nil {
		return false, err
	}
	cctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if out, err := runCmd(cctx, "chronyc", "reload", "sources"); err != nil && !errors.Is(err, exec.ErrNotFound) {
		slog.Warn("chronyc reload sources failed", "component", "netfallback", "error", err, "output", strings.TrimSpace(string(out)))
	}
	return true, nil
}

// ensureResolvconf switches openresolv's static fallback between the default
// and the China line, then regenerates resolv.conf.
func ensureResolvconf(ctx context.Context, china bool) (bool, error) {
	from, to := chinaResolvconfLine, defaultResolvconfLine
	if china {
		from, to = to, from
	}
	changed, err := replaceLine(resolvconfConf, from, to)
	if err != nil || !changed {
		return false, err
	}
	cctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if out, err := runCmd(cctx, "resolvconf", "-u"); err != nil && !errors.Is(err, exec.ErrNotFound) {
		slog.Warn("resolvconf -u failed", "component", "netfallback", "error", err, "output", strings.TrimSpace(string(out)))
	}
	return true, nil
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

// replaceLine swaps the one whole line equal to old for repl. Files without
// that exact line (missing, already switched, or edited by hand) are left alone.
func replaceLine(path, old, repl string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	lines := strings.Split(string(data), "\n")
	at := -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case repl:
			return false, nil
		case old:
			if at >= 0 {
				return false, nil
			}
			at = i
		}
	}
	if at < 0 {
		return false, nil
	}
	lines[at] = repl
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return true, writeFileAtomic(path, []byte(strings.Join(lines, "\n")), info.Mode().Perm())
}

// appendMissing returns list plus every extra not already in it.
func appendMissing(list, extra []string) ([]string, bool) {
	seen := make(map[string]bool, len(list))
	for _, s := range list {
		seen[s] = true
	}
	out := append([]string(nil), list...)
	for _, s := range extra {
		if !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	return out, len(out) != len(list)
}

func anyExists(paths []string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
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
