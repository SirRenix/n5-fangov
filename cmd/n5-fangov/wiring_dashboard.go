// wiring_dashboard.go holds the dashboard store behind /api/dashboard
// ([dashboard] sensors) and the reload hook that re-applies [alert] after
// every config write that reaches Service.Reload.
package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/schedule"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// ---------------------------------------------------------------------------
// Dashboard store (web.DashboardStore): [dashboard] sensors.

// watchedSetter is the controller side of the dashboard store
// (*control.Controller; a fake in tests).
type watchedSetter interface {
	SetWatched(ids []string)
	Watched() []string
}

type dashboardStore struct {
	mu      sync.Mutex
	cfgPath string
	pin     func([]byte) []byte
	ctrl    watchedSetter
	factory sensorFactory
}

func newDashboardStore(cfgPath string, ctrl watchedSetter, f sensorFactory, pin func([]byte) []byte) *dashboardStore {
	return &dashboardStore{cfgPath: cfgPath, pin: pin, ctrl: ctrl, factory: f}
}

func (s *dashboardStore) Sensors() []string { return s.ctrl.Watched() }

// SetSensors validates the list (0..MaxDashboardSensors ids, each in a
// known id form — sensor.Parse; an id that is well-formed but has no
// device right now is kept with a warning), writes [dashboard] sensors
// and hands the list to the controller.
func (s *dashboardStore) SetSensors(ids []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if len(clean) > config.MaxDashboardSensors {
		return nil, fmt.Errorf("%d sensors, at most %d allowed", len(clean), config.MaxDashboardSensors)
	}
	var warns []string
	for _, id := range clean {
		if strings.ContainsAny(id, "\"\n\r\\") || strings.Contains(id, "<") {
			return nil, fmt.Errorf("sensor id %q is not valid", id)
		}
		if _, err := s.factory(id); err != nil {
			if !errors.Is(err, sensor.ErrNoDevice) {
				return nil, fmt.Errorf("sensor id %q: %w", id, err)
			}
			warns = append(warns, fmt.Sprintf("%s: %v (kept; charted once the device appears)", id, err))
		}
	}
	err := editConfig(s.cfgPath, s.pin, "dashboard", func(raw []byte) []byte {
		return setConfigKey(raw, "dashboard", "sensors", tomlStringArray(clean))
	}, func(cfg config.Config) bool {
		return slices.Equal(cfg.Dashboard.Sensors, clean)
	})
	if err != nil {
		return nil, err
	}
	s.ctrl.SetWatched(clean)
	if warns == nil {
		warns = []string{}
	}
	return warns, nil
}

// tomlStringArray renders ids as a TOML array of basic strings.
func tomlStringArray(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, tomlString(id))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// ---------------------------------------------------------------------------
// Reload hook: every config text that reaches Service.Reload (PUT
// /api/config, settings import, preset apply) re-applies [alert] and
// hands the [[schedule]] list to the scheduler on success. [dashboard] is
// applied by the controller's own Reload.

type hookedService struct {
	control.Service
	alerts *alertManager
	sched  *scheduler
}

func (h hookedService) Reload(raw []byte) error {
	err := h.Service.Reload(raw)
	if err != nil {
		// ErrRestartRequired: nothing was applied; the restart reads the file.
		return err
	}
	if cfg, _, perr := config.Parse(raw); perr == nil {
		if h.alerts != nil {
			h.alerts.apply(cfg.Alert)
		}
		if h.sched != nil {
			h.sched.Set(schedule.FromConfig(cfg.Schedules))
		}
	}
	return nil
}
