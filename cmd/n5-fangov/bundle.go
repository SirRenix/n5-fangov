package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/version"
)

func init() {
	register("export", command{run: cmdExport})
	register("import", command{run: cmdImport})
}

// bundleFormat is the "format" field of a settings bundle.
const bundleFormat = 1

// settingsBundle is the JSON document of GET /api/config/export and
// `n5-fangov export`: the config file text (password hash redacted) and
// every preset file text.
type settingsBundle struct {
	Format   int               `json:"format"`
	Version  string            `json:"version"`
	Exported int64             `json:"exported"`
	Config   string            `json:"config"`
	Presets  map[string]string `json:"presets"`
}

// fileBundle implements bundle on the config file and preset directory.
// reload is Service.Reload in the daemon, a socket call in the CLI, or nil
// when no daemon is reachable (files are written, nothing is reloaded).
type fileBundle struct {
	cfgPath   string
	presetDir string
	reload    func(raw []byte) error
}

// Export builds the bundle. A missing config file exports the built-in
// defaults; the stored password hash is replaced by redactedHash wherever
// it appears in the text.
func (b fileBundle) Export() ([]byte, error) {
	raw, err := os.ReadFile(b.cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = defaultConfigRaw(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg, _ := parseConfig(raw)
	if h := webOf(cfg).PasswordHash; h != "" {
		raw = []byte(strings.ReplaceAll(string(raw), h, redactedHash))
	}
	out := settingsBundle{
		Format:   bundleFormat,
		Version:  version.Version,
		Exported: time.Now().Unix(),
		Config:   string(raw),
		Presets:  map[string]string{},
	}
	for _, name := range presetNames(b.presetDir) {
		p, err := os.ReadFile(presetPath(b.presetDir, name))
		if err != nil {
			return nil, fmt.Errorf("preset %s: %w", name, err)
		}
		out.Presets[name] = string(p)
	}
	return json.MarshalIndent(out, "", "  ")
}

// Import validates every part of the bundle (config syntax, preset names
// and syntax, password placeholder resolvable) before it writes anything,
// then writes presets and config atomically and reloads the daemon.
// restartRequired is true when the daemon says the channel set or profile
// changed. Presets present locally but absent from the bundle stay.
func (b fileBundle) Import(data []byte) (bool, error) {
	var in settingsBundle
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return false, fmt.Errorf("bundle: not a settings bundle: %v", err)
	}
	if in.Format != bundleFormat {
		return false, fmt.Errorf("bundle: format %d not supported (want %d)", in.Format, bundleFormat)
	}
	if strings.TrimSpace(in.Config) == "" {
		return false, errors.New("bundle: config is empty")
	}
	var errs []string
	raw := in.Config
	if strings.Contains(raw, redactedHash) {
		cur, _, _ := loadConfig(b.cfgPath)
		h := webOf(cur).PasswordHash
		if h == "" {
			errs = append(errs, "config keeps the current password ("+redactedHash+") but none is stored here; run `n5-fangov passwd` first or put a password_hash into the bundle")
		} else {
			raw = strings.ReplaceAll(raw, redactedHash, h)
		}
	}
	_, warns, err := parseConfigErr([]byte(raw))
	if err != nil {
		errs = append(errs, "config: "+err.Error())
	}
	names := make([]string, 0, len(in.Presets))
	for name := range in.Presets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !validPresetName(name) {
			errs = append(errs, fmt.Sprintf("preset %q: invalid name (use [a-z0-9_-], 1..64 characters)", name))
			continue
		}
		n, err := parsePresetRaw([]byte(in.Presets[name]))
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("preset %s: %v", name, err))
		case n == 0:
			errs = append(errs, fmt.Sprintf("preset %s: no [[channel]] table", name))
		}
	}
	if len(errs) > 0 {
		return false, &bundleError{errs}
	}
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "import: config warning: %s\n", w)
	}
	for _, name := range names {
		if err := savePresetRaw(b.presetDir, name, []byte(in.Presets[name])); err != nil {
			return false, fmt.Errorf("preset %s: %w", name, err)
		}
	}
	if err := saveConfig(b.cfgPath, []byte(raw)); err != nil {
		return false, fmt.Errorf("config: %w", err)
	}
	if b.reload == nil {
		return false, nil
	}
	err = b.reload([]byte(raw))
	switch {
	case err == nil:
		return false, nil
	case isRestartRequired(err):
		return true, nil
	default:
		return false, fmt.Errorf("files written, but the daemon did not reload: %w", err)
	}
}

// bundleError carries every validation problem of an import so the API
// can answer 400 with the full list.
type bundleError struct{ errs []string }

func (e *bundleError) Error() string { return strings.Join(e.errs, "; ") }

// Errors returns the individual problems (web layer: "errors": [...]).
func (e *bundleError) Errors() []string { return e.errs }

// ---------------------------------------------------------------------------
// CLI

func cmdExport(args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	presetDir := fs.String("presets", defaultPresetDir, "preset directory")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov export [FILE]")
		return exitUsage
	}
	data, err := fileBundle{cfgPath: *cfgPath, presetDir: *presetDir}.Export()
	if err != nil {
		fmt.Fprintln(os.Stderr, "export:", err)
		return exitFail
	}
	data = append(data, '\n')
	if fs.NArg() == 0 || fs.Arg(0) == "-" {
		os.Stdout.Write(data)
		return exitOK
	}
	if err := os.WriteFile(fs.Arg(0), data, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "export:", err)
		return exitFail
	}
	fmt.Printf("settings written to %s (password hash redacted as %s)\n", fs.Arg(0), redactedHash)
	return exitOK
}

func cmdImport(args []string) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	presetDir := fs.String("presets", defaultPresetDir, "preset directory")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov import FILE   (\"-\" reads stdin)")
		return exitUsage
	}
	var data []byte
	var err error
	if fs.Arg(0) == "-" {
		data, err = io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	} else {
		data, err = os.ReadFile(fs.Arg(0))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "import:", err)
		return exitFail
	}
	b := fileBundle{cfgPath: *cfgPath, presetDir: *presetDir}
	dir := runDir()
	online := daemonRunning(dir)
	if online {
		b.reload = socketReload(dir)
	}
	restart, err := b.Import(data)
	if err != nil {
		var be *bundleError
		if errors.As(err, &be) {
			fmt.Fprintln(os.Stderr, "import: nothing written, the bundle has problems:")
			for _, e := range be.Errors() {
				fmt.Fprintln(os.Stderr, "  -", e)
			}
			return exitFail
		}
		fmt.Fprintln(os.Stderr, "import:", err)
		return exitFail
	}
	fmt.Printf("settings imported into %s and %s\n", *cfgPath, *presetDir)
	switch {
	case !online:
		fmt.Println("daemon not running; the settings apply at the next start")
	case restart:
		fmt.Println("channel set or profile changed: run  systemctl restart n5-fangov")
	default:
		fmt.Println("daemon reloaded (curves, sensors, daemon values); [web] and [log] changes need a restart")
	}
	return exitOK
}

// socketReload asks the running daemon to reload raw via PUT /api/config
// over the unix socket; 202 becomes the restart sentinel.
func socketReload(dir string) func([]byte) error {
	return func(raw []byte) error {
		status, err := newAPI(dir).doRaw("PUT", "/api/config", "application/toml", raw, nil)
		if err != nil {
			return err
		}
		if status == 202 {
			return errRestartRequired()
		}
		return nil
	}
}
