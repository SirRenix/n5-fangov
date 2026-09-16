package web

import (
	"fmt"
	"strings"
	"sync"
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

// TestAuthHashSlotsGlobal: with every verification slot taken, a correct
// credential from any address answers 429 without a hash computation, a
// log line or a failure count; released slots serve again.
func TestAuthHashSlotsGlobal(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
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

// TestAuthBusyPerAddress: one host of a /64 with limitConcurrent
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

// TestLimitKeyZone: a link-local address with a zone falls into its
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
