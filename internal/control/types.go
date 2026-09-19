// Package control implements the regulation loop (see DESIGN.md "Controller").
package control

import (
	"errors"
	"time"

	"github.com/SirRenix/n5-fangov/internal/history"
)

// Status of the last regulation cycle (Snapshot.Status). The dashboard
// keys its status classes on the same strings.
type Status string

const (
	StatusStarting    Status = "starting"     // no cycle finished yet
	StatusOK          Status = "ok"           // every channel read and written
	StatusWriteError  Status = "write-error"  // at least one pwm write failed this cycle
	StatusSensorError Status = "sensor-error" // every channel's sensor failed
	StatusDryRun      Status = "dry-run"      // --dry-run: ok, nothing written
)

// Mode of a channel in the current cycle.
type Mode string

const (
	ModeAuto     Mode = "auto"
	ModeManual   Mode = "manual"
	ModeCritical Mode = "critical"
	ModeStall    Mode = "stall"
	ModeSensor   Mode = "sensor-error"
	ModeFailsafe Mode = "failsafe"
)

// ChannelState is the per-channel part of a Snapshot.
type ChannelState struct {
	Name   string  `json:"name"`
	PWM    int     `json:"pwm"`
	Sensor string  `json:"sensor"`
	Temp   float64 `json:"temp"`   // degrees C, NaN-free (-999 when unknown)
	Duty   int     `json:"duty"`   // last written / current; -1 = unknown (write failed)
	Target int     `json:"target"` // curve/override target before slew
	RPM    int     `json:"rpm"`    // -1 when no tach
	Mode   Mode    `json:"mode"`
	// HeldTemp is the hysteresis-held temperature the curve was evaluated
	// at, present only with hysteresis > 0 and when it differs from Temp.
	HeldTemp float64 `json:"held_temp,omitempty"`
	// HoldUntil is the unix time a running min_on hold ends; 0 = no hold.
	HoldUntil int64 `json:"hold_until,omitempty"`
	// Ceiling is the effective ceiling in degrees C (the sensor's built-in
	// one, lowered by [[channel]] ceiling); CeilingHit reports the ceiling
	// state: 255 / mode critical until the reading is 3 degrees below it.
	Ceiling    int  `json:"ceiling"`
	CeilingHit bool `json:"ceiling_hit"`
}

// Snapshot is the daemon state exposed to CLI and web.
type Snapshot struct {
	TS         int64              `json:"ts"`
	Status     Status             `json:"status"` // starting | ok | sensor-error | write-error | dry-run
	Profile    string             `json:"profile"`
	Verified   bool               `json:"verified"`
	HwmonPath  string             `json:"hwmon_path"`
	DryRun     bool               `json:"dry_run"`
	Channels   []ChannelState     `json:"channels"`
	ExtraTemps map[string]float64 `json:"extra_temps"`
	// Watched carries the live values of the [dashboard].sensors ids that
	// could be read this cycle (v0.3); never nil.
	Watched map[string]float64 `json:"watched"`
	Alerts  map[string]int64   `json:"alerts"` // type → unix ts of last alert
	Uptime  int64              `json:"uptime_s"`
}

// HistoryPoint is one history entry for the charts: {ts, temp{}, duty{},
// rpm{}, extra{}} by channel name (extra by sensor id). The type lives in
// internal/history (the store); the alias keeps the controller API.
type HistoryPoint = history.Point

// Service is what the web/ipc layer needs from the controller.
type Service interface {
	Snapshot() Snapshot
	// History returns the raw tier (one point per cycle) not older than since.
	History(since time.Duration) []HistoryPoint
	// HistoryRange returns the tier for span (raw ≤ 2 h, 1-min means ≤ 24 h,
	// else 5-min means) with ts > since; see history.Store.Range.
	HistoryRange(span time.Duration, since int64) []HistoryPoint
	SetOverride(channel string, duty int) error
	ClearOverride(channel string) error
	// Reload applies a new config; returns ErrRestartRequired when the channel
	// set changed and the daemon must be restarted.
	Reload(rawTOML []byte) error
}

// ErrRestartRequired is returned by Reload when the channel set or the
// profile changed: nothing was applied, the restart reads the file.
var ErrRestartRequired = errors.New("restart required: channel set or profile changed")
