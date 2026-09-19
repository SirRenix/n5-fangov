// wiring_alerts.go holds the alert manager behind /api/alerts (the swappable
// sink the controller and serve deliver through, wrapped in the ring the
// panel reads) and the [alert] views check and the alerts CLI use.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/version"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// ---------------------------------------------------------------------------
// Alert manager (web.AlertMgr): the swappable sink the controller and
// serve deliver through, wrapped in the ring the panel reads.

// alertKinds is the panel's kind list with one-line descriptions. Order
// as in the contract.
var alertKinds = []web.AlertKind{
	{Kind: "sensor", Description: "a channel's sensor is unresolved, unreadable, implausible or frozen; the channel sits at its safe duty"},
	{Kind: "stall", Description: "a fan reports 0 RPM at a duty that should turn it; the channel goes to 255"},
	{Kind: "temp", Description: "a channel reached its critical temperature; 255 immediately"},
	{Kind: "ceiling", Description: "a channel reached the built-in ceiling of its sensor kind (HDD 65, SSD 85, CPU 100 C, or a lower configured one); 255 until it is 3 C below, whatever critical says"},
	{Kind: "emergency", Description: "a channel stayed at its ceiling for emergency_cycles cycles with a stalled fan (or 3x as long regardless) and the emergency hook ran; the message carries the exit status"},
	{Kind: "write", Description: "repeated pwm write or read-back errors; every channel at 255 (failsafe)"},
	{Kind: "config", Description: "the config file has problems; built-in defaults are in effect for those values"},
	{Kind: "config-channels", Description: "the channel set was corrected (N5 Pro channel added, forced stop duty, pwm the device lacks)"},
	{Kind: "restart", Description: "the unit failed and systemd restarted it (onfailure)"},
	{Kind: "failed", Description: "the unit failed and did not come back (onfailure)"},
	{Kind: "kernel", Description: "a kernel the box can boot into lacks the DKMS fan driver module (apt hook)"},
	{Kind: "tls", Description: "the configured certificate pair could not be served; the automatic one is in use"},
	{Kind: "device", Description: "the fan controller stopped accepting writes for several cycles; the daemon restarts to re-detect it"},
	{Kind: "profile", Description: "no fan controller was detected at start; nothing is regulated, fans stay in BIOS/EC control"},
	{Kind: "start", Description: "the controller could not be set up at start (see the journal)"},
	{Kind: "web", Description: "the web UI listener could not be started (TLS setup or bind failed); the CLI socket keeps working"},
	{Kind: "schedule", Description: "a scheduled preset switch failed (preset missing, invalid, write or reload error); the previous curves stay"},
	{Kind: "test", Description: "a test alert sent from the dashboard or `n5-fangov alerts test`"},
}

// alertManager implements web.AlertMgr. cur mirrors the [alert] section
// in effect; sw is what the controller holds, ring wraps it and records.
// cooldown reports the daemon's alert_cooldown for the panel (nil
// offline).
type alertManager struct {
	mu        sync.Mutex
	cfgPath   string
	pin       func([]byte) []byte
	sw        *alert.Swappable
	ring      *alert.Ring
	cur       config.Alert
	effective string
	cooldown  func() time.Duration
	logger    alert.Logger

	// Template probe cache: TemplateStatus creates and unlinks a
	// file on pmxcfs; the panel polls Status every minute. The result is
	// kept for templateProbeEvery and dropped after InstallTemplate and
	// Configure. now/probe are swapped in tests.
	tmplMu    sync.Mutex
	tmplAt    time.Time // zero = nothing cached
	tmpl      web.TemplateStatus
	now       func() time.Time
	probe     func() (installed, current, writable bool, reason string)
	testMu    sync.Mutex    // one synchronous test delivery at a time
	testLimit time.Duration // bound of one test delivery
}

// templateProbeEvery is how long a template probe result is reused.
const templateProbeEvery = 10 * time.Minute

// testDeliveryLimit bounds the panel's synchronous test delivery below the
// HTTP write timeout (30 s).
const testDeliveryLimit = 20 * time.Second

// newAlertManager builds the sink chain for the [alert] section of cfg.
// alertsFile "" keeps the history in memory only.
func newAlertManager(cfgPath, alertsFile string, a config.Alert, pin func([]byte) []byte) *alertManager {
	m := &alertManager{cfgPath: cfgPath, pin: pin, logger: log.Default(), now: time.Now, probe: alert.TemplateStatus, testLimit: testDeliveryLimit}
	sink, eff := alert.NewForConfig(alertConfig(a), m.logger)
	m.cur, m.effective = a, eff
	m.sw = alert.NewSwappable(sink)
	m.ring = alert.NewRing(m.sw, alertsFile, m.logger)
	return m
}

// alertConfig is the [alert] section as the sink factory takes it.
func alertConfig(a config.Alert) alert.Config {
	return alert.Config{Transport: a.Transport, MailTo: a.MailTo, WebhookURL: a.WebhookURL, WebhookFormat: a.WebhookFormat}
}

// templateStatus returns the cached probe, re-probing after
// templateProbeEvery or after invalidateTemplate.
func (m *alertManager) templateStatus() web.TemplateStatus {
	m.tmplMu.Lock()
	defer m.tmplMu.Unlock()
	now := m.now()
	if m.tmplAt.IsZero() || now.Sub(m.tmplAt) >= templateProbeEvery || now.Before(m.tmplAt) {
		inst, cur, wr, reason := m.probe()
		m.tmpl = web.TemplateStatus{Installed: inst, Current: cur, Writable: wr, Path: alert.TemplatePath, Reason: reason}
		m.tmplAt = now
	}
	return m.tmpl
}

// invalidateTemplate makes the next Status probe again.
func (m *alertManager) invalidateTemplate() {
	m.tmplMu.Lock()
	m.tmplAt = time.Time{}
	m.tmplMu.Unlock()
}

// sink is what serve and the controller deliver through (records + delivers).
func (m *alertManager) sink() alert.Sink { return m.ring }

// apply swaps the sink when the [alert] section differs from the one in
// effect (Configure, and every successful config reload). Returns the
// effective transport.
func (m *alertManager) apply(a config.Alert) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a == m.cur {
		return m.effective
	}
	sink, eff := alert.NewForConfig(alertConfig(a), m.logger)
	m.sw.Set(sink)
	m.cur, m.effective = a, eff
	m.logger.Printf("alerts: transport %s (%s)%s", a.Transport, eff, transportNote(a))
	return eff
}

// transportNote names the target of the transport for the log line: the
// recipient for mail, the redacted URL and format for the webhook.
func transportNote(a config.Alert) string {
	switch a.Transport {
	case alert.TransportMail, alert.TransportAuto:
		return ", mail_to " + a.MailTo
	case alert.TransportWebhook:
		return ", webhook " + alert.RedactURL(a.WebhookURL) + " (" + a.WebhookFormat + ")"
	}
	return ""
}

func (m *alertManager) Status() web.AlertStatus {
	m.mu.Lock()
	cur, eff := m.cur, m.effective
	m.mu.Unlock()
	pve, mail := alert.Available()
	cool := ""
	var coolS int64
	if m.cooldown != nil {
		d := m.cooldown()
		cool, coolS = d.String(), int64(d.Seconds())
	}
	return web.AlertStatus{
		Transport: cur.Transport, Effective: eff, MailTo: cur.MailTo,
		WebhookURL: cur.WebhookURL, WebhookFormat: cur.WebhookFormat,
		PVEAvailable: pve, MailAvailable: mail,
		Template: m.templateStatus(),
		Cooldown: cool, CooldownS: coolS,
		Kinds: append([]web.AlertKind(nil), alertKinds...),
	}
}

// Recent returns the newest n delivered alerts, newest first.
func (m *alertManager) Recent(n int) []web.AlertRecord {
	recs := m.ring.Recent(n)
	out := make([]web.AlertRecord, 0, len(recs))
	for _, r := range recs {
		out = append(out, web.AlertRecord{TS: r.TS, Kind: r.Kind, Msg: r.Msg, Error: r.Error})
	}
	return out
}

// Last returns the newest delivery per kind (GET /api/alerts "last").
func (m *alertManager) Last() map[string]int64 { return m.ring.Last() }

// Test sends a "test" alert now, without cooldown, through the ring (so
// it shows in the history) and returns the transport that was tried. One
// test at a time (alert.ErrTestBusy while another runs) and bounded by
// testLimit, so it neither outlives the HTTP write timeout nor piles up
// perl/mail children.
func (m *alertManager) Test() (string, error) {
	m.mu.Lock()
	eff := m.effective
	m.mu.Unlock()
	if !m.testMu.TryLock() {
		return eff, alert.ErrTestBusy
	}
	defer m.testMu.Unlock()
	host, _ := os.Hostname()
	msg := fmt.Sprintf("test alert from n5-fangov %s on %s at %s - delivery works if you can read this.",
		version.Version, host, time.Now().Format("2006-01-02 15:04:05"))
	ctx, cancel := context.WithTimeout(context.Background(), m.testLimit)
	defer cancel()
	return eff, m.ring.SendCtx(ctx, "test", msg)
}

// InstallTemplate writes the embedded PVE template pair. The probe cache
// is dropped either way: the operator asked, the panel shows fresh state.
func (m *alertManager) InstallTemplate() (string, error) {
	defer m.invalidateTemplate()
	return alert.InstallTemplate()
}

// Configure validates, writes [alert] to the config file (comments kept,
// tls pin applied) and hot-applies it. A nil member of s keeps the value
// in the file — the merge basis is the [alert] section of the file as
// read under the file lock, not the section in effect, so a value another
// writer put there (PUT /api/config, an import) is neither overwritten
// nor lost; only the keys s sets are written. The merged section is
// validated as a whole (a transport "webhook" needs a URL, whichever
// request supplied it).
func (m *alertManager) Configure(s web.AlertSettings) (web.AlertStatus, error) {
	var a config.Alert
	var verr error
	err := editConfig(m.cfgPath, m.pin, "alert", func(raw []byte) []byte {
		var base config.Alert
		if cfg, _, perr := config.Parse(raw); perr == nil {
			base = cfg.Alert
		} else {
			m.mu.Lock()
			base = m.cur
			m.mu.Unlock()
		}
		if a, verr = mergeAlertSettings(base, s); verr != nil {
			return nil
		}
		if s.Transport != nil {
			raw = setConfigKey(raw, "alert", "transport", tomlString(a.Transport))
		}
		if s.MailTo != nil {
			raw = setConfigKey(raw, "alert", "mail_to", tomlString(a.MailTo))
		}
		if s.WebhookURL != nil || a.WebhookURL != base.WebhookURL {
			raw = setConfigKey(raw, "alert", "webhook_url", tomlString(a.WebhookURL))
		}
		if s.WebhookFormat != nil {
			raw = setConfigKey(raw, "alert", "webhook_format", tomlString(a.WebhookFormat))
		}
		return raw
	}, func(cfg config.Config) bool {
		return cfg.Alert == a
	})
	if verr != nil {
		return web.AlertStatus{}, verr
	}
	if err != nil {
		return web.AlertStatus{}, err
	}
	m.apply(a)
	m.invalidateTemplate()
	return m.Status(), nil
}

// mergeAlertSettings lays the set members of s over base, normalises
// (trim, lower-case, defaults for an empty mail_to and format) and
// validates the result as a whole.
func mergeAlertSettings(base config.Alert, s web.AlertSettings) (config.Alert, error) {
	a := base
	if s.Transport != nil {
		a.Transport = strings.ToLower(strings.TrimSpace(*s.Transport))
	}
	if s.MailTo != nil {
		a.MailTo = strings.TrimSpace(*s.MailTo)
	}
	if s.WebhookURL != nil {
		a.WebhookURL = strings.TrimSpace(*s.WebhookURL)
	}
	if s.WebhookFormat != nil {
		a.WebhookFormat = strings.ToLower(strings.TrimSpace(*s.WebhookFormat))
	}
	if a.MailTo == "" {
		a.MailTo = config.DefaultMailTo
	}
	if a.WebhookFormat == "" {
		a.WebhookFormat = config.DefaultWebhookFormat
	}
	if !contains(config.AlertTransports, a.Transport) {
		return a, fmt.Errorf("transport %q unknown (%s)", a.Transport, strings.Join(config.AlertTransports, "|"))
	}
	if !config.ValidMailTo(a.MailTo) {
		return a, fmt.Errorf("mail_to %q is not a local user or address", a.MailTo)
	}
	if a.WebhookURL != "" {
		if err := config.ValidWebhookURL(a.WebhookURL); err != nil {
			return a, fmt.Errorf("webhook_url %v", err)
		}
	}
	if !contains(config.WebhookFormats, a.WebhookFormat) {
		return a, fmt.Errorf("webhook_format %q unknown (%s)", a.WebhookFormat, strings.Join(config.WebhookFormats, "|"))
	}
	if a.Transport == alert.TransportWebhook && a.WebhookURL == "" {
		return a, fmt.Errorf("transport %q needs webhook_url", a.Transport)
	}
	if s.Transport != nil && a.Transport != alert.TransportWebhook {
		// a switch away from the webhook clears its URL: a receiver key must
		// not linger in the file after the transport is gone (0.4.1)
		a.WebhookURL = ""
	}
	return a, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Local views used by check and the alerts CLI.

// alertSpec is the [alert] section.
type alertSpec struct {
	Transport     string // auto | pve | mail | webhook | log | off
	MailTo        string
	WebhookURL    string
	WebhookFormat string
}

func alertOf(cfg config.Config) alertSpec {
	return alertSpec{Transport: cfg.Alert.Transport, MailTo: cfg.Alert.MailTo, WebhookURL: cfg.Alert.WebhookURL, WebhookFormat: cfg.Alert.WebhookFormat}
}

// alertToolsAvailable reports PVE::Notify+perl and mail(1) presence.
func alertToolsAvailable() (pve, mail bool) { return alert.Available() }

// alertEffective names the sink a transport setting yields on this box.
func alertEffective(a alertSpec) string {
	_, eff := alert.NewForConfig(alert.Config{Transport: a.Transport, MailTo: a.MailTo, WebhookURL: a.WebhookURL, WebhookFormat: a.WebhookFormat}, nil)
	return eff
}

// cfgAlert is the [alert] section as the alert manager takes it.
func cfgAlert(cfg config.Config) config.Alert { return cfg.Alert }

// daemonOfCooldown is [daemon].alert_cooldown.
func daemonOfCooldown(cfg config.Config) time.Duration { return cfg.Daemon.AlertCooldown }

// installAlertTemplate writes the embedded PVE template pair directly
// (CLI path outside the sandbox).
func installAlertTemplate() (string, error) { return alert.InstallTemplate() }
