package schedule

import (
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// utc2 is a fixed zone without DST so the plain tests do not depend on
// the tz database.
var utc2 = time.FixedZone("UTC+2", 2*3600)

// at builds a local time in utc2; 2026-09-14 is a Monday.
func at(day, hour, min int) time.Time {
	return time.Date(2026, 9, day, hour, min, 0, 0, utc2)
}

func night() Entry { return Entry{Preset: "night", From: "22:00", To: "07:00"} }
func office() Entry {
	return Entry{Preset: "office", From: "08:00", To: "18:00", Days: []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}}
}
func fallback() Entry { return Entry{Preset: "balanced", Fallback: true} }

func wantActive(t *testing.T, entries []Entry, now time.Time, idx int, ok bool) {
	t.Helper()
	i, o := Active(entries, now)
	if i != idx || o != ok {
		t.Errorf("Active(%s) = %d,%v want %d,%v", now.Format("Mon 02 15:04"), i, o, idx, ok)
	}
}

func TestActivePlainWindow(t *testing.T) {
	e := []Entry{office()}
	wantActive(t, e, at(14, 7, 59), -1, false)
	wantActive(t, e, at(14, 8, 0), 0, true)
	wantActive(t, e, at(14, 12, 30), 0, true)
	wantActive(t, e, at(14, 17, 59), 0, true)
	wantActive(t, e, at(14, 18, 0), -1, false) // to is exclusive
}

func TestActiveMidnightCrossing(t *testing.T) {
	e := []Entry{night()}
	wantActive(t, e, at(14, 21, 59), -1, false)
	wantActive(t, e, at(14, 22, 0), 0, true)
	wantActive(t, e, at(14, 23, 59), 0, true)
	wantActive(t, e, at(15, 0, 0), 0, true)
	wantActive(t, e, at(15, 6, 59), 0, true)
	wantActive(t, e, at(15, 7, 0), -1, false)
	wantActive(t, e, at(15, 12, 0), -1, false)
}

func TestActiveDays(t *testing.T) {
	// office: Mon..Fri; 19.09.2026 is a Saturday
	e := []Entry{office()}
	wantActive(t, e, at(18, 12, 0), 0, true)   // Friday
	wantActive(t, e, at(19, 12, 0), -1, false) // Saturday
	wantActive(t, e, at(20, 12, 0), -1, false) // Sunday
	// a midnight-crossing window belongs to the day from falls in: the
	// Friday-night window covers the early Saturday hours, not Saturday night
	fri := night()
	fri.Days = []time.Weekday{time.Friday}
	e = []Entry{fri}
	wantActive(t, e, at(18, 23, 0), 0, true) // Friday evening
	wantActive(t, e, at(19, 3, 0), 0, true)  // Saturday 03:00 (Friday's window)
	wantActive(t, e, at(19, 23, 0), -1, false)
	wantActive(t, e, at(20, 3, 0), -1, false)
}

func TestActiveFallbackAndOrder(t *testing.T) {
	e := []Entry{fallback(), night(), office()}
	wantActive(t, e, at(14, 12, 0), 2, true) // office
	wantActive(t, e, at(14, 23, 0), 1, true) // night
	wantActive(t, e, at(14, 19, 0), 0, true) // fallback
	// overlapping windows: the first windowed entry wins
	early := Entry{Preset: "early", From: "06:00", To: "09:00"}
	e = []Entry{office(), early}
	wantActive(t, e, at(14, 8, 30), 0, true)
	wantActive(t, e, at(14, 6, 30), 1, true)
	// no entries, only a fallback
	wantActive(t, nil, at(14, 12, 0), -1, false)
	wantActive(t, []Entry{fallback()}, at(14, 12, 0), 0, true)
}

func TestNext(t *testing.T) {
	e := []Entry{night(), office()}
	// inside the office window → next change is 18:00 (nothing active)
	nx, idx, ok := Next(e, at(14, 12, 0))
	if !ok || idx != -1 || !nx.Equal(at(14, 18, 0)) {
		t.Errorf("Next from Mon 12:00: %s %d %v", nx, idx, ok)
	}
	// gap in the evening → 22:00 night
	nx, idx, ok = Next(e, at(14, 19, 0))
	if !ok || idx != 0 || !nx.Equal(at(14, 22, 0)) {
		t.Errorf("Next from Mon 19:00: %s %d %v", nx, idx, ok)
	}
	// inside the night window across midnight → 07:00 next day
	nx, idx, ok = Next(e, at(14, 23, 30))
	if !ok || idx != -1 || !nx.Equal(at(15, 7, 0)) {
		t.Errorf("Next from Mon 23:30: %s %d %v", nx, idx, ok)
	}
	// with a fallback the switch at 07:00 goes to the fallback
	f := []Entry{night(), fallback()}
	nx, idx, ok = Next(f, at(14, 23, 30))
	if !ok || idx != 1 || !nx.Equal(at(15, 7, 0)) {
		t.Errorf("Next with fallback: %s %d %v", nx, idx, ok)
	}
	// across a week: a Sunday-only window seen from Monday
	sun := Entry{Preset: "sunday", From: "10:00", To: "12:00", Days: []time.Weekday{time.Sunday}}
	nx, idx, ok = Next([]Entry{sun}, at(14, 12, 0))
	if !ok || idx != 0 || !nx.Equal(at(20, 10, 0)) {
		t.Errorf("Next across the week: %s %d %v", nx, idx, ok)
	}
	// nothing ever changes: fallback only, or no entries
	if _, _, ok := Next([]Entry{fallback()}, at(14, 12, 0)); ok {
		t.Errorf("Next with only a fallback reported a change")
	}
	if _, _, ok := Next(nil, at(14, 12, 0)); ok {
		t.Errorf("Next without entries reported a change")
	}
}

func TestNextDST(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("tz database unavailable: %v", err)
	}
	// spring forward 2026-03-29: 02:00 CET → 03:00 CEST
	cet := time.FixedZone("CET", 3600)
	e := []Entry{night()}
	from := time.Date(2026, 3, 29, 1, 30, 0, 0, berlin)
	wantActive(t, e, from, 0, true)
	nx, idx, ok := Next(e, from)
	want := time.Date(2026, 3, 29, 7, 0, 0, 0, berlin)
	if !ok || idx != -1 || !nx.Equal(want) {
		t.Errorf("Next over the DST gap: %s %d %v, want %s", nx, idx, ok, want)
	}
	if _, off := want.Zone(); off != 2*3600 {
		t.Errorf("07:00 on the DST day is not CEST: offset %d", off)
	}
	// a window starting in the skipped hour begins at the jump instant
	gap := []Entry{{Preset: "gap", From: "02:30", To: "05:00"}}
	nx, idx, ok = Next(gap, time.Date(2026, 3, 29, 1, 0, 0, 0, berlin))
	jump := time.Date(2026, 3, 29, 2, 0, 0, 0, cet) // = 03:00 CEST
	if !ok || idx != 0 || !nx.Equal(jump) {
		t.Errorf("Next into the skipped hour: %s %d %v, want %s", nx, idx, ok, jump.In(berlin))
	}
	// fall back 2026-10-25: 03:00 CEST → 02:00 CET; the night window is
	// simply an hour longer, the next change is 07:00 CET
	from = time.Date(2026, 10, 25, 1, 30, 0, 0, berlin)
	nx, idx, ok = Next(e, from)
	want = time.Date(2026, 10, 25, 7, 0, 0, 0, berlin)
	if !ok || idx != -1 || !nx.Equal(want) || want.Sub(from) != 6*time.Hour+30*time.Minute {
		t.Errorf("Next over the repeated hour: %s %d %v, want %s (%s later)", nx, idx, ok, want, want.Sub(from))
	}
}

func TestFromConfigAndEqual(t *testing.T) {
	in := []config.Schedule{
		{Preset: "night", From: "22:00", To: "07:00", Days: []string{"fri", "sat"}},
		{Preset: "day"},
	}
	got := FromConfig(in)
	if len(got) != 2 || got[0].Preset != "night" || got[0].Fallback || len(got[0].Days) != 2 || got[0].Days[0] != time.Friday || got[0].Days[1] != time.Saturday {
		t.Errorf("FromConfig: %+v", got)
	}
	if !got[1].Fallback || got[1].Days != nil {
		t.Errorf("fallback: %+v", got[1])
	}
	if !Equal(got[0], got[0]) || Equal(got[0], got[1]) {
		t.Errorf("Equal")
	}
	b := got[0]
	b.Days = []time.Weekday{time.Friday, time.Sunday}
	if Equal(got[0], b) {
		t.Errorf("Equal ignores the days")
	}
	// the days are a set: order and repetition do not make another entry
	b.Days = []time.Weekday{time.Saturday, time.Friday}
	if !Equal(got[0], b) {
		t.Errorf("Equal depends on the day order: %v vs %v", got[0].Days, b.Days)
	}
	b.Days = []time.Weekday{time.Saturday, time.Friday, time.Saturday}
	if !Equal(got[0], b) {
		t.Errorf("Equal counts a repeated day: %v vs %v", got[0].Days, b.Days)
	}
	b.Days = []time.Weekday{time.Friday}
	if Equal(got[0], b) {
		t.Errorf("Equal ignores a missing day")
	}
	if !Equal(Entry{Preset: "x"}, Entry{Preset: "x", Days: nil}) || !Equal(Entry{Preset: "x", Days: []time.Weekday{}}, Entry{Preset: "x"}) {
		t.Errorf("Equal: empty day lists")
	}
	if DayName(time.Wednesday) != "wed" || DayName(time.Sunday) != "sun" {
		t.Errorf("DayName")
	}
}
