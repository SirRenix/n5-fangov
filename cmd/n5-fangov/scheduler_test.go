package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/schedule"
	"github.com/SirRenix/n5-fangov/internal/web"
)

type schedTest struct {
	s       *scheduler
	clock   time.Time
	applied []string
	fail    map[string]error
	alerts  []string
	logs    []string
}

var schedZone = time.FixedZone("UTC+2", 2*3600)

// newSchedTest builds a scheduler on a fake clock (Monday 2026-09-14 in a
// fixed zone) with a fake preset applier.
func newSchedTest(t *testing.T, hour, min int) *schedTest {
	t.Helper()
	st := &schedTest{clock: time.Date(2026, 9, 14, hour, min, 0, 0, schedZone), fail: map[string]error{}}
	st.s = newScheduler(
		func(name string) error {
			st.applied = append(st.applied, name)
			return st.fail[name]
		},
		func(msg string) { st.alerts = append(st.alerts, msg) },
		func(format string, args ...any) { st.logs = append(st.logs, fmt.Sprintf(format, args...)) },
		func() time.Time { return st.clock },
	)
	return st
}

func (st *schedTest) tick(d time.Duration) {
	st.clock = st.clock.Add(d)
	st.s.evaluate()
}

func schedEntries() []schedule.Entry {
	return schedule.FromConfig([]config.Schedule{
		{Preset: "night", From: "22:00", To: "07:00"},
		{Preset: "office", From: "08:00", To: "18:00", Days: []string{"mon", "tue", "wed", "thu", "fri"}},
		{Preset: "balanced"},
	})
}

func TestSchedulerTransitions(t *testing.T) {
	st := newSchedTest(t, 17, 59)
	st.s.Set(schedEntries())
	// first evaluation applies the active entry once
	st.s.evaluate()
	if strings.Join(st.applied, ",") != "office" {
		t.Fatalf("start: applied %v", st.applied)
	}
	// ticks inside the window never re-apply
	st.tick(30 * time.Second)
	st.tick(29 * time.Second)
	if len(st.applied) != 1 {
		t.Errorf("re-applied inside the window: %v", st.applied)
	}
	// 18:00 → fallback
	st.tick(time.Second)
	if strings.Join(st.applied, ",") != "office,balanced" {
		t.Errorf("18:00: applied %v", st.applied)
	}
	// 22:00 → night
	st.tick(4 * time.Hour)
	if strings.Join(st.applied, ",") != "office,balanced,night" {
		t.Errorf("22:00: applied %v", st.applied)
	}
	if len(st.alerts) != 0 {
		t.Errorf("alerts: %v", st.alerts)
	}
	if !strings.Contains(strings.Join(st.logs, "\n"), `schedule: preset "night" applied (22:00-07:00)`) {
		t.Errorf("log lines: %v", st.logs)
	}
	if !strings.Contains(strings.Join(st.logs, "\n"), `schedule: preset "balanced" applied (fallback)`) {
		t.Errorf("fallback log line: %v", st.logs)
	}
}

func TestSchedulerNoFallback(t *testing.T) {
	st := newSchedTest(t, 17, 59)
	st.s.Set(schedEntries()[:2])
	st.s.evaluate()
	st.tick(time.Minute) // 18:00: every window left, no fallback → nothing applied
	if strings.Join(st.applied, ",") != "office" {
		t.Errorf("applied %v", st.applied)
	}
	if !strings.Contains(strings.Join(st.logs, "\n"), "no window active") {
		t.Errorf("logs: %v", st.logs)
	}
	// re-entering a window applies again
	st.tick(4 * time.Hour)
	if strings.Join(st.applied, ",") != "office,night" {
		t.Errorf("applied %v", st.applied)
	}
	// start outside every window: nothing applied, no alert
	st2 := newSchedTest(t, 19, 0)
	st2.s.Set(schedEntries()[:2])
	st2.s.evaluate()
	if len(st2.applied) != 0 || len(st2.alerts) != 0 {
		t.Errorf("start outside: applied %v alerts %v", st2.applied, st2.alerts)
	}
}

func TestSchedulerFailureAlertsOnceRetriesNextTransition(t *testing.T) {
	st := newSchedTest(t, 21, 59)
	st.s.Set(schedEntries())
	st.fail["night"] = fmt.Errorf("preset %q: %w", "/etc/n5-fangov/presets/night.toml", fs.ErrNotExist)
	st.s.evaluate() // fallback applied
	st.tick(time.Minute)
	if strings.Join(st.applied, ",") != "balanced,night" {
		t.Fatalf("applied %v", st.applied)
	}
	// the alert and the API carry the class, the log the full error
	if len(st.alerts) != 1 || !strings.Contains(st.alerts[0], "preset missing") || strings.Contains(st.alerts[0], "/etc/") {
		t.Errorf("alerts: %v", st.alerts)
	}
	if l := strings.Join(st.logs, "\n"); !strings.Contains(l, "(preset missing): preset \"/etc/n5-fangov/presets/night.toml\": file does not exist") {
		t.Errorf("logs: %v", st.logs)
	}
	// no retry on the following ticks
	st.tick(30 * time.Second)
	st.tick(30 * time.Second)
	if len(st.applied) != 2 || len(st.alerts) != 1 {
		t.Errorf("retried inside the window: applied %v alerts %v", st.applied, st.alerts)
	}
	status := st.s.Status().(scheduleStatus)
	if status.Last == nil || status.Last.OK || status.Last.Preset != "night" || status.Last.Error != "preset missing" {
		t.Errorf("last: %+v", status.Last)
	}
	// the next transition (07:00 → fallback) applies again
	st.tick(9 * time.Hour)
	if strings.Join(st.applied, ",") != "balanced,night,balanced" {
		t.Errorf("applied %v", st.applied)
	}
	status = st.s.Status().(scheduleStatus)
	if status.Last == nil || !status.Last.OK || status.Last.Preset != "balanced" || status.Last.Error != "" {
		t.Errorf("last after success: %+v", status.Last)
	}
}

// TestPresetErrorClass: the API sees a class, never the file path or the
// parser text of the failure.
func TestPresetErrorClass(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("preset %q: %w", "x", fs.ErrNotExist), "preset missing"},
		{fmt.Errorf("%w: config: toml: line 3: expected value", errPresetInvalid), "preset invalid"},
		{fmt.Errorf("%w: current config: open /etc/n5-fangov/config.toml: permission denied", errPresetWrite), "write failed"},
		{fmt.Errorf("preset x written, %w", &web.ReloadError{Err: errors.New("channel cpu: pwm1_enable not writable")}), "reload failed"},
		{errRestartRequired(), "restart required"},
		{errors.New("something else"), "apply failed"},
	} {
		if got := presetErrorClass(c.err); got != c.want {
			t.Errorf("presetErrorClass(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	// the class of every Apply failure reaches Status().last.error
	st := newSchedTest(t, 21, 59)
	st.s.Set(schedEntries())
	st.fail["night"] = fmt.Errorf("%w: config: toml: line 3: expected value", errPresetInvalid)
	st.s.evaluate()
	st.tick(time.Minute)
	b, _ := json.Marshal(st.s.Status())
	if s := string(b); !strings.Contains(s, `"error":"preset invalid"`) || strings.Contains(s, "toml") {
		t.Errorf("status: %s", s)
	}
}

// TestSchedulerFirstRunNothingActive: an empty entry list, or a start
// outside every window without a fallback, is not a transition — nothing
// is applied, nothing is logged. Leaving a window later still logs.
func TestSchedulerFirstRunNothingActive(t *testing.T) {
	st := newSchedTest(t, 12, 0)
	st.s.evaluate()
	st.tick(30 * time.Second)
	if len(st.applied) != 0 || len(st.alerts) != 0 || len(st.logs) != 0 {
		t.Errorf("empty list: applied %v alerts %v logs %v", st.applied, st.alerts, st.logs)
	}
	st2 := newSchedTest(t, 19, 0)
	st2.s.Set(schedEntries()[:2])
	st2.s.evaluate()
	if len(st2.applied) != 0 || len(st2.logs) != 0 {
		t.Errorf("outside every window: applied %v logs %v", st2.applied, st2.logs)
	}
	// 22:00 → night, 07:00 → nothing active: that leave is logged
	st2.tick(3 * time.Hour)
	st2.tick(9 * time.Hour)
	if strings.Join(st2.applied, ",") != "night" || !strings.Contains(strings.Join(st2.logs, "\n"), "no window active") {
		t.Errorf("leave: applied %v logs %v", st2.applied, st2.logs)
	}
}

// TestSchedulerNoApplier: a scheduler without an apply function logs the
// wiring error and records no switch instead of panicking.
func TestSchedulerNoApplier(t *testing.T) {
	st := newSchedTest(t, 12, 0)
	st.s.apply = nil
	st.s.Set(schedEntries())
	st.s.evaluate()
	if l := strings.Join(st.logs, "\n"); !strings.Contains(l, "no preset store") {
		t.Errorf("logs: %v", st.logs)
	}
	if status := st.s.Status().(scheduleStatus); status.Last != nil {
		t.Errorf("last: %+v", status.Last)
	}
}

func TestSchedulerSetChangesActiveEntry(t *testing.T) {
	st := newSchedTest(t, 12, 0)
	st.s.Set(schedEntries())
	st.s.evaluate()
	if strings.Join(st.applied, ",") != "office" {
		t.Fatalf("applied %v", st.applied)
	}
	// the same list again (preset apply goes through Reload → Set): no transition
	st.s.Set(schedEntries())
	st.tick(30 * time.Second)
	if len(st.applied) != 1 {
		t.Errorf("unchanged list re-applied: %v", st.applied)
	}
	// the office window now maps to another preset: transition
	changed := schedEntries()
	changed[1].Preset = "cool"
	st.s.Set(changed)
	st.tick(30 * time.Second)
	if strings.Join(st.applied, ",") != "office,cool" {
		t.Errorf("changed entry: applied %v", st.applied)
	}
	// the window is removed: fallback becomes active
	st.s.Set([]schedule.Entry{changed[0], changed[2]})
	st.tick(30 * time.Second)
	if strings.Join(st.applied, ",") != "office,cool,balanced" {
		t.Errorf("removed entry: applied %v", st.applied)
	}
	// an empty list: nothing active, nothing applied
	st.s.Set(nil)
	st.tick(30 * time.Second)
	if len(st.applied) != 3 {
		t.Errorf("empty list applied something: %v", st.applied)
	}
}

func TestSchedulerStatusShape(t *testing.T) {
	st := newSchedTest(t, 12, 0)
	st.s.Set(schedEntries())
	st.s.evaluate()
	b, err := json.Marshal(st.s.Status())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Entries []struct {
			Preset   string   `json:"preset"`
			From     string   `json:"from"`
			To       string   `json:"to"`
			Days     []string `json:"days"`
			Fallback bool     `json:"fallback"`
			Active   bool     `json:"active"`
		} `json:"entries"`
		Active int `json:"active"`
		Next   *struct {
			TS     int64  `json:"ts"`
			Preset string `json:"preset"`
		} `json:"next"`
		Last *struct {
			TS     int64  `json:"ts"`
			Preset string `json:"preset"`
			OK     bool   `json:"ok"`
		} `json:"last"`
		Timezone string `json:"timezone"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 3 || got.Active != 1 || !got.Entries[1].Active || got.Entries[0].Active {
		t.Errorf("entries/active: %s", b)
	}
	if got.Entries[0].Days == nil || len(got.Entries[1].Days) != 5 || got.Entries[1].Days[0] != "mon" || !got.Entries[2].Fallback || got.Entries[2].From != "" {
		t.Errorf("entry fields: %s", b)
	}
	if got.Next == nil || got.Next.Preset != "balanced" || got.Next.TS != time.Date(2026, 9, 14, 18, 0, 0, 0, schedZone).Unix() {
		t.Errorf("next: %s", b)
	}
	if got.Last == nil || !got.Last.OK || got.Last.Preset != "office" || got.Last.TS != st.clock.Unix() {
		t.Errorf("last: %s", b)
	}
	if got.Timezone != "UTC+2 +02:00" {
		t.Errorf("timezone: %q", got.Timezone)
	}
	// without entries: empty list, -1, null next/last
	empty := newSchedTest(t, 12, 0)
	b, _ = json.Marshal(empty.s.Status())
	if s := string(b); !strings.Contains(s, `"entries":[]`) || !strings.Contains(s, `"active":-1`) || !strings.Contains(s, `"next":null`) || !strings.Contains(s, `"last":null`) {
		t.Errorf("empty status: %s", s)
	}
}
