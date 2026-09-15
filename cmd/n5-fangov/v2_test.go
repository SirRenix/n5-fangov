package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
)

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
	if line != "kernel 6.17.4-1-pve: fan driver module missing — run: dkms install minisforum-n5-it5571/0.2.0 -k 6.17.4-1-pve" {
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

// ---------------------------------------------------------------------------
// settings bundle: export/import round trip

const bundleCfg = `# my config
[daemon]
profile = "n5pro"

[web]
listen = "192.0.2.10:8010"
auth = "basic"
user = "admin"
password_hash = "` + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + `"
tls = "auto"

[[channel]]
name = "cpu"
pwm = 1
sensor = "k10temp"
curve = [[45, 85], [80, 255]]
critical = 88
stop = "auto"
`

const quietPreset = "[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[50, 60], [85, 255]]\ncritical = 90\n"

func bundleDirs(t *testing.T) (cfgPath, presetDir string) {
	t.Helper()
	root := t.TempDir()
	cfgPath = filepath.Join(root, "config.toml")
	presetDir = filepath.Join(root, "presets")
	if err := os.WriteFile(cfgPath, []byte(bundleCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(presetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(presetDir, "quiet.toml"), []byte(quietPreset), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath, presetDir
}

func TestBundleExportImportRoundTrip(t *testing.T) {
	cfgPath, presetDir := bundleDirs(t)
	src := fileBundle{cfgPath: cfgPath, presetDir: presetDir}
	data, err := src.Export()
	if err != nil {
		t.Fatal(err)
	}
	var b settingsBundle
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	if b.Format != 1 || b.Exported == 0 || b.Version == "" {
		t.Errorf("header: %+v", b)
	}
	if strings.Contains(b.Config, "0123456789abcdef") || !strings.Contains(b.Config, `password_hash = "<unchanged>"`) {
		t.Errorf("hash not redacted:\n%s", b.Config)
	}
	if !strings.Contains(b.Config, "# my config") {
		t.Errorf("comments must survive export")
	}
	if b.Presets["quiet"] != quietPreset || len(b.Presets) != 1 {
		t.Errorf("presets: %v", b.Presets)
	}

	// import onto a second box that already has a password: the
	// placeholder is replaced by the local hash
	root2 := t.TempDir()
	cfg2 := filepath.Join(root2, "config.toml")
	pre2 := filepath.Join(root2, "presets")
	localHash := strings.Repeat("ab", 32)
	if err := os.WriteFile(cfg2, []byte("[web]\nauth = \"basic\"\nuser = \"x\"\npassword_hash = \""+localHash+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a local preset not in the bundle stays
	if err := os.MkdirAll(pre2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pre2, "local.toml"), []byte(quietPreset), 0o644); err != nil {
		t.Fatal(err)
	}
	var reloaded []byte
	dst := fileBundle{cfgPath: cfg2, presetDir: pre2, reload: func(raw []byte) error { reloaded = raw; return nil }}
	restart, err := dst.Import(data)
	if err != nil || restart {
		t.Fatalf("import: restart=%v err=%v", restart, err)
	}
	got, _ := os.ReadFile(cfg2)
	if !strings.Contains(string(got), localHash) || strings.Contains(string(got), redactedHash) {
		t.Errorf("placeholder not replaced:\n%s", got)
	}
	if !strings.Contains(string(got), "# my config") || !strings.Contains(string(got), `listen = "192.0.2.10:8010"`) {
		t.Errorf("config text not restored:\n%s", got)
	}
	if string(reloaded) != string(got) {
		t.Errorf("daemon reloaded with different text")
	}
	if p, _ := os.ReadFile(filepath.Join(pre2, "quiet.toml")); string(p) != quietPreset {
		t.Errorf("preset not restored: %q", p)
	}
	if _, err := os.Stat(filepath.Join(pre2, "local.toml")); err != nil {
		t.Errorf("local preset removed")
	}
	// the restored box exports the same bundle again (modulo timestamp)
	data2, err := fileBundle{cfgPath: cfg2, presetDir: pre2}.Export()
	if err != nil {
		t.Fatal(err)
	}
	var b2 settingsBundle
	_ = json.Unmarshal(data2, &b2)
	if b2.Config != b.Config || b2.Presets["quiet"] != quietPreset {
		t.Errorf("second export differs:\n%s", b2.Config)
	}

	// restart sentinel from the daemon passes through
	dst.reload = func([]byte) error { return errRestartRequired() }
	if restart, err := dst.Import(data); err != nil || !restart {
		t.Errorf("restart: %v %v", restart, err)
	}
	// reload failure after the write is reported
	dst.reload = func([]byte) error { return errors.New("boom") }
	if _, err := dst.Import(data); err == nil || !strings.Contains(err.Error(), "written, but") {
		t.Errorf("reload error: %v", err)
	}
	// no daemon: written, nothing to reload
	dst.reload = nil
	if restart, err := dst.Import(data); err != nil || restart {
		t.Errorf("offline: %v %v", restart, err)
	}
}

// Import validates everything before it writes anything.
func TestBundleImportValidation(t *testing.T) {
	cfgPath, presetDir := bundleDirs(t)
	before, _ := os.ReadFile(cfgPath)
	b := fileBundle{cfgPath: cfgPath, presetDir: presetDir, reload: func([]byte) error { t.Error("reload called"); return nil }}
	unchanged := func(name string) {
		t.Helper()
		after, _ := os.ReadFile(cfgPath)
		if string(after) != string(before) {
			t.Errorf("%s: config written despite errors", name)
		}
		if _, err := os.Stat(filepath.Join(presetDir, "bad.toml")); err == nil {
			t.Errorf("%s: preset written despite errors", name)
		}
	}
	mk := func(cfg string, presets map[string]string) []byte {
		d, _ := json.Marshal(settingsBundle{Format: 1, Config: cfg, Presets: presets})
		return d
	}
	cases := map[string][]byte{
		"not json":          []byte("nope"),
		"unknown field":     []byte(`{"format":1,"config":"[daemon]\n","presets":{},"extra":1}`),
		"format 2":          mk("[daemon]\n", nil),
		"empty config":      mk("", nil),
		"config syntax":     mk("[daemon\n", nil),
		"preset syntax":     mk("[daemon]\n", map[string]string{"bad": "[[["}),
		"preset no channel": mk("[daemon]\n", map[string]string{"bad": "[daemon]\ninterval = \"10s\"\n"}),
		"preset name":       mk("[daemon]\n", map[string]string{"Bad Name": quietPreset}),
		"good and bad":      mk("[daemon]\n", map[string]string{"ok": quietPreset, "bad": "[[["}),
	}
	cases["format 2"] = []byte(strings.Replace(string(cases["format 2"]), `"format":1`, `"format":2`, 1))
	for name, data := range cases {
		_, err := b.Import(data)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		unchanged(name)
		if _, err := os.Stat(filepath.Join(presetDir, "ok.toml")); err == nil {
			t.Errorf("%s: the valid preset was written although the bundle failed", name)
		}
	}
	// several problems are all listed
	_, err := b.Import(mk("[daemon\n", map[string]string{"bad": "[[[", "Bad": quietPreset}))
	var be *bundleError
	if !errors.As(err, &be) || len(be.Errors()) != 3 {
		t.Errorf("error list: %v", err)
	}
	// placeholder without a local password
	if err := os.WriteFile(cfgPath, []byte("[daemon]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ = os.ReadFile(cfgPath)
	_, err = b.Import(mk("[web]\nauth = \"basic\"\nuser = \"a\"\npassword_hash = \"<unchanged>\"\n", nil))
	if err == nil || !strings.Contains(err.Error(), "passwd") {
		t.Errorf("placeholder without local hash: %v", err)
	}
	unchanged("placeholder")
}

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
	if w.Listen != "192.0.2.10:8010" || w.Auth != "basic" || w.User != "admin" || w.TLS != "auto" || w.PasswordHash != passwordHash("admin", "secret") {
		t.Errorf("lan: %+v", w)
	}
	if len(w.PasswordHash) != 64 {
		t.Errorf("hash length %d", len(w.PasswordHash))
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

// passwd edits the three keys in place and leaves the rest of the file alone.
func TestSetWebAuth(t *testing.T) {
	src := "# keep me\n[daemon]\nprofile = \"n5pro\"\n\n[web]\nlisten = \"127.0.0.1:8010\"  # local\nauth = \"none\"\n\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\n"
	out := string(setWebAuth([]byte(src), "admin", "pw"))
	for _, want := range []string{"# keep me", "listen = \"127.0.0.1:8010\"  # local", "auth = \"basic\"", "user = \"admin\"", "password_hash = \"" + passwordHash("admin", "pw") + "\"", "[[channel]]\nname = \"cpu\""} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	cfg, warns, err := parseConfigErr([]byte(out))
	if err != nil || len(warns) != 0 || webOf(cfg).Auth != "basic" || webOf(cfg).User != "admin" {
		t.Errorf("parse: %v %v %+v", err, warns, webOf(cfg))
	}
	// on an empty file the [web] table is created
	cfg, warns, err = parseConfigErr(setWebAuth(nil, "u", "p"))
	if err != nil || len(warns) != 0 || webOf(cfg).Auth != "basic" {
		t.Errorf("empty: %v %v %+v", err, warns, webOf(cfg))
	}
}

// tlsHosts: deterministic, deduplicated, wildcard dropped, ports stripped.
func TestTLSHosts(t *testing.T) {
	hosts := tlsHosts(webSpec{Listen: "192.0.2.10:8010", AllowedHosts: []string{"fans.example:8010", "*", " ", "fans.example"}})
	joined := " " + strings.Join(hosts, " ") + " "
	for _, want := range []string{"192.0.2.10", "fans.example", "localhost", "127.0.0.1"} {
		if !strings.Contains(joined, " "+want+" ") {
			t.Errorf("missing %s in %v", want, hosts)
		}
	}
	if strings.Contains(joined, " * ") || strings.Contains(joined, ":8010") {
		t.Errorf("wildcard or port leaked: %v", hosts)
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		if seen[h] {
			t.Errorf("duplicate %s", h)
		}
		seen[h] = true
	}
	if again := tlsHosts(webSpec{Listen: "192.0.2.10:8010", AllowedHosts: []string{"fans.example:8010", "*", " ", "fans.example"}}); strings.Join(again, ",") != strings.Join(hosts, ",") {
		t.Errorf("not deterministic")
	}
	if hn, _ := os.Hostname(); hn != "" && !seen[hn] {
		t.Errorf("hostname %s missing", hn)
	}
}
