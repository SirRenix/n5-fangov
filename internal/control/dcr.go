package control

import (
	"errors"
	"fmt"

	"github.com/SirRenix/n5-fangov/internal/logfile"
	"github.com/SirRenix/n5-fangov/internal/profile"
)

// DCRWarnInterval is the minimum time between two journal warnings about
// an EC duty register that differs from the written duty, per channel.
// Occurrences in between are counted and reported with the next warning.
const DCRWarnInterval = ReadErrInterval

// dcrDeviation handles the result of a duty write. A profile.ErrDCRMismatch
// (driver loaded with debug_regs=1: the register dump shows another value
// than the one the driver accepted) is no write error: the sysfs read-back
// passed, and the failsafe would not change what the EC does with the
// value. It is logged as its own rate-limited warning and nil is returned;
// every other error is returned unchanged (audit 2026-10-16 G4).
func (c *Controller) dcrDeviation(name string, err error) error {
	if !errors.Is(err, profile.ErrDCRMismatch) {
		return err
	}
	key := "dcr " + name
	now := c.opts.Now()
	c.logMu.Lock()
	if c.readErrs == nil {
		c.readErrs = map[string]*readErrState{}
	}
	st := c.readErrs[key]
	if st == nil {
		st = &readErrState{}
		c.readErrs[key] = st
	}
	if !st.last.IsZero() && now.Sub(st.last) < DCRWarnInterval {
		st.suppressed++
		c.logMu.Unlock()
		return nil
	}
	more := ""
	if st.suppressed > 0 {
		more = fmt.Sprintf(" (%d more since %s)", st.suppressed, st.last.Format("15:04:05"))
	}
	st.last, st.suppressed = now, 0
	c.logMu.Unlock()
	c.log.Printf("%sEC register deviation: %s: %v%s; regulation continues (debug_regs is for maintenance windows only)", logfile.PrefixWarning, name, err, more)
	return nil
}

// noteRegisterWrite remembers a successful duty write for the register
// comparison in a later cycle (checkRegisters). Rewriting the same duty
// keeps the time of the first write: the EC is already settling on it.
// Caller holds hwMu.
func (c *Controller) noteRegisterWrite(ch *channel, duty int) {
	rv, ok := c.dev.(profile.RegisterVerifier)
	if !ok || rv.RegisterSettle(ch.cfg.PWM) <= 0 {
		return
	}
	if ch.regPending && ch.regDuty == duty {
		return
	}
	ch.regDuty, ch.regAt, ch.regPending = duty, c.opts.Now(), true
}

// checkRegisters compares the hardware register of every channel whose
// last write is at least its settle time old (RegisterSettle) with that
// duty: one read each, no wait, at the start of the write phase. The EC
// takes a written duty with a delay (maintenance window 1, 2026-10-17:
// PCIe within 0.22 s, the HDD channel ramps DCR3/DCR4 over about 4.6 s,
// CPU/SSD read 116 instead of 188 right after the write); a comparison
// right after the write reported every channel in the first cycle.
// Caller holds hwMu.
func (c *Controller) checkRegisters(chans []*channel) {
	rv, ok := c.dev.(profile.RegisterVerifier)
	if !ok || c.opts.DryRun {
		return
	}
	now := c.opts.Now()
	for _, ch := range chans {
		if !ch.regPending || now.Sub(ch.regAt) < rv.RegisterSettle(ch.cfg.PWM) {
			continue
		}
		ch.regPending = false
		_ = c.dcrDeviation(ch.cfg.Name, rv.VerifyRegister(ch.cfg.PWM, ch.regDuty))
	}
}
