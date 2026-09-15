package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
