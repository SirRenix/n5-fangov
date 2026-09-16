package web

// Regression tests for the second v0.4 review round: strict PUT scoped to
// the channel tables (M1), the concurrency cap per address instead of per
// /64 (M2), IPv6 zones in the limiter key (L3), the serialised legacy hash
// upgrade (L4), the inline-table placeholder (L6) and the 500 for a preset
// that was written but not reloaded (L7).

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// realValidate is Deps.Validate as cmd wires it: the config parser's
// warnings as "<field>: <msg>".
func realValidate(raw []byte) ([]string, error) {
	_, warns, err := config.Parse(raw)
	out := make([]string, 0, len(warns))
	for _, w := range warns {
		out = append(out, w.String())
	}
	return out, err
}

// TestPutConfigStrictChannelOnly (M1): under ?strict=1 only warnings on
// the [[channel]] tables refuse the PUT. A pre-existing unknown key in
// [web] is not the editor's doing: written, 200, reported as a warning.
// A falling-duty curve is refused with 400 and the channel warning under
// "errors"; the unrelated warning rides along under "warnings".
func TestPutConfigStrictChannelOnly(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.validate = realValidate
	withWeb := strings.Replace(sampleTOML, "[web]\n", "[web]\nunknown = 1\n", 1)
	r := e.do(t, "PUT", "/api/config?strict=1", withWeb, csrf)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"warnings":["web.unknown: unknown key, ignored"]`) || len(e.cfg.saved) != 1 {
		t.Fatalf("strict with a [web] warning: %s (saved %d)", r.body, len(e.cfg.saved))
	}
	falling := strings.Replace(withWeb, "curve = [[45,85],[80,255]]", "curve = [[45,200],[80,100]]", 1)
	r = e.do(t, "PUT", "/api/config?strict=1", falling, csrf)
	wantError(t, r, 400, "config rejected")
	var m struct {
		Errors   []string `json:"errors"`
		Warnings []string `json:"warnings"`
	}
	decode(t, r.body, &m)
	if len(m.Errors) != 1 || !strings.HasPrefix(m.Errors[0], "channel.cpu.curve: ") || strings.Join(m.Warnings, "|") != "web.unknown: unknown key, ignored" {
		t.Fatalf("strict with a curve warning: %s", r.body)
	}
	if len(e.cfg.saved) != 1 {
		t.Fatalf("refused PUT saved (%d)", len(e.cfg.saved))
	}
	// a nameless table ("channel[1].name") and a broken array ("channel:")
	// count as channel warnings; a section merely starting with the word
	// does not
	ch, rest := splitChannelWarnings([]string{"channel[1].name: missing, channel dropped", "channel: not an array of tables", "channels: unknown section, ignored", "channel_x.y: z", "alert.mail_to: x"})
	if strings.Join(ch, "|") != "channel[1].name: missing, channel dropped|channel: not an array of tables" || strings.Join(rest, "|") != "channels: unknown section, ignored|channel_x.y: z|alert.mail_to: x" {
		t.Fatalf("split = %v / %v", ch, rest)
	}
	// without strict the falling curve is written with the warning
	r = e.do(t, "PUT", "/api/config", falling, csrf)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"channel.cpu.curve: point 1 duty 100 below previous 200`) || strings.Contains(r.body, `"errors"`) || len(e.cfg.saved) != 2 {
		t.Fatalf("lenient: %s (saved %d)", r.body, len(e.cfg.saved))
	}
}

// TestAuthBusyPerAddress (M2): one host of a /64 with limitConcurrent
// failed attempts parked in the delay does not make busy() refuse its
// neighbours — a correct password from another address of the same /64
// is served, while the host itself gets 429. The delay counter stays
// shared: the neighbour's own failure is already delayed.
func TestAuthBusyPerAddress(t *testing.T) {
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: LegacyPasswordHash("admin", "pw")})
	const bad, good = "[2001:db8:7::bad]:4000", "[2001:db8:7::600d]:4001"
	if limitKey("2001:db8:7::bad") != limitKey("2001:db8:7::600d") {
		t.Fatal("test addresses must share a /64")
	}
	release := make(chan struct{})
	var parked sync.WaitGroup
	var slept []time.Duration
	var sleptMu sync.Mutex
	e.srv.limiter.sleep = func(d time.Duration) {
		sleptMu.Lock()
		slept = append(slept, d)
		sleptMu.Unlock()
		parked.Done()
		<-release
	}
	for i := 0; i < limitFree; i++ {
		if rec := serveAs(e, bad, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong")); rec.Code != 401 {
			t.Fatalf("free attempt %d: %d", i, rec.Code)
		}
	}
	parked.Add(limitConcurrent)
	var done sync.WaitGroup
	for i := 0; i < limitConcurrent; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			serveAs(e, bad, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong"))
		}()
	}
	if !waitTimeout(&parked, 10*time.Second) {
		close(release)
		t.Fatal("the parked attempts never reached the limiter sleep")
	}
	if !e.srv.limiter.busy("2001:db8:7::bad") || e.srv.limiter.busy("2001:db8:7::600d") {
		t.Fatalf("busy: bad=%v good=%v", e.srv.limiter.busy("2001:db8:7::bad"), e.srv.limiter.busy("2001:db8:7::600d"))
	}
	if rec := serveAs(e, bad, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")); rec.Code != 429 {
		t.Fatalf("saturated host with the right password: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveAs(e, good, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")); rec.Code != 200 {
		t.Fatalf("neighbour with the right password: %d %s", rec.Code, rec.Body.String())
	}
	if len(e.svc.overrides) != 1 {
		t.Fatalf("overrides = %d", len(e.svc.overrides))
	}
	// the neighbour's success reset the shared counter; the parked
	// sleepers of the host are still counted against the host only
	e.srv.limiter.mu.Lock()
	_, tracked := e.srv.limiter.byIP[limitKey("2001:db8:7::bad")]
	sl := e.srv.limiter.sleeping[addrKey("2001:db8:7::bad")]
	e.srv.limiter.mu.Unlock()
	if tracked || sl == nil || sl.n != limitConcurrent {
		t.Fatalf("after the neighbour's success: tracked=%v sleepers=%+v", tracked, sl)
	}
	close(release)
	done.Wait()
	if e.srv.limiter.busy("2001:db8:7::bad") {
		t.Fatal("host still busy after the sleepers returned")
	}
	e.srv.limiter.mu.Lock()
	n := len(e.srv.limiter.sleeping)
	e.srv.limiter.mu.Unlock()
	if n != 0 {
		t.Fatalf("idle sleeper entries kept: %d", n)
	}
	sleptMu.Lock()
	defer sleptMu.Unlock()
	if len(slept) != limitConcurrent {
		t.Fatalf("sleeps = %v", slept)
	}
}

// TestLimitKeyZone (L3): a link-local address with a zone falls into its
// /64 like any other; the address key drops the zone too.
func TestLimitKeyZone(t *testing.T) {
	if k := limitKey("fe80::1%vmbr0"); k != "fe80::/64" || k != limitKey("fe80::2%eth0") {
		t.Errorf("zoned key = %q", k)
	}
	if k := addrKey("fe80::1%vmbr0"); k != "fe80::1" {
		t.Errorf("zoned address key = %q", k)
	}
	if k := addrKey("::ffff:192.0.2.10"); k != "192.0.2.10" {
		t.Errorf("mapped address key = %q", k)
	}
	if addrKey("not-an-ip") != "not-an-ip" || limitKey("fe80::1%") != "fe80::1%" {
		t.Errorf("passthrough: %q %q", addrKey("not-an-ip"), limitKey("fe80::1%"))
	}
	l := newAuthLimiter()
	l.sleep = func(time.Duration) {}
	for i := 1; i <= limitFree; i++ {
		l.fail(fmt.Sprintf("fe80::%x%%vmbr0", i))
	}
	if n, d := l.fail("fe80::abcd%eth1"); n != limitFree+1 || d != limitBase {
		t.Errorf("zoned addresses not in one bucket: n=%d delay=%s", n, d)
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

// TestPutConfigInlinePlaceholderRefused (L6, API side): a config text that
// carries the <unchanged> placeholder in an inline table — where
// RestoreHash does not reach — is refused instead of written with the
// placeholder as the stored hash.
func TestPutConfigInlinePlaceholderRefused(t *testing.T) {
	hash := PasswordHash("admin", "pw")
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: hash})
	e.cfg.raw = []byte("[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"" + hash + "\"\n")
	body := "web = { auth = \"basic\", user = \"admin\", password_hash = \"" + RedactedHash + "\" }\n"
	wantError(t, e.do(t, "PUT", "/api/config", body, basicAuth("admin", "pw")), 400, "not restored")
	if len(e.cfg.saved) != 0 {
		t.Fatalf("placeholder written: %s", e.cfg.saved[0])
	}
	// the line form still restores and writes
	wantCode(t, e.do(t, "PUT", "/api/config", "[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \""+RedactedHash+"\"\n", basicAuth("admin", "pw")), 200)
	if len(e.cfg.saved) != 1 || !strings.Contains(string(e.cfg.saved[0]), hash) {
		t.Fatalf("line form: saved %d", len(e.cfg.saved))
	}
}

// TestApplyPresetReloadFailed500 (L7): a preset that was written but not
// taken by the daemon answers 500 with the store's text; a refusal stays
// 400 and a write failure 500 as before.
func TestApplyPresetReloadFailed500(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.presets.applyErr = fmt.Errorf("preset quiet written, %w", &ReloadError{Err: errors.New("channel hdd: pwm3 not writable")})
	r := e.do(t, "POST", "/api/presets/quiet/apply", "", csrf)
	wantError(t, r, 500, "apply preset: preset quiet written, reload failed: channel hdd: pwm3 not writable")
	e.presets.applyErr = errors.New(`preset "quiet" contains no usable [[channel]] table`)
	wantCode(t, e.do(t, "POST", "/api/presets/quiet/apply", "", csrf), 400)
	e.presets.applyErr = fmt.Errorf("current config: %w", ErrStore)
	wantCode(t, e.do(t, "POST", "/api/presets/quiet/apply", "", csrf), 500)
	if !isReloadError(fmt.Errorf("x: %w", &ReloadError{Err: errors.New("y")})) || isReloadError(errors.New("reload failed: y")) {
		t.Error("isReloadError classification")
	}
}
