// Package control implements the regulation loop (see DESIGN.md "Controller").
package control

import "time"

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
	Duty   int     `json:"duty"`   // last written / current
	Target int     `json:"target"` // curve/override target before slew
	RPM    int     `json:"rpm"`    // -1 when no tach
	Mode   Mode    `json:"mode"`
}

// Snapshot is the daemon state exposed to CLI and web.
type Snapshot struct {
	TS         int64              `json:"ts"`
	Status     string             `json:"status"` // ok | sensor-error | write-error | dry-run
	Profile    string             `json:"profile"`
	Verified   bool               `json:"verified"`
	HwmonPath  string             `json:"hwmon_path"`
	DryRun     bool               `json:"dry_run"`
	Channels   []ChannelState     `json:"channels"`
	ExtraTemps map[string]float64 `json:"extra_temps"`
	Alerts     map[string]int64   `json:"alerts"` // type → unix ts of last alert
	Uptime     int64              `json:"uptime_s"`
}

// HistoryPoint is one ring-buffer entry for charts.
type HistoryPoint struct {
	TS   int64              `json:"ts"`
	Temp map[string]float64 `json:"temp"` // by channel name
	Duty map[string]int     `json:"duty"`
	RPM  map[string]int     `json:"rpm"`
}

// Service is what the web/ipc layer needs from the controller.
type Service interface {
	Snapshot() Snapshot
	History(since time.Duration) []HistoryPoint
	SetOverride(channel string, duty int) error
	ClearOverride(channel string) error
	// Reload applies a new config; returns ErrRestartRequired when the channel
	// set changed and the daemon must be restarted.
	Reload(rawTOML []byte) error
}

// ErrRestartRequired is returned by Reload when a restart is needed.
type restartRequired struct{}

func (restartRequired) Error() string { return "restart required: channel set or profile changed" }

// ErrRestartRequired sentinel.
var ErrRestartRequired error = restartRequired{}