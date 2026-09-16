package web

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
)

// v0.3.0-beta review fixes: credential epoch (M1), rename keeps the
// session's user (L10), query semicolons (L7), preset status codes (L8,
// U8), busy test alert (L9).

// TestSessionStoreEpoch (R-M1): a mirror file written under another
// credential epoch is dropped with one log line; the same epoch keeps it;
// SetEpoch moves the file to the new epoch; an epoch-less store and a
// legacy file without epoch behave as documented.
func TestSessionStoreEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	epochA := CredentialEpoch("admin", PasswordHash("admin", "pw"))
	epochB := CredentialEpoch("admin", PasswordHash("admin", "other"))
	if epochA == epochB || len(epochA) != 64 || CredentialEpoch("admin", "h") == CredentialEpoch("admi", "nh") {
		t.Fatalf("epochs: %s %s", epochA, epochB)
	}
	st := NewSessionStoreEpoch(path, epochA, logf)
	tok1, _, _ := st.Create("admin", true, "")
	st.Create("admin", false, "")
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"epoch": "`+epochA+`"`) {
		t.Fatalf("file lacks the epoch: %s", b)
	}
	// different epoch: everything dropped, one line
	if l := NewSessionStoreEpoch(path, epochB, logf).List(); len(l) != 0 {
		t.Errorf("other epoch loaded %d sessions", len(l))
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "credentials changed") || !strings.Contains(logged[0], "2 session(s) dropped") {
		t.Errorf("log = %v", logged)
	}
	// the file is untouched by a load that drops it: the same epoch still finds them
	if _, ok := NewSessionStoreEpoch(path, epochA, logf).Lookup(tok1); !ok {
		t.Error("same epoch lost the session")
	}
	// epoch "" accepts any file
	if _, ok := NewSessionStore(path, logf).Lookup(tok1); !ok {
		t.Error("epoch-less store must accept the file")
	}
	// SetEpoch rewrites the file under the new epoch: the kept sessions survive a restart into it
	st.SetEpoch(epochB)
	if l := NewSessionStoreEpoch(path, epochB, logf).List(); len(l) != 2 {
		t.Errorf("after SetEpoch: %d sessions under the new epoch", len(l))
	}
	if l := NewSessionStoreEpoch(path, epochA, logf).List(); len(l) != 0 {
		t.Errorf("after SetEpoch: %d sessions under the old epoch", len(l))
	}
	// legacy file without epoch (v0.3.0-beta.1 layout): dropped by an epoch store
	_ = os.WriteFile(path, []byte(`{"format":1,"sessions":[{"key":"`+strings.Repeat("ab", 32)+`","user":"admin","expires":"2999-01-01T00:00:00Z"}]}`), 0o600)
	if l := NewSessionStoreEpoch(path, epochA, logf).List(); len(l) != 0 {
		t.Errorf("legacy file loaded: %+v", l)
	}
	if l := NewSessionStore(path, logf).List(); len(l) != 1 {
		t.Errorf("legacy file must still load into an epoch-less store: %+v", l)
	}
	// an empty file is dropped silently
	_ = os.WriteFile(path, []byte(`{"format":1,"sessions":[]}`), 0o600)
	n := len(logged)
	NewSessionStoreEpoch(path, epochA, logf)
	if len(logged) != n {
		t.Errorf("empty file logged: %v", logged[n:])
	}
}

// TestSessionStoreRevokeAllRename (R-L10): the kept session carries the
// new user, the others go; "" keeps the user.
func TestSessionStoreRevokeAllRename(t *testing.T) {
	st := NewSessionStore("", nil)
	tok1, _, _ := st.Create("admin", false, "")
	st.Create("admin", false, "")
	st.RevokeAllRename(tok1, "root")
	l := st.List()
	if len(l) != 1 || l[0].User != "root" {
		t.Fatalf("List = %+v", l)
	}
	if s, ok := st.Lookup(tok1); !ok || s.User != "root" {
		t.Errorf("Lookup = %+v %v", s, ok)
	}
	st.RevokeAllRename(tok1, "")
	if s, _ := st.Lookup(tok1); s.User != "root" {
		t.Errorf("\"\" must keep the user: %+v", s)
	}
	st.RevokeAllRename("", "x")
	if len(st.List()) != 0 {
		t.Error("no keep token must revoke everything")
	}
}

// restartEnv builds a server on a fixed session file so a test can
// "restart" it with other credentials (withDeps + the same file).
func restartEnv(t *testing.T, e *env, sessionFile string, auth AuthConfig, acc *fakeAccount) {
	t.Helper()
	e.withDeps(t, auth, func(d *Deps) {
		d.SessionFile = sessionFile
		d.Account = acc
	})
}

// TestAccountChangeSurvivesRestart (R-M1, R-L10) end to end: after a
// password change through the API the caller's cookie is valid on a
// restarted daemon (new epoch written), other cookies are not; after a
// rename the cookie reports the new user; a rotation outside the daemon
// (restart with other credentials) drops the persisted sessions.
func TestAccountChangeSurvivesRestart(t *testing.T) {
	e := newEnv(t, adminBasic)
	sessionFile := filepath.Join(t.TempDir(), "sessions.json")
	acc := &fakeAccount{cfg: adminBasic}
	restartEnv(t, e, sessionFile, adminBasic, acc)
	a, _, _ := e.login(t, "admin", "pw", true)
	b, _, _ := e.login(t, "admin", "pw", true)
	wantCode(t, e.do(t, "POST", "/api/account/password", `{"current_password":"pw","new_password":"newpassword"}`, a), 200)
	// restart with the credentials the store now holds
	restartEnv(t, e, sessionFile, acc.cfg, acc)
	wantCode(t, e.do(t, "GET", "/api/config", "", a), 200)
	wantError(t, e.do(t, "GET", "/api/config", "", b), 401, "authentication")
	if strings.Contains(e.logLines(), "credentials changed") {
		t.Errorf("sessions dropped after an API change: %q", e.logLines())
	}
	// rename: the surviving cookie reports the new name, also after a restart
	r := e.do(t, "POST", "/api/account/user", `{"current_password":"newpassword","user":"ops"}`, a)
	wantCode(t, r, 200)
	r = e.do(t, "GET", "/api/session", "", a)
	if !strings.Contains(r.body, `"user":"ops"`) || !strings.Contains(r.body, `"via":"cookie"`) {
		t.Errorf("session after rename = %s", r.body)
	}
	restartEnv(t, e, sessionFile, acc.cfg, acc)
	r = e.do(t, "GET", "/api/session", "", a)
	if !strings.Contains(r.body, `"user":"ops"`) || !strings.Contains(r.body, `"authenticated":true`) {
		t.Errorf("session after rename + restart = %s", r.body)
	}
	// rotation outside the daemon: `n5-fangov passwd` wrote a new hash
	rotated := AuthConfig{Mode: "basic", User: "ops", PasswordHash: PasswordHash("ops", "rotated")}
	restartEnv(t, e, sessionFile, rotated, &fakeAccount{cfg: rotated})
	wantError(t, e.do(t, "GET", "/api/config", "", a), 401, "authentication")
	if !strings.Contains(e.logLines(), "credentials changed since") || !strings.Contains(e.logLines(), "1 session(s) dropped") {
		t.Errorf("no drop line: %q", e.logLines())
	}
	wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("ops", "rotated")), 200)
}

// TestQuerySemicolonNotLogged (R-L7): a request with ';' in the query
// produces no ErrorLog line on the daemon's server (through the
// handshake filter into Logf) nor on a stock server around the socket
// handler, and ';' acts as a separator. (Go 1.25 no longer logs the
// pre-1.25 warning itself; the wrapper keeps that explicit.)
func TestQuerySemicolonNotLogged(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- e.srv.Serve(ctx, ln) }()
	for _, p := range []string{"/?a;b", "/api/version?x;y=1", "/api/history?minutes=5;since=0"} {
		res, err := http.Get("http://" + ln.Addr().String() + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
	// a ';'-separated pair is parsed: minutes=9999 is out of range → 400
	res, err := http.Get("http://" + ln.Addr().String() + "/api/history?since=0;minutes=9999")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Errorf("';' not treated as a separator: %d", res.StatusCode)
	}
	cancel()
	select {
	case <-errc:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
	if l := e.logLines(); strings.Contains(l, "semicolon") || strings.Contains(l, "http:") {
		t.Errorf("server error line reached the log: %q", l)
	}
	var buf bytes.Buffer
	ts := httptest.NewUnstartedServer(e.srv.SocketHandler())
	ts.Config.ErrorLog = log.New(&buf, "", 0)
	ts.Start()
	defer ts.Close()
	res, err = http.Get(ts.URL + "/?a;b")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if buf.Len() != 0 {
		t.Errorf("socket handler logged: %q", buf.String())
	}
}

// TestPresetStatusCodes: save over a built-in → 409 (R-U8), apply a
// preset the store reports as missing → 404 (R-L8).
func TestPresetStatusCodes(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.presets.saveErr = fmt.Errorf("preset %q: %w", "n5pro-quiet", ErrPresetBuiltin)
	wantError(t, e.do(t, "PUT", "/api/presets/n5pro-quiet", "", csrf), 409, "built-in")
	e.presets.saveErr = fmt.Errorf("disk full")
	wantError(t, e.do(t, "PUT", "/api/presets/mine", "", csrf), 400, "disk full")
	e.presets.applyErr = fmt.Errorf("preset %q: built-in for profile n5pro, not nct67xx: %w", "n5pro-quiet", fs.ErrNotExist)
	wantError(t, e.do(t, "POST", "/api/presets/n5pro-quiet/apply", "", csrf), 404, "unknown preset n5pro-quiet")
	e.presets.applyErr = fmt.Errorf("no usable channel")
	wantError(t, e.do(t, "POST", "/api/presets/mine/apply", "", csrf), 400, "no usable channel")
}

// TestAlertsTestBusy (R-L9): a test delivery still running answers 409.
func TestAlertsTestBusy(t *testing.T) {
	e, _, al, _ := v3Env(t, adminBasic)
	al.testErr = alert.ErrTestBusy
	r := e.do(t, "POST", "/api/alerts/test", "", basicAuth("admin", "pw"))
	wantError(t, r, 409, "test in progress")
	if !strings.Contains(r.body, `"transport":"mail"`) {
		t.Errorf("busy = %s", r.body)
	}
}
