package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func parseSchedules(t *testing.T, raw string) ([]Schedule, []Warning) {
	t.Helper()
	cfg, warns, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg.Schedules, warns
}

func TestScheduleParse(t *testing.T) {
	scheds, warns := parseSchedules(t, `
[[schedule]]
preset = "n5pro-quiet"
from = "22:00"
to = "07:00"
days = ["Mon", "tue ", "sun"]

[[schedule]]
preset = "n5pro-cool"
from = "7:30"
to = "18:00"

[[schedule]]
preset = "n5pro-balanced"
`)
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
	want := []Schedule{
		{Preset: "n5pro-quiet", From: "22:00", To: "07:00", Days: []string{"mon", "tue", "sun"}},
		{Preset: "n5pro-cool", From: "07:30", To: "18:00"},
		{Preset: "n5pro-balanced"},
	}
	if !reflect.DeepEqual(scheds, want) {
		t.Errorf("schedules:\n got %+v\nwant %+v", scheds, want)
	}
	if !scheds[2].Fallback() || scheds[0].Fallback() {
		t.Errorf("fallback flags: %+v", scheds)
	}
}

func TestScheduleAbsent(t *testing.T) {
	scheds, warns := parseSchedules(t, good)
	if scheds != nil || len(warns) != 0 {
		t.Errorf("no [[schedule]]: %v %v", scheds, warns)
	}
}

func TestScheduleInvalidEntries(t *testing.T) {
	for _, c := range []struct {
		name, raw, field string
	}{
		{"no preset", `[[schedule]]
from = "22:00"
to = "07:00"`, "schedule[0].preset"},
		{"bad preset", `[[schedule]]
preset = "Night Mode"
from = "22:00"
to = "07:00"`, "schedule[0].preset"},
		{"only from", `[[schedule]]
preset = "night"
from = "22:00"`, "schedule[0]"},
		{"only to", `[[schedule]]
preset = "night"
to = "22:00"`, "schedule[0]"},
		{"bad from", `[[schedule]]
preset = "night"
from = "25:00"
to = "07:00"`, "schedule[0].from"},
		{"bad to", `[[schedule]]
preset = "night"
from = "22:00"
to = "7pm"`, "schedule[0].to"},
		{"from == to", `[[schedule]]
preset = "night"
from = "22:00"
to = "22:00"`, "schedule[0]"},
		{"unknown day", `[[schedule]]
preset = "night"
from = "22:00"
to = "07:00"
days = ["mon", "monday"]`, "schedule[0].days"},
		{"days not strings", `[[schedule]]
preset = "night"
from = "22:00"
to = "07:00"
days = [1, 2]`, "schedule[0].days"},
	} {
		t.Run(c.name, func(t *testing.T) {
			scheds, warns := parseSchedules(t, c.raw)
			if len(scheds) != 0 {
				t.Errorf("entry kept: %+v", scheds)
			}
			hasWarn(t, warns, c.field)
		})
	}
}

func TestScheduleOthersStay(t *testing.T) {
	// rule 8: the invalid entry is dropped, the valid ones keep their order
	scheds, warns := parseSchedules(t, `
[[schedule]]
preset = "a"
from = "08:00"
to = "12:00"

[[schedule]]
preset = "b"
from = "12:00"

[[schedule]]
preset = "c"
from = "12:00"
to = "18:00"
`)
	hasWarn(t, warns, "schedule[1]")
	if len(scheds) != 2 || scheds[0].Preset != "a" || scheds[1].Preset != "c" {
		t.Errorf("schedules: %+v", scheds)
	}
}

func TestScheduleSecondFallbackDropped(t *testing.T) {
	scheds, warns := parseSchedules(t, `
[[schedule]]
preset = "a"

[[schedule]]
preset = "b"
`)
	hasWarn(t, warns, "schedule[1]")
	if len(scheds) != 1 || scheds[0].Preset != "a" {
		t.Errorf("schedules: %+v", scheds)
	}
}

func TestScheduleDuplicateDayAndUnknownKey(t *testing.T) {
	scheds, warns := parseSchedules(t, `
[[schedule]]
preset = "a"
from = "08:00"
to = "12:00"
days = ["mon", "mon", "fri"]
colour = "blue"
`)
	hasWarn(t, warns, "schedule[0].days")
	hasWarn(t, warns, "schedule[0].colour")
	if len(scheds) != 1 || !reflect.DeepEqual(scheds[0].Days, []string{"mon", "fri"}) {
		t.Errorf("schedules: %+v", scheds)
	}
}

func TestScheduleMax(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxSchedules+2; i++ {
		fmt.Fprintf(&b, "[[schedule]]\npreset = \"p%d\"\nfrom = \"08:00\"\nto = \"09:00\"\n\n", i)
	}
	scheds, warns := parseSchedules(t, b.String())
	if len(scheds) != MaxSchedules {
		t.Errorf("%d entries kept, want %d", len(scheds), MaxSchedules)
	}
	hasWarn(t, warns, fmt.Sprintf("schedule[%d]", MaxSchedules))
}

func TestScheduleNotArray(t *testing.T) {
	scheds, warns := parseSchedules(t, "[schedule]\npreset = \"a\"\n")
	if len(scheds) != 0 {
		t.Errorf("schedules: %+v", scheds)
	}
	hasWarn(t, warns, "schedule")
}

func TestScheduleRoundTripAndClone(t *testing.T) {
	cfg := Default()
	cfg.Schedules = []Schedule{
		{Preset: "night", From: "22:00", To: "07:00", Days: []string{"fri", "sat"}},
		{Preset: "day"},
	}
	back, warns, err := Parse(Marshal(cfg))
	if err != nil || len(warns) != 0 {
		t.Fatalf("round trip: %v %v", err, warns)
	}
	if !reflect.DeepEqual(back.Schedules, cfg.Schedules) {
		t.Errorf("round trip:\n got %+v\nwant %+v", back.Schedules, cfg.Schedules)
	}
	// no schedules: Marshal emits no [[schedule]] table
	if strings.Contains(string(Marshal(Default())), "schedule") {
		t.Errorf("Marshal(Default()) carries a schedule table")
	}
	cl := cfg.Clone()
	cl.Schedules[0].Days[0] = "mon"
	cl.Schedules[1].Preset = "other"
	if cfg.Schedules[0].Days[0] != "fri" || cfg.Schedules[1].Preset != "day" {
		t.Errorf("Clone shares the schedule slice: %+v", cfg.Schedules)
	}
}
