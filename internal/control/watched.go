package control

import (
	"strings"
)

// Watched dashboard sensors (DESIGN "Dashboard sensors", v0.3): the ids of
// [dashboard].sensors are read once per cycle after the channel sensors,
// purely for the chart — they never influence regulation. A read error
// leaves the id out of that cycle's values; an id the factory cannot
// resolve is skipped with one warning line and retried every
// resolveEvery cycles (a drive that spins up later, a module loaded
// later).

// watchedSensor is one watched id with its reader (nil while unresolved).
type watchedSensor struct {
	id     string
	sensor SensorReader
}

// buildWatched resolves ids through the sensor factory. Called by New and
// by the loop at the start of a cycle (with c.mu held).
func (c *Controller) buildWatched(ids []string) []*watchedSensor {
	out := make([]*watchedSensor, 0, len(ids))
	for _, id := range ids {
		w := &watchedSensor{id: id}
		w.sensor = c.resolveWatched(id)
		out = append(out, w)
	}
	return out
}

func (c *Controller) resolveWatched(id string) SensorReader {
	s, err := c.newS(id)
	if err != nil {
		c.logOnce("watch."+id, "dashboard sensor %q: %v (skipped, retried periodically)", id, err)
		return nil
	}
	c.logClear("watch." + id)
	return s
}

// readWatched reads every watched sensor; unreadable or implausible ones
// are absent from the result. Unresolved ids are retried every
// resolveEvery cycles. Loop goroutine only.
func (c *Controller) readWatched(watched []*watchedSensor, n int) map[string]float64 {
	if len(watched) == 0 {
		return nil
	}
	out := make(map[string]float64, len(watched))
	for _, w := range watched {
		if w.sensor == nil {
			if n == 0 || n%resolveEvery != 0 {
				continue
			}
			if w.sensor = c.resolveWatched(w.id); w.sensor == nil {
				continue
			}
		}
		v, err := w.sensor.Read()
		if err != nil || v < MinPlausible || v > MaxPlausible {
			continue
		}
		out[w.id] = float64(v) / 1000
	}
	return out
}

// SetWatched replaces the watched id list (PUT /api/dashboard). The list
// is stored in the active config (so a later Reload with the same file
// content is a no-op) and rebuilt by the loop at the next cycle. ids are
// trimmed and de-duplicated; the caller validates count and syntax.
func (c *Controller) SetWatched(ids []string) {
	clean := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.Dashboard.Sensors = clean
	if c.pending != nil {
		c.pending.Dashboard.Sensors = append([]string{}, clean...)
	}
	c.watchDirty = true
	c.log.Printf("dashboard sensors: %s (applied on next cycle)", joinOrNone(clean))
}

// Watched returns the configured watched ids (a copy).
func (c *Controller) Watched() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.cfg.Dashboard.Sensors...)
}

func joinOrNone(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
