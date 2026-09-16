package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/web"
)

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
	// pwm3: preset values, config name; the preset lacks hysteresis/min_on → defaults
	if d := cfg.Channel("disks"); d.PWM != 3 || d.Critical != 60 || d.Sensor != "drivetemp:max" || d.Stop != "140" || d.Hysteresis != 0 || d.MinOn != 0 {
		t.Errorf("pwm3 not replaced by the preset: %+v", d)
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
		{Name: "processor", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{30, 60}, {80, 255}}, Critical: 85, Stop: "auto", Hysteresis: 3},
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
