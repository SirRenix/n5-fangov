package web

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
)

type fakeBundle struct {
	doc       string
	exportErr error
	imported  [][]byte
	restart   bool
	importErr error
	warnings  []string
}

func (b *fakeBundle) Export() ([]byte, error) { return []byte(b.doc), b.exportErr }

func (b *fakeBundle) Import(raw []byte) (bool, []string, error) {
	if b.importErr != nil {
		return false, nil, b.importErr
	}
	b.imported = append(b.imported, raw)
	return b.restart, b.warnings, nil
}

// multiErr mimics the cmd bundle's validation error (Errors() []string).
type multiErr []string

func (m multiErr) Error() string    { return strings.Join(m, "; ") }
func (m multiErr) Errors() []string { return m }

// ---- settings bundle -------------------------------------------------------

const sampleBundle = `{"format":1,"version":"0.2.0","exported":1789500000,"config":%q,"presets":{"quiet":"[[channel]]\nname = \"cpu\"\n"}}`

// TestBundleExport: attachment headers, the hash inside "config" is redacted,
// everything else survives, and auth = basic protects it.
func TestBundleExport(t *testing.T) {
	hash := PasswordHash("admin", "pw")
	cfgText := "[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"" + hash + "\"\n\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45,85],[80,255]]\ncritical = 88\nstop = \"auto\"\n"
	b := &fakeBundle{doc: fmt.Sprintf(sampleBundle, cfgText)}
	e := newEnv(t, AuthConfig{})
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Bundle = b })

	r := e.do(t, "GET", "/api/config/export", "", nil)
	wantAttachment(t, r, "application/json", `^n5-fangov-settings-\d{8}-\d{6}\.json$`)
	if strings.Contains(r.body, hash) {
		t.Fatalf("hash leaked: %s", r.body)
	}
	var doc map[string]any
	decode(t, r.body, &doc)
	cfg, _ := doc["config"].(string)
	if !strings.Contains(cfg, `password_hash = "`+RedactedHash+`"`) || !strings.Contains(cfg, "critical = 88") {
		t.Fatalf("config not redacted/kept: %q", cfg)
	}
	if doc["format"].(float64) != 1 || doc["version"] != "0.2.0" || doc["exported"].(float64) != 1789500000 {
		t.Fatalf("bundle members changed: %v", doc)
	}
	if p := doc["presets"].(map[string]any); p["quiet"] != "[[channel]]\nname = \"cpu\"\n" {
		t.Fatalf("presets changed: %v", p)
	}
	// no hash → nothing to redact, text untouched
	b.doc = fmt.Sprintf(sampleBundle, sampleTOML)
	r = e.do(t, "GET", "/api/config/export", "", nil)
	decode(t, r.body, &doc)
	if doc["config"] != sampleTOML {
		t.Fatalf("config altered without a hash: %q", doc["config"])
	}
	// non-JSON / non-object bundle → 500, never sent raw
	b.doc = "not json"
	wantError(t, e.do(t, "GET", "/api/config/export", "", nil), 500, "JSON object")
	b.doc = `{"config": 5}`
	wantError(t, e.do(t, "GET", "/api/config/export", "", nil), 500, "not a string")
	b.doc = `{"format":1}`
	wantCode(t, e.do(t, "GET", "/api/config/export", "", nil), 200)
	b.doc, b.exportErr = "{}", errors.New("cannot read presets")
	wantError(t, e.do(t, "GET", "/api/config/export", "", nil), 500, "cannot read presets")

	// auth = basic: export is a protected read; socket handler stays open
	auth := AuthConfig{Mode: "basic", User: "admin", PasswordHash: hash}
	b.doc, b.exportErr = fmt.Sprintf(sampleBundle, cfgText), nil
	e.withDeps(t, auth, func(d *Deps) { d.Bundle = b })
	wantError(t, e.do(t, "GET", "/api/config/export", "", nil), 401, "authentication")
	wantCode(t, e.do(t, "GET", "/api/config/export", "", basicAuth("admin", "pw")), 200)
	sock := httptest.NewServer(e.srv.SocketHandler())
	defer sock.Close()
	res, err := http.Get(sock.URL + "/api/config/export")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || strings.Contains(string(body), hash) {
		t.Fatalf("socket export: %d %s", res.StatusCode, body)
	}
}

// TestBundleImport: CSRF + auth, 1 MiB limit, JSON-object check, 400 with
// the error lines, 200 / 202 by restartRequired.
func TestBundleImport(t *testing.T) {
	b := &fakeBundle{doc: "{}"}
	e := newEnv(t, AuthConfig{})
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Bundle = b })
	doc := fmt.Sprintf(sampleBundle, sampleTOML)

	wantError(t, e.do(t, "POST", "/api/config/import", doc, nil), 403, CSRFHeader)
	wantError(t, e.do(t, "POST", "/api/config/import", "", csrf), 400, "JSON settings bundle")
	wantError(t, e.do(t, "POST", "/api/config/import", "[1,2]", csrf), 400, "JSON settings bundle")
	wantError(t, e.do(t, "POST", "/api/config/import", "{oops", csrf), 400, "JSON settings bundle")
	big := `{"pad":"` + strings.Repeat("x", maxImportBody) + `"}`
	wantError(t, e.do(t, "POST", "/api/config/import", big, csrf), 413, "exceeds")
	if len(b.imported) != 0 {
		t.Fatal("Import called on a rejected body")
	}

	r := e.do(t, "POST", "/api/config/import", doc, csrf)
	wantCode(t, r, 200)
	var m map[string]any
	decode(t, r.body, &m)
	if m["ok"] != true || m["restart_required"] != false || len(b.imported) != 1 || string(b.imported[0]) != doc {
		t.Fatalf("import = %v (%d calls)", m, len(b.imported))
	}
	b.restart = true
	r = e.do(t, "POST", "/api/config/import", doc, csrf)
	wantCode(t, r, 202)
	decode(t, r.body, &m)
	if m["restart_required"] != true || !strings.Contains(m["message"].(string), "systemctl restart n5-fangov") {
		t.Fatalf("202 body = %v", m)
	}
	b.importErr = errors.New("config: toml: line 3: expected key\npreset quiet: curve needs 2..8 points")
	r = e.do(t, "POST", "/api/config/import", doc, csrf)
	wantCode(t, r, 400)
	decode(t, r.body, &m)
	errs, _ := m["errors"].([]any)
	if !strings.Contains(m["error"].(string), "import rejected: config: toml") || len(errs) != 2 || errs[1] != "preset quiet: curve needs 2..8 points" {
		t.Fatalf("400 body = %v", m)
	}
	if len(b.imported) != 2 {
		t.Fatalf("Import calls = %d", len(b.imported))
	}
	// an error with Errors() []string (cmd's bundle validation) is listed item by item
	b.importErr = fmt.Errorf("wrapped: %w", multiErr{"config: bad", "preset x: bad"})
	r = e.do(t, "POST", "/api/config/import", doc, csrf)
	wantCode(t, r, 400)
	decode(t, r.body, &m)
	errs, _ = m["errors"].([]any)
	if m["error"] != "import rejected: config: bad" || len(errs) != 2 || errs[1] != "preset x: bad" {
		t.Fatalf("400 body (Errors()) = %v", m)
	}

	// auth = basic: import is a write
	b.importErr = nil
	auth := AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")}
	e.withDeps(t, auth, func(d *Deps) { d.Bundle = b })
	wantError(t, e.do(t, "POST", "/api/config/import", doc, csrf), 401, "authentication")
	wantCode(t, e.do(t, "POST", "/api/config/import", doc, basicAuth("admin", "pw")), 202)
}

// TestImportWarningsReported: the config warnings of an import reach the
// 200/202 answer.
func TestImportWarningsReported(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	b := &fakeBundle{doc: "{}", warnings: []string{"daemon.interval: 1s below 2s, default 10s used"}}
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Bundle = b })
	r := e.do(t, "POST", "/api/config/import", `{"format":1,"config":"[daemon]\n"}`, csrf)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"warnings":["daemon.interval`) {
		t.Fatalf("warnings missing: %s", r.body)
	}
	b.restart = true
	r = e.do(t, "POST", "/api/config/import", `{"format":1,"config":"[daemon]\n"}`, csrf)
	wantCode(t, r, 202)
	if !strings.Contains(r.body, `"warnings":["daemon.interval`) {
		t.Fatalf("202 warnings missing: %s", r.body)
	}
	b.importErr = &fs.PathError{Op: "rename", Path: "/etc/n5-fangov/config.toml", Err: syscall.EROFS}
	wantError(t, e.do(t, "POST", "/api/config/import", `{"format":1,"config":"[daemon]\n"}`, csrf), 500, "import failed")
}

// TestRestoreHash: only password_hash assignments get the hash back.
func TestRestoreHash(t *testing.T) {
	raw := "# <unchanged>\n[web]\npassword_hash = '<unchanged>'\nuser = \"<unchanged>\"\n"
	got := RestoreHash(raw, "HASH")
	if got != "# <unchanged>\n[web]\npassword_hash = 'HASH'\nuser = \"<unchanged>\"\n" {
		t.Errorf("RestoreHash:\n%s", got)
	}
}
