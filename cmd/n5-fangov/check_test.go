package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
)

// fakeN5 points N5FANGOV_SYSFS at a writable clone of the N5 Pro tree,
// optionally without the drivetemp devices (no HDDs / module not loaded).
func fakeN5(t *testing.T, dropDrivetemp bool) string {
	t.Helper()
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	if dropDrivetemp {
		for _, d := range []string{"hwmon6", "hwmon7", "hwmon8", "hwmon9"} {
			if err := os.RemoveAll(filepath.Join(root, "class", "hwmon", d)); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("N5FANGOV_SYSFS", root)
	return root
}

func writeCfg(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func fatals(res []checkResult) []string {
	var out []string
	for _, r := range res {
		if !r.ok && !r.advisory {
			out = append(out, r.name+": "+r.detail)
		}
	}
	return out
}

func find(res []checkResult, name string) (checkResult, bool) {
	for _, r := range res {
		if r.name == name {
			return r, true
		}
	}
	return checkResult{}, false
}

const cpuOnlyTOML = `[daemon]
profile = "n5pro"
[[channel]]
name = "cpu"
pwm = 1
sensor = "k10temp"
curve = [[45, 85], [80, 255]]
critical = 88
stop = "auto"
`

// Polish M1b: check evaluates the sanitized channel set; a built-in channel
// whose sensor cannot be resolved is a warning (serve isolates it), not a
// failed ExecStartPre.
func TestCheckUnresolvableBuiltinSensorIsAdvisory(t *testing.T) {
	fakeN5(t, true)
	dir := t.TempDir()
	res := runChecks(writeCfg(t, dir, cpuOnlyTOML), dir)
	if f := fatals(res); len(f) != 0 {
		t.Fatalf("fatal results: %v", f)
	}
	r, ok := find(res, "sensor drivetemp:max")
	if !ok || r.ok || !r.advisory {
		t.Fatalf("drivetemp result: %+v (found %v)", r, ok)
	}
	for _, want := range []string{`channel "hdd"`, "built-in channel", "duty 140", "sensor-error", "other channels regulate"} {
		if !strings.Contains(r.detail, want) {
			t.Errorf("detail lacks %q: %s", want, r.detail)
		}
	}
	// the added channels are listed (pwm writable) and the correction noted
	for _, name := range []string{"channel cpu", "channel ssd", "channel hdd"} {
		if r, ok := find(res, name); !ok || !r.ok {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if r, ok := find(res, "channels"); !ok || r.ok || !r.advisory || !strings.Contains(r.detail, "pwm2 (ssd) not in config") {
		t.Errorf("sanitizer note: %+v", r)
	}
	if r, ok := find(res, "sensor k10temp"); !ok || !r.ok {
		t.Errorf("k10temp: %+v", r)
	}
	// with drivetemp present everything resolves
	fakeN5(t, false)
	res = runChecks(writeCfg(t, dir, cpuOnlyTOML), dir)
	if r, ok := find(res, "sensor drivetemp:max"); !ok || !r.ok {
		t.Errorf("drivetemp present: %+v", r)
	}
	// a configured channel with an unresolvable sensor is advisory too
	res = runChecks(writeCfg(t, dir, strings.Replace(cpuOnlyTOML, "k10temp", "coretemp", 1)), dir)
	if f := fatals(res); len(f) != 0 {
		t.Errorf("fatal results: %v", f)
	}
	if r, ok := find(res, "sensor coretemp"); !ok || r.ok || !r.advisory || !strings.Contains(r.detail, `channel "cpu" stays at duty 255`) {
		t.Errorf("coretemp: %+v", r)
	}
}

// DESIGN rule 8 in check: only what stops serve is fatal.
func TestCheckConfigSeverity(t *testing.T) {
	fakeN5(t, false)
	dir := t.TempDir()

	// TOML syntax error: advisory, the remaining checks still run
	res := runChecks(writeCfg(t, dir, "[daemon\ninterval = 1"), dir)
	if f := fatals(res); len(f) != 0 {
		t.Errorf("syntax error must not be fatal: %v", f)
	}
	if r, ok := find(res, "config"); !ok || r.ok || !r.advisory || !strings.Contains(r.detail, "syntax error") {
		t.Errorf("config result: %+v", r)
	}
	if _, ok := find(res, "profile"); !ok {
		t.Errorf("checks stopped after the config error")
	}

	// missing file: advisory
	res = runChecks(filepath.Join(dir, "nope.toml"), dir)
	if f := fatals(res); len(f) != 0 {
		t.Errorf("missing file must not be fatal: %v", f)
	}

	// unreadable (a directory in its place): fatal
	res = runChecks(dir, dir)
	if f := fatals(res); len(f) != 1 || !strings.HasPrefix(f[0], "config:") {
		t.Errorf("unreadable config must be the one fatal: %v", f)
	}

	// a pwm the profile lacks: advisory (serve ignores the channel)
	res = runChecks(writeCfg(t, dir, cpuOnlyTOML+"\n[[channel]]\nname = \"x\"\npwm = 7\nsensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\n"), dir)
	if f := fatals(res); len(f) != 0 {
		t.Errorf("unexposed pwm must not be fatal: %v", f)
	}
	if r, ok := find(res, "channel x"); !ok || r.ok || !r.advisory {
		t.Errorf("channel x: %+v", r)
	}

	// no device for the requested profile: fatal
	res = runChecks(writeCfg(t, dir, strings.Replace(cpuOnlyTOML, "n5pro", "nct67xx", 1)), dir)
	if f := fatals(res); len(f) != 1 || !strings.HasPrefix(f[0], "profile:") {
		t.Errorf("undetected profile: %v", f)
	}
}

// Polish 7: a network-reachable listener without auth is reported.
func TestCheckWebAdvisory(t *testing.T) {
	fakeN5(t, false)
	dir := t.TempDir()
	res := runChecks(writeCfg(t, dir, "[web]\nlisten = \"0.0.0.0:8010\"\nauth = \"none\"\n"+cpuOnlyTOML), dir)
	if f := fatals(res); len(f) != 0 {
		t.Errorf("fatal: %v", f)
	}
	if r, ok := find(res, "web"); !ok || r.ok || !r.advisory || !strings.Contains(r.detail, "0.0.0.0:8010") || !strings.Contains(r.detail, "auth") {
		t.Errorf("web result: %+v", r)
	}
	res = runChecks(writeCfg(t, dir, "[web]\nlisten = \"127.0.0.1:8010\"\n"+cpuOnlyTOML), dir)
	if r, ok := find(res, "web"); !ok || !r.ok {
		t.Errorf("loopback: %+v", r)
	}
}

// serve's independent watchdog pings stop when the loop is stuck.
func TestWatchdogAlive(t *testing.T) {
	base := time.Now()
	s := time.Second
	if watchdogAlive(time.Time{}, 10*s, base) {
		t.Errorf("no cycle yet must not count as alive")
	}
	if !watchdogAlive(base.Add(-25*s), 10*s, base) {
		t.Errorf("25 s after the last cycle at interval 10 s is alive (slack 3x)")
	}
	if watchdogAlive(base.Add(-31*s), 10*s, base) {
		t.Errorf("31 s after the last cycle at interval 10 s is stuck")
	}
	if !watchdogAlive(base.Add(-89*s), 30*s, base) || watchdogAlive(base.Add(-91*s), 30*s, base) {
		t.Errorf("slack must scale with the interval")
	}
	if watchdogPing >= 30*s || watchdogPing < 5*s {
		t.Errorf("watchdogPing %s must sit well inside WatchdogSec=60", watchdogPing)
	}
}

// ---------------------------------------------------------------------------
// check --after-update: kernel module scan on a temp tree

func mkKernel(t *testing.T, root, name string, withBuild bool, module string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if withBuild {
		if err := os.MkdirAll(filepath.Join(dir, "build"), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "modules.dep"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if module != "" {
		mdir := filepath.Join(dir, "updates", "dkms")
		if err := os.MkdirAll(mdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mdir, module), []byte("elf"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanKernelModules(t *testing.T) {
	root := t.TempDir()
	mkKernel(t, root, "6.14.8-2-pve", true, dkmsKernelObject)
	mkKernel(t, root, "6.17.2-1-pve", false, dkmsKernelObject+".zst") // compressed counts
	mkKernel(t, root, "6.17.4-1-pve", true, "")                       // fresh kernel, no module
	mkKernel(t, root, "6.17.4-2-pve", false, "")                      // headers gone, modules.dep present
	// leftovers that are not kernels: an empty dir, a file
	if err := os.MkdirAll(filepath.Join(root, "6.11.0-old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	kernels, missing, err := scanKernelModules(root, dkmsKernelObject)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(kernels, " ") != "6.14.8-2-pve 6.17.2-1-pve 6.17.4-1-pve 6.17.4-2-pve" {
		t.Errorf("kernels: %v", kernels)
	}
	if strings.Join(missing, " ") != "6.17.4-1-pve 6.17.4-2-pve" {
		t.Errorf("missing: %v", missing)
	}
	line := kernelMissingLine("6.17.4-1-pve", "0.2.0")
	if line != "kernel 6.17.4-1-pve: fan driver module missing - run: dkms install minisforum-n5-it5571/0.2.0 -k 6.17.4-1-pve" {
		t.Errorf("line: %s", line)
	}
	if _, _, err := scanKernelModules(filepath.Join(root, "nope"), dkmsKernelObject); err == nil {
		t.Errorf("missing root must error")
	}
	empty := t.TempDir()
	if k, m, err := scanKernelModules(empty, dkmsKernelObject); err != nil || len(k) != 0 || len(m) != 0 {
		t.Errorf("empty root: %v %v %v", k, m, err)
	}
}

// Only kernels the box can boot into count. With proxmox-boot-tool
// present its manual/automatic/pinned selection plus the running kernel
// are relevant; without it the running one plus the newest installed.
// Installed-but-unselected kernels are info only.
func TestRelevantKernels(t *testing.T) {
	installed := []string{"6.14.8-2-pve", "6.17.2-1-pve", "6.17.4-1-pve", "6.17.4-2-pve", "6.8.12-9-pve"}
	toolOut := `Manually selected kernels:
None.

Automatically selected kernels:
6.17.4-2-pve
6.17.4-1-pve

Pinned kernel:
6.14.8-2-pve
`
	rel, source := relevantKernels("6.17.2-1-pve", installed, toolOut, nil)
	want := map[string]bool{"6.17.2-1-pve": true, "6.17.4-2-pve": true, "6.17.4-1-pve": true, "6.14.8-2-pve": true}
	if fmt.Sprint(rel) != fmt.Sprint(want) || !strings.Contains(source, "proxmox-boot-tool") {
		t.Errorf("with tool: %v (%s)", rel, source)
	}
	if rel["6.8.12-9-pve"] {
		t.Error("old installed kernel counted as relevant")
	}
	// a listed kernel that is not installed under /lib/modules is ignored
	rel, _ = relevantKernels("6.17.2-1-pve", installed, "Automatically selected kernels:\n7.0.0-1-pve\n6.17.4-2-pve\n", nil)
	if rel["7.0.0-1-pve"] || !rel["6.17.4-2-pve"] {
		t.Errorf("uninstalled listed kernel: %v", rel)
	}
	// tool absent: running + newest installed (numeric order, not lexical:
	// 6.17 > 6.8)
	rel, source = relevantKernels("6.14.8-2-pve", installed, "", errors.New("exec: not found"))
	if fmt.Sprint(rel) != fmt.Sprint(map[string]bool{"6.14.8-2-pve": true, "6.17.4-2-pve": true}) || !strings.Contains(source, "newest") {
		t.Errorf("without tool: %v (%s)", rel, source)
	}
	// tool present but lists nothing usable: same fallback
	rel, source = relevantKernels("6.14.8-2-pve", installed, "Manually selected kernels:\nNone.\n\nAutomatically selected kernels:\nNone.\n", nil)
	if !rel["6.17.4-2-pve"] || len(rel) != 2 || !strings.Contains(source, "newest") {
		t.Errorf("empty tool output: %v (%s)", rel, source)
	}
	// running kernel unknown: newest only
	rel, _ = relevantKernels("", installed, "", errors.New("x"))
	if len(rel) != 1 || !rel["6.17.4-2-pve"] {
		t.Errorf("no running kernel: %v", rel)
	}
	if got := parseBootToolKernels(toolOut); strings.Join(got, " ") != "6.17.4-2-pve 6.17.4-1-pve 6.14.8-2-pve" {
		t.Errorf("parse: %v", got)
	}
	// unknown sections are not harvested
	if got := parseBootToolKernels("Kernels in /boot:\n6.1.0-x\n\nAutomatically selected kernels:\n6.2.0-y\n"); strings.Join(got, " ") != "6.2.0-y" {
		t.Errorf("foreign section harvested: %v", got)
	}
	for _, c := range [][2]string{{"6.8.12-9-pve", "6.17.4-1-pve"}, {"6.17.4-1-pve", "6.17.4-2-pve"}, {"6.17.4-2-pve", "6.17.10-1-pve"}, {"6.17-pve", "6.17.0-1-pve"}} {
		if !kernelLess(c[0], c[1]) || kernelLess(c[1], c[0]) {
			t.Errorf("kernelLess(%s, %s)", c[0], c[1])
		}
	}
	if kernelLess("6.17.4-2-pve", "6.17.4-2-pve") {
		t.Error("kernelLess equal")
	}
	if l := kernelMissingInfo("6.8.12-9-pve", "0.2.0"); !strings.Contains(l, "info only") || !strings.Contains(l, "dkms install minisforum-n5-it5571/0.2.0 -k 6.8.12-9-pve") {
		t.Errorf("info line: %s", l)
	}
	// the func vars run real commands by default; on the test host uname
	// works and the tool is absent or works — both paths must not panic
	_ = runningKernel()
	_, _ = bootToolKernelList()
}

func TestWantsN5ProAndDKMSVersion(t *testing.T) {
	cases := []struct {
		profile, detected string
		src, want         bool
	}{
		{"n5pro", "", false, true},
		{"n5pro", "nct67xx", false, true},
		{"auto", "n5pro", false, true},
		{"auto", "", true, true},
		{"auto", "", false, false},
		{"auto", "monitor", false, false},
		{"", "n5pro", false, true},
		{"nct67xx", "n5pro", true, false},
		{"monitor", "", true, false},
	}
	for _, c := range cases {
		if got := wantsN5Pro(c.profile, c.detected, c.src); got != c.want {
			t.Errorf("wantsN5Pro(%q,%q,%v) = %v", c.profile, c.detected, c.src, got)
		}
	}
	src := t.TempDir()
	if v := dkmsSourceVersion(src); v != "" {
		t.Errorf("no source: %q", v)
	}
	for _, d := range []string{dkmsPackage + "-0.2.0", dkmsPackage + "-0.10.1", dkmsPackage + "-0.9.0", "other-1.0"} {
		if err := os.Mkdir(filepath.Join(src, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if v := dkmsSourceVersion(src); v != "0.10.1" {
		t.Errorf("newest source version: %q", v)
	}
	if dkmsSourceVersion(filepath.Join(src, "nope")) != "" {
		t.Errorf("missing root")
	}
}

// TestCheckLegacyHashAdvisory: a legacy sha256 hash under auth = basic is
// an advisory line pointing at passwd; a PBKDF2 hash is not.
func TestCheckLegacyHashAdvisory(t *testing.T) {
	fakeN5(t, false)
	dir := t.TempDir()
	legacy := strings.Repeat("ab", 32)
	cfg := writeCfg(t, dir, "[web]\nlisten = \"127.0.0.1:8010\"\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \""+legacy+"\"\n")
	res := runChecks(cfg, dir)
	r, ok := find(res, "web auth")
	if !ok || r.ok || !r.advisory || !strings.Contains(r.detail, "n5-fangov passwd") {
		t.Fatalf("legacy advisory = %+v (found %v)", r, ok)
	}
	if f := fatals(res); len(f) != 0 {
		t.Fatalf("legacy hash must not be fatal: %v", f)
	}
	cfg = writeCfg(t, dir, "[web]\nlisten = \"127.0.0.1:8010\"\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \""+passwordHash("admin", "longenough")+"\"\n")
	if _, ok := find(runChecks(cfg, dir), "web auth"); ok {
		t.Fatal("pbkdf2 hash reported as legacy")
	}
}

// A critical above the sensor's built-in ceiling is accepted, but check
// says that the ceiling acts first (advisory, exit 0).
func TestCheckCriticalAboveCeilingAdvisory(t *testing.T) {
	fakeN5(t, false)
	dir := t.TempDir()
	hdd := cpuOnlyTOML + "\n[[channel]]\nname = \"hdd\"\npwm = 3\nsensor = \"drivetemp:max\"\ncurve = [[36, 105], [46, 255]]\ncritical = 70\nstop = 140\n"
	res := runChecks(writeCfg(t, dir, hdd), dir)
	if f := fatals(res); len(f) != 0 {
		t.Fatalf("fatal results: %v", f)
	}
	var lines []string
	for _, r := range res {
		if r.name == "channel hdd" && !r.ok {
			lines = append(lines, r.detail)
			if !r.advisory {
				t.Errorf("must be advisory: %+v", r)
			}
		}
	}
	if len(lines) != 1 || lines[0] != "critical 70 above the built-in ceiling 65 \u2014 the ceiling acts first" {
		t.Errorf("ceiling advisory: %q", lines)
	}
	// cpu (k10temp, ceiling 100) with critical 88 gets no such line
	for _, r := range res {
		if r.name == "channel cpu" && strings.Contains(r.detail, "ceiling") {
			t.Errorf("cpu flagged: %+v", r)
		}
	}
	// at or below the ceiling: no line
	res = runChecks(writeCfg(t, dir, strings.Replace(hdd, "critical = 70", "critical = 65", 1)), dir)
	for _, r := range res {
		if strings.Contains(r.detail, "ceiling") && r.name != "emergency hook" {
			t.Errorf("flagged at the ceiling: %+v", r)
		}
	}
	// a configured ceiling below critical is the same finding
	res = runChecks(writeCfg(t, dir, strings.Replace(hdd, "critical = 70", "critical = 55\nceiling = 50", 1)), dir)
	lines = nil
	for _, r := range res {
		if r.name == "channel hdd" && !r.ok {
			lines = append(lines, r.detail)
		}
	}
	if len(lines) != 1 || lines[0] != "critical 55 above the configured ceiling 50 \u2014 the ceiling acts first" {
		t.Errorf("configured ceiling advisory: %q", lines)
	}
}

// TestCheckEmergencyHookLine: check reports the hook path's state; absent
// or refused is a warning only with [daemon] emergency = true.
func TestCheckEmergencyHookLine(t *testing.T) {
	fakeN5(t, false)
	dir := t.TempDir()
	hook := filepath.Join(dir, "emergency.sh")
	emergencyHookPath = hook
	t.Cleanup(func() { emergencyHookPath = control.DefaultEmergencyHook })
	find := func(res []checkResult) checkResult {
		for _, r := range res {
			if r.name == "emergency hook" {
				return r
			}
		}
		t.Fatalf("no emergency hook line in %+v", res)
		return checkResult{}
	}
	// off + absent: ok line
	r := find(runChecks(writeCfg(t, dir, cpuOnlyTOML), dir))
	if !r.ok || r.detail != hook+" (absent)" {
		t.Errorf("off+absent: %+v", r)
	}
	// on + absent: advisory warning, still no fatal
	res := runChecks(writeCfg(t, dir, strings.Replace(cpuOnlyTOML, "[daemon]\n", "[daemon]\nemergency = true\n", 1)), dir)
	if f := fatals(res); len(f) != 0 {
		t.Fatalf("fatal results: %v", f)
	}
	if r = find(res); r.ok || !r.advisory || !strings.HasPrefix(r.detail, hook+" (absent); emergency = true") {
		t.Errorf("on+absent: %+v", r)
	}
	// on + world-writable: refused
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o777); err != nil {
		t.Fatal(err)
	}
	if r = find(runChecks(writeCfg(t, dir, strings.Replace(cpuOnlyTOML, "[daemon]\n", "[daemon]\nemergency = true\n", 1)), dir)); r.ok || !strings.Contains(r.detail, "(refused: world-writable (mode 0777))") {
		t.Errorf("world-writable: %+v", r)
	}
	// 0750: ok when root owns it (the test user may not be root)
	if err := os.Chmod(hook, 0o750); err != nil {
		t.Fatal(err)
	}
	r = find(runChecks(writeCfg(t, dir, strings.Replace(cpuOnlyTOML, "[daemon]\n", "[daemon]\nemergency = true\n", 1)), dir))
	if os.Getuid() == 0 {
		if !r.ok || r.detail != hook+" (ok)" {
			t.Errorf("root 0750: %+v", r)
		}
	} else if r.ok || !strings.Contains(r.detail, "refused: not owned by root") {
		t.Errorf("non-root 0750: %+v", r)
	}
}
