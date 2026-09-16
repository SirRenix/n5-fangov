package web

import (
	"net"
	"sync"
	"time"
)

// Failed basic-auth attempts are throttled per source bucket (M4): the
// first limitFree failures answer immediately, then the answer is delayed
// limitBase, doubling up to limitMax. A success or limitReset of quiet
// resets the counter.
//
// The bucket is the IPv4 address or the IPv6 /64 prefix (limitKey): a LAN
// host with router advertisements can pick any address of its /64, so a
// per-address bucket would hand out limitFree free attempts per new
// address. Independent of the buckets, at most limitHashConcurrent
// password checks (PBKDF2, tens of milliseconds of CPU each) run at the
// same time in the whole process; further ones are refused with 429
// before any hash is computed, so many source addresses cannot saturate
// the host the regulation loop shares.
const (
	limitFree    = 5
	limitBase    = 250 * time.Millisecond
	limitMax     = 2 * time.Second
	limitReset   = 10 * time.Minute
	limitEntries = 4096 // upper bound on tracked buckets
	// limitConcurrent is how many failed attempts of one bucket may sleep
	// at the same time; further attempts are refused immediately (429)
	// without a hash computation (M3c).
	limitConcurrent = 4
	// limitHashConcurrent is the process-wide bound on concurrent
	// password verifications (all buckets together).
	limitHashConcurrent = 4
	// limitFullLogEvery bounds the "table full" log line: the table only
	// fills under a flood, and the flood must not become a log flood.
	limitFullLogEvery = limitReset
)

type authFails struct {
	n        int
	last     time.Time
	sleeping int // attempts of this bucket currently inside sleep
}

type authLimiter struct {
	mu    sync.Mutex
	byIP  map[string]*authFails // keyed by limitKey
	now   func() time.Time
	sleep func(time.Duration)
	logf  func(string, ...any)
	// hashSem holds one token per password verification in flight.
	hashSem chan struct{}
	// fullLogged is when "table full" was last logged (zero: never).
	fullLogged time.Time
}

// newAuthLimiter returns a limiter that logs nowhere; New sets logf.
func newAuthLimiter() *authLimiter {
	return &authLimiter{
		byIP:    map[string]*authFails{},
		now:     time.Now,
		sleep:   time.Sleep,
		logf:    func(string, ...any) {},
		hashSem: make(chan struct{}, limitHashConcurrent),
	}
}

// limitKey maps a remote address to its limiter bucket: an IPv4 address
// is its own bucket, an IPv6 address shares the bucket of its /64 prefix
// (rendered as "<prefix>/64"). Anything that does not parse is used as
// given.
func limitKey(ip string) string {
	addr := net.ParseIP(ip)
	if addr == nil {
		return ip
	}
	if v4 := addr.To4(); v4 != nil {
		return v4.String()
	}
	return addr.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// delayFor is the delay applied to the n-th consecutive failure.
func delayFor(n int) time.Duration {
	if n <= limitFree {
		return 0
	}
	k := n - limitFree - 1
	if k > 8 {
		return limitMax
	}
	d := limitBase << uint(k)
	if d > limitMax {
		d = limitMax
	}
	return d
}

// fail records a failure for the bucket of ip, sleeps for the resulting
// delay and returns the failure count and delay.
func (l *authLimiter) fail(ip string) (int, time.Duration) {
	key := limitKey(ip)
	l.mu.Lock()
	now := l.now()
	f := l.byIP[key]
	if f == nil || now.Sub(f.last) > limitReset {
		if f == nil && len(l.byIP) >= limitEntries {
			l.pruneLocked(now)
		}
		f = &authFails{}
		l.byIP[key] = f
	}
	f.n++
	f.last = now
	n := f.n
	d := delayFor(n)
	if d > 0 {
		f.sleeping++
	}
	l.mu.Unlock()
	if d > 0 {
		l.sleep(d)
		l.mu.Lock()
		// reset may have replaced or removed the entry meanwhile; only
		// decrement the one this attempt incremented.
		if cur := l.byIP[key]; cur == f {
			f.sleeping--
		}
		l.mu.Unlock()
	}
	return n, d
}

// busy reports whether the bucket of ip already has limitConcurrent
// failed attempts sleeping; the caller refuses the request without
// touching the hash.
func (l *authLimiter) busy(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.byIP[limitKey(ip)]
	return f != nil && f.sleeping >= limitConcurrent
}

// acquire takes a slot for one password verification; false when
// limitHashConcurrent verifications are already running (the caller
// answers 429 without computing anything). Every true must be paired with
// release.
func (l *authLimiter) acquire() bool {
	select {
	case l.hashSem <- struct{}{}:
		return true
	default:
		return false
	}
}

// release returns the slot taken by acquire.
func (l *authLimiter) release() { <-l.hashSem }

// reset forgets the bucket of ip after a successful authentication.
func (l *authLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.byIP, limitKey(ip))
	l.mu.Unlock()
}

// pruneLocked drops expired entries; if the table is still full, the
// oldest live entry is evicted and the event logged (rate-limited).
func (l *authLimiter) pruneLocked(now time.Time) {
	var oldestIP string
	var oldest time.Time
	for ip, f := range l.byIP {
		if now.Sub(f.last) > limitReset {
			delete(l.byIP, ip)
			continue
		}
		if oldestIP == "" || f.last.Before(oldest) {
			oldestIP, oldest = ip, f.last
		}
	}
	if len(l.byIP) >= limitEntries && oldestIP != "" {
		delete(l.byIP, oldestIP)
		if l.fullLogged.IsZero() || now.Sub(l.fullLogged) >= limitFullLogEvery || now.Before(l.fullLogged) {
			l.fullLogged = now
			l.logf("web: auth limiter table full (%d buckets with recent failures); oldest entry evicted", limitEntries)
		}
	}
}
