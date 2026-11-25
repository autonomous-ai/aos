package netfallback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeCmds struct {
	fail  map[string]error
	calls []string
}

func (f *fakeCmds) run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	return nil, f.fail[key]
}

func setup(t *testing.T) (string, *fakeCmds) {
	t.Helper()
	dir := t.TempDir()
	saved := []string{timesyncdDropInPath, chronyConf, chronySourcesDir, resolvconfConf, staModeScript}
	savedRun, savedEnabled, savedUnits := runCmd, enabled, timesyncdUnitPaths
	t.Cleanup(func() {
		timesyncdDropInPath, chronyConf, chronySourcesDir, resolvconfConf, staModeScript = saved[0], saved[1], saved[2], saved[3], saved[4]
		runCmd, enabled, timesyncdUnitPaths = savedRun, savedEnabled, savedUnits
	})
	// timesyncd is installed unless a test removes this unit file.
	timesyncdUnitPaths = []string{filepath.Join(dir, "systemd-timesyncd.service")}
	if err := os.WriteFile(timesyncdUnitPaths[0], nil, 0o644); err != nil {
		t.Fatal(err)
	}
	timesyncdDropInPath = filepath.Join(dir, "timesyncd.conf.d", "90-autonomous-fallback-ntp.conf")
	chronyConf = filepath.Join(dir, "chrony.conf")
	chronySourcesDir = filepath.Join(dir, "sources.d")
	resolvconfConf = filepath.Join(dir, "resolvconf.conf")
	staModeScript = filepath.Join(dir, "device-sta-mode")
	f := &fakeCmds{}
	runCmd = f.run
	enabled = true
	return dir, f
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTimesyncdDropInOnlyInChina(t *testing.T) {
	_, f := setup(t)
	if changed, err := ensureTimesyncd(context.Background(), false, false); changed || err != nil || len(f.calls) != 0 {
		t.Fatalf("non-CN, nothing there: changed=%v err=%v calls=%v", changed, err, f.calls)
	}
	if changed, err := ensureTimesyncd(context.Background(), true, false); !changed || err != nil {
		t.Fatalf("CN: changed=%v err=%v", changed, err)
	}
	if got := read(t, timesyncdDropInPath); !strings.HasSuffix(got,
		"[Time]\nFallbackNTP=0.debian.pool.ntp.org 1.debian.pool.ntp.org ntp.aliyun.com ntp.tencent.com cn.pool.ntp.org\n") {
		t.Fatalf("drop-in = %q", got)
	}
	if len(f.calls) != 1 || f.calls[0] != "systemctl try-restart systemd-timesyncd" {
		t.Fatalf("calls = %v", f.calls)
	}
	if changed, _ := ensureTimesyncd(context.Background(), true, false); changed || len(f.calls) != 1 {
		t.Fatalf("CN again must not rewrite or restart: calls=%v", f.calls)
	}
	if changed, err := ensureTimesyncd(context.Background(), false, false); !changed || err != nil {
		t.Fatalf("leaving CN: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(timesyncdDropInPath); !os.IsNotExist(err) {
		t.Fatalf("drop-in still there: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("leaving CN must restart timesyncd: %v", f.calls)
	}
}

func TestChronySourcesOnlyInChina(t *testing.T) {
	_, f := setup(t)
	if changed, err := ensureChrony(context.Background(), true, false); changed || err != nil {
		t.Fatalf("no chrony: changed=%v err=%v", changed, err)
	}
	if err := os.WriteFile(chronyConf, []byte("pool 2.debian.pool.ntp.org iburst\nsourcedir "+chronySourcesDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := ensureChrony(context.Background(), true, false); !changed || err != nil {
		t.Fatalf("CN: changed=%v err=%v", changed, err)
	}
	got := read(t, filepath.Join(chronySourcesDir, chronySourcesName))
	for _, want := range []string{"server ntp.aliyun.com iburst", "server ntp.tencent.com iburst", "pool cn.pool.ntp.org iburst"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sources missing %q:\n%s", want, got)
		}
	}
	if changed, _ := ensureChrony(context.Background(), true, false); changed {
		t.Fatal("CN again must be a no-op")
	}
	if changed, err := ensureChrony(context.Background(), false, false); !changed || err != nil {
		t.Fatalf("leaving CN: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(chronySourcesDir, chronySourcesName)); !os.IsNotExist(err) {
		t.Fatalf("sources still there: %v", err)
	}
	if strings.Join(f.calls, ";") != "chronyc reload sources;chronyc reload sources" {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestResolvconfAppendsInChinaAndRestoresImageLine(t *testing.T) {
	_, f := setup(t)
	image := "# comment\n" + defaultResolvconfLine + "\n"
	if err := os.WriteFile(resolvconfConf, []byte(image), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := ensureResolvconf(context.Background(), false, false); changed || err != nil || len(f.calls) != 0 {
		t.Fatalf("non-CN on the image's line: changed=%v err=%v calls=%v", changed, err, f.calls)
	}
	if changed, err := ensureResolvconf(context.Background(), true, false); !changed || err != nil {
		t.Fatalf("CN: changed=%v err=%v", changed, err)
	}
	if got := read(t, resolvconfConf); got != "# comment\n"+chinaResolvconfLine+"\n" {
		t.Fatalf("resolvconf.conf = %q", got)
	}
	if changed, _ := ensureResolvconf(context.Background(), true, false); changed {
		t.Fatal("CN again must be a no-op")
	}
	if changed, err := ensureResolvconf(context.Background(), false, false); !changed || err != nil {
		t.Fatalf("leaving CN: changed=%v err=%v", changed, err)
	}
	if got := read(t, resolvconfConf); got != image {
		t.Fatalf("image line not restored: %q", got)
	}
	if strings.Join(f.calls, ";") != "resolvconf -u;resolvconf -u" {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestResolvconfOutsideChinaLeavesOtherLinesAlone(t *testing.T) {
	setup(t)
	custom := "name_servers=\"9.9.9.9\"\n"
	if err := os.WriteFile(resolvconfConf, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := ensureResolvconf(context.Background(), false, false); changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got := read(t, resolvconfConf); got != custom {
		t.Fatalf("custom setting changed: %q", got)
	}
}

func TestResolvconfFixed(t *testing.T) {
	cases := []struct{ now, want string }{
		{"", chinaResolvconfLine + "\n"},
		{"a=1\n", "a=1\n" + chinaResolvconfLine + "\n"},
		{"a=1\nname_servers=\"x\"\nname_servers_append=\"y\"\nb=2", "a=1\n" + chinaResolvconfLine + "\nb=2\n"},
	}
	for _, c := range cases {
		if got := resolvconfFixed(c.now, chinaResolvconfLine); got != c.want {
			t.Errorf("resolvconfFixed(%q) = %q, want %q", c.now, got, c.want)
		}
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
	if changed, err := ensureStaModeScript(true); !changed || err != nil {
		t.Fatalf("CN: changed=%v err=%v", changed, err)
	}
	if got := read(t, staModeScript); !strings.Contains(got, chinaStaDNS) {
		t.Fatalf("script = %s", got)
	}
	if info, _ := os.Stat(staModeScript); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
	if changed, _ := ensureStaModeScript(true); changed {
		t.Fatal("CN again must be a no-op")
	}
	if changed, err := ensureStaModeScript(false); !changed || err != nil {
		t.Fatalf("leaving CN: changed=%v err=%v", changed, err)
	}
	if got := read(t, staModeScript); got != script {
		t.Fatalf("script not restored: %s", got)
	}
}

func TestApplierFollowsRegionAfterSetup(t *testing.T) {
	_, f := setup(t)
	if err := os.WriteFile(resolvconfConf, []byte(defaultResolvconfLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	region, setUp := "CN", false
	a := NewApplier(func() (string, bool) { return region, setUp })

	// Before setup nothing changes, so an image built for China keeps its settings.
	a.Apply(context.Background())
	if len(f.calls) != 0 {
		t.Fatalf("before setup: calls=%v", f.calls)
	}

	setUp = true
	a.Apply(context.Background())
	if got := read(t, resolvconfConf); got != chinaResolvconfLine+"\n" {
		t.Fatalf("CN: resolvconf.conf = %q", got)
	}
	if _, err := os.Stat(timesyncdDropInPath); err != nil {
		t.Fatalf("CN: drop-in missing: %v", err)
	}

	// Same decision again: no work.
	n := len(f.calls)
	a.Apply(context.Background())
	if len(f.calls) != n {
		t.Fatalf("repeat apply ran commands: %v", f.calls[n:])
	}

	// An empty region means not in China: everything goes back.
	region = ""
	a.Apply(context.Background())
	if got := read(t, resolvconfConf); got != defaultResolvconfLine+"\n" {
		t.Fatalf("no region: resolvconf.conf = %q", got)
	}
	if _, err := os.Stat(timesyncdDropInPath); !os.IsNotExist(err) {
		t.Fatalf("no region: drop-in still there: %v", err)
	}

	// " cn " counts as China.
	region = " cn "
	a.Apply(context.Background())
	if got := read(t, resolvconfConf); got != chinaResolvconfLine+"\n" {
		t.Fatalf("cn: resolvconf.conf = %q", got)
	}
}

func TestApplierRetriesAfterAFailedStep(t *testing.T) {
	_, f := setup(t)
	if err := os.WriteFile(resolvconfConf, []byte(defaultResolvconfLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.fail = map[string]error{"resolvconf -u": errors.New("busy")}
	a := NewApplier(func() (string, bool) { return "CN", true })

	a.Apply(context.Background())
	if a.applied != nil || !a.retry {
		t.Fatalf("a failed step must not mark the state applied: applied=%v retry=%v", a.applied, a.retry)
	}

	// The file was already written; the retry still reruns every refresh.
	f.fail, f.calls = nil, nil
	a.Apply(context.Background())
	if a.applied == nil || !*a.applied || a.retry {
		t.Fatalf("retry did not complete: applied=%v retry=%v", a.applied, a.retry)
	}
	want := "systemctl try-restart systemd-timesyncd;resolvconf -u"
	if got := strings.Join(f.calls, ";"); got != want {
		t.Fatalf("retry calls = %q, want %q", got, want)
	}

	// Once applied, nothing runs again.
	f.calls = nil
	a.Apply(context.Background())
	if len(f.calls) != 0 {
		t.Fatalf("applied state reran commands: %v", f.calls)
	}
}

type exitErr int

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

func TestApplierWithoutTimesyncdOrChronyIsApplied(t *testing.T) {
	_, f := setup(t)
	if err := os.Remove(timesyncdUnitPaths[0]); err != nil {
		t.Fatal(err)
	}
	// chronyc absent; a systemctl call would report the unit missing.
	f.fail = map[string]error{
		"chronyc reload sources":                  exec.ErrNotFound,
		"systemctl try-restart systemd-timesyncd": exitErr(5),
	}
	region := "CN"
	a := NewApplier(func() (string, bool) { return region, true })

	a.Apply(context.Background())
	if a.applied == nil || !*a.applied || a.retry {
		t.Fatalf("CN not applied: applied=%v retry=%v", a.applied, a.retry)
	}
	if _, err := os.Stat(timesyncdDropInPath); !os.IsNotExist(err) {
		t.Fatalf("drop-in written without timesyncd: %v", err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "systemctl") || strings.HasPrefix(c, "chronyc") {
			t.Fatalf("unexpected refresh %q", c)
		}
	}

	region = ""
	a.Apply(context.Background())
	if a.applied == nil || *a.applied || a.retry {
		t.Fatalf("leaving CN not applied: applied=%v retry=%v", a.applied, a.retry)
	}
}

func TestRefreshToleratesMissingUnitOrCommand(t *testing.T) {
	_, f := setup(t)
	f.fail = map[string]error{
		"systemctl try-restart systemd-timesyncd": exitErr(5),
		"chronyc reload sources":                  exec.ErrNotFound,
		"resolvconf -u":                           exitErr(5),
	}
	ctx := context.Background()
	if err := refreshCmd(ctx, "systemctl", "try-restart", "systemd-timesyncd"); err != nil {
		t.Fatalf("missing unit: %v", err)
	}
	if err := refreshCmd(ctx, "chronyc", "reload", "sources"); err != nil {
		t.Fatalf("missing chronyc: %v", err)
	}
	if err := refreshCmd(ctx, "resolvconf", "-u"); err == nil {
		t.Fatal("a failing resolvconf must stay an error")
	}
}
