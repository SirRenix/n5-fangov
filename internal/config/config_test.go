package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const good = `
[daemon]
interval = "10s"
step_up = 40
step_down = 15
stall_min_duty = 60
stall_cycles = 2
stale_cycles = 18
alert_cooldown = "30m"
log_every = 30
profile = "n5pro"

[web]
listen = "127.0.0.1:8010"
auth = "none"

[[channel]]
name = "cpu"
pwm = 1
sensor = "k10temp"
curve = [[45,85],[80,255]]
critical = 88
stop = "auto"

[[channel]]
name = "hdd"
pwm = 3
sensor = "drivetemp:max"
curve = [[36,105],[46,255]]
critical = 56
stop = 140
`

func hasWarn(t *testing.T, warns []Warning, field string) {
	t.Helper()
	for _, w := range warns {
		if w.Field == field {
			return
		}
	}
	t.Errorf("expected warning for %q, got %v", field, warns)
}

func TestDefault(t *testing.T) {
	d := Default()
	if d.Daemon.Interval != 10*time.Second || d.Daemon.StepUp != 40 || d.Daemon.StepDown != 15 ||
		d.Daemon.StallMinDuty != 60 || d.Daemon.StallCycles != 2 || d.Daemon.StaleCycles != 18 ||
		d.Daemon.AlertCooldown != 30*time.Minute || d.Daemon.LogEvery != 30 || d.Daemon.Profile != "auto" {
		t.Errorf("unexpected daemon defaults: %+v", d.Daemon)
	}
	if d.Web.Listen != "127.0.0.1:8010" || d.Web.Auth != "none" {
		t.Errorf("unexpected web defaults: %+v", d.Web)
	}
	if len(d.Channels) != 0 {
		t.Errorf("Default() must not carry channels")
	}
	if len(N5ProChannels()) != 3 {
		t.Errorf("N5ProChannels")
	}
}

func TestParseGood(t *testing.T) {
	cfg, warns, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if cfg.Daemon.Profile != "n5pro" || cfg.Daemon.Interval != 10*time.Second {
		t.Errorf("daemon: %+v", cfg.Daemon)
	}
	if len(cfg.Channels) != 2 {
		t.Fatalf("channels: %+v", cfg.Channels)
	}
	hdd := cfg.Channel("hdd")
	if hdd == nil || hdd.Stop != "140" || hdd.Critical != 56 || hdd.PWM != 3 {
		t.Errorf("hdd: %+v", hdd)
	}
	if d, ok := hdd.StopDuty(); !ok || d != 140 {
		t.Errorf("StopDuty: %d %v", d, ok)
	}
	if _, ok := cfg.Channel("cpu").StopDuty(); ok {
		t.Errorf("cpu stop must be auto")
	}
	if !reflect.DeepEqual(cfg.Channel("cpu").Curve, [][2]int{{45, 85}, {80, 255}}) {
		t.Errorf("curve: %v", cfg.Channel("cpu").Curve)
	}
}

func TestParseEmpty(t *testing.T) {
	cfg, warns, err := Parse(nil)
	if err != nil || len(warns) != 0 {
		t.Fatalf("empty: %v %v", warns, err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("empty config must equal Default()")
	}
}

func TestSyntaxError(t *testing.T) {
	cfg, warns, err := Parse([]byte("[daemon\ninterval = 1"))
	if err == nil {
		t.Fatal("expected syntax error")
	}
	if len(warns) != 1 || warns[0].Field != "toml" {
		t.Errorf("warnings: %v", warns)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("syntax error must return Default()")
	}
}

func TestDaemonWarnings(t *testing.T) {
	src := `
[daemon]
interval = "1s"
step_up = 0
step_down = 256
stall_min_duty = "x"
stall_cycles = 21
stale_cycles = 2
alert_cooldown = "abc"
log_every = -1
profile = "foo"
bogus = 1
[web]
listen = "nope"
auth = "digest"
`
	cfg, warns, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"daemon.interval", "daemon.step_up", "daemon.step_down", "daemon.stall_min_duty",
		"daemon.stall_cycles", "daemon.stale_cycles", "daemon.alert_cooldown", "daemon.log_every",
		"daemon.profile", "daemon.bogus", "web.listen", "web.auth"} {
		hasWarn(t, warns, f)
	}
	if !reflect.DeepEqual(cfg.Daemon, Default().Daemon) {
		t.Errorf("all-invalid daemon must equal defaults: %+v", cfg.Daemon)
	}
	if !reflect.DeepEqual(cfg.Web, Default().Web) {
		t.Errorf("all-invalid web must equal defaults: %+v", cfg.Web)
	}
}

func TestDurationAsInteger(t *testing.T) {
	cfg, warns, _ := Parse([]byte("[daemon]\ninterval = 5\nalert_cooldown = 60\n"))
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
	if cfg.Daemon.Interval != 5*time.Second || cfg.Daemon.AlertCooldown != time.Minute {
		t.Errorf("integer seconds: %+v", cfg.Daemon)
	}
}

func TestWebBasicAuth(t *testing.T) {
	cfg, warns, _ := Parse([]byte("[web]\nauth = \"basic\"\nuser = \"admin\"\n"))
	hasWarn(t, warns, "web.auth")
	if cfg.Web.Auth != "none" {
		t.Errorf("basic without hash must fall back to none")
	}
	cfg, warns, _ = Parse([]byte("[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"zz\"\n"))
	hasWarn(t, warns, "web.password_hash")
	if cfg.Web.Auth != "none" {
		t.Errorf("bad hash must fall back to none")
	}
	h := strings.Repeat("ab", 32)
	cfg, warns, _ = Parse([]byte("[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"" + h + "\"\n"))
	if len(warns) != 0 || cfg.Web.Auth != "basic" || cfg.Web.User != "admin" {
		t.Errorf("valid basic: %+v %v", cfg.Web, warns)
	}
}

func TestChannelWarnings(t *testing.T) {
	src := `
[[channel]]
name = "CPU"
pwm = 1
sensor = "k10temp"

[[channel]]
name = "a"
pwm = 9
sensor = "k10temp"

[[channel]]
name = "b"
pwm = 1
sensor = ""

[[channel]]
name = "c"
pwm = 1
sensor = "k10temp"
curve = [[45,85],[80,255]]
critical = 88

[[channel]]
name = "c"
pwm = 2
sensor = "k10temp"

[[channel]]
name = "d"
pwm = 1
sensor = "k10temp"

[[channel]]
name = "e"
pwm = 2
sensor = "nvme:max"
curve = [[50,100],[40,255]]
critical = 30
stop = "fast"
extra = true

[[channel]]
name = "f"
pwm = 3
sensor = "x"
curve = [[10,10]]
critical = 300
stop = 999
`
	cfg, warns, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	hasWarn(t, warns, "channel[0].name")    // uppercase
	hasWarn(t, warns, "channel.a.pwm")      // 9 > 8
	hasWarn(t, warns, "channel.b.sensor")   // empty
	hasWarn(t, warns, "channel.c.name")     // duplicate
	hasWarn(t, warns, "channel.d.pwm")      // duplicate pwm 1
	hasWarn(t, warns, "channel.e.curve")    // temps not ascending
	hasWarn(t, warns, "channel.e.critical") // 30 <= last curve temp of default curve
	hasWarn(t, warns, "channel.e.stop")
	hasWarn(t, warns, "channel.e.extra")
	hasWarn(t, warns, "channel.f.curve") // one point
	hasWarn(t, warns, "channel.f.critical")
	hasWarn(t, warns, "channel.f.stop")

	if len(cfg.Channels) != 3 {
		t.Fatalf("expected c, e, f kept; got %+v", cfg.Channels)
	}
	e := cfg.Channel("e")
	if !reflect.DeepEqual(e.Curve, DefaultCurve()) || e.Critical != 90 || e.Stop != "auto" {
		t.Errorf("e defaults: %+v", e)
	}
	f := cfg.Channel("f")
	if f.Critical != 90 || f.Stop != "auto" {
		t.Errorf("f defaults: %+v", f)
	}
}

func TestCurveValidation(t *testing.T) {
	bad := [][][]int64{
		{{45, 85}}, // too few
		{{1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 5}, {6, 6}, {7, 7}, {8, 8}, {9, 9}}, // too many
		{{45, 85, 1}, {80, 255}}, // triple
		{{-30, 85}, {80, 255}},   // temp low
		{{45, 85}, {121, 255}},   // temp high
		{{45, -1}, {80, 255}},    // duty low
		{{45, 85}, {80, 256}},    // duty high
		{{45, 85}, {45, 255}},    // equal temps
		{{45, 200}, {80, 100}},   // duty decreasing
	}
	for i, b := range bad {
		if _, err := ValidateCurve(b); err == nil {
			t.Errorf("case %d should fail: %v", i, b)
		}
	}
	okc, err := ValidateCurve([][]int64{{30, 0}, {40, 0}, {60, 128}, {80, 255}})
	if err != nil || len(okc) != 4 {
		t.Errorf("valid curve rejected: %v", err)
	}
}

func TestCriticalMustExceedCurve(t *testing.T) {
	cfg, warns, _ := Parse([]byte("[[channel]]\nname=\"x\"\npwm=1\nsensor=\"s\"\ncurve=[[40,80],[70,255]]\ncritical=70\n"))
	hasWarn(t, warns, "channel.x.critical")
	if cfg.Channels[0].Critical != 80 {
		t.Errorf("default critical = last+10, got %d", cfg.Channels[0].Critical)
	}
}

func TestStructuralErrors(t *testing.T) {
	_, warns, err := Parse([]byte("daemon = 1\nweb = \"x\"\n[channel]\nname = \"a\"\n"))
	if err != nil {
		t.Fatalf("structural problems must not be errors: %v", err)
	}
	hasWarn(t, warns, "daemon")
	hasWarn(t, warns, "web")
	hasWarn(t, warns, "channel")
	_, warns, _ = Parse([]byte("[unknown]\nx = 1\n"))
	hasWarn(t, warns, "unknown")
}

func TestRoundTrip(t *testing.T) {
	cfg, _, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Auth = "basic"
	cfg.Web.User = "admin"
	cfg.Web.PasswordHash = strings.Repeat("0", 64)
	raw := Marshal(cfg)
	t.Logf("marshal:\n%s", raw)
	back, warns, err := Parse(raw)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, raw)
	}
	if len(warns) != 0 {
		t.Errorf("re-parse warnings: %v\n%s", warns, raw)
	}
	if !reflect.DeepEqual(cfg, back) {
		t.Errorf("round trip mismatch:\n%+v\n%+v\n%s", cfg, back, raw)
	}
	d := Default()
	back, warns, err = Parse(Marshal(d))
	if err != nil || len(warns) != 0 || !reflect.DeepEqual(d, back) {
		t.Errorf("default round trip: %v %v\n%+v", err, warns, back)
	}
}

func TestLoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.toml")
	cfg, warns, err := Load(path)
	if err != nil || len(warns) != 1 || warns[0].Field != "file" || !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("missing file: %v %v", warns, err)
	}
	if err := Save(path, []byte(good)); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
	cfg, warns, err = Load(path)
	if err != nil || len(warns) != 0 || len(cfg.Channels) != 2 {
		t.Fatalf("load: %v %v", warns, err)
	}
	if err := Save(path, []byte("[[[")); err != nil {
		t.Fatal(err)
	}
	cfg, warns, err = Load(path)
	if err == nil || len(warns) != 1 || !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("syntax error via Load: %v %v", warns, err)
	}
}

func TestPresets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	if got := LoadPresets(dir); len(got) != 0 {
		t.Errorf("missing dir must give empty map")
	}
	chans := N5ProChannels()
	if err := SavePreset(dir, "quiet", chans); err != nil {
		t.Fatal(err)
	}
	if err := SavePreset(dir, "Bad Name", chans); err == nil {
		t.Errorf("invalid preset name accepted")
	}
	// a preset file must contain only [[channel]] tables
	raw, _ := os.ReadFile(filepath.Join(dir, "quiet.toml"))
	if strings.Contains(string(raw), "[daemon]") || !strings.Contains(string(raw), "[[channel]]") {
		t.Errorf("preset format:\n%s", raw)
	}
	// junk file is skipped
	_ = os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("[[["), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	got := LoadPresets(dir)
	if len(got) != 1 || !reflect.DeepEqual(got["quiet"], chans) {
		t.Errorf("LoadPresets: %+v", got)
	}
	if names := PresetNames(dir); !reflect.DeepEqual(names, []string{"broken", "quiet"}) {
		t.Errorf("PresetNames: %v", names)
	}
	if _, _, err := LoadPreset(dir, "broken"); err == nil {
		t.Errorf("broken preset must error")
	}
	if err := DeletePreset(dir, "quiet"); err != nil {
		t.Fatal(err)
	}
	if got := LoadPresets(dir); len(got) != 0 {
		t.Errorf("after delete: %+v", got)
	}
}

func TestClone(t *testing.T) {
	cfg := Default()
	cfg.Channels = N5ProChannels()
	c2 := cfg.Clone()
	c2.Channels[0].Curve[0][1] = 1
	if cfg.Channels[0].Curve[0][1] == 1 {
		t.Errorf("Clone shares curve storage")
	}
}
