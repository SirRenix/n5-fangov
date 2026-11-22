package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/SirRenix/n5-fangov/internal/fsutil"
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
// existing one keeps its mode — unless raw carries a password hash:
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

// writeAtomic creates the directory and writes via fsutil.WriteAtomic.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(path, data, perm)
}

// SetKey returns raw with `key = value` set in the top-level table
// [section], leaving everything else (comments, order, other tables)
// untouched. An existing assignment of key inside that section is replaced
// in place (a trailing comment on that line is dropped); otherwise the line
// is appended at the end of the section, before its trailing blank lines.
// Without a [section] header the dotted layout is handled: a
// top-level `section.key = …` line is replaced in place, and when other
// `section.*` lines exist there the new one is inserted after the last of
// them (a [section] header appended after dotted keys would redefine the
// table and break the parse). Only then is a missing section appended at
// the end of the file. An inline table (`section = { … }`) is not edited;
// callers verify the result with Parse. value must already be a TOML
// literal (`"text"`, `5`, `["a"]`). Only used for the few single-key edits
// (passwd, the account/alert/dashboard stores); everything else goes
// through Marshal.
func SetKey(raw []byte, section, key, value string) []byte {
	text := string(raw)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	header := "[" + section + "]"
	start, end := -1, len(lines) // section body is lines[start+1:end]
	first := len(lines)          // first table header: the top-level region ends there
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "[") {
			continue
		}
		if first == len(lines) {
			first = i
		}
		if start >= 0 {
			end = i
			break
		}
		if strings.HasPrefix(t, header) && (len(t) == len(header) || t[len(header)] == ' ' || t[len(header)] == '#') {
			start = i
		}
	}
	join := func(ls []string) []byte { return []byte(strings.Join(ls, "\n") + "\n") }
	insertAt := func(i int, line string) []byte {
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:i]...)
		out = append(out, line)
		out = append(out, lines[i:]...)
		return join(out)
	}
	newLine := key + " = " + value
	if start < 0 {
		dotted := section + "." + key
		last := -1
		for i := 0; i < first; i++ {
			k := assignedKey(lines[i])
			if k == dotted {
				lines[i] = dotted + " = " + value
				return join(lines)
			}
			if strings.HasPrefix(k, section+".") {
				last = i
			}
		}
		if last >= 0 {
			return insertAt(last+1, dotted+" = "+value)
		}
		out := lines
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, header, newLine)
		return join(out)
	}
	for i := start + 1; i < end; i++ {
		if assignedKey(lines[i]) == key {
			lines[i] = newLine
			return join(lines)
		}
	}
	ins := end
	for ins > start+1 && strings.TrimSpace(lines[ins-1]) == "" {
		ins--
	}
	return insertAt(ins, newLine)
}

// ReplaceChannels returns raw with every [[channel]] table replaced by
// MarshalChannels(chans), everything else byte-identical (comments,
// [[schedule]], [[assist]], [alert], password_hash, key order). A channel block runs
// from its header line to its last key line before the next top-level
// header that is not a channel table (or EOF): the blank and comment
// lines between that key and the next header stay, so a comment above a
// [[channel]] header or above the section that follows keeps its place.
// The new tables go where the first block was (a comment right above it
// still reads as the channel heading), or at the end of the file when
// there was none. An inline `channel = [{…}]` at the top level is not a
// header and is not touched; callers verify the result with Parse.
func ReplaceChannels(raw []byte, chans []Channel) []byte {
	text := string(raw)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	// Pass 1: the line ranges of the channel blocks. A block opens at a
	// [[channel]] header and closes at its last key line; a [channel.x]
	// sub-table header extends the open block, any other header or a
	// further [[channel]] header closes it.
	removed := make([]bool, len(lines))
	first := -1
	start, last := -1, -1 // open block: header line, last key line
	closeBlock := func() {
		if start < 0 {
			return
		}
		for i := start; i <= last; i++ {
			removed[i] = true
		}
		if first < 0 {
			first = start
		}
		start, last = -1, -1
	}
	for i, ln := range lines {
		name, isHeader := tableHeader(ln)
		switch {
		case isHeader && name == "channel":
			closeBlock()
			start, last = i, i
		case isHeader && strings.HasPrefix(name, "channel."):
			if start < 0 {
				start = i
			}
			last = i
		case isHeader:
			closeBlock()
		case start >= 0:
			if t := strings.TrimSpace(ln); t != "" && !strings.HasPrefix(t, "#") {
				last = i
			}
		}
	}
	closeBlock()
	// Pass 2: copy what stays, the new tables at the first block's place
	// or at the end.
	body := strings.TrimSuffix(string(MarshalChannels(chans)), "\n")
	out := make([]string, 0, len(lines)+strings.Count(body, "\n")+2)
	for i, ln := range lines {
		if i == first {
			out = append(out, body)
		}
		if !removed[i] {
			out = append(out, ln)
		}
	}
	if first < 0 {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, body)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// SetAssistEnabled returns raw with the enabled key of the [[assist]] table
// for channel set to on, everything else byte-identical (comments, the
// other tables, the [[assist.source]] tables, key order). An existing
// `enabled = …` line of that table is rewritten in place, its indentation
// and trailing comment kept; without one the key is inserted after the
// table's channel line. The table's own keys are the lines between its
// [[assist]] header and the next header ([[assist.source]] included).
// found is false (raw returned unchanged) when no [[assist]] header is
// followed by `channel = "<channel>"`. An inline or dotted layout is not
// edited; callers verify the result with Parse.
func SetAssistEnabled(raw []byte, channel string, on bool) (out []byte, found bool) {
	text := string(raw)
	if text == "" {
		return raw, false
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	val := "false"
	if on {
		val = "true"
	}
	for i := 0; i < len(lines); i++ {
		if name, ok := tableHeader(lines[i]); !ok || name != "assist" || !strings.HasPrefix(strings.TrimSpace(lines[i]), "[[") {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if _, ok := tableHeader(lines[j]); ok {
				end = j
				break
			}
		}
		chLine, enLine := -1, -1
		for j := i + 1; j < end; j++ {
			switch assignedKey(lines[j]) {
			case "channel":
				if v, ok := tomlStringValue(lines[j]); ok && v == channel {
					chLine = j
				}
			case "enabled":
				enLine = j
			}
		}
		if chLine < 0 {
			i = end - 1
			continue
		}
		if enLine >= 0 {
			body, cr := strings.CutSuffix(lines[enLine], "\r") // a CRLF file keeps its line ending
			lines[enLine] = replaceValue(body, val) + map[bool]string{true: "\r"}[cr]
		} else {
			indent := lines[chLine][:len(lines[chLine])-len(strings.TrimLeft(lines[chLine], " \t"))]
			eol := ""
			if strings.HasSuffix(lines[chLine], "\r") {
				eol = "\r"
			}
			lines = slices.Insert(lines, chLine+1, indent+"enabled = "+val+eol)
		}
		return []byte(strings.Join(lines, "\n") + "\n"), true
	}
	return raw, false
}

// tomlStringValue returns the quoted string value of a `key = "v"` or
// `key = 'v'` line.
func tomlStringValue(line string) (string, bool) {
	_, v, ok := strings.Cut(line, "=")
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if len(v) < 2 || (v[0] != '"' && v[0] != '\'') {
		return "", false
	}
	end := strings.IndexByte(v[1:], v[0])
	if end < 0 {
		return "", false
	}
	return v[1 : end+1], true
}

// replaceValue swaps the bare value token of a `key = value  # comment`
// line for val, keeping the indentation, the spacing and the comment.
func replaceValue(line, val string) string {
	k, rest, _ := strings.Cut(line, "=")
	lead := rest[:len(rest)-len(strings.TrimLeft(rest, " \t"))]
	if lead == "" {
		lead = " "
	}
	tok := strings.TrimLeft(rest, " \t")
	n := strings.IndexAny(tok, " \t#")
	if n < 0 {
		n = len(tok)
	}
	return k + "=" + lead + val + tok[n:]
}

// tableHeader parses a `[name]` or `[[name]]` line into its table name
// (trailing comment allowed); ok is false for anything else.
func tableHeader(line string) (name string, ok bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") {
		return "", false
	}
	if strings.HasPrefix(t, "[[") {
		end := strings.Index(t, "]]")
		if end < 0 {
			return "", false
		}
		return strings.TrimSpace(t[2:end]), true
	}
	end := strings.IndexByte(t, ']')
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(t[1:end]), true
}

// assignedKey returns the bare key of a `key = value` line ("" for
// comments, blank lines and anything without "=").
func assignedKey(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return ""
	}
	k, _, ok := strings.Cut(t, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(k)
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
