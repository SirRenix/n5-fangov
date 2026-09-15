package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/tlscert"
)

// ---- fakes ---------------------------------------------------------------

type fakeLogStore struct {
	lines    []string
	path     string
	cleared  int
	clearErr error
	exportN  int
}

func (l *fakeLogStore) Lines(n int) ([]string, error) {
	if n < len(l.lines) {
		return l.lines[len(l.lines)-n:], nil
	}
	return l.lines, nil
}
func (l *fakeLogStore) Export(w io.Writer) error {
	l.exportN++
	_, err := io.WriteString(w, strings.Join(l.lines, "\n")+"\n")
	return err
}
func (l *fakeLogStore) Clear() error {
	if l.clearErr != nil {
		return l.clearErr
	}
	l.cleared++
	l.lines = nil
	return nil
}
func (l *fakeLogStore) Path() string { return l.path }

type fakeBundle struct {
	doc       string
	exportErr error
	imported  [][]byte
	restart   bool
	importErr error
}

func (b *fakeBundle) Export() ([]byte, error) { return []byte(b.doc), b.exportErr }
func (b *fakeBundle) Import(raw []byte) (bool, error) {
	if b.importErr != nil {
		return false, b.importErr
	}
	b.imported = append(b.imported, raw)
	return b.restart, nil
}

// withDeps rebuilds the env's server with f applied to the deps.
func (e *env) withDeps(t *testing.T, auth AuthConfig, f func(*Deps)) {
	t.Helper()
	d := e.deps(auth)
	f(&d)
	e.srv = New(d)
	e.ts.Close()
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
}

var attachmentName = regexp.MustCompile(`^attachment; filename="([^"]+)"$`)

func wantAttachment(t *testing.T, r resp, ctype, namePattern string) string {
	t.Helper()
	wantCode(t, r, 200)
	if ct := r.hdr.Get("Content-Type"); !strings.HasPrefix(ct, ctype) {
		t.Errorf("content-type %q, want %q", ct, ctype)
	}
	m := attachmentName.FindStringSubmatch(r.hdr.Get("Content-Disposition"))
	if m == nil {
		t.Fatalf("content-disposition %q", r.hdr.Get("Content-Disposition"))
	}
	if ok, _ := regexp.MatchString(namePattern, m[1]); !ok {
		t.Errorf("filename %q does not match %s", m[1], namePattern)
	}
	if r.hdr.Get("Cache-Control") != "no-store" || r.hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("export headers: %v", r.hdr)
	}
	return m[1]
}

// ---- log store -------------------------------------------------------------

// TestLogStoreEndpoints: with a LogStore GET /api/log reports source "file",
// the export is a text attachment with the whole file, DELETE truncates and
// answers the contract body. CSRF is required on DELETE.
func TestLogStoreEndpoints(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	ls := &fakeLogStore{lines: []string{"a", "b", "c"}, path: "/var/log/n5-fangov/n5-fangov.log"}
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Log = ls })

	r := e.do(t, "GET", "/api/log?lines=2", "", nil)
	wantCode(t, r, 200)
	var m struct {
		Lines  []string `json:"lines"`
		Source string   `json:"source"`
	}
	decode(t, r.body, &m)
	if fmt.Sprint(m.Lines) != "[b c]" || m.Source != "file" {
		t.Fatalf("log = %+v", m)
	}

	r = e.do(t, "GET", "/api/log/export", "", nil)
	wantAttachment(t, r, "text/plain", `^n5-fangov-[A-Za-z0-9.-]+-\d{8}-\d{6}\.log$`)
	if r.body != "a\nb\nc\n" || ls.exportN != 1 {
		t.Fatalf("export body %q (exports %d)", r.body, ls.exportN)
	}

	wantError(t, e.do(t, "DELETE", "/api/log", "", nil), 403, CSRFHeader)
	if ls.cleared != 0 {
		t.Fatal("cleared without CSRF")
	}
	r = e.do(t, "DELETE", "/api/log", "", csrf)
	wantCode(t, r, 200)
	var c map[string]any
	decode(t, r.body, &c)
	if c["cleared"] != true || c["note"] != "journal untouched" || ls.cleared != 1 {
		t.Fatalf("clear = %v (cleared %d)", c, ls.cleared)
	}
	r = e.do(t, "GET", "/api/log", "", nil)
	decode(t, r.body, &m)
	if len(m.Lines) != 0 {
		t.Fatalf("lines after clear = %v", m.Lines)
	}
	if !strings.Contains(r.body, `"lines":[]`) {
		t.Fatalf("nil lines not normalised: %s", r.body)
	}
	ls.clearErr = errors.New("disk says no")
	wantError(t, e.do(t, "DELETE", "/api/log", "", csrf), 500, "disk says no")
	ls.clearErr = fmt.Errorf("file disabled: %w", errors.ErrUnsupported)
	wantError(t, e.do(t, "DELETE", "/api/log", "", csrf), 501, "not supported")

	// file disabled (journal fallback) → source "journal"
	ls.path = ""
	r = e.do(t, "GET", "/api/log", "", nil)
	decode(t, r.body, &m)
	if m.Source != "journal" {
		t.Fatalf("source with empty path = %q", m.Source)
	}
	// socket handler: DELETE without CSRF works there
	sock := httptest.NewServer(e.srv.SocketHandler())
	defer sock.Close()
	ls.clearErr = nil
	req, _ := http.NewRequest("DELETE", sock.URL+"/api/log", nil)
	res, err := sock.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || ls.cleared != 2 {
		t.Fatalf("socket clear = %d (cleared %d)", res.StatusCode, ls.cleared)
	}
}

// TestLogEndpointsAuth: with auth = basic the export needs credentials like
// GET /api/log, and DELETE needs auth on top of CSRF.
func TestLogEndpointsAuth(t *testing.T) {
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")})
	ls := &fakeLogStore{lines: []string{"x"}, path: "/var/log/x.log"}
	e.withDeps(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")}, func(d *Deps) { d.Log = ls })
	wantError(t, e.do(t, "GET", "/api/log/export", "", nil), 401, "authentication")
	wantError(t, e.do(t, "DELETE", "/api/log", "", csrf), 401, "authentication")
	wantError(t, e.do(t, "DELETE", "/api/log", "", map[string]string{"Authorization": basicAuth("admin", "pw")["Authorization"]}), 403, CSRFHeader)
	if ls.cleared != 0 || ls.exportN != 0 {
		t.Fatal("log touched without auth")
	}
	ok := basicAuth("admin", "pw")
	wantCode(t, e.do(t, "GET", "/api/log/export", "", ok), 200)
	wantCode(t, e.do(t, "DELETE", "/api/log", "", ok), 200)
	if ls.cleared != 1 {
		t.Fatal("not cleared with auth")
	}
}

// TestLogFuncSourceAdapter: the deprecated func form still works for one
// release — source "journal", export streams the newest lines, clear → 501.
func TestLogFuncSourceAdapter(t *testing.T) {
	e := newEnv(t, AuthConfig{}) // newEnv wires a func(int) ([]string, error)
	r := e.do(t, "GET", "/api/log", "", nil)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"source":"journal"`) {
		t.Fatalf("func source: %s", r.body)
	}
	r = e.do(t, "GET", "/api/log/export", "", nil)
	wantAttachment(t, r, "text/plain", `\.log$`)
	if r.body != "line1\nline2\nline3\n" {
		t.Fatalf("export = %q", r.body)
	}
	wantError(t, e.do(t, "DELETE", "/api/log", "", csrf), 501, "journal-only")
	// the named type works as well
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Log = LogSource(func(int) ([]string, error) { return []string{"named"}, nil }) })
	r = e.do(t, "GET", "/api/log", "", nil)
	if !strings.Contains(r.body, `"named"`) || !strings.Contains(r.body, `"source":"journal"`) {
		t.Fatalf("LogSource type: %s", r.body)
	}
	// unsupported type: 501 everywhere and one log line at New
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Log = 42 })
	for _, c := range []struct{ m, p string }{{"GET", "/api/log"}, {"GET", "/api/log/export"}, {"DELETE", "/api/log"}} {
		wantCode(t, e.do(t, c.m, c.p, "", csrf), 501)
	}
	e.logMu.Lock()
	joined := strings.Join(e.logged, "\n")
	e.logMu.Unlock()
	if !strings.Contains(joined, "unsupported type int") {
		t.Fatalf("no log line for the unsupported type: %q", joined)
	}
}

// TestLogNotImplementedWithoutStore: no Deps.Log → 501 on all three.
func TestLogNotImplementedWithoutStore(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Log = nil; d.Bundle = nil })
	for _, c := range []struct{ m, p string }{{"GET", "/api/log"}, {"GET", "/api/log/export"}, {"DELETE", "/api/log"}, {"GET", "/api/config/export"}, {"POST", "/api/config/import"}} {
		r := e.do(t, c.m, c.p, "{}", csrf)
		if r.code != 501 {
			t.Errorf("%s %s = %d, want 501 (%s)", c.m, c.p, r.code, r.body)
		}
	}
	e.logMu.Lock()
	n := len(e.logged)
	e.logMu.Unlock()
	if n != 0 {
		t.Fatalf("nil Log must not be logged as unsupported: %v", e.logged)
	}
}

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

	// auth = basic: import is a write
	b.importErr = nil
	auth := AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")}
	e.withDeps(t, auth, func(d *Deps) { d.Bundle = b })
	wantError(t, e.do(t, "POST", "/api/config/import", doc, csrf), 401, "authentication")
	wantCode(t, e.do(t, "POST", "/api/config/import", doc, basicAuth("admin", "pw")), 202)
}

// ---- auth logging ------------------------------------------------------------

// TestAnonymous401Silent: a request without Authorization gets a 401 that is
// neither logged nor counted; a presented wrong credential is both.
func TestAnonymous401Silent(t *testing.T) {
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")})
	var slept []time.Duration
	var mu sync.Mutex
	e.srv.limiter.sleep = func(d time.Duration) { mu.Lock(); slept = append(slept, d); mu.Unlock() }
	nslept := func() int { mu.Lock(); defer mu.Unlock(); return len(slept) }
	for i := 0; i < limitFree+3; i++ {
		wantError(t, e.do(t, "GET", "/api/config", "", nil), 401, "authentication")
		wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, csrf), 401, "authentication")
	}
	e.logMu.Lock()
	n := len(e.logged)
	e.logMu.Unlock()
	if n != 0 || nslept() != 0 {
		t.Fatalf("anonymous 401 logged %d times, delays %d", n, nslept())
	}
	e.srv.limiter.mu.Lock()
	entries := len(e.srv.limiter.byIP)
	e.srv.limiter.mu.Unlock()
	if entries != 0 {
		t.Fatalf("anonymous 401 counted: %d limiter entries", entries)
	}
	// a presented credential still counts and is logged
	wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "nope")), 401)
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, map[string]string{CSRFHeader: "1", "Authorization": "Bearer abc"}), 401)
	e.logMu.Lock()
	n = len(e.logged)
	last := ""
	if n > 0 {
		last = e.logged[n-1]
	}
	e.logMu.Unlock()
	if n != 2 || !strings.Contains(last, "auth failure") || !strings.Contains(last, "2 recent failures") {
		t.Fatalf("presented credential: %d log lines, last %q", n, last)
	}
	// and the login that follows the silent 401 works without any delay
	wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "pw")), 200)
	if nslept() != 0 {
		t.Fatalf("delays after two failures: %d", nslept())
	}
}

// ---- TLS ------------------------------------------------------------------------

// TestServeTLSRoundTrip: a generated certificate serves HTTPS, the response
// carries HSTS and "tls":true, the negotiated version is ≥ 1.2, TLS 1.1 is
// refused, and plain Serve has neither HSTS nor the flag.
func TestServeTLSRoundTrip(t *testing.T) {
	cert, _, err := tlscert.EnsureAuto(tlscert.Options{Dir: t.TempDir(), Hosts: []string{"127.0.0.1"}, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, AuthConfig{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- e.srv.ServeTLS(ctx, ln, cert) }()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	url := "https://" + ln.Addr().String()
	res, err := client.Get(url + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Strict-Transport-Security"); got != hstsValue {
		t.Errorf("HSTS = %q, want %q", got, hstsValue)
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil || v["tls"] != true {
		t.Errorf("version over TLS = %s", body)
	}
	if res.TLS == nil || res.TLS.Version < tls.VersionTLS12 {
		t.Errorf("negotiated TLS %#x", res.TLS.Version)
	}
	if !bytes.Equal(res.TLS.PeerCertificates[0].Raw, cert.Leaf.Raw) {
		t.Error("served a different certificate")
	}
	// HSTS also on guarded errors (Host check, CSRF) and on the UI
	req, _ := http.NewRequest("PUT", url+"/api/override/cpu", strings.NewReader(`{"duty":1}`))
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 || res.Header.Get("Strict-Transport-Security") == "" {
		t.Errorf("CSRF answer over TLS: %d HSTS %q", res.StatusCode, res.Header.Get("Strict-Transport-Security"))
	}
	res, err = client.Get(url + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Strict-Transport-Security") == "" {
		t.Error("index without HSTS")
	}
	// TLS 1.1 refused
	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}}}
	if res, err := old.Get(url + "/api/version"); err == nil {
		res.Body.Close()
		t.Error("TLS 1.1 handshake succeeded")
	}
	// plain HTTP on the TLS port fails (no downgrade)
	if res, err := http.Get("http://" + ln.Addr().String() + "/api/version"); err == nil {
		res.Body.Close()
		if res.StatusCode == 200 {
			t.Error("plain HTTP served on the TLS listener")
		}
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("ServeTLS: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeTLS did not stop")
	}

	// plain Serve: no HSTS, tls false
	e2 := newEnv(t, AuthConfig{})
	r := e2.do(t, "GET", "/api/version", "", nil)
	wantCode(t, r, 200)
	if r.hdr.Get("Strict-Transport-Security") != "" || !strings.Contains(r.body, `"tls":false`) {
		t.Errorf("plain HTTP: HSTS %q body %s", r.hdr.Get("Strict-Transport-Security"), r.body)
	}
	// Deps.TLS is informational for a TLS reverse proxy in front of plain Serve
	e2.withDeps(t, AuthConfig{}, func(d *Deps) { d.TLS = true })
	if r := e2.do(t, "GET", "/api/version", "", nil); !strings.Contains(r.body, `"tls":true`) || r.hdr.Get("Strict-Transport-Security") != "" {
		t.Errorf("Deps.TLS: %s HSTS %q", r.body, r.hdr.Get("Strict-Transport-Security"))
	}
}

// TestServeTLSWarnsNonLoopbackWithoutAuth: TLS does not silence the H3
// warning — encryption is not authentication.
func TestServeTLSWarnsNonLoopbackWithoutAuth(t *testing.T) {
	cert, _, err := tlscert.EnsureAuto(tlscert.Options{Dir: t.TempDir(), Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, AuthConfig{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- e.srv.ServeTLS(ctx, addrListener{ln, &net.TCPAddr{IP: net.IPv4zero, Port: 8010}}, cert)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-errc
	e.logMu.Lock()
	defer e.logMu.Unlock()
	joined := strings.Join(e.logged, "\n")
	if !strings.Contains(joined, "non-loopback") || !strings.Contains(joined, "without auth") {
		t.Fatalf("no warning: %q", joined)
	}
}
