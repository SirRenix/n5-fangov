package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	// L2: the clear leaves a trace naming the client
	e.logMu.Lock()
	cleared := strings.Join(e.logged, "\n")
	e.logMu.Unlock()
	if !strings.Contains(cleared, "web: log cleared by 127.0.0.1") {
		t.Fatalf("clear not logged: %q", cleared)
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

// TestLogJournalOnlyStore: a store without a file path (cmd's journal
// store) reports source "journal", exports its lines and refuses Clear
// with ErrUnsupported → 501.
func TestLogJournalOnlyStore(t *testing.T) {
	e := newEnv(t, AuthConfig{}) // newEnv wires a fakeLogStore with an empty path
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
