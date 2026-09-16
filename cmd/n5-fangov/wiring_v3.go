// wiring_v3.go holds the v0.3.0-beta adapters between cmd and the internal
// packages (DESIGN "v0.3.0-beta contract"): the state directory, the
// account store, the alert manager, the dashboard store, the About card
// and the reload hook that re-applies [alert] after every config write.
// Like wiring_v2.go and tlsmgr.go it imports internal/* directly.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/sensor"
	"github.com/SirRenix/n5-fangov/internal/version"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// ---------------------------------------------------------------------------
// State directory (/var/lib/n5-fangov: sessions.json, alerts.json).

const (
	defaultStateDir  = "/var/lib/n5-fangov"
	stateDirEnv      = "N5FANGOV_STATE_DIR"
	sessionsFileName = "sessions.json"
	alertsFileName   = "alerts.json"
)

// stateDir returns the state directory: N5FANGOV_STATE_DIR, else the
// unit's $STATE_DIRECTORY (first entry), else /var/lib/n5-fangov.
func stateDir() string {
	if d := os.Getenv(stateDirEnv); d != "" {
		return d
	}
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		if i := strings.IndexByte(d, ':'); i >= 0 {
			d = d[:i]
		}
		return d
	}
	return defaultStateDir
}

// ensureStateDir creates dir (0700) and probes that a file can be written
// there. On failure it logs once and returns "": the daemon then runs
// without persistence (sessions and alert history in memory only).
func ensureStateDir(dir string) string {
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("state-dir %s: %v (sessions and alert history kept in memory only)", dir, err)
		return ""
	}
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		log.Printf("state-dir %s not writable: %v (sessions and alert history kept in memory only)", dir, err)
		return ""
	}
	probe.Close()
	_ = os.Remove(probe.Name())
	return dir
}

func sessionsPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, sessionsFileName)
}

func alertsPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, alertsFileName)
}

// ---------------------------------------------------------------------------
// Alert manager (web.AlertMgr): the swappable sink the controller and
// serve deliver through, wrapped in the ring the panel reads.

// alertKinds is the panel's kind list with one-line descriptions. Order
// as in the contract.
var alertKinds = []web.AlertKind{
	{Kind: "sensor", Description: "a channel's sensor is unresolved, unreadable, implausible or frozen; the channel sits at its safe duty"},
	{Kind: "stall", Description: "a fan reports 0 RPM at a duty that should turn it; the channel goes to 255"},
	{Kind: "temp", Description: "a channel reached its critical temperature; 255 immediately"},
	{Kind: "write", Description: "repeated pwm write or read-back errors; every channel at 255 (failsafe)"},
	{Kind: "config", Description: "the config file has problems; built-in defaults are in effect for those values"},
	{Kind: "config-channels", Description: "the channel set was corrected (N5 Pro channel added, forced stop duty, pwm the device lacks)"},
	{Kind: "restart", Description: "the unit failed and systemd restarted it (onfailure)"},
	{Kind: "failed", Description: "the unit failed and did not come back (onfailure)"},
	{Kind: "kernel", Description: "a kernel the box can boot into lacks the DKMS fan driver module (apt hook)"},
	{Kind: "tls", Description: "the configured certificate pair could not be served; the automatic one is in use"},
	{Kind: "test", Description: "a test alert sent from the dashboard or `n5-fangov alerts test`"},
}

// alertManager implements web.AlertMgr. transport/mailTo mirror the
// [alert] section in effect; sw is what the controller holds, ring wraps
// it and records. cooldown reports the daemon's alert_cooldown for the
// panel (nil offline).
type alertManager struct {
	mu        sync.Mutex
	cfgPath   string
	pin       func([]byte) []byte
	sw        *alert.Swappable
	ring      *alert.Ring
	transport string
	mailTo    string
	effective string
	cooldown  func() time.Duration
	logger    alert.Logger
}

// newAlertManager builds the sink chain for the [alert] section of cfg.
// alertsFile "" keeps the history in memory only.
func newAlertManager(cfgPath, alertsFile string, a config.Alert, pin func([]byte) []byte) *alertManager {
	m := &alertManager{cfgPath: cfgPath, pin: pin, logger: log.Default()}
	sink, eff := alert.NewFor(a.Transport, a.MailTo, m.logger)
	m.transport, m.mailTo, m.effective = a.Transport, a.MailTo, eff
	m.sw = alert.NewSwappable(sink)
	m.ring = alert.NewRing(m.sw, alertsFile, m.logger)
	return m
}

// sink is what serve and the controller deliver through (records + delivers).
func (m *alertManager) sink() alert.Sink { return m.ring }

// apply swaps the sink when the [alert] section differs from the one in
// effect (Configure, and every successful config reload). Returns the
// effective transport.
func (m *alertManager) apply(a config.Alert) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.Transport == m.transport && a.MailTo == m.mailTo {
		return m.effective
	}
	sink, eff := alert.NewFor(a.Transport, a.MailTo, m.logger)
	m.sw.Set(sink)
	m.transport, m.mailTo, m.effective = a.Transport, a.MailTo, eff
	m.logger.Printf("alerts: transport %s (%s)%s", a.Transport, eff, mailToNote(a.Transport, a.MailTo))
	return eff
}

func mailToNote(transport, mailTo string) string {
	if transport == alert.TransportMail || transport == alert.TransportAuto {
		return ", mail_to " + mailTo
	}
	return ""
}

func (m *alertManager) Status() web.AlertStatus {
	m.mu.Lock()
	transport, mailTo, eff := m.transport, m.mailTo, m.effective
	m.mu.Unlock()
	pve, mail := alert.Available()
	inst, cur, wr, reason := alert.TemplateStatus()
	cool := ""
	if m.cooldown != nil {
		cool = m.cooldown().String()
	}
	return web.AlertStatus{
		Transport: transport, Effective: eff, MailTo: mailTo,
		PVEAvailable: pve, MailAvailable: mail,
		Template: web.TemplateStatus{Installed: inst, Current: cur, Writable: wr, Path: alert.TemplatePath, Reason: reason},
		Cooldown: cool,
		Kinds:    append([]web.AlertKind(nil), alertKinds...),
	}
}

// Recent returns the newest n delivered alerts, newest first.
func (m *alertManager) Recent(n int) []web.AlertRecord {
	recs := m.ring.Recent(n)
	out := make([]web.AlertRecord, 0, len(recs))
	for _, r := range recs {
		out = append(out, web.AlertRecord{TS: r.TS, Kind: r.Kind, Msg: r.Msg})
	}
	return out
}

// Last returns the newest delivery per kind (GET /api/alerts "last").
func (m *alertManager) Last() map[string]int64 { return m.ring.Last() }

// Test sends a "test" alert now, without cooldown, through the ring (so
// it shows in the history) and returns the transport that was tried.
func (m *alertManager) Test() (string, error) {
	m.mu.Lock()
	eff := m.effective
	m.mu.Unlock()
	host, _ := os.Hostname()
	msg := fmt.Sprintf("test alert from n5-fangov %s on %s at %s — delivery works if you can read this.",
		version.Version, host, time.Now().Format("2006-01-02 15:04:05"))
	return eff, m.ring.Send("test", msg)
}

// InstallTemplate writes the embedded PVE template pair.
func (m *alertManager) InstallTemplate() (string, error) { return alert.InstallTemplate() }

// Configure validates, writes [alert] to the config file (comments kept,
// tls pin applied) and hot-applies it.
func (m *alertManager) Configure(transport, mailTo string) (web.AlertStatus, error) {
	transport = strings.ToLower(strings.TrimSpace(transport))
	mailTo = strings.TrimSpace(mailTo)
	if mailTo == "" {
		mailTo = config.DefaultMailTo
	}
	found := false
	for _, t := range config.AlertTransports {
		if t == transport {
			found = true
		}
	}
	if !found {
		return web.AlertStatus{}, fmt.Errorf("transport %q unknown (%s)", transport, strings.Join(config.AlertTransports, "|"))
	}
	if !config.ValidMailTo(mailTo) {
		return web.AlertStatus{}, fmt.Errorf("mail_to %q is not a local user or address", mailTo)
	}
	raw, err := readConfigRaw(m.cfgPath)
	if err != nil {
		return web.AlertStatus{}, err
	}
	raw = setConfigKey(raw, "alert", "transport", tomlString(transport))
	raw = setConfigKey(raw, "alert", "mail_to", tomlString(mailTo))
	if _, _, err := config.Parse(raw); err != nil {
		return web.AlertStatus{}, fmt.Errorf("config would not parse after the edit: %w", err)
	}
	if m.pin != nil {
		raw = m.pin(raw)
	}
	if err := saveConfig(m.cfgPath, raw); err != nil {
		return web.AlertStatus{}, fmt.Errorf("write %s: %w", m.cfgPath, err)
	}
	m.apply(config.Alert{Transport: transport, MailTo: mailTo})
	return m.Status(), nil
}

// readConfigRaw reads the config file; a missing file reads as empty
// text (SetKey then appends the section).
func readConfigRaw(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return raw, nil
}

// ---------------------------------------------------------------------------
// Account store (web.AccountStore): [web] user / password_hash.

type accountStore struct {
	mu      sync.Mutex
	cfgPath string
	pin     func([]byte) []byte
	cur     web.AuthConfig
}

func newAccountStore(cfgPath string, w webSpec, pin func([]byte) []byte) *accountStore {
	return &accountStore{cfgPath: cfgPath, pin: pin,
		cur: web.AuthConfig{Mode: w.Auth, User: w.User, PasswordHash: w.PasswordHash}}
}

func (s *accountStore) Current() web.AuthConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Update rewrites [web] user and/or password_hash ("" keeps the current
// value) with config.SetKey, so comments and the other keys stay, and
// returns the credentials now in effect. The mode is not touched: the
// web layer refuses account changes while auth is "none".
func (s *accountStore) Update(user, passwordHash string) (web.AuthConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cur
	if user != "" {
		next.User = user
	}
	if passwordHash != "" {
		if _, err := config.ParsePasswordHash(passwordHash); err != nil {
			return s.cur, fmt.Errorf("password hash: %w", err)
		}
		next.PasswordHash = passwordHash
	}
	if next == s.cur {
		return s.cur, nil
	}
	raw, err := readConfigRaw(s.cfgPath)
	if err != nil {
		return s.cur, err
	}
	raw = setConfigKey(raw, "web", "user", tomlString(next.User))
	raw = setConfigKey(raw, "web", "password_hash", tomlString(next.PasswordHash))
	if _, _, err := config.Parse(raw); err != nil {
		return s.cur, fmt.Errorf("config would not parse after the edit: %w", err)
	}
	if s.pin != nil {
		raw = s.pin(raw)
	}
	if err := saveConfig(s.cfgPath, raw); err != nil {
		return s.cur, fmt.Errorf("write %s: %w", s.cfgPath, err)
	}
	s.cur = next
	return s.cur, nil
}

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
				return nil, fmt.Errorf("sensor id %q: %v", id, err)
			}
			warns = append(warns, fmt.Sprintf("%s: %v (kept; charted once the device appears)", id, err))
		}
	}
	raw, err := readConfigRaw(s.cfgPath)
	if err != nil {
		return nil, err
	}
	raw = setConfigKey(raw, "dashboard", "sensors", tomlStringArray(clean))
	if _, _, err := config.Parse(raw); err != nil {
		return nil, fmt.Errorf("config would not parse after the edit: %w", err)
	}
	if s.pin != nil {
		raw = s.pin(raw)
	}
	if err := saveConfig(s.cfgPath, raw); err != nil {
		return nil, fmt.Errorf("write %s: %w", s.cfgPath, err)
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
// /api/config, settings import, preset apply) re-applies [alert] on
// success. [dashboard] is applied by the controller's own Reload.

type hookedService struct {
	control.Service
	alerts *alertManager
}

func (h hookedService) Reload(raw []byte) error {
	err := h.Service.Reload(raw)
	if err != nil {
		// ErrRestartRequired: nothing was applied; the restart reads the file.
		return err
	}
	if cfg, _, perr := config.Parse(raw); perr == nil && h.alerts != nil {
		h.alerts.apply(cfg.Alert)
	}
	return nil
}

// ---------------------------------------------------------------------------
// About (GET /api/about).

func aboutInfo() web.About {
	return web.About{
		Name:       "n5-fangov",
		Version:    version.Version,
		Prerelease: version.Prerelease(),
		License:    "GPL-2.0-only",
		LicenseURL: "https://www.gnu.org/licenses/old-licenses/gpl-2.0.html",
		Repo:       "https://github.com/SirRenix/n5-fangov",
		Author:     "SirRenix",
		AuthorURL:  "https://github.com/SirRenix",
		Go:         runtime.Version(),
		Credits: []web.Credit{
			{Name: "ltdstudio/minisforum-n5-it5571", URL: "https://github.com/ltdstudio/minisforum-n5-it5571", Note: "kernel driver for the IT5571 EC"},
			{Name: "Sl0thC0der/proxfansx", URL: "https://github.com/Sl0thC0der/proxfansx", Note: "dashboard idea"},
		},
	}
}

// ---------------------------------------------------------------------------
// Built-in presets (dirPresetStore helpers).

// builtinPresetInfo is one embedded preset as the store sees it.
type builtinPresetInfo struct {
	Name        string
	Description string
	Raw         []byte
}

// builtinPresetsFor lists the embedded presets of profileName.
func builtinPresetsFor(profileName string) []builtinPresetInfo {
	var out []builtinPresetInfo
	for _, p := range config.BuiltinPresets(profileName) {
		out = append(out, builtinPresetInfo{Name: p.Name, Description: p.Description, Raw: p.Raw})
	}
	return out
}

func isBuiltinPreset(name string) bool { return config.IsBuiltinPreset(name) }

// builtinPresetRaw returns the embedded preset text for any profile.
func builtinPresetRaw(name string) ([]byte, bool) {
	p, ok := config.BuiltinPresetByName(name)
	return p.Raw, ok
}

func errPresetBuiltin() error { return web.ErrPresetBuiltin }

// deletePreset removes a user preset file; fs.ErrNotExist passes through.
func deletePreset(dir, name string) error {
	err := config.DeletePreset(dir, name)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("preset %q: %w", name, fs.ErrNotExist)
	}
	return err
}

// ---------------------------------------------------------------------------
// web.Deps members of v0.3.

// applyV3Deps sets SessionFile, Account, Alerts, Dashboard and About. A
// typed nil must not become a non-nil interface, hence the checks.
func applyV3Deps(deps *web.Deps, d webDeps) {
	deps.SessionFile = d.SessionFile
	if d.Account != nil {
		deps.Account = d.Account
	}
	if d.Alerts != nil {
		deps.Alerts = d.Alerts
	}
	if d.Dashboard != nil {
		deps.Dashboard = d.Dashboard
	}
	deps.About = aboutInfo()
}

// orMemory names a state file path or says that the data stays in memory.
func orMemory(path string) string {
	if path == "" {
		return "in memory only"
	}
	return path
}

// ---------------------------------------------------------------------------
// Local views used by check and the alerts CLI.

// alertSpec is the [alert] section.
type alertSpec struct {
	Transport string // auto | pve | mail | log | off
	MailTo    string
}

func alertOf(cfg config.Config) alertSpec {
	return alertSpec{Transport: cfg.Alert.Transport, MailTo: cfg.Alert.MailTo}
}

// alertToolsAvailable reports PVE::Notify+perl and mail(1) presence.
func alertToolsAvailable() (pve, mail bool) { return alert.Available() }

// alertEffective names the sink a transport setting yields on this box.
func alertEffective(a alertSpec) string {
	_, eff := alert.NewFor(a.Transport, a.MailTo, nil)
	return eff
}

// cfgAlert is the [alert] section as the alert manager takes it.
func cfgAlert(cfg config.Config) config.Alert { return cfg.Alert }

// daemonOfCooldown is [daemon].alert_cooldown.
func daemonOfCooldown(cfg config.Config) time.Duration { return cfg.Daemon.AlertCooldown }

// installAlertTemplate writes the embedded PVE template pair directly
// (CLI path outside the sandbox).
func installAlertTemplate() (string, error) { return alert.InstallTemplate() }
