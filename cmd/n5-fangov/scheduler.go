// scheduler.go runs the preset schedules ([[schedule]], DESIGN "Schedules"):
// a ticker evaluates which entry is active and applies its preset on a
// transition through the same dirPresetStore.Apply the API uses. It never
// re-applies within a window, so a manual preset apply or curve edit
// during a window stands until the next transition.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/schedule"
)

// schedulerTick is how often the scheduler evaluates the entries.
const schedulerTick = 30 * time.Second

// scheduler implements web.Deps.Schedules (Status) and the transition
// logic. apply is dirPresetStore.Apply, alert delivers the cooled
// "schedule" alert (serve: sendAlertCooled).
type scheduler struct {
	apply func(name string) error
	alert func(msg string) // the cooled "schedule" alert
	logf  func(string, ...any)
	now   func() time.Time

	mu      sync.Mutex
	entries []schedule.Entry
	// baseline of the last evaluation: the active entry (copy) and
	// whether one was active; evaluated is false before the first run.
	evaluated bool
	lastOK    bool
	lastEntry schedule.Entry
	last      *scheduleSwitch
}

// scheduleSwitch is the last preset switch the scheduler attempted.
type scheduleSwitch struct {
	TS     int64  `json:"ts"`
	Preset string `json:"preset"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

func newScheduler(apply func(string) error, alert func(msg string), logf func(string, ...any), now func() time.Time) *scheduler {
	if now == nil {
		now = time.Now
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if alert == nil {
		alert = func(string) {}
	}
	return &scheduler{apply: apply, alert: alert, logf: logf, now: now}
}

// Set replaces the entries (every reload). The next evaluation compares
// the new active entry with the baseline: a changed active entry counts
// as a transition, an unchanged one does not.
func (s *scheduler) Set(entries []schedule.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append([]schedule.Entry(nil), entries...)
}

// Run evaluates once now, then every schedulerTick until ctx is done.
func (s *scheduler) Run(ctx context.Context) {
	s.evaluate()
	t := time.NewTicker(schedulerTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.evaluate()
		}
	}
}

// evaluate is one tick: on a transition the preset of the now active
// entry is applied (outside the lock — Apply reloads the daemon, and the
// reload hook calls Set). Leaving every window without a fallback applies
// nothing. A failed apply is logged and alerted once; the baseline moves
// anyway, so the retry happens at the next transition, not every tick.
func (s *scheduler) evaluate() {
	now := s.now()
	s.mu.Lock()
	idx, ok := schedule.Active(s.entries, now)
	var entry schedule.Entry
	if ok {
		entry = s.entries[idx]
	}
	transition := !s.evaluated || ok != s.lastOK || (ok && !schedule.Equal(entry, s.lastEntry))
	s.evaluated, s.lastOK, s.lastEntry = true, ok, entry
	s.mu.Unlock()
	if !transition {
		return
	}
	if !ok {
		s.logf("schedule: no window active, curves stay as they are")
		return
	}
	window := "fallback"
	if !entry.Fallback {
		window = entry.From + "–" + entry.To
	}
	sw := scheduleSwitch{TS: now.Unix(), Preset: entry.Preset, OK: true}
	if err := s.apply(entry.Preset); err != nil {
		sw.OK = false
		sw.Error = err.Error()
		s.logf("%sschedule: preset %q (%s) not applied: %v — the previous curves stay", logError, entry.Preset, window, err)
		s.alert(fmt.Sprintf("scheduled preset %q (%s) could not be applied: %v\nThe previous curves stay in effect; the scheduler retries at the next switch.", entry.Preset, window, err))
	} else {
		s.logf("schedule: preset %q applied (%s)", entry.Preset, window)
	}
	s.mu.Lock()
	s.last = &sw
	s.mu.Unlock()
}

// scheduleEntry is one entry in the Status answer.
type scheduleEntry struct {
	Preset   string   `json:"preset"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Days     []string `json:"days"`
	Fallback bool     `json:"fallback"`
	Active   bool     `json:"active"`
}

type scheduleNext struct {
	TS     int64  `json:"ts"`
	Preset string `json:"preset"` // "" when no entry is active after the switch
}

type scheduleStatus struct {
	Entries  []scheduleEntry `json:"entries"`
	Active   int             `json:"active"` // index, -1 = none
	Next     *scheduleNext   `json:"next"`
	Last     *scheduleSwitch `json:"last"`
	Timezone string          `json:"timezone"`
}

// Status is the GET /api/schedules body (DESIGN "Schedules").
func (s *scheduler) Status() any {
	now := s.now()
	s.mu.Lock()
	entries := s.entries
	last := s.last
	s.mu.Unlock()
	idx, ok := schedule.Active(entries, now)
	if !ok {
		idx = -1
	}
	st := scheduleStatus{Entries: make([]scheduleEntry, 0, len(entries)), Active: idx, Timezone: zoneLabel(now)}
	for i, e := range entries {
		days := make([]string, 0, len(e.Days))
		for _, d := range e.Days {
			days = append(days, schedule.DayName(d))
		}
		st.Entries = append(st.Entries, scheduleEntry{Preset: e.Preset, From: e.From, To: e.To, Days: days, Fallback: e.Fallback, Active: i == idx})
	}
	if at, nidx, ok := schedule.Next(entries, now); ok {
		n := &scheduleNext{TS: at.Unix()}
		if nidx >= 0 {
			n.Preset = entries[nidx].Preset
		}
		st.Next = n
	}
	if last != nil {
		l := *last
		st.Last = &l
	}
	return st
}

// zoneLabel names the zone the windows are evaluated in ("CEST +02:00").
func zoneLabel(now time.Time) string {
	name, off := now.Zone()
	sign := "+"
	if off < 0 {
		sign = "-"
		off = -off
	}
	return fmt.Sprintf("%s %s%02d:%02d", name, sign, off/3600, (off%3600)/60)
}
