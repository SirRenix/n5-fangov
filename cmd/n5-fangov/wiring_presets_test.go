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

// TestPresetBuiltinOtherProfile (R-L8): a built-in of another profile is
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

// TestPresetApplyReloadError (L7): a preset that is written but not taken
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
