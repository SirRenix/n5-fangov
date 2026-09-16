package web

import (
	"encoding/json"
	"fmt"
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

// ---- visibility -------------------------------------------------------------

// TestVisibilityStateHistory: anonymous callers get the reduced state and
// history, signed-in callers (cookie, basic, socket, auth = none) the full
// documents.
func TestVisibilityStateHistory(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
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
	e, _, _, _ := storesEnv(t, adminBasic)
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
	e, _, _, _ := storesEnv(t, adminBasic)
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
	e, _, _, _ := storesEnv(t, adminBasic)
	e.srv.limiter.mu.Lock()
	e.srv.limiter.byIP[remoteIPOf(e)] = &authFails{n: limitFree + 1, last: time.Now()}
	e.srv.limiter.sleeping[addrKey(remoteIPOf(e))] = &sleepers{n: limitConcurrent}
	e.srv.limiter.mu.Unlock()
	wantError(t, e.do(t, "POST", "/api/login", `{"user":"admin","password":"pw"}`, csrf), 429, "too many")
	wantError(t, e.do(t, "POST", "/api/account/password", `{"current_password":"pw","new_password":"longenough"}`, basicAuth("admin", "pw")), 429, "too many")
}

// TestLoginRemember: remember=true → ~30 days.
func TestLoginRemember(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
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
	e, _, _, _ := storesEnv(t, adminBasic)
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

// ---- session expiry over HTTP ------------------------------------------------

// fakeStoreClock replaces the session store's clock with a settable one.
// It must be installed before the first request: handlers read the func
// from their own goroutines, the value behind it is mutex-guarded.
type fakeStoreClock struct {
	mu  sync.Mutex
	now time.Time
}

func installStoreClock(e *env, base time.Time) *fakeStoreClock {
	c := &fakeStoreClock{now: base}
	e.srv.sessions.(*sessionStore).now = func() time.Time {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.now
	}
	return c
}

func (c *fakeStoreClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// TestSessionExpiresOverHTTP (AUDIT 7): a plain login stops working once
// the store's clock reaches Created + 12 h, a "remember me" login at
// Created + 30 d; the protected tree answers 401, /api/session reports an
// anonymous caller, and the mirror file drops the expired entry.
func TestSessionExpiresOverHTTP(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
	if sessionTTL != 12*time.Hour || sessionRememberTTL != 30*24*time.Hour {
		t.Fatalf("TTLs changed: %s / %s", sessionTTL, sessionRememberTTL)
	}
	base := time.Now().Truncate(time.Second)
	clock := installStoreClock(e, base)
	plain, plainCk, r := e.login(t, "admin", "pw", false)
	wantCode(t, r, 200)
	remember, _, r := e.login(t, "admin", "pw", true)
	wantCode(t, r, 200)
	sessionOf := func(hdr map[string]string) map[string]any {
		t.Helper()
		var m map[string]any
		decode(t, e.do(t, "GET", "/api/session", "", map[string]string{"Cookie": hdr["Cookie"]}).body, &m)
		return m
	}
	if s := sessionOf(plain); s["authenticated"] != true || s["via"] != "cookie" || s["expires"].(float64) != float64(base.Add(sessionTTL).Unix()) {
		t.Fatalf("plain session = %v", s)
	}
	if s := sessionOf(remember); s["remember"] != true || s["expires"].(float64) != float64(base.Add(sessionRememberTTL).Unix()) {
		t.Fatalf("remember session = %v", s)
	}

	// one second before the 12 h mark both still work
	clock.set(base.Add(sessionTTL - time.Second))
	wantCode(t, e.do(t, "GET", "/api/config", "", plain), 200)
	wantCode(t, e.do(t, "GET", "/api/config", "", remember), 200)
	// at exactly 12 h the plain session is gone (Expires is not after now)
	clock.set(base.Add(sessionTTL))
	wantError(t, e.do(t, "GET", "/api/config", "", plain), 401, "authentication")
	if s := sessionOf(plain); s["authenticated"] != false || s["via"] != "none" {
		t.Errorf("expired plain session reports %v", s)
	}
	wantCode(t, e.do(t, "GET", "/api/config", "", remember), 200)
	// the expired session left the mirror file; the remember one stayed
	mirror := readSessionFile(t, e)
	if len(mirror.Sessions) != 1 || !mirror.Sessions[0].Remember {
		t.Errorf("mirror after 12 h: %+v", mirror.Sessions)
	}
	// a fresh login after expiry works and gets a new token
	plain2, plain2Ck, r := e.login(t, "admin", "pw", false)
	wantCode(t, r, 200)
	if plain2Ck.Value == plainCk.Value {
		t.Error("expired token reissued")
	}
	wantCode(t, e.do(t, "GET", "/api/config", "", plain2), 200)

	// 30 d: the remember session expires at Created + 30 d, not later
	clock.set(base.Add(sessionRememberTTL - time.Second))
	wantCode(t, e.do(t, "GET", "/api/config", "", remember), 200)
	clock.set(base.Add(sessionRememberTTL))
	wantError(t, e.do(t, "GET", "/api/config", "", remember), 401, "authentication")
	// the second plain session (created at +12 h) is long gone as well
	wantError(t, e.do(t, "GET", "/api/config", "", plain2), 401, "authentication")
	if l := e.srv.sessions.List(); len(l) != 0 {
		t.Errorf("sessions left after 30 d: %+v", l)
	}
	if mirror := readSessionFile(t, e); len(mirror.Sessions) != 0 {
		t.Errorf("mirror after 30 d: %+v", mirror.Sessions)
	}
	// logout with an expired cookie is still a clean 204
	wantCode(t, e.do(t, "POST", "/api/logout", "", plain), 204)
}

// readSessionFile decodes the env's mirror file.
func readSessionFile(t *testing.T, e *env) sessionFile {
	t.Helper()
	data, err := os.ReadFile(e.srv.deps.SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	var f sessionFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("%s: %v", e.srv.deps.SessionFile, err)
	}
	return f
}

// TestSessionRestartKeepsExpiry: after a restart (new store on the same
// mirror file) the loaded sessions keep their original Expires — the
// restart does not extend them.
func TestSessionRestartKeepsExpiry(t *testing.T) {
	e, acc, _, _ := storesEnv(t, adminBasic)
	base := time.Now().Truncate(time.Second)
	installStoreClock(e, base)
	remember, _, r := e.login(t, "admin", "pw", true)
	wantCode(t, r, 200)
	file := e.srv.deps.SessionFile
	// restart at +29 d: still valid
	restartEnv(t, e, file, adminBasic, acc)
	clock := installStoreClock(e, base.Add(29*24*time.Hour))
	wantCode(t, e.do(t, "GET", "/api/config", "", remember), 200)
	// move to +30 d on the same instance
	clock.set(base.Add(sessionRememberTTL))
	wantError(t, e.do(t, "GET", "/api/config", "", remember), 401, "authentication")
	// a restart after the deadline does not load it at all: load() skips
	// expired entries, so the store starts empty
	restartEnv(t, e, file, adminBasic, acc)
	installStoreClock(e, base.Add(sessionRememberTTL+time.Hour))
	wantError(t, e.do(t, "GET", "/api/config", "", remember), 401, "authentication")
	if l := e.srv.sessions.List(); len(l) != 0 {
		t.Errorf("expired session loaded after restart: %+v", l)
	}
}

// TestSessionStoreConcurrentCreate (AUDIT 7): 50 goroutines creating
// sessions at once on a file-backed store yield 50 distinct live sessions
// and a consistent mirror; 50 more concurrent creates keep the cap at
// sessionMax with the newest surviving. Run under -race.
func TestSessionStoreConcurrentCreate(t *testing.T) {
	if sessionMax != 50 {
		t.Fatalf("sessionMax = %d", sessionMax)
	}
	path := filepath.Join(t.TempDir(), "sessions.json")
	var logged []string
	var logMu sync.Mutex
	st := NewSessionStore(path, func(f string, a ...any) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(f, a...))
		logMu.Unlock()
	}).(*sessionStore)
	// distinct Created per goroutine so the cap has a defined "oldest";
	// anchored at the real clock because the reload below uses time.Now
	var clockMu sync.Mutex
	tick := time.Now()
	st.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		tick = tick.Add(time.Millisecond)
		return tick
	}
	create := func(n int) []string {
		tokens := make([]string, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				tokens[i], _, errs[i] = st.Create("admin", i%2 == 0, "192.0.2.7")
			}(i)
		}
		if !waitTimeout(&wg, 30*time.Second) {
			t.Fatal("concurrent Create did not finish")
		}
		for i, err := range errs {
			if err != nil {
				t.Fatalf("Create %d: %v", i, err)
			}
		}
		return tokens
	}
	first := create(sessionMax)
	seen := map[string]bool{}
	for _, tok := range first {
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
		if _, ok := st.Lookup(tok); !ok {
			t.Fatal("fresh session not found")
		}
	}
	if l := st.List(); len(l) != sessionMax {
		t.Fatalf("List = %d after %d creates", len(l), sessionMax)
	}
	// reload from the mirror: the file is complete and consistent
	st2 := NewSessionStore(path, nil).(*sessionStore)
	if l := st2.List(); len(l) != sessionMax {
		t.Fatalf("mirror holds %d sessions, want %d", len(l), sessionMax)
	}
	// a second wave: the cap holds, every new token is live, the first
	// wave is gone entirely (its Created values are all older)
	second := create(sessionMax)
	if l := st.List(); len(l) != sessionMax {
		t.Fatalf("List = %d after the second wave", len(l))
	}
	for _, tok := range second {
		if _, ok := st.Lookup(tok); !ok {
			t.Fatal("second-wave session dropped")
		}
	}
	for _, tok := range first {
		if _, ok := st.Lookup(tok); ok {
			t.Fatal("first-wave session survived the cap")
		}
	}
	logMu.Lock()
	defer logMu.Unlock()
	if len(logged) != 0 {
		t.Errorf("unexpected log lines: %v", logged)
	}
}

// TestLoginConcurrentCap: 60 parallel logins over HTTP all succeed and the
// store ends at the cap; the mirror file is intact afterwards. The global
// password-check semaphore answers 429 to the surplus callers; like a real
// client they retry, so every login eventually lands.
func TestLoginConcurrentCap(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
	const n = sessionMax + 10
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for try := 0; try < 500; try++ {
				r, err := e.try("POST", "/api/login", `{"user":"admin","password":"pw"}`, csrf)
				if err != nil {
					codes[i] = -1
					return
				}
				codes[i] = r.code
				if r.code != http.StatusTooManyRequests {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}(i)
	}
	if !waitTimeout(&wg, 60*time.Second) {
		t.Fatal("parallel logins did not finish")
	}
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("login %d: status %d", i, c)
		}
	}
	if l := e.srv.sessions.List(); len(l) != sessionMax {
		t.Errorf("sessions after %d logins: %d, want %d", n, len(l), sessionMax)
	}
	if f := readSessionFile(t, e); len(f.Sessions) != sessionMax {
		t.Errorf("mirror after %d logins: %d entries", n, len(f.Sessions))
	}
}

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

// TestSessionCookieSecureBehindProxy: the cookie is Secure over TLS, with
// Deps.TLS, or with BehindTLSProxy — and plain otherwise.
func TestSessionCookieSecureBehindProxy(t *testing.T) {
	cookieOf := func(e *env) *http.Cookie {
		t.Helper()
		_, ck, r := e.login(t, "admin", "pw", false)
		if ck == nil {
			t.Fatalf("login = %d %s", r.code, r.body)
		}
		return ck
	}
	e, _, _, _ := storesEnv(t, adminBasic)
	if cookieOf(e).Secure {
		t.Error("plain HTTP without a proxy flag: cookie Secure")
	}
	e.withDeps(t, adminBasic, func(d *Deps) { d.SessionFile = ""; d.Account = &fakeAccount{cfg: adminBasic}; d.BehindTLSProxy = true })
	if !cookieOf(e).Secure {
		t.Error("behind_tls_proxy: cookie not Secure")
	}
	e.withDeps(t, adminBasic, func(d *Deps) { d.SessionFile = ""; d.Account = &fakeAccount{cfg: adminBasic}; d.TLS = true })
	if !cookieOf(e).Secure {
		t.Error("Deps.TLS: cookie not Secure")
	}
	// logout clears with the same attribute
	req := httptest.NewRequest("POST", "http://127.0.0.1:8010/api/logout", nil)
	req.Header.Set(CSRFHeader, "1")
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	if cks := rec.Result().Cookies(); len(cks) != 1 || !cks[0].Secure || cks[0].MaxAge != -1 {
		t.Errorf("logout cookie = %v", cks)
	}
}
