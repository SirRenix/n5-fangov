package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultPath is the config file location on a normal install.
const DefaultPath = "/etc/n5-fangov/config.toml"

// DefaultPresetDir holds preset files (<name>.toml with [[channel]] tables only).
const DefaultPresetDir = "/etc/n5-fangov/presets"

// ErrUnreadable marks a Load error caused by the file itself (exists but
// cannot be read), as opposed to a TOML syntax error. `n5-fangov check`
// treats only the former as fatal: with a syntax error serve runs on the
// built-in defaults (rule 8), with an unreadable file the operator's
// intent is unknown.
var ErrUnreadable = errors.New("config file unreadable")

// Load reads and parses path. The returned Config is always usable:
//   - file missing: Default(), one warning, err == nil
//   - unreadable: Default(), warning, err wraps ErrUnreadable
//   - TOML syntax error: Default(), warning, err != nil
//   - invalid values: defaults per field, warnings, err == nil
func Load(path string) (Config, []Warning, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), []Warning{{Field: "file", Msg: path + " not found, built-in defaults active"}}, nil
		}
		return Default(), []Warning{{Field: "file", Msg: err.Error()}}, fmt.Errorf("config: %w: %w", ErrUnreadable, err)
	}
	return Parse(raw)
}

// Logf receives the one-line notice when Save tightens the mode of an
// existing config file. nil → log.Printf.
var Logf func(format string, args ...any)

func logf(format string, args ...any) {
	if Logf != nil {
		Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// secretLine matches a non-empty password_hash assignment in either quote
// style (also the dotted web.password_hash form).
var secretLine = regexp.MustCompile(`(?m)^[ \t]*(?:web\.)?password_hash[ \t]*=[ \t]*("[^"\n]+"|'[^'\n]+')`)

// HasSecret reports whether raw carries a password hash: the parsed value
// when the text parses, else a line-form assignment (Save is also called
// with text that does not parse).
func HasSecret(raw []byte) bool {
	if cfg, _, err := Parse(raw); err == nil {
		return cfg.Web.PasswordHash != ""
	}
	return secretLine.Match(raw)
}

// Save writes raw atomically (temp file + rename in the same directory).
// It does not validate; callers run Parse first. A new file is 0600, an
// existing one keeps its mode — unless raw carries a password hash (M3):
// then the file is 0600 regardless, and a file that was wider before is
// logged once as tightened. The daemon and the CLI run as root; nothing
// else needs to read the file.
func Save(path string, raw []byte) error {
	perm := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
		perm = st.Mode().Perm()
	}
	if HasSecret(raw) && perm&0o077 != 0 {
		logf("config: %s carries password_hash, mode %04o tightened to 0600", path, perm)
		perm = 0o600
	}
	return writeAtomic(path, raw, perm)
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

// SetKey returns raw with `key = value` set in the top-level table
// [section], leaving everything else (comments, order, other tables)
// untouched. An existing assignment of key inside that section is replaced
// in place (a trailing comment on that line is dropped); otherwise the line
// is appended at the end of the section, before its trailing blank lines. A
// missing section is appended at the end of the file. value must already
// be a TOML literal (`"text"`, `5`, `["a"]`). Only used for the few
// single-key edits of the CLI (passwd); everything else goes through
// Marshal.
func SetKey(raw []byte, section, key, value string) []byte {
	text := string(raw)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	header := "[" + section + "]"
	start, end := -1, len(lines) // section body is lines[start+1:end]
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "[") {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		if strings.HasPrefix(t, header) && (len(t) == len(header) || t[len(header)] == ' ' || t[len(header)] == '#') {
			start = i
		}
	}
	newLine := key + " = " + value
	if start < 0 {
		out := lines
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, header, newLine)
		return []byte(strings.Join(out, "\n") + "\n")
	}
	for i := start + 1; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, key) {
			rest := strings.TrimSpace(t[len(key):])
			if strings.HasPrefix(rest, "=") {
				lines[i] = newLine
				return []byte(strings.Join(lines, "\n") + "\n")
			}
		}
	}
	ins := end
	for ins > start+1 && strings.TrimSpace(lines[ins-1]) == "" {
		ins--
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:ins]...)
	out = append(out, newLine)
	out = append(out, lines[ins:]...)
	return []byte(strings.Join(out, "\n") + "\n")
}

// ValidPresetName reports whether name is usable as a preset file stem
// ([a-z0-9_-], 1..64 characters; the web layer applies the same rule).
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
		return fmt.Errorf("preset: invalid name %q (use [a-z0-9_-], 1..64 characters)", name)
	}
	return writeAtomic(filepath.Join(dir, name+".toml"), MarshalChannels(chans), 0o644)
}

// SavePresetRaw writes raw preset text as <dir>/<name>.toml atomically. It
// does not validate the content; callers run ParseChannels first.
func SavePresetRaw(dir, name string, raw []byte) error {
	if !ValidPresetName(name) {
		return fmt.Errorf("preset: invalid name %q (use [a-z0-9_-], 1..64 characters)", name)
	}
	return writeAtomic(filepath.Join(dir, name+".toml"), raw, 0o644)
}

// DeletePreset removes <dir>/<name>.toml.
func DeletePreset(dir, name string) error {
	if !ValidPresetName(name) {
		return fmt.Errorf("preset: invalid name %q", name)
	}
	return os.Remove(filepath.Join(dir, name+".toml"))
}
