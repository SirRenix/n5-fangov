package control

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// Assist (DESIGN "Assist"): a channel's curve target is raised by the heat
// of other sources ([[assist]]). There is no second regulator — the curve
// (after hysteresis and min_on) stays the base, assist adds to it:
//
//  1. per source a boost 0 → boost, linear between start and full, at a
//     hysteresis-held reading of that source;
//  2. the channel's raw boost is the maximum over its sources (not the
//     sum), held for min_on after a rise;
//  3. target = min(255, curve target + boost); assist never lowers.
//
// Only mode auto is raised: an override replaces the target (assist idle),
// critical, the ceiling, stall, failsafe and the channel's own sensor error
// win as before. A source that cannot be read contributes 0 (logged once
// per episode, no alert). Switching assist off ends a running boost at
// once; the slew (step_down) ramps the duty down.

// ErrNoAssist is returned by SetAssistEnabled for a channel without an
// [[assist]] table in the running config.
var ErrNoAssist = errors.New("no [[assist]] table for this channel")

// assistSrc is the runtime state of one [[assist.source]]: its reader (nil
// while unresolved), the last reading, the hysteresis-held reading the
// boost is computed at, the source's boost of this cycle and whether it is
// in an unreadable episode (bad: logged once on the way in and out).
type assistSrc struct {
	cfg    config.AssistSource
	sensor SensorReader
	temp   int
	ok     bool
	held   int
	heldOK bool
	boost  int
	bad    bool
}

// assistRT is the runtime state of one channel's [[assist]] table. raw and
// rawSrc are this cycle's maximum over the sources; last the previous
// cycle's raw boost (0 at the start and after an idle cycle, so the first
// boost counts as a rise); holding/holdVal/holdSrc/holdStart/holdUntil the
// min_on hold; boost and source what was added this cycle (0 when off,
// idle or the sources are below start). Loop goroutine only.
type assistRT struct {
	cfg       config.Assist
	srcs      []*assistSrc
	raw       int
	rawSrc    string
	last      int
	holding   bool
	holdVal   int
	holdSrc   string
	holdStart time.Time
	holdUntil time.Time
	boost     int
	source    string
}

// bindAssists attaches the [[assist]] tables of cfg to the managed
// channels (New, and the loop's applyPendingLocked with c.mu held). A
// source whose id is unchanged keeps its reader and held reading, a new
// one is resolved now (and retried every cycle while unresolved). A table
// that is switched off ends its hold at once; a changed min_on re-times a
// running hold like retimeHolds. A table for a channel this device does
// not manage is logged once and ignored.
func (c *Controller) bindAssists(cfg config.Config) {
	managed := map[string]bool{}
	for _, ch := range c.chans {
		managed[ch.cfg.Name] = true
		ac := cfg.Assist(ch.cfg.Name)
		if ac == nil {
			if ch.assist != nil {
				c.log.Printf("assist %s: table removed", ch.cfg.Name)
			}
			ch.assist = nil
			continue
		}
		old := ch.assist
		rt := &assistRT{cfg: *ac}
		rt.cfg.Sources = append([]config.AssistSource(nil), ac.Sources...)
		if old != nil {
			rt.last, rt.holding, rt.holdVal, rt.holdSrc = old.last, old.holding, old.holdVal, old.holdSrc
			rt.holdStart, rt.holdUntil = old.holdStart, old.holdUntil
		}
		for _, sc := range rt.cfg.Sources {
			s := &assistSrc{cfg: sc}
			if old != nil {
				for _, os := range old.srcs {
					if os.cfg.Sensor == sc.Sensor {
						s.sensor, s.temp, s.ok, s.held, s.heldOK, s.bad = os.sensor, os.temp, os.ok, os.held, os.heldOK, os.bad
						break
					}
				}
			}
			if s.sensor == nil {
				s.sensor = c.resolveAssist(ch.cfg.Name, sc.Sensor)
			}
			rt.srcs = append(rt.srcs, s)
		}
		rt.dropStaleHold()
		switch {
		case !rt.cfg.Enabled:
			rt.idle()
		case rt.holding && rt.cfg.MinOn <= 0:
			rt.holding = false
		case rt.holding && (old == nil || old.cfg.MinOn != rt.cfg.MinOn):
			if until := rt.holdStart.Add(rt.cfg.MinOn); until.After(c.opts.Now()) {
				rt.holdUntil = until
			} else {
				rt.holdUntil = c.opts.Now()
			}
		}
		if old == nil || old.cfg.Enabled != rt.cfg.Enabled {
			c.log.Printf("assist %s: %s (%s)", ch.cfg.Name, onOff(rt.cfg.Enabled), rt.describe())
		}
		ch.assist = rt
	}
	for _, a := range cfg.Assists {
		if !managed[a.Channel] {
			c.logOnce("assist.chan."+a.Channel, "assist %s: channel not managed on this device, table ignored", a.Channel)
		} else {
			c.logClear("assist.chan." + a.Channel)
		}
	}
}

// dropStaleHold ends a running hold that the new table no longer backs:
// its source was removed, or that source's boost is now below the held
// value (a lowered boost must not stay up for min_on under the old value).
// The hold is dropped, not clamped: the next cycle computes the boost from
// the live readings and starts a new hold on a rise. last is lowered to
// the new tables' largest boost so that rise is seen.
func (a *assistRT) dropStaleHold() {
	maxAll := 0
	backed := false
	for _, s := range a.cfg.Sources {
		maxAll = max(maxAll, s.Boost)
		if s.Sensor == a.holdSrc && s.Boost >= a.holdVal {
			backed = true
		}
	}
	a.last = min(a.last, maxAll)
	if a.holding && !backed {
		a.holding, a.holdVal, a.holdSrc = false, 0, ""
	}
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// describe lists the sources of a table for the log.
func (a *assistRT) describe() string {
	parts := make([]string, 0, len(a.cfg.Sources))
	for _, s := range a.cfg.Sources {
		parts = append(parts, fmt.Sprintf("%s %d..%dC +%d", s.Sensor, s.Start, s.Full, s.Boost))
	}
	return strings.Join(parts, ", ")
}

// resolveAssist resolves one source id; a failure is logged once per id
// (the source then contributes 0 and is retried every cycle).
func (c *Controller) resolveAssist(chName, id string) SensorReader {
	s, err := c.newS(id)
	if err != nil {
		c.logOnce("assist.resolve."+chName+"."+id, "assist %s: source %q: %v (contributes 0, retried)", chName, id, err)
		return nil
	}
	c.logClear("assist.resolve." + chName + "." + id)
	return s
}

// readAssist reads every assist source and computes the per-source boosts
// and each channel's raw boost (the maximum). It runs every cycle for every
// table, switched on or off, so the snapshot shows what assist would add.
// Loop goroutine only.
func (c *Controller) readAssist(chans []*channel) {
	for _, ch := range chans {
		a := ch.assist
		if a == nil {
			continue
		}
		a.raw, a.rawSrc = 0, ""
		for _, s := range a.srcs {
			why := ""
			if s.sensor == nil {
				s.sensor = c.resolveAssist(ch.cfg.Name, s.cfg.Sensor)
			}
			if s.sensor == nil {
				why = "unresolved"
			} else if v, err := s.sensor.Read(); err != nil {
				why = "read: " + err.Error()
				// the device may have re-enumerated: resolve again next cycle
				if r := c.resolveAssist(ch.cfg.Name, s.cfg.Sensor); r != nil {
					s.sensor = r
				}
			} else if !sensor.Plausible(v) {
				why = fmt.Sprintf("implausible %d mdegC", v)
			} else {
				s.temp, s.ok = v, true
			}
			if why != "" {
				s.ok, s.heldOK, s.boost = false, false, 0
				if !s.bad {
					s.bad = true
					c.log.Printf("assist %s: source %s unreadable (%s), contributes 0", ch.cfg.Name, s.cfg.Sensor, why)
				}
				continue
			}
			if s.bad {
				s.bad = false
				c.log.Printf("assist %s: source %s readable again", ch.cfg.Name, s.cfg.Sensor)
			}
			s.boost = sourceBoost(s.cfg, s.heldTemp(a.cfg.Hysteresis))
			if s.boost > a.raw {
				a.raw, a.rawSrc = s.boost, s.cfg.Sensor
			}
		}
	}
}

// heldTemp is heldTemp of the channel for one source: the held value
// follows the reading only when it moved by at least n degrees.
func (s *assistSrc) heldTemp(n int) int {
	if n <= 0 || !s.heldOK {
		s.held, s.heldOK = s.temp, true
		return s.held
	}
	if d := s.temp - s.held; d >= n*1000 || -d >= n*1000 {
		s.held = s.temp
	}
	return s.held
}

// sourceBoost is the boost of one source at milli: 0 up to start, boost
// from full on, linear (rounded) between.
func sourceBoost(sc config.AssistSource, milli int) int {
	lo, hi := sc.Start*1000, sc.Full*1000
	switch {
	case milli <= lo:
		return 0
	case milli >= hi || hi <= lo:
		return sc.Boost
	}
	v := int(float64(sc.Boost)*float64(milli-lo)/float64(hi-lo) + 0.5)
	return min(max(v, 0), sc.Boost)
}

// idle ends a running boost and hold (assist off, override, the channel's
// sensor error): the next boost counts as a rise from 0.
func (a *assistRT) idle() {
	a.holding, a.holdVal, a.holdSrc = false, 0, ""
	a.last, a.boost, a.source = 0, 0, ""
}

// hold applies min_on to the raw boost the way minOnHold does to the curve:
// a rise above the previous cycle's raw boost holds the higher value (and
// its source) for min_on; a further rise above it restarts the timer.
func (a *assistRT) hold(now time.Time) (int, string) {
	d := a.cfg.MinOn
	prev := a.last
	a.last = a.raw
	if d <= 0 {
		a.holding = false
		return a.raw, a.rawSrc
	}
	if a.holding && !now.Before(a.holdUntil) {
		a.holding = false
	}
	if a.raw > prev && (!a.holding || a.raw > a.holdVal) {
		a.holding, a.holdVal, a.holdSrc = true, a.raw, a.rawSrc
		a.holdStart, a.holdUntil = now, now.Add(d)
	}
	if a.holding && a.holdVal > a.raw {
		return a.holdVal, a.holdSrc
	}
	return a.raw, a.rawSrc
}

// applyAssist adds the channel's boost to the curve target (mode auto,
// called by computeTargets after the curve and min_on). Off: idle, the
// target unchanged.
func (ch *channel) applyAssist(target int, now time.Time) int {
	a := ch.assist
	if a == nil {
		return target
	}
	if !a.cfg.Enabled {
		a.idle()
		return target
	}
	a.boost, a.source = a.hold(now)
	if a.boost <= 0 {
		a.boost, a.source = 0, ""
		return target
	}
	return min(target+a.boost, 255)
}

// assistIdle is idle for a channel with a table (override, sensor error).
func (ch *channel) assistIdle() {
	if ch.assist != nil {
		ch.assist.idle()
	}
}

// assistBoost is the boost the channel's target carries this cycle: only
// in mode auto (critical, ceiling, stall, failsafe and the override set
// the target themselves).
func (ch *channel) assistBoost() (int, string) {
	if ch.assist == nil || ch.mode != ModeAuto {
		return 0, ""
	}
	return ch.assist.boost, ch.assist.source
}

// assistState renders the snapshot member (nil without a table).
func (ch *channel) assistState() *AssistState {
	a := ch.assist
	if a == nil {
		return nil
	}
	st := &AssistState{Enabled: a.cfg.Enabled, Sources: make([]AssistSourceState, 0, len(a.srcs))}
	st.Boost, st.Source = ch.assistBoost()
	for _, s := range a.srcs {
		ss := AssistSourceState{Sensor: s.cfg.Sensor, Temp: -999, Boost: s.boost}
		if s.ok {
			ss.Temp = float64(s.temp) / 1000
		}
		st.Sources = append(st.Sources, ss)
	}
	return st
}

// AssistLabel is the short name of a source id in the status line and the
// dashboard: the chip name of hwmon:<name>:tempN, the id itself otherwise.
func AssistLabel(id string) string {
	if rest, ok := strings.CutPrefix(id, "hwmon:"); ok {
		if name, _, ok := strings.Cut(rest, ":"); ok && name != "" {
			return name
		}
	}
	return id
}

// SetAssistEnabled switches the [[assist]] table of a channel on or off
// in the running config (PUT /api/assist/{channel}; the caller writes the
// file). The change goes through the pending config and takes effect on
// the next cycle; ErrNoAssist when the running config has no table for
// the channel.
func (c *Controller) SetAssistEnabled(name string, on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	base := &c.cfg
	if c.pending != nil {
		base = c.pending
	}
	if base.Assist(name) == nil {
		return fmt.Errorf("channel %q: %w", name, ErrNoAssist)
	}
	clone := base.Clone()
	clone.Assist(name).Enabled = on
	c.pending = &clone
	c.log.Printf("assist %s: switched %s (applied on next cycle)", name, onOff(on))
	return nil
}
