package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// keepHash is a syntactically valid (legacy sha256) password hash for the
// file-text tests.
const keepHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestPresetStoreBuiltins(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	dir := filepath.Join(t.TempDir(), "presets")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "mine.toml"), []byte(quietPreset), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "n5pro-quiet.toml"), []byte(quietPreset), 0o644) // shadowed
	svc := &fakeService{}
	s := dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: "n5pro"}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	builtin := 0
	for _, p := range list {
		names = append(names, p.Name)
		if p.Builtin {
			builtin++
			if p.Description == "" || len(p.Channels) != 3 {
				t.Errorf("built-in %s: %+v", p.Name, p)
			}
		}
	}
	if builtin != 3 || !reflect.DeepEqual(names, []string{"n5pro-balanced", "n5pro-cool", "n5pro-quiet", "mine"}) {
		t.Errorf("list: %v (builtin %d)", names, builtin)
	}
	// other profile: no built-ins
	if l, _ := (dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: "nct67xx"}).List(); len(l) != 1 || l[0].Builtin {
		t.Errorf("nct67xx list: %+v", l)
	}
	// apply a built-in: config rewritten, daemon reloaded
	if err := s.Apply("n5pro-balanced"); err != nil || svc.reloads() != 1 {
		t.Fatalf("apply built-in: %v", err)
	}
	cfg, _, _ := config.Load(cfgPath)
	if len(cfg.Channels) != 3 || cfg.Channel("hdd") == nil || cfg.Channel("hdd").Critical != 60 {
		t.Errorf("applied channels: %+v", cfg.Channels)
	}
	// save over / delete a built-in → ErrPresetBuiltin
	if err := s.Save("n5pro-cool"); !errors.Is(err, web.ErrPresetBuiltin) {
		t.Errorf("save built-in: %v", err)
	}
	if err := s.Delete("n5pro-cool"); !errors.Is(err, web.ErrPresetBuiltin) {
		t.Errorf("delete built-in: %v", err)
	}
	// user preset: save, delete, delete again → ErrNotExist
	if err := s.Save("custom"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("custom"); err != nil {
		t.Errorf("delete: %v", err)
	}
	if err := s.Delete("custom"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("delete missing: %v", err)
	}
	if err := s.Delete("../x"); err == nil {
		t.Errorf("invalid name must be refused")
	}
}

func TestPresetDetailAndRename(t *testing.T) {
	dir := t.TempDir()
	chans := config.N5ProChannels()
	if err := config.SavePreset(dir, "winter", chans); err != nil {
		t.Fatal(err)
	}
	s := dirPresetStore{dir: dir, profile: "n5pro"}
	det, err := s.Detail("winter")
	if err != nil || det.Builtin || len(det.Channels) != len(chans) || det.Channels[0].Sensor != chans[0].Sensor || len(det.Channels[0].Curve) == 0 {
		t.Fatalf("detail: %+v %v", det, err)
	}
	if det, err := s.Detail("n5pro-balanced"); err != nil || !det.Builtin || det.Description == "" || len(det.Channels) != 3 {
		t.Fatalf("builtin detail: %+v %v", det, err)
	}
	if _, err := s.Detail("nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing detail: %v", err)
	}
	if err := s.Rename("winter", "n5pro-quiet"); !errors.Is(err, web.ErrPresetBuiltin) {
		t.Fatalf("rename onto builtin: %v", err)
	}
	if err := s.Rename("nope", "x"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rename missing: %v", err)
	}
	if err := config.SavePreset(dir, "summer", chans); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("winter", "summer"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("rename onto existing: %v", err)
	}
	if err := s.Rename("winter", "cold"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(presetPath(dir, "cold")); err != nil {
		t.Fatal("renamed file missing")
	}
	if _, err := os.Stat(presetPath(dir, "winter")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("old file still there")
	}
}

// TestPresetBuiltinOtherProfile: a built-in of another profile is
// not applied (fs.ErrNotExist → 404), on the active profile it is.
func TestPresetBuiltinOtherProfile(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	dir := filepath.Join(t.TempDir(), "presets")
	svc := &fakeService{}
	for _, prof := range []string{"nct67xx", "it87xx", "monitor", ""} {
		s := dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: prof}
		if err := s.Apply("n5pro-quiet"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("profile %q: %v", prof, err)
		}
	}
	if svc.reloads() != 0 {
		t.Fatal("a refused apply must not reload")
	}
	// a user file with a built-in name is shadowed (not applied) on every profile
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "n5pro-quiet.toml"), []byte(quietPreset), 0o644)
	if err := (dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: "nct67xx"}).Apply("n5pro-quiet"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("shadowed file on other profile: %v", err)
	}
	if err := (dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: "n5pro"}).Apply("n5pro-quiet"); err != nil || svc.reloads() != 1 {
		t.Errorf("active profile: %v", err)
	}
	if cfg, _, _ := config.Load(cfgPath); cfg.Channel("hdd") == nil || cfg.Channel("hdd").Critical != 66 {
		t.Errorf("the built-in, not the shadowed file, was applied: %+v", cfg.Channels)
	}
}

// TestPresetApplyMergesByPWM: a preset channel replaces the config channel
// with the same pwm (the config name is kept), channels the preset does not
// name stay (an optional pwm4 channel survives a built-in preset), a pwm
// the config lacks is added.
func TestPresetApplyMergesByPWM(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	src := storeConfig + `
[[channel]]
name = "disks"
pwm = 3
sensor = ["drivetemp:max", "ec:hdd"]
curve = [[36,105],[46,255]]
critical = 56
hysteresis = 2
min_on = "60s"

[[channel]]
name = "pcie"
pwm = 4
sensor = "k10temp"
curve = [[40,80],[70,255]]
critical = 90
`
	if err := os.WriteFile(cfgPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &fakeService{}
	s := dirPresetStore{dir: filepath.Join(t.TempDir(), "presets"), cfgPath: cfgPath, svc: svc, profile: "n5pro"}
	if err := s.Apply("n5pro-balanced"); err != nil || svc.reloads() != 1 {
		t.Fatalf("apply: %v", err)
	}
	cfg, warns, err := config.Load(cfgPath)
	if err != nil || len(warns) != 0 {
		t.Fatalf("load: %v %v", err, warns)
	}
	var names []string
	for _, c := range cfg.Channels {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"cpu", "disks", "pcie", "ssd"}) {
		t.Fatalf("channels after merge: %v", names)
	}
	// pwm3: preset values, config name; the preset lacks hysteresis/min_on
	// → the config channel's post-processing stays
	if d := cfg.Channel("disks"); d.PWM != 3 || d.Critical != 60 || d.Sensor != "drivetemp:max" || d.Stop != "140" || d.Hysteresis != 2 || d.MinOn != 60*time.Second {
		t.Errorf("pwm3 not replaced by the preset (post-processing kept): %+v", d)
	}
	// pwm1: replaced (balanced cpu curve starts at 35)
	if c := cfg.Channel("cpu"); c.PWM != 1 || c.Curve[0][0] != 35 {
		t.Errorf("pwm1 not replaced: %+v", c)
	}
	// pwm4: untouched
	if p := cfg.Channel("pcie"); p.PWM != 4 || p.Critical != 90 || p.Curve[0][0] != 40 {
		t.Errorf("pwm4 changed by the merge: %+v", p)
	}
	// pwm2: added from the preset
	if s := cfg.Channel("ssd"); s.PWM != 2 || s.Critical != 72 {
		t.Errorf("pwm2 not added: %+v", s)
	}
	// the daemon saw the merged text
	if back, _, _ := config.Parse(svc.raws[0]); len(back.Channels) != 4 {
		t.Errorf("reload text has %d channels", len(back.Channels))
	}
}

// TestMergeChannelsByPWM: the pure merge, including a preset name that
// collides with a kept config channel on another pwm.
func TestMergeChannelsByPWM(t *testing.T) {
	cfgChans := []config.Channel{
		{Name: "cpu", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 88, Stop: "auto"},
		{Name: "ssd", PWM: 4, Sensor: "nvme:max", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 72, Stop: "auto"},
	}
	preset := []config.Channel{
		{Name: "processor", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{30, 60}, {80, 255}}, Critical: 85, Stop: "auto", Hysteresis: 3, PostSet: true},
		{Name: "ssd", PWM: 2, Sensor: "nvme:max", Curve: [][2]int{{35, 74}, {68, 255}}, Critical: 72, Stop: "auto"},
	}
	out := mergeChannelsByPWM(cfgChans, preset)
	if len(out) != 3 {
		t.Fatalf("merged: %+v", out)
	}
	if out[0].Name != "cpu" || out[0].PWM != 1 || out[0].Critical != 85 || out[0].Hysteresis != 3 {
		t.Errorf("pwm1: %+v", out[0])
	}
	if out[1].Name != "ssd" || out[1].PWM != 4 || out[1].Critical != 72 {
		t.Errorf("pwm4 must stay: %+v", out[1])
	}
	if out[2].Name != "pwm2" || out[2].PWM != 2 {
		t.Errorf("added channel with a colliding name must be renamed: %+v", out[2])
	}
	// the inputs are not aliased
	out[0].Curve[0][0] = 1
	if cfgChans[0].Curve[0][0] != 45 || preset[0].Curve[0][0] != 30 {
		t.Errorf("merge aliased its inputs")
	}
	if got := mergeChannelsByPWM(nil, preset); len(got) != 2 || got[0].Name != "processor" {
		t.Errorf("empty config: %+v", got)
	}
}

// TestMergeKeepsPostProcessing: a preset channel that says nothing about
// hysteresis/min_on (PostSet false — a preset written before 0.3.1, or one
// saved with the defaults) keeps the config channel's values; a preset
// channel that sets them (PostSet true) replaces them, also with 0.
func TestMergeKeepsPostProcessing(t *testing.T) {
	cfgChans := []config.Channel{
		{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{30, 90}, {50, 255}}, Critical: 60, Stop: "140", Hysteresis: 2, MinOn: 90 * time.Second, PostSet: true},
	}
	silent := []config.Channel{{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{28, 105}, {50, 255}}, Critical: 58, Stop: "140"}}
	out := mergeChannelsByPWM(cfgChans, silent)
	if len(out) != 1 || out[0].Critical != 58 || out[0].Hysteresis != 2 || out[0].MinOn != 90*time.Second || !out[0].PostSet {
		t.Errorf("silent preset must keep the config post-processing: %+v", out)
	}
	set := []config.Channel{{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{28, 105}, {50, 255}}, Critical: 58, Stop: "140", Hysteresis: 0, MinOn: 30 * time.Second, PostSet: true}}
	out = mergeChannelsByPWM(cfgChans, set)
	if len(out) != 1 || out[0].Hysteresis != 0 || out[0].MinOn != 30*time.Second {
		t.Errorf("explicit preset values must win: %+v", out)
	}
	// parsed text: the flag comes from the table's keys
	chans, _, err := config.ParseChannels([]byte("[[channel]]\nname = \"hdd\"\npwm = 3\nsensor = \"drivetemp:max\"\ncurve = [[30,90],[50,255]]\ncritical = 60\nhysteresis = 0\n"))
	if err != nil || len(chans) != 1 || !chans[0].PostSet {
		t.Fatalf("hysteresis = 0 must count as set: %+v %v", chans, err)
	}
	if out = mergeChannelsByPWM(cfgChans, chans); out[0].Hysteresis != 0 || out[0].MinOn != 0 {
		t.Errorf("explicit 0 must reset: %+v", out)
	}
	chans, _, _ = config.ParseChannels([]byte("[[channel]]\nname = \"hdd\"\npwm = 3\nsensor = \"drivetemp:max\"\ncurve = [[30,90],[50,255]]\ncritical = 60\n"))
	if len(chans) != 1 || chans[0].PostSet {
		t.Fatalf("no keys must read as not set: %+v", chans)
	}
	if out = mergeChannelsByPWM(cfgChans, chans); out[0].Hysteresis != 2 || out[0].MinOn != 90*time.Second {
		t.Errorf("parsed silent preset must keep: %+v", out)
	}
}

// TestMergeRenameUntilUnused: an added preset channel whose name collides
// with a kept config channel gets "pwm<N>", and when that is taken too,
// "pwm<N>_2", "pwm<N>_3", … — never a duplicate name.
func TestMergeRenameUntilUnused(t *testing.T) {
	ch := func(name string, pwm int) config.Channel {
		return config.Channel{Name: name, PWM: pwm, Sensor: "k10temp", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 88, Stop: "auto"}
	}
	cfgChans := []config.Channel{ch("ssd", 1), ch("pwm2", 3), ch("pwm2_2", 4)}
	preset := []config.Channel{ch("ssd", 2)}
	out := mergeChannelsByPWM(cfgChans, preset)
	if len(out) != 4 || out[3].PWM != 2 || out[3].Name != "pwm2_3" {
		t.Fatalf("merged: %+v", out)
	}
	seen := map[string]bool{}
	for _, c := range out {
		if seen[c.Name] {
			t.Errorf("duplicate name %q: %+v", c.Name, out)
		}
		seen[c.Name] = true
	}
	if back, warns, err := config.ParseChannels(config.MarshalChannels(out)); err != nil || len(warns) != 0 || len(back) != 4 {
		t.Errorf("merged set does not parse cleanly: %v %v", err, warns)
	}
}

// TestPresetApplyKeepsFileText: the apply splices the [[channel]] tables
// into the file text — comments, [[schedule]], [alert] with its webhook
// URL and the password_hash are byte-identical outside the channel
// blocks; a comment above the first [[channel]] stays above the new ones.
func TestPresetApplyKeepsFileText(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	src := `# n5-fangov config — hand-edited, keep my comments
[daemon]
interval = "10s"   # cycle

[web]
auth = "basic"
user = "admin"
password_hash = "` + keepHash + `"

[alert]
transport = "webhook"
webhook_url = "https://gotify.example.test/message?token=abc123"

# Fan channels (curves from the dashboard)
[[channel]]
name = "cpu"
pwm = 1
sensor = "k10temp"
curve = [[45,85],[80,255]]
critical = 88
stop = "auto"

[[channel]]
name = "disks"
pwm = 3
sensor = "drivetemp:max"
curve = [[36,105],[46,255]]
critical = 56
hysteresis = 2

# Night mode on the weekend
[[schedule]]
preset = "n5pro-quiet"
from = "22:00"
to = "07:00"
days = ["fri", "sat"]

[[schedule]]
preset = "n5pro-balanced"
`
	if err := os.WriteFile(cfgPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &fakeService{}
	s := dirPresetStore{dir: filepath.Join(t.TempDir(), "presets"), cfgPath: cfgPath, svc: svc, profile: "n5pro"}
	if err := s.Apply("n5pro-quiet"); err != nil || svc.reloads() != 1 {
		t.Fatalf("apply: %v", err)
	}
	got, _ := os.ReadFile(cfgPath)
	text := string(got)
	head := src[:strings.Index(src, "[[channel]]")]
	tail := src[strings.Index(src, "\n# Night mode"):]
	if !strings.HasPrefix(text, head) {
		t.Errorf("text before the channels changed:\n%s", text)
	}
	if !strings.HasSuffix(text, tail) {
		t.Errorf("text after the channels changed:\n%s", text)
	}
	if strings.Count(text, "[[channel]]") != 3 || strings.Count(text, "[[schedule]]") != 2 || strings.Count(text, "[alert]") != 1 {
		t.Errorf("tables:\n%s", text)
	}
	cfg, warns, err := config.Load(cfgPath)
	if err != nil || len(warns) != 0 {
		t.Fatalf("load: %v %v", err, warns)
	}
	if cfg.Alert.WebhookURL != "https://gotify.example.test/message?token=abc123" || cfg.Web.PasswordHash != keepHash || len(cfg.Schedules) != 2 || cfg.Schedules[0].Days[1] != "sat" {
		t.Errorf("other sections: %+v %+v %+v", cfg.Alert, cfg.Web, cfg.Schedules)
	}
	// the preset's pwm3 channel keeps the config name and hysteresis
	if d := cfg.Channel("disks"); d == nil || d.Critical != 66 || d.Hysteresis != 2 {
		t.Errorf("disks: %+v", d)
	}
	if len(cfg.Channels) != 3 || cfg.Channels[0].Name != "cpu" || cfg.Channels[1].Name != "disks" || cfg.Channels[2].Name != "ssd" {
		t.Errorf("channel order: %+v", cfg.Channels)
	}
	// the daemon saw the same text
	if string(svc.raws[0]) != text {
		t.Errorf("reload text differs from the file")
	}
}

// TestPresetApplyReloadError: a preset that is written but not taken
// by the daemon comes back as web.ReloadError with the "written, reload
// failed" text — the file carries the preset; the restart sentinel passes
// through unchanged.
func TestPresetApplyReloadError(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	dir := filepath.Join(t.TempDir(), "presets")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "mine.toml"), []byte(quietPreset), 0o644)
	svc := &fakeService{err: errors.New("channel cpu: pwm1_enable not writable")}
	s := dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: "n5pro"}
	err := s.Apply("mine")
	var re *web.ReloadError
	if !errors.As(err, &re) || err.Error() != "preset mine written, reload failed: channel cpu: pwm1_enable not writable" {
		t.Fatalf("apply: %v", err)
	}
	if isRestartRequired(err) {
		t.Fatal("reload error reads as restart required")
	}
	if cfg, _, _ := config.Load(cfgPath); cfg.Channel("cpu") == nil || cfg.Channel("cpu").Critical != 90 {
		t.Fatalf("preset not written before the reload: %+v", cfg.Channels)
	}
	svc.err = errRestartRequired()
	if err := s.Apply("mine"); !isRestartRequired(err) || errors.As(err, &re) {
		t.Fatalf("restart sentinel: %v", err)
	}
	if svc.reloads() != 2 {
		t.Fatalf("reloads = %d", svc.reloads())
	}
}
