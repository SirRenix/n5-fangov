package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SirRenix/pvefand/internal/config"
	"github.com/SirRenix/pvefand/internal/profile"
	"github.com/SirRenix/pvefand/internal/sdnotify"
)

// SensorReader is what the controller needs from a temperature source.
// Read returns millidegrees Celsius.
type SensorReader interface {
	ID() string
	Read() (int, error)
}

// SensorFactory resolves a sensor id from the config (e.g. "k10temp",
// "nvme:max") to a reader. Injected by cmd; re-invoked by the controller to
// re-resolve sensors after errors and periodically.
type SensorFactory func(id string) (SensorReader, error)

// Alerter receives alerts that passed the per-kind cooldown.
type Alerter interface {
	Alert(kind, msg string)
}

// Logger is satisfied by *log.Logger.
type Logger interface {
	Printf(format string, args ...any)
}

// Options tune a Controller. Zero values select production behaviour.
type Options struct {
	DryRun bool   // compute and log, never write hardware
	RunDir string // /run/pvefand: state.json, override.<name>, alert.<kind>; "" = no files
	Logger Logger // default: log.New(os.Stdout, "", 0)

	// Injection points for tests. nil = real time / real sd_notify.
	Now    func() time.Time
	Wait   func(ctx context.Context, d time.Duration) bool // false when ctx is done
	Notify func()                                          // per cycle (WATCHDOG=1)
	Status func(string)                                    // sd_notify STATUS
	// SyncAlerts delivers alerts on the loop goroutine instead of a new one
	// (tests). Production keeps them asynchronous so a slow perl/mail cannot
	// starve the watchdog.
	SyncAlerts bool
}

// Limits on temperature plausibility in millidegrees.
const (
	MinPlausible = -20000
	MaxPlausible = 120000
)

// MinHDDOverride is the lowest duty a manual override may set on a channel
// whose sensor is drivetemp:max. Rationale: on the N5 Pro the EC stops
// regulating the HDD channel after the first manual write, so a low manual
// duty is a permanent low duty for spinning disks whose temperature reacts
// with a lag of many minutes; 60 (~900 RPM) keeps some airflow. Critical
// still applies on top of any override.
const MinHDDOverride = 60

// rewriteEvery re-applies unchanged duties every N cycles to undo external
// writes to pwmN (port of the Bash "n % 6" rule, one minute at 10 s).
const rewriteEvery = 6

// historySpan is how much history the ring keeps.
const historySpan = 2 * time.Hour

// resolveEvery re-resolves all sensors every N cycles (Bash: hwmon re-resolve
// every 60 cycles), so devices that re-enumerated are picked up.
const resolveEvery = 60

// stallRecoverCycles with RPM > 0 end a stall condition.
const stallRecoverCycles = 3

// writeErrorsBeforeFailsafe consecutive cycles with a write error trigger
// the all-channel failsafe.
const writeErrorsBeforeFailsafe = 2

// channel is the runtime state of one configured channel.
type channel struct {
	cfg     config.Channel
	hasTach bool
	sensor  SensorReader // nil while unresolved

	cur    int // last duty written (or assumed at start)
	rpm    int // -1 when no tach / unreadable
	temp   int // millidegrees; valid when tempOK
	tempOK bool
	target int
	mode   Mode

	stallCnt int
	stalled  bool
	recov    int
}

// Controller runs the regulation loop. It implements Service.
type Controller struct {
	dev   profile.Device
	newS  SensorFactory
	alert Alerter
	opts  Options
	log   Logger

	mu        sync.Mutex // guards everything below
	cfg       config.Config
	chans     []*channel
	overrides map[string]int
	snap      Snapshot
	hist      []HistoryPoint // ring, oldest first
	histCap   int
	alerts    map[string]int64 // kind → unix ts of last delivered alert
	extra     map[string]string
	pending   *config.Config // accepted by Apply, swapped in by the loop

	logMu   sync.Mutex
	lastMsg map[string]string

	// loop-only state (touched by the Run goroutine only)
	n        int
	wrErr    int
	lastRaw  int
	sameRaw  int
	haveRaw  bool
	started  time.Time
	stopOnce sync.Once
}

// New builds a controller for cfg on dev. Sensors are resolved through
// sensors; a sensor that cannot be resolved now is retried every cycle (the
// channel meanwhile runs the sensor-error path, i.e. 255). Channels whose
// pwm does not exist on dev are dropped with a log line (rule 8: config
// errors never prevent start).
func New(cfg config.Config, dev profile.Device, sensors SensorFactory, alerter Alerter, opts Options) (*Controller, error) {
	if dev == nil {
		return nil, errors.New("control: nil device")
	}
	if sensors == nil {
		return nil, errors.New("control: nil sensor factory")
	}
	if opts.Logger == nil {
		opts.Logger = log.New(os.Stdout, "", 0)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Wait == nil {
		opts.Wait = realWait
	}
	if opts.Notify == nil {
		opts.Notify = func() { _ = sdnotify.Watchdog() }
	}
	if opts.Status == nil {
		opts.Status = func(s string) { _ = sdnotify.Status(s) }
	}
	if alerter == nil {
		alerter = alertLogger{opts.Logger}
	}
	c := &Controller{
		dev:       dev,
		newS:      sensors,
		alert:     alerter,
		opts:      opts,
		log:       opts.Logger,
		overrides: map[string]int{},
		alerts:    map[string]int64{},
		lastMsg:   map[string]string{},
		extra:     dev.ExtraTemps(),
	}
	c.cfg = cfg.Clone()
	c.histCap = historyCapacity(c.cfg.Daemon.Interval)
	if opts.RunDir != "" {
		if err := os.MkdirAll(opts.RunDir, 0o755); err != nil {
			c.log.Printf("control: run dir %s: %v (state files disabled)", opts.RunDir, err)
			c.opts.RunDir = ""
		} else {
			c.loadAlertStamps()
		}
	}
	c.chans = c.buildChannels(c.cfg)
	if c.opts.RunDir != "" {
		c.loadOverrides()
	}
	c.started = c.opts.Now()
	c.snap = c.buildSnapshot("starting")
	return c, nil
}

func historyCapacity(interval time.Duration) int {
	if interval <= 0 {
		return 1
	}
	n := int(historySpan / interval)
	if n < 1 {
		n = 1
	}
	return n
}

func realWait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

type alertLogger struct{ l Logger }

func (a alertLogger) Alert(kind, msg string) { a.l.Printf("ALERT[%s]: %s", kind, msg) }

func (c *Controller) buildChannels(cfg config.Config) []*channel {
	devChans := map[int]profile.Channel{}
	for _, dc := range c.dev.Channels() {
		devChans[dc.Index] = dc
	}
	var out []*channel
	for _, cc := range cfg.Channels {
		dc, ok := devChans[cc.PWM]
		if !ok {
			c.log.Printf("config: channel %q: pwm%d not present on %s, channel ignored", cc.Name, cc.PWM, c.dev.Profile().Name())
			c.raise("config", fmt.Sprintf("channel %q uses pwm%d which the %s profile does not provide; channel ignored", cc.Name, cc.PWM, c.dev.Profile().Name()))
			continue
		}
		ch := &channel{cfg: cc, hasTach: dc.HasTach, cur: 255, rpm: -1, mode: ModeAuto, target: 255}
		ch.sensor = c.resolveSensor(cc)
		out = append(out, ch)
	}
	return out
}

func (c *Controller) resolveSensor(cc config.Channel) SensorReader {
	s, err := c.newS(cc.Sensor)
	if err != nil {
		c.logOnce("resolve."+cc.Name, "sensor %q for channel %q: %v", cc.Sensor, cc.Name, err)
		return nil
	}
	c.logClear("resolve." + cc.Name)
	return s
}

// Channels returns the names of the channels actually managed.
func (c *Controller) Channels() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, len(c.chans))
	for i, ch := range c.chans {
		names[i] = ch.cfg.Name
	}
	return names
}

// Config returns a copy of the active configuration.
func (c *Controller) Config() config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.Clone()
}

// ---- loop -------------------------------------------------------------------

// Run executes the regulation loop until ctx is done, then calls Stop.
// It returns ctx.Err() (or nil when ctx was never cancelled, which cannot
// happen in practice).
func (c *Controller) Run(ctx context.Context) error {
	defer c.Stop()
	c.log.Printf("start: profile=%s hwmon=%s channels=%s interval=%s dryrun=%v",
		c.dev.Profile().Name(), c.dev.HwmonPath(), strings.Join(c.Channels(), ","), c.interval(), c.opts.DryRun)
	for {
		c.cycle()
		if !c.opts.Wait(ctx, c.interval()) {
			return ctx.Err()
		}
	}
}

func (c *Controller) interval() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.Daemon.Interval
}

// cycle is one regulation step (port of the Bash loop body).
func (c *Controller) cycle() {
	c.opts.Notify()
	n := c.n

	c.mu.Lock()
	c.applyPendingLocked()
	d := c.cfg.Daemon
	chans := c.chans
	ovr := make(map[string]int, len(c.overrides))
	for k, v := range c.overrides {
		ovr[k] = v
	}
	c.mu.Unlock()

	if n > 0 && n%resolveEvery == 0 {
		c.reresolve(chans)
	}

	// -- sensors ----------------------------------------------------------
	ok := true
	for _, ch := range chans {
		ch.tempOK = false
		if ch.sensor == nil {
			// unresolved at start or after a reload: retry every cycle
			ch.sensor = c.resolveSensor(ch.cfg)
		}
		if ch.sensor == nil {
			ok = false
			continue
		}
		v, err := ch.sensor.Read()
		if err != nil {
			c.logOnce("read."+ch.cfg.Name, "sensor %s (%s): %v", ch.cfg.Sensor, ch.cfg.Name, err)
			ok = false
			continue
		}
		c.logClear("read." + ch.cfg.Name)
		if v < MinPlausible || v > MaxPlausible {
			c.logOnce("read."+ch.cfg.Name, "sensor %s (%s): implausible %d m°C", ch.cfg.Sensor, ch.cfg.Name, v)
			ok = false
			continue
		}
		ch.temp, ch.tempOK = v, true
	}
	if ok && len(chans) > 0 {
		// stale detection on the first channel's sensor (the CPU sensor on the
		// N5 Pro: it never stays bit-identical for minutes on a live system;
		// NVMe/HDD sensors legitimately do).
		raw := chans[0].temp
		if c.haveRaw && raw == c.lastRaw {
			c.sameRaw++
		} else {
			c.sameRaw = 0
		}
		c.lastRaw, c.haveRaw = raw, true
		if c.sameRaw >= d.StaleCycles {
			ok = false
			c.logOnce("stale", "sensor %s unchanged for %s (%d m°C) -> frozen?",
				chans[0].cfg.Sensor, time.Duration(c.sameRaw)*d.Interval, raw)
		}
	}
	extra := c.readExtra()
	c.readRPMs(chans)

	if !ok {
		c.logOnce("sensor", "sensor error -> all channels 255 (%s)", c.sensorSummary(chans))
		c.raise("sensor", "sensor unreadable/implausible/frozen -> fans at 255 (journalctl -u pvefand)")
		c.reresolve(chans)
		c.failsafeAll(chans, ModeSensor)
		c.finishCycle(chans, extra, "sensor-error", d)
		return
	}
	c.logClear("sensor")
	c.logClear("stale")

	// -- targets ----------------------------------------------------------
	crit := false
	var critParts []string
	for _, ch := range chans {
		ch.mode = ModeAuto
		ch.target = Interpolate(ch.cfg.Curve, ch.temp)
		if v, has := ovr[ch.cfg.Name]; has {
			ch.target = v
			ch.mode = ModeManual
		}
		if ch.temp >= ch.cfg.Critical*1000 {
			ch.target = 255
			ch.mode = ModeCritical
			crit = true
		}
		critParts = append(critParts, fmt.Sprintf("%s=%s", ch.cfg.Name, fmtTemp(ch.temp)))
	}
	if crit {
		c.raise("temp", "critical temperature reached: "+strings.Join(critParts, " ")+" -> 255")
	}

	// -- stall ------------------------------------------------------------
	for _, ch := range chans {
		if !ch.hasTach {
			continue
		}
		if ch.stalled {
			ch.target = 255
			ch.mode = ModeStall
			if ch.rpm > 0 {
				ch.recov++
			} else {
				ch.recov = 0
			}
			if ch.recov >= stallRecoverCycles {
				ch.stalled, ch.stallCnt, ch.recov = false, 0, 0
				c.log.Printf("%s: fan spinning again (%d RPM), regulation resumed", ch.cfg.Name, ch.rpm)
				// keep 255 for this cycle; the curve takes over next cycle with slew
			}
		} else if ch.rpm == 0 && ch.cur >= d.StallMinDuty {
			ch.stallCnt++
			if ch.stallCnt >= d.StallCycles {
				ch.stalled = true
				ch.target = 255
				ch.mode = ModeStall
				c.raise("stall", fmt.Sprintf("%s: fan stopped (0 RPM at duty %d) -> channel at 255. Check fan/cable.", ch.cfg.Name, ch.cur))
			}
		} else {
			ch.stallCnt = 0
		}
	}

	// -- slew + write -----------------------------------------------------
	werr := false
	for _, ch := range chans {
		next := ch.target
		if n > 0 && ch.mode == ModeAuto {
			next = Slew(ch.cur, ch.target, d.StepUp, d.StepDown)
		}
		if next != ch.cur || n%rewriteEvery == 0 {
			if err := c.write(ch, next); err != nil {
				c.log.Printf("%s: write pwm%d=%d failed: %v", ch.cfg.Name, ch.cfg.PWM, next, err)
				werr = true
			} else {
				ch.cur = next
			}
		}
	}
	status := "ok"
	if werr {
		c.wrErr++
		c.log.Printf("pwm write error (%d consecutive)", c.wrErr)
		status = "write-error"
		if c.wrErr >= writeErrorsBeforeFailsafe {
			c.raise("write", "repeated pwm write errors -> all channels 255; check driver/EC (dmesg, journalctl -u pvefand)")
			c.reresolve(chans)
			c.failsafeAll(chans, ModeFailsafe)
		}
	} else {
		c.wrErr = 0
	}
	c.finishCycle(chans, extra, status, d)
}

// finishCycle publishes the snapshot, appends history and logs periodically.
func (c *Controller) finishCycle(chans []*channel, extra map[string]float64, status string, d config.Daemon) {
	if c.opts.DryRun && status == "ok" {
		status = "dry-run"
	}
	now := c.opts.Now()
	c.mu.Lock()
	c.snap = c.buildSnapshotLocked(status, extra, now)
	c.pushHistoryLocked(chans, now)
	snap := c.snap
	c.mu.Unlock()
	c.writeState(snap)
	c.n++
	line := c.statusLine(chans)
	c.opts.Status(status + ": " + line)
	if d.LogEvery > 0 && (c.n-1)%d.LogEvery == 0 {
		c.log.Printf("%s", line)
	}
}

func (c *Controller) statusLine(chans []*channel) string {
	var parts, rpms []string
	for _, ch := range chans {
		t := "n/a"
		if ch.tempOK {
			t = fmtTemp(ch.temp)
		}
		parts = append(parts, fmt.Sprintf("%s=%s->pwm%d=%d(%s)", ch.cfg.Name, t, ch.cfg.PWM, ch.cur, ch.mode))
		rpms = append(rpms, strconv.Itoa(ch.rpm))
	}
	return strings.Join(parts, "  ") + "  rpm=" + strings.Join(rpms, "/")
}

func fmtTemp(milli int) string {
	return strconv.FormatFloat(float64(milli)/1000, 'f', 1, 64) + "C"
}

func (c *Controller) sensorSummary(chans []*channel) string {
	var parts []string
	for _, ch := range chans {
		v := "?"
		if ch.sensor == nil {
			v = "unresolved"
		} else if ch.tempOK {
			v = fmtTemp(ch.temp)
		}
		parts = append(parts, ch.cfg.Name+"="+v)
	}
	return strings.Join(parts, " ")
}

func (c *Controller) readRPMs(chans []*channel) {
	for _, ch := range chans {
		ch.rpm = -1
		if !ch.hasTach {
			continue
		}
		if r, err := c.dev.ReadRPM(ch.cfg.PWM); err == nil && r >= 0 {
			ch.rpm = r
		}
	}
}

// readExtra reads the profile's extra temperature files (informational only).
func (c *Controller) readExtra() map[string]float64 {
	if len(c.extra) == 0 {
		return nil
	}
	out := make(map[string]float64, len(c.extra))
	for id, path := range c.extra {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || v < MinPlausible || v > MaxPlausible {
			continue
		}
		out[id] = float64(v) / 1000
	}
	return out
}

func (c *Controller) reresolve(chans []*channel) {
	for _, ch := range chans {
		if s := c.resolveSensor(ch.cfg); s != nil {
			ch.sensor = s
		}
	}
}

// write puts the channel in manual mode and writes duty (verified by the
// device). Dry-run only pretends.
func (c *Controller) write(ch *channel, duty int) error {
	if c.opts.DryRun {
		return nil
	}
	if err := c.dev.EnterManual(ch.cfg.PWM); err != nil {
		return fmt.Errorf("enter manual: %w", err)
	}
	return c.dev.WriteDuty(ch.cfg.PWM, duty)
}

// failsafeAll drives every managed channel to 255 (rule 4).
func (c *Controller) failsafeAll(chans []*channel, mode Mode) {
	for _, ch := range chans {
		ch.mode = mode
		ch.target = 255
		if err := c.write(ch, 255); err != nil {
			c.log.Printf("%s: failsafe write pwm%d=255 failed: %v", ch.cfg.Name, ch.cfg.PWM, err)
			continue
		}
		ch.cur = 255
	}
}

// Stop returns every channel to its configured safe state (rule 7) and
// removes the state file. Safe to call more than once.
func (c *Controller) Stop() {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		chans := c.chans
		c.mu.Unlock()
		var parts []string
		for _, ch := range chans {
			parts = append(parts, fmt.Sprintf("%s=%s", ch.cfg.Name, ch.cfg.Stop))
		}
		c.log.Printf("stop: safe state %s", strings.Join(parts, " "))
		if !c.opts.DryRun {
			for _, ch := range chans {
				if err := c.dev.SafeStop(ch.cfg.PWM, ch.cfg.Stop); err != nil {
					c.log.Printf("%s: safe stop (%s) failed: %v", ch.cfg.Name, ch.cfg.Stop, err)
				}
			}
		}
		if c.opts.RunDir != "" {
			_ = os.Remove(filepath.Join(c.opts.RunDir, "state.json"))
		}
	})
}

// Failsafe puts every configured channel that exists on dev into its
// configured safe state (SafeStop). Used by `pvefand failsafe`
// (ExecStopPost) without a running controller; continues after errors and
// returns them joined.
func Failsafe(dev profile.Device, cfg config.Config) error {
	present := map[int]bool{}
	for _, dc := range dev.Channels() {
		present[dc.Index] = true
	}
	var errs []error
	for _, ch := range cfg.Channels {
		if !present[ch.PWM] {
			continue
		}
		if err := dev.SafeStop(ch.PWM, ch.Stop); err != nil {
			errs = append(errs, fmt.Errorf("%s (pwm%d, stop=%s): %w", ch.Name, ch.PWM, ch.Stop, err))
		}
	}
	return errors.Join(errs...)
}

// FullSpeed drives every configured channel to 255 in manual mode. Emergency
// helper for the CLI; not used by the loop.
func FullSpeed(dev profile.Device, cfg config.Config) error {
	var errs []error
	for _, ch := range cfg.Channels {
		if err := dev.EnterManual(ch.PWM); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ch.Name, err))
			continue
		}
		if err := dev.WriteDuty(ch.PWM, 255); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ch.Name, err))
		}
	}
	return errors.Join(errs...)
}

// ---- curve / slew -------------------------------------------------------------

// Interpolate returns the duty for tempMilli on a curve of [temp_c, duty]
// points: linear between points, clamped to the first/last duty.
func Interpolate(curve [][2]int, tempMilli int) int {
	if len(curve) == 0 {
		return 255
	}
	t := float64(tempMilli) / 1000
	if t <= float64(curve[0][0]) {
		return curve[0][1]
	}
	last := curve[len(curve)-1]
	if t >= float64(last[0]) {
		return last[1]
	}
	for i := 1; i < len(curve); i++ {
		t0, d0 := float64(curve[i-1][0]), float64(curve[i-1][1])
		t1, d1 := float64(curve[i][0]), float64(curve[i][1])
		if t <= t1 {
			if t1 == t0 {
				return int(d1)
			}
			v := d0 + (t-t0)*(d1-d0)/(t1-t0)
			return clampDuty(int(v + 0.5))
		}
	}
	return last[1]
}

// Slew limits the change from cur towards target to +stepUp / -stepDown.
func Slew(cur, target, stepUp, stepDown int) int {
	switch {
	case target > cur+stepUp:
		return cur + stepUp
	case target < cur-stepDown:
		return cur - stepDown
	default:
		return target
	}
}

func clampDuty(d int) int {
	if d < 0 {
		return 0
	}
	if d > 255 {
		return 255
	}
	return d
}

// ---- Service ------------------------------------------------------------------

// Snapshot returns the state after the last cycle.
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.snap
	s.Uptime = int64(c.opts.Now().Sub(c.started).Seconds())
	s.Channels = append([]ChannelState(nil), s.Channels...)
	s.ExtraTemps = copyMapF(s.ExtraTemps)
	s.Alerts = copyMapI(s.Alerts)
	return s
}

// History returns ring entries not older than since (oldest first).
func (c *Controller) History(since time.Duration) []HistoryPoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	cut := c.opts.Now().Add(-since).Unix()
	var out []HistoryPoint
	for _, p := range c.hist {
		if p.TS >= cut {
			out = append(out, p)
		}
	}
	return out
}

// SetOverride pins a channel to duty (manual mode) until ClearOverride.
// Critical temperature and stall handling still apply. Channels fed by
// drivetemp:max refuse duties below MinHDDOverride (see there).
func (c *Controller) SetOverride(name string, duty int) error {
	if duty < 0 || duty > 255 {
		return fmt.Errorf("duty %d outside 0..255", duty)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.findLocked(name)
	if ch == nil {
		return fmt.Errorf("unknown channel %q", name)
	}
	if ch.cfg.Sensor == "drivetemp:max" && duty < MinHDDOverride {
		return fmt.Errorf("channel %q (drivetemp:max): manual duty must be at least %d", name, MinHDDOverride)
	}
	c.overrides[name] = duty
	c.log.Printf("%s: manual override %d", name, duty)
	if c.opts.RunDir != "" {
		if err := writeFileAtomic(filepath.Join(c.opts.RunDir, "override."+name), []byte(strconv.Itoa(duty)+"\n")); err != nil {
			c.log.Printf("%s: override file: %v", name, err)
		}
	}
	return nil
}

// ClearOverride returns a channel to curve control.
func (c *Controller) ClearOverride(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.findLocked(name) == nil {
		return fmt.Errorf("unknown channel %q", name)
	}
	delete(c.overrides, name)
	c.log.Printf("%s: override cleared, back to curve", name)
	if c.opts.RunDir != "" {
		_ = os.Remove(filepath.Join(c.opts.RunDir, "override."+name))
	}
	return nil
}

// Overrides returns a copy of the active overrides.
func (c *Controller) Overrides() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return copyMapInt(c.overrides)
}

// Reload parses rawTOML and applies it. Daemon parameters, curves, sensors,
// critical and stop values are swapped atomically between cycles. When the
// channel set (names or pwm numbers) or the profile differ, nothing is
// applied and ErrRestartRequired is returned. Parse warnings are logged; a
// syntax error is returned unchanged.
func (c *Controller) Reload(rawTOML []byte) error {
	cfg, warns, err := config.Parse(rawTOML)
	if err != nil {
		return err
	}
	for _, w := range warns {
		c.log.Printf("reload: config warning: %s", w)
	}
	return c.Apply(cfg)
}

// Apply is Reload for an already parsed config.
func (c *Controller) Apply(cfg config.Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	base := c.cfg
	if c.pending != nil {
		base = *c.pending
	}
	if cfg.Daemon.Profile != base.Daemon.Profile || !sameChannelSet(cfg.Channels, base.Channels) {
		return ErrRestartRequired
	}
	// Channel structs belong to the loop goroutine; the swap happens at the
	// start of the next cycle (applyPendingLocked).
	clone := cfg.Clone()
	c.pending = &clone
	c.log.Printf("config reload accepted (interval=%s, %d channels), applied on next cycle", cfg.Daemon.Interval, len(cfg.Channels))
	return nil
}

// applyPendingLocked swaps in a config accepted by Apply. Called by the loop
// goroutine with c.mu held.
func (c *Controller) applyPendingLocked() {
	cfg := c.pending
	if cfg == nil {
		return
	}
	c.pending = nil
	old := c.cfg
	c.cfg = *cfg
	byName := map[string]config.Channel{}
	for _, cc := range c.cfg.Channels {
		byName[cc.Name] = cc
	}
	firstSensorChanged := false
	for i, ch := range c.chans {
		cc := byName[ch.cfg.Name]
		if cc.Sensor != ch.cfg.Sensor {
			ch.sensor = c.resolveSensor(cc)
			if i == 0 {
				firstSensorChanged = true
			}
		}
		ch.cfg = cc
	}
	if firstSensorChanged {
		c.haveRaw, c.sameRaw = false, 0
	}
	if cfg.Daemon.Interval != old.Daemon.Interval {
		c.histCap = historyCapacity(cfg.Daemon.Interval)
		if len(c.hist) > c.histCap {
			c.hist = append([]HistoryPoint(nil), c.hist[len(c.hist)-c.histCap:]...)
		}
	}
	c.log.Printf("config applied")
}

func sameChannelSet(a, b []config.Channel) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]int{}
	for _, ch := range a {
		set[ch.Name] = ch.PWM
	}
	for _, ch := range b {
		pwm, ok := set[ch.Name]
		if !ok || pwm != ch.PWM {
			return false
		}
	}
	return true
}

func (c *Controller) findLocked(name string) *channel {
	for _, ch := range c.chans {
		if ch.cfg.Name == name {
			return ch
		}
	}
	return nil
}

// ---- snapshot / history / state file ------------------------------------------

func (c *Controller) buildSnapshot(status string) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buildSnapshotLocked(status, nil, c.opts.Now())
}

func (c *Controller) buildSnapshotLocked(status string, extra map[string]float64, now time.Time) Snapshot {
	s := Snapshot{
		TS:         now.Unix(),
		Status:     status,
		Profile:    c.dev.Profile().Name(),
		Verified:   c.dev.Profile().Verified(),
		HwmonPath:  c.dev.HwmonPath(),
		DryRun:     c.opts.DryRun,
		Channels:   make([]ChannelState, 0, len(c.chans)),
		ExtraTemps: extra,
		Alerts:     copyMapI(c.alerts),
		Uptime:     int64(now.Sub(c.started).Seconds()),
	}
	if s.ExtraTemps == nil {
		s.ExtraTemps = map[string]float64{}
	}
	for _, ch := range c.chans {
		cs := ChannelState{
			Name: ch.cfg.Name, PWM: ch.cfg.PWM, Sensor: ch.cfg.Sensor,
			Temp: -999, Duty: ch.cur, Target: ch.target, RPM: ch.rpm, Mode: ch.mode,
		}
		if ch.tempOK {
			cs.Temp = float64(ch.temp) / 1000
		}
		s.Channels = append(s.Channels, cs)
	}
	return s
}

func (c *Controller) pushHistoryLocked(chans []*channel, now time.Time) {
	p := HistoryPoint{TS: now.Unix(), Temp: map[string]float64{}, Duty: map[string]int{}, RPM: map[string]int{}}
	for _, ch := range chans {
		if ch.tempOK {
			p.Temp[ch.cfg.Name] = float64(ch.temp) / 1000
		}
		p.Duty[ch.cfg.Name] = ch.cur
		if ch.rpm >= 0 {
			p.RPM[ch.cfg.Name] = ch.rpm
		}
	}
	if len(c.hist) >= c.histCap {
		drop := len(c.hist) - c.histCap + 1
		c.hist = append(c.hist[:0], c.hist[drop:]...)
	}
	c.hist = append(c.hist, p)
}

func (c *Controller) writeState(s Snapshot) {
	if c.opts.RunDir == "" {
		return
	}
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	if err := writeFileAtomic(filepath.Join(c.opts.RunDir, "state.json"), append(b, '\n')); err != nil {
		c.logOnce("state", "state.json: %v", err)
		return
	}
	c.logClear("state")
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadOverrides restores override.<name> files from RunDir (a restart of the
// daemon keeps manual settings, like the Bash daemon's override files).
func (c *Controller) loadOverrides() {
	for _, ch := range c.chans {
		b, err := os.ReadFile(filepath.Join(c.opts.RunDir, "override."+ch.cfg.Name))
		if err != nil {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || v < 0 || v > 255 || (ch.cfg.Sensor == "drivetemp:max" && v < MinHDDOverride) {
			c.log.Printf("%s: override file invalid (%q), removed", ch.cfg.Name, strings.TrimSpace(string(b)))
			_ = os.Remove(filepath.Join(c.opts.RunDir, "override."+ch.cfg.Name))
			continue
		}
		c.overrides[ch.cfg.Name] = v
		c.log.Printf("%s: manual override %d restored from %s", ch.cfg.Name, v, c.opts.RunDir)
	}
}

// ---- alerts -------------------------------------------------------------------

// raise delivers an alert of kind unless one of the same kind was delivered
// within AlertCooldown. Stamps live in memory and in RunDir/alert.<kind> so
// a restart does not repeat the alert.
func (c *Controller) raise(kind, msg string) {
	now := c.opts.Now().Unix()
	c.mu.Lock()
	cool := int64(c.cfg.Daemon.AlertCooldown.Seconds())
	last, seen := c.alerts[kind]
	if seen && now-last < cool {
		c.mu.Unlock()
		return
	}
	c.alerts[kind] = now
	c.mu.Unlock()
	if c.opts.RunDir != "" {
		_ = os.WriteFile(filepath.Join(c.opts.RunDir, "alert."+kind), []byte(strconv.FormatInt(now, 10)+"\n"), 0o644)
	}
	c.log.Printf("ALERT[%s]: %s", kind, msg)
	if c.opts.SyncAlerts {
		c.alert.Alert(kind, msg)
		return
	}
	go c.alert.Alert(kind, msg)
}

func (c *Controller) loadAlertStamps() {
	entries, err := os.ReadDir(c.opts.RunDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "alert.") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.opts.RunDir, e.Name()))
		if err != nil {
			continue
		}
		ts, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			continue
		}
		c.alerts[strings.TrimPrefix(e.Name(), "alert.")] = ts
	}
}

// ---- log helpers ----------------------------------------------------------------

// logOnce logs msg unless the last message under key was identical (port
// of the Bash log_once: no repeated lines while a condition persists).
func (c *Controller) logOnce(key, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	c.logMu.Lock()
	same := c.lastMsg[key] == msg
	c.lastMsg[key] = msg
	c.logMu.Unlock()
	if !same {
		c.log.Printf("%s", msg)
	}
}

func (c *Controller) logClear(key string) {
	c.logMu.Lock()
	delete(c.lastMsg, key)
	c.logMu.Unlock()
}

func copyMapF(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyMapI(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyMapInt(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
