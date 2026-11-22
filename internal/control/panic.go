// panic.go is the full cooling ("panic button", DESIGN "Full cooling"):
// every channel at 255 for PanicDuration, started by hand only.
//
// It is an overlay on the target, applied in computeTargets after the
// normal rules. The loop keeps computing everything underneath — curve,
// hysteresis-held temperature, min_on hold, assist with its hold, the
// override, critical and the ceiling state — so when the full cooling
// ends each channel has the target it would have now, including an
// override, preset, schedule or assist change made meanwhile. Nothing of
// that state is frozen or dropped; only target and mode are replaced, and
// the write phase writes 255 without the step_up ramp (mode panic is not
// slewed). The way down after the end is the normal one: step_down in mode
// auto, an override at once as always. The failsafe of write errors stays
// first (writeAndFinish), the stall check after it as always.
package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/events"
)

// PanicDuration is the fixed length of a full cooling; a start during one
// sets the end to this long from then.
const PanicDuration = 10 * time.Minute

// panicDedupe: starts within this time of the last counted start are one
// (one event, one log line; the end still moves to 10 min from now).
const panicDedupe = 5 * time.Second

// panicFile is the run-dir file with the end of a running full cooling, so
// a daemon restart continues it (a host reboot clears /run).
const panicFile = "panic.until"

// ErrPanicRefused is returned by Panic when the daemon cannot write the
// fans; the wrapping error names the reason (409 in the API).
var ErrPanicRefused = errors.New("full cooling refused")

// panicRefusal is why a full cooling would not do anything: no writable
// channel, profile monitor, or what Options.PanicRefusal says (a read-only
// driver). "" = it would run.
func (c *Controller) panicRefusal() string {
	switch {
	case c.dev.Profile().Name() == "monitor":
		return "profile monitor: n5-fangov only reads the sensors here, it does not drive the fans"
	case len(c.chans) == 0:
		return "no regulated channels"
	case c.opts.PanicRefusal != nil:
		return c.opts.PanicRefusal()
	}
	return ""
}

// Panic starts the full cooling, or moves its end to PanicDuration from
// now, on behalf of by. A real start (not a repeat within 5 s, not an
// extension) wakes the loop for a cycle at once, at most once per
// interval. It returns the end. Refused after Stop.
//
// The state change and panic.until are written together under c.mu (a
// small file on the run dir's tmpfs): a concurrent PanicStop or expiry
// can never leave the file of a stopped full cooling behind, nor remove
// the file of a running one.
func (c *Controller) Panic(by string) (time.Time, error) {
	if r := c.panicRefusal(); r != "" {
		return time.Time{}, fmt.Errorf("%w: %s", ErrPanicRefused, r)
	}
	now := c.opts.Now()
	c.mu.Lock()
	if c.stopped.Load() {
		c.mu.Unlock()
		return time.Time{}, fmt.Errorf("%w: the daemon is stopping", ErrPanicRefused)
	}
	active := now.Before(c.panicUntil)
	dup := active && now.Sub(c.panicAt) < panicDedupe
	c.panicUntil = now.Add(PanicDuration)
	until := c.panicUntil
	if !dup {
		c.panicAt = now
		c.panicBy = by
	}
	kick := !active && (c.lastKick.IsZero() || now.Sub(c.lastKick) >= c.cfg.Daemon.Interval)
	if kick {
		c.lastKick = now
	}
	c.savePanic(until, by)
	c.mu.Unlock()
	if !dup {
		who := events.Who(by)
		if active {
			c.log.Printf("full cooling extended until %s by %s", until.Format("15:04:05"), by)
			c.eventBy(events.PanicExtend, "", fmt.Sprintf("full cooling extended until %s (%s)", until.Format("15:04"), who), by)
		} else {
			c.log.Printf("full cooling: every channel at 255 until %s, started by %s", until.Format("15:04:05"), by)
			c.eventBy(events.Panic, "", fmt.Sprintf("full cooling until %s (%s)", until.Format("15:04"), who), by)
		}
	}
	if kick {
		c.kickLoop()
	}
	return until, nil
}

// PanicStop ends a running full cooling early on behalf of by; false when
// none was running. The channels go back the normal way from the next
// regular cycle on (no extra cycle: the way down needs no hurry).
func (c *Controller) PanicStop(by string) bool {
	now := c.opts.Now()
	c.mu.Lock()
	active := now.Before(c.panicUntil)
	c.panicUntil, c.panicBy = time.Time{}, ""
	c.removeRunFile(panicFile, panicFile)
	c.mu.Unlock()
	if !active {
		return false
	}
	c.log.Printf("full cooling stopped by %s", by)
	c.eventBy(events.PanicEnd, "", "full cooling stopped ("+events.Who(by)+")", by)
	return true
}

// PanicState returns the end and the starter of a running full cooling;
// ok false when none runs.
func (c *Controller) PanicState() (until time.Time, by string, ok bool) {
	now := c.opts.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !now.Before(c.panicUntil) {
		return time.Time{}, "", false
	}
	return c.panicUntil, c.panicBy, true
}

// panicCycle is the loop's view at the start of a cycle: whether the full
// cooling applies; an end found here (expiry) is logged and recorded once.
func (c *Controller) panicCycle(now time.Time) bool {
	c.mu.Lock()
	until := c.panicUntil
	expired := !until.IsZero() && !now.Before(until)
	if expired {
		c.panicUntil, c.panicBy = time.Time{}, ""
		c.removeRunFile(panicFile, panicFile)
	}
	c.mu.Unlock()
	if expired {
		c.log.Printf("full cooling ended (10 min passed), regulation resumed")
		c.event(events.PanicEnd, "", "full cooling ended (10 min passed)")
	}
	return !until.IsZero() && !expired
}

// panicSnapLocked is the snapshot member: the running full cooling or
// nil. Caller holds c.mu.
func (c *Controller) panicSnapLocked(now time.Time) *PanicState {
	if !now.Before(c.panicUntil) {
		return nil
	}
	return &PanicState{Until: c.panicUntil.Unix(), By: c.panicBy}
}

// applyPanic is the overlay of computeTargets: 255 on every channel; mode
// panic where the channel would run its curve or an override. critical
// (also the ceiling state), stall and sensor-error keep their mode: they
// want 255 as well, or are faults the snapshot must still show.
func applyPanic(chans []*channel) {
	for _, ch := range chans {
		ch.target = 255
		if ch.mode == ModeAuto || ch.mode == ModeManual {
			ch.mode = ModePanic
		}
	}
}

// kickLoop wakes the loop for a cycle now (a full cooling must not wait
// for the interval). Never blocks. A cycle that comes early this way does
// not count towards the stall, stale and emergency limits (cycle,
// cycleState.early): those count cycles and assume one interval each.
func (c *Controller) kickLoop() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// wait is Options.Wait that also ends early on kickLoop; false only when
// ctx is done.
func (c *Controller) wait(ctx context.Context, d time.Duration) bool {
	kctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-c.kick:
			cancel()
		case <-done:
		}
	}()
	c.opts.Wait(kctx, d)
	return ctx.Err() == nil
}

// savePanic writes panic.until ("<unix>\n<by>\n"); a dry run writes no
// files of its own (it computes the full cooling all the same), nor does a
// stopped controller. Caller holds c.mu.
func (c *Controller) savePanic(until time.Time, by string) {
	if c.opts.RunDir == "" || c.opts.DryRun || c.stopped.Load() {
		return
	}
	path := filepath.Join(c.opts.RunDir, panicFile)
	if err := writeFileAtomic(path, []byte(strconv.FormatInt(until.Unix(), 10)+"\n"+by+"\n")); err != nil {
		c.log.Printf("%s: %v (a daemon restart ends the full cooling)", panicFile, err)
	}
}

// loadPanic continues a full cooling from panic.until at start; an
// expired or broken file is removed with a log line.
func (c *Controller) loadPanic() {
	path := filepath.Join(c.opts.RunDir, panicFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	first, rest, _ := strings.Cut(string(b), "\n")
	ts, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	until := time.Unix(ts, 0)
	// the file holds wall time; the rest is measured from now on the clock
	// the loop uses (monotonic in production), so a clock step after the
	// restart neither lengthens nor shortens it, and never beyond 10 min
	now := c.opts.Now()
	left := until.Sub(now)
	switch {
	case err != nil || ts <= 0:
		c.log.Printf("%s invalid (%q), removed: no full cooling", panicFile, strings.TrimSpace(first))
	case left <= 0:
		c.log.Printf("%s: full cooling ended at %s while the daemon was down, removed", panicFile, until.Format("15:04:05"))
	case left > PanicDuration+time.Minute:
		c.log.Printf("%s: end %s lies too far ahead, removed: no full cooling", panicFile, until.Format("2006-01-02 15:04:05"))
	default:
		c.panicUntil, c.panicBy = now.Add(min(left, PanicDuration)), strings.TrimSpace(rest)
		c.log.Printf("full cooling continued until %s (%s)", c.panicUntil.Format("15:04:05"), path)
		return
	}
	c.removeRunFile(panicFile, panicFile)
}
