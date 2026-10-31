package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
)

// fakeProc points procRoot at a minimal /proc for identify/validate.
func fakeProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for p, v := range map[string]string{
		"sys/kernel/osrelease":      "7.0.14-20-pve",
		"sys/kernel/tainted":        "12288",
		"sys/kernel/random/boot_id": "0b5e6f6e-0000-4000-8000-000000000000",
		"uptime":                    "3600.12 40000.00",
		"loadavg":                   "0.20 0.30 0.40 1/500 12345",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
	return root
}

// referenceN5 is the N5 Pro tree with the reference machine's DMI, the
// module entry of driver 0.2.1+sr1~rc1 and its register dump.
func referenceN5(t *testing.T) string {
	t.Helper()
	root := fakeN5(t, false)
	w := func(p, v string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{"product_name": "N5 PRO", "board_name": "F8NAA", "board_version": "1.0", "sys_vendor": "Micro Computer (HK) Tech Limited", "bios_version": "1.05", "bios_date": "03/31/2026", "ec_firmware_release": "0.14"} {
		w("class/dmi/id/"+k, v)
	}
	w("module/minisforum_n5_it5571/version", "0.2.1+sr1~rc1")
	w("module/minisforum_n5_it5571/srcversion", "886E3C25669AF187D2E96F0")
	w("module/minisforum_n5_it5571/taint", "OE")
	w("module/minisforum_n5_it5571/parameters/force", "N")
	w("module/minisforum_n5_it5571/parameters/experimental_write", "Y")
	w("module/minisforum_n5_it5571/parameters/debug_regs", "Y")
	w("kernel/debug/minisforum_n5_it5571/dcr", "1800: 00 ff 00 55 4a 8c 8c 8c 00 00 00 06 00 00 00 00")
	return root
}

func TestIdentifyJSON(t *testing.T) {
	referenceN5(t)
	fakeProc(t)
	out, _, rc := captureOutput(t, func() int { return cmdIdentify([]string{"--json"}) })
	if rc != exitOK {
		t.Fatalf("rc %d", rc)
	}
	var id machineIdentity
	if err := json.Unmarshal([]byte(out), &id); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if id.DMI.EC != "0.14" || id.Kernel != "7.0.14-20-pve" || id.Driver.Version != "0.2.1+sr1~rc1" || id.Driver.Params["debug_regs"] != "Y" ||
		id.DCR.Channels["pwm3"] != 0x8c || id.Profile != "n5pro" || id.HWStatus.Status != "experimental" || id.UptimeS != 3600.12 || id.HWTable.Source == "" {
		t.Fatalf("identity %+v", id)
	}
	text, _, _ := captureOutput(t, func() int { return cmdIdentify(nil) })
	for _, want := range []string{"EC firmware     0.14", "hardware status experimental (N5 PRO / F8NAA / BIOS 1.05 / EC 0.14", "debug_regs=Y", "dcr             1800: 00 ff 00 55 4a 8c"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

// identify never writes: every file of the tree is unchanged afterwards.
func TestIdentifyNeverWrites(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	before := snapshotTree(t, root)
	captureOutput(t, func() int { return cmdIdentify([]string{"--json"}) })
	if after := snapshotTree(t, root); len(after) != len(before) {
		t.Fatal("file set changed")
	} else {
		for k, v := range before {
			if after[k] != v {
				t.Fatalf("%s changed", k)
			}
		}
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		out[p] = string(b)
		return nil
	})
	return out
}

// stubValidate replaces the environment checks of validate for one test:
// no daemon, no other writer, the given session; run dir in a temp dir.
func stubValidate(t *testing.T, s session) {
	t.Helper()
	oldD, oldS, oldW, oldIn := validateDaemon, validateSession, validateOtherWriters, validateStdin
	oldNow, oldSleep := validateNow, validateSleep
	t.Cleanup(func() {
		validateDaemon, validateSession, validateOtherWriters, validateStdin = oldD, oldS, oldW, oldIn
		validateNow, validateSleep = oldNow, oldSleep
	})
	validateDaemon = func() bool { return false }
	validateOtherWriters = func() []string { return nil }
	validateSession = func() session { return s }
	t.Setenv("N5FANGOV_RUN_DIR", t.TempDir())
}

var localConsole = session{stdin: "/dev/tty1", console: true}

// The safety refusals come before anything else: daemon running, piped
// input, a remote or non-console session without --i-am-at-the-console, a
// missing confirmation, another writer holding the lock.
func TestValidateRefusals(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	before := snapshotTree(t, root)
	stubValidate(t, localConsole)
	cfg := writeCfg(t, t.TempDir(), "[daemon]\nprofile = \"n5pro\"\n")

	validateDaemon = func() bool { return true }
	_, errOut, rc := captureOutput(t, func() int { return cmdValidate([]string{"--config", cfg, "pwm3"}) })
	if rc != exitFail || !strings.Contains(errOut, "daemon is running") {
		t.Fatalf("daemon: rc %d %q", rc, errOut)
	}
	validateDaemon = func() bool { return false }
	// sudo clears SSH_*: the ancestor sshd-session still makes it remote
	validateSession = func() session {
		return session{stdin: "/dev/pts/0", remote: []string{"ancestor process sshd-session (pid 900)"}}
	}
	_, errOut, rc = captureOutput(t, func() int { return cmdValidate([]string{"--config", cfg, "pwm3"}) })
	if rc != exitFail || !strings.Contains(errOut, "session is remote (ancestor process sshd-session") {
		t.Fatalf("ssh: rc %d %q", rc, errOut)
	}
	// piped confirmation: refused even with the flag
	validateSession = func() session { return session{remote: []string{"SSH_CONNECTION set"}} }
	_, errOut, rc = captureOutput(t, func() int {
		return cmdValidate([]string{"--config", cfg, "--i-am-at-the-console", "pwm3"})
	})
	if rc != exitFail || !strings.Contains(errOut, "not a terminal") {
		t.Fatalf("pipe: rc %d %q", rc, errOut)
	}
	// a pseudo terminal without a VT login in the chain needs the flag
	validateSession = func() session { return session{stdin: "/dev/pts/3"} }
	_, errOut, rc = captureOutput(t, func() int { return cmdValidate([]string{"--config", cfg, "pwm3"}) })
	if rc != exitFail || !strings.Contains(errOut, "/dev/pts/3 is not a local console") {
		t.Fatalf("pts: rc %d %q", rc, errOut)
	}
	// read-only tests need no console
	if _, errOut, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", cfg, "--tests", "T0,T6", "--boot", "cold", "--out", t.TempDir(), "pwm3"})
	}); rc != exitOK {
		t.Fatalf("T0/T6 from a pts: rc %d %q", rc, errOut)
	}
	// another writer holds the lock
	validateSession = func() session { return localConsole }
	release, err := acquireWriterLock(runDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	_, errOut, rc = captureOutput(t, func() int { return cmdValidate([]string{"--config", cfg, "pwm3"}) })
	release()
	if rc != exitFail || !strings.Contains(errOut, "another pwm writer holds the lock") || !strings.Contains(errOut, "test pid") {
		t.Fatalf("lock: rc %d %q", rc, errOut)
	}
	validateStdin = strings.NewReader("yes\n")
	out, _, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", cfg, "--i-am-at-the-console", "--out", t.TempDir(), "pwm3"})
	})
	if rc != exitFail || !strings.Contains(out, "Aborted, nothing written.") {
		t.Fatalf("confirmation: rc %d %q", rc, out)
	}
	if _, _, rc := captureOutput(t, func() int { return cmdValidate([]string{"--hold", "1s", "pwm3"}) }); rc != exitUsage {
		t.Fatalf("--hold without --dry-run: rc %d", rc)
	}
	after := snapshotTree(t, root)
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s written by a refused run", k)
		}
	}
}

// --dry-run: the whole procedure against the simulator, bundle per §8;
// the sysfs tree is only read.
func TestValidateDryRun(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	before := snapshotTree(t, root)
	out := t.TempDir()
	stdout, errOut, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--dry-run", "pwm3", "--out", out, "--boot", "cold", "--hold", "10s", "--observe", "300s", "--gate-window", "20s"}) // flags after the channel too
	})
	if rc != exitOK {
		t.Fatalf("rc %d\n%s\n%s", rc, stdout, errOut)
	}
	dirs, _ := os.ReadDir(out)
	if len(dirs) != 1 || !strings.HasSuffix(dirs[0].Name(), "-F8NAA-1.05-validate-pwm3-dryrun") {
		t.Fatalf("bundle dirs %v", dirs)
	}
	b := filepath.Join(out, dirs[0].Name())
	for _, f := range []string{"meta.json", "samples.csv", "events.log", "verdict.json", "SHA256SUMS"} {
		if _, err := os.Stat(filepath.Join(b, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(b, "verdict.json"))
	var v struct {
		Tests []struct {
			ID, Verdict string
			Reasons     []string
		}
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range v.Tests {
		got[x.ID] = x.Verdict
	}
	want := map[string]string{"T0": "PASS", "T6": "REPORTED", "T1": "PASS", "T2": "PASS", "T3": "PASS", "C1": "INCONCLUSIVE", "T4": "INCONCLUSIVE", "T5": "INCONCLUSIVE"}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %s, want %s (%s)", k, got[k], w, raw)
		}
	}
	// pwm3: the simulated EC 0x06 is 22 C, below the 34 C table step
	for _, x := range v.Tests {
		if (x.ID == "T4" || x.ID == "T5") && (len(x.Reasons) == 0 || !strings.HasPrefix(x.Reasons[0], "HDD input EC 0x06 below 34 C")) {
			t.Errorf("%s reasons %v", x.ID, x.Reasons)
		}
	}
	ev, _ := os.ReadFile(filepath.Join(b, "events.log"))
	if !strings.Contains(string(ev), "restore pwm3 after T2 ok") || !strings.Contains(string(ev), "gate G1 SKIPPED") {
		t.Fatalf("events: %s", ev)
	}
	after := snapshotTree(t, root)
	for k, val := range before {
		if after[k] != val {
			t.Fatalf("dry-run wrote %s", k)
		}
	}
}

// --dry-run pwm4: T1 runs, T2-T5 are N/A (no EC automatic on DCR5,
// TESTSPEC 0.3 §6) and T2/T3 write nothing.
func TestValidateDryRunPCIe(t *testing.T) {
	referenceN5(t)
	fakeProc(t)
	out := t.TempDir()
	stdout, errOut, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--dry-run", "pwm4", "--tests", "T1,T2,T3,T4,T5", "--out", out, "--hold", "10s", "--observe", "300s", "--gate-window", "20s"})
	})
	if rc != exitOK {
		t.Fatalf("rc %d\n%s\n%s", rc, stdout, errOut)
	}
	dirs, _ := os.ReadDir(out)
	if len(dirs) != 1 {
		t.Fatalf("bundle dirs %v", dirs)
	}
	raw, _ := os.ReadFile(filepath.Join(out, dirs[0].Name(), "verdict.json"))
	var v struct {
		Tests []struct {
			ID, Verdict string
			Reasons     []string
		}
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range v.Tests {
		got[x.ID] = x.Verdict
		if x.ID != "T1" && (len(x.Reasons) == 0 || x.Reasons[0] != "no EC automatic on DCR5 (firmware 0.14)") {
			t.Errorf("%s reasons %v", x.ID, x.Reasons)
		}
	}
	for k, w := range map[string]string{"T1": "PASS", "T2": "N/A", "T3": "N/A", "T4": "N/A", "T5": "N/A"} {
		if got[k] != w {
			t.Errorf("%s = %s, want %s (%s)", k, got[k], w, raw)
		}
	}
	ev, _ := os.ReadFile(filepath.Join(out, dirs[0].Name(), "events.log"))
	if strings.Contains(string(ev), "T2 pwm4") || strings.Contains(string(ev), "pwm4 auto (EC automatic command)") {
		t.Errorf("T2 ran on pwm4: %s", ev)
	}
}

// --replay on the 2026-09-14 logs: T2 INCONCLUSIVE, criterion met.
func TestValidateReplayCLI(t *testing.T) {
	dir := filepath.Join(hwmontest.ModuleRoot(t), "internal", "validate", "testdata", "replay-20260914")
	out, errOut, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--replay", filepath.Join(dir, "06-pwmtest-ch3-20260914-171352.txt"), filepath.Join(dir, "06-i2ec-dcr-20260914.txt")})
	})
	if rc != exitOK {
		t.Fatalf("rc %d %s", rc, errOut)
	}
	if !strings.Contains(out, `"id": "T2"`) || !strings.Contains(out, `"verdict": "INCONCLUSIVE"`) || strings.Contains(out, `"verdict": "FAIL"`) || !strings.Contains(out, `"left_written_value": true`) {
		t.Fatalf("replay output:\n%s", out)
	}
}

type msgSink struct{ kinds, msgs []string }

func (s *msgSink) Alert(kind, msg string) {
	s.kinds = append(s.kinds, kind)
	s.msgs = append(s.msgs, msg)
}
func (s *msgSink) Name() string { return "rec" }

// Gate (a) at serve: auto with a read-only N5 Pro driver alerts "driver"
// and does not start on the monitor profile.
func TestServeDeviceWriteDisabledAlerts(t *testing.T) {
	root := fakeN5(t, false)
	for _, a := range []string{"pwm1", "pwm1_enable", "pwm2", "pwm2_enable", "pwm3", "pwm3_enable", "pwm4", "pwm4_enable"} {
		if err := os.Remove(filepath.Join(root, "class", "hwmon", "hwmon14", a)); err != nil {
			t.Fatal(err)
		}
	}
	sink := &msgSink{}
	st := &serveState{rdir: t.TempDir(), alerter: sink, dspec: daemonSpec{Profile: "auto"}}
	if serveDevice(st) {
		t.Fatal("serveDevice started on a read-only driver")
	}
	if len(sink.kinds) != 1 || sink.kinds[0] != "driver" || !strings.Contains(sink.msgs[0], "read-only") {
		t.Fatalf("alerts %v %v", sink.kinds, sink.msgs)
	}
}

// G5 detection: sudo clears SSH_*, the process chain still shows sshd; a
// VT login in the chain makes a pts (sudo use_pty) a local console.
func TestDetectSession(t *testing.T) {
	root := fakeProc(t)
	stat := func(pid int, comm string, ppid, tty int) {
		d := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		line := strconv.Itoa(pid) + " (" + comm + ") S " + strconv.Itoa(ppid) + " 1 1 " + strconv.Itoa(tty) + " -1 4194560 0 0\n"
		if err := os.WriteFile(filepath.Join(d, "stat"), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pts3, tty1 := 136<<8|3, 4<<8|1
	noEnv := func(string) string { return "" }
	stat(1, "systemd", 0, 0)
	// ssh -> sudo -> n5-fangov
	stat(200, "sshd-session", 1, 0)
	stat(300, "bash", 200, pts3)
	stat(400, "sudo", 300, pts3)
	stat(500, "n5-fangov", 400, pts3)
	s := detectSession(500, noEnv, "/dev/pts/3")
	if len(s.remote) != 1 || !strings.Contains(s.remote[0], "sshd-session (pid 200)") || s.console {
		t.Fatalf("ssh+sudo: %+v", s)
	}
	// login on tty1 -> sudo (use_pty) -> n5-fangov on a pts
	stat(210, "login", 1, tty1)
	stat(310, "bash", 210, tty1)
	stat(410, "sudo", 310, tty1)
	stat(510, "n5-fangov", 410, 136<<8|7)
	if s := detectSession(510, noEnv, "/dev/pts/7"); len(s.remote) != 0 || !s.console {
		t.Fatalf("tty1+sudo: %+v", s)
	}
	if s := detectSession(510, func(k string) string {
		if k == "SSH_CONNECTION" {
			return "192.0.2.1 5 192.0.2.2 22"
		}
		return ""
	}, "/dev/pts/7"); len(s.remote) != 1 || s.remote[0] != "SSH_CONNECTION set" {
		t.Fatalf("env: %+v", s)
	}
	// PVE web console
	stat(220, "termproxy", 1, 0)
	stat(320, "login", 220, 136<<8|9)
	stat(420, "bash", 320, 136<<8|9)
	if s := detectSession(420, noEnv, "/dev/pts/9"); len(s.remote) != 1 || !strings.Contains(s.remote[0], "termproxy") || s.console {
		t.Fatalf("termproxy: %+v", s)
	}
	for nr, want := range map[int]bool{tty1: true, 4<<8 | 64: true, 5<<8 | 1: true, 229 << 8: true, pts3: false, 0: false, 4 << 8: false} {
		if localConsoleTTY(nr) != want {
			t.Errorf("localConsoleTTY(%#x) != %v", nr, want)
		}
	}
}

// Restore values: the daemon's stop, never a duty below 100.
func TestValidateStops(t *testing.T) {
	fakeN5(t, false)
	dev, err := detectDevice(hwmon.New(), "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	st := validateStops(dev, []chanSpec{{Name: "pcie", PWM: 4, Sensor: "k10temp", Curve: [][2]int{{40, 100}, {80, 255}}, Critical: 90, Stop: "80"}})
	if st[3] != "140" || st[4] != "100" {
		t.Fatalf("stops %v", st)
	}
}

// fakeClock drives liveEnv: Sleep advances the time at once.
type fakeClock struct{ now time.Time }

func (c *fakeClock) install(t *testing.T, onSleep func(n int, ctx context.Context) bool) {
	t.Helper()
	n := 0
	validateNow = func() time.Time { return c.now }
	validateSleep = func(ctx context.Context, d time.Duration) bool {
		n++
		if ctx.Err() != nil {
			return false
		}
		if onSleep != nil && !onSleep(n, ctx) {
			return false
		}
		c.now = c.now.Add(d)
		return true
	}
}

var writeEvent = regexp.MustCompile(`write pwm(\d)=(\d+)`)

// The real write path on a fake sysfs tree with the spec timing (virtual
// clock): only pwm3 is written, never below 100, and it ends in its stop
// state; meta.json carries the deviations and the restore values.
func TestValidateHardwarePathFakeSysfs(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	(&fakeClock{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}).install(t, nil)
	validateStdin = strings.NewReader(validateConfirm + "\n")
	before := snapshotTree(t, root)
	out := t.TempDir()
	stdout, errOut, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T1,T2,T3", "--boot", "cold", "--out", out, "pwm3"})
	})
	if rc != exitOK {
		t.Fatalf("rc %d\n%s\n%s", rc, stdout, errOut)
	}
	after := snapshotTree(t, root)
	for k, v := range before {
		if after[k] == v {
			continue
		}
		base := filepath.Base(k)
		if base != "pwm3" && base != "pwm3_enable" {
			t.Errorf("%s written", k)
		}
	}
	ec := filepath.Join(root, "class", "hwmon", "hwmon14")
	if b, _ := os.ReadFile(filepath.Join(ec, "pwm3")); strings.TrimSpace(string(b)) != "140" {
		t.Errorf("pwm3 ends at %q, want the stop duty 140", b)
	}
	dirs, _ := os.ReadDir(out)
	if len(dirs) != 1 {
		t.Fatalf("bundle %v", dirs)
	}
	ev, _ := os.ReadFile(filepath.Join(out, dirs[0].Name(), "events.log"))
	writes := writeEvent.FindAllStringSubmatch(string(ev), -1)
	if len(writes) < 4 {
		t.Fatalf("writes: %s", ev)
	}
	for _, w := range writes {
		d, _ := strconv.Atoi(w[2])
		if w[1] != "3" || d < 100 {
			t.Errorf("write pwm%s=%d", w[1], d)
		}
	}
	if !strings.Contains(string(ev), "restore pwm3 after T2 ok") {
		t.Errorf("restore after T2 missing: %s", ev)
	}
	var meta map[string]any
	raw, _ := os.ReadFile(filepath.Join(out, dirs[0].Name(), "meta.json"))
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	// TESTSPEC §1 rule 6 (since 0.2): the list is always present, empty while the
	// tool has no known deviation
	if devs, ok := meta["spec_deviations"].([]any); !ok || len(devs) != 0 {
		t.Errorf("spec_deviations: want an empty list, got %v", meta["spec_deviations"])
	}
	if rv, _ := meta["restore_values"].(map[string]any); rv["3"] != "140" {
		t.Errorf("restore_values %v", meta["restore_values"])
	}
	if tm, _ := meta["timing"].(map[string]any); tm["handback_observe_s"] != float64(1020) {
		t.Errorf("timing %v", meta["timing"])
	}
}

// A gate that blocks the writing tests ends with exit 1 (nothing written).
func TestValidateBlockedGateExits1(t *testing.T) {
	root := referenceN5(t)
	proc := fakeProc(t)
	if err := os.WriteFile(filepath.Join(proc, "loadavg"), []byte("3.50 2.00 1.00 1/500 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubValidate(t, localConsole)
	(&fakeClock{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}).install(t, nil)
	validateStdin = strings.NewReader(validateConfirm + "\n")
	before := snapshotTree(t, root)
	stdout, _, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T1", "--boot", "cold", "--out", t.TempDir(), "pwm3"})
	})
	if rc != exitFail || !strings.Contains(stdout, "Writing tests not run: G3 failed") {
		t.Fatalf("rc %d\n%s", rc, stdout)
	}
	after := snapshotTree(t, root)
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s written by a blocked run", k)
		}
	}
}

// pwm4 with only T2/T3 is not a writing run: T2/T3 are N/A on DCR5
// (TESTSPEC §6), so no confirmation, no gate window, nothing written.
func TestValidatePCIeHandbackOnlyIsReadOnly(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	(&fakeClock{now: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}).install(t, nil)
	validateStdin = strings.NewReader("") // a prompt would read EOF and abort
	before := snapshotTree(t, root)
	out := t.TempDir()
	stdout, _, _ := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T2,T3", "--boot", "cold", "--out", out, "pwm4"})
	})
	for _, s := range []string{"This command WRITES", "Type exactly", "sampling", "Aborted"} {
		if strings.Contains(stdout, s) {
			t.Errorf("pwm4 T2,T3 printed %q:\n%s", s, stdout)
		}
	}
	after := snapshotTree(t, root)
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s written by a pwm4 T2,T3 run", k)
		}
	}
	dirs, _ := os.ReadDir(out)
	if len(dirs) != 1 {
		t.Fatalf("bundle %v\n%s", dirs, stdout)
	}
	raw, _ := os.ReadFile(filepath.Join(out, dirs[0].Name(), "verdict.json"))
	if !strings.Contains(string(raw), "no EC automatic on DCR5") {
		t.Errorf("verdict.json without the N/A reason: %s", raw)
	}
}

// check as ExecStartPre sends the "driver" alert serve would have sent.
func TestCheckDriverAlertUnderSystemd(t *testing.T) {
	root := fakeN5(t, false)
	for _, a := range []string{"pwm1", "pwm1_enable", "pwm2", "pwm2_enable", "pwm3", "pwm3_enable", "pwm4", "pwm4_enable"} {
		if err := os.Remove(filepath.Join(root, "class", "hwmon", "hwmon14", a)); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	t.Setenv("N5FANGOV_RUN_DIR", dir)
	sink := &msgSink{}
	oldA, oldS := checkAlerter, underSystemd
	t.Cleanup(func() { checkAlerter, underSystemd = oldA, oldS })
	checkAlerter = func() alert.Sink { return sink }
	cfg := writeCfg(t, dir, "[daemon]\nprofile = \"auto\"\n")
	underSystemd = func() bool { return false }
	if _, _, rc := captureOutput(t, func() int { return cmdCheck([]string{"--quiet", "--config", cfg}) }); rc != exitFail || len(sink.kinds) != 0 {
		t.Fatalf("interactive: rc %d alerts %v", rc, sink.kinds)
	}
	underSystemd = func() bool { return true }
	for i := 0; i < 2; i++ { // second start within the cooldown: no second alert
		if _, _, rc := captureOutput(t, func() int { return cmdCheck([]string{"--quiet", "--config", cfg}) }); rc != exitFail {
			t.Fatalf("rc %d", rc)
		}
	}
	if len(sink.kinds) != 1 || sink.kinds[0] != "driver" || !strings.Contains(sink.msgs[0], "read-only") {
		t.Fatalf("alerts %v %v", sink.kinds, sink.msgs)
	}
}

// G2 finds backup processes by comm and by command line: comm is cut to 15
// characters and a PVE worker carries its task type only in its title.
func TestBackupProcess(t *testing.T) {
	for _, c := range []struct{ comm, cmdline, want string }{
		{"vzdump", "/usr/bin/perl\x00-T\x00/usr/bin/vzdump\x00101\x00", "process vzdump"},
		{"proxmox-backup-", "/usr/bin/proxmox-backup-client\x00backup\x00root.pxar:/\x00", "process proxmox-backup-client"},
		{"proxmox-backup-", "proxmox-backup-client\x00backup\x00", "process proxmox-backup-client"},
		{"task UPID:n5host", "task UPID:n5host:0001E240:0A1B2C3D:66F00000:vzdump::root@pam:\x00", "PVE task vzdump"},
		{"task UPID:n5host", "task UPID:n5host:0001E240:0A1B2C3D:66F00000:vzdump:100:root@pam:\x00", "PVE task vzdump"},
		{"task UPID:n5host", "task UPID:n5host:0001E240:0A1B2C3D:66F00000:qmstart:100:root@pam:\x00", ""},
		{"proxmox-backup-", "/usr/lib/x86_64-linux-gnu/proxmox-backup/proxmox-backup-proxy\x00", ""},
		{"bash", "bash\x00-c\x00echo vzdump\x00", ""},
		{"kworker/0:1", "", ""},
	} {
		if got := backupProcess(c.comm, c.cmdline); got != c.want {
			t.Errorf("backupProcess(%q, %q) = %q, want %q", c.comm, c.cmdline, got, c.want)
		}
	}
}

// Maintenance window 1, D1-b: --out is created with its parents before the
// gates; one that cannot be created ends the run before the confirmation,
// with nothing written to the EC.
func TestValidateOutPreparedBeforeGates(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	(&fakeClock{now: time.Date(2026, 10, 17, 12, 51, 0, 0, time.UTC)}).install(t, nil)
	before := snapshotTree(t, root)

	base := t.TempDir()
	blocker := filepath.Join(base, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	validateStdin = strings.NewReader(validateConfirm + "\n")
	stdout, errOut, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T1", "--boot", "cold", "--out", filepath.Join(blocker, "runs"), "pwm1"})
	})
	if rc != exitFail || !strings.Contains(errOut, "output directory") || strings.Contains(stdout, "Type exactly") || strings.Contains(stdout, "gate") {
		t.Fatalf("blocked --out: rc %d\n%s\n%s", rc, stdout, errOut)
	}
	after := snapshotTree(t, root)
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s written although --out was unusable", k)
		}
	}

	// missing parents: created, the bundle lands there
	out := filepath.Join(base, "n5-mw1", "runs")
	_, errOut, rc = captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T0,T6", "--boot", "cold", "--out", out, "pwm1"})
	})
	if rc != exitOK {
		t.Fatalf("nested --out: rc %d %s", rc, errOut)
	}
	if dirs, _ := os.ReadDir(out); len(dirs) != 1 {
		t.Fatalf("bundle under the new --out: %v", dirs)
	}
}
