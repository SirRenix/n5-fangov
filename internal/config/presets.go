package config

import (
	"bufio"
	"bytes"
	"embed"
	"path"
	"sort"
	"strings"
)

// Built-in presets (DESIGN "Presets: built-in N5 Pro sets"): TOML texts with
// only [[channel]] tables, embedded so install.sh ships no preset files.
// Each file carries a header comment with `# description: ...` (shown in
// the dashboard) and `# profile: <name>` (the profile it is listed for).
//
//go:embed presets/*.toml
var presetFS embed.FS

// BuiltinPreset is one embedded preset.
type BuiltinPreset struct {
	Name        string // file stem, e.g. "n5pro-quiet"
	Description string // from the "# description:" header line
	Profile     string // from the "# profile:" header line
	Raw         []byte // the TOML text (ParseChannels input)
}

// builtinPresets is filled once from presetFS, sorted by name.
var builtinPresets = loadBuiltinPresets()

func loadBuiltinPresets() []BuiltinPreset {
	entries, err := presetFS.ReadDir("presets")
	if err != nil {
		return nil
	}
	var out []BuiltinPreset
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		raw, err := presetFS.ReadFile(path.Join("presets", e.Name()))
		if err != nil {
			continue
		}
		p := BuiltinPreset{Name: strings.TrimSuffix(e.Name(), ".toml"), Raw: raw}
		p.Description, p.Profile = presetHeader(raw)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// presetHeader reads the "# description:" and "# profile:" lines of the
// leading comment block.
func presetHeader(raw []byte) (description, profile string) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break // end of the header block
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		switch {
		case strings.HasPrefix(body, "description:"):
			description = strings.TrimSpace(strings.TrimPrefix(body, "description:"))
		case strings.HasPrefix(body, "profile:"):
			profile = strings.TrimSpace(strings.TrimPrefix(body, "profile:"))
		}
	}
	return description, profile
}

// BuiltinPresets returns the embedded presets for profile ("" = all),
// sorted by name. The returned slices are copies.
func BuiltinPresets(profile string) []BuiltinPreset {
	var out []BuiltinPreset
	for _, p := range builtinPresets {
		if profile != "" && p.Profile != profile {
			continue
		}
		p.Raw = append([]byte(nil), p.Raw...)
		out = append(out, p)
	}
	return out
}

// BuiltinPresetByName returns the embedded preset with that name (any profile).
func BuiltinPresetByName(name string) (BuiltinPreset, bool) {
	for _, p := range builtinPresets {
		if p.Name == name {
			p.Raw = append([]byte(nil), p.Raw...)
			return p, true
		}
	}
	return BuiltinPreset{}, false
}

// IsBuiltinPreset reports whether name is an embedded preset (of any
// profile): such a name can never be saved over or deleted.
func IsBuiltinPreset(name string) bool {
	_, ok := BuiltinPresetByName(name)
	return ok
}
