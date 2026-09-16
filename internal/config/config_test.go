package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
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
	// M3: the PBKDF2 form is accepted as well
	p := "pbkdf2$210000$" + strings.Repeat("0f", 16) + "$" + strings.Repeat("ab", 32)
	cfg, warns, _ = Parse([]byte("[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"" + p + "\"\n"))
	if len(warns) != 0 || cfg.Web.Auth != "basic" || cfg.Web.PasswordHash != p {
		t.Errorf("valid pbkdf2: %+v %v", cfg.Web, warns)
	}
}

// M3: both stored hash forms parse; malformed ones are rejected with a
// reason.
func TestParsePasswordHash(t *testing.T) {
	legacy, err := ParsePasswordHash(strings.Repeat("ab", 32))
	if err != nil || len(legacy.Legacy) != 32 || legacy.Iter != 0 {
		t.Errorf("legacy: %+v %v", legacy, err)
	}
	ph, err := ParsePasswordHash(" pbkdf2$210000$" + strings.Repeat("0f", 16) + "$" + strings.Repeat("ab", 32) + " ")
	if err != nil || ph.Legacy != nil || ph.Iter != 210000 || len(ph.Salt) != 16 || len(ph.Key) != 32 {
		t.Errorf("pbkdf2: %+v %v", ph, err)
	}
	bad := []string{
		"",
		"zz",
		strings.Repeat("g", 64),
		strings.Repeat("ab", 31),
		"pbkdf2$210000$" + strings.Repeat("0f", 16),
		"pbkdf2$abc$" + strings.Repeat("0f", 16) + "$" + strings.Repeat("ab", 32),
		"pbkdf2$10$" + strings.Repeat("0f", 16) + "$" + strings.Repeat("ab", 32),
		"pbkdf2$210000$zz$" + strings.Repeat("ab", 32),
		"pbkdf2$210000$0f$" + strings.Repeat("ab", 32),
		"pbkdf2$210000$" + strings.Repeat("0f", 16) + "$" + strings.Repeat("ab", 16),
		"pbkdf2$210000$" + strings.Repeat("0f", 16) + "$" + strings.Repeat("ab", 32) + "$x",
		"scrypt$1$2$3",
	}
	for _, s := range bad {
		if _, err := ParsePasswordHash(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

// TestWebAuthFailOpen (H2): a broken auth setting must never leave the API
// reachable from the network without auth — listen falls back to loopback.
func TestWebAuthFailOpen(t *testing.T) {
	cases := map[string]string{
		"basic without hash":  "[web]\nlisten = \"0.0.0.0:8010\"\nauth = \"basic\"\nuser = \"admin\"\n",
		"basic with bad hash": "[web]\nlisten = \"198.51.100.20:8010\"\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"zz\"\n",
		"typo in auth":        "[web]\nlisten = \"[::]:8010\"\nauth = \"basci\"\nuser = \"admin\"\npassword_hash = \"" + strings.Repeat("ab", 32) + "\"\n",
	}
	for name, src := range cases {
		cfg, warns, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cfg.Web.Auth != "none" {
			t.Errorf("%s: auth = %q", name, cfg.Web.Auth)
		}
		if cfg.Web.Listen != "127.0.0.1:8010" {
			t.Errorf("%s: listen = %q, want loopback", name, cfg.Web.Listen)
		}
		found := false
		for _, w := range warns {
			if w.Field == "web.listen" && strings.Contains(w.Msg, "auth misconfigured") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no loopback warning in %v", name, warns)
		}
	}
	// auth = "none" on a LAN address is allowed (warned at startup, not here).
	cfg, warns, _ := Parse([]byte("[web]\nlisten = \"0.0.0.0:8010\"\nauth = \"none\"\n"))
	if cfg.Web.Listen != "0.0.0.0:8010" || len(warns) != 0 {
		t.Errorf("explicit none must keep listen: %+v %v", cfg.Web, warns)
	}
	// valid basic on a LAN address keeps listen.
	cfg, warns, _ = Parse([]byte("[web]\nlisten = \"0.0.0.0:8010\"\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"" + strings.Repeat("ab", 32) + "\"\n"))
	if cfg.Web.Listen != "0.0.0.0:8010" || cfg.Web.Auth != "basic" || len(warns) != 0 {
		t.Errorf("valid basic must keep listen: %+v %v", cfg.Web, warns)
	}
	// broken auth on loopback: no extra warning about listen.
	_, warns, _ = Parse([]byte("[web]\nlisten = \"localhost:8010\"\nauth = \"basic\"\n"))
	for _, w := range warns {
		if w.Field == "web.listen" {
			t.Errorf("loopback listen must not be touched: %v", warns)
		}
	}
	for _, l := range []string{"127.0.0.1:8010", "127.1.2.3:1", "[::1]:8010", "localhost:8010"} {
		if !IsLoopbackListen(l) {
			t.Errorf("%q should be loopback", l)
		}
	}
	for _, l := range []string{"0.0.0.0:8010", ":8010", "[::]:8010", "198.51.100.20:8010", "n5host:8010", "nope"} {
		if IsLoopbackListen(l) {
			t.Errorf("%q should not be loopback", l)
		}
	}
}

func TestWebAllowedHosts(t *testing.T) {
	cfg, warns, _ := Parse([]byte("[web]\nallowed_hosts = [\"n5host.lan\", \" fans.example \", \"\"]\n"))
	if len(warns) != 0 || !reflect.DeepEqual(cfg.Web.AllowedHosts, []string{"n5host.lan", "fans.example"}) {
		t.Errorf("allowed_hosts: %v %v", cfg.Web.AllowedHosts, warns)
	}
	cfg, warns, _ = Parse([]byte("[web]\nallowed_hosts = \"n5host\"\n"))
	hasWarn(t, warns, "web.allowed_hosts")
	if cfg.Web.AllowedHosts != nil {
		t.Errorf("invalid allowed_hosts must be ignored: %v", cfg.Web.AllowedHosts)
	}
	cfg, _, _ = Parse([]byte("[web]\nallowed_hosts = []\n"))
	if cfg.Web.AllowedHosts != nil {
		t.Errorf("empty allowed_hosts must be nil: %v", cfg.Web.AllowedHosts)
	}
	cfg = Default()
	cfg.Web.AllowedHosts = []string{"n5host.lan"}
	back, warns, err := Parse(Marshal(cfg))
	if err != nil || len(warns) != 0 || !reflect.DeepEqual(cfg, back) {
		t.Errorf("allowed_hosts round trip: %v %v\n%+v", err, warns, back)
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
	// a new file is private (it may carry the password hash)
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("new config mode = %v, want 0600", st.Mode().Perm())
	}
	cfg, warns, err = Load(path)
	if err != nil || len(warns) != 0 || len(cfg.Channels) != 2 {
		t.Fatalf("load: %v %v", warns, err)
	}
	// an existing file keeps its mode across Save
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []byte("[[[")); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o640 {
		t.Errorf("config mode after Save = %v, want 0640 (preserved)", st.Mode().Perm())
	}
	cfg, warns, err = Load(path)
	if err == nil || len(warns) != 1 || !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("syntax error via Load: %v %v", warns, err)
	}
	if errors.Is(err, ErrUnreadable) {
		t.Errorf("syntax error must not count as unreadable: %v", err)
	}
	// a directory in place of the file: unreadable (fatal for `check`)
	cfg, warns, err = Load(dir)
	if !errors.Is(err, ErrUnreadable) || len(warns) != 1 || !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("unreadable: %v %v", warns, err)
	}
}

// M3: a config that carries a password hash is written 0600 even when the
// existing file was wider, with one log line; a file without a hash keeps
// its mode.
func TestSaveTightensModeWithHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	var logged []string
	Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	t.Cleanup(func() { Logf = nil })
	if err := Save(path, []byte(good)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	withHash := strings.Replace(good, "auth = \"none\"", "auth = \"basic\"\nuser = \"a\"\npassword_hash = \""+strings.Repeat("ab", 32)+"\"", 1)
	if !HasSecret([]byte(withHash)) || HasSecret([]byte(good)) {
		t.Fatal("HasSecret")
	}
	// hash present in text that does not parse: still a secret
	if !HasSecret([]byte("[web]\npassword_hash = 'x'\n[[[")) || HasSecret([]byte("[web]\npassword_hash = \"\"\n[[[")) {
		t.Error("HasSecret on unparsable text")
	}
	if err := Save(path, []byte(withHash)); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode with hash = %o, want 0600", st.Mode().Perm())
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "tightened") || !strings.Contains(logged[0], "0644") {
		t.Errorf("log = %v", logged)
	}
	// already 0600: no log line
	if err := Save(path, []byte(withHash)); err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 {
		t.Errorf("logged again: %v", logged)
	}
	// without a hash the existing mode is kept
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []byte(good)); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Errorf("mode without hash = %o, want 0644 (kept)", st.Mode().Perm())
	}
}

// daemon.interval is capped at MaxInterval (WatchdogSec=60 in the unit):
// above it the value is clamped with a warning, below MinInterval the
// default applies.
func TestIntervalCap(t *testing.T) {
	if MaxInterval != 30*time.Second {
		t.Fatalf("MaxInterval %s, want 30s (WatchdogSec=60 needs two cycles)", MaxInterval)
	}
	cases := []struct {
		src  string
		want time.Duration
		warn bool
	}{
		{`"45s"`, 30 * time.Second, true},
		{`"2m"`, 30 * time.Second, true},
		{`120`, 30 * time.Second, true},
		{`"30s"`, 30 * time.Second, false},
		{`"2s"`, 2 * time.Second, false},
		{`"1s"`, 10 * time.Second, true},
		{`"abc"`, 10 * time.Second, true},
	}
	for _, c := range cases {
		cfg, warns, err := Parse([]byte("[daemon]\ninterval = " + c.src + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Daemon.Interval != c.want {
			t.Errorf("interval %s: got %s, want %s", c.src, cfg.Daemon.Interval, c.want)
		}
		if got := len(warns) > 0; got != c.warn {
			t.Errorf("interval %s: warnings %v, want warning=%v", c.src, warns, c.warn)
		}
		if c.warn {
			hasWarn(t, warns, "daemon.interval")
		}
	}
	_, warns, _ := Parse([]byte("[daemon]\ninterval = \"45s\"\n"))
	if !strings.Contains(warns[0].Msg, "above 30s") || !strings.Contains(warns[0].Msg, "30s used") {
		t.Errorf("clamp warning text: %v", warns)
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

// M1: stop parsing — sensor-aware default, invalid values warn, fixed
// stop duties below MinFixedStop are raised.
func TestStopParsing(t *testing.T) {
	src := `
[[channel]]
name = "hdd_missing"
pwm = 1
sensor = "drivetemp:max"

[[channel]]
name = "hdd_bogus"
pwm = 2
sensor = "drivetemp:max"
stop = "bogus"

[[channel]]
name = "zero"
pwm = 3
sensor = "k10temp"
stop = 0

[[channel]]
name = "low_str"
pwm = 4
sensor = "k10temp"
stop = "30"

[[channel]]
name = "fine"
pwm = 5
sensor = "k10temp"
stop = 200

[[channel]]
name = "auto_hdd"
pwm = 6
sensor = "drivetemp:max"
stop = "auto"

[[channel]]
name = "cpu_missing"
pwm = 7
sensor = "k10temp"
`
	cfg, warns, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"hdd_missing": "140", "hdd_bogus": "140", "zero": "60", "low_str": "60",
		"fine": "200", "auto_hdd": "auto", "cpu_missing": "auto",
	}
	for name, stop := range want {
		if got := cfg.Channel(name).Stop; got != stop {
			t.Errorf("%s: stop %q, want %q", name, got, stop)
		}
	}
	hasWarn(t, warns, "channel.hdd_bogus.stop")
	hasWarn(t, warns, "channel.zero.stop")
	hasWarn(t, warns, "channel.low_str.stop")
	for _, w := range warns {
		if w.Field == "channel.hdd_missing.stop" || w.Field == "channel.fine.stop" || w.Field == "channel.auto_hdd.stop" {
			t.Errorf("unexpected warning %v", w)
		}
	}
	if DefaultStop("drivetemp:max") != HDDStop || DefaultStop("nvme:max") != "auto" {
		t.Errorf("DefaultStop")
	}
}

// M3 / L6: stale_cycles below 6 and alert_cooldown below 60 s fall back
// to the defaults.
func TestLowerBounds(t *testing.T) {
	cfg, warns, err := Parse([]byte("[daemon]\nstale_cycles = 5\nalert_cooldown = \"10s\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	hasWarn(t, warns, "daemon.stale_cycles")
	hasWarn(t, warns, "daemon.alert_cooldown")
	if cfg.Daemon.StaleCycles != 18 || cfg.Daemon.AlertCooldown != 30*time.Minute {
		t.Errorf("defaults not restored: %+v", cfg.Daemon)
	}
	cfg, warns, _ = Parse([]byte("[daemon]\nstale_cycles = 6\nalert_cooldown = \"60s\"\n"))
	if len(warns) != 0 || cfg.Daemon.StaleCycles != 6 || cfg.Daemon.AlertCooldown != time.Minute {
		t.Errorf("lower bounds must be accepted: %v %+v", warns, cfg.Daemon)
	}
	if MinStaleCycle != 6 || MinCooldown != time.Minute {
		t.Errorf("limits: stale %d cooldown %s", MinStaleCycle, MinCooldown)
	}
}

// Preset names are file stems: no path separators or traversal.
func TestPresetNameTraversal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	for _, bad := range []string{"../x", "..", "a/b", "a\b", ".", "x.toml", "", strings.Repeat("a", 65)} {
		if ValidPresetName(bad) {
			t.Errorf("ValidPresetName(%q) = true", bad)
		}
		if err := SavePreset(dir, bad, N5ProChannels()); err == nil {
			t.Errorf("SavePreset(%q) accepted", bad)
		}
		if _, _, err := LoadPreset(dir, bad); err == nil {
			t.Errorf("LoadPreset(%q) accepted", bad)
		}
		if err := DeletePreset(dir, bad); err == nil {
			t.Errorf("DeletePreset(%q) accepted", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "x.toml")); err == nil {
		t.Errorf("traversal wrote outside the preset dir")
	}
	// Same rule as web.presetName: [a-z0-9_-], 1..64 characters.
	for _, good := range []string{"a", "quiet-night_2", strings.Repeat("a", 64)} {
		if !ValidPresetName(good) {
			t.Errorf("ValidPresetName(%q) = false", good)
		}
	}
}

// v0.2: [web].tls — default depends on listen, "file" needs both paths,
// a non-loopback listener is never plain HTTP.
func TestWebTLS(t *testing.T) {
	h := strings.Repeat("ab", 32)
	basic := "auth = \"basic\"\nuser = \"admin\"\npassword_hash = \"" + h + "\"\n"
	cases := []struct {
		name     string
		src      string
		wantTLS  string
		wantWarn bool
	}{
		{"loopback default", "[web]\nlisten = \"127.0.0.1:8010\"\n", "off", false},
		{"lan default", "[web]\nlisten = \"0.0.0.0:8010\"\n", "auto", false},
		{"lan explicit auto", "[web]\nlisten = \"[::]:8010\"\ntls = \"auto\"\n", "auto", false},
		{"lan off forced", "[web]\nlisten = \"0.0.0.0:8010\"\ntls = \"off\"\n", "auto", true},
		{"loopback auto ok", "[web]\ntls = \"auto\"\n", "auto", false},
		{"loopback off", "[web]\ntls = \"off\"\n", "off", false},
		{"unknown loopback", "[web]\ntls = \"tls13\"\n", "off", true},
		{"unknown lan", "[web]\nlisten = \"0.0.0.0:8010\"\ntls = \"nope\"\n", "auto", true},
		{"file complete", "[web]\ntls = \"file\"\ncert_file = \"/etc/x/c.pem\"\nkey_file = \"/etc/x/k.pem\"\n", "file", false},
		{"file missing key loopback", "[web]\ntls = \"file\"\ncert_file = \"/etc/x/c.pem\"\n", "off", true},
		{"file missing both lan", "[web]\nlisten = \"0.0.0.0:8010\"\ntls = \"file\"\n", "auto", true},
		{"file with basic lan", "[web]\nlisten = \"0.0.0.0:8010\"\n" + basic + "tls = \"file\"\ncert_file = \"/c\"\nkey_file = \"/k\"\n", "file", false},
		// auth misconfigured forces loopback first; tls "off" is then fine
		{"auth broken forces loopback", "[web]\nlisten = \"0.0.0.0:8010\"\nauth = \"basic\"\ntls = \"off\"\n", "off", true},
	}
	for _, c := range cases {
		cfg, warns, err := Parse([]byte(c.src))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if cfg.Web.TLS != c.wantTLS {
			t.Errorf("%s: tls = %q, want %q (warns %v)", c.name, cfg.Web.TLS, c.wantTLS, warns)
		}
		tlsWarn := false
		for _, w := range warns {
			if w.Field == "web.tls" {
				tlsWarn = true
			}
		}
		if c.name == "auth broken forces loopback" {
			continue // warnings come from web.auth/web.listen, tls itself is legal on loopback
		}
		if tlsWarn != c.wantWarn {
			t.Errorf("%s: tls warning %v, want %v: %v", c.name, tlsWarn, c.wantWarn, warns)
		}
	}
	cfg, _, _ := Parse([]byte("[web]\ntls = \"file\"\ncert_file = \" /c.pem \"\nkey_file = \"/k.pem\"\n"))
	if cfg.Web.CertFile != "/c.pem" || cfg.Web.KeyFile != "/k.pem" {
		t.Errorf("paths not trimmed: %+v", cfg.Web)
	}
	if DefaultTLS("127.0.0.1:1") != "off" || DefaultTLS("0.0.0.0:1") != "auto" || DefaultTLS("bad") != "auto" {
		t.Errorf("DefaultTLS")
	}
	// round trip with file mode
	cfg = Default()
	cfg.Web.Listen = "192.0.2.10:8010"
	cfg.Web.Auth, cfg.Web.User, cfg.Web.PasswordHash = "basic", "admin", h
	cfg.Web.TLS, cfg.Web.CertFile, cfg.Web.KeyFile = "file", "/c.pem", "/k.pem"
	back, warns, err := Parse(Marshal(cfg))
	if err != nil || len(warns) != 0 || !reflect.DeepEqual(cfg, back) {
		t.Errorf("tls round trip: %v %v\n%+v", err, warns, back)
	}
}

// v0.2: [log] file/max_size_mb/max_files.
func TestLogSection(t *testing.T) {
	cfg, warns, err := Parse(nil)
	if err != nil || len(warns) != 0 {
		t.Fatal(warns, err)
	}
	if cfg.Log.File != DefaultLogFile || cfg.Log.MaxSizeMB != 5 || cfg.Log.MaxFiles != 5 {
		t.Errorf("defaults: %+v", cfg.Log)
	}
	cfg, warns, _ = Parse([]byte("[log]\nfile = \"\"\nmax_size_mb = 100\nmax_files = 1\n"))
	if len(warns) != 0 || cfg.Log.File != "" || cfg.Log.MaxSizeMB != 100 || cfg.Log.MaxFiles != 1 {
		t.Errorf("explicit: %+v %v", cfg.Log, warns)
	}
	cfg, warns, _ = Parse([]byte("[log]\nfile = \"relative.log\"\nmax_size_mb = 0\nmax_files = 21\nextra = 1\n"))
	hasWarn(t, warns, "log.file")
	hasWarn(t, warns, "log.max_size_mb")
	hasWarn(t, warns, "log.max_files")
	hasWarn(t, warns, "log.extra")
	if !reflect.DeepEqual(cfg.Log, Default().Log) {
		t.Errorf("all-invalid log must equal defaults: %+v", cfg.Log)
	}
	_, warns, _ = Parse([]byte("log = 5\n"))
	hasWarn(t, warns, "log")
	cfg, warns, _ = Parse([]byte("[log]\nfile = \"/var/log/x/y.log\"\nmax_size_mb = 101\n"))
	hasWarn(t, warns, "log.max_size_mb")
	if cfg.Log.File != "/var/log/x/y.log" || cfg.Log.MaxSizeMB != 5 {
		t.Errorf("partial: %+v", cfg.Log)
	}
	// round trip incl. disabled file
	cfg = Default()
	cfg.Log = Log{File: "", MaxSizeMB: 7, MaxFiles: 3}
	back, warns, err := Parse(Marshal(cfg))
	if err != nil || len(warns) != 0 || !reflect.DeepEqual(cfg, back) {
		t.Errorf("log round trip: %v %v\n%+v\n%s", err, warns, back, Marshal(cfg))
	}
}

// H2: [log].file is appended to as root, so it must be a file under
// /var/log in clean form. Anything else falls back to the default with a
// warning; N5FANGOV_LOG_ROOT moves the root for tests only.
func TestLogFileUnderVarLog(t *testing.T) {
	bad := []string{
		"/dev/sda",
		"/etc/n5-fangov/config.toml",
		"/var/log/../etc/passwd",
		"/var/log/n5-fangov/../../../etc/shadow",
		"/var/log//n5-fangov/x.log",
		"/var/log/./x.log",
		"/var/log/",
		"/var/log",
		"/var/logs/x.log",
		"/var/log/n5-fangov/",
		"relative.log",
		"../var/log/x.log",
	}
	for _, f := range bad {
		cfg, warns, err := Parse([]byte("[log]\nfile = " + strconv.Quote(f) + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		hasWarn(t, warns, "log.file")
		if cfg.Log.File != DefaultLogFile {
			t.Errorf("%q accepted as %q", f, cfg.Log.File)
		}
		if ValidLogPath(f) == nil {
			t.Errorf("ValidLogPath(%q) = nil", f)
		}
	}
	for _, f := range []string{"/var/log/n5-fangov/n5-fangov.log", "/var/log/fans.log", "/var/log/a/b/c.log"} {
		cfg, warns, _ := Parse([]byte("[log]\nfile = " + strconv.Quote(f) + "\n"))
		if len(warns) != 0 || cfg.Log.File != f {
			t.Errorf("%q: %v %q", f, warns, cfg.Log.File)
		}
	}
	// test override: a temp root replaces /var/log; /var/log itself is then refused
	root := t.TempDir()
	t.Setenv(LogRootEnv, root)
	if err := ValidLogPath(filepath.Join(root, "x.log")); err != nil {
		t.Errorf("override root: %v", err)
	}
	if ValidLogPath("/var/log/x.log") == nil {
		t.Errorf("/var/log accepted while the root is overridden")
	}
	if ValidLogPath(root+"/../escape.log") == nil {
		t.Errorf("escape from the overridden root accepted")
	}
}

// SetKey edits one key in place and keeps everything else.
func TestSetKey(t *testing.T) {
	const tail = "\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\n"
	src := "# header\n[daemon]\ninterval = \"10s\"\n\n[web]\nlisten = \"127.0.0.1:8010\"   # keep local\nauth = \"none\"\n" + tail
	out := string(SetKey([]byte(src), "web", "auth", `"basic"`))
	want := "# header\n[daemon]\ninterval = \"10s\"\n\n[web]\nlisten = \"127.0.0.1:8010\"   # keep local\nauth = \"basic\"\n" + tail
	if out != want {
		t.Errorf("replace:\n%s", out)
	}
	out = string(SetKey([]byte(out), "web", "user", `"admin"`))
	want = "# header\n[daemon]\ninterval = \"10s\"\n\n[web]\nlisten = \"127.0.0.1:8010\"   # keep local\nauth = \"basic\"\nuser = \"admin\"\n" + tail
	if out != want {
		t.Errorf("append in section:\n%s", out)
	}
	// missing section is appended; a [[channel]] table named like the key
	// prefix must not be touched
	out = string(SetKey([]byte("[daemon]\ninterval = \"10s\"\n"), "web", "tls", `"auto"`))
	if out != "[daemon]\ninterval = \"10s\"\n\n[web]\ntls = \"auto\"\n" {
		t.Errorf("missing section:\n%s", out)
	}
	out = string(SetKey(nil, "web", "tls", `"auto"`))
	if out != "[web]\ntls = \"auto\"\n" {
		t.Errorf("empty file:\n%s", out)
	}
	// key with a common prefix (user vs user_x) is not confused
	out = string(SetKey([]byte("[web]\nuser_x = 1\n"), "web", "user", `"a"`))
	if out != "[web]\nuser_x = 1\nuser = \"a\"\n" {
		t.Errorf("prefix:\n%s", out)
	}
	// the result parses and carries the value
	cfg, warns, err := Parse(SetKey(SetKey(SetKey([]byte(src), "web", "auth", `"basic"`), "web", "user", `"admin"`), "web", "password_hash", `"`+strings.Repeat("0", 64)+`"`))
	if err != nil || len(warns) != 0 || cfg.Web.Auth != "basic" || cfg.Web.User != "admin" || len(cfg.Channels) != 1 {
		t.Errorf("parse after SetKey: %v %v %+v", err, warns, cfg.Web)
	}
}

// [alert] and [dashboard] sections.

func TestAlertSection(t *testing.T) {
	cfg, warns, err := Parse([]byte("[alert]\ntransport = \"mail\"\nmail_to = \"ops@example.test\"\n"))
	if err != nil || len(warns) != 0 {
		t.Fatalf("valid: %v %v", warns, err)
	}
	if cfg.Alert.Transport != "mail" || cfg.Alert.MailTo != "ops@example.test" {
		t.Errorf("alert: %+v", cfg.Alert)
	}
	// case and spacing are tolerated for the transport
	cfg, warns, _ = Parse([]byte("[alert]\ntransport = \" PVE \"\n"))
	if cfg.Alert.Transport != "pve" || len(warns) != 0 {
		t.Errorf("normalised transport: %+v %v", cfg.Alert, warns)
	}
	// invalid values → warning + default
	cfg, warns, _ = Parse([]byte("[alert]\ntransport = \"pigeon\"\nmail_to = \"two words\"\nbogus = 1\n"))
	for _, f := range []string{"alert.transport", "alert.mail_to", "alert.bogus"} {
		hasWarn(t, warns, f)
	}
	if !reflect.DeepEqual(cfg.Alert, Default().Alert) {
		t.Errorf("all-invalid alert must equal defaults: %+v", cfg.Alert)
	}
	cfg, warns, _ = Parse([]byte("[alert]\nmail_to = \"a\\\"b\"\n"))
	hasWarn(t, warns, "alert.mail_to")
	if cfg.Alert.MailTo != DefaultMailTo {
		t.Errorf("quoted mail_to must fall back: %q", cfg.Alert.MailTo)
	}
	cfg, warns, _ = Parse([]byte("alert = 5\n"))
	hasWarn(t, warns, "alert")
	if cfg.Alert.Transport != "auto" {
		t.Errorf("non-table alert: %+v", cfg.Alert)
	}
	// round trip
	cfg = Default()
	cfg.Alert = Alert{Transport: "log", MailTo: "admin"}
	back, warns, err := Parse(Marshal(cfg))
	if err != nil || len(warns) != 0 || !reflect.DeepEqual(cfg, back) {
		t.Errorf("alert round trip: %v %v\n%+v", err, warns, back)
	}
}

func TestValidMailTo(t *testing.T) {
	for _, ok := range []string{"root", "admin", "ops@example.test", "first.last+tag@mail.example", "user_1"} {
		if !ValidMailTo(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "two words", "a\"b", "a;b", "root@", "@host", "x@y z", strings.Repeat("a", 260)} {
		if ValidMailTo(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

func TestDashboardSection(t *testing.T) {
	cfg, warns, err := Parse([]byte("[dashboard]\nsensors = [\"hwmon:amdgpu:temp1\", \" nvme:max \", \"\", \"hwmon:amdgpu:temp1\"]\n"))
	if err != nil || len(warns) != 0 {
		t.Fatalf("valid: %v %v", warns, err)
	}
	if !reflect.DeepEqual(cfg.Dashboard.Sensors, []string{"hwmon:amdgpu:temp1", "nvme:max"}) {
		t.Errorf("sensors (trimmed, deduplicated, empties dropped): %v", cfg.Dashboard.Sensors)
	}
	// absent / empty → non-nil empty slice (round trip with Default)
	cfg, _, _ = Parse(nil)
	if cfg.Dashboard.Sensors == nil || len(cfg.Dashboard.Sensors) != 0 {
		t.Errorf("absent sensors must be an empty non-nil slice: %#v", cfg.Dashboard.Sensors)
	}
	cfg, warns, _ = Parse([]byte("[dashboard]\nsensors = []\n"))
	if cfg.Dashboard.Sensors == nil || len(warns) != 0 {
		t.Errorf("empty sensors: %#v %v", cfg.Dashboard.Sensors, warns)
	}
	// too many → warning, truncated to MaxDashboardSensors
	var ids []string
	for i := 0; i < MaxDashboardSensors+2; i++ {
		ids = append(ids, "\"hwmon:x:temp"+string(rune('1'+i))+"\"")
	}
	cfg, warns, _ = Parse([]byte("[dashboard]\nsensors = [" + strings.Join(ids, ",") + "]\n"))
	hasWarn(t, warns, "dashboard.sensors")
	if len(cfg.Dashboard.Sensors) != MaxDashboardSensors {
		t.Errorf("truncated to %d, got %d", MaxDashboardSensors, len(cfg.Dashboard.Sensors))
	}
	// wrong type → warning, empty
	cfg, warns, _ = Parse([]byte("[dashboard]\nsensors = \"k10temp\"\nextra = 1\n"))
	hasWarn(t, warns, "dashboard.sensors")
	hasWarn(t, warns, "dashboard.extra")
	if len(cfg.Dashboard.Sensors) != 0 {
		t.Errorf("non-array sensors must be ignored: %v", cfg.Dashboard.Sensors)
	}
	// round trip and Clone independence
	cfg = Default()
	cfg.Dashboard.Sensors = []string{"k10temp", "ec:system"}
	back, warns, err := Parse(Marshal(cfg))
	if err != nil || len(warns) != 0 || !reflect.DeepEqual(cfg, back) {
		t.Errorf("dashboard round trip: %v %v\n%+v", err, warns, back)
	}
	cl := cfg.Clone()
	cl.Dashboard.Sensors[0] = "changed"
	if cfg.Dashboard.Sensors[0] != "k10temp" {
		t.Errorf("Clone must copy the sensor list")
	}
}

// The shipped example config must parse without a single warning: it is
// the reference for every key, and the daemon would alert on it.
func TestExampleConfigParses(t *testing.T) {
	// The file is part of the repository and shipped by the deb: a missing
	// copy is a broken checkout, not a reason to skip (AUDIT 7).
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "config.example.toml"))
	if err != nil {
		t.Fatal("deploy/config.example.toml not found:", err)
	}
	cfg, warns, err := Parse(raw)
	if err != nil || len(warns) != 0 {
		t.Fatalf("example config: %v %v", warns, err)
	}
	if cfg.Alert.Transport != "auto" || cfg.Alert.MailTo != "root" || len(cfg.Dashboard.Sensors) != 0 || len(cfg.Channels) != 3 {
		t.Errorf("example values: %+v %+v %d channels", cfg.Alert, cfg.Dashboard, len(cfg.Channels))
	}
}

// TestValidMailToNoLeadingDash (R-M3): a recipient that mail(1) would take
// as an option is refused; the leading character is a letter, digit or _.
func TestValidMailToNoLeadingDash(t *testing.T) {
	for _, bad := range []string{"-root", "-Sexpandaddr", "-a@example.test", "--", ".hidden", "%x", "+tag"} {
		if ValidMailTo(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
	for _, ok := range []string{"root", "_svc", "a-b", "x.y-z+tag@mail.example", "9lives"} {
		if !ValidMailTo(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	// the parser falls back to the default and warns
	cfg, warns, _ := Parse([]byte("[alert]\nmail_to = \"-Sfoo\"\n"))
	hasWarn(t, warns, "alert.mail_to")
	if cfg.Alert.MailTo != DefaultMailTo {
		t.Errorf("fallback: %q", cfg.Alert.MailTo)
	}
}

// TestParseDottedTables (R-L11): sections written as top-level dotted keys
// are tables to the parser (toml records no type for an implicit table);
// a scalar under a section name is still refused.
func TestParseDottedTables(t *testing.T) {
	src := "daemon.interval = \"7s\"\nweb.auth = \"basic\"\nweb.user = \"admin\"\nweb.password_hash = \"" + strings.Repeat("0", 64) + "\"\nalert.transport = \"log\"\ndashboard.sensors = [\"k10temp\"]\nlog.max_files = 2\n"
	cfg, warns, err := Parse([]byte(src))
	if err != nil || len(warns) != 0 {
		t.Fatalf("dotted layout: %v %v", err, warns)
	}
	if cfg.Daemon.Interval.String() != "7s" || cfg.Web.Auth != "basic" || cfg.Web.User != "admin" || cfg.Web.PasswordHash != strings.Repeat("0", 64) || cfg.Alert.Transport != "log" || len(cfg.Dashboard.Sensors) != 1 || cfg.Log.MaxFiles != 2 {
		t.Errorf("values: %+v %+v %+v", cfg.Daemon, cfg.Web, cfg.Alert)
	}
	if !HasSecret([]byte(src)) {
		t.Error("HasSecret must see the dotted hash")
	}
	// unknown dotted key → warning like inside a header table
	_, warns, _ = Parse([]byte("web.bogus = 1\n"))
	hasWarn(t, warns, "web.bogus")
	// scalar / array under the section name → not a table
	for _, bad := range []string{"web = 5\n", "web = \"x\"\n", "web = [1]\n"} {
		cfg, warns, err := Parse([]byte(bad))
		if err != nil {
			t.Fatalf("%q: %v", bad, err)
		}
		hasWarn(t, warns, "web")
		if cfg.Web.Auth != Default().Web.Auth || cfg.Web.User != "" {
			t.Errorf("%q: %+v", bad, cfg.Web)
		}
	}
	// inline table parses like a header table
	cfg, warns, err = Parse([]byte("web = { auth = \"basic\", user = \"u\", password_hash = \"" + strings.Repeat("1", 64) + "\" }\n"))
	if err != nil || len(warns) != 0 || cfg.Web.User != "u" {
		t.Errorf("inline: %v %v %+v", err, warns, cfg.Web)
	}
}

// TestSetKeyDotted (R-L11): a file that writes [web] as top-level dotted
// keys is edited in place — replace the key, insert a new key after the
// last section.* line — instead of appending a second [web] table, which
// the parser rejects. Other sections are untouched; the result parses.
func TestSetKeyDotted(t *testing.T) {
	src := "# dotted layout\ndaemon.interval = \"10s\"\nweb.listen = \"127.0.0.1:8010\"\nweb.auth = \"basic\"   # keep\nweb.user = \"admin\"\n\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45,85],[80,255]]\ncritical = 88\n"
	out := string(SetKey([]byte(src), "web", "user", `"ops"`))
	want := strings.Replace(src, `web.user = "admin"`, `web.user = "ops"`, 1)
	if out != want {
		t.Errorf("replace dotted:\n%s", out)
	}
	out = string(SetKey([]byte(out), "web", "password_hash", `"`+strings.Repeat("0", 64)+`"`))
	want = strings.Replace(want, "web.user = \"ops\"\n", "web.user = \"ops\"\nweb.password_hash = \""+strings.Repeat("0", 64)+"\"\n", 1)
	if out != want {
		t.Errorf("insert after the last dotted key:\n%s", out)
	}
	if strings.Count(out, "[web]") != 0 {
		t.Errorf("a [web] header must not be appended:\n%s", out)
	}
	cfg, warns, err := Parse([]byte(out))
	if err != nil || len(warns) != 0 || cfg.Web.User != "ops" || cfg.Web.Auth != "basic" || cfg.Web.Listen != "127.0.0.1:8010" || cfg.Daemon.Interval.String() != "10s" || len(cfg.Channels) != 1 {
		t.Errorf("parse after dotted SetKey: %v %v %+v", err, warns, cfg.Web)
	}
	// a section absent in both forms is still appended as a header table
	out = string(SetKey([]byte(out), "alert", "transport", `"log"`))
	if !strings.HasSuffix(out, "\n[alert]\ntransport = \"log\"\n") {
		t.Errorf("missing section appended:\n%s", out)
	}
	if cfg, _, err := Parse([]byte(out)); err != nil || cfg.Alert.Transport != "log" || cfg.Web.User != "ops" {
		t.Errorf("parse after append: %v %+v", err, cfg.Alert)
	}
	// dotted keys of another section do not attract the insert; the
	// dotted form only counts before the first table header
	out = string(SetKey([]byte("daemon.interval = \"5s\"\n\n[daemon]\nlog_every = 3\n"), "web", "tls", `"auto"`))
	if out != "daemon.interval = \"5s\"\n\n[daemon]\nlog_every = 3\n\n[web]\ntls = \"auto\"\n" {
		t.Errorf("unrelated dotted keys:\n%s", out)
	}
	out = string(SetKey([]byte("[daemon]\nweb.user = \"x\"\n"), "web", "user", `"y"`))
	if out != "[daemon]\nweb.user = \"x\"\n\n[web]\nuser = \"y\"\n" {
		t.Errorf("dotted key inside a table is not top level:\n%s", out)
	}
	// header layout unchanged by the refactor: key with spaces around "=" and a comment
	out = string(SetKey([]byte("[web]\n  user   =\"a\" # c\nuser_x = 1\n"), "web", "user", `"b"`))
	if out != "[web]\nuser = \"b\"\nuser_x = 1\n" {
		t.Errorf("header replace:\n%s", out)
	}
	if assignedKey("# user = 1") != "" || assignedKey("user") != "" || assignedKey(" web.user = \"a=b\" ") != "web.user" {
		t.Error("assignedKey")
	}
}

func warnFields(warns []Warning) string {
	var out []string
	for _, w := range warns {
		out = append(out, w.Field)
	}
	return strings.Join(out, ",")
}

// TestEnumKeysNormalised: blanks and letter case around profile, auth,
// tls, transport and stop do not turn a valid value into "unknown".
func TestEnumKeysNormalised(t *testing.T) {
	src := "[daemon]\nprofile = \" N5Pro \"\n[web]\nlisten = \"192.0.2.20:8010\"\nauth = \"Basic\"\nuser = \"admin\"\npassword_hash = \"" + strings.Repeat("ab", 32) + "\"\ntls = \" AUTO\"\n[alert]\ntransport = \"LOG \"\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\nstop = \"Auto \"\n"
	cfg, warns, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	if cfg.Daemon.Profile != "n5pro" || cfg.Web.Auth != "basic" || cfg.Web.TLS != "auto" || cfg.Alert.Transport != "log" || cfg.Channels[0].Stop != "auto" {
		t.Errorf("normalised: %+v %+v %+v %+v", cfg.Daemon.Profile, cfg.Web, cfg.Alert, cfg.Channels[0].Stop)
	}
}

// TestListenPortChecked: a listen value whose port is not a number in
// 1..65535 falls back to the default with one warning.
func TestListenPortChecked(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:abc", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:"} {
		cfg, warns, err := Parse([]byte("[web]\nlisten = \"" + listen + "\"\n"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Web.Listen != Default().Web.Listen || len(warns) != 1 || warns[0].Field != "web.listen" {
			t.Errorf("%q: listen=%q warns=%v", listen, cfg.Web.Listen, warns)
		}
	}
	cfg, warns, _ := Parse([]byte("[web]\nlisten = \"[::1]:65535\"\n"))
	if cfg.Web.Listen != "[::1]:65535" || len(warns) != 0 {
		t.Errorf("valid port: %q %v", cfg.Web.Listen, warns)
	}
}

// TestChannelNameLength: 32 characters pass, 33 drop the channel — the
// same bound the override API applies.
func TestChannelNameLength(t *testing.T) {
	mk := func(name string) string {
		return "[[channel]]\nname = \"" + name + "\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\n"
	}
	if cfg, _, _ := Parse([]byte(mk(strings.Repeat("a", 32)))); len(cfg.Channels) != 1 {
		t.Errorf("32-character name dropped")
	}
	cfg, warns, _ := Parse([]byte(mk(strings.Repeat("a", 33))))
	if len(cfg.Channels) != 0 || len(warns) != 1 || !strings.Contains(warns[0].Msg, "{1,32}") {
		t.Errorf("33-character name: %d channels, %v", len(cfg.Channels), warns)
	}
}

// TestPwmOneWarning: a missing, non-integer or out-of-range pwm yields
// exactly one warning that says the channel is dropped.
func TestPwmOneWarning(t *testing.T) {
	for _, pwm := range []string{"", "pwm = 0\n", "pwm = 9\n", "pwm = \"one\"\n"} {
		src := "[[channel]]\nname = \"cpu\"\n" + pwm + "sensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\n"
		cfg, warns, err := Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Channels) != 0 || len(warns) != 1 || warns[0].Field != "channel.cpu.pwm" || !strings.Contains(warns[0].Msg, "channel dropped") {
			t.Errorf("%q: %d channels, warnings %s / %v", pwm, len(cfg.Channels), warnFields(warns), warns)
		}
	}
}

// TestUserNameRule: a user outside the API rule with auth = basic drops
// to auth = none (fail closed: loopback) with a warning naming the rule.
func TestUserNameRule(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	for _, user := range []string{" admin", "ad min", "über", strings.Repeat("a", 33)} {
		cfg, warns, _ := Parse([]byte("[web]\nlisten = \"192.0.2.20:8010\"\nauth = \"basic\"\nuser = \"" + user + "\"\npassword_hash = \"" + hash + "\"\n"))
		if cfg.Web.Auth != "none" || cfg.Web.Listen != Default().Web.Listen || !strings.Contains(warnFields(warns), "web.user") {
			t.Errorf("%q: auth=%s listen=%s warns=%v", user, cfg.Web.Auth, cfg.Web.Listen, warns)
		}
	}
	cfg, warns, _ := Parse([]byte("[web]\nlisten = \"192.0.2.20:8010\"\nauth = \"basic\"\nuser = \"root.ops-1\"\npassword_hash = \"" + hash + "\"\n"))
	if cfg.Web.Auth != "basic" || len(warns) != 0 {
		t.Errorf("valid user: auth=%s warns=%v", cfg.Web.Auth, warns)
	}
	if !UserRe.MatchString("Admin_1.x-y") || UserRe.MatchString("") || MinPasswordLen != 8 || MaxPasswordLen != 128 {
		t.Error("shared rules")
	}
}

// TestBehindTLSProxy: the key parses, defaults to false, and a non-boolean
// value is one warning.
func TestBehindTLSProxy(t *testing.T) {
	cfg, warns, _ := Parse([]byte("[web]\nbehind_tls_proxy = true\n"))
	if !cfg.Web.BehindTLSProxy || len(warns) != 0 {
		t.Errorf("true: %v %v", cfg.Web.BehindTLSProxy, warns)
	}
	cfg, warns, _ = Parse([]byte("[web]\nlisten = \"127.0.0.1:8010\"\n"))
	if cfg.Web.BehindTLSProxy || len(warns) != 0 {
		t.Errorf("default: %v %v", cfg.Web.BehindTLSProxy, warns)
	}
	cfg, warns, _ = Parse([]byte("[web]\nbehind_tls_proxy = \"yes\"\n"))
	if cfg.Web.BehindTLSProxy || len(warns) != 1 || warns[0].Field != "web.behind_tls_proxy" {
		t.Errorf("string: %v %v", cfg.Web.BehindTLSProxy, warns)
	}
	// round trip through Marshal
	c := Default()
	c.Web.BehindTLSProxy = true
	back, warns, err := Parse(Marshal(c))
	if err != nil || len(warns) != 0 || !back.Web.BehindTLSProxy {
		t.Errorf("round trip: %v %v %v", err, warns, back.Web.BehindTLSProxy)
	}
}
