package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
)

// ---------------------------------------------------------------------------
// setup: config generation from the N5 Pro fixture and a generic tree

func TestSetupConfigN5Pro(t *testing.T) {
	fakeN5(t, false)
	hw := hwmon.New()
	dev, err := detectDevice(hw, "auto")
	if err != nil {
		t.Fatal(err)
	}
	chans := setupChannels(dev, hw, newSensorFactory(hw, dev))
	if len(chans) != 3 || chans[0].Name != "cpu" || chans[2].PWM != 3 || chans[2].Stop != "140" {
		t.Fatalf("n5pro channels: %+v", chans)
	}
	// local scope
	local := webSpec{Listen: "127.0.0.1:8010", Auth: "none", TLS: "off"}
	raw := setupConfigText("n5pro", chans, local)
	cfg, warns, err := parseConfigErr(raw)
	if err != nil || len(warns) != 0 {
		t.Fatalf("local config: %v %v\n%s", err, warns, raw)
	}
	if daemonOf(cfg).Profile != "n5pro" || len(channelSpecs(cfg)) != 3 || webOf(cfg).TLS != "off" || webOf(cfg).Auth != "none" {
		t.Errorf("local: %+v %+v", daemonOf(cfg), webOf(cfg))
	}
	if !strings.HasPrefix(string(raw), "# n5-fangov configuration, written by") {
		t.Errorf("header missing:\n%s", raw)
	}
	// lan scope: auth basic + tls auto, hash matches user:password
	lan := webSpec{Listen: "192.0.2.10:8010", Auth: "basic", User: "admin", PasswordHash: passwordHash("admin", "secret"), TLS: "auto"}
	raw = setupConfigText("n5pro", chans, lan)
	cfg, warns, err = parseConfigErr(raw)
	if err != nil || len(warns) != 0 {
		t.Fatalf("lan config: %v %v\n%s", err, warns, raw)
	}
	w := webOf(cfg)
	if w.Listen != "192.0.2.10:8010" || w.Auth != "basic" || w.User != "admin" || w.TLS != "auto" || w.PasswordHash != lan.PasswordHash {
		t.Errorf("lan: %+v", w)
	}
	// Setup writes the salted PBKDF2 form, which verifies the password
	if !strings.HasPrefix(w.PasswordHash, "pbkdf2$") || !verifyPassword("admin", "secret", w.PasswordHash) || verifyPassword("admin", "wrong", w.PasswordHash) {
		t.Errorf("hash form %q", w.PasswordHash)
	}
	// the written file is what check evaluates: no fatal findings
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := saveConfig(path, raw); err != nil {
		t.Fatal(err)
	}
	if f := fatals(runChecks(path, dir)); len(f) != 0 {
		t.Errorf("check on the generated config: %v", f)
	}
}

// A generic chip gets one conservative channel per pwm with the CPU
// sensor; without k10temp/coretemp the first hwmon temperature is used.
func TestSetupConfigGeneric(t *testing.T) {
	root := t.TempDir()
	hwmontest.WriteAttr(t, root, "hwmon0", "name", "nct6798")
	for _, a := range []string{"pwm1", "pwm1_enable", "pwm2", "pwm2_enable", "fan1_input", "fan2_input"} {
		v := "128"
		if strings.HasSuffix(a, "_enable") {
			v = "5"
		}
		if strings.HasSuffix(a, "_input") {
			v = "1200"
		}
		hwmontest.WriteAttr(t, root, "hwmon0", a, v)
	}
	hwmontest.WriteAttr(t, root, "hwmon0", "temp1_input", "41000")
	hwmontest.WriteAttr(t, root, "hwmon1", "name", "acpitz")
	hwmontest.WriteAttr(t, root, "hwmon1", "temp1_input", "27800")
	t.Setenv("N5FANGOV_SYSFS", root)
	hw := hwmon.New()
	dev, err := detectDevice(hw, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Profile().Name() != "nct67xx" {
		t.Fatalf("profile %s", dev.Profile().Name())
	}
	chans := setupChannels(dev, hw, newSensorFactory(hw, dev))
	if len(chans) != 2 {
		t.Fatalf("channels: %+v", chans)
	}
	for i, c := range chans {
		if c.Name != "fan"+string(rune('1'+i)) || c.PWM != i+1 || c.Sensor != "hwmon:nct6798:temp1" || c.Stop != "auto" || c.Critical != genericCritical {
			t.Errorf("channel %d: %+v", i, c)
		}
		if len(c.Curve) != 2 || c.Curve[0][1] < 80 {
			t.Errorf("curve not conservative: %v", c.Curve)
		}
	}
	raw := setupConfigText("nct67xx", chans, webSpec{Listen: "127.0.0.1:8010", Auth: "none", TLS: "off"})
	if _, warns, err := parseConfigErr(raw); err != nil || len(warns) != 0 {
		t.Errorf("generic config: %v %v\n%s", err, warns, raw)
	}
	// with a k10temp device present it is preferred
	hwmontest.WriteAttr(t, root, "hwmon2", "name", "k10temp")
	hwmontest.WriteAttr(t, root, "hwmon2", "temp1_input", "45000")
	dev, _ = detectDevice(hw, "auto")
	if chans := setupChannels(dev, hw, newSensorFactory(hw, dev)); chans[0].Sensor != "k10temp" {
		t.Errorf("k10temp not preferred: %s", chans[0].Sensor)
	}
}

// TestSetupKeepsExisting: setup over an existing config rewrites the
// profile, the channel set and the asked [web] keys and carries the rest
// over (the 0.4.0 release gate lost the HDD hysteresis this way); --fresh
// and a file with a syntax error start from the defaults. A backup is
// written in every case.
func TestSetupKeepsExisting(t *testing.T) {
	fakeN5(t, false)
	dir := t.TempDir()
	prior := `[daemon]
profile = "n5pro"
interval = "15s"
emergency = true

[web]
listen = "127.0.0.1:8010"
auth = "none"
allowed_hosts = ["fans.example.test"]

[alert]
transport = "mail"
mail_to = "ops@example.test"

[dashboard]
sensors = ["ec:ambient"]

[[channel]]
name = "cpu"
pwm = 1
sensor = "k10temp"
curve = [[40, 100], [80, 255]]
critical = 90

[[channel]]
name = "hdd"
pwm = 3
sensor = "drivetemp:max"
curve = [[30, 65], [55, 255]]
critical = 60
stop = 140
hysteresis = 2
min_on = "60s"
ceiling = 55

[[channel]]
name = "aux"
pwm = 4
sensor = "k10temp"
curve = [[45, 85], [80, 255]]
critical = 88

[[schedule]]
preset = "n5pro-balanced"
from = "22:00"
to = "07:00"
`
	run := func(t *testing.T, content string, extra ...string) (string, string) {
		t.Helper()
		cfg := writeCfg(t, dir, content)
		out, errOut, rc := captureOutput(t, func() int {
			return cmdSetup(append([]string{"--config", cfg, "--yes", "--listen", "local"}, extra...))
		})
		if rc != exitOK {
			t.Fatalf("rc=%d\n%s\n%s", rc, out, errOut)
		}
		raw, _ := os.ReadFile(cfg)
		return string(raw), out
	}

	raw, out := run(t, prior)
	c, warns, err := parseConfigErr([]byte(raw))
	if err != nil || len(warns) != 0 {
		t.Fatalf("rewritten config: %v %v\n%s", err, warns, raw)
	}
	if c.Daemon.Interval.String() != "15s" || !c.Daemon.Emergency || c.Daemon.Profile != "n5pro" {
		t.Errorf("[daemon] not kept: %+v", c.Daemon)
	}
	if c.Alert.Transport != "mail" || c.Alert.MailTo != "ops@example.test" {
		t.Errorf("[alert] not kept: %+v", c.Alert)
	}
	if len(c.Dashboard.Sensors) != 1 || len(c.Schedules) != 1 || len(c.Web.AllowedHosts) != 1 {
		t.Errorf("dashboard/schedule/allowed_hosts not kept: %+v %+v %+v", c.Dashboard, c.Schedules, c.Web)
	}
	hdd, cpu, aux := c.Channel("hdd"), c.Channel("cpu"), c.Channel("aux")
	if hdd == nil || hdd.Hysteresis != 2 || hdd.MinOn.String() != "1m0s" || hdd.Ceiling != 55 {
		t.Errorf("hdd post-processing not kept: %+v", hdd)
	}
	// the curve is the profile's again (setup's job), the critical too
	if cpu == nil || cpu.Critical == 90 || cpu.Curve[0][0] == 40 {
		t.Errorf("cpu not reset to the profile set: %+v", cpu)
	}
	if aux == nil || aux.PWM != 4 {
		t.Errorf("channel on pwm4 dropped: %+v", c.Channels)
	}
	for _, want := range []string{"backup: ", "kept: [daemon] interval, emergency", "kept: [alert] transport, mail_to", "kept: [dashboard] sensors",
		"kept: [[schedule]] 1 entries", "kept: [web] allowed_hosts", "kept: channel hdd: hysteresis 2, min_on 1m0s, ceiling 55", "kept: channel aux (pwm4)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(raw, "rewritten by `n5-fangov setup`") {
		t.Errorf("header:\n%s", raw)
	}

	// --fresh: defaults everywhere
	raw, out = run(t, prior, "--fresh")
	c, _ = parseConfig([]byte(raw))
	if c.Alert.Transport != "auto" || c.Daemon.Emergency || len(c.Schedules) != 0 || c.Channel("hdd").Hysteresis != 0 || c.Channel("aux") != nil {
		t.Errorf("--fresh kept something:\n%s", raw)
	}
	if strings.Contains(out, "kept:") || !strings.Contains(out, "backup: ") {
		t.Errorf("--fresh output:\n%s", out)
	}

	// syntax error: nothing carried over, the file is still replaced
	raw, out = run(t, prior+"\n[broken\n")
	c, _ = parseConfig([]byte(raw))
	if c.Alert.Transport != "auto" || !strings.Contains(out, "syntax error: nothing is carried over") {
		t.Errorf("syntax error case:\n%s\n%s", out, raw)
	}
	if bak, _ := filepath.Glob(filepath.Join(dir, "config.toml"+setupBakStem+"*")); len(bak) == 0 {
		t.Error("no backup written")
	}

	// invalid values and typos of the old file are named, not silently lost (review R1)
	_, out = run(t, strings.Replace(prior, "hysteresis = 2", "hysterese = 2", 1)+"\n")
	if !strings.Contains(out, "not carried over (invalid in the old file, default written): channel.hdd.hysterese") {
		t.Errorf("typo not reported:\n%s", out)
	}

	// a syntax error that quotes a bare hash must not print it (review R7)
	hash := strings.Repeat("ab", 32)
	_, out = run(t, "[web]\npassword_hash = "+hash+"\n")
	if strings.Contains(out, hash[:16]) {
		t.Errorf("hash in the syntax error message:\n%s", out)
	}
}

// passwd edits the three keys in place and leaves the rest of the file alone.
func TestSetWebAuth(t *testing.T) {
	src := "# keep me\n[daemon]\nprofile = \"n5pro\"\n\n[web]\nlisten = \"127.0.0.1:8010\"  # local\nauth = \"none\"\n\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\n"
	out := string(setWebAuth([]byte(src), "admin", "pw"))
	for _, want := range []string{"# keep me", "listen = \"127.0.0.1:8010\"  # local", "auth = \"basic\"", "user = \"admin\"", "password_hash = \"pbkdf2$", "[[channel]]\nname = \"cpu\""} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	cfg, warns, err := parseConfigErr([]byte(out))
	if err != nil || len(warns) != 0 || webOf(cfg).Auth != "basic" || webOf(cfg).User != "admin" {
		t.Errorf("parse: %v %v %+v", err, warns, webOf(cfg))
	}
	if !verifyPassword("admin", "pw", webOf(cfg).PasswordHash) {
		t.Errorf("written hash does not verify: %q", webOf(cfg).PasswordHash)
	}
	// on an empty file the [web] table is created
	cfg, warns, err = parseConfigErr(setWebAuth(nil, "u", "p"))
	if err != nil || len(warns) != 0 || webOf(cfg).Auth != "basic" {
		t.Errorf("empty: %v %v %+v", err, warns, webOf(cfg))
	}
}

// The password comes from a file or from stdin ("-"); a literal flag
// value is refused (it would sit in ps and the shell history), empty
// sources are refused.
func TestPasswordFromArgs(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "pw")
	if err := os.WriteFile(pf, []byte("s3cret\r\nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pw, err := passwordFromArgs("", pf, nil); err != nil || pw != "s3cret" {
		t.Errorf("file: %q %v", pw, err)
	}
	if pw, err := passwordFromArgs("literal", pf, nil); err == nil {
		t.Errorf("literal next to a file accepted: %q", pw)
	}
	if pw, err := passwordFromArgs("-", "", strings.NewReader("from-stdin\nignored\n")); err != nil || pw != "from-stdin" {
		t.Errorf("stdin: %q %v", pw, err)
	}
	if pw, err := passwordFromArgs("-", "", strings.NewReader("no-newline")); err != nil || pw != "no-newline" {
		t.Errorf("stdin without newline: %q %v", pw, err)
	}
	if pw, err := passwordFromArgs("literal", "", nil); err == nil || !strings.Contains(err.Error(), "--password-file") {
		t.Errorf("literal accepted: %q %v", pw, err)
	}
	if pw, err := passwordFromArgs("", "", nil); err != nil || pw != "" {
		t.Errorf("nothing given must mean ask: %q %v", pw, err)
	}
	for name, c := range map[string]struct{ lit, file, in string }{
		"empty file":    {"", filepath.Join(dir, "empty"), ""},
		"missing file":  {"", filepath.Join(dir, "nope"), ""},
		"empty stdin":   {"-", "", ""},
		"blank stdin":   {"-", "", "\n"},
		"newline first": {"", filepath.Join(dir, "blank"), ""},
	} {
		_ = os.WriteFile(filepath.Join(dir, "empty"), nil, 0o600)
		_ = os.WriteFile(filepath.Join(dir, "blank"), []byte("\npw\n"), 0o600)
		if pw, err := passwordFromArgs(c.lit, c.file, strings.NewReader(c.in)); err == nil {
			t.Errorf("%s: accepted %q", name, pw)
		}
	}
	if !strings.Contains(passwordFlagHelp, "ps") || !strings.Contains(passwordFlagHelp, "--password-file") {
		t.Errorf("usage text does not steer away from --password: %q", passwordFlagHelp)
	}
}

// TestPasswdRules: passwd refuses a user outside the API rule and a
// password below the API minimum before touching the file; a valid pair
// is written as PBKDF2 with auth = basic.
func TestPasswdRules(t *testing.T) {
	dir := t.TempDir()
	cfg := writeCfg(t, dir, "[web]\nlisten = \"127.0.0.1:8010\"\nauth = \"none\"\n")
	pwFile := filepath.Join(dir, "pw")
	if err := os.WriteFile(pwFile, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStderr(t, func() {
		if rc := cmdPasswd([]string{"--config", cfg, "--user", "admin", "--password-file", pwFile}); rc != exitUsage {
			t.Errorf("short password: rc=%d", rc)
		}
	})
	if !strings.Contains(out, "8..128") {
		t.Errorf("short password message: %q", out)
	}
	if err := os.WriteFile(pwFile, []byte("longenough\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = captureStderr(t, func() {
		if rc := cmdPasswd([]string{"--config", cfg, "--user", "ad min", "--password-file", pwFile}); rc != exitUsage {
			t.Errorf("bad user: rc=%d", rc)
		}
	})
	if !strings.Contains(out, "must match") {
		t.Errorf("bad user message: %q", out)
	}
	out = captureStderr(t, func() {
		if rc := cmdPasswd([]string{"--config", cfg, "--user", "admin", "--password", "literalpw"}); rc != exitUsage {
			t.Errorf("literal password: rc=%d", rc)
		}
	})
	if !strings.Contains(out, "--password-file") {
		t.Errorf("literal password message: %q", out)
	}
	raw, _ := os.ReadFile(cfg)
	if strings.Contains(string(raw), "password_hash") {
		t.Fatalf("file touched by refused calls:\n%s", raw)
	}
	if rc := cmdPasswd([]string{"--config", cfg, "--user", "admin", "--password-file", pwFile}); rc != exitOK {
		t.Fatalf("valid: rc=%d", rc)
	}
	raw, _ = os.ReadFile(cfg)
	c, warns := parseConfig(raw)
	w := webOf(c)
	if len(warns) != 0 || w.Auth != "basic" || w.User != "admin" || !strings.HasPrefix(w.PasswordHash, "pbkdf2$") || !verifyPassword("admin", "longenough", w.PasswordHash) {
		t.Fatalf("written: %+v %v\n%s", w, warns, raw)
	}
}

// TestSetupRejectsBadUser: --yes with a LAN listener and a user outside
// the API rule exits with usage before anything is written.
func TestSetupRejectsBadUser(t *testing.T) {
	fakeN5(t, false)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	pwFile := filepath.Join(dir, "pw")
	if err := os.WriteFile(pwFile, []byte("longenough\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStderr(t, func() {
		if rc := cmdSetup([]string{"--config", cfg, "--yes", "--listen", "192.0.2.20:8010", "--user", "bad user", "--password-file", pwFile}); rc != exitUsage {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "must match") {
		t.Errorf("message: %q", out)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("config written despite the refused user")
	}
	if err := os.WriteFile(pwFile, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rc := cmdSetup([]string{"--config", cfg, "--yes", "--listen", "192.0.2.20:8010", "--user", "admin", "--password-file", pwFile}); rc != exitUsage {
		t.Errorf("short password: rc=%d", rc)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("config written despite the short password")
	}
}
