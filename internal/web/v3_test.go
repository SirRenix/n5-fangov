package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fakes ---------------------------------------------------------------

type fakeAccount struct {
	mu      sync.Mutex
	cfg     AuthConfig
	updates [][2]string
	err     error
}

func (a *fakeAccount) Current() AuthConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}
func (a *fakeAccount) Update(user, hash string) (AuthConfig, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return AuthConfig{}, a.err
	}
	if user != "" {
		a.cfg.User = user
	}
	if hash != "" {
		a.cfg.PasswordHash = hash
	}
	a.updates = append(a.updates, [2]string{user, hash})
	return a.cfg, nil
}

type fakeAlerts struct {
	status      AlertStatus
	recent      []AlertRecord
	testErr     error
	templateErr error
	configured  [][2]string
	configErr   error
}

func (a *fakeAlerts) Status() AlertStatus        { return a.status }
func (a *fakeAlerts) Recent(n int) []AlertRecord { return a.recent }
func (a *fakeAlerts) Test() (string, error)      { return "mail", a.testErr }
func (a *fakeAlerts) InstallTemplate() (string, error) {
	return "/etc/pve/notification-templates/default", a.templateErr
}
func (a *fakeAlerts) Configure(transport, mailTo string) (AlertStatus, error) {
	if a.configErr != nil {
		return AlertStatus{}, a.configErr
	}
	a.configured = append(a.configured, [2]string{transport, mailTo})
	a.status.Transport, a.status.MailTo = transport, mailTo
	return a.status, nil
}

type fakeDashboard struct {
	sensors []string
	err     error
}

func (d *fakeDashboard) Sensors() []string { return d.sensors }
func (d *fakeDashboard) SetSensors(ids []string) ([]string, error) {
	if d.err != nil {
		return nil, d.err
	}
	d.sensors = ids
	var warn []string
	for _, id := range ids {
		if strings.HasPrefix(id, "bogus") {
			warn = append(warn, id+" does not resolve")
		}
	}
	return warn, nil
}

// fakePresetDeleter adds Delete to fakePresets; the plain fakePresets is the
// 501 case.
type fakePresetDeleter struct {
	fakePresets
	deleted []string
}

func (p *fakePresetDeleter) Delete(name string) error {
	for _, e := range p.list {
		if e.Name == name {
			if e.Builtin {
				return ErrPresetBuiltin
			}
			p.deleted = append(p.deleted, name)
			return nil
		}
	}
	return fs.ErrNotExist
}

var adminBasic = AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")}

// v3Env is newEnv plus the v0.3 stores (account, alerts, dashboard,
// deletable presets, session file in a temp dir).
func v3Env(t *testing.T, auth AuthConfig) (*env, *fakeAccount, *fakeAlerts, *fakeDashboard) {
	t.Helper()
	e := newEnv(t, auth)
	acc := &fakeAccount{cfg: auth}
	al := &fakeAlerts{status: AlertStatus{Transport: "auto", Effective: "mail", MailTo: "root", MailAvailable: true, Cooldown: "10m", Kinds: []AlertKind{{Kind: "stall", Description: "fan stopped"}}}, recent: []AlertRecord{{TS: 1789490000, Kind: "stall", Msg: "hdd stalled"}}}
	db := &fakeDashboard{sensors: []string{"hwmon:amdgpu:temp1"}}
	e.withDeps(t, auth, func(d *Deps) {
		d.SessionFile = filepath.Join(t.TempDir(), "sessions.json")
		d.Account = acc
		d.Alerts = al
		d.Dashboard = db
		d.Presets = &fakePresetDeleter{fakePresets: fakePresets{list: []Preset{{Name: "quiet", Channels: []string{"cpu"}}, {Name: "n5pro-balanced", Builtin: true, Channels: []string{"cpu"}}}}}
		d.About = About{Name: "n5-fangov", Version: "0.3.0-beta.1", Prerelease: "beta.1", License: "GPL-2.0-only", Credits: []Credit{{Name: "x", URL: "https://example.invalid", Note: "n"}}}
	})
	return e, acc, al, db
}

// login signs in and returns the request headers for later calls (cookie +
// CSRF) and the raw cookie.
func (e *env) login(t *testing.T, user, pass string, remember bool) (map[string]string, *http.Cookie, resp) {
	t.Helper()
	r := e.do(t, "POST", "/api/login", fmt.Sprintf(`{"user":%q,"password":%q,"remember":%v}`, user, pass, remember), csrf)
	if r.code != 200 {
		return nil, nil, r
	}
	ck := sessionCookieOf(t, r)
	return map[string]string{CSRFHeader: "1", "Cookie": ck.Name + "=" + ck.Value}, ck, r
}

func sessionCookieOf(t *testing.T, r resp) *http.Cookie {
	t.Helper()
	for _, ck := range (&http.Response{Header: r.hdr}).Cookies() {
		if ck.Name == sessionCookie {
			return ck
		}
	}
	t.Fatalf("no %s cookie in %v", sessionCookie, r.hdr.Values("Set-Cookie"))
	return nil
}

func (e *env) logLines() string {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	return strings.Join(e.logged, "\n")
}

func keys(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	decode(t, body, &m)
	return m
}

// ---- visibility -------------------------------------------------------------

// TestVisibilityStateHistory: anonymous callers get the reduced state and
// history, signed-in callers (cookie, basic, socket, auth = none) the full
// documents.
func TestVisibilityStateHistory(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	r := e.do(t, "GET", "/api/state", "", nil)
	wantCode(t, r, 200)
	m := keys(t, r.body)
	for _, k := range []string{"hwmon_path", "extra_temps", "alerts", "watched"} {
		if _, has := m[k]; has {
			t.Errorf("anonymous state carries %q: %s", k, r.body)
		}
	}
	for _, k := range []string{"ts", "status", "profile", "verified", "uptime_s", "dry_run", "channels"} {
		if _, has := m[k]; !has {
			t.Errorf("anonymous state lacks %q: %s", k, r.body)
		}
	}
	var chans []map[string]any
	decode(t, string(m["channels"]), &chans)
	if len(chans) != 2 || chans[0]["name"] != "cpu" || chans[0]["rpm"].(float64) != 2000 || chans[1]["mode"] != "manual" || len(chans[0]) != 8 {
		t.Errorf("anonymous channels = %v", chans)
	}
	r = e.do(t, "GET", "/api/history", "", nil)
	wantCode(t, r, 200)
	var pts []map[string]any
	decode(t, r.body, &pts)
	if len(pts) != 2 || len(pts[0]) != 4 || pts[1]["ts"].(float64) != 1789500000 {
		t.Errorf("anonymous history = %s", r.body)
	}
	full := func(hdr map[string]string, label string) {
		t.Helper()
		r := e.do(t, "GET", "/api/state", "", hdr)
		wantCode(t, r, 200)
		if !strings.Contains(r.body, `"hwmon_path":"/sys/class/hwmon/hwmon14"`) || !strings.Contains(r.body, `"extra_temps":{"ec:system":32}`) || !strings.Contains(r.body, `"alerts":{"stall":1789490000}`) {
			t.Errorf("%s: state not full: %s", label, r.body)
		}
		r = e.do(t, "GET", "/api/history", "", hdr)
		wantCode(t, r, 200)
		var pts []map[string]any
		decode(t, r.body, &pts)
		if len(pts) != 2 {
			t.Errorf("%s: history = %s", label, r.body)
		}
	}
	full(basicAuth("admin", "pw"), "basic")
	hdr, _, _ := e.login(t, "admin", "pw", false)
	full(hdr, "cookie")
	// socket handler: always signed in
	sock := httptest.NewServer(e.srv.SocketHandler())
	defer sock.Close()
	res, err := http.Get(sock.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4096)
	n, _ := res.Body.Read(b)
	res.Body.Close()
	if !strings.Contains(string(b[:n]), `"hwmon_path"`) {
		t.Errorf("socket state reduced: %s", b[:n])
	}
	// auth = none: everyone is signed in
	e2 := newEnv(t, AuthConfig{})
	full2 := e2.do(t, "GET", "/api/state", "", nil)
	if !strings.Contains(full2.body, `"hwmon_path"`) {
		t.Errorf("auth none state reduced: %s", full2.body)
	}
	r = e2.do(t, "GET", "/api/session", "", nil)
	if !strings.Contains(r.body, `"authenticated":true`) || !strings.Contains(r.body, `"mode":"none"`) || !strings.Contains(r.body, `"via":"none"`) {
		t.Errorf("auth none session = %s", r.body)
	}
}

// TestProtectedTreeAnonymous: /api/tls (incl. the downloads), alerts,
// dashboard, account and unknown /api paths are 401 for anonymous callers —
// silently (no log line, no limiter entry).
func TestProtectedTreeAnonymous(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	e.withDeps(t, adminBasic, func(d *Deps) { d.TLSMgr = newFakeTLSMgr("auto") })
	for _, p := range []string{"/api/tls", "/api/tls/cert.crt", "/api/tls/cert.cer", "/api/alerts", "/api/dashboard", "/api/account", "/api/sensors", "/api/nope"} {
		wantError(t, e.do(t, "GET", p, "", nil), 401, "authentication")
	}
	wantError(t, e.do(t, "DELETE", "/api/presets/quiet", "", csrf), 401, "authentication")
	if l := e.logLines(); l != "" {
		t.Errorf("anonymous 401 logged: %q", l)
	}
	e.srv.limiter.mu.Lock()
	n := len(e.srv.limiter.byIP)
	e.srv.limiter.mu.Unlock()
	if n != 0 {
		t.Errorf("anonymous 401 counted: %d limiter entries", n)
	}
	wantCode(t, e.do(t, "GET", "/api/tls", "", basicAuth("admin", "pw")), 200)
	wantCode(t, e.do(t, "GET", "/api/tls/cert.crt", "", basicAuth("admin", "pw")), 200)
}

// ---- sessions ---------------------------------------------------------------

// TestLoginLogout: CSRF on login, failure counted and logged, success sets
// the cookie with the right attributes, the cookie signs requests, logout
// revokes it.
func TestLoginLogout(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	e.srv.limiter.sleep = func(time.Duration) {}
	wantError(t, e.do(t, "POST", "/api/login", `{"user":"admin","password":"pw"}`, nil), 403, CSRFHeader)
	r := e.do(t, "POST", "/api/login", `{"user":"admin","password":"nope"}`, csrf)
	wantError(t, r, 401, "invalid user or password")
	if len(r.hdr.Values("Set-Cookie")) != 0 {
		t.Errorf("cookie on failed login: %v", r.hdr.Values("Set-Cookie"))
	}
	if l := e.logLines(); !strings.Contains(l, "web: login failure from 127.0.0.1 (user \"admin\"") || strings.Contains(l, "nope") {
		t.Errorf("login failure log = %q", l)
	}
	e.srv.limiter.mu.Lock()
	n := e.srv.limiter.byIP[remoteIPOf(e)]
	e.srv.limiter.mu.Unlock()
	if n == nil || n.n != 1 {
		t.Fatalf("login failure not counted: %+v", n)
	}
	wantError(t, e.do(t, "POST", "/api/login", `{"user":"admin","password":"pw","extra":1}`, csrf), 400, "invalid JSON")
	wantError(t, e.do(t, "POST", "/api/login", `{"user":"admin","password":"`+strings.Repeat("x", maxJSONBody)+`"}`, csrf), 413, "exceeds")

	hdr, ck, r := e.login(t, "admin", "pw", false)
	wantCode(t, r, 200)
	var body struct {
		OK       bool   `json:"ok"`
		User     string `json:"user"`
		Expires  int64  `json:"expires"`
		Remember bool   `json:"remember"`
	}
	decode(t, r.body, &body)
	if !body.OK || body.User != "admin" || body.Remember {
		t.Fatalf("login body = %s", r.body)
	}
	if d := time.Until(time.Unix(body.Expires, 0)); d < sessionTTL-time.Minute || d > sessionTTL+time.Minute {
		t.Errorf("expires in %v, want ~%v", d, sessionTTL)
	}
	if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.Secure || ck.MaxAge < int(sessionTTL.Seconds())-60 || ck.MaxAge > int(sessionTTL.Seconds()) {
		t.Errorf("cookie attributes: %+v", ck)
	}
	if len(ck.Value) != 43 || strings.ContainsAny(ck.Value, "+/=") {
		t.Errorf("token %q is not 32 bytes base64url", ck.Value)
	}
	// success reset the limiter
	e.srv.limiter.mu.Lock()
	_, tracked := e.srv.limiter.byIP[remoteIPOf(e)]
	e.srv.limiter.mu.Unlock()
	if tracked {
		t.Error("limiter not reset after login")
	}
	// the cookie signs reads and writes
	wantCode(t, e.do(t, "GET", "/api/config", "", hdr), 200)
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, hdr), 200)
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, map[string]string{"Cookie": hdr["Cookie"]}), 403, CSRFHeader)
	r = e.do(t, "GET", "/api/session", "", hdr)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"authenticated":true`) || !strings.Contains(r.body, `"user":"admin"`) || !strings.Contains(r.body, `"via":"cookie"`) || !strings.Contains(r.body, `"remember":false`) || !strings.Contains(r.body, `"mode":"basic"`) {
		t.Errorf("session = %s", r.body)
	}
	r = e.do(t, "GET", "/api/session", "", nil)
	if !strings.Contains(r.body, `"authenticated":false`) || !strings.Contains(r.body, `"via":"none"`) {
		t.Errorf("anonymous session = %s", r.body)
	}
	r = e.do(t, "GET", "/api/session", "", basicAuth("admin", "pw"))
	if !strings.Contains(r.body, `"via":"basic"`) {
		t.Errorf("basic session = %s", r.body)
	}
	// a bogus cookie is anonymous, silently
	wantError(t, e.do(t, "GET", "/api/config", "", map[string]string{"Cookie": sessionCookie + "=bogus"}), 401, "authentication")
	// logout revokes and clears
	r = e.do(t, "POST", "/api/logout", "", hdr)
	wantCode(t, r, 204)
	if c := sessionCookieOf(t, r); c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("logout cookie = %+v", c)
	}
	wantError(t, e.do(t, "GET", "/api/config", "", hdr), 401, "authentication")
	wantCode(t, e.do(t, "POST", "/api/logout", "", csrf), 204) // no session: still fine
	wantError(t, e.do(t, "POST", "/api/logout", "", nil), 403, CSRFHeader)
	// auth = none → login 409
	e2 := newEnv(t, AuthConfig{})
	wantError(t, e2.do(t, "POST", "/api/login", `{"user":"a","password":"b"}`, csrf), 409, "auth is none")
}

// TestLoginThrottled: an IP with limitConcurrent sleeping failures gets 429
// from login before any hash is computed.
func TestLoginThrottled(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	e.srv.limiter.mu.Lock()
	e.srv.limiter.byIP[remoteIPOf(e)] = &authFails{n: limitFree + 1, last: time.Now(), sleeping: limitConcurrent}
	e.srv.limiter.mu.Unlock()
	wantError(t, e.do(t, "POST", "/api/login", `{"user":"admin","password":"pw"}`, csrf), 429, "too many")
	wantError(t, e.do(t, "POST", "/api/account/password", `{"current_password":"pw","new_password":"longenough"}`, basicAuth("admin", "pw")), 429, "too many")
}

// TestLoginRemember: remember=true → ~30 days.
func TestLoginRemember(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	hdr, ck, r := e.login(t, "admin", "pw", true)
	wantCode(t, r, 200)
	var body struct {
		Expires  int64 `json:"expires"`
		Remember bool  `json:"remember"`
	}
	decode(t, r.body, &body)
	if d := time.Until(time.Unix(body.Expires, 0)); !body.Remember || d < sessionRememberTTL-time.Minute || d > sessionRememberTTL+time.Minute {
		t.Errorf("remember: %+v, expires in %v", body, d)
	}
	if ck.MaxAge < int(sessionRememberTTL.Seconds())-60 {
		t.Errorf("cookie Max-Age = %d", ck.MaxAge)
	}
	r = e.do(t, "GET", "/api/session", "", hdr)
	if !strings.Contains(r.body, `"remember":true`) {
		t.Errorf("session = %s", r.body)
	}
}

// TestSessionCookieSecureOverTLS: the Secure attribute follows the transport.
func TestSessionCookieSecureOverTLS(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	e.ts.Close()
	e.ts = httptest.NewTLSServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
	_, ck, r := e.login(t, "admin", "pw", false)
	wantCode(t, r, 200)
	if !ck.Secure {
		t.Errorf("cookie over TLS lacks Secure: %+v", ck)
	}
	if r.hdr.Get("Strict-Transport-Security") == "" {
		t.Error("no HSTS over TLS")
	}
}

// TestSessionStore: file persistence across instances, expiry, LastSeen
// refresh at most once a minute, cap 50, RevokeAll with keep, List order.
func TestSessionStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	// load() runs on the real clock before the fake one is installed, so
	// the fixture time is anchored to now.
	now := time.Now().Truncate(time.Second)
	st := NewSessionStore(path, logf).(*sessionStore)
	st.now = func() time.Time { return now }
	tok1, s1, err := st.Create("admin", false, "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	if len(s1.ID) != 8 || s1.ID != sessionKey(tok1)[:8] || s1.Expires.Sub(now) != sessionTTL || s1.User != "admin" || s1.IP != "203.0.113.5" {
		t.Fatalf("session = %+v", s1)
	}
	now = now.Add(time.Second)
	tok2, s2, _ := st.Create("admin", true, "203.0.113.6")
	if s2.Expires.Sub(now) != sessionRememberTTL || !s2.Remember {
		t.Fatalf("remember session = %+v", s2)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("session file mode: %v %v", fi, err)
		}
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), tok1) || strings.Contains(string(b), tok2) || !strings.Contains(string(b), sessionKey(tok1)) {
		t.Errorf("file carries a token or lacks the key: %s", b)
	}
	// LastSeen: unchanged within a minute, refreshed after
	now = now.Add(30 * time.Second)
	if s, ok := st.Lookup(tok1); !ok || !s.LastSeen.Equal(s1.LastSeen) {
		t.Errorf("LastSeen refreshed too early: %+v", s)
	}
	now = now.Add(31 * time.Second)
	if s, ok := st.Lookup(tok1); !ok || !s.LastSeen.Equal(now) {
		t.Errorf("LastSeen not refreshed: %+v", s)
	}
	// a second instance loads the file
	st2 := NewSessionStore(path, logf).(*sessionStore)
	st2.now = func() time.Time { return now }
	if s, ok := st2.Lookup(tok2); !ok || s.ID != s2.ID || !s.Remember {
		t.Fatalf("reloaded session = %+v %v", s, ok)
	}
	if _, ok := st2.Lookup("nope"); ok {
		t.Error("unknown token found")
	}
	l := st2.List()
	if len(l) != 2 || l[0].ID != s2.ID || l[1].ID != s1.ID {
		t.Errorf("List = %+v", l)
	}
	// expiry: the 12 h one goes, the 30 d one stays
	now = now.Add(sessionTTL)
	if _, ok := st2.Lookup(tok1); ok {
		t.Error("expired session found")
	}
	if _, ok := st2.Lookup(tok2); !ok {
		t.Error("remember session lost")
	}
	st3 := NewSessionStore(path, logf).(*sessionStore)
	st3.now = func() time.Time { return now }
	if l := st3.List(); len(l) != 1 || l[0].ID != s2.ID {
		t.Errorf("after expiry List = %+v", l)
	}
	// revoke + revoke all with keep
	tok3, _, _ := st3.Create("admin", false, "")
	tok4, _, _ := st3.Create("admin", false, "")
	st3.Revoke(tok3)
	if _, ok := st3.Lookup(tok3); ok {
		t.Error("revoked session found")
	}
	st3.RevokeAll(tok4)
	if l := st3.List(); len(l) != 1 || l[0].ID != sessionKey(tok4)[:8] {
		t.Errorf("RevokeAll(keep) List = %+v", l)
	}
	st3.RevokeAll("")
	if len(st3.List()) != 0 {
		t.Error("RevokeAll(\"\") left sessions")
	}
	// cap: the oldest is dropped
	var first string
	for i := 0; i < sessionMax+1; i++ {
		now = now.Add(time.Second)
		tok, _, _ := st3.Create("admin", false, "")
		if i == 0 {
			first = tok
		}
	}
	if len(st3.List()) != sessionMax {
		t.Errorf("List after cap = %d", len(st3.List()))
	}
	if _, ok := st3.Lookup(first); ok {
		t.Error("oldest session survived the cap")
	}
	if len(logged) != 0 {
		t.Errorf("unexpected log lines: %v", logged)
	}
	// memory-only store
	mem := NewSessionStore("", logf).(*sessionStore)
	tok, _, err := mem.Create("a", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mem.Lookup(tok); !ok || len(logged) != 0 {
		t.Errorf("memory store: lookup %v, log %v", ok, logged)
	}
}

// TestSessionStoreWriteFailure: an unwritable path is logged once; the
// store keeps working in memory.
func TestSessionStoreWriteFailure(t *testing.T) {
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	st := NewSessionStore(filepath.Join(t.TempDir(), "missing", "sessions.json"), logf)
	tok, _, err := st.Create("admin", false, "")
	if err != nil {
		t.Fatal(err)
	}
	st.Create("admin", false, "")
	if _, ok := st.Lookup(tok); !ok {
		t.Error("session lost in memory")
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "web: sessions: write") || !strings.Contains(logged[0], "continuing in memory") {
		t.Errorf("log = %v", logged)
	}
}

// ---- account ----------------------------------------------------------------

// TestAccountPassword: wrong current → 403 + limiter count; ok → the new
// password works, the old one does not, other sessions are revoked, the
// caller's cookie survives.
func TestAccountPassword(t *testing.T) {
	e, acc, _, _ := v3Env(t, adminBasic)
	e.srv.limiter.sleep = func(time.Duration) {}
	a, _, _ := e.login(t, "admin", "pw", false)
	b, _, _ := e.login(t, "admin", "pw", true)
	wantError(t, e.do(t, "POST", "/api/account/password", `{"current_password":"pw","new_password":"short"}`, a), 400, "8..128")
	wantError(t, e.do(t, "POST", "/api/account/password", `{"current_password":"pw","new_password":"`+strings.Repeat("x", 129)+`"}`, a), 400, "8..128")
	wantError(t, e.do(t, "POST", "/api/account/password", `{"current_password":"wrong","new_password":"newpassword"}`, a), 403, "current password wrong")
	e.srv.limiter.mu.Lock()
	f := e.srv.limiter.byIP[remoteIPOf(e)]
	e.srv.limiter.mu.Unlock()
	if f == nil || f.n != 1 {
		t.Fatalf("wrong current password not counted: %+v", f)
	}
	if len(acc.updates) != 0 {
		t.Fatal("account updated on refusal")
	}
	r := e.do(t, "POST", "/api/account/password", `{"current_password":"pw","new_password":"newpassword"}`, a)
	wantCode(t, r, 200)
	if len(acc.updates) != 1 || acc.updates[0][0] != "" || !strings.HasPrefix(acc.updates[0][1], "pbkdf2$") || !VerifyPassword("admin", "newpassword", acc.updates[0][1]) {
		t.Fatalf("updates = %v", acc.updates)
	}
	if !strings.Contains(e.logLines(), "web: account password changed by 127.0.0.1") {
		t.Errorf("no audit line: %q", e.logLines())
	}
	wantError(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "pw")), 401, "authentication")
	wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "newpassword")), 200)
	wantCode(t, e.do(t, "GET", "/api/config", "", a), 200)
	wantError(t, e.do(t, "GET", "/api/config", "", b), 401, "authentication")
	// login with the new password; a change via basic auth revokes every cookie
	c, _, r := e.login(t, "admin", "newpassword", false)
	wantCode(t, r, 200)
	wantCode(t, e.do(t, "POST", "/api/account/password", `{"current_password":"newpassword","new_password":"thirdpassword"}`, basicAuth("admin", "newpassword")), 200)
	wantError(t, e.do(t, "GET", "/api/config", "", a), 401, "authentication")
	wantError(t, e.do(t, "GET", "/api/config", "", c), 401, "authentication")
	if len(e.srv.sessions.List()) != 0 {
		t.Errorf("sessions left: %+v", e.srv.sessions.List())
	}
	// store failure → 500, credentials unchanged
	acc.err = errors.New("disk full")
	wantError(t, e.do(t, "POST", "/api/account/password", `{"current_password":"thirdpassword","new_password":"fourthpassword"}`, basicAuth("admin", "thirdpassword")), 500, "disk full")
	wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "thirdpassword")), 200)
}

// TestAccountUser: rename re-hashes for the new name (also from a legacy
// hash), validates the name, keeps the caller's session.
func TestAccountUser(t *testing.T) {
	legacy := AuthConfig{Mode: "basic", User: "admin", PasswordHash: LegacyPasswordHash("admin", "pw")}
	e, acc, _, _ := v3Env(t, legacy)
	a, _, _ := e.login(t, "admin", "pw", false)
	b, _, _ := e.login(t, "admin", "pw", false)
	wantError(t, e.do(t, "POST", "/api/account/user", `{"current_password":"pw","user":"bad name"}`, a), 400, "user must match")
	wantError(t, e.do(t, "POST", "/api/account/user", `{"current_password":"pw","user":""}`, a), 400, "user must match")
	wantError(t, e.do(t, "POST", "/api/account/user", `{"current_password":"nope","user":"root"}`, a), 403, "current password wrong")
	r := e.do(t, "POST", "/api/account/user", `{"current_password":"pw","user":"root.ops-1"}`, a)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"user":"root.ops-1"`) {
		t.Errorf("body = %s", r.body)
	}
	if len(acc.updates) != 1 || acc.updates[0][0] != "root.ops-1" || !strings.HasPrefix(acc.updates[0][1], "pbkdf2$") || !VerifyPassword("root.ops-1", "pw", acc.updates[0][1]) {
		t.Fatalf("updates = %v", acc.updates)
	}
	if !strings.Contains(e.logLines(), "web: account user changed by 127.0.0.1") {
		t.Errorf("no audit line: %q", e.logLines())
	}
	wantError(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "pw")), 401, "authentication")
	wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("root.ops-1", "pw")), 200)
	wantCode(t, e.do(t, "GET", "/api/config", "", a), 200)
	wantError(t, e.do(t, "GET", "/api/config", "", b), 401, "authentication")
	r = e.do(t, "GET", "/api/account", "", a)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"user":"root.ops-1"`) {
		t.Errorf("account = %s", r.body)
	}
}

// TestAccountGetRevoke: the session list marks the caller; revoke others
// keeps the caller's cookie.
func TestAccountGetRevoke(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	a, ckA, _ := e.login(t, "admin", "pw", false)
	b, _, _ := e.login(t, "admin", "pw", true)
	r := e.do(t, "GET", "/api/account", "", a)
	wantCode(t, r, 200)
	var out struct {
		User     string `json:"user"`
		Mode     string `json:"mode"`
		Sessions []struct {
			ID       string `json:"id"`
			Remember bool   `json:"remember"`
			IP       string `json:"ip"`
			Current  bool   `json:"current"`
		} `json:"sessions"`
	}
	decode(t, r.body, &out)
	if out.User != "admin" || out.Mode != "basic" || len(out.Sessions) != 2 {
		t.Fatalf("account = %s", r.body)
	}
	cur := 0
	for _, s := range out.Sessions {
		if s.Current {
			cur++
			if s.ID != sessionKey(ckA.Value)[:8] || s.Remember {
				t.Errorf("current session = %+v", s)
			}
		}
		if s.IP != "127.0.0.1" {
			t.Errorf("session ip = %q", s.IP)
		}
	}
	if cur != 1 {
		t.Errorf("%d current sessions", cur)
	}
	// via basic: nothing is current
	r = e.do(t, "GET", "/api/account", "", basicAuth("admin", "pw"))
	if strings.Contains(r.body, `"current":true`) {
		t.Errorf("basic caller has a current session: %s", r.body)
	}
	wantError(t, e.do(t, "POST", "/api/account/sessions/revoke", `{"others":false}`, a), 400, "others")
	r = e.do(t, "POST", "/api/account/sessions/revoke", `{"others":true}`, a)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"revoked":1`) {
		t.Errorf("revoke = %s", r.body)
	}
	wantCode(t, e.do(t, "GET", "/api/config", "", a), 200)
	wantError(t, e.do(t, "GET", "/api/config", "", b), 401, "authentication")
	// via basic: every session goes
	e.login(t, "admin", "pw", false)
	r = e.do(t, "POST", "/api/account/sessions/revoke", `{"others":true}`, basicAuth("admin", "pw"))
	if !strings.Contains(r.body, `"revoked":2`) {
		t.Errorf("revoke via basic = %s", r.body)
	}
}

// TestAccountAuthNone: with auth = none the account changes answer 409 and
// everything is visible; the account card still reports the mode.
func TestAccountAuthNone(t *testing.T) {
	e, _, _, _ := v3Env(t, AuthConfig{})
	for _, p := range []string{"/api/account/password", "/api/account/user"} {
		wantError(t, e.do(t, "POST", p, `{"current_password":"pw","new_password":"longenough","user":"x"}`, csrf), 409, "auth is none")
	}
	r := e.do(t, "GET", "/api/account", "", nil)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"mode":"none"`) || !strings.Contains(r.body, `"sessions":[]`) {
		t.Errorf("account = %s", r.body)
	}
	wantCode(t, e.do(t, "GET", "/api/config", "", nil), 200)
	wantCode(t, e.do(t, "GET", "/api/alerts", "", nil), 200)
	wantCode(t, e.do(t, "GET", "/api/tls", "", nil), 501) // no manager, but reachable
	r = e.do(t, "GET", "/api/version", "", nil)
	if !strings.Contains(r.body, `"auth":"none"`) {
		t.Errorf("version = %s", r.body)
	}
}

// TestV3NotImplementedWithoutDeps: nil stores → 501 on every new endpoint;
// a preset store without Delete → 501.
func TestV3NotImplementedWithoutDeps(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/account", ""},
		{"POST", "/api/account/password", `{"current_password":"pw","new_password":"longenough"}`},
		{"POST", "/api/account/user", `{"current_password":"pw","user":"x"}`},
		{"POST", "/api/account/sessions/revoke", `{"others":true}`},
		{"GET", "/api/alerts", ""},
		{"PUT", "/api/alerts", `{"transport":"log","mail_to":"root"}`},
		{"POST", "/api/alerts/test", ""},
		{"POST", "/api/alerts/template", ""},
		{"GET", "/api/dashboard", ""},
		{"PUT", "/api/dashboard", `{"sensors":[]}`},
		{"DELETE", "/api/presets/quiet", ""},
	} {
		if r := e.do(t, c.method, c.path, c.body, csrf); r.code != 501 {
			t.Errorf("%s %s = %d %s, want 501", c.method, c.path, r.code, r.body)
		}
	}
}

// ---- alerts -----------------------------------------------------------------

func TestAlertsEndpoints(t *testing.T) {
	e, _, al, _ := v3Env(t, adminBasic)
	ok := basicAuth("admin", "pw")
	r := e.do(t, "GET", "/api/alerts", "", ok)
	wantCode(t, r, 200)
	m := keys(t, r.body)
	for _, k := range []string{"transport", "effective", "mail_to", "pve_available", "mail_available", "template", "cooldown", "kinds", "last", "recent"} {
		if _, has := m[k]; !has {
			t.Errorf("alerts lacks %q: %s", k, r.body)
		}
	}
	if string(m["last"]) != `{"stall":1789490000}` || !strings.Contains(string(m["recent"]), `"msg":"hdd stalled"`) || !strings.Contains(string(m["kinds"]), `"fan stopped"`) {
		t.Errorf("alerts = %s", r.body)
	}
	al.recent = nil
	al.status.Kinds = nil
	r = e.do(t, "GET", "/api/alerts", "", ok)
	if !strings.Contains(r.body, `"recent":[]`) || !strings.Contains(r.body, `"kinds":[]`) {
		t.Errorf("nil lists: %s", r.body)
	}
	// test
	r = e.do(t, "POST", "/api/alerts/test", "", ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"transport":"mail"`) || !strings.Contains(r.body, `"ok":true`) {
		t.Errorf("test = %s", r.body)
	}
	al.testErr = errors.New("sendmail: exit 1")
	r = e.do(t, "POST", "/api/alerts/test", "", ok)
	wantError(t, r, 502, "sendmail")
	if !strings.Contains(r.body, `"transport":"mail"`) {
		t.Errorf("test failure = %s", r.body)
	}
	// template
	r = e.do(t, "POST", "/api/alerts/template", "", ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"path":"/etc/pve/notification-templates/default"`) {
		t.Errorf("template = %s", r.body)
	}
	al.templateErr = fmt.Errorf("no PVE here: %w", errors.ErrUnsupported)
	wantError(t, e.do(t, "POST", "/api/alerts/template", "", ok), 501, "no PVE")
	al.templateErr = errors.New("permission denied")
	wantError(t, e.do(t, "POST", "/api/alerts/template", "", ok), 500, "run: n5-fangov alerts template")
	// configure
	r = e.do(t, "PUT", "/api/alerts", `{"transport":"log","mail_to":"ops"}`, ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"ok":true`) || !strings.Contains(r.body, `"transport":"log"`) || !strings.Contains(r.body, `"mail_to":"ops"`) || len(al.configured) != 1 {
		t.Errorf("configure = %s %v", r.body, al.configured)
	}
	al.configErr = errors.New("unknown transport \"fax\"")
	wantError(t, e.do(t, "PUT", "/api/alerts", `{"transport":"fax","mail_to":"ops"}`, ok), 400, "fax")
	wantError(t, e.do(t, "PUT", "/api/alerts", `{"transport":`, ok), 400, "invalid JSON")
	// all protected
	wantError(t, e.do(t, "POST", "/api/alerts/test", "", csrf), 401, "authentication")
	wantError(t, e.do(t, "PUT", "/api/alerts", `{"transport":"log"}`, csrf), 401, "authentication")
}

// ---- dashboard --------------------------------------------------------------

func TestDashboardEndpoints(t *testing.T) {
	e, _, _, db := v3Env(t, adminBasic)
	ok := basicAuth("admin", "pw")
	r := e.do(t, "GET", "/api/dashboard", "", ok)
	wantCode(t, r, 200)
	if r.body != "{\"sensors\":[\"hwmon:amdgpu:temp1\"]}\n" {
		t.Errorf("dashboard = %s", r.body)
	}
	r = e.do(t, "PUT", "/api/dashboard", `{"sensors":["hwmon:nic1:temp1","bogus:x"]}`, ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"sensors":["hwmon:nic1:temp1","bogus:x"]`) || !strings.Contains(r.body, `"warnings":["bogus:x does not resolve"]`) || len(db.sensors) != 2 {
		t.Errorf("put = %s", r.body)
	}
	r = e.do(t, "PUT", "/api/dashboard", `{"sensors":[]}`, ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"sensors":[]`) || !strings.Contains(r.body, `"warnings":[]`) {
		t.Errorf("empty put = %s", r.body)
	}
	wantError(t, e.do(t, "PUT", "/api/dashboard", `{}`, ok), 400, "array required")
	wantError(t, e.do(t, "PUT", "/api/dashboard", `{"sensors":["a","b","c","d","e","f","g","h","i"]}`, ok), 400, "at most 8")
	db.err = errors.New("bad id")
	wantError(t, e.do(t, "PUT", "/api/dashboard", `{"sensors":["x"]}`, ok), 400, "bad id")
	db.sensors = nil
	db.err = nil
	if r := e.do(t, "GET", "/api/dashboard", "", ok); !strings.Contains(r.body, `"sensors":[]`) {
		t.Errorf("nil list = %s", r.body)
	}
	wantError(t, e.do(t, "GET", "/api/dashboard", "", nil), 401, "authentication")
}

// ---- about / version / presets ---------------------------------------------

func TestAboutAndVersion(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	r := e.do(t, "GET", "/api/about", "", nil)
	wantCode(t, r, 200)
	var a About
	decode(t, r.body, &a)
	if a.Name != "n5-fangov" || a.Version != "0.3.0-beta.1" || a.Prerelease != "beta.1" || a.License != "GPL-2.0-only" || a.Go != runtime.Version() || len(a.Credits) != 1 {
		t.Errorf("about = %+v", a)
	}
	r = e.do(t, "GET", "/api/version", "", nil)
	if !strings.Contains(r.body, `"prerelease":"beta.1"`) || !strings.Contains(r.body, `"auth":"basic"`) {
		t.Errorf("version = %s", r.body)
	}
	// defaults for an empty About
	e2 := newEnv(t, AuthConfig{})
	r = e2.do(t, "GET", "/api/about", "", nil)
	if !strings.Contains(r.body, `"name":"n5-fangov"`) || !strings.Contains(r.body, `"version":"1.2.3-test"`) || !strings.Contains(r.body, `"credits":[]`) || !strings.Contains(r.body, `"prerelease":""`) {
		t.Errorf("default about = %s", r.body)
	}
}

func TestPresetDelete(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	ok := basicAuth("admin", "pw")
	wantError(t, e.do(t, "DELETE", "/api/presets/quiet", "", csrf), 401, "authentication")
	wantError(t, e.do(t, "DELETE", "/api/presets/nope", "", ok), 404, "unknown preset")
	wantError(t, e.do(t, "DELETE", "/api/presets/n5pro-balanced", "", ok), 409, "built-in")
	wantError(t, e.do(t, "DELETE", "/api/presets/Bad!", "", ok), 400, "invalid preset name")
	r := e.do(t, "DELETE", "/api/presets/quiet", "", ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"deleted":"quiet"`) || !strings.Contains(e.logLines(), `web: preset "quiet" deleted by 127.0.0.1`) {
		t.Errorf("delete = %s / %q", r.body, e.logLines())
	}
	if d := e.srv.deps.Presets.(*fakePresetDeleter); len(d.deleted) != 1 || d.deleted[0] != "quiet" {
		t.Errorf("deleted = %v", d.deleted)
	}
}

// ---- error log filter --------------------------------------------------------

// TestHandshakeFilter: handshake-error lines are swallowed and counted (one
// summary per window), foreign lines pass through.
func TestHandshakeFilter(t *testing.T) {
	var logged []string
	now := time.Unix(1789500000, 0)
	f := newHandshakeFilter(func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }, func() time.Time { return now })
	lg := func(line string) {
		if _, err := f.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	lg("http: TLS handshake error from 203.0.113.7:51234: remote error: tls: unknown certificate")
	if len(logged) != 1 || logged[0] != "web: 1 TLS handshakes rejected by 1 client(s) since last summary (certificate not trusted by the browser yet?)" {
		t.Fatalf("first summary = %v", logged)
	}
	lg("http: TLS handshake error from 203.0.113.7:51235: EOF")
	lg("http: TLS handshake error from [2001:db8::9]:4000: read tcp: i/o timeout")
	lg("http: TLS handshake error from 203.0.113.8:1: EOF")
	if len(logged) != 1 {
		t.Fatalf("summary inside the window: %v", logged)
	}
	lg("http: Accept error: accept tcp: too many open files; retrying in 1s")
	if len(logged) != 2 || logged[1] != "http: Accept error: accept tcp: too many open files; retrying in 1s" {
		t.Fatalf("foreign line: %v", logged)
	}
	now = now.Add(handshakeSummaryEvery)
	lg("http: TLS handshake error from 203.0.113.9:2: EOF")
	if len(logged) != 3 || logged[2] != "web: 4 TLS handshakes rejected by 4 client(s) since last summary (certificate not trusted by the browser yet?)" {
		t.Fatalf("second summary = %v", logged)
	}
	if ip := handshakeIP("http: TLS handshake error from [2001:db8::9]:4000: EOF"); ip != "2001:db8::9" {
		t.Errorf("handshakeIP = %q", ip)
	}
}

func (p *fakePresetDeleter) Detail(name string) (PresetDetail, error) {
	for _, e := range p.list {
		if e.Name == name {
			return PresetDetail{Name: name, Builtin: e.Builtin, Channels: []PresetChannel{{Name: "cpu", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{40, 80}, {80, 255}}, Critical: 88, Stop: "auto"}}}, nil
		}
	}
	return PresetDetail{}, fs.ErrNotExist
}

func (p *fakePresetDeleter) Rename(oldName, newName string) error {
	for _, e := range p.list {
		if e.Name == newName {
			if e.Builtin {
				return ErrPresetBuiltin
			}
			return fs.ErrExist
		}
	}
	for i, e := range p.list {
		if e.Name == oldName {
			if e.Builtin {
				return ErrPresetBuiltin
			}
			p.list[i].Name = newName
			return nil
		}
	}
	return fs.ErrNotExist
}

// TestPresetDetailRename: GET /api/presets/{name} shows the tables, rename
// refuses built-ins and taken names, both are protected.
func TestPresetDetailRename(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	wantCode(t, e.do(t, "GET", "/api/presets/quiet", "", nil), 401)
	r := e.do(t, "GET", "/api/presets/quiet", "", basicAuth("admin", "pw"))
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"curve":[[40,80],[80,255]]`) || !strings.Contains(r.body, `"sensor":"k10temp"`) {
		t.Errorf("detail = %s", r.body)
	}
	wantError(t, e.do(t, "GET", "/api/presets/none", "", basicAuth("admin", "pw")), 404, "unknown preset")
	wantError(t, e.do(t, "GET", "/api/presets/Bad%20Name", "", basicAuth("admin", "pw")), 400, "invalid")
	auth := basicAuth("admin", "pw")
	auth[CSRFHeader] = "1"
	wantError(t, e.do(t, "POST", "/api/presets/quiet/rename", `{"name":"n5pro-balanced"}`, auth), 409, "built-in")
	wantError(t, e.do(t, "POST", "/api/presets/n5pro-balanced/rename", `{"name":"x"}`, auth), 409, "built-in")
	wantError(t, e.do(t, "POST", "/api/presets/none/rename", `{"name":"x"}`, auth), 404, "unknown preset")
	wantError(t, e.do(t, "POST", "/api/presets/quiet/rename", `{"name":"Bad Name"}`, auth), 400, "invalid")
	r = e.do(t, "POST", "/api/presets/quiet/rename", `{"name":"silent"}`, auth)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"name":"silent"`) {
		t.Errorf("rename = %s", r.body)
	}
	wantCode(t, e.do(t, "GET", "/api/presets/silent", "", basicAuth("admin", "pw")), 200)
}
