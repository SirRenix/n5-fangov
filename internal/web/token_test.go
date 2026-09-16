package web

import (
	"encoding/json"
	"fmt"
	"io"
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

// bearer returns the request headers of a token caller (no CSRF header:
// a bearer caller needs none).
func bearer(secret string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + secret}
}

// mint creates a token through the store behind e and returns the secret.
func mint(t *testing.T, e *env, name, scope string, ttl time.Duration) (string, Token) {
	t.Helper()
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	secret, tok, err := e.srv.tokens.Create(name, scope, exp)
	if err != nil {
		t.Fatal(err)
	}
	return secret, tok
}

// tokenEnv is storesEnv with a token file in a temp dir.
func tokenEnv(t *testing.T) (*env, string) {
	t.Helper()
	e, _, _, _ := storesEnv(t, adminBasic)
	file := filepath.Join(t.TempDir(), "tokens.json")
	e.withDeps(t, adminBasic, func(d *Deps) {
		d.TokenFile = file
		d.Account = &fakeAccount{cfg: adminBasic}
		d.Alerts = &fakeAlerts{status: AlertStatus{Transport: "log", Effective: "log"}}
		d.Dashboard = &fakeDashboard{sensors: []string{}}
		d.SessionFile = filepath.Join(t.TempDir(), "sessions.json")
	})
	return e, file
}

// ---- store ----------------------------------------------------------------

func TestTokenStoreRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tokens.json")
	var logged []string
	st := NewTokenStore(file, func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
	secret, tok, err := st.Create("Home Assistant", "control", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "n5t_") || len(secret) != 4+43 || len(tok.ID) != 8 || tok.Name != "Home Assistant" || tok.Scope != "control" {
		t.Errorf("secret %q token %+v", secret, tok)
	}
	if tok.ID != tokenKey(secret)[:8] {
		t.Errorf("id is not the hash prefix")
	}
	// the file: 0600, format 1, hashed keys, no secret
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode %o", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(file)
	var f tokenFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Format != 1 || len(f.Tokens) != 1 || f.Tokens[0].Key != tokenKey(secret) || f.Tokens[0].Name != "Home Assistant" {
		t.Errorf("file: %v %s", err, raw)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("secret written to disk")
	}
	// default scope, never expires
	s2, t2, err := st.Create("script", "", time.Time{})
	if err != nil || t2.Scope != ScopeRead || !t2.Expires.IsZero() {
		t.Errorf("defaults: %+v %v", t2, err)
	}
	// name rule and uniqueness (case-insensitive)
	for _, bad := range []string{"", " x", "-x", "a\tb", strings.Repeat("a", 33), "ünïcode", "x/y"} {
		if _, _, err := st.Create(bad, "", time.Time{}); err != ErrTokenName {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, _, err := st.Create("home assistant", "", time.Time{}); err != ErrTokenTaken {
		t.Errorf("duplicate: %v", err)
	}
	if _, _, err := st.Create("x", "root", time.Time{}); err != ErrTokenScope {
		t.Errorf("scope: %v", err)
	}
	// lookup touches LastUsed/LastIP
	got, ok, expired := st.Lookup(secret, "192.0.2.10")
	if !ok || expired || got.ID != tok.ID || got.LastIP != "192.0.2.10" || got.LastUsed.IsZero() {
		t.Errorf("lookup: %+v %v %v", got, ok, expired)
	}
	if _, ok, _ := st.Lookup("n5t_nope", ""); ok {
		t.Errorf("unknown accepted")
	}
	if _, ok, _ := st.Lookup(secret[4:], ""); ok {
		t.Errorf("secret without prefix accepted")
	}
	// list: newest first, both tokens
	list := st.List()
	if len(list) != 2 || list[0].ID != t2.ID && list[1].ID != t2.ID {
		t.Errorf("list: %+v", list)
	}
	// reload from the file: same tokens, same lookups
	st2 := NewTokenStore(file, nil)
	if got, ok, _ := st2.Lookup(secret, "192.0.2.11"); !ok || got.Name != "Home Assistant" || got.Scope != "control" {
		t.Errorf("reload lookup: %+v %v", got, ok)
	}
	if got, ok, _ := st2.Lookup(s2, ""); !ok || got.Scope != ScopeRead {
		t.Errorf("reload lookup 2: %+v %v", got, ok)
	}
	// revoke: gone in memory and on disk
	if !st2.Revoke(tok.ID) || st2.Revoke(tok.ID) || st2.Revoke("00000000") {
		t.Errorf("revoke")
	}
	if _, ok, _ := st2.Lookup(secret, ""); ok {
		t.Errorf("revoked token still accepted")
	}
	st3 := NewTokenStore(file, nil)
	if l := st3.List(); len(l) != 1 || l[0].ID != t2.ID {
		t.Errorf("after revoke and reload: %+v", l)
	}
	// corrupt or foreign file: empty store, one log line
	if err := os.WriteFile(file, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	logged = nil
	st4 := NewTokenStore(file, func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
	if len(st4.List()) != 0 || len(logged) != 1 || !strings.Contains(logged[0], "starting empty") {
		t.Errorf("corrupt file: %d tokens, log %v", len(st4.List()), logged)
	}
	if err := os.WriteFile(file, []byte(`{"format":2,"tokens":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	logged = nil
	NewTokenStore(file, func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
	if len(logged) != 1 || !strings.Contains(logged[0], "format 2") {
		t.Errorf("foreign format: %v", logged)
	}
	// memory only
	mem := NewTokenStore("", nil)
	if s, _, err := mem.Create("m", "", time.Time{}); err != nil || s == "" {
		t.Errorf("memory store: %v", err)
	}
}

func TestTokenStoreExpiryCapTouch(t *testing.T) {
	st := NewTokenStore("", nil).(*tokenStore)
	now := time.Unix(1789500000, 0)
	st.now = func() time.Time { return now }
	secret, tok, err := st.Create("short", "read", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// LastUsed is touched at most once a minute
	if got, ok, _ := st.Lookup(secret, "a"); !ok || !got.LastUsed.Equal(now) || got.LastIP != "a" {
		t.Errorf("first touch: %+v", got)
	}
	now = now.Add(30 * time.Second)
	if got, _, _ := st.Lookup(secret, "b"); !got.LastUsed.Equal(now.Add(-30*time.Second)) || got.LastIP != "a" {
		t.Errorf("touched again within the minute: %+v", got)
	}
	now = now.Add(30 * time.Second)
	if got, _, _ := st.Lookup(secret, "c"); !got.LastUsed.Equal(now) || got.LastIP != "c" {
		t.Errorf("not touched after a minute: %+v", got)
	}
	// expiry: not ok, expired reported, token still listed until revoked
	now = now.Add(time.Hour)
	if _, ok, expired := st.Lookup(secret, "d"); ok || !expired {
		t.Errorf("expired token: ok=%v expired=%v", ok, expired)
	}
	if l := st.List(); len(l) != 1 || !l[0].Expired(now) || l[0].ID != tok.ID {
		t.Errorf("expired token must stay listed: %+v", l)
	}
	if !st.Revoke(tok.ID) {
		t.Errorf("revoke expired")
	}
	// cap 50
	for i := 0; i < tokenMax; i++ {
		if _, _, err := st.Create(fmt.Sprintf("t%d", i), "", time.Time{}); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if _, _, err := st.Create("one more", "", time.Time{}); err != ErrTokenLimit {
		t.Errorf("cap: %v", err)
	}
}

func TestScopeAllows(t *testing.T) {
	cases := map[[2]string]bool{
		{"read", "read"}: true, {"read", "control"}: false, {"read", "admin"}: false,
		{"control", "read"}: true, {"control", "control"}: true, {"control", "admin"}: false,
		{"admin", "read"}: true, {"admin", "control"}: true, {"admin", "admin"}: true,
		{"admin", "session"}: false, {"", "read"}: false, {"root", "read"}: false,
	}
	for c, want := range cases {
		if got := ScopeAllows(c[0], c[1]); got != want {
			t.Errorf("ScopeAllows(%q, %q) = %v", c[0], c[1], got)
		}
	}
}

func TestTokenLimiter(t *testing.T) {
	l := newTokenLimiter()
	now := time.Unix(1789500000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < tokenRateBurst; i++ {
		if !l.allow("a") {
			t.Fatalf("request %d refused within the burst", i)
		}
	}
	if l.allow("a") {
		t.Error("burst exceeded but allowed")
	}
	if !l.allow("b") {
		t.Error("another token is not affected")
	}
	// refill: one second → tokenRatePerSec requests
	now = now.Add(time.Second)
	for i := 0; i < tokenRatePerSec; i++ {
		if !l.allow("a") {
			t.Fatalf("refilled request %d refused", i)
		}
	}
	if l.allow("a") {
		t.Error("refill above the rate")
	}
	// the bucket never exceeds the burst
	now = now.Add(time.Hour)
	n := 0
	for l.allow("a") {
		n++
	}
	if n != tokenRateBurst {
		t.Errorf("after an idle hour %d requests allowed, want %d", n, tokenRateBurst)
	}
	// the table is bounded
	for i := 0; i < tokenRateEntries+10; i++ {
		l.allow(fmt.Sprintf("id%d", i))
	}
	if len(l.by) > tokenRateEntries {
		t.Errorf("table grew to %d", len(l.by))
	}
}

// ---- guard ----------------------------------------------------------------

func TestBearerAccepted(t *testing.T) {
	e, _ := tokenEnv(t)
	secret, tok := mint(t, e, "reader", "read", time.Hour)
	// protected read without CSRF header
	r := e.do(t, "GET", "/api/sensors", "", bearer(secret))
	wantCode(t, r, 200)
	r = e.do(t, "GET", "/api/state", "", bearer(secret))
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"hwmon_path"`) {
		t.Errorf("token caller must get the full state: %s", r.body)
	}
	// session view
	r = e.do(t, "GET", "/api/session", "", bearer(secret))
	wantCode(t, r, 200)
	var sess map[string]any
	decode(t, r.body, &sess)
	if sess["authenticated"] != true || sess["via"] != "bearer" || sess["user"] != "reader" || sess["scope"] != "read" || sess["token_id"] != tok.ID || sess["mode"] != "basic" {
		t.Errorf("session = %v", sess)
	}
	if _, has := sess["expires"]; has {
		t.Errorf("bearer session must not carry cookie members: %v", sess)
	}
	// the scheme is case-insensitive; a bare header is not a bearer
	wantCode(t, e.do(t, "GET", "/api/sensors", "", map[string]string{"Authorization": "bearer " + secret}), 200)
	// last used / last address recorded once
	list := e.srv.tokens.List()
	if len(list) != 1 || list[0].LastUsed.IsZero() || list[0].LastIP == "" {
		t.Errorf("last used: %+v", list)
	}
	// no log line for accepted tokens, no limiter entries
	if logs := e.logLines(); strings.Contains(logs, "rejected") {
		t.Errorf("log: %s", logs)
	}
}

func TestBearerRejected(t *testing.T) {
	e, _ := tokenEnv(t)
	var slept []time.Duration
	var mu sync.Mutex
	e.srv.limiter.sleep = func(d time.Duration) { mu.Lock(); slept = append(slept, d); mu.Unlock() }
	// unknown
	r := e.do(t, "GET", "/api/config", "", bearer("n5t_"+strings.Repeat("x", 43)))
	wantError(t, r, 401, "token unknown")
	if r.hdr.Get("WWW-Authenticate") != "" {
		t.Error("no challenge expected")
	}
	// expired
	st := e.srv.tokens.(*tokenStore)
	secret, _, err := st.Create("old", "admin", time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do(t, "GET", "/api/config", "", bearer(secret)), 401, "token expired")
	// a public path is refused as well: a presented credential must verify
	wantError(t, e.do(t, "GET", "/api/version", "", bearer("n5t_nope")), 401, "authentication failed")
	// an empty bearer value is a malformed credential, not a bearer:
	// refused like a malformed Basic header
	wantError(t, e.do(t, "GET", "/api/version", "", map[string]string{"Authorization": "Bearer "}), 401, "authentication required")
	// logged with the reason, counted by the limiter
	logs := e.logLines()
	if !strings.Contains(logs, "web: bearer token rejected from 127.0.0.1: unknown") || !strings.Contains(logs, "web: bearer token rejected from 127.0.0.1: expired") {
		t.Errorf("log: %s", logs)
	}
	for i := 0; i < limitFree; i++ {
		e.do(t, "GET", "/api/config", "", bearer("n5t_nope"))
	}
	mu.Lock()
	n := len(slept)
	mu.Unlock()
	if n == 0 {
		t.Errorf("bad bearer tokens beyond limitFree must be delayed")
	}
	// three rejected tokens above plus limitFree here in the bearer
	// bucket; the empty "Bearer " header failed as a Basic credential and
	// sits in the password bucket alone
	e.srv.limiter.mu.Lock()
	counted := e.srv.limiter.bearer[limitKey("127.0.0.1")]
	pw := e.srv.limiter.byIP[limitKey("127.0.0.1")]
	e.srv.limiter.mu.Unlock()
	if counted == nil || counted.n != 3+limitFree {
		t.Errorf("failures counted: %+v, want %d", counted, 3+limitFree)
	}
	if pw == nil || pw.n != 1 {
		t.Errorf("password bucket: %+v, want the one malformed header", pw)
	}
	if !strings.Contains(e.logLines(), fmt.Sprintf("%d recent failures", 3+limitFree)) {
		t.Errorf("failures not in the log: %s", e.logLines())
	}
	// a valid token resets the counter
	good, _ := mint(t, e, "good", "read", 0)
	wantCode(t, e.do(t, "GET", "/api/sensors", "", bearer(good)), 200)
	e.srv.limiter.mu.Lock()
	entries := len(e.srv.limiter.bearer)
	e.srv.limiter.mu.Unlock()
	if entries != 0 {
		t.Errorf("limiter not reset after a valid token: %d entries", entries)
	}
	// the PBKDF2 semaphore is not touched by a bearer lookup: with every
	// slot taken a token still resolves
	for i := 0; i < limitHashConcurrent; i++ {
		if !e.srv.limiter.acquire() {
			t.Fatal("acquire")
		}
	}
	defer func() {
		for i := 0; i < limitHashConcurrent; i++ {
			e.srv.limiter.release()
		}
	}()
	wantCode(t, e.do(t, "GET", "/api/sensors", "", bearer(good)), 200)
	wantError(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "pw")), 429, "too many concurrent")
}

// TestBearerSuccessKeepsPasswordDelay: the password and the bearer
// failures live in separate buckets — a valid token does not reset the
// delay that Basic failures from the same address earned, and a Basic
// success does not reset the bearer bucket.
func TestBearerSuccessKeepsPasswordDelay(t *testing.T) {
	e, _ := tokenEnv(t)
	var slept []time.Duration
	var mu sync.Mutex
	e.srv.limiter.sleep = func(d time.Duration) { mu.Lock(); slept = append(slept, d); mu.Unlock() }
	for i := 0; i < limitFree; i++ {
		wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "wrong")), 401)
	}
	good, _ := mint(t, e, "good", "read", 0)
	wantCode(t, e.do(t, "GET", "/api/sensors", "", bearer(good)), 200)
	mu.Lock()
	before := len(slept)
	mu.Unlock()
	if before != 0 {
		t.Fatalf("free attempts were delayed: %v", slept)
	}
	// the sixth Basic failure is still delayed
	wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "wrong")), 401)
	mu.Lock()
	after := len(slept)
	mu.Unlock()
	if after != 1 {
		t.Errorf("a bearer success reset the password delay: %d sleeps, want 1", after)
	}
	// the other direction: bearer failures, then a Basic success, then the
	// next bad token is still delayed
	e2, _ := tokenEnv(t)
	slept = nil
	e2.srv.limiter.sleep = e.srv.limiter.sleep
	for i := 0; i < limitFree; i++ {
		wantCode(t, e2.do(t, "GET", "/api/config", "", bearer("n5t_nope")), 401)
	}
	wantCode(t, e2.do(t, "GET", "/api/config", "", basicAuth("admin", "pw")), 200)
	wantCode(t, e2.do(t, "GET", "/api/config", "", bearer("n5t_nope")), 401)
	mu.Lock()
	n := len(slept)
	mu.Unlock()
	if n != 1 {
		t.Errorf("a Basic success reset the bearer delay: %d sleeps, want 1", n)
	}
}

// TestBearerConcurrencyCap: an address with limitConcurrent delayed
// bearer attempts in flight is answered 429 before the lookup.
func TestBearerConcurrencyCap(t *testing.T) {
	e, _ := tokenEnv(t)
	release := make(chan struct{})
	var started sync.WaitGroup
	e.srv.limiter.sleep = func(time.Duration) { started.Done(); <-release }
	// limitFree failures without delay, then limitConcurrent sleeping ones
	for i := 0; i < limitFree; i++ {
		e.do(t, "GET", "/api/config", "", bearer("n5t_nope"))
	}
	started.Add(limitConcurrent)
	var wg sync.WaitGroup
	for i := 0; i < limitConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.try("GET", "/api/config", "", bearer("n5t_nope"))
		}()
	}
	if !waitTimeout(&started, 5*time.Second) {
		t.Fatal("delayed attempts did not start")
	}
	wantError(t, e.do(t, "GET", "/api/config", "", bearer("n5t_nope")), 429, "too many concurrent")
	close(release)
	wg.Wait()
}

func TestBearerScopes(t *testing.T) {
	e, _ := tokenEnv(t)
	read, _ := mint(t, e, "r", "read", time.Hour)
	control, _ := mint(t, e, "c", "control", time.Hour)
	admin, _ := mint(t, e, "a", "admin", time.Hour)
	type call struct{ method, path, body string }
	reads := []call{{"GET", "/api/version", ""}, {"GET", "/api/about", ""}, {"GET", "/api/session", ""}, {"GET", "/api/openapi.json", ""}, {"GET", "/api/state", ""}, {"GET", "/api/history", ""}, {"GET", "/api/sensors", ""}, {"GET", "/api/profiles", ""}, {"GET", "/api/presets", ""}, {"GET", "/api/presets/quiet", ""}, {"GET", "/api/alerts", ""}, {"GET", "/api/dashboard", ""}, {"GET", "/api/tls", ""}}
	controls := []call{{"PUT", "/api/override/cpu", `{"duty":100}`}, {"DELETE", "/api/override/cpu", ""}, {"POST", "/api/presets/quiet/apply", ""}, {"PUT", "/api/dashboard", `{"sensors":[]}`}}
	admins := []call{{"GET", "/api/config", ""}, {"PUT", "/api/config", sampleTOML}, {"GET", "/api/config/export", ""}, {"GET", "/api/log", ""}, {"GET", "/api/log/export", ""}, {"GET", "/api/tls/cert.crt", ""}, {"GET", "/api/tls/cert.cer", ""}, {"PUT", "/api/alerts", `{"transport":"log"}`}, {"POST", "/api/alerts/test", ""}, {"PUT", "/api/presets/mine", ""}, {"DELETE", "/api/presets/quiet", ""}}
	sessions := []call{{"GET", "/api/tokens", ""}, {"POST", "/api/tokens", `{"name":"x"}`}, {"DELETE", "/api/tokens/00000000", ""}, {"GET", "/api/account", ""}, {"POST", "/api/account/password", `{"current_password":"pw","new_password":"12345678"}`}, {"POST", "/api/account/user", `{"current_password":"pw","user":"x"}`}, {"POST", "/api/account/sessions/revoke", `{"others":true}`}, {"POST", "/api/login", `{"user":"admin","password":"pw"}`}, {"POST", "/api/logout", ""}}
	// a 403 with scope and required is the only refusal the guard makes
	forbidden := func(t *testing.T, c call, secret, scope, required string) {
		t.Helper()
		r := e.do(t, c.method, c.path, c.body, bearer(secret))
		wantCode(t, r, 403)
		var m map[string]any
		decode(t, r.body, &m)
		if m["scope"] != scope || m["required"] != required || !strings.Contains(m["error"].(string), "token scope "+scope+" does not allow "+c.method+" "+c.path) {
			t.Errorf("%s %s with %s: %s", c.method, c.path, scope, r.body)
		}
	}
	allowed := func(t *testing.T, c call, secret string) {
		t.Helper()
		if r := e.do(t, c.method, c.path, c.body, bearer(secret)); r.code == 401 || r.code == 403 {
			t.Errorf("%s %s refused: %d %s", c.method, c.path, r.code, r.body)
		}
	}
	for _, c := range reads {
		allowed(t, c, read)
		allowed(t, c, control)
		allowed(t, c, admin)
	}
	for _, c := range controls {
		forbidden(t, c, read, "read", "control")
		allowed(t, c, control)
		allowed(t, c, admin)
	}
	for _, c := range admins {
		forbidden(t, c, read, "read", "admin")
		forbidden(t, c, control, "control", "admin")
		allowed(t, c, admin)
	}
	for _, c := range sessions {
		forbidden(t, c, read, "read", "session")
		forbidden(t, c, admin, "admin", "session")
	}
	// an unknown path passes the scope check and meets the fallback
	wantError(t, e.do(t, "GET", "/api/nope", "", bearer(read)), 404, "unknown endpoint")
	wantError(t, e.do(t, "POST", "/api/state", "", bearer(read)), 405, "method not allowed")
}

// TestCSRFForCookieAndBasicNotBearer: the header stays required for a
// cookie or Basic caller on a write; a bearer caller needs none.
func TestCSRFForCookieAndBasicNotBearer(t *testing.T) {
	e, _ := tokenEnv(t)
	secret, _ := mint(t, e, "c", "control", time.Hour)
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":100}`, bearer(secret)), 200)
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":100}`, map[string]string{"Authorization": basicAuth("admin", "pw")["Authorization"]}), 403, CSRFHeader)
	hdr, _, _ := e.login(t, "admin", "pw", false)
	delete(hdr, CSRFHeader)
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":100}`, hdr), 403, CSRFHeader)
	hdr[CSRFHeader] = "1"
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":100}`, hdr), 200)
}

// TestAuthNoneIgnoresBearer: with auth = none a Bearer header is not
// looked up; everyone is signed in via "none".
func TestAuthNoneIgnoresBearer(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "GET", "/api/session", "", bearer("n5t_whatever"))
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"via":"none"`) {
		t.Errorf("session = %s", r.body)
	}
	wantCode(t, e.do(t, "GET", "/api/config", "", bearer("n5t_whatever")), 200)
	// the CSRF header stays required (the caller is not a bearer)
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, bearer("n5t_whatever")), 403, CSRFHeader)
	if strings.Contains(e.logLines(), "rejected") {
		t.Errorf("bearer looked up with auth none: %s", e.logLines())
	}
	// a token cannot be minted with auth none (anyone reaching the
	// listener could, and the guard would ignore it anyway); list and
	// revoke stay available for the clean-up before switching to basic
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"x"}`, csrf), 409, "auth is none")
	wantCode(t, e.do(t, "GET", "/api/tokens", "", nil), 200)
	wantCode(t, e.do(t, "DELETE", "/api/tokens/0123abcd", "", csrf), 404)
}

func TestBearerRateLimit(t *testing.T) {
	e, _ := tokenEnv(t)
	secret, _ := mint(t, e, "busy", "read", time.Hour)
	other, _ := mint(t, e, "calm", "read", time.Hour)
	now := time.Now()
	e.srv.tokenLimit.now = func() time.Time { return now }
	for i := 0; i < tokenRateBurst; i++ {
		wantCode(t, e.do(t, "GET", "/api/version", "", bearer(secret)), 200)
	}
	r := e.do(t, "GET", "/api/version", "", bearer(secret))
	wantError(t, r, 429, "token rate limit")
	if r.body != "{\"error\":\"token rate limit\"}\n" {
		t.Errorf("body %q", r.body)
	}
	// another token, a Basic caller and a cookie caller are not limited
	wantCode(t, e.do(t, "GET", "/api/version", "", bearer(other)), 200)
	wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "pw")), 200)
	// after a second the token may send tokenRatePerSec requests again
	now = now.Add(time.Second)
	for i := 0; i < tokenRatePerSec; i++ {
		wantCode(t, e.do(t, "GET", "/api/version", "", bearer(secret)), 200)
	}
	wantError(t, e.do(t, "GET", "/api/version", "", bearer(secret)), 429, "token rate limit")
}

// ---- endpoints -------------------------------------------------------------

func TestTokenEndpoints(t *testing.T) {
	e, file := tokenEnv(t)
	hdr, _, _ := e.login(t, "admin", "pw", false)
	// empty list
	r := e.do(t, "GET", "/api/tokens", "", hdr)
	wantCode(t, r, 200)
	if r.body != "{\"tokens\":[]}\n" {
		t.Errorf("empty list: %q", r.body)
	}
	// create with defaults
	r = e.do(t, "POST", "/api/tokens", `{"name":"Home Assistant"}`, hdr)
	wantCode(t, r, 201)
	var created map[string]any
	decode(t, r.body, &created)
	secret, _ := created["token"].(string)
	id, _ := created["id"].(string)
	if !strings.HasPrefix(secret, "n5t_") || len(id) != 8 || created["scope"] != "read" || created["name"] != "Home Assistant" || created["ok"] != true || created["expires"] == nil {
		t.Errorf("created = %v", created)
	}
	if _, has := created["warning"]; has {
		t.Errorf("warning on a token that expires: %v", created)
	}
	exp, _ := time.Parse(time.RFC3339, created["expires"].(string))
	if d := time.Until(exp); d < TokenTTLDefault*24*time.Hour-time.Minute || d > TokenTTLDefault*24*time.Hour {
		t.Errorf("default expiry %s", d)
	}
	// the secret works and the list shows it (without the secret)
	wantCode(t, e.do(t, "GET", "/api/state", "", bearer(secret)), 200)
	r = e.do(t, "GET", "/api/tokens", "", hdr)
	var list struct{ Tokens []tokenJSON }
	decode(t, r.body, &list)
	if len(list.Tokens) != 1 || list.Tokens[0].ID != id || list.Tokens[0].Name != "Home Assistant" || list.Tokens[0].Scope != "read" || list.Tokens[0].Expired || list.Tokens[0].LastUsed == nil || list.Tokens[0].LastIP == "" || list.Tokens[0].Expires == nil {
		t.Errorf("list = %s", r.body)
	}
	if strings.Contains(r.body, secret) {
		t.Errorf("secret listed")
	}
	// ttl 0: never, with a warning; expires null in the list
	r = e.do(t, "POST", "/api/tokens", `{"name":"forever","scope":"admin","ttl_days":0}`, hdr)
	wantCode(t, r, 201)
	if !strings.Contains(r.body, `"warning":"token never expires"`) || !strings.Contains(r.body, `"expires":null`) || !strings.Contains(r.body, `"scope":"admin"`) {
		t.Errorf("never: %s", r.body)
	}
	r = e.do(t, "GET", "/api/tokens", "", hdr)
	if !strings.Contains(r.body, `"expires":null`) || !strings.Contains(r.body, `"last_used":null`) {
		t.Errorf("list with never: %s", r.body)
	}
	// rules: 400
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":""}`, hdr), 400, "token name")
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"-bad"}`, hdr), 400, "token name")
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"x","scope":"root"}`, hdr), 400, "scope")
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"x","ttl_days":3651}`, hdr), 400, "ttl_days")
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"x","ttl_days":-1}`, hdr), 400, "ttl_days")
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"x","bogus":1}`, hdr), 400, "invalid JSON")
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"`+strings.Repeat("a", 5000)+`"}`, hdr), 413, "body exceeds")
	// name taken: 409 (case-insensitive)
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"home assistant"}`, hdr), 409, "already in use")
	// cap: 409
	for i := 0; i < tokenMax-2; i++ {
		wantCode(t, e.do(t, "POST", "/api/tokens", fmt.Sprintf(`{"name":"bulk %d"}`, i), hdr), 201)
	}
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"too many"}`, hdr), 409, "at most 50")
	// revoke: 200, then 404; bad id 400
	r = e.do(t, "DELETE", "/api/tokens/"+id, "", hdr)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"revoked":"`+id+`"`) {
		t.Errorf("revoke = %s", r.body)
	}
	wantError(t, e.do(t, "DELETE", "/api/tokens/"+id, "", hdr), 404, "unknown token")
	wantError(t, e.do(t, "DELETE", "/api/tokens/nothex00", "", hdr), 400, "8 hex")
	wantError(t, e.do(t, "DELETE", "/api/tokens/abc", "", hdr), 400, "8 hex")
	wantError(t, e.do(t, "GET", "/api/state", "", bearer(secret)), 401, "token unknown")
	// the file mirrors the store
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || !strings.Contains(string(raw), `"name": "forever"`) || strings.Contains(string(raw), `"name": "Home Assistant"`) {
		t.Errorf("file: %s", raw)
	}
	// session only: a token cannot list, mint or revoke; Basic can
	admin, _ := mint(t, e, "admin token", "admin", time.Hour)
	wantCode(t, e.do(t, "GET", "/api/tokens", "", bearer(admin)), 403)
	wantCode(t, e.do(t, "POST", "/api/tokens", `{"name":"leak"}`, bearer(admin)), 403)
	wantCode(t, e.do(t, "DELETE", "/api/tokens/"+id, "", bearer(admin)), 403)
	wantCode(t, e.do(t, "GET", "/api/tokens", "", basicAuth("admin", "pw")), 200)
	// the CSRF header is required for cookie and Basic callers on the writes
	nocsrf := map[string]string{"Cookie": hdr["Cookie"]}
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"x"}`, nocsrf), 403, CSRFHeader)
	wantError(t, e.do(t, "DELETE", "/api/tokens/"+id, "", nocsrf), 403, CSRFHeader)
	// anonymous: 401
	wantError(t, e.do(t, "GET", "/api/tokens", "", nil), 401, "authentication")
	wantError(t, e.do(t, "POST", "/api/tokens", `{"name":"x"}`, csrf), 401, "authentication")
	// log lines name the token, never the secret
	logs := e.logLines()
	if !strings.Contains(logs, `api token "Home Assistant" (`+id+`, scope read`) || !strings.Contains(logs, "api token "+id+" revoked") || strings.Contains(logs, secret) {
		t.Errorf("log: %s", logs)
	}
}

// TestTokensViaSocket: the socket caller (root CLI) manages tokens.
func TestTokensViaSocket(t *testing.T) {
	e, _ := tokenEnv(t)
	sock := httptest.NewServer(e.srv.SocketHandler())
	defer sock.Close()
	do := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, sock.URL+path, strings.NewReader(body))
		res, err := sock.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := do("POST", "/api/tokens", `{"name":"cli","scope":"control","ttl_days":30}`)
	if code != 201 || !strings.Contains(body, `"token":"n5t_`) {
		t.Fatalf("create via socket: %d %s", code, body)
	}
	var created map[string]any
	decode(t, body, &created)
	if code, body := do("GET", "/api/tokens", ""); code != 200 || !strings.Contains(body, `"name":"cli"`) {
		t.Errorf("list via socket: %d %s", code, body)
	}
	// the minted token works on TCP
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":100}`, bearer(created["token"].(string))), 200)
	if code, body := do("DELETE", "/api/tokens/"+created["id"].(string), ""); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Errorf("revoke via socket: %d %s", code, body)
	}
	if code, _ := do("DELETE", "/api/tokens/"+created["id"].(string), ""); code != 404 {
		t.Errorf("revoke again: %d", code)
	}
	if code, _ := do("POST", "/api/tokens", `{"name":"x","scope":"nope"}`); code != 400 {
		t.Errorf("bad scope via socket: %d", code)
	}
	// the socket caller's log line says so
	if !strings.Contains(e.logLines(), "via socket") {
		t.Errorf("log: %s", e.logLines())
	}
}

// TestTokensSurviveCredentialChange: a password change and "sign out
// other sessions" leave the tokens valid; only Revoke ends them.
func TestTokensSurviveCredentialChange(t *testing.T) {
	e, file := tokenEnv(t)
	secret, _ := mint(t, e, "keep", "read", time.Hour)
	hdr, _, _ := e.login(t, "admin", "pw", false)
	wantCode(t, e.do(t, "POST", "/api/account/password", `{"current_password":"pw","new_password":"new-secret-1"}`, hdr), 200)
	wantCode(t, e.do(t, "POST", "/api/account/sessions/revoke", `{"others":true}`, hdr), 200)
	wantCode(t, e.do(t, "GET", "/api/sensors", "", bearer(secret)), 200)
	// a restart under the new credentials still loads the file
	auth := e.srv.authCfg()
	e.withDeps(t, auth, func(d *Deps) { d.TokenFile = file })
	wantCode(t, e.do(t, "GET", "/api/sensors", "", bearer(secret)), 200)
}
