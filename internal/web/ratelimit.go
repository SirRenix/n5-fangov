package web

import (
	"sync"
	"time"
)

// Failed basic-auth attempts are throttled per remote IP (M4): the first
// limitFree failures answer immediately, then the answer is delayed
// limitBase, doubling up to limitMax. A success or limitReset of quiet
// resets the counter.
const (
	limitFree    = 5
	limitBase    = 250 * time.Millisecond
	limitMax     = 2 * time.Second
	limitReset   = 10 * time.Minute
	limitEntries = 4096 // upper bound on tracked IPs
	// limitConcurrent is how many failed attempts of one IP may sleep at
	// the same time; further attempts are refused immediately (429)
	// without a hash computation (M3c).
	limitConcurrent = 4
)

type authFails struct {
	n        int
	last     time.Time
	sleeping int // attempts of this IP currently inside sleep
}

type authLimiter struct {
	mu    sync.Mutex
	byIP  map[string]*authFails
	now   func() time.Time
	sleep func(time.Duration)
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{byIP: map[string]*authFails{}, now: time.Now, sleep: time.Sleep}
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

// fail records a failure for ip, sleeps for the resulting delay and returns
// the failure count and delay.
func (l *authLimiter) fail(ip string) (int, time.Duration) {
	l.mu.Lock()
	now := l.now()
	f := l.byIP[ip]
	if f == nil || now.Sub(f.last) > limitReset {
		if f == nil && len(l.byIP) >= limitEntries {
			l.pruneLocked(now)
		}
		f = &authFails{}
		l.byIP[ip] = f
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
		if cur := l.byIP[ip]; cur == f {
			f.sleeping--
		}
		l.mu.Unlock()
	}
	return n, d
}

// busy reports whether ip already has limitConcurrent failed attempts
// sleeping; the caller refuses the request without touching the hash.
func (l *authLimiter) busy(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.byIP[ip]
	return f != nil && f.sleeping >= limitConcurrent
}

// reset forgets ip after a successful authentication.
func (l *authLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.byIP, ip)
	l.mu.Unlock()
}

// pruneLocked drops expired entries; if the table is still full, the oldest.
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
	}
}
