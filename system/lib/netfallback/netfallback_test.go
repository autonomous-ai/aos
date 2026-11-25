package netfallback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeCmds struct {
	output map[string]string
	errs   map[string]error
	calls  []string
}

func (f *fakeCmds) run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	return []byte(f.output[key]), f.errs[key]
}

func setup(t *testing.T) (string, *fakeCmds) {
	t.Helper()
	dir := t.TempDir()
	saved := []string{timesyncdDropInDir, chronyConf, chronySourcesDir, resolvconfConf, staModeScript}
	savedBins, savedRun := timesyncdBinaries, runCmd
	t.Cleanup(func() {
		timesyncdDropInDir, chronyConf, chronySourcesDir, resolvconfConf, staModeScript = saved[0], saved[1], saved[2], saved[3], saved[4]
		timesyncdBinaries, runCmd = savedBins, savedRun
	})
	timesyncdDropInDir = filepath.Join(dir, "timesyncd.conf.d")
	timesyncdBinaries = []string{filepath.Join(dir, "systemd-timesyncd")}
	chronyConf = filepath.Join(dir, "chrony.conf")
	chronySourcesDir = filepath.Join(dir, "sources.d")
	resolvconfConf = filepath.Join(dir, "resolvconf.conf")
	staModeScript = filepath.Join(dir, "device-sta-mode")
	f := &fakeCmds{output: map[string]string{}, errs: map[string]error{}}
	runCmd = f.run
	return dir, f
}

const showTimesync = "timedatectl show-timesync -p FallbackNTPServers --value"

func TestTimesyncdKeepsDefaultsFirstAndIsIdempotent(t *testing.T) {
	dir, f := setup(t)
	if err := os.WriteFile(filepath.Join(dir, "systemd-timesyncd"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	f.output[showTimesync] = "0.debian.pool.ntp.org 1.debian.pool.ntp.org\n"
	changed, err := ensureTimesyncd(context.Background())
	if err != nil || !changed {
		t.Fatalf("first run: changed=%v err=%v", changed, err)
	}
	got, err := os.ReadFile(filepath.Join(timesyncdDropInDir, timesyncdDropInName))
	if err != nil {
		t.Fatal(err)
	}
	want := "FallbackNTP=0.debian.pool.ntp.org 1.debian.pool.ntp.org ntp.aliyun.com ntp.tencent.com cn.pool.ntp.org\n"
	if !strings.HasSuffix(string(got), "[Time]\n"+want) {
		t.Fatalf("drop-in = %q", got)
	}
	if f.calls[len(f.calls)-1] != "systemctl try-restart systemd-timesyncd" {
		t.Fatalf("calls = %v", f.calls)
	}

	// Once applied, timesyncd reports the merged list: nothing more to do.
	f.output[showTimesync] = strings.TrimPrefix(want, "FallbackNTP=")
	f.calls = nil
	changed, err = ensureTimesyncd(context.Background())
	if err != nil || changed {
		t.Fatalf("second run: changed=%v err=%v", changed, err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("second run must not restart timesyncd: %v", f.calls)
	}
}

func TestTimesyncdSkippedWhenAbsentOrUnreadable(t *testing.T) {
	dir, f := setup(t)
	if changed, err := ensureTimesyncd(context.Background()); changed || err != nil || len(f.calls) != 0 {
		t.Fatalf("no timesyncd: changed=%v err=%v calls=%v", changed, err, f.calls)
	}
	if err := os.WriteFile(filepath.Join(dir, "systemd-timesyncd"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	f.errs[showTimesync] = errors.New("not running")
	if changed, err := ensureTimesyncd(context.Background()); changed || err == nil {
		t.Fatalf("unreadable defaults must not write: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(timesyncdDropInDir, timesyncdDropInName)); !os.IsNotExist(err) {
		t.Fatalf("drop-in written without known defaults: %v", err)
	}
}

func TestChronyAddsSourcesOnce(t *testing.T) {
	_, f := setup(t)
	if changed, err := ensureChrony(context.Background()); changed || err != nil {
		t.Fatalf("no chrony: changed=%v err=%v", changed, err)
	}
	if err := os.WriteFile(chronyConf, []byte("pool 2.debian.pool.ntp.org iburst\nsourcedir "+chronySourcesDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := ensureChrony(context.Background())
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(filepath.Join(chronySourcesDir, chronySourcesName))
	for _, want := range []string{"server ntp.aliyun.com iburst", "server ntp.tencent.com iburst", "pool cn.pool.ntp.org iburst"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("sources missing %q:\n%s", want, got)
		}
	}
	if f.calls[len(f.calls)-1] != "chronyc reload sources" {
		t.Fatalf("calls = %v", f.calls)
	}
	if changed, err := ensureChrony(context.Background()); changed || err != nil {
		t.Fatalf("second run: changed=%v err=%v", changed, err)
	}
}

func TestResolvconfFollowsRegion(t *testing.T) {
	_, f := setup(t)
	if err := os.WriteFile(resolvconfConf, []byte("# comment\n"+defaultResolvconfLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Outside China the default line stays as it is.
	if changed, err := ensureResolvconf(context.Background(), false); changed || err != nil || len(f.calls) != 0 {
		t.Fatalf("non-CN: changed=%v err=%v calls=%v", changed, err, f.calls)
	}
	changed, err := ensureResolvconf(context.Background(), true)
	if err != nil || !changed {
		t.Fatalf("CN: changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(resolvconfConf)
	if string(got) != "# comment\n"+chinaResolvconfLine+"\n" {
		t.Fatalf("resolvconf.conf = %q", got)
	}
	if len(f.calls) != 1 || f.calls[0] != "resolvconf -u" {
		t.Fatalf("calls = %v", f.calls)
	}
	if changed, err := ensureResolvconf(context.Background(), true); changed || err != nil {
		t.Fatalf("CN again: changed=%v err=%v", changed, err)
	}
	// Leaving China puts the default line back.
	if changed, err := ensureResolvconf(context.Background(), false); !changed || err != nil {
		t.Fatalf("back to non-CN: changed=%v err=%v", changed, err)
	}
	got, _ = os.ReadFile(resolvconfConf)
	if string(got) != "# comment\n"+defaultResolvconfLine+"\n" {
		t.Fatalf("resolvconf.conf = %q", got)
	}
}

func TestResolvconfLeavesCustomSettingsAlone(t *testing.T) {
	setup(t)
	custom := "name_servers=\"9.9.9.9\"\n"
	if err := os.WriteFile(resolvconfConf, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, china := range []bool{true, false} {
		if changed, err := ensureResolvconf(context.Background(), china); changed || err != nil {
			t.Fatalf("china=%v: changed=%v err=%v", china, changed, err)
		}
	}
	got, _ := os.ReadFile(resolvconfConf)
	if string(got) != custom {
		t.Fatalf("custom setting changed: %q", got)
	}
}

func TestStaModeScriptFollowsRegion(t *testing.T) {
	setup(t)
	script := "#!/bin/bash\n" + defaultStaDNS + "\nprintf \"%b\" \"$DNS_LINES\" > /etc/resolv.conf\n"
	if err := os.WriteFile(staModeScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if changed, err := ensureStaModeScript(false); changed || err != nil {
		t.Fatalf("non-CN: changed=%v err=%v", changed, err)
	}
	changed, err := ensureStaModeScript(true)
	if err != nil || !changed {
		t.Fatalf("CN: changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(staModeScript)
	if !strings.Contains(string(got), `DNS_LINES="${DNS_LINES}nameserver 223.5.5.5\nnameserver 119.29.29.29\nnameserver 1.1.1.1\nnameserver 8.8.8.8\n"`) {
		t.Fatalf("script = %s", got)
	}
	info, _ := os.Stat(staModeScript)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
	if changed, err := ensureStaModeScript(true); changed || err != nil {
		t.Fatalf("CN again: changed=%v err=%v", changed, err)
	}
	if changed, err := ensureStaModeScript(false); !changed || err != nil {
		t.Fatalf("back to non-CN: changed=%v err=%v", changed, err)
	}
	got, _ = os.ReadFile(staModeScript)
	if string(got) != script {
		t.Fatalf("script not restored: %s", got)
	}
}

func TestApplyRegionWaitsForKnownRegion(t *testing.T) {
	_, f := setup(t)
	if err := os.WriteFile(resolvconfConf, []byte(defaultResolvconfLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := applyRegion(context.Background(), "", ""); got != "" || len(f.calls) != 0 {
		t.Fatalf("unknown region: applied=%q calls=%v", got, f.calls)
	}
	if got := applyRegion(context.Background(), " cn ", ""); got != "CN" {
		t.Fatalf("applied = %q, want CN", got)
	}
	data, _ := os.ReadFile(resolvconfConf)
	if string(data) != chinaResolvconfLine+"\n" {
		t.Fatalf("resolvconf.conf = %q", data)
	}
	// A reply without a region keeps what was applied.
	if got := applyRegion(context.Background(), "", "CN"); got != "CN" {
		t.Fatalf("applied = %q, want CN kept", got)
	}
}
