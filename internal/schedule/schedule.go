// Package schedule evaluates the [[schedule]] tables (DESIGN.md
// "Schedules"): which entry is active at a moment and when the active
// entry changes next. Windows are wall-clock times in the zone of the
// time passed in (the daemon passes time.Local); the scheduler in cmd
// applies the presets.
package schedule

import (
	"sort"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// Entry is one schedule entry. A windowed entry applies in [From, To) on
// the listed days (empty = every day); a window with To < From crosses
// midnight and belongs to the day From falls in. The fallback (no window)
// applies whenever no window matches.
type Entry struct {
	Preset   string
	From, To string // "HH:MM"; empty for the fallback
	Days     []time.Weekday
	Fallback bool
}

// Lookahead is how far Next searches for a change.
const Lookahead = 8 * 24 * time.Hour

// weekdays maps the config day names to time.Weekday.
var weekdays = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
	"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

// DayName returns the config name of a weekday ("mon".."sun").
func DayName(d time.Weekday) string {
	return strings.ToLower(d.String()[:3])
}

// FromConfig converts parsed [[schedule]] tables (already validated by
// config.Parse) into entries.
func FromConfig(in []config.Schedule) []Entry {
	out := make([]Entry, 0, len(in))
	for _, s := range in {
		e := Entry{Preset: s.Preset, From: s.From, To: s.To, Fallback: s.Fallback()}
		for _, d := range s.Days {
			if wd, ok := weekdays[d]; ok {
				e.Days = append(e.Days, wd)
			}
		}
		out = append(out, e)
	}
	return out
}

// Equal reports whether two entries describe the same window and preset.
func Equal(a, b Entry) bool {
	if a.Preset != b.Preset || a.From != b.From || a.To != b.To || a.Fallback != b.Fallback || len(a.Days) != len(b.Days) {
		return false
	}
	for i := range a.Days {
		if a.Days[i] != b.Days[i] {
			return false
		}
	}
	return true
}

// minutes parses "HH:MM" into minutes of the day; -1 when malformed.
func minutes(s string) int {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return -1
	}
	return t.Hour()*60 + t.Minute()
}

func (e Entry) onDay(d time.Weekday) bool {
	if len(e.Days) == 0 {
		return true
	}
	for _, x := range e.Days {
		if x == d {
			return true
		}
	}
	return false
}

// contains reports whether the window holds now (minute granularity, the
// wall clock of now's location).
func (e Entry) contains(now time.Time) bool {
	if e.Fallback {
		return false
	}
	from, to := minutes(e.From), minutes(e.To)
	if from < 0 || to < 0 || from == to {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	if from < to {
		return m >= from && m < to && e.onDay(now.Weekday())
	}
	// crosses midnight: the evening part on the window's day, the early
	// hours on the following day
	if m >= from {
		return e.onDay(now.Weekday())
	}
	if m < to {
		return e.onDay(now.AddDate(0, 0, -1).Weekday())
	}
	return false
}

// Active returns the first windowed entry that contains now, else the
// fallback, else ok = false.
func Active(entries []Entry, now time.Time) (idx int, ok bool) {
	fallback := -1
	for i, e := range entries {
		if e.Fallback {
			if fallback < 0 {
				fallback = i
			}
			continue
		}
		if e.contains(now) {
			return i, true
		}
	}
	if fallback >= 0 {
		return fallback, true
	}
	return -1, false
}

// Next returns the next moment within Lookahead at which the active entry
// changes, with the entry active from then on (idx -1 when none) and ok =
// true; ok = false when nothing changes. Candidates are every window
// boundary and every full hour of the coming days in now's location, so a
// change caused by a DST transition (a boundary in the skipped or the
// repeated hour) is found at the hour mark.
func Next(entries []Entry, now time.Time) (at time.Time, idx int, ok bool) {
	curIdx, curOK := Active(entries, now)
	loc := now.Location()
	y, mo, d := now.Date()
	var cands []time.Time
	days := int(Lookahead / (24 * time.Hour))
	for day := 0; day <= days; day++ {
		for h := 0; h < 24; h++ {
			cands = append(cands, time.Date(y, mo, d+day, h, 0, 0, 0, loc))
		}
		for _, e := range entries {
			if e.Fallback {
				continue
			}
			for _, s := range []string{e.From, e.To} {
				if m := minutes(s); m >= 0 {
					cands = append(cands, time.Date(y, mo, d+day, m/60, m%60, 0, 0, loc))
				}
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Before(cands[j]) })
	limit := now.Add(Lookahead)
	for _, t := range cands {
		if !t.After(now) || t.After(limit) {
			continue
		}
		i, o := Active(entries, t)
		if i != curIdx || o != curOK {
			return t, i, true
		}
	}
	return time.Time{}, -1, false
}
