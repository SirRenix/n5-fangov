package control

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// v0.3: watched dashboard sensors are read per cycle into
// Snapshot.Watched and HistoryPoint.Extra, never touch regulation, and
// follow SetWatched / Reload without a restart.

func TestWatchedSensors(t *testing.T) {
	cfg := n5cfg()
	cfg.Daemon.StaleCycles = 600 // constant fake k10temp must not trip stale detection over 60+ cycles
	cfg.Dashboard.Sensors = []string{"hwmon:amdgpu:temp1", "nvme:max", "hwmon:missing:temp1"}
	h := newHarness(t, cfg, nil)
	h.sensors.add("hwmon:amdgpu:temp1", 51500)
	// the harness resolved at New: amdgpu was not there yet → unresolved,
	// nvme:max resolved, missing never resolves
	if !h.log.contains(`dashboard sensor "hwmon:missing:temp1"`) {
		t.Errorf("unresolvable id must be logged once: %v", h.log.lines)
	}
	if got := h.c.Snapshot().Watched; got == nil || len(got) != 0 {
		t.Errorf("before the first cycle Watched must be an empty map: %#v", got)
	}
	h.cycles(1)
	s := h.c.Snapshot()
	if !reflect.DeepEqual(s.Watched, map[string]float64{"nvme:max": 44}) {
		t.Errorf("watched after cycle 1: %v", s.Watched)
	}
	hist := h.c.History(24 * time.Hour)
	if len(hist) != 1 || !reflect.DeepEqual(hist[0].Extra, map[string]float64{"nvme:max": 44}) {
		t.Errorf("history extra: %+v", hist)
	}
	// regulation unaffected: cpu still follows k10temp
	h.expectDuty("cpu", 85)
	h.expectMode("cpu", ModeAuto)

	// a read error drops the id for that cycle only
	h.sensors.get("nvme:max").fail(errors.New("gone"))
	h.cycles(1)
	if got := h.c.Snapshot().Watched; len(got) != 0 {
		t.Errorf("read error must leave the id out: %v", got)
	}
	if last := h.c.History(24 * time.Hour); last[len(last)-1].Extra != nil {
		t.Errorf("no values → Extra omitted: %+v", last[len(last)-1])
	}
	h.sensors.get("nvme:max").set(45000)

	// unresolved ids are retried every resolveEvery cycles: amdgpu
	// appeared after New and is picked up at the next resolve round
	h.cycles(resolveEvery - 2)
	if got := h.c.Snapshot().Watched; got["hwmon:amdgpu:temp1"] != 0 {
		t.Errorf("amdgpu must not be resolved before the resolve round: %v", got)
	}
	h.cycles(1)
	if got := h.c.Snapshot().Watched; got["hwmon:amdgpu:temp1"] != 51.5 || got["nvme:max"] != 45 {
		t.Errorf("after the resolve round: %v", got)
	}

	// implausible values are dropped
	h.sensors.get("hwmon:amdgpu:temp1").set(MaxPlausible + 1)
	h.cycles(1)
	if got := h.c.Snapshot().Watched; got["hwmon:amdgpu:temp1"] != 0 || len(got) != 1 {
		t.Errorf("implausible must be dropped: %v", got)
	}
}

func TestSetWatchedAndReload(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.sensors.add("ec:system", 32000)
	h.cycles(1)
	if got := h.c.Snapshot().Watched; len(got) != 0 {
		t.Errorf("nothing watched by default: %v", got)
	}
	h.c.SetWatched([]string{" ec:system ", "", "ec:system", "k10temp"})
	if got := h.c.Watched(); !reflect.DeepEqual(got, []string{"ec:system", "k10temp"}) {
		t.Errorf("Watched (trimmed, deduplicated): %v", got)
	}
	h.cycles(1)
	if got := h.c.Snapshot().Watched; got["ec:system"] != 32 || got["k10temp"] != 36 {
		t.Errorf("SetWatched applied on the next cycle: %v", got)
	}
	// the active config carries the list, so Reload with the same file is a no-op
	cfg := h.c.Config()
	if !reflect.DeepEqual(cfg.Dashboard.Sensors, []string{"ec:system", "k10temp"}) {
		t.Errorf("config must carry the watched list: %v", cfg.Dashboard.Sensors)
	}
	if err := h.c.Reload(config.Marshal(cfg)); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	if h.c.watchDirty {
		t.Errorf("unchanged list must not mark the watch list dirty")
	}
	// Reload with a different list applies it
	cfg.Dashboard.Sensors = []string{"k10temp"}
	if err := h.c.Reload(config.Marshal(cfg)); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	if got := h.c.Snapshot().Watched; !reflect.DeepEqual(got, map[string]float64{"k10temp": 36}) {
		t.Errorf("reload must replace the list: %v", got)
	}
	// SetWatched between Apply and the cycle wins over the pending config
	cfg.Dashboard.Sensors = []string{"ec:system"}
	if err := h.c.Reload(config.Marshal(cfg)); err != nil {
		t.Fatal(err)
	}
	h.c.SetWatched(nil)
	h.cycles(1)
	if got := h.c.Snapshot().Watched; len(got) != 0 || len(h.c.Watched()) != 0 {
		t.Errorf("SetWatched(nil) after Reload: %v %v", got, h.c.Watched())
	}
	if last := h.c.History(24 * time.Hour); last[len(last)-1].Extra != nil {
		t.Errorf("empty list → no Extra: %+v", last[len(last)-1])
	}
}
