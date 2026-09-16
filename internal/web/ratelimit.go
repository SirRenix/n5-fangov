package web

import (
	"net/netip"
	"sync"
	"time"
)

// Failed basic-auth attempts are throttled per source bucket: the
// first limitFree failures answer immediately, then the answer is delayed
// limitBase, doubling up to limitMax. A success or limitReset of quiet
// resets the counter.
//
// The bucket is the IPv4 address or the IPv6 /64 prefix (limitKey): a LAN
// host with router advertisements can pick any address of its /64, so a
// per-address bucket would hand out limitFree free attempts per new
// address. The concurrency cap (busy) is per full address (addrKey)
// instead: it exists to stop one client from side-stepping its delay with
// parallel requests, and keyed by the prefix one misbehaving host would
// answer 429 to every other host of the LAN's /64. Independent of both,
// at most limitHashConcurrent password checks (PBKDF2, tens of
// milliseconds of CPU each) run at the same time in the whole process;
// further ones are refused with 429 before any hash is computed, so many
// source addresses cannot saturate the host the regulation loop shares.
const (
	limitFree    = 5
	limitBase    = 250 * time.Millisecond
	limitMax     = 2 * time.Second
	limitReset   = 10 * time.Minute
	limitEntries = 4096 // upper bound on tracked buckets
	// limitConcurrent is how many failed attempts of one address may sleep
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
	n    int
	last time.Time
}

// sleepers counts the attempts of one address currently inside sleep. The
// entry is removed when the count returns to zero, so the map holds at
// most one entry per address with a delayed attempt in flight — bounded
// by the open connections, not by the addresses ever seen.
type sleepers struct {
	n int
}

type authLimiter struct {
	mu   sync.Mutex
	byIP map[string]*authFails // keyed by limitKey
	// sleeping is keyed by addrKey; see sleepers.
	sleeping map[string]*sleepers
	now      func() time.Time
	sleep    func(time.Duration)
	logf     func(string, ...any)
	// hashSem holds one token per password verification in flight.
	hashSem chan struct{}
	// fullLogged is when "table full" was last logged (zero: never).
	fullLogged time.Time
}

// newAuthLimiter returns a limiter that logs nowhere; New sets logf.
func newAuthLimiter() *authLimiter {
	return &authLimiter{
		byIP:     map[string]*authFails{},
		sleeping: map[string]*sleepers{},
		now:      time.Now,
		sleep:    time.Sleep,
		logf:     func(string, ...any) {},
		hashSem:  make(chan struct{}, limitHashConcurrent),
	}
}

// parseAddr parses a remote address for the limiter keys: an IPv4-mapped
// address is unmapped, a link-local zone ("fe80::1%vmbr0") is dropped so
// the address falls into its /64 like any other. ok is false for input
// that is not an IP address.
func parseAddr(ip string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

// limitKey maps a remote address to its limiter bucket: an IPv4 address
// is its own bucket, an IPv6 address shares the bucket of its /64 prefix
// (rendered as "<prefix>/64"). Anything that does not parse is used as
// given.
func limitKey(ip string) string {
	addr, ok := parseAddr(ip)
	if !ok {
		return ip
	}
	if addr.Is4() {
		return addr.String()
	}
	p, err := addr.Prefix(64)
	if err != nil {
		return ip
	}
	return p.String()
}

// addrKey maps a remote address to its concurrency-cap key: the address
// itself in canonical form (unmapped, without zone). Anything that does
// not parse is used as given.
func addrKey(ip string) string {
	addr, ok := parseAddr(ip)
	if !ok {
		return ip
	}
	return addr.String()
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
// delay (counted against the address of ip) and returns the failure count
// and delay.
func (l *authLimiter) fail(ip string) (int, time.Duration) {
	key, addr := limitKey(ip), addrKey(ip)
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
	var sl *sleepers
	if d > 0 {
		sl = l.sleeping[addr]
		if sl == nil {
			sl = &sleepers{}
			l.sleeping[addr] = sl
		}
		sl.n++
	}
	l.mu.Unlock()
	if d > 0 {
		l.sleep(d)
		l.mu.Lock()
		// reset may have removed the entry meanwhile; only decrement the
		// one this attempt incremented, and drop it when it is idle.
		if cur := l.sleeping[addr]; cur == sl {
			sl.n--
			if sl.n <= 0 {
				delete(l.sleeping, addr)
			}
		}
		l.mu.Unlock()
	}
	return n, d
}

// busy reports whether the address ip already has limitConcurrent failed
// attempts sleeping; the caller refuses the request without touching the
// hash.
func (l *authLimiter) busy(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	sl := l.sleeping[addrKey(ip)]
	return sl != nil && sl.n >= limitConcurrent
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

// reset forgets the bucket of ip and the concurrency count of its address
// after a successful authentication.
func (l *authLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.byIP, limitKey(ip))
	delete(l.sleeping, addrKey(ip))
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
