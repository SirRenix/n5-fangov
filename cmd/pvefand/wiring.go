//go:build integrate

// wiring.go isolates every call into a sibling package whose exact signature
// was not fixed in DESIGN.md. All other files in cmd/pvefand use only the
// local types and functions defined here, so an API mismatch at integration
// time is fixed in exactly one place. Every assumption is marked INTEGRATE.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/SirRenix/pvefand/internal/alert"
	"github.com/SirRenix/pvefand/internal/config"
	"github.com/SirRenix/pvefand/internal/control"
	"github.com/SirRenix/pvefand/internal/hwmon"
	"github.com/SirRenix/pvefand/internal/ipc"
	"github.com/SirRenix/pvefand/internal/profile"
	"github.com/SirRenix/pvefand/internal/sdnotify"
	"github.com/SirRenix/pvefand/internal/sensor"
	"github.com/SirRenix/pvefand/internal/version"
	"github.com/SirRenix/pvefand/internal/web"
)

// ---------------------------------------------------------------------------
// Local views on the config (so the CLI never touches config.Config fields
// directly).

// chanSpec is one [[channel]] table.
type chanSpec struct {
	Name     string
	PWM      int
	Sensor   string
	Curve    [][2]int // [temp_c, duty], ascending
	Critical int
	Stop     string // "auto" or fixed duty "0".."255"
}

// daemonSpec is the subset of [daemon] the commands need.
type daemonSpec struct {
	Interval time.Duration
	Profile  string // auto | n5pro | nct67xx | it87xx | monitor
}

// webSpec is the subset of [web] the commands need.
type webSpec struct {
	Listen       string
	Auth         string // none | basic
	User         string
	PasswordHash string
}

// loadConfig reads the config file. Per DESIGN rule 8 a broken file never
// prevents start: the returned Config is always usable (defaults filled in),
// warnings describe what was replaced. err is only set for I/O problems
// other than "file does not exist".
func loadConfig(path string) (config.Config, []string, error) {
	// INTEGRATE: assumed config.Load(path) (config.Config, []config.Warning, error).
	cfg, warns, err := config.Load(path)
	msgs := make([]string, 0, len(warns))
	for _, w := range warns {
		msgs = append(msgs, fmt.Sprint(w))
	}
	if err != nil {
		if os.IsNotExist(err) {
			return config.Default(), append(msgs, "config file "+path+" not found, using built-in defaults"), nil
		}
		return config.Default(), append(msgs, "config file "+path+": "+err.Error()+", using built-in defaults"), err
	}
	return cfg, msgs, nil
}

// parseConfig validates raw TOML text without touching the file system.
func parseConfig(raw []byte) (config.Config, []string) {
	// INTEGRATE: assumed config.Parse(raw []byte) (config.Config, []config.Warning).
	cfg, warns := config.Parse(raw)
	msgs := make([]string, 0, len(warns))
	for _, w := range warns {
		msgs = append(msgs, fmt.Sprint(w))
	}
	return cfg, msgs
}

func channelSpecs(cfg config.Config) []chanSpec {
	// INTEGRATE: assumed config.Channel{Name string; PWM int; Sensor string;
	// Curve [][2]int; Critical int; Stop string} in cfg.Channels.
	out := make([]chanSpec, 0, len(cfg.Channels))
	for _, c := range cfg.Channels {
		curve := make([][2]int, 0, len(c.Curve))
		for _, p := range c.Curve {
			curve = append(curve, [2]int{int(p[0]), int(p[1])})
		}
		out = append(out, chanSpec{
			Name:     c.Name,
			PWM:      int(c.PWM),
			Sensor:   c.Sensor,
			Curve:    curve,
			Critical: int(c.Critical),
			Stop:     c.Stop,
		})
	}
	return out
}

func daemonOf(cfg config.Config) daemonSpec {
	// INTEGRATE: assumed cfg.Daemon.Interval has an int64 underlying type
	// (time.Duration or a TOML duration wrapper) and cfg.Daemon.Profile string.
	return daemonSpec{
		Interval: time.Duration(cfg.Daemon.Interval),
		Profile:  cfg.Daemon.Profile,
	}
}

func webOf(cfg config.Config) webSpec {
	// INTEGRATE: assumed cfg.Web{Listen, Auth, User, PasswordHash string}.
	return webSpec{
		Listen:       cfg.Web.Listen,
		Auth:         cfg.Web.Auth,
		User:         cfg.Web.User,
		PasswordHash: cfg.Web.PasswordHash,
	}
}

// ---------------------------------------------------------------------------
// Profiles and sensors (agent A).

func allProfiles() []profile.Profile {
	// INTEGRATE: assumed profile.All() []profile.Profile.
	return profile.All()
}

// detectDevice resolves want ("auto" or a profile name) to a Device.
func detectDevice(fs *hwmon.FS, want string) (profile.Device, error) {
	// INTEGRATE: assumed profile.Detect(fs *hwmon.FS, want string) (profile.Device, error).
	return profile.Detect(fs, want)
}

// listHwmon returns every hwmon device directory (read-only).
func listHwmon(fs *hwmon.FS) ([]hwmon.Device, error) {
	// INTEGRATE: assumed (*hwmon.FS).List() ([]hwmon.Device, error).
	return fs.List()
}

// findHwmon returns the hwmon device with the given name or an error.
func findHwmon(fs *hwmon.FS, name string) (hwmon.Device, error) {
	// INTEGRATE: assumed (*hwmon.FS).FindByName(name) (hwmon.Device, error).
	return fs.FindByName(name)
}

// readIntPath reads an integer sysfs attribute by absolute path.
func readIntPath(fs *hwmon.FS, path string) (int, error) {
	// INTEGRATE: assumed (*hwmon.FS).ReadInt(path string) (int, error).
	return fs.ReadInt(path)
}

// readHwmonInt reads an integer attribute of a hwmon device (e.g. "fan1_input").
func readHwmonInt(fs *hwmon.FS, dev hwmon.Device, attr string) (int, error) {
	return readIntPath(fs, filepath.Join(dev.Path, attr))
}

// sensorFactory creates a sensor.Source from its id ("k10temp", "nvme:max", ...).
type sensorFactory func(id string) (sensor.Source, error)

func newSensorFactory(fs *hwmon.FS, dev profile.Device) sensorFactory {
	// INTEGRATE: assumed sensor.Parse(id string, fs *hwmon.FS, dev profile.Device) (sensor.Source, error).
	return func(id string) (sensor.Source, error) { return sensor.Parse(id, fs, dev) }
}

// readTempC reads a source and returns degrees Celsius.
func readTempC(src sensor.Source) (float64, error) {
	// INTEGRATE: assumed sensor.Source has Read() (int, error) returning millidegrees.
	mc, err := src.Read()
	if err != nil {
		return 0, err
	}
	return float64(mc) / 1000, nil
}

// knownSensors lists the sensor ids that resolve on this machine.
func knownSensors(fs *hwmon.FS, dev profile.Device) []string {
	// INTEGRATE: assumed sensor.Known(fs *hwmon.FS, dev profile.Device) []string.
	return sensor.Known(fs, dev)
}

// ---------------------------------------------------------------------------
// Alerts, controller, sd_notify (agent B).

func newAlerter() alert.Alerter {
	return alert.New()
}

// sendAlert delivers one alert; the alert package applies the per-type cooldown.
func sendAlert(a alert.Alerter, typ, msg string) {
	// INTEGRATE: assumed alert.Alerter has Send(typ, msg string).
	a.Send(typ, msg)
}

// controlOpts is what serve passes to the controller besides config/device.
type controlOpts struct {
	DryRun bool
	RunDir string // state.json, alert stamps
}

func newController(cfg config.Config, dev profile.Device, f sensorFactory, a alert.Alerter, o controlOpts) (*control.Controller, error) {
	// INTEGRATE: assumed control.New(cfg config.Config, dev profile.Device,
	// f func(string) (sensor.Source, error), a alert.Alerter, opts control.Options)
	// (*control.Controller, error) with Options{DryRun bool; RunDir string}.
	return control.New(cfg, dev, f, a, control.Options{DryRun: o.DryRun, RunDir: o.RunDir})
}

// runController blocks until ctx is done or the loop fails fatally.
func runController(ctx context.Context, c *control.Controller) error {
	// INTEGRATE: assumed (*control.Controller).Run(ctx) error.
	return c.Run(ctx)
}

// stopController performs the profile-defined SafeStop on every channel.
func stopController(c *control.Controller) {
	// INTEGRATE: assumed (*control.Controller).Stop() (idempotent, blocking).
	c.Stop()
}

func notifyReady() {
	// INTEGRATE: assumed sdnotify.Ready() (no return value used).
	sdnotify.Ready()
}

// ---------------------------------------------------------------------------
// Web handler and IPC (agent C).

// webDeps is the local view of web.Deps.
type webDeps struct {
	Service    control.Service
	ConfigPath string
	PresetDir  string
	Profiles   []profile.Profile
	Sensors    []string
	Web        webSpec
}

func newWebHandler(d webDeps) http.Handler {
	// INTEGRATE: assumed web.Deps{Service control.Service; Config web.ConfigStore;
	// Presets web.PresetStore; Log web.LogSource; Profiles []profile.Profile;
	// Version string; Auth web.Auth{Mode, User, PasswordHash string}; Sensors []string}
	// and the store interfaces below (method sets guessed from the API table).
	return web.NewHandler(web.Deps{
		Service:  d.Service,
		Config:   fileConfigStore{path: d.ConfigPath},
		Presets:  dirPresetStore{dir: d.PresetDir},
		Log:      journalLog{unit: unitName},
		Profiles: d.Profiles,
		Version:  version.Version,
		Auth:     web.Auth{Mode: d.Web.Auth, User: d.Web.User, PasswordHash: d.Web.PasswordHash},
		Sensors:  d.Sensors,
	})
}

// fileConfigStore backs GET/PUT /api/config with the TOML file.
type fileConfigStore struct{ path string }

// Raw returns the current file content.
func (s fileConfigStore) Raw() ([]byte, error) { return os.ReadFile(s.path) }

// Save validates and writes raw TOML; warnings are returned, not fatal.
func (s fileConfigStore) Save(raw []byte) ([]string, error) {
	// INTEGRATE: assumed config.Save(path string, raw []byte) error.
	_, warns := parseConfig(raw)
	return warns, config.Save(s.path, raw)
}

// dirPresetStore backs /api/presets with /etc/pvefand/presets/*.toml.
type dirPresetStore struct{ dir string }

// List returns preset names with their channel tables.
func (s dirPresetStore) List() ([]config.Preset, error) {
	// INTEGRATE: assumed config.LoadPresets(dir) ([]config.Preset, error).
	return config.LoadPresets(s.dir)
}

// Save stores the given channels under name.
func (s dirPresetStore) Save(name string, channels []config.Channel) error {
	// INTEGRATE: assumed config.SavePreset(dir, name string, ch []config.Channel) error.
	return config.SavePreset(s.dir, name, channels)
}

// journalLog backs GET /api/log with journalctl.
type journalLog struct{ unit string }

// Lines returns the last n journal lines of the unit.
func (j journalLog) Lines(n int) ([]string, error) { return journalLines(j.unit, n) }

// serveIPC serves handler on the unix socket until ctx is done.
func serveIPC(ctx context.Context, sock string, h http.Handler) error {
	// INTEGRATE: assumed ipc.Serve(ctx, sock string, h http.Handler) error (blocking).
	return ipc.Serve(ctx, sock, h)
}

// ipcClient returns an http.Client that dials the unix socket; any host in
// the URL is accepted.
func ipcClient(sock string) *http.Client {
	// INTEGRATE: assumed ipc.Client(sock string) *http.Client.
	return ipc.Client(sock)
}
