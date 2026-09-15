package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultPath is the config file location on a normal install.
const DefaultPath = "/etc/pvefand/config.toml"

// DefaultPresetDir holds preset files (<name>.toml with [[channel]] tables only).
const DefaultPresetDir = "/etc/pvefand/presets"

// Load reads and parses path. The returned Config is always usable:
//   - file missing: Default(), one warning, err == nil
//   - unreadable or TOML syntax error: Default(), warning, err != nil
//   - invalid values: defaults per field, warnings, err == nil
func Load(path string) (Config, []Warning, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), []Warning{{Field: "file", Msg: path + " not found, built-in defaults active"}}, nil
		}
		return Default(), []Warning{{Field: "file", Msg: err.Error()}}, fmt.Errorf("config: %w", err)
	}
	return Parse(raw)
}

// Save writes raw atomically (temp file + rename in the same directory).
// It does not validate; callers run Parse first.
func Save(path string, raw []byte) error {
	return writeAtomic(path, raw, 0o644)
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// ValidPresetName reports whether name is usable as a preset file stem.
func ValidPresetName(name string) bool { return presetRe.MatchString(name) }

// LoadPresets reads every <name>.toml in dir. Files that do not parse are
// skipped; per-field warnings are applied silently (use LoadPreset for them).
// A missing directory yields an empty map.
func LoadPresets(dir string) map[string][]Channel {
	out := map[string][]Channel{}
	for _, name := range PresetNames(dir) {
		chans, _, err := LoadPreset(dir, name)
		if err == nil {
			out[name] = chans
		}
	}
	return out
}

// PresetNames lists the preset names in dir, sorted.
func PresetNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		if ValidPresetName(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// LoadPreset reads one preset with its warnings.
func LoadPreset(dir, name string) ([]Channel, []Warning, error) {
	if !ValidPresetName(name) {
		return nil, nil, fmt.Errorf("preset: invalid name %q", name)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name+".toml"))
	if err != nil {
		return nil, nil, fmt.Errorf("preset: %w", err)
	}
	return ParseChannels(raw)
}

// SavePreset writes chans as <dir>/<name>.toml atomically.
func SavePreset(dir, name string, chans []Channel) error {
	if !ValidPresetName(name) {
		return fmt.Errorf("preset: invalid name %q (use [a-z0-9_-]+)", name)
	}
	return writeAtomic(filepath.Join(dir, name+".toml"), MarshalChannels(chans), 0o644)
}

// DeletePreset removes <dir>/<name>.toml.
func DeletePreset(dir, name string) error {
	if !ValidPresetName(name) {
		return fmt.Errorf("preset: invalid name %q", name)
	}
	return os.Remove(filepath.Join(dir, name+".toml"))
}
