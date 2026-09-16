package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/web"
)

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
	restart, _, err := dst.Import(data)
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
	if restart, _, err := dst.Import(data); err != nil || !restart {
		t.Errorf("restart: %v %v", restart, err)
	}
	// reload failure after the write is reported
	dst.reload = func([]byte) error { return errors.New("boom") }
	if _, _, err := dst.Import(data); err == nil || !strings.Contains(err.Error(), "written, but") {
		t.Errorf("reload error: %v", err)
	}
	// no daemon: written, nothing to reload
	dst.reload = nil
	if restart, _, err := dst.Import(data); err != nil || restart {
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
		_, _, err := b.Import(data)
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
	_, _, err := b.Import(mk("[daemon\n", map[string]string{"bad": "[[[", "Bad": quietPreset}))
	var be *bundleError
	if !errors.As(err, &be) || len(be.Errors()) != 3 {
		t.Errorf("error list: %v", err)
	}
	// placeholder without a local password
	if err := os.WriteFile(cfgPath, []byte("[daemon]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ = os.ReadFile(cfgPath)
	_, _, err = b.Import(mk("[web]\nauth = \"basic\"\nuser = \"a\"\npassword_hash = \"<unchanged>\"\n", nil))
	if err == nil || !strings.Contains(err.Error(), "passwd") {
		t.Errorf("placeholder without local hash: %v", err)
	}
	unchanged("placeholder")
}

// A short password hash must not be replaced as a bare substring —
// with the old ReplaceAll a hash of "a" mangled every "a" in the config.
func TestBundleExportShortHash(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.toml")
	src := "[daemon]\nprofile = \"n5pro\"\n\n[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"a\"\n\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\n"
	if err := os.WriteFile(cfgPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := fileBundle{cfgPath: cfgPath, presetDir: filepath.Join(root, "presets")}.Export()
	if err != nil {
		t.Fatal(err)
	}
	var b settingsBundle
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(src, `password_hash = "a"`, `password_hash = "<unchanged>"`, 1)
	if b.Config != want {
		t.Errorf("short hash mangled the config:\n%s", b.Config)
	}
	// a long hash is still removed wherever it appears (inline table form)
	long := strings.Repeat("ab", 32)
	if err := os.WriteFile(cfgPath, []byte("web = { auth = \"basic\", user = \"admin\", password_hash = \""+long+"\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ = fileBundle{cfgPath: cfgPath, presetDir: filepath.Join(root, "presets")}.Export()
	_ = json.Unmarshal(data, &b)
	if strings.Contains(b.Config, long) || !strings.Contains(b.Config, redactedHash) {
		t.Errorf("inline hash not redacted:\n%s", b.Config)
	}
}

// Presets are staged under temp names and renamed only after the
// config is written; a config write failure leaves the preset directory
// untouched and no temp files behind.
func TestBundleImportStagesPresets(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "etc")
	presetDir := filepath.Join(root, "presets")
	if err := os.MkdirAll(presetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(presetDir, "quiet.toml"), []byte("[[channel]]\nname = \"old\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, _ := json.Marshal(settingsBundle{Format: 1, Config: "[daemon]\n", Presets: map[string]string{"quiet": quietPreset, "fast": quietPreset}})
	// config path inside a file (not a directory): Save fails after the presets were staged
	if err := os.WriteFile(cfgDir, []byte("i am a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := fileBundle{cfgPath: filepath.Join(cfgDir, "config.toml"), presetDir: presetDir}
	if _, _, err := b.Import(doc); err == nil {
		t.Fatal("import succeeded with an unwritable config path")
	}
	entries, _ := os.ReadDir(presetDir)
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "quiet.toml" {
		t.Errorf("preset dir after failed import: %v (staged files must be gone, fast.toml not created)", names)
	}
	if p, _ := os.ReadFile(filepath.Join(presetDir, "quiet.toml")); !strings.Contains(string(p), "old") {
		t.Errorf("existing preset replaced although the import failed")
	}
	// now with a writable config path: both presets land, no temp files
	if err := os.Remove(cfgDir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Import(doc); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(presetDir)
	names = names[:0]
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "fast.toml,quiet.toml" {
		t.Errorf("preset dir after import: %v", names)
	}
	if p, _ := os.ReadFile(filepath.Join(presetDir, "quiet.toml")); string(p) != quietPreset {
		t.Errorf("preset content: %q", p)
	}
}

// TestBundleImportInlinePlaceholder: a bundle whose config carries
// the <unchanged> placeholder in an inline table — where restoreHash does
// not reach — is refused (400 over the API) instead of written with the
// placeholder as the stored hash.
func TestBundleImportInlinePlaceholder(t *testing.T) {
	cfgPath, presetDir := bundleDirs(t)
	before, _ := os.ReadFile(cfgPath)
	reloads := 0
	b := fileBundle{cfgPath: cfgPath, presetDir: presetDir, reload: func([]byte) error { reloads++; return nil }}
	mk := func(cfg string) []byte {
		d, _ := json.Marshal(settingsBundle{Format: bundleFormat, Config: cfg, Presets: map[string]string{}})
		return d
	}
	inline := "web = { auth = \"basic\", user = \"admin\", password_hash = \"" + redactedHash + "\" }\n"
	_, _, err := b.Import(mk(inline))
	var be *bundleError
	if !errors.As(err, &be) || len(be.Errors()) != 1 || !strings.Contains(be.Errors()[0], "password_hash "+redactedHash+" not restored") {
		t.Fatalf("inline placeholder: %v", err)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) || reloads != 0 {
		t.Fatalf("refused bundle changed the config (reloads %d)", reloads)
	}
	// over the API: 400 with the line under "errors"
	srv := web.New(web.Deps{Bundle: b, Logf: func(string, ...any) {}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/config/import", strings.NewReader(string(mk(inline))))
	req.Header.Set(web.CSRFHeader, "1")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 400 || !strings.Contains(string(raw), "import rejected: config: password_hash") {
		t.Fatalf("API: %d %s", res.StatusCode, raw)
	}
	// the line form restores the stored hash and is imported
	if _, _, err := b.Import(mk("[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"" + redactedHash + "\"\n")); err != nil {
		t.Fatalf("line form: %v", err)
	}
	cfg, _, err := config.Load(cfgPath)
	if err != nil || cfg.Web.PasswordHash != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" || reloads != 1 {
		t.Fatalf("line form not restored: %+v %v (reloads %d)", cfg.Web, err, reloads)
	}
}

// TestRestoreHashOnlyAssignments: the bundle import puts the hash back into
// the password_hash assignment only, not into a comment.
func TestRestoreHashOnlyAssignments(t *testing.T) {
	raw := "# keep <unchanged> here\n[web]\npassword_hash = \"<unchanged>\"\nuser = \"<unchanged>\"\n"
	got := restoreHash(raw, "HASH")
	if strings.Count(got, "HASH") != 1 || !strings.Contains(got, "# keep <unchanged> here") || !strings.Contains(got, `user = "<unchanged>"`) {
		t.Errorf("restoreHash:\n%s", got)
	}
}
