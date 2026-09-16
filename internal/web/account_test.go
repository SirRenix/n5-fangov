package web

import (
	"errors"
	"fmt"
	"path/filepath"
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

// ---- account ----------------------------------------------------------------

// TestAccountPassword: wrong current → 403 + limiter count; ok → the new
// password works, the old one does not, other sessions are revoked, the
// caller's cookie survives.
func TestAccountPassword(t *testing.T) {
	e, acc, _, _ := storesEnv(t, adminBasic)
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
	e, acc, _, _ := storesEnv(t, legacy)
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
	// updates[0] is the transparent legacy→PBKDF2 upgrade of the first login
	// (TestLegacyHashUpgraded); the rename is the last one.
	last := acc.updates[len(acc.updates)-1]
	if len(acc.updates) != 2 || last[0] != "root.ops-1" || !strings.HasPrefix(last[1], "pbkdf2$") || !VerifyPassword("root.ops-1", "pw", last[1]) {
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
	e, _, _, _ := storesEnv(t, adminBasic)
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
	e, _, _, _ := storesEnv(t, AuthConfig{})
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

// TestLegacyHashUpgraded: a successful basic request or login against a
// legacy sha256 hash rewrites the stored hash as PBKDF2 through the
// account store; the epoch follows, sessions stay, and the file is not
// rewritten again on the next request. Without an account store the
// legacy hash simply stays.
func TestLegacyHashUpgraded(t *testing.T) {
	legacy := AuthConfig{Mode: "basic", User: "admin", PasswordHash: LegacyPasswordHash("admin", "pw")}
	e, acc, _, _ := storesEnv(t, legacy)
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
	e2, acc2, _, _ := storesEnv(t, legacy)
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
	e4, acc4, _, _ := storesEnv(t, legacy)
	acc4.err = fmt.Errorf("read-only file system")
	wantCode(t, e4.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")), 200)
	if !strings.Contains(e4.logLines(), "legacy password hash not upgraded") {
		t.Fatalf("log = %q", e4.logLines())
	}
}

// gatedAccount blocks the first Update until released so a second
// verification can arrive while the first one is mid-rewrite.
type gatedAccount struct {
	*fakeAccount
	entered chan struct{}
	release chan struct{}
}

func (g *gatedAccount) Update(user, hash string) (AuthConfig, error) {
	select {
	case g.entered <- struct{}{}:
		<-g.release
	default:
	}
	return g.fakeAccount.Update(user, hash)
}

// TestLegacyHashUpgradeSerialised (L4): two successful verifications
// against a legacy hash at the same time rewrite the file once — the
// second one waits and finds the PBKDF2 hash in effect.
func TestLegacyHashUpgradeSerialised(t *testing.T) {
	legacy := AuthConfig{Mode: "basic", User: "admin", PasswordHash: LegacyPasswordHash("admin", "pw")}
	e := newEnv(t, legacy)
	acc := &gatedAccount{fakeAccount: &fakeAccount{cfg: legacy}, entered: make(chan struct{}), release: make(chan struct{})}
	e.withDeps(t, legacy, func(d *Deps) { d.Account = acc; d.SessionFile = "" })
	var done sync.WaitGroup
	done.Add(2)
	go func() { defer done.Done(); e.srv.upgradeLegacyHash("admin", "pw") }()
	select {
	case <-acc.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first upgrade never reached the store")
	}
	go func() { defer done.Done(); e.srv.upgradeLegacyHash("admin", "pw") }()
	// the second call either blocks on the mutex or arrives after the
	// swap; both end without a second Update
	time.Sleep(20 * time.Millisecond)
	close(acc.release)
	done.Wait()
	acc.mu.Lock()
	n := len(acc.updates)
	acc.mu.Unlock()
	if n != 1 {
		t.Fatalf("Account.Update called %d times", n)
	}
	if cur := e.srv.authCfg(); !strings.HasPrefix(cur.PasswordHash, "pbkdf2$") || !VerifyPassword("admin", "pw", cur.PasswordHash) {
		t.Fatalf("credentials in effect = %+v", cur)
	}
	if c := strings.Count(e.logLines(), "legacy password hash upgraded"); c != 1 {
		t.Fatalf("upgrade logged %d times: %q", c, e.logLines())
	}
}
