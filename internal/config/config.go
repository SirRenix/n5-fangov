// Package config loads, validates and writes the n5-fangov TOML configuration
// (see DESIGN.md "Config"). The guiding rule is DESIGN rule 8: config errors
// never prevent start. Parse replaces every invalid value with its built-in
// default and reports it as a Warning; only a TOML syntax error is returned as
// an error, in which case the caller uses Default().
package config

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// Limits used by validation. Exported so the web UI and CLI can show them.
const (
	MinInterval = 2 * time.Second
	// MaxInterval: the unit runs with WatchdogSec=60 and the daemon pings
	// once per cycle (plus an independent 10 s ticker while the loop is
	// alive); two cycles must fit into the watchdog window. Larger values
	// are clamped to MaxInterval with a warning, not replaced by the default.
	MaxInterval   = 30 * time.Second
	MinCurvePts   = 2
	MaxCurvePts   = 8
	MinCurveTemp  = -20
	MaxCurveTemp  = 120
	MaxCritical   = 150
	MaxPWM        = 8
	MaxStallCycle = 20
	// MinStaleCycle: the stale check only runs on k10temp (sub-degree
	// resolution); even there a quiet system can hold one value for a
	// while, so fewer than 6 identical cycles must never count as frozen.
	MinStaleCycle = 6
	MaxStaleCycle = 600
	// MinCooldown keeps a restart loop or a flapping sensor from producing
	// one notification per cycle.
	MinCooldown = time.Minute
	MaxCooldown = 24 * time.Hour
	// MinFixedStop is the lowest fixed stop duty. A fixed stop is used
	// exactly where the chip does not regulate the channel itself any more
	// (N5 Pro pwm3): a stop duty of 0 would leave those fans off for good.
	MinFixedStop = 60
	// HDDStop is the stop duty used when a channel fed by drivetemp:max has
	// no or an invalid stop value (~2250 RPM on the N5 Pro HDD channel).
	HDDStop = "140"
	// HysteresisMax bounds [[channel]].hysteresis (degrees C; 0 = off).
	HysteresisMax = 10
	// MinOnMax bounds [[channel]].min_on (0 = off).
	MinOnMax = time.Hour
	// MaxSensorParts is how many sensor ids one channel may combine
	// (sensor = ["a", "b"] → the maximum of the parts).
	MaxSensorParts = 4
	// MinCeiling / MaxEmergencyCycles bound [[channel]] ceiling (the upper
	// bound is the sensor's built-in ceiling, sensor.BuiltinCeiling) and
	// [daemon] emergency_cycles (DESIGN "Ceilings and emergency").
	MinCeiling             = sensor.MinCeiling
	MaxEmergencyCycles     = 60
	DefaultEmergencyCycles = 6
)

// Profiles accepted in daemon.profile.
var Profiles = []string{"auto", "n5pro", "nct67xx", "it87xx", "monitor"}

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)  // same rule as web.channelName
	presetRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`) // same rule as web.presetName
	// UserRe is the web user name rule, shared with the account API and
	// the CLI (setup, passwd).
	UserRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

// Password length rule (runes) of the account API and the CLI.
const (
	MinPasswordLen = 8
	MaxPasswordLen = 128
)

// Daemon holds the regulation parameters.
type Daemon struct {
	Interval      time.Duration `toml:"interval"`
	StepUp        int           `toml:"step_up"`
	StepDown      int           `toml:"step_down"`
	StallMinDuty  int           `toml:"stall_min_duty"`
	StallCycles   int           `toml:"stall_cycles"`
	StaleCycles   int           `toml:"stale_cycles"`
	AlertCooldown time.Duration `toml:"alert_cooldown"`
	LogEvery      int           `toml:"log_every"`
	// EmergencyCommand runs (/bin/sh -c) once per ceiling episode after a
	// channel spent EmergencyCycles cycles at its ceiling with 0 RPM, or
	// three times as many regardless of RPM; "" = off.
	EmergencyCommand string `toml:"emergency_command"`
	EmergencyCycles  int    `toml:"emergency_cycles"`
	Profile          string `toml:"profile"`
}

// Web holds the HTTP listener settings.
type Web struct {
	Listen       string   `toml:"listen"`
	Auth         string   `toml:"auth"` // none | basic
	User         string   `toml:"user"`
	PasswordHash string   `toml:"password_hash"`           // sha256 hex of "user:password"
	AllowedHosts []string `toml:"allowed_hosts,omitempty"` // extra Host header values besides IPs/localhost; "*" disables the check
	TLS          string   `toml:"tls"`                     // auto | off | file (see TLSModes)
	CertFile     string   `toml:"cert_file"`               // tls = "file": PEM certificate (chain) path
	KeyFile      string   `toml:"key_file"`                // tls = "file": PEM private key path
	// BehindTLSProxy: the plain-HTTP listener is only reached through a
	// TLS-terminating reverse proxy; the session cookie is then marked
	// Secure although the daemon itself serves HTTP.
	BehindTLSProxy bool `toml:"behind_tls_proxy"`
}

// TLSModes lists the values accepted in web.tls. "auto" is a self-signed
// certificate the daemon creates and keeps under the config directory,
// "file" uses cert_file/key_file, "off" is plain HTTP. A non-loopback
// listener never runs "off": it is forced to "auto" with a warning (LAN
// traffic is never plain HTTP).
var TLSModes = []string{"auto", "off", "file"}

// Log holds the daemon's own log file settings. The journal (stdout) is
// always written; the file is an addition with size rotation.
type Log struct {
	File      string `toml:"file"`        // "" disables the file
	MaxSizeMB int    `toml:"max_size_mb"` // rotate above this size (1..100)
	MaxFiles  int    `toml:"max_files"`   // rotated files kept as .1..N (1..20)
}

// Limits of the [log] section.
const (
	DefaultLogFile = "/var/log/n5-fangov/n5-fangov.log"
	MinLogSizeMB   = 1
	MaxLogSizeMB   = 100
	MinLogFiles    = 1
	MaxLogFiles    = 20
	// LogRoot is the directory [log].file must live under: the daemon
	// runs as root and appends to that path, so the config must not be able
	// to point it at a device, another service's file or its own config.
	LogRoot = "/var/log/"
	// LogRootEnv overrides LogRoot for tests only (a temp dir).
	LogRootEnv = "N5FANGOV_LOG_ROOT"
)

// Alert holds the alert transport settings (v0.3: the alerts panel).
type Alert struct {
	Transport     string `toml:"transport"`      // auto | pve | mail | webhook | log | off (see AlertTransports)
	MailTo        string `toml:"mail_to"`        // mail transport only: local user or address
	WebhookURL    string `toml:"webhook_url"`    // webhook transport only: absolute http/https URL
	WebhookFormat string `toml:"webhook_format"` // json | text (see WebhookFormats)
}

// AlertTransports accepted in alert.transport. "auto" picks PVE::Notify
// when the Proxmox stack is present, else mail(1), else the log; "off"
// drops every alert (the journal line stays); "webhook" posts to
// webhook_url and is never chosen by "auto".
var AlertTransports = []string{"auto", "pve", "mail", "webhook", "log", "off"}

// WebhookFormats accepted in alert.webhook_format: "json" is the document
// Gotify and Home Assistant read, "text" the bare message for ntfy and
// plain receivers.
var WebhookFormats = []string{"json", "text"}

// DefaultWebhookFormat is the body format when webhook_format is absent
// or invalid.
const DefaultWebhookFormat = "json"

// MaxWebhookURLLen bounds alert.webhook_url.
const MaxWebhookURLLen = 2048

// ValidWebhookURL checks an alert.webhook_url value: an absolute http or
// https URL with a host, without userinfo (a key belongs into the query
// or a path segment, never into the authority), at most MaxWebhookURLLen
// bytes. The error text is meant to be appended to the value.
func ValidWebhookURL(s string) error {
	if s == "" {
		return errors.New("is empty")
	}
	if len(s) > MaxWebhookURLLen {
		return fmt.Errorf("is longer than %d bytes", MaxWebhookURLLen)
	}
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("is not a URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("is not an http or https URL")
	}
	if u.Host == "" || u.Hostname() == "" {
		return errors.New("has no host")
	}
	if u.User != nil {
		return errors.New("must not carry user:password (userinfo)")
	}
	return nil
}

// webhookURLOrigin is the scheme and host ("https://h.example.test:8443")
// of s for a warning about it — never the userinfo, path, query or
// fragment; "" when s has no scheme and host to name.
func webhookURLOrigin(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// DefaultMailTo is the mail recipient when mail_to is absent or invalid.
const DefaultMailTo = "root"

// mailToRe: a local user name or an address; no spaces, quotes or shell
// metacharacters (the value becomes an argv element of mail(1), never a
// shell string, but a recipient with spaces is a typo, not an address).
// The first character is never "-": a value like "-Sopt" would be
// parsed by mail(1) as an option (the sink also passes "--" before it).
var mailToRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._%+-]*(@[A-Za-z0-9.-]+)?$`)

// ValidMailTo reports whether s is acceptable as alert.mail_to.
func ValidMailTo(s string) bool { return len(s) <= 254 && mailToRe.MatchString(s) }

// Dashboard holds the dashboard-only settings: extra sensors recorded in
// the history for the chart (v0.3).
type Dashboard struct {
	Sensors []string `toml:"sensors"` // sensor ids, 0..MaxDashboardSensors
}

// MaxDashboardSensors caps [dashboard].sensors; more are dropped with a
// warning (each one costs a sysfs read per cycle and a chart series).
const MaxDashboardSensors = 8

// Channel is one regulated PWM output.
type Channel struct {
	Name string `toml:"name"`
	PWM  int    `toml:"pwm"`
	// Sensor is the canonical sensor id: one id, or several joined by ","
	// (the file may write them as an array; Sensors splits them again).
	Sensor   string   `toml:"sensor"`
	Curve    [][2]int `toml:"curve"` // [temp_c, duty], ascending temp
	Critical int      `toml:"critical"`
	Stop     string   `toml:"stop"` // "auto" or "0".."255"
	// Hysteresis in degrees C: the curve is evaluated at a held temperature
	// that follows the reading only when it moved by at least this much;
	// 0 = off. MinOn holds a rise of the curve target for that long; 0 =
	// off. Both are curve post-processing (DESIGN "Controller").
	Hysteresis int           `toml:"hysteresis,omitzero"`
	MinOn      time.Duration `toml:"min_on,omitzero"`
	// Ceiling lowers the built-in ceiling of the sensor (sensor.BuiltinCeiling)
	// when set; 0 = built-in. It can never raise it.
	Ceiling int `toml:"ceiling,omitzero"`
	// PostSet is true when the table carried a hysteresis or min_on key
	// (parser only; Marshal ignores it). The preset merge keeps the config
	// channel's post-processing when the preset channel says nothing about
	// it — a preset written before 0.3.1 must not reset it.
	PostSet bool `toml:"-"`
}

// Sensors returns the parts of the canonical sensor id (one element for a
// single id).
func (ch Channel) Sensors() []string {
	if ch.Sensor == "" {
		return nil
	}
	return strings.Split(ch.Sensor, ",")
}

// Schedule is one [[schedule]] table: a preset that applies in a daily
// window (From..To, local time, HH:MM) on the listed days, or — without
// From/To — the fallback that applies whenever no window matches. Days are
// the canonical lower-case names (DayNames); empty = every day.
type Schedule struct {
	Preset string   `toml:"preset"`
	From   string   `toml:"from"`
	To     string   `toml:"to"`
	Days   []string `toml:"days,omitempty"`
}

// Fallback reports whether the entry is the fallback (no window).
func (s Schedule) Fallback() bool { return s.From == "" && s.To == "" }

// DayNames are the values accepted in [[schedule]] days, Monday first.
var DayNames = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// MaxSchedules caps the [[schedule]] tables; further ones are dropped with
// a warning.
const MaxSchedules = 16

// Config is the whole configuration file.
type Config struct {
	Daemon    Daemon     `toml:"daemon"`
	Web       Web        `toml:"web"`
	Log       Log        `toml:"log"`
	Alert     Alert      `toml:"alert"`
	Dashboard Dashboard  `toml:"dashboard"`
	Channels  []Channel  `toml:"channel"`
	Schedules []Schedule `toml:"schedule,omitempty"`
}

// Warning describes one value that was replaced by its default (or dropped).
type Warning struct {
	Field string
	Msg   string
}

func (w Warning) String() string { return w.Field + ": " + w.Msg }

// Default returns the built-in defaults: daemon and web settings, no channels.
// N5ProChannels provides the verified channel set for the N5 Pro.
func Default() Config {
	return Config{
		Daemon: Daemon{
			Interval:        10 * time.Second,
			StepUp:          40,
			StepDown:        15,
			StallMinDuty:    60,
			StallCycles:     2,
			StaleCycles:     18,
			AlertCooldown:   30 * time.Minute,
			LogEvery:        30,
			EmergencyCycles: DefaultEmergencyCycles,
			Profile:         "auto",
		},
		Web: Web{
			Listen: "127.0.0.1:8010",
			Auth:   "none",
			TLS:    "off", // DefaultTLS of the loopback listen
		},
		Log: Log{
			File:      DefaultLogFile,
			MaxSizeMB: 5,
			MaxFiles:  5,
		},
		Alert: Alert{
			Transport:     "auto",
			MailTo:        DefaultMailTo,
			WebhookFormat: DefaultWebhookFormat,
		},
		Dashboard: Dashboard{Sensors: []string{}},
	}
}

// DefaultTLS is the tls mode used when the key is absent or invalid:
// "off" on a loopback listener, "auto" everywhere else.
func DefaultTLS(listen string) string {
	if IsLoopbackListen(listen) {
		return "off"
	}
	return "auto"
}

// N5ProChannels returns the channel set setup writes and SanitizeChannels
// adds on the Minisforum N5 Pro: the built-in preset n5pro-balanced, the one
// the dashboard marks as recommended (one source: the embedded TOML). Until
// 0.3.1-rc3 this was a separate literal set (the n5-fand values of
// 14.09.2026) that matched none of the three presets, so a fresh setup ran
// the HDD fan at 76 % at 42 C until a preset was applied (release-gate
// finding 5b, 2026-09-18). The literal below is the fallback should the
// embedded preset ever fail to parse — it is pinned by TestN5ProChannelsAreBalanced.
func N5ProChannels() []Channel {
	if p, ok := BuiltinPresetByName("n5pro-balanced"); ok {
		if chans, _, err := ParseChannels(p.Raw); err == nil && len(chans) == 3 {
			return chans
		}
	}
	return []Channel{
		{Name: "cpu", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{35, 60}, {60, 150}, {80, 255}}, Critical: 88, Stop: "auto"},
		{Name: "ssd", PWM: 2, Sensor: "nvme:max", Curve: [][2]int{{35, 74}, {55, 160}, {68, 255}}, Critical: 72, Stop: "auto"},
		{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{30, 87}, {42, 140}, {50, 200}, {55, 255}}, Critical: 60, Stop: "140"},
	}
}

// DefaultCurve is used when a channel's curve is invalid.
func DefaultCurve() [][2]int { return [][2]int{{45, 85}, {80, 255}} }

// Channel returns the channel with the given name, or nil.
func (c *Config) Channel(name string) *Channel {
	for i := range c.Channels {
		if c.Channels[i].Name == name {
			return &c.Channels[i]
		}
	}
	return nil
}

// Clone returns a deep copy.
func (c Config) Clone() Config {
	out := c
	out.Channels = CloneChannels(c.Channels)
	out.Web.AllowedHosts = append([]string(nil), c.Web.AllowedHosts...)
	out.Dashboard.Sensors = append([]string{}, c.Dashboard.Sensors...)
	out.Schedules = CloneSchedules(c.Schedules)
	return out
}

// CloneSchedules deep-copies a schedule slice.
func CloneSchedules(in []Schedule) []Schedule {
	if in == nil {
		return nil
	}
	out := make([]Schedule, len(in))
	for i, s := range in {
		out[i] = s
		out[i].Days = append([]string(nil), s.Days...)
	}
	return out
}

// CloneChannels deep-copies a channel slice.
func CloneChannels(in []Channel) []Channel {
	if in == nil {
		return nil
	}
	out := make([]Channel, len(in))
	for i, ch := range in {
		out[i] = ch
		out[i].Curve = append([][2]int(nil), ch.Curve...)
	}
	return out
}

// table decodes the top-level section name as a table; false (with a
// warning) when the key is absent or not a table. toml's map decoding
// silently yields an empty map for non-table values, so the TOML type is
// checked explicitly: "Hash" for a [name] header or an inline table, ""
// for a table that exists only implicitly through dotted keys
// (`web.user = …`) — that layout is valid TOML and was ignored before.
func (p *parser) table(top map[string]toml.Primitive, name string) (map[string]toml.Primitive, bool) {
	prim, ok := top[name]
	if !ok {
		return nil, false
	}
	var sec map[string]toml.Primitive
	if t := p.md.Type(name); (t != "Hash" && t != "") || p.md.PrimitiveDecode(prim, &sec) != nil {
		p.warn(name, "not a table, defaults used")
		return nil, false
	}
	return sec, true
}

// Parse decodes raw TOML. Invalid values are replaced by their defaults and
// reported as warnings; a channel whose identity (name, pwm, sensor) is
// unusable is dropped with a warning. Only a TOML syntax/structure error
// returns a non-nil error, together with Default().
func Parse(raw []byte) (Config, []Warning, error) {
	var top map[string]toml.Primitive
	md, err := toml.Decode(string(raw), &top)
	if err != nil {
		return Default(), []Warning{{Field: "toml", Msg: err.Error()}}, fmt.Errorf("config: %w", err)
	}
	p := &parser{md: md}
	cfg := Default()

	for _, k := range sortedKeys(top) {
		switch k {
		case "daemon", "web", "log", "alert", "dashboard", "channel", "schedule":
		default:
			p.warn(k, "unknown section, ignored")
		}
	}

	if sec, ok := p.table(top, "daemon"); ok {
		p.daemon(sec, &cfg.Daemon)
	}
	if sec, ok := p.table(top, "web"); ok {
		p.web(sec, &cfg.Web)
	}
	if sec, ok := p.table(top, "log"); ok {
		p.log(sec, &cfg.Log)
	}
	if sec, ok := p.table(top, "alert"); ok {
		p.alert(sec, &cfg.Alert)
	}
	if sec, ok := p.table(top, "dashboard"); ok {
		p.dashboard(sec, &cfg.Dashboard)
	}
	if prim, ok := top["channel"]; ok {
		var secs []map[string]toml.Primitive
		if md.Type("channel") != "ArrayHash" || md.PrimitiveDecode(prim, &secs) != nil {
			p.warn("channel", "not an array of tables ([[channel]]), no channels configured")
		} else {
			cfg.Channels = p.channels(secs)
		}
	}
	if prim, ok := top["schedule"]; ok {
		var secs []map[string]toml.Primitive
		if md.Type("schedule") != "ArrayHash" || md.PrimitiveDecode(prim, &secs) != nil {
			p.warn("schedule", "not an array of tables ([[schedule]]), no schedules configured")
		} else {
			cfg.Schedules = p.schedules(secs)
		}
	}
	return cfg, p.warns, nil
}

// ParseChannels decodes a file containing only [[channel]] tables (presets).
func ParseChannels(raw []byte) ([]Channel, []Warning, error) {
	cfg, warns, err := Parse(raw)
	if err != nil {
		return nil, warns, err
	}
	return cfg.Channels, warns, nil
}

// Marshal encodes cfg as TOML.
func Marshal(cfg Config) []byte {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	// Encode never fails for this plain struct.
	_ = enc.Encode(cfg)
	return numericStop(buf.Bytes())
}

// MarshalChannels encodes only [[channel]] tables (preset file format).
func MarshalChannels(chans []Channel) []byte {
	var buf bytes.Buffer
	_ = toml.NewEncoder(&buf).Encode(struct {
		Channels []Channel `toml:"channel"`
	}{chans})
	return numericStop(buf.Bytes())
}

// quotedStop matches a stop key whose value the encoder quoted although it
// is a duty ("140"); Channel.Stop is a string for the sake of "auto".
var quotedStop = regexp.MustCompile(`(?m)^([ \t]*stop[ \t]*=[ \t]*)"(\d+)"[ \t]*$`)

// numericStop writes a fixed stop duty as a TOML integer (stop = 140), the
// form the built-in presets, the example config and the dashboard use; only
// "auto" stays quoted. The parser accepts both (stop).
func numericStop(raw []byte) []byte { return quotedStop.ReplaceAll(raw, []byte("$1$2")) }

// ---- parser -------------------------------------------------------------

type parser struct {
	md    toml.MetaData
	warns []Warning
}

func (p *parser) warn(field, format string, args ...any) {
	p.warns = append(p.warns, Warning{Field: field, Msg: fmt.Sprintf(format, args...)})
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

func (p *parser) unknown(prefix string, sec map[string]toml.Primitive, known ...string) {
	for _, k := range sortedKeys(sec) {
		found := false
		for _, kn := range known {
			if k == kn {
				found = true
				break
			}
		}
		if !found {
			p.warn(prefix+"."+k, "unknown key, ignored")
		}
	}
}

// intField decodes an integer within [lo, hi]; on any problem the default is
// kept and a warning recorded.
func (p *parser) intField(prefix string, sec map[string]toml.Primitive, key string, def, lo, hi int) int {
	prim, ok := sec[key]
	if !ok {
		return def
	}
	var v int64
	if err := p.md.PrimitiveDecode(prim, &v); err != nil {
		p.warn(prefix+"."+key, "not an integer, default %d used", def)
		return def
	}
	if v < int64(lo) || v > int64(hi) {
		p.warn(prefix+"."+key, "%d outside %d..%d, default %d used", v, lo, hi, def)
		return def
	}
	return int(v)
}

func (p *parser) strField(prefix string, sec map[string]toml.Primitive, key, def string) (string, bool) {
	prim, ok := sec[key]
	if !ok {
		return def, false
	}
	var v string
	if err := p.md.PrimitiveDecode(prim, &v); err != nil {
		p.warn(prefix+"."+key, "not a string, default %q used", def)
		return def, false
	}
	return v, true
}

// durField accepts a duration string ("10s", "30m") or an integer (seconds);
// a value outside lo..hi yields the default with a warning.
func (p *parser) durField(prefix string, sec map[string]toml.Primitive, key string, def, lo, hi time.Duration) time.Duration {
	d, ok := p.durRaw(prefix, sec, key, def)
	if !ok {
		return d
	}
	if d < lo || d > hi {
		p.warn(prefix+"."+key, "%s outside %s..%s, default %s used", d, lo, hi, def)
		return def
	}
	return d
}

// durRaw decodes a duration string or integer seconds. ok is false when the
// key is absent or malformed (then def is returned, with a warning for the
// malformed case).
func (p *parser) durRaw(prefix string, sec map[string]toml.Primitive, key string, def time.Duration) (time.Duration, bool) {
	prim, ok := sec[key]
	if !ok {
		return def, false
	}
	var s string
	if err := p.md.PrimitiveDecode(prim, &s); err == nil {
		v, perr := time.ParseDuration(s)
		if perr != nil {
			p.warn(prefix+"."+key, "%q is not a duration, default %s used", s, def)
			return def, false
		}
		return v, true
	}
	var n int64
	if err := p.md.PrimitiveDecode(prim, &n); err != nil {
		p.warn(prefix+"."+key, "not a duration string or integer seconds, default %s used", def)
		return def, false
	}
	return time.Duration(n) * time.Second, true
}

func (p *parser) daemon(sec map[string]toml.Primitive, d *Daemon) {
	const pre = "daemon"
	p.unknown(pre, sec, "interval", "step_up", "step_down", "stall_min_duty", "stall_cycles",
		"stale_cycles", "alert_cooldown", "log_every", "emergency_command", "emergency_cycles", "profile")
	def := Default().Daemon
	// interval: below the minimum -> default; above MaxInterval -> clamped
	// (the operator wanted "slow", the watchdog window only allows 30 s).
	if iv, ok := p.durRaw(pre, sec, "interval", def.Interval); !ok {
		d.Interval = iv
	} else if iv < MinInterval {
		p.warn(pre+".interval", "%s below %s, default %s used", iv, MinInterval, def.Interval)
		d.Interval = def.Interval
	} else if iv > MaxInterval {
		p.warn(pre+".interval", "%s above %s (WatchdogSec=60 needs two cycles), %s used", iv, MaxInterval, MaxInterval)
		d.Interval = MaxInterval
	} else {
		d.Interval = iv
	}
	d.StepUp = p.intField(pre, sec, "step_up", def.StepUp, 1, 255)
	d.StepDown = p.intField(pre, sec, "step_down", def.StepDown, 1, 255)
	d.StallMinDuty = p.intField(pre, sec, "stall_min_duty", def.StallMinDuty, 1, 255)
	d.StallCycles = p.intField(pre, sec, "stall_cycles", def.StallCycles, 1, MaxStallCycle)
	d.StaleCycles = p.intField(pre, sec, "stale_cycles", def.StaleCycles, MinStaleCycle, MaxStaleCycle)
	d.AlertCooldown = p.durField(pre, sec, "alert_cooldown", def.AlertCooldown, MinCooldown, MaxCooldown)
	d.LogEvery = p.intField(pre, sec, "log_every", def.LogEvery, 0, 1000000)
	d.EmergencyCommand, _ = p.strField(pre, sec, "emergency_command", def.EmergencyCommand)
	d.EmergencyCycles = p.intField(pre, sec, "emergency_cycles", def.EmergencyCycles, 1, MaxEmergencyCycles)
	prof, _ := p.strField(pre, sec, "profile", def.Profile)
	prof = enumValue(prof)
	if !contains(Profiles, prof) {
		p.warn(pre+".profile", "%q unknown (%s), default %q used", prof, strings.Join(Profiles, "|"), def.Profile)
		prof = def.Profile
	}
	d.Profile = prof
}

func (p *parser) web(sec map[string]toml.Primitive, w *Web) {
	const pre = "web"
	p.unknown(pre, sec, "listen", "auth", "user", "password_hash", "allowed_hosts", "tls", "cert_file", "key_file", "behind_tls_proxy")
	def := Default().Web
	listen, _ := p.strField(pre, sec, "listen", def.Listen)
	listen = strings.TrimSpace(listen)
	if _, port, err := net.SplitHostPort(listen); err != nil {
		p.warn(pre+".listen", "%q is not host:port, default %q used", listen, def.Listen)
		listen = def.Listen
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		p.warn(pre+".listen", "%q: port must be 1..65535, default %q used", listen, def.Listen)
		listen = def.Listen
	}
	auth, _ := p.strField(pre, sec, "auth", def.Auth)
	auth = enumValue(auth)
	// authBroken: the operator asked for something other than plain "none"
	// and did not get it. Such a config must not end up reachable from the
	// network without auth (no fail-open).
	authBroken := false
	if auth != "none" && auth != "basic" {
		p.warn(pre+".auth", "%q unknown (none|basic), default %q used", auth, def.Auth)
		auth = def.Auth
		authBroken = true
	}
	w.User, _ = p.strField(pre, sec, "user", "")
	w.PasswordHash, _ = p.strField(pre, sec, "password_hash", "")
	if auth == "basic" {
		if w.User == "" || w.PasswordHash == "" {
			p.warn(pre+".auth", "basic requires user and password_hash, auth set to none")
			auth = "none"
			authBroken = true
		} else if !UserRe.MatchString(w.User) {
			// the same rule the account API and the CLI apply; a name the
			// login form cannot send must not leave the file half-usable
			p.warn(pre+".user", "%q does not match %s, auth set to none", w.User, UserRe)
			auth = "none"
			authBroken = true
		} else if _, err := ParsePasswordHash(w.PasswordHash); err != nil {
			p.warn(pre+".password_hash", "%v, auth set to none", err)
			auth = "none"
			authBroken = true
		}
	}
	if authBroken && !IsLoopbackListen(listen) {
		p.warn(pre+".listen", "auth misconfigured — web bound to loopback (%q instead of %q)", def.Listen, listen)
		listen = def.Listen
	}
	w.Listen = listen
	w.Auth = auth
	if prim, ok := sec["allowed_hosts"]; ok {
		var hosts []string
		if err := p.md.PrimitiveDecode(prim, &hosts); err != nil {
			p.warn(pre+".allowed_hosts", "not an array of strings, ignored")
		} else {
			for _, h := range hosts {
				if h = strings.TrimSpace(h); h != "" {
					w.AllowedHosts = append(w.AllowedHosts, h)
				}
			}
		}
	}
	p.tls(sec, w)
	w.BehindTLSProxy = p.boolField(pre, sec, "behind_tls_proxy", def.BehindTLSProxy)
}

// boolField decodes a boolean; on any problem the default is kept and a
// warning recorded.
func (p *parser) boolField(prefix string, sec map[string]toml.Primitive, key string, def bool) bool {
	prim, ok := sec[key]
	if !ok {
		return def
	}
	var v bool
	if err := p.md.PrimitiveDecode(prim, &v); err != nil {
		p.warn(prefix+"."+key, "not a boolean, default %v used", def)
		return def
	}
	return v
}

// tls validates web.tls after listen is final: the default depends on it
// and a non-loopback listener is never left on plain HTTP.
func (p *parser) tls(sec map[string]toml.Primitive, w *Web) {
	const pre = "web"
	def := DefaultTLS(w.Listen)
	mode, present := p.strField(pre, sec, "tls", def)
	mode = enumValue(mode)
	w.CertFile, _ = p.strField(pre, sec, "cert_file", "")
	w.KeyFile, _ = p.strField(pre, sec, "key_file", "")
	w.CertFile = strings.TrimSpace(w.CertFile)
	w.KeyFile = strings.TrimSpace(w.KeyFile)
	if present && !contains(TLSModes, mode) {
		p.warn(pre+".tls", "%q unknown (%s), %q used", mode, strings.Join(TLSModes, "|"), def)
		mode = def
	}
	if mode == "file" && (w.CertFile == "" || w.KeyFile == "") {
		p.warn(pre+".tls", "\"file\" needs cert_file and key_file, %q used", def)
		mode = def
	}
	if mode == "off" && !IsLoopbackListen(w.Listen) {
		p.warn(pre+".tls", "\"off\" on non-loopback listen %s: LAN traffic is never plain HTTP, \"auto\" used", w.Listen)
		mode = "auto"
	}
	w.TLS = mode
}

func (p *parser) log(sec map[string]toml.Primitive, l *Log) {
	const pre = "log"
	p.unknown(pre, sec, "file", "max_size_mb", "max_files")
	def := Default().Log
	file, present := p.strField(pre, sec, "file", def.File)
	file = strings.TrimSpace(file)
	if present && file != "" {
		if err := ValidLogPath(file); err != nil {
			p.warn(pre+".file", "%q %v, default %q used", file, err, def.File)
			file = def.File
		}
	}
	l.File = file
	l.MaxSizeMB = p.intField(pre, sec, "max_size_mb", def.MaxSizeMB, MinLogSizeMB, MaxLogSizeMB)
	l.MaxFiles = p.intField(pre, sec, "max_files", def.MaxFiles, MinLogFiles, MaxLogFiles)
}

func (p *parser) alert(sec map[string]toml.Primitive, a *Alert) {
	const pre = "alert"
	p.unknown(pre, sec, "transport", "mail_to", "webhook_url", "webhook_format")
	def := Default().Alert
	tr, _ := p.strField(pre, sec, "transport", def.Transport)
	tr = enumValue(tr)
	if !contains(AlertTransports, tr) {
		p.warn(pre+".transport", "%q unknown (%s), default %q used", tr, strings.Join(AlertTransports, "|"), def.Transport)
		tr = def.Transport
	}
	to, present := p.strField(pre, sec, "mail_to", def.MailTo)
	to = strings.TrimSpace(to)
	if present && !ValidMailTo(to) {
		p.warn(pre+".mail_to", "%q is not a local user or address, default %q used", to, def.MailTo)
		to = def.MailTo
	}
	a.MailTo = to
	// webhook_url: an invalid value is dropped (""), and a transport that
	// needs it falls back to auto — rule 8, the alert still goes somewhere.
	wu, present := p.strField(pre, sec, "webhook_url", "")
	wu = strings.TrimSpace(wu)
	if present && wu != "" {
		if err := ValidWebhookURL(wu); err != nil {
			// The warning reaches the journal and the dashboard: never the
			// value itself (it may carry a key in the userinfo or query),
			// only its origin.
			what := "value"
			if o := webhookURLOrigin(wu); o != "" {
				what = o + "/…"
			}
			p.warn(pre+".webhook_url", "%s %v, ignored", what, err)
			wu = ""
		}
	}
	a.WebhookURL = wu
	wf, present := p.strField(pre, sec, "webhook_format", def.WebhookFormat)
	wf = enumValue(wf)
	if wf == "" {
		// Marshal writes every key; an empty format is "not set".
		wf = def.WebhookFormat
	}
	if present && !contains(WebhookFormats, wf) {
		p.warn(pre+".webhook_format", "%q unknown (%s), default %q used", wf, strings.Join(WebhookFormats, "|"), def.WebhookFormat)
		wf = def.WebhookFormat
	}
	a.WebhookFormat = wf
	if tr == "webhook" && a.WebhookURL == "" {
		p.warn(pre+".transport", "\"webhook\" needs a valid webhook_url, %q used", def.Transport)
		tr = def.Transport
	}
	a.Transport = tr
}

// dashboard validates [dashboard].sensors: an array of at most
// MaxDashboardSensors non-empty strings. Only the shape is checked here;
// whether an id resolves on this machine is the controller's business
// (an unresolvable id is kept in the file and skipped with a warning).
func (p *parser) dashboard(sec map[string]toml.Primitive, d *Dashboard) {
	const pre = "dashboard"
	p.unknown(pre, sec, "sensors")
	d.Sensors = []string{}
	prim, ok := sec["sensors"]
	if !ok {
		return
	}
	var ids []string
	if err := p.md.PrimitiveDecode(prim, &ids); err != nil {
		p.warn(pre+".sensors", "not an array of strings, ignored")
		return
	}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if len(d.Sensors) >= MaxDashboardSensors {
			p.warn(pre+".sensors", "more than %d ids, %q and the rest dropped", MaxDashboardSensors, id)
			break
		}
		seen[id] = true
		d.Sensors = append(d.Sensors, id)
	}
}

// logRoot is LogRoot, or the test override, always with a trailing slash.
func logRoot() string {
	root := LogRoot
	if r := os.Getenv(LogRootEnv); r != "" {
		root = filepath.ToSlash(r)
	}
	if !strings.HasSuffix(root, "/") {
		root += "/"
	}
	return root
}

// ValidLogPath checks a [log].file value: absolute, in clean form (no "..",
// "." or doubled slashes, so the prefix check cannot be escaped), a file
// name under LogRoot. The error text is meant to be appended to the value.
func ValidLogPath(p string) error {
	if !strings.HasPrefix(p, "/") {
		return errors.New("is not an absolute path")
	}
	if strings.HasSuffix(p, "/") {
		return errors.New("is a directory, not a file")
	}
	if path.Clean(p) != p {
		return errors.New("is not a clean path (\"..\", \".\" or repeated slashes)")
	}
	root := logRoot()
	if !strings.HasPrefix(p, root) || len(p) == len(root) {
		return fmt.Errorf("is not a file under %s", root)
	}
	return nil
}

// IsLoopbackListen reports whether a host:port binds only to the loopback
// interface (127.0.0.0/8, ::1, localhost). An empty host means all
// interfaces and is not loopback.
func IsLoopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// PasswordHash is a parsed [web].password_hash value in one of the two
// stored forms:
//
//	<64 hex>                              legacy: sha256("user:password")
//	pbkdf2$<iter>$<salt hex>$<key hex>    PBKDF2-HMAC-SHA256 of the password
//
// The legacy form keeps working; `n5-fangov passwd` writes the PBKDF2 form.
// Verification (internal/web) uses the fields; this package only checks
// the syntax.
type PasswordHash struct {
	// Legacy is the 32-byte sha256 digest, nil for the PBKDF2 form.
	Legacy []byte
	// Iter, Salt, Key describe the PBKDF2 form (Key is the derived key).
	Iter int
	Salt []byte
	Key  []byte
}

// PBKDF2 parameters accepted (and, for Iter, produced by the writers).
const (
	PBKDF2Prefix  = "pbkdf2"
	PBKDF2Iter    = 210000 // OWASP 2023 minimum for HMAC-SHA256
	PBKDF2MinIter = 1000
	PBKDF2MaxIter = 10000000
	PBKDF2KeyLen  = 32
	PBKDF2SaltLen = 16
)

// ParsePasswordHash checks the syntax of a stored hash (see PasswordHash).
func ParsePasswordHash(s string) (PasswordHash, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, PBKDF2Prefix+"$") {
		parts := strings.Split(s, "$")
		if len(parts) != 4 {
			return PasswordHash{}, errors.New("pbkdf2 hash needs pbkdf2$<iter>$<salt>$<key>")
		}
		iter, err := strconv.Atoi(parts[1])
		if err != nil || iter < PBKDF2MinIter || iter > PBKDF2MaxIter {
			return PasswordHash{}, fmt.Errorf("pbkdf2 iterations %q outside %d..%d", parts[1], PBKDF2MinIter, PBKDF2MaxIter)
		}
		salt, err := hex.DecodeString(parts[2])
		if err != nil || len(salt) < 8 {
			return PasswordHash{}, errors.New("pbkdf2 salt is not hex of at least 8 bytes")
		}
		key, err := hex.DecodeString(parts[3])
		if err != nil || len(key) != PBKDF2KeyLen {
			return PasswordHash{}, fmt.Errorf("pbkdf2 key is not hex of %d bytes", PBKDF2KeyLen)
		}
		return PasswordHash{Iter: iter, Salt: salt, Key: key}, nil
	}
	if len(s) != 64 {
		return PasswordHash{}, errors.New("not a 64-char sha256 hex digest or a pbkdf2$ hash")
	}
	legacy, err := hex.DecodeString(s)
	if err != nil {
		return PasswordHash{}, errors.New("not a 64-char sha256 hex digest or a pbkdf2$ hash")
	}
	return PasswordHash{Legacy: legacy}, nil
}

func (p *parser) channels(secs []map[string]toml.Primitive) []Channel {
	var out []Channel
	names := map[string]bool{}
	pwms := map[int]bool{}
	for i, sec := range secs {
		pre := fmt.Sprintf("channel[%d]", i)
		var ch Channel
		name, ok := p.strField(pre, sec, "name", "")
		if ok && nameRe.MatchString(name) {
			pre = "channel." + name
		}
		p.unknown(pre, sec, "name", "pwm", "sensor", "curve", "critical", "stop", "hysteresis", "min_on", "ceiling")
		if !ok || !nameRe.MatchString(name) {
			p.warn(pre+".name", "missing or not [a-z0-9_]{1,32}, channel dropped")
			continue
		}
		if names[name] {
			p.warn(pre+".name", "duplicate name, channel dropped")
			continue
		}
		ch.Name = name
		// pwm has no default: one warning names the problem and drops the
		// channel (intField's "default 0 used" line would be a second one).
		ch.PWM = p.pwmField(pre, sec)
		if ch.PWM == 0 {
			continue
		}
		if pwms[ch.PWM] {
			p.warn(pre+".pwm", "pwm%d already used by another channel, channel dropped", ch.PWM)
			continue
		}
		sensor, ok := p.sensorField(pre, sec)
		if !ok {
			continue
		}
		ch.Sensor = sensor
		ch.Curve = p.curve(pre, sec)
		last := ch.Curve[len(ch.Curve)-1][0]
		defCrit := last + 10
		if defCrit > MaxCritical {
			defCrit = MaxCritical
		}
		ch.Critical = p.intField(pre, sec, "critical", defCrit, last+1, MaxCritical)
		if _, has := sec["critical"]; !has {
			p.warn(pre+".critical", "missing, default %d used", defCrit)
		}
		ch.Stop = p.stop(pre, sec, ch.Sensor)
		ch.Hysteresis = p.intField(pre, sec, "hysteresis", 0, 0, HysteresisMax)
		ch.MinOn = p.durField(pre, sec, "min_on", 0, 0, MinOnMax)
		// ceiling may only lower the built-in one; the parser has no sysfs, so
		// disk:<dev> ids validate against the default (the daemon applies
		// min(configured, built-in) anyway)
		ch.Ceiling = p.ceiling(pre, sec, ch.Sensor)
		_, hasHyst := sec["hysteresis"]
		_, hasMinOn := sec["min_on"]
		ch.PostSet = hasHyst || hasMinOn
		names[name] = true
		pwms[ch.PWM] = true
		out = append(out, ch)
	}
	return out
}

// sensorField decodes the mandatory sensor id: a string ("k10temp", or
// several ids joined by ",") or an array of 1..MaxSensorParts strings.
// Every part is trimmed and must be non-empty and distinct; the result is
// the canonical comma-joined id. ok is false (with one warning) when the
// key is missing or unusable — the channel is then dropped, a sensor has
// no default.
func (p *parser) sensorField(pre string, sec map[string]toml.Primitive) (string, bool) {
	prim, ok := sec["sensor"]
	if !ok {
		p.warn(pre+".sensor", "missing, channel dropped")
		return "", false
	}
	var parts []string
	var s string
	if err := p.md.PrimitiveDecode(prim, &s); err == nil {
		parts = strings.Split(s, ",")
	} else if err := p.md.PrimitiveDecode(prim, &parts); err != nil {
		p.warn(pre+".sensor", "not a string or an array of strings, channel dropped")
		return "", false
	}
	id, err := JoinSensors(parts)
	if err != nil {
		p.warn(pre+".sensor", "%v, channel dropped", err)
		return "", false
	}
	return id, true
}

// JoinSensors validates 1..MaxSensorParts sensor ids (trimmed, non-empty,
// without ",", distinct) and returns the canonical comma-joined id.
func JoinSensors(parts []string) (string, error) {
	if len(parts) == 0 {
		return "", errors.New("no sensor id")
	}
	if len(parts) > MaxSensorParts {
		return "", fmt.Errorf("%d sensor ids, at most %d", len(parts), MaxSensorParts)
	}
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for i, part := range parts {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
			return "", fmt.Errorf("sensor id %d is empty", i+1)
		case strings.Contains(part, ","):
			return "", fmt.Errorf("sensor id %q contains a comma", part)
		case seen[part]:
			return "", fmt.Errorf("sensor id %q listed twice", part)
		}
		seen[part] = true
		out = append(out, part)
	}
	return strings.Join(out, ","), nil
}

// pwmField decodes the mandatory pwm index (1..MaxPWM); 0 with one warning
// when it is missing or unusable.
func (p *parser) pwmField(pre string, sec map[string]toml.Primitive) int {
	prim, ok := sec["pwm"]
	if !ok {
		p.warn(pre+".pwm", "missing, channel dropped")
		return 0
	}
	var v int64
	if err := p.md.PrimitiveDecode(prim, &v); err != nil {
		p.warn(pre+".pwm", "not an integer, channel dropped")
		return 0
	}
	if v < 1 || v > MaxPWM {
		p.warn(pre+".pwm", "%d outside 1..%d, channel dropped", v, MaxPWM)
		return 0
	}
	return int(v)
}

// schedules validates the [[schedule]] tables (DESIGN "Schedule rules"):
// at most MaxSchedules entries, a preset name, either both from/to as
// HH:MM (from == to is dropped) or neither (the fallback, at most one),
// days a distinct subset of DayNames. An invalid entry is dropped with a
// warning and the others stay (rule 8); the order is kept because the
// first matching window wins.
func (p *parser) schedules(secs []map[string]toml.Primitive) []Schedule {
	var out []Schedule
	haveFallback := false
	for i, sec := range secs {
		pre := fmt.Sprintf("schedule[%d]", i)
		if len(out) >= MaxSchedules {
			p.warn(pre, "more than %d schedule entries, this one and the rest dropped", MaxSchedules)
			break
		}
		p.unknown(pre, sec, "preset", "from", "to", "days")
		preset, _ := p.strField(pre, sec, "preset", "")
		preset = strings.TrimSpace(preset)
		if !presetRe.MatchString(preset) {
			p.warn(pre+".preset", "missing or not %s, entry dropped", presetRe)
			continue
		}
		s := Schedule{Preset: preset}
		// from/to must be strings: a bare TOML time literal (from = 22:00)
		// decodes as a time, not a string — that entry is dropped, it must
		// never turn into the fallback by looking absent.
		from, fromOK := p.clockField(sec, "from")
		to, toOK := p.clockField(sec, "to")
		if !fromOK || !toOK {
			p.warn(pre, "from/to must be strings (\"22:00\"), entry dropped")
			continue
		}
		switch {
		case from == "" && to == "":
			if haveFallback {
				p.warn(pre, "a second entry without from/to (fallback), entry dropped")
				continue
			}
			haveFallback = true
			if _, ok := sec["days"]; ok {
				// the fallback applies whenever no window matches, on any day
				p.warn(pre+".days", "days ignored on the fallback")
				out = append(out, s)
				continue
			}
		case from == "" || to == "":
			p.warn(pre, "from and to must both be set (or neither for the fallback), entry dropped")
			continue
		default:
			var ok bool
			if s.From, ok = clockTime(from); !ok {
				p.warn(pre+".from", "%q is not HH:MM, entry dropped", from)
				continue
			}
			if s.To, ok = clockTime(to); !ok {
				p.warn(pre+".to", "%q is not HH:MM, entry dropped", to)
				continue
			}
			if s.From == s.To {
				p.warn(pre, "from and to are equal (%s), entry dropped", s.From)
				continue
			}
		}
		if prim, ok := sec["days"]; ok {
			var days []string
			if err := p.md.PrimitiveDecode(prim, &days); err != nil {
				p.warn(pre+".days", "not an array of strings, entry dropped")
				continue
			}
			bad := false
			seen := map[string]bool{}
			for _, d := range days {
				d = enumValue(d)
				if !contains(DayNames, d) {
					p.warn(pre+".days", "%q is not one of %s, entry dropped", d, strings.Join(DayNames, "|"))
					bad = true
					break
				}
				if seen[d] {
					p.warn(pre+".days", "%q listed twice, duplicate ignored", d)
					continue
				}
				seen[d] = true
				s.Days = append(s.Days, d)
			}
			if bad {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// clockField reads the from/to key of a [[schedule]] table as a trimmed
// string: "" when absent, ok = false when present but not a string (a
// bare TOML time literal, an integer).
func (p *parser) clockField(sec map[string]toml.Primitive, key string) (string, bool) {
	prim, ok := sec[key]
	if !ok {
		return "", true
	}
	var v string
	if err := p.md.PrimitiveDecode(prim, &v); err != nil {
		return "", false
	}
	return strings.TrimSpace(v), true
}

// clockTime parses a wall-clock time "HH:MM" (24 h) and returns it in the
// canonical two-digit form.
func clockTime(s string) (string, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return "", false
	}
	return t.Format("15:04"), true
}

func (p *parser) curve(pre string, sec map[string]toml.Primitive) [][2]int {
	prim, ok := sec["curve"]
	if !ok {
		p.warn(pre+".curve", "missing, default curve used")
		return DefaultCurve()
	}
	var pts [][]int64
	if err := p.md.PrimitiveDecode(prim, &pts); err != nil {
		p.warn(pre+".curve", "not an array of [temp, duty] pairs, default curve used")
		return DefaultCurve()
	}
	out, err := ValidateCurve(pts)
	if err != nil {
		p.warn(pre+".curve", "%v, default curve used", err)
		return DefaultCurve()
	}
	return out
}

// ValidateCurve checks 2..8 [temp, duty] points with strictly ascending temps
// in -20..120 and non-decreasing duties in 0..255.
func ValidateCurve(pts [][]int64) ([][2]int, error) {
	if len(pts) < MinCurvePts || len(pts) > MaxCurvePts {
		return nil, fmt.Errorf("%d points, need %d..%d", len(pts), MinCurvePts, MaxCurvePts)
	}
	out := make([][2]int, len(pts))
	for i, pt := range pts {
		if len(pt) != 2 {
			return nil, fmt.Errorf("point %d has %d values, need [temp, duty]", i, len(pt))
		}
		t, d := pt[0], pt[1]
		if t < MinCurveTemp || t > MaxCurveTemp {
			return nil, fmt.Errorf("point %d temp %d outside %d..%d", i, t, MinCurveTemp, MaxCurveTemp)
		}
		if d < 0 || d > 255 {
			return nil, fmt.Errorf("point %d duty %d outside 0..255", i, d)
		}
		if i > 0 {
			if t <= pts[i-1][0] {
				return nil, fmt.Errorf("point %d temp %d not above previous %d", i, t, pts[i-1][0])
			}
			if d < pts[i-1][1] {
				return nil, fmt.Errorf("point %d duty %d below previous %d", i, d, pts[i-1][1])
			}
		}
		out[i] = [2]int{int(t), int(d)}
	}
	return out, nil
}

// DefaultStop is the stop value used when a channel has none or an invalid
// one: "auto" hands the channel back to the chip, except for channels fed by
// drivetemp:max (alone or as a part of a composite id), whose fans must
// keep turning even when nobody regulates them (HDDStop).
func DefaultStop(sensor string) string {
	for _, part := range (Channel{Sensor: sensor}).Sensors() {
		if strings.TrimSpace(part) == "drivetemp:max" {
			return HDDStop
		}
	}
	return "auto"
}

// stop parses the stop value: "auto", a duty string or an integer. Invalid
// values fall back to DefaultStop(sensor) with a warning; a fixed duty
// below MinFixedStop is raised to MinFixedStop with a warning.
func (p *parser) stop(pre string, sec map[string]toml.Primitive, sensor string) string {
	def := DefaultStop(sensor)
	prim, ok := sec["stop"]
	if !ok {
		return def
	}
	var n int64
	var s string
	if err := p.md.PrimitiveDecode(prim, &s); err == nil {
		if enumValue(s) == "auto" {
			return "auto"
		}
		v, perr := strconv.Atoi(strings.TrimSpace(s))
		if perr != nil || v < 0 || v > 255 {
			p.warn(pre+".stop", "%q is neither \"auto\" nor 0..255, %q used", s, def)
			return def
		}
		n = int64(v)
	} else if err := p.md.PrimitiveDecode(prim, &n); err != nil || n < 0 || n > 255 {
		p.warn(pre+".stop", "neither \"auto\" nor 0..255, %q used", def)
		return def
	}
	if n < MinFixedStop {
		p.warn(pre+".stop", "fixed stop duty %d below %d (fans would stay off after stop), %d used", n, MinFixedStop, MinFixedStop)
		n = MinFixedStop
	}
	return strconv.FormatInt(n, 10)
}

// enumValue normalises an enumeration key (profile, auth, tls, transport,
// stop): surrounding blanks and letter case do not make a value unknown.
func enumValue(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// StopDuty returns the fixed stop duty of a channel and true, or false for "auto".
func (ch Channel) StopDuty() (int, bool) {
	if ch.Stop == "auto" || ch.Stop == "" {
		return 0, false
	}
	n, err := strconv.Atoi(ch.Stop)
	if err != nil {
		return 0, false
	}
	return n, true
}

// ceiling parses [[channel]] ceiling: MinCeiling..the sensor's built-in
// ceiling (sensor.BuiltinCeiling without a sysfs resolver, so disk:<dev>
// ids validate against the default). A value above it, or not an integer,
// yields 0 (= built-in) with a warning: the key can only lower the
// ceiling, never raise it.
func (p *parser) ceiling(pre string, sec map[string]toml.Primitive, sensorID string) int {
	prim, ok := sec["ceiling"]
	if !ok {
		return 0
	}
	built := sensor.BuiltinCeiling(sensorID, nil)
	var v int64
	if err := p.md.PrimitiveDecode(prim, &v); err != nil {
		p.warn(pre+".ceiling", "not an integer, built-in ceiling %d used", built)
		return 0
	}
	if v < MinCeiling || v > int64(built) {
		p.warn(pre+".ceiling", "%d outside %d..%d (the built-in ceiling of %s can only be lowered), built-in ceiling %d used", v, MinCeiling, built, sensorID, built)
		return 0
	}
	return int(v)
}
