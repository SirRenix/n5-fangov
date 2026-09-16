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
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
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
)

// Profiles accepted in daemon.profile.
var Profiles = []string{"auto", "n5pro", "nct67xx", "it87xx", "monitor"}

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9_]+$`)
	presetRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`) // same rule as web.presetName
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
	Profile       string        `toml:"profile"`
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

// TLS modes accepted in web.tls. "auto" is a self-signed certificate the
// daemon creates and keeps under the config directory, "file" uses
// cert_file/key_file, "off" is plain HTTP. A non-loopback listener never
// runs "off": it is forced to "auto" with a warning (LAN traffic is never
// plain HTTP).
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
	// LogRoot is the directory [log].file must live under (H2): the daemon
	// runs as root and appends to that path, so the config must not be able
	// to point it at a device, another service's file or its own config.
	LogRoot = "/var/log/"
	// LogRootEnv overrides LogRoot for tests only (a temp dir).
	LogRootEnv = "N5FANGOV_LOG_ROOT"
)

// Alert holds the alert transport settings (v0.3: the alerts panel).
type Alert struct {
	Transport string `toml:"transport"` // auto | pve | mail | log | off (see AlertTransports)
	MailTo    string `toml:"mail_to"`   // mail transport only: local user or address
}

// AlertTransports accepted in alert.transport. "auto" picks PVE::Notify
// when the Proxmox stack is present, else mail(1), else the log; "off"
// drops every alert (the journal line stays).
var AlertTransports = []string{"auto", "pve", "mail", "log", "off"}

// DefaultMailTo is the mail recipient when mail_to is absent or invalid.
const DefaultMailTo = "root"

// mailToRe: a local user name or an address; no spaces, quotes or shell
// metacharacters (the value becomes an argv element of mail(1), never a
// shell string, but a recipient with spaces is a typo, not an address).
// The first character is never "-" (R-M3): a value like "-Sopt" would be
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
	Name     string   `toml:"name"`
	PWM      int      `toml:"pwm"`
	Sensor   string   `toml:"sensor"`
	Curve    [][2]int `toml:"curve"` // [temp_c, duty], ascending temp
	Critical int      `toml:"critical"`
	Stop     string   `toml:"stop"` // "auto" or "0".."255"
}

// Config is the whole configuration file.
type Config struct {
	Daemon    Daemon    `toml:"daemon"`
	Web       Web       `toml:"web"`
	Log       Log       `toml:"log"`
	Alert     Alert     `toml:"alert"`
	Dashboard Dashboard `toml:"dashboard"`
	Channels  []Channel `toml:"channel"`
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
			Interval:      10 * time.Second,
			StepUp:        40,
			StepDown:      15,
			StallMinDuty:  60,
			StallCycles:   2,
			StaleCycles:   18,
			AlertCooldown: 30 * time.Minute,
			LogEvery:      30,
			Profile:       "auto",
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
			Transport: "auto",
			MailTo:    DefaultMailTo,
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

// N5ProChannels returns the channel set verified on the Minisforum N5 Pro
// (values from n5pro-ec/deploy/n5-fand.conf, measured 14.09.2026).
func N5ProChannels() []Channel {
	return []Channel{
		{Name: "cpu", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 88, Stop: "auto"},
		{Name: "ssd", PWM: 2, Sensor: "nvme:max", Curve: [][2]int{{48, 74}, {65, 255}}, Critical: 72, Stop: "auto"},
		{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{36, 105}, {46, 255}}, Critical: 56, Stop: "140"},
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
// (`web.user = …`) — that layout is valid TOML and was ignored before
// (R-L11).
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
		case "daemon", "web", "log", "alert", "dashboard", "channel":
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
	return buf.Bytes()
}

// MarshalChannels encodes only [[channel]] tables (preset file format).
func MarshalChannels(chans []Channel) []byte {
	var buf bytes.Buffer
	_ = toml.NewEncoder(&buf).Encode(struct {
		Channels []Channel `toml:"channel"`
	}{chans})
	return buf.Bytes()
}

// ---- parser -------------------------------------------------------------

type parser struct {
	md    toml.MetaData
	warns []Warning
}

func (p *parser) warn(field, format string, args ...any) {
	p.warns = append(p.warns, Warning{Field: field, Msg: fmt.Sprintf(format, args...)})
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

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
		"stale_cycles", "alert_cooldown", "log_every", "profile")
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
	prof, _ := p.strField(pre, sec, "profile", def.Profile)
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
	if _, _, err := net.SplitHostPort(listen); err != nil {
		p.warn(pre+".listen", "%q is not host:port, default %q used", listen, def.Listen)
		listen = def.Listen
	}
	auth, _ := p.strField(pre, sec, "auth", def.Auth)
	// authBroken: the operator asked for something other than plain "none"
	// and did not get it. Such a config must not end up reachable from the
	// network without auth (H2: no fail-open).
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
	mode = strings.TrimSpace(mode)
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
	p.unknown(pre, sec, "transport", "mail_to")
	def := Default().Alert
	tr, _ := p.strField(pre, sec, "transport", def.Transport)
	tr = strings.ToLower(strings.TrimSpace(tr))
	if !contains(AlertTransports, tr) {
		p.warn(pre+".transport", "%q unknown (%s), default %q used", tr, strings.Join(AlertTransports, "|"), def.Transport)
		tr = def.Transport
	}
	a.Transport = tr
	to, present := p.strField(pre, sec, "mail_to", def.MailTo)
	to = strings.TrimSpace(to)
	if present && !ValidMailTo(to) {
		p.warn(pre+".mail_to", "%q is not a local user or address, default %q used", to, def.MailTo)
		to = def.MailTo
	}
	a.MailTo = to
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
		p.unknown(pre, sec, "name", "pwm", "sensor", "curve", "critical", "stop")
		if !ok || !nameRe.MatchString(name) {
			p.warn(pre+".name", "missing or not [a-z0-9_]+, channel dropped")
			continue
		}
		if names[name] {
			p.warn(pre+".name", "duplicate name, channel dropped")
			continue
		}
		ch.Name = name
		ch.PWM = p.intField(pre, sec, "pwm", 0, 1, MaxPWM)
		if ch.PWM == 0 {
			p.warn(pre+".pwm", "missing or invalid, channel dropped")
			continue
		}
		if pwms[ch.PWM] {
			p.warn(pre+".pwm", "pwm%d already used by another channel, channel dropped", ch.PWM)
			continue
		}
		sensor, _ := p.strField(pre, sec, "sensor", "")
		sensor = strings.TrimSpace(sensor)
		if sensor == "" {
			p.warn(pre+".sensor", "missing, channel dropped")
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
		names[name] = true
		pwms[ch.PWM] = true
		out = append(out, ch)
	}
	return out
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
// drivetemp:max, whose fans must keep turning even when nobody regulates
// them (HDDStop).
func DefaultStop(sensor string) string {
	if sensor == "drivetemp:max" {
		return HDDStop
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
		if s == "auto" {
			return s
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
