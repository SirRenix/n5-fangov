// wiring_presets.go holds the preset store behind /api/presets: user files
// under /etc/n5-fangov/presets plus the built-in presets embedded for the
// active profile (list, apply, save, delete, detail, rename).
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// Preset directory helpers (bundle export/import works on the raw files).
func presetNames(dir string) []string    { return config.PresetNames(dir) }
func validPresetName(name string) bool   { return config.ValidPresetName(name) }
func presetPath(dir, name string) string { return filepath.Join(dir, name+".toml") }

// parsePresetRaw validates preset text and returns its channel count.
func parsePresetRaw(raw []byte) (int, error) {
	chans, _, err := config.ParseChannels(raw)
	return len(chans), err
}

// dirPresetStore backs /api/presets with <dir>/<name>.toml files that hold
// only [[channel]] tables, plus the built-in presets embedded for the
// active profile (config.BuiltinPresets): listed with Builtin=true and
// their description, applied like a file, never saved over or deleted.
// A file whose name collides with a built-in is shadowed (the built-in
// wins) and logged once per List.
type dirPresetStore struct {
	dir     string
	cfgPath string
	svc     control.Service
	pin     func([]byte) []byte // see fileConfigStore
	profile string              // active profile name (built-in filter); "" = none
}

// List returns the built-in presets of the active profile first, then
// every user preset with the channel names it contains.
func (s dirPresetStore) List() ([]web.Preset, error) {
	var out []web.Preset
	for _, b := range builtinPresetsFor(s.profile) {
		chans, _, err := config.ParseChannels(b.Raw)
		if err != nil {
			continue
		}
		out = append(out, web.Preset{Name: b.Name, Channels: channelNames(chans), Builtin: true, Description: b.Description})
	}
	all := config.LoadPresets(s.dir)
	for _, name := range config.PresetNames(s.dir) {
		chans, ok := all[name]
		if !ok {
			continue // did not parse; LoadPresets skipped it
		}
		if isBuiltinPreset(name) {
			log.Printf("preset %s: %s/%s.toml is shadowed by the built-in preset of that name", name, s.dir, name)
			continue
		}
		out = append(out, web.Preset{Name: name, Channels: channelNames(chans)})
	}
	if out == nil {
		out = []web.Preset{}
	}
	return out, nil
}

func channelNames(chans []config.Channel) []string {
	names := make([]string, 0, len(chans))
	for _, c := range chans {
		names = append(names, c.Name)
	}
	return names
}

// load returns the channel tables of a preset: the embedded text for a
// built-in name of the active profile, else the file. A built-in of
// another profile is not applied: it is not listed either, and its
// channels name pwm outputs this device may not have — fs.ErrNotExist,
// which the web layer answers with 404. A user file of that name is
// shadowed by the built-in in List and therefore not applied here either.
func (s dirPresetStore) load(name string) ([]config.Channel, []config.Warning, error) {
	if raw, prof, ok := builtinPresetRaw(name); ok {
		if prof != s.profile {
			return nil, nil, fmt.Errorf("preset %q: built-in for profile %s, not %s: %w", name, prof, s.profile, fs.ErrNotExist)
		}
		return config.ParseChannels(raw)
	}
	return config.LoadPreset(s.dir, name)
}

// Apply merges the [[channel]] tables of the preset into the config file
// by pwm (mergeChannelsByPWM), writes the file and reloads the daemon. The
// file is rewritten from the parsed config, so comments in it are lost.
// control.ErrRestartRequired passes through unchanged (the web layer
// answers 202 for it; with the merge that only happens when the preset
// names a pwm the config lacks); any other reload failure comes back as
// web.ReloadError — "preset written, reload failed" — so it is told apart
// from a write error.
func (s dirPresetStore) Apply(name string) error {
	chans, warns, err := s.load(name)
	if err != nil {
		return err
	}
	for _, w := range warns {
		log.Printf("preset %s: %s", name, w)
	}
	if len(chans) == 0 {
		return fmt.Errorf("preset %q contains no usable [[channel]] table", name)
	}
	raw, n, err := s.write(name, chans)
	if err != nil {
		return err
	}
	log.Printf("preset %s applied to %s (%d channels, %d in the file)", name, s.cfgPath, len(chans), n)
	if err := s.svc.Reload(raw); err != nil {
		if isRestartRequired(err) {
			return err
		}
		return fmt.Errorf("preset %s written, %w", name, &web.ReloadError{Err: err})
	}
	return nil
}

// mergeChannelsByPWM applies the preset channels to the config channels
// (DESIGN "Presets: Apply merges by pwm"): a preset channel replaces the
// config channel with the same pwm, keeping the config channel's name (the
// daemon's channel set is keyed by name and pwm, so a renamed channel would
// force a restart); config channels the preset does not name stay as they
// are (an optional pwm4 channel survives a built-in preset); preset
// channels whose pwm the config lacks are appended. A preset name that
// collides with a kept config channel of another pwm is replaced by
// "pwm<N>" so the file stays valid. Order: config channels first (in their
// order), then the additions in preset order.
func mergeChannelsByPWM(cfgChans, preset []config.Channel) []config.Channel {
	out := config.CloneChannels(cfgChans)
	if out == nil {
		out = []config.Channel{}
	}
	byPWM := map[int]int{}
	for i, ch := range out {
		byPWM[ch.PWM] = i
	}
	var adds []config.Channel
	for _, pc := range config.CloneChannels(preset) {
		if i, ok := byPWM[pc.PWM]; ok {
			pc.Name = out[i].Name
			out[i] = pc
			continue
		}
		adds = append(adds, pc)
	}
	names := map[string]bool{}
	for _, ch := range out {
		names[ch.Name] = true
	}
	for _, pc := range adds {
		if names[pc.Name] {
			pc.Name = fmt.Sprintf("pwm%d", pc.PWM)
		}
		names[pc.Name] = true
		out = append(out, pc)
	}
	return out
}

// write is Apply's read-modify-write of the config file, under the file
// lock. It returns the written text and the number of channels in it.
func (s dirPresetStore) write(name string, chans []config.Channel) ([]byte, int, error) {
	configFileMu.Lock()
	defer configFileMu.Unlock()
	cfg, _, err := config.Load(s.cfgPath)
	if err != nil {
		return nil, 0, fmt.Errorf("current config: %w", err)
	}
	cfg.Channels = mergeChannelsByPWM(cfg.Channels, chans)
	raw := config.Marshal(cfg)
	if s.pin != nil {
		raw = s.pin(raw)
	}
	if err := config.Save(s.cfgPath, raw); err != nil {
		return nil, 0, err
	}
	return raw, len(cfg.Channels), nil
}

// Save stores the channel tables of the current config file as preset
// name. A built-in name is refused (web.ErrPresetBuiltin → 409).
func (s dirPresetStore) Save(name string) error {
	if isBuiltinPreset(name) {
		return fmt.Errorf("preset %q: %w", name, errPresetBuiltin())
	}
	cfg, _, err := config.Load(s.cfgPath)
	if err != nil {
		return fmt.Errorf("current config: %w", err)
	}
	if len(cfg.Channels) == 0 {
		return errors.New("current config has no channels to save")
	}
	return config.SavePreset(s.dir, name, cfg.Channels)
}

// Delete removes a user preset file (web.PresetDeleter). A built-in name
// → web.ErrPresetBuiltin (409), a missing file → fs.ErrNotExist (404).
func (s dirPresetStore) Delete(name string) error {
	if isBuiltinPreset(name) {
		return fmt.Errorf("preset %q: %w", name, errPresetBuiltin())
	}
	if err := deletePreset(s.dir, name); err != nil {
		return err
	}
	log.Printf("preset %s deleted from %s", name, s.dir)
	return nil
}

// Detail returns a preset's channel tables (web.PresetDetailer): the
// operator wants to see what a saved preset contains, not only its names.
func (s dirPresetStore) Detail(name string) (web.PresetDetail, error) {
	chans, _, err := s.load(name)
	if err != nil {
		return web.PresetDetail{}, err
	}
	det := web.PresetDetail{Name: name, Channels: make([]web.PresetChannel, 0, len(chans))}
	for _, b := range builtinPresetsFor(s.profile) {
		if b.Name == name {
			det.Builtin, det.Description = true, b.Description
		}
	}
	for _, c := range chans {
		det.Channels = append(det.Channels, web.PresetChannel{
			Name: c.Name, PWM: c.PWM, Sensor: c.Sensor, Curve: c.Curve, Critical: c.Critical, Stop: c.Stop,
			Hysteresis: c.Hysteresis, MinOn: c.MinOn.String(),
		})
	}
	return det, nil
}

// Rename moves a user preset file (web.PresetRenamer). Built-in names are
// refused on both sides (409), a taken target is never overwritten
// (fs.ErrExist → 409), a missing source is fs.ErrNotExist (404).
func (s dirPresetStore) Rename(oldName, newName string) error {
	if !config.ValidPresetName(oldName) || !config.ValidPresetName(newName) {
		return fmt.Errorf("preset: invalid name")
	}
	if isBuiltinPreset(oldName) || isBuiltinPreset(newName) {
		return fmt.Errorf("preset %q -> %q: %w", oldName, newName, errPresetBuiltin())
	}
	src, dst := presetPath(s.dir, oldName), presetPath(s.dir, newName)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("preset %q: %w", oldName, err)
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("preset %q: %w", newName, fs.ErrExist)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("preset rename: %w", err)
	}
	log.Printf("preset %s renamed to %s in %s", oldName, newName, s.dir)
	return nil
}

// ---------------------------------------------------------------------------
// Built-in presets (dirPresetStore helpers).

// builtinPresetInfo is one embedded preset as the store sees it.
type builtinPresetInfo struct {
	Name        string
	Description string
	Raw         []byte
}

// builtinPresetsFor lists the embedded presets of profileName.
func builtinPresetsFor(profileName string) []builtinPresetInfo {
	var out []builtinPresetInfo
	for _, p := range config.BuiltinPresets(profileName) {
		out = append(out, builtinPresetInfo{Name: p.Name, Description: p.Description, Raw: p.Raw})
	}
	return out
}

func isBuiltinPreset(name string) bool { return config.IsBuiltinPreset(name) }

// builtinPresetRaw returns the embedded preset text and the profile it is
// meant for, for any profile.
func builtinPresetRaw(name string) (raw []byte, profile string, ok bool) {
	p, ok := config.BuiltinPresetByName(name)
	return p.Raw, p.Profile, ok
}

func errPresetBuiltin() error { return web.ErrPresetBuiltin }

// deletePreset removes a user preset file; fs.ErrNotExist passes through.
func deletePreset(dir, name string) error {
	err := config.DeletePreset(dir, name)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("preset %q: %w", name, fs.ErrNotExist)
	}
	return err
}
