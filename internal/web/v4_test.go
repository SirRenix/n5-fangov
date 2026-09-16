package web

// Regression tests for the v0.4 audit fixes in this package (docs/AUDIT.md,
// sections 1 and 3): limiter buckets and the global verification bound,
// legacy hash upgrade, Secure cookie behind a proxy, handshake map cap,
// public /api/about without the toolchain, truncated user names in logs.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLimitKey: IPv4 addresses are their own bucket, IPv6 addresses share
// their /64, unparsable input passes through.
func TestLimitKey(t *testing.T) {
	if limitKey("192.0.2.10") != "192.0.2.10" || limitKey("192.0.2.10") == limitKey("192.0.2.11") {
		t.Errorf("ipv4 keys: %q %q", limitKey("192.0.2.10"), limitKey("192.0.2.11"))
	}
	if limitKey("::ffff:192.0.2.10") != "192.0.2.10" {
		t.Errorf("ipv4-mapped key = %q", limitKey("::ffff:192.0.2.10"))
	}
	a, b, c := limitKey("2001:db8:1:2::1"), limitKey("2001:db8:1:2:ffff:ffff:ffff:ffff"), limitKey("2001:db8:1:3::1")
	if a != b || a == c || a != "2001:db8:1:2::/64" {
		t.Errorf("ipv6 keys: %q %q %q", a, b, c)
	}
	if limitKey("not-an-ip") != "not-an-ip" || limitKey("") != "" {
		t.Errorf("passthrough: %q", limitKey("not-an-ip"))
	}
}

// TestAuthLimiterIPv6Rotation: rotating through addresses of one /64 does
// not hand out limitFree free attempts per address — the sixth attempt
// from the sixth address is already delayed.
func TestAuthLimiterIPv6Rotation(t *testing.T) {
	l := newAuthLimiter()
	var slept []time.Duration
	l.sleep = func(d time.Duration) { slept = append(slept, d) }
	for i := 1; i <= limitFree; i++ {
		n, d := l.fail(fmt.Sprintf("2001:db8:5::%x", i))
		if n != i || d != 0 {
			t.Fatalf("attempt %d from a new address: n=%d delay=%s", i, n, d)
		}
	}
	n, d := l.fail("2001:db8:5:0:dead:beef:1:2")
	if n != limitFree+1 || d != limitBase {
		t.Fatalf("attempt %d: n=%d delay=%s, want %d/%s (one bucket for the /64)", limitFree+1, n, d, limitFree+1, limitBase)
	}
	if len(l.byIP) != 1 {
		t.Fatalf("buckets = %d, want 1", len(l.byIP))
	}
	// another /64 starts fresh; busy/reset address the bucket
	if n, d := l.fail("2001:db8:6::1"); n != 1 || d != 0 {
		t.Fatalf("other /64: n=%d delay=%s", n, d)
	}
	l.reset("2001:db8:5::77")
	if n, _ := l.fail("2001:db8:5::1"); n != 1 {
		t.Fatalf("reset by another address of the /64 did not clear the bucket: n=%d", n)
	}
	if len(slept) != 1 {
		t.Fatalf("sleeps = %v", slept)
	}
}

// TestAuthLimiterTableFullLogged: evicting a live entry from a full table
// is logged, once per limitFullLogEvery.
func TestAuthLimiterTableFullLogged(t *testing.T) {
	l := newAuthLimiter()
	l.sleep = func(time.Duration) {}
	now := time.Unix(1789500000, 0)
	l.now = func() time.Time { return now }
	var logged []string
	l.logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	for i := 0; i < limitEntries+10; i++ {
		l.fail(fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256))
	}
	if len(l.byIP) > limitEntries {
		t.Fatalf("table grew to %d", len(l.byIP))
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "auth limiter table full") {
		t.Fatalf("log = %v, want one 'table full' line", logged)
	}
	now = now.Add(limitFullLogEvery + time.Second)
	// every entry is expired now: pruning drops them all, no eviction, no line
	l.fail("198.51.100.99")
	if len(logged) != 1 || len(l.byIP) != 1 {
		t.Fatalf("after expiry: log=%d buckets=%d", len(logged), len(l.byIP))
	}
}

// serveAs runs one request through the TCP handler with a chosen remote
// address (the httptest server always connects from 127.0.0.1).
func serveAs(e *env, remote, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:8010"+path, strings.NewReader(body))
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestAuthHashSlotsGlobal: with every verification slot taken, a correct
// credential from any address answers 429 without a hash computation, a
// log line or a failure count; released slots serve again.
func TestAuthHashSlotsGlobal(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	for i := 0; i < limitHashConcurrent; i++ {
		if !e.srv.limiter.acquire() {
			t.Fatalf("slot %d not acquired", i)
		}
	}
	if e.srv.limiter.acquire() {
		t.Fatal("slot beyond limitHashConcurrent acquired")
	}
	remotes := []string{"192.0.2.1:4000", "198.51.100.7:4001", "[2001:db8::1]:4002", "[2001:db8:1::1]:4003"}
	for _, rem := range remotes {
		if rec := serveAs(e, rem, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")); rec.Code != 429 || !strings.Contains(rec.Body.String(), "too many concurrent") {
			t.Errorf("basic from %s: %d %s", rem, rec.Code, rec.Body.String())
		}
		if rec := serveAs(e, rem, "POST", "/api/login", `{"user":"admin","password":"pw"}`, csrf); rec.Code != 429 {
			t.Errorf("login from %s: %d %s", rem, rec.Code, rec.Body.String())
		}
	}
	if len(e.svc.overrides) != 0 {
		t.Fatal("override applied while the verification slots were full")
	}
	if l := e.logLines(); strings.Contains(l, "failure") {
		t.Fatalf("refused attempts logged as failures: %q", l)
	}
	e.srv.limiter.mu.Lock()
	n := len(e.srv.limiter.byIP)
	e.srv.limiter.mu.Unlock()
	if n != 0 {
		t.Fatalf("refused attempts counted: %d buckets", n)
	}
	// anonymous public reads are unaffected
	if rec := serveAs(e, remotes[0], "GET", "/api/state", "", nil); rec.Code != 200 {
		t.Fatalf("anonymous state = %d", rec.Code)
	}
	for i := 0; i < limitHashConcurrent; i++ {
		e.srv.limiter.release()
	}
	if rec := serveAs(e, remotes[2], "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")); rec.Code != 200 {
		t.Fatalf("after release: %d %s", rec.Code, rec.Body.String())
	}
	// the slot is returned after the check: the wrong password is a
	// counted failure (the sleep is patched away), not a 429
	e.srv.limiter.sleep = func(time.Duration) {}
	for i := 0; i < limitFree+2; i++ {
		if rec := serveAs(e, remotes[1], "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong")); rec.Code != 401 {
			t.Fatalf("failure %d = %d", i, rec.Code)
		}
	}
	if len(e.srv.limiter.hashSem) != 0 {
		t.Fatalf("slots leaked: %d in use", len(e.srv.limiter.hashSem))
	}
}

// TestLegacyHashUpgraded: a successful basic request or login against a
// legacy sha256 hash rewrites the stored hash as PBKDF2 through the
// account store; the epoch follows, sessions stay, and the file is not
// rewritten again on the next request. Without an account store the
// legacy hash simply stays.
func TestLegacyHashUpgraded(t *testing.T) {
	legacy := AuthConfig{Mode: "basic", User: "admin", PasswordHash: LegacyPasswordHash("admin", "pw")}
	e, acc, _, _ := v3Env(t, legacy)
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")), 200)
	if len(acc.updates) != 1 || acc.updates[0][0] != "" || !strings.HasPrefix(acc.updates[0][1], "pbkdf2$") {
		t.Fatalf("updates = %v", acc.updates)
	}
	if !strings.Contains(e.logLines(), "legacy password hash upgraded") {
		t.Fatalf("log = %q", e.logLines())
	}
	cur := e.srv.authCfg()
	if !strings.HasPrefix(cur.PasswordHash, "pbkdf2$") || cur.User != "admin" || !VerifyPassword("admin", "pw", cur.PasswordHash) {
		t.Fatalf("credentials in effect = %+v", cur)
	}
	// second request: no further rewrite, the new hash verifies
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":11}`, basicAuth("admin", "pw")), 200)
	if len(acc.updates) != 1 {
		t.Fatalf("hash rewritten again: %v", acc.updates)
	}
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":11}`, basicAuth("admin", "PW")), 401)

	// login path, with an existing cookie session that must survive
	e2, acc2, _, _ := v3Env(t, legacy)
	hdr, _, _ := e2.login(t, "admin", "pw", true)
	if hdr == nil {
		t.Fatal("login failed")
	}
	if len(acc2.updates) != 1 {
		t.Fatalf("login did not upgrade: %v", acc2.updates)
	}
	wantCode(t, e2.do(t, "GET", "/api/account", "", hdr), 200)
	r := e2.do(t, "GET", "/api/session", "", hdr)
	if !strings.Contains(r.body, `"authenticated":true`) {
		t.Fatalf("session after upgrade = %s", r.body)
	}

	// no account store: nothing to write to, the legacy hash stays accepted
	e3 := newEnv(t, legacy)
	wantCode(t, e3.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")), 200)
	if e3.srv.authCfg().PasswordHash != legacy.PasswordHash || strings.Contains(e3.logLines(), "upgraded") {
		t.Fatalf("without a store: %+v %q", e3.srv.authCfg(), e3.logLines())
	}

	// a failing store is logged, the login still succeeds
	e4, acc4, _, _ := v3Env(t, legacy)
	acc4.err = fmt.Errorf("read-only file system")
	wantCode(t, e4.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")), 200)
	if !strings.Contains(e4.logLines(), "legacy password hash not upgraded") {
		t.Fatalf("log = %q", e4.logLines())
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
	e, _, _, _ := v3Env(t, adminBasic)
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

// TestHandshakeFilterClientCap: the per-client map stops growing at
// handshakeClientsMax; the count and the summary still reflect the flood.
func TestHandshakeFilterClientCap(t *testing.T) {
	now := time.Unix(1789500000, 0)
	var logged []string
	f := newHandshakeFilter(func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }, func() time.Time { return now })
	f.last = now // no summary during the flood
	for i := 0; i < handshakeClientsMax+50; i++ {
		line := fmt.Sprintf("http: TLS handshake error from [2001:db8::%x]:4000: EOF\n", i+1)
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.clients) != handshakeClientsMax || f.count != handshakeClientsMax+50 || !f.overflow {
		t.Fatalf("clients=%d count=%d overflow=%v", len(f.clients), f.count, f.overflow)
	}
	now = now.Add(handshakeSummaryEvery)
	if _, err := f.Write([]byte("http: TLS handshake error from 203.0.113.1:1: EOF\n")); err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], fmt.Sprintf("%d TLS handshakes rejected by %d or more client(s)", handshakeClientsMax+51, handshakeClientsMax)) {
		t.Fatalf("summary = %v", logged)
	}
	if len(f.clients) != 0 || f.overflow {
		t.Fatalf("not reset after the summary: clients=%d overflow=%v", len(f.clients), f.overflow)
	}
}

// TestAboutGoOnlySignedIn: the toolchain version is absent for anonymous
// callers and present for a signed-in one (auth = none counts as signed in).
func TestAboutGoOnlySignedIn(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	var a About
	decode(t, e.do(t, "GET", "/api/about", "", nil).body, &a)
	if a.Go != "" || a.Version != "0.3.0-beta.1" {
		t.Errorf("anonymous about = %+v", a)
	}
	decode(t, e.do(t, "GET", "/api/about", "", basicAuth("admin", "pw")).body, &a)
	if a.Go != runtime.Version() {
		t.Errorf("signed-in about go = %q", a.Go)
	}
	e2 := newEnv(t, AuthConfig{})
	decode(t, e2.do(t, "GET", "/api/about", "", nil).body, &a)
	if a.Go != runtime.Version() {
		t.Errorf("auth=none about go = %q", a.Go)
	}
}

// TestAuthLogUserTruncated: a kilobyte user name in a failed login or
// basic credential reaches the log as 64 characters, not as a flood.
func TestAuthLogUserTruncated(t *testing.T) {
	e, _, _, _ := v3Env(t, adminBasic)
	e.srv.limiter.sleep = func(time.Duration) {}
	long := strings.Repeat("u", 3000)
	wantCode(t, e.do(t, "POST", "/api/login", fmt.Sprintf(`{"user":%q,"password":"x"}`, long), csrf), 401)
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth(long, "x")), 401)
	e.logMu.Lock()
	defer e.logMu.Unlock()
	if len(e.logged) != 2 {
		t.Fatalf("log lines = %d", len(e.logged))
	}
	for _, l := range e.logged {
		if len(l) > 400 || !strings.Contains(l, `user "`+strings.Repeat("u", 64)+`"`) {
			t.Errorf("line not truncated: %d bytes: %.120s", len(l), l)
		}
	}
}
