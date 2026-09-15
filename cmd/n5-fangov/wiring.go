// wiring.go isolates every call into a sibling package. All other files in
// cmd/n5-fangov use only the local types and functions defined here, so an API
// change in an internal package is fixed in exactly one place.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/ipc"
	"github.com/SirRenix/n5-fangov/internal/logfile"
	"github.com/SirRenix/n5-fangov/internal/profile"
	"github.com/SirRenix/n5-fangov/internal/sdnotify"
	"github.com/SirRenix/n5-fangov/internal/sensor"
	"github.com/SirRenix/n5-fangov/internal/version"
	"github.com/SirRenix/n5-fangov/internal/web"
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
	TLS          string   // auto | off | file
	CertFile     string
	KeyFile      string
}

// logSpec is the [log] section.
type logSpec struct {
	File      string // "" = journal only
	MaxSizeMB int
	MaxFiles  int
}

func logOf(cfg config.Config) logSpec {
	return logSpec{File: cfg.Log.File, MaxSizeMB: cfg.Log.MaxSizeMB, MaxFiles: cfg.Log.MaxFiles}
}

// defaultTLSMode is the tls mode for a listen address without an explicit
// setting: "off" on loopback, "auto" elsewhere.
func defaultTLSMode(listen string) string { return config.DefaultTLS(listen) }

// passwordHash computes the [web].password_hash value for user/password:
// salted PBKDF2-HMAC-SHA256 (`pbkdf2$<iter>$<salt>$<key>`). The legacy
// sha256("user:password") hex form stays accepted by the daemon.
func passwordHash(user, password string) string { return web.PasswordHash(user, password) }

// verifyPassword checks a password against a stored hash of either form.
func verifyPassword(user, password, stored string) bool {
	return web.VerifyPassword(user, password, stored)
}

// redactedHash is the placeholder the API and bundles use for the stored
// password hash.
const redactedHash = web.RedactedHash

// redactConfigText replaces the password hash in config text the same way
// GET /api/config does (line forms by regex, the parsed value as a bare
// substring only when it is long enough not to hit other text) (M7).
func redactConfigText(raw string) string { return web.RedactRaw(raw) }

// setConfigKey edits one key of a top-level table in raw TOML text without
// touching comments or other keys (passwd, setup on an existing file).
func setConfigKey(raw []byte, section, key, value string) []byte {
	return config.SetKey(raw, section, key, value)
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string { return strconv.Quote(s) }

// saveConfig writes raw atomically.
func saveConfig(path string, raw []byte) error { return config.Save(path, raw) }

// n5proChannels is the verified N5 Pro channel set.
func n5proChannels() []chanSpec { return channelSpecs(config.Config{Channels: config.N5ProChannels()}) }

// renderConfig builds a fresh config (built-in defaults, the given profile,
// channels and [web] settings) and renders it as TOML (setup).
func renderConfig(profileName string, chans []chanSpec, w webSpec) []byte {
	cfg := config.Default()
	cfg.Daemon.Profile = profileName
	for _, c := range chans {
		cfg.Channels = append(cfg.Channels, config.Channel{
			Name: c.Name, PWM: c.PWM, Sensor: c.Sensor,
			Curve: append([][2]int(nil), c.Curve...), Critical: c.Critical, Stop: c.Stop,
		})
	}
	cfg.Web = config.Web{
		Listen: w.Listen, Auth: w.Auth, User: w.User, PasswordHash: w.PasswordHash,
		AllowedHosts: append([]string(nil), w.AllowedHosts...),
		TLS:          w.TLS, CertFile: w.CertFile, KeyFile: w.KeyFile,
	}
	return config.Marshal(cfg)
}

// Preset directory helpers (bundle export/import works on the raw files).
func presetNames(dir string) []string    { return config.PresetNames(dir) }
func validPresetName(name string) bool   { return config.ValidPresetName(name) }
func presetPath(dir, name string) string { return filepath.Join(dir, name+".toml") }

// parsePresetRaw validates preset text and returns its channel count.
func parsePresetRaw(raw []byte) (int, error) {
	chans, _, err := config.ParseChannels(raw)
	return len(chans), err
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

// parseConfigErr is parseConfig with the syntax error exposed (bundle
// import refuses on it).
func parseConfigErr(raw []byte) (config.Config, []string, error) {
	cfg, warns, err := config.Parse(raw)
	return cfg, warningStrings(warns), err
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

// sanitizeChannelSpecs applies control.SanitizeChannels to a channel list:
// on the n5pro profile pwm1..3 missing from the config are added with the
// built-in defaults and pwm3 never keeps stop="auto" (the EC does not
// regulate it after a write). Other profiles come back unchanged. notes
// carries one line per correction (what serve logs and alerts).
func sanitizeChannelSpecs(profileName string, chans []chanSpec) (out []chanSpec, notes []string) {
	in := make([]config.Channel, 0, len(chans))
	for _, c := range chans {
		in = append(in, config.Channel{
			Name:     c.Name,
			PWM:      c.PWM,
			Sensor:   c.Sensor,
			Curve:    append([][2]int(nil), c.Curve...),
			Critical: c.Critical,
			Stop:     c.Stop,
		})
	}
	fixed, notes := control.SanitizeChannels(profileName, in)
	return channelSpecs(config.Config{Channels: fixed}), notes
}

// safeDutyOf is the duty a channel holds while its sensor is unusable
// (control.safeDuty): the fixed stop duty when configured, else 255.
func safeDutyOf(c chanSpec) string {
	if d, fixed := (config.Channel{Stop: c.Stop}).StopDuty(); fixed {
		return strconv.Itoa(d) + " (configured stop duty)"
	}
	return "255"
}

// configUnreadable reports whether a loadConfig error means the file exists
// but cannot be read (as opposed to a TOML syntax error, which serve
// survives on the built-in defaults).
func configUnreadable(err error) bool { return errors.Is(err, config.ErrUnreadable) }

// isLoopbackListen reports whether a [web].listen value binds loopback only.
func isLoopbackListen(listen string) bool { return config.IsLoopbackListen(listen) }

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
		TLS:          cfg.Web.TLS,
		CertFile:     cfg.Web.CertFile,
		KeyFile:      cfg.Web.KeyFile,
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

// sendAlert delivers one alert immediately (no cooldown). Used by the
// hidden `alert` subcommand (onfailure unit); serve uses sendAlertCooled.
func sendAlert(a alert.Sink, kind, msg string) { a.Alert(kind, msg) }

// startAlertCooldown is the minimum gap between two start-up alerts of the
// same kind. A daemon caught in a restart loop (Restart=always, RestartSec=5)
// would otherwise send one PVE notification per attempt.
const startAlertCooldown = 30 * time.Minute

// sendAlertCooled delivers an alert unless one of the same kind went out
// within startAlertCooldown. The stamp lives in <runDir>/alert.<kind> in the
// same format the controller uses (unix seconds), so the two cooldowns are
// one: a "config" alert from serve also silences the controller's "config"
// alert for the period and vice versa. An unusable run dir means no
// cooldown (alert always sent).
func sendAlertCooled(runDir string, a alert.Sink, kind, msg string) {
	if runDir != "" {
		stamp := filepath.Join(runDir, "alert."+kind)
		now := time.Now().Unix()
		if b, err := os.ReadFile(stamp); err == nil {
			if last, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && now-last >= 0 && now-last < int64(startAlertCooldown.Seconds()) {
				log.Printf("ALERT[%s] suppressed (last one %s ago, cooldown %s): %s", kind,
					(time.Duration(now-last) * time.Second).String(), startAlertCooldown, firstLine(msg))
				return
			}
		}
		if err := os.WriteFile(stamp, []byte(strconv.FormatInt(now, 10)+"\n"), 0o644); err != nil {
			log.Printf("alert stamp %s: %v", stamp, err)
		}
	}
	a.Alert(kind, msg)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// failsafeDevice puts every channel into its safe state without a
// controller (ExecStopPost). The channel list is completed and corrected
// the same way the daemon does it (N5 Pro: pwm1..3 always, pwm3 never
// "auto"); one log line per channel goes to logger.
func failsafeDevice(dev profile.Device, cfg config.Config, logger *log.Logger) error {
	var l control.Logger
	if logger != nil {
		l = logger
	}
	return control.Failsafe(dev, cfg, l)
}

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

// controllerLastCycle is when the loop last finished a cycle (zero before
// the first); controllerInterval is the active cycle interval.
func controllerLastCycle(c *control.Controller) time.Time    { return c.LastCycle() }
func controllerInterval(c *control.Controller) time.Duration { return c.Interval() }

func notifyReady()    { _ = sdnotify.Ready() }
func notifyStopping() { _ = sdnotify.Stopping() }
func notifyWatchdog() { _ = sdnotify.Watchdog() }

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
	Log        logStore    // file store when [log].file is set, else the journal
	Bundle     bundle      // settings export/import
	TLS        bool        // the TCP listener serves HTTPS (HSTS)
	TLSMgr     *tlsManager // certificate manager behind /api/tls (nil: 501)
	TLSHosts   []string    // SAN hosts reported by GET /api/tls
}

// logStore is the log read side (DESIGN v0.2 "Log store"): implemented by
// *logfile.Writer for the file and by journalLogStore without one.
type logStore interface {
	Lines(n int) ([]string, error)
	Export(w io.Writer) error
	Clear() error
	Path() string
}

// bundle is the settings bundle (DESIGN v0.2 "Settings bundle").
type bundle interface {
	Export() ([]byte, error)
	Import(b []byte) (restartRequired bool, err error)
}

// webServer exposes the two handler flavours of internal/web: the TCP one
// enforces CSRF and optional basic auth, the socket one does not.
type webServer struct {
	TCP      http.Handler
	Socket   http.Handler
	serve    func(ctx context.Context, ln net.Listener) error
	serveTLS func(ctx context.Context, ln net.Listener, mgr *tlsManager) error
}

// ServeTCP serves the TCP handler on ln until ctx is done.
func (s webServer) ServeTCP(ctx context.Context, ln net.Listener) error { return s.serve(ctx, ln) }

// ServeTLS serves the TCP handler over TLS with the certificate the
// manager holds (hot-swappable) until ctx is done.
func (s webServer) ServeTLS(ctx context.Context, ln net.Listener, mgr *tlsManager) error {
	return s.serveTLS(ctx, ln, mgr)
}

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
	deps := web.Deps{
		Service: d.Service,
		Config:  fileConfigStore{path: d.ConfigPath},
		Validate: func(raw []byte) ([]string, error) {
			_, warns, err := config.Parse(raw)
			return warningStrings(warns), err
		},
		Presets:      dirPresetStore{dir: d.PresetDir, cfgPath: d.ConfigPath, svc: d.Service},
		Profiles:     profiles,
		Version:      version.Version,
		Auth:         web.AuthConfig{Mode: d.Web.Auth, User: d.Web.User, PasswordHash: d.Web.PasswordHash},
		Sensors:      sensors,
		AllowedHosts: allowed,
		Logf:         log.Printf,
	}
	if d.Log == nil {
		d.Log = journalLogStore{}
	}
	// Log, Bundle and TLS are the v0.2 members of web.Deps; see wiring_v2.go.
	applyV2Deps(&deps, d)
	s := web.New(deps)
	return webServer{TCP: s.Handler(), Socket: s.SocketHandler(), serve: s.Serve, serveTLS: serveTLSFunc(s)}
}

// journalLogStore is the logStore without a log file: Lines and Export
// come from journalctl, Clear is refused (the journal is never touched).
type journalLogStore struct{}

func (journalLogStore) Lines(n int) ([]string, error) { return journalLines(unitName, n) }

func (journalLogStore) Export(w io.Writer) error {
	lines, err := journalLines(unitName, 100000)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := io.WriteString(w, l+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// Clear is refused with errors.ErrUnsupported so the API answers 501, not
// 500 (L5): there is nothing to clear, the journal is never touched.
func (journalLogStore) Clear() error {
	return fmt.Errorf("no log file configured ([log].file is empty); the journal is not cleared: %w", errors.ErrUnsupported)
}

func (journalLogStore) Path() string { return "" }

// newLogFile opens the rotating log file of [log].
func newLogFile(l logSpec) (*logfile.Writer, error) {
	return logfile.New(l.File, l.MaxSizeMB, l.MaxFiles)
}

// teeLog makes the standard logger write to stdout (journald) and, stamped
// with the local time, to w.
func teeLog(w io.Writer) { log.SetOutput(io.MultiWriter(os.Stdout, logfile.Timestamped(w))) }

// Read-side helpers for the CLI on a file the daemon owns.
func readLogLines(path string, n int) ([]string, error) { return logfile.ReadLines(path, n) }
func exportLogFile(path string, w io.Writer) error      { return logfile.ExportFile(path, w) }
func truncateLogFile(path string) error                 { return logfile.Truncate(path) }

// Restart sentinel of Service.Reload (channel set / profile changed).
func isRestartRequired(err error) bool { return errors.Is(err, control.ErrRestartRequired) }
func errRestartRequired() error        { return control.ErrRestartRequired }

// Config text helpers for bundles and setup.
func defaultConfigRaw() []byte { return config.Marshal(config.Default()) }

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
			"tls":           cfg.Web.TLS, "cert_file": cfg.Web.CertFile, "key_file": cfg.Web.KeyFile,
		},
		"log": map[string]any{
			"file": cfg.Log.File, "max_size_mb": cfg.Log.MaxSizeMB, "max_files": cfg.Log.MaxFiles,
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
