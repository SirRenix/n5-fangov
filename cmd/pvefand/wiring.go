// wiring.go isolates every call into a sibling package. All other files in
// cmd/pvefand use only the local types and functions defined here, so an API
// change in an internal package is fixed in exactly one place.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
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
	AllowedHosts []string // extra Host header values; "*" disables the check
}

func warningStrings(warns []config.Warning) []string {
	msgs := make([]string, 0, len(warns))
	for _, w := range warns {
		msgs = append(msgs, w.String())
	}
	return msgs
}

// loadConfig reads the config file. Per DESIGN rule 8 a broken file never
// prevents start: the returned Config is always usable (defaults filled in),
// warnings describe what was replaced. A missing file yields the defaults
// plus one warning and no error; err is only set for read errors and TOML
// syntax errors (defaults are in cfg in both cases).
func loadConfig(path string) (config.Config, []string, error) {
	cfg, warns, err := config.Load(path)
	return cfg, warningStrings(warns), err
}

// parseConfig validates raw TOML text without touching the file system. A
// syntax error is reported as a warning and yields the defaults.
func parseConfig(raw []byte) (config.Config, []string) {
	cfg, warns, _ := config.Parse(raw)
	return cfg, warningStrings(warns)
}

func channelSpecs(cfg config.Config) []chanSpec {
	out := make([]chanSpec, 0, len(cfg.Channels))
	for _, c := range cfg.Channels {
		out = append(out, chanSpec{
			Name:     c.Name,
			PWM:      c.PWM,
			Sensor:   c.Sensor,
			Curve:    append([][2]int(nil), c.Curve...),
			Critical: c.Critical,
			Stop:     c.Stop,
		})
	}
	return out
}

func daemonOf(cfg config.Config) daemonSpec {
	return daemonSpec{
		Interval: cfg.Daemon.Interval,
		Profile:  cfg.Daemon.Profile,
	}
}

func webOf(cfg config.Config) webSpec {
	return webSpec{
		Listen:       cfg.Web.Listen,
		Auth:         cfg.Web.Auth,
		User:         cfg.Web.User,
		PasswordHash: cfg.Web.PasswordHash,
		AllowedHosts: append([]string(nil), cfg.Web.AllowedHosts...),
	}
}

// ---------------------------------------------------------------------------
// Profiles and sensors (agent A).

func allProfiles() []profile.Profile { return profile.All() }

// detectDevice resolves want ("auto" or a profile name) to a Device.
func detectDevice(fs *hwmon.FS, want string) (profile.Device, error) {
	return profile.Detect(fs, want)
}

// listHwmon returns every hwmon device directory (read-only).
func listHwmon(fs *hwmon.FS) ([]hwmon.Device, error) { return fs.List() }

// readIntPath reads an integer sysfs attribute by absolute path.
func readIntPath(fs *hwmon.FS, path string) (int, error) { return fs.ReadInt(path) }

// sensorFactory creates a sensor.Source from its id ("k10temp", "nvme:max", ...).
type sensorFactory func(id string) (sensor.Source, error)

func newSensorFactory(fs *hwmon.FS, dev profile.Device) sensorFactory {
	return func(id string) (sensor.Source, error) { return sensor.Parse(id, fs, dev) }
}

// controlFactory adapts sensorFactory to the controller's interface type.
// A nil sensor.Source must not become a non-nil SensorReader, hence the
// explicit error branch.
func (f sensorFactory) controlFactory() control.SensorFactory {
	return func(id string) (control.SensorReader, error) {
		s, err := f(id)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
}

// readTempC reads a source and returns degrees Celsius.
func readTempC(src sensor.Source) (float64, error) {
	mc, err := src.Read()
	if err != nil {
		return 0, err
	}
	return float64(mc) / 1000, nil
}

// sensorInfo is one selectable sensor id with a description.
type sensorInfo struct {
	ID          string
	Description string
}

// knownSensors lists the sensor ids that resolve on this machine plus the
// generic patterns (hwmon:<name>:tempN, ec:<label>) at the end.
func knownSensors(fs *hwmon.FS, dev profile.Device) []sensorInfo {
	infos := sensor.Known(fs, dev)
	out := make([]sensorInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, sensorInfo{ID: i.ID, Description: i.Description})
	}
	return out
}

// ---------------------------------------------------------------------------
// Alerts, controller, sd_notify (agent B).

func newAlerter() alert.Sink { return alert.New(log.Default()) }

// sendAlert delivers one alert immediately (no cooldown; the controller
// applies the per-kind cooldown for alerts raised from the loop).
func sendAlert(a alert.Sink, kind, msg string) { a.Alert(kind, msg) }

// controlOpts is what serve passes to the controller besides config/device.
type controlOpts struct {
	DryRun bool
	RunDir string // state.json, override.<name>, alert stamps
}

func newController(cfg config.Config, dev profile.Device, f sensorFactory, a alert.Sink, o controlOpts) (*control.Controller, error) {
	return control.New(cfg, dev, f.controlFactory(), a, control.Options{
		DryRun: o.DryRun,
		RunDir: o.RunDir,
		Logger: log.Default(),
		Notify: func() { _ = sdnotify.Watchdog() },
		Status: func(s string) { _ = sdnotify.Status(s) },
	})
}

// runController blocks until ctx is done or the loop fails fatally. The
// controller performs its own SafeStop when Run returns.
func runController(ctx context.Context, c *control.Controller) error { return c.Run(ctx) }

// stopController performs the profile-defined SafeStop on every channel.
// Idempotent; a no-op when Run already did it.
func stopController(c *control.Controller) { c.Stop() }

func notifyReady()    { _ = sdnotify.Ready() }
func notifyStopping() { _ = sdnotify.Stopping() }

// ---------------------------------------------------------------------------
// Web handler and IPC (agent C).

// webDeps is the local view of web.Deps.
type webDeps struct {
	Service    control.Service
	ConfigPath string
	PresetDir  string
	Device     profile.Device // detected device; its profile is "active"
	Sysfs      *hwmon.FS
	Web        webSpec
}

// webServer exposes the two handler flavours of internal/web: the TCP one
// enforces CSRF and optional basic auth, the socket one does not.
type webServer struct {
	TCP    http.Handler
	Socket http.Handler
	serve  func(ctx context.Context, ln net.Listener) error
}

// ServeTCP serves the TCP handler on ln until ctx is done.
func (s webServer) ServeTCP(ctx context.Context, ln net.Listener) error { return s.serve(ctx, ln) }

func newWebServer(d webDeps) webServer {
	active := ""
	if d.Device != nil {
		active = d.Device.Profile().Name()
	}
	profiles := func() []web.ProfileInfo {
		var out []web.ProfileInfo
		for _, p := range profile.All() {
			out = append(out, web.ProfileInfo{
				Name: p.Name(), Title: p.Title(), Verified: p.Verified(), Notes: p.Notes(),
				Active: p.Name() == active,
			})
		}
		return out
	}
	// sensors lists the selectable ids; concrete ids (no "<...>" pattern)
	// carry a live reading so the curve editor can show it.
	sensors := func() []web.SensorInfo {
		var out []web.SensorInfo
		for _, i := range sensor.Known(d.Sysfs, d.Device) {
			info := web.SensorInfo{ID: i.ID, Description: i.Description}
			if !strings.Contains(i.ID, "<") {
				if src, err := sensor.Parse(i.ID, d.Sysfs, d.Device); err == nil {
					if t, err := readTempC(src); err == nil {
						info.Temp = &t
					}
				}
			}
			out = append(out, info)
		}
		return out
	}
	// Host header allow-list (DNS rebinding): the listen host itself plus
	// [web].allowed_hosts; IP literals and localhost always pass.
	allowed := append([]string(nil), d.Web.AllowedHosts...)
	if h, _, err := net.SplitHostPort(d.Web.Listen); err == nil && h != "" {
		allowed = append(allowed, h)
	}
	s := web.New(web.Deps{
		Service: d.Service,
		Config:  fileConfigStore{path: d.ConfigPath},
		Validate: func(raw []byte) ([]string, error) {
			_, warns, err := config.Parse(raw)
			return warningStrings(warns), err
		},
		Presets:      dirPresetStore{dir: d.PresetDir, cfgPath: d.ConfigPath, svc: d.Service},
		Log:          func(n int) ([]string, error) { return journalLines(unitName, n) },
		Profiles:     profiles,
		Version:      version.Version,
		Auth:         web.AuthConfig{Mode: d.Web.Auth, User: d.Web.User, PasswordHash: d.Web.PasswordHash},
		Sensors:      sensors,
		AllowedHosts: allowed,
		Logf:         log.Printf,
	})
	return webServer{TCP: s.Handler(), Socket: s.SocketHandler(), serve: s.Serve}
}

// fileConfigStore backs GET/PUT /api/config with the TOML file.
type fileConfigStore struct{ path string }

// Raw returns the current file content; a missing file reads as the
// built-in defaults so the editor has something to start from.
func (s fileConfigStore) Raw() ([]byte, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return config.Marshal(config.Default()), nil
	}
	return raw, err
}

// Save rejects TOML syntax errors and writes raw atomically. Per-field
// warnings do not block the write (rule 8); they are logged here and again
// by the controller's Reload.
func (s fileConfigStore) Save(raw []byte) error {
	_, warns, err := config.Parse(raw)
	if err != nil {
		return err
	}
	for _, w := range warns {
		log.Printf("config save: %s", w)
	}
	return config.Save(s.path, raw)
}

// Parsed makes GET /api/config also carry the validated config in TOML
// layout (lowercase keys, durations as strings) plus the parse warnings.
func (s fileConfigStore) Parsed() (any, error) {
	raw, err := s.Raw()
	if err != nil {
		return nil, err
	}
	cfg, warns, err := config.Parse(raw)
	if err != nil {
		return nil, err
	}
	return configJSON(cfg, warns), nil
}

// configJSON renders cfg the way the TOML file is laid out.
func configJSON(cfg config.Config, warns []config.Warning) map[string]any {
	d := cfg.Daemon
	chans := make([]map[string]any, 0, len(cfg.Channels))
	for _, c := range cfg.Channels {
		chans = append(chans, map[string]any{
			"name": c.Name, "pwm": c.PWM, "sensor": c.Sensor, "curve": c.Curve,
			"critical": c.Critical, "stop": c.Stop,
		})
	}
	return map[string]any{
		"daemon": map[string]any{
			"interval": d.Interval.String(), "step_up": d.StepUp, "step_down": d.StepDown,
			"stall_min_duty": d.StallMinDuty, "stall_cycles": d.StallCycles, "stale_cycles": d.StaleCycles,
			"alert_cooldown": d.AlertCooldown.String(), "log_every": d.LogEvery, "profile": d.Profile,
		},
		"web": map[string]any{
			"listen": cfg.Web.Listen, "auth": cfg.Web.Auth, "user": cfg.Web.User,
			"password_hash": cfg.Web.PasswordHash, // redacted by the web layer before it leaves the daemon
			"allowed_hosts": nonNilStrings(cfg.Web.AllowedHosts),
		},
		"channel":  chans,
		"warnings": warningStrings(warns),
	}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// dirPresetStore backs /api/presets with <dir>/<name>.toml files that hold
// only [[channel]] tables.
type dirPresetStore struct {
	dir     string
	cfgPath string
	svc     control.Service
}

// List returns every preset with the channel names it contains.
func (s dirPresetStore) List() ([]web.Preset, error) {
	all := config.LoadPresets(s.dir)
	out := make([]web.Preset, 0, len(all))
	for _, name := range config.PresetNames(s.dir) {
		chans, ok := all[name]
		if !ok {
			continue // did not parse; LoadPresets skipped it
		}
		names := make([]string, 0, len(chans))
		for _, c := range chans {
			names = append(names, c.Name)
		}
		out = append(out, web.Preset{Name: name, Channels: names})
	}
	return out, nil
}

// Apply replaces the [[channel]] tables of the config file with the preset,
// writes the file and reloads the daemon. The file is rewritten from the
// parsed config, so comments in it are lost. control.ErrRestartRequired
// passes through unchanged (the web layer answers 202 for it).
func (s dirPresetStore) Apply(name string) error {
	chans, warns, err := config.LoadPreset(s.dir, name)
	if err != nil {
		return err
	}
	for _, w := range warns {
		log.Printf("preset %s: %s", name, w)
	}
	if len(chans) == 0 {
		return fmt.Errorf("preset %q contains no usable [[channel]] table", name)
	}
	cfg, _, err := config.Load(s.cfgPath)
	if err != nil {
		return fmt.Errorf("current config: %w", err)
	}
	cfg.Channels = config.CloneChannels(chans)
	raw := config.Marshal(cfg)
	if err := config.Save(s.cfgPath, raw); err != nil {
		return err
	}
	log.Printf("preset %s applied to %s (%d channels)", name, s.cfgPath, len(chans))
	return s.svc.Reload(raw)
}

// Save stores the channel tables of the current config file as preset name.
func (s dirPresetStore) Save(name string) error {
	cfg, _, err := config.Load(s.cfgPath)
	if err != nil {
		return fmt.Errorf("current config: %w", err)
	}
	if len(cfg.Channels) == 0 {
		return errors.New("current config has no channels to save")
	}
	return config.SavePreset(s.dir, name, cfg.Channels)
}

// serveIPC serves handler on the unix socket until ctx is done.
func serveIPC(ctx context.Context, sock string, h http.Handler) error {
	return ipc.Serve(ctx, sock, h)
}

// ipcClient returns an http.Client that dials the unix socket; any host in
// the URL is accepted.
func ipcClient(sock string) *http.Client { return ipc.Client(sock) }
