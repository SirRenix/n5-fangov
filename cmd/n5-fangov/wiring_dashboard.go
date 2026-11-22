// wiring_dashboard.go holds the dashboard store behind /api/dashboard
// ([dashboard] sensors) and the reload hook that re-applies [alert] after
// every config write that reaches Service.Reload.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/schedule"
	"github.com/SirRenix/n5-fangov/internal/sensor"
	"github.com/SirRenix/n5-fangov/internal/web"
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
	err := editConfigThen(s.cfgPath, s.pin, "dashboard", func(raw []byte) []byte {
		return setConfigKey(raw, "dashboard", "sensors", tomlStringArray(clean))
	}, func(cfg config.Config) bool {
		return slices.Equal(cfg.Dashboard.Sensors, clean)
	}, func() error {
		s.ctrl.SetWatched(clean) // under the file lock (editConfigThen)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if warns == nil {
		warns = []string{}
	}
	return warns, nil
}

// ---------------------------------------------------------------------------
// Assist store (web.AssistStore): the enabled line of one [[assist]] table.

// assistSetter is the controller side of the assist store
// (*control.Controller; a fake in tests).
type assistSetter interface {
	ApplyAssists(tables []config.Assist, want string) error
}

type assistStore struct {
	mu      sync.Mutex
	cfgPath string
	pin     func([]byte) []byte
	ctrl    assistSetter
}

func newAssistStore(cfgPath string, ctrl assistSetter, pin func([]byte) []byte) *assistStore {
	return &assistStore{cfgPath: cfgPath, pin: pin, ctrl: ctrl}
}

// SetEnabled writes enabled = on into the channel's [[assist]] table
// (config.SetAssistEnabled: that line only, comments kept) and hands the
// file's [[assist]] tables to the controller, which takes them on the next
// cycle — both under the config file lock (editConfigThen), so a
// concurrent reload cannot apply an older file text afterwards. Handing
// over all tables, not only the switch, lets a table added, changed or
// removed by hand take effect with the switch (BACKLOG #63: a new table
// used to need a restart and the switch answered 500). A file that does
// not parse is a plain error (400, like the other single-key stores); a
// channel whose file has no valid [[assist]] table is fs.ErrNotExist
// (404). A table that does not fit the running channel set (the channels
// were edited by hand too) is a web.ReloadError (500); the daemon then
// applies nothing, the file keeps the switch until a restart.
func (s *assistStore) SetEnabled(channel string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pre error
	var written []config.Assist
	err := editConfigThen(s.cfgPath, s.pin, "assist", func(raw []byte) []byte {
		cfg, _, perr := config.Parse(raw)
		switch {
		case perr != nil:
			pre = fmt.Errorf("config does not parse, nothing written: %w", perr)
			return nil
		case cfg.Assist(channel) == nil:
			pre = fmt.Errorf("channel %q has no valid [[assist]] table in %s: %w", channel, s.cfgPath, fs.ErrNotExist)
			return nil
		}
		out, found := config.SetAssistEnabled(raw, channel, on)
		if !found {
			return []byte{} // a layout the line editor cannot find: verify refuses it
		}
		return out
	}, func(cfg config.Config) bool {
		a := cfg.Assist(channel)
		written = cfg.Assists
		return a != nil && a.Enabled == on
	}, func() error {
		if err := s.ctrl.ApplyAssists(written, channel); err != nil {
			return &web.ReloadError{Err: fmt.Errorf("config written, not applied: %w", err)}
		}
		return nil
	})
	if pre != nil {
		return pre
	}
	return err
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
// hands the [[schedule]] list to the scheduler — on success and on
// control.ErrRestartRequired alike: the controller then applied nothing,
// but the file is written and is the truth for the two sections that
// need no restart (otherwise the alert manager would merge the next
// PUT /api/alerts on a stale section and the scheduler would run the old
// windows until the restart). [dashboard] is applied by the controller's
// own Reload.
//
// With cfgPath set the hook takes configFileMu and reloads the file as it
// is now, not the text the caller wrote earlier: the writers release the
// lock between their write and the reload (preset apply, PUT /api/config,
// import), and a single-key store (the assist switch, the dashboard
// sensors, the alert transport) may write and apply in between — a reload
// of the older text would set the daemon back while the file keeps the
// newer value. Holding the lock across the reload orders every reload and
// every store apply by their file writes. Lock order: configFileMu, then
// the controller's, the alert manager's and the scheduler's locks; none of
// them takes configFileMu. An unreadable file falls back to raw.

type hookedService struct {
	control.Service
	alerts  *alertManager
	sched   *scheduler
	cfgPath string
}

func (h hookedService) Reload(raw []byte) error {
	if h.cfgPath != "" {
		configFileMu.Lock()
		defer configFileMu.Unlock()
		if cur, err := os.ReadFile(h.cfgPath); err == nil {
			raw = cur
		}
	}
	err := h.Service.Reload(raw)
	if err != nil && !isRestartRequired(err) {
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
	return err
}
