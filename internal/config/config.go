// Package config loads, validates and writes the pvefand TOML configuration
// (see DESIGN.md "Config"). The guiding rule is DESIGN rule 8: config errors
// never prevent start. Parse replaces every invalid value with its built-in
// default and reports it as a Warning; only a TOML syntax error is returned as
// an error, in which case the caller uses Default().
package config

import (
	"bytes"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Limits used by validation. Exported so the web UI and CLI can show them.
const (
	MinInterval   = 2 * time.Second
	MaxInterval   = 120 * time.Second
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
	presetRe = regexp.MustCompile(`^[a-z0-9_-]+$`)
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
}

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
	Daemon   Daemon    `toml:"daemon"`
	Web      Web       `toml:"web"`
	Channels []Channel `toml:"channel"`
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
		},
	}
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
		case "daemon", "web", "channel":
		default:
			p.warn(k, "unknown section, ignored")
		}
	}

	// toml's map decoding silently yields an empty map for non-table values,
	// so the TOML type is checked explicitly.
	if prim, ok := top["daemon"]; ok {
		var sec map[string]toml.Primitive
		if md.Type("daemon") != "Hash" || md.PrimitiveDecode(prim, &sec) != nil {
			p.warn("daemon", "not a table, defaults used")
		} else {
			p.daemon(sec, &cfg.Daemon)
		}
	}
	if prim, ok := top["web"]; ok {
		var sec map[string]toml.Primitive
		if md.Type("web") != "Hash" || md.PrimitiveDecode(prim, &sec) != nil {
			p.warn("web", "not a table, defaults used")
		} else {
			p.web(sec, &cfg.Web)
		}
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

// durField accepts a duration string ("10s", "30m") or an integer (seconds).
func (p *parser) durField(prefix string, sec map[string]toml.Primitive, key string, def, lo, hi time.Duration) time.Duration {
	prim, ok := sec[key]
	if !ok {
		return def
	}
	var d time.Duration
	var s string
	if err := p.md.PrimitiveDecode(prim, &s); err == nil {
		v, perr := time.ParseDuration(s)
		if perr != nil {
			p.warn(prefix+"."+key, "%q is not a duration, default %s used", s, def)
			return def
		}
		d = v
	} else {
		var n int64
		if err := p.md.PrimitiveDecode(prim, &n); err != nil {
			p.warn(prefix+"."+key, "not a duration string or integer seconds, default %s used", def)
			return def
		}
		d = time.Duration(n) * time.Second
	}
	if d < lo || d > hi {
		p.warn(prefix+"."+key, "%s outside %s..%s, default %s used", d, lo, hi, def)
		return def
	}
	return d
}

func (p *parser) daemon(sec map[string]toml.Primitive, d *Daemon) {
	const pre = "daemon"
	p.unknown(pre, sec, "interval", "step_up", "step_down", "stall_min_duty", "stall_cycles",
		"stale_cycles", "alert_cooldown", "log_every", "profile")
	def := Default().Daemon
	d.Interval = p.durField(pre, sec, "interval", def.Interval, MinInterval, MaxInterval)
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
	p.unknown(pre, sec, "listen", "auth", "user", "password_hash", "allowed_hosts")
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
		} else if _, err := parseHex(w.PasswordHash); err != nil {
			p.warn(pre+".password_hash", "not a 64-char sha256 hex digest, auth set to none")
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

func parseHex(s string) ([]byte, error) {
	if len(s) != 64 {
		return nil, fmt.Errorf("length %d", len(s))
	}
	out := make([]byte, 32)
	for i := 0; i < 32; i++ {
		b, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(b)
	}
	return out, nil
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
