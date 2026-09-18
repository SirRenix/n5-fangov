package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/fsutil"
	"github.com/SirRenix/n5-fangov/internal/history"
	"github.com/SirRenix/n5-fangov/internal/logfile"
	"github.com/SirRenix/n5-fangov/internal/profile"
	"github.com/SirRenix/n5-fangov/internal/sdnotify"
	"github.com/SirRenix/n5-fangov/internal/sensor"
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
	RunDir string // /run/n5-fangov: state.json, override.<name>, alert.<kind>; "" = no files
	Logger Logger // default: log.New(os.Stdout, "", 0)
	// HistoryFile is <state dir>/history.json, the persisted chart history
	// (internal/history); "" keeps the history in memory only.
	HistoryFile string

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

// Limits on temperature plausibility in millidegrees; defined once in
// internal/sensor, re-exported here for the callers of this package.
const (
	MinPlausible = sensor.MinPlausible
	MaxPlausible = sensor.MaxPlausible
)

// MinHDDOverride is the lowest duty a manual override may set on a channel
// that the chip no longer regulates by itself (see hddLike): a channel with
// a fixed stop duty, or pwm3 on the N5 Pro. Rationale: on the N5 Pro the EC
// stops regulating the HDD channel after the first manual write, so a low
// manual duty is a permanent low duty for spinning disks whose temperature
// reacts with a lag of many minutes; 60 (~900 RPM) keeps some airflow.
// Critical still applies on top of any override.
const MinHDDOverride = 60

// deviceLostCycles consecutive cycles in which the all-channel failsafe
// could not write a single channel mean the device is gone (typically a
// driver reload re-enumerated hwmonN). The loop then returns ErrDeviceLost
// so that systemd restarts the daemon: ExecStopPost runs the failsafe and
// the fresh process detects the device again.
const deviceLostCycles = 6

// ErrDeviceLost is returned by Run when the device stopped accepting writes.
var ErrDeviceLost = errors.New("control: device unreachable, restart required")

// staleSensor is the only sensor id the stale check runs on: k10temp reports
// with sub-degree resolution and never stays bit-identical for minutes on a
// live system. Whole-degree sources (nvme:max, drivetemp:max, coretemp, ec:*)
// legitimately do, so the check would produce false failsafes there.
const staleSensor = "k10temp"

// enableReader is implemented by devices that can report pwmN_enable (the
// hwmon-backed profiles). The controller uses it only for a diagnostic log
// line; the interface stays optional.
type enableReader interface {
	ReadEnable(ch int) (string, error)
}

// rewriteEvery re-applies unchanged duties every N cycles to undo external
// writes to pwmN (port of the Bash "n % 6" rule, one minute at 10 s).
const rewriteEvery = 6

// historySaveEvery is how often the loop persists the history store
// (Options.Now readings); Stop saves once more.
const historySaveEvery = 10 * time.Minute

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

	cur     int  // last duty written (ReadDuty at start); -1 = unknown after a failed write
	written bool // at least one successful write by this process (stall detection gate)
	rpm     int  // -1 when no tach / unreadable
	temp    int  // millidegrees; valid when tempOK
	tempOK  bool
	target  int
	mode    Mode

	// sensorBad is the channel's sensor state after the last cycle (alerts
	// go out on the transition into it); sensorErr says why, for the log
	// and the alert text.
	sensorBad bool
	sensorErr string

	stallCnt int
	stalled  bool
	recov    int

	// Curve post-processing (DESIGN "Curve post-processing"), loop-only
	// state. held is the hysteresis-held temperature the curve is evaluated
	// at (valid when heldOK; reset by a sensor error and a sensor change).
	// lastCurve is the previous cycle's curve output (valid when haveCurve);
	// a rise above it starts a min_on hold of holdTarget from holdStart
	// until holdUntil (holding).
	held       int
	heldOK     bool
	lastCurve  int
	haveCurve  bool
	holding    bool
	holdTarget int
	holdStart  time.Time
	holdUntil  time.Time
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
	hist      *history.Store   // tiered history (raw 2 h, 1-min 24 h, 5-min 7 d)
	alerts    map[string]int64 // kind → unix ts of last delivered alert (snapshot, stamp files)
	// alertAt is the in-memory cooldown clock per kind: Options.Now()
	// readings, which carry a monotonic reading in production, so a wall
	// clock step (NTP correction after a wrong RTC at boot) neither
	// silences nor repeats alerts. alerts above is the wall-clock view.
	alertAt map[string]time.Time
	extra   map[string]string
	pending *config.Config // accepted by Apply, swapped in by the loop
	// watched are the [dashboard].sensors readers (v0.3); rebuilt by the
	// loop at the start of a cycle when watchDirty is set (Apply with a
	// changed list, SetWatched). Read by the loop goroutine only.
	watched    []*watchedSensor
	watchDirty bool

	logMu   sync.Mutex
	lastMsg map[string]string

	// hwMu serializes hardware writes between the loop (write phase,
	// failsafe) and Stop (SafeStop). It is never held together with c.mu
	// by Stop, so a panic inside a c.mu section cannot deadlock the
	// deferred Stop. Lock order where both are taken: c.mu, then hwMu.
	hwMu    sync.Mutex
	stopped atomic.Bool // set by Stop: the loop skips all further writes

	// lastCycle is the unix-nanosecond time at which the last cycle
	// finished (0 before the first); see LastCycle.
	lastCycle atomic.Int64

	// loop-only state (touched by the Run goroutine only)
	n        int
	wrErr    int
	fsFail   int // consecutive cycles in which failsafeAll wrote nothing
	lastRaw  int
	sameRaw  int
	haveRaw  bool
	started  time.Time
	lastSave time.Time // last history Save (Options.Now)
	stopOnce sync.Once
}

// New builds a controller for cfg on dev. Sensors are resolved through
// sensors; a sensor that cannot be resolved now is retried every cycle (the
// channel meanwhile sits at its safe duty in mode sensor-error while the
// other channels regulate, see safeDuty). Channels whose pwm does not exist
// on dev are dropped with a log line (rule 8: config errors never prevent
// start). On the N5 Pro, pwm1..3 missing from the config are added with
// the built-in defaults (SanitizeChannels), because a channel the daemon
// touched once and then ignores stays wherever the last write left it.
// Both corrections go out as one "config-channels" alert (distinct from the
// "config" alert serve sends for parse warnings, so neither suppresses the
// other through the shared cooldown stamps).
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
		alertAt:   map[string]time.Time{},
		lastMsg:   map[string]string{},
		extra:     dev.ExtraTemps(),
	}
	c.cfg = cfg.Clone()
	c.hist = history.New(opts.HistoryFile, c.cfg.Daemon.Interval, opts.Now, c.log.Printf)
	if opts.RunDir != "" {
		if err := os.MkdirAll(opts.RunDir, 0o755); err != nil {
			c.log.Printf("control: run dir %s: %v (state files disabled)", opts.RunDir, err)
			c.opts.RunDir = ""
		} else {
			c.loadAlertStamps()
		}
	}
	var notes, dropped []string
	c.cfg.Channels, notes = SanitizeChannels(dev.Profile().Name(), c.cfg.Channels)
	c.chans, dropped = c.buildChannels(c.cfg)
	c.reportNotes("config corrected at start", append(notes, dropped...))
	c.watched = c.buildWatched(c.cfg.Dashboard.Sensors)
	if c.opts.RunDir != "" {
		c.loadOverrides()
	}
	c.started = c.opts.Now()
	c.lastSave = c.started
	c.snap = c.buildSnapshot(StatusStarting)
	return c, nil
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

func (a alertLogger) Alert(kind, msg string) {
	a.l.Printf("%sALERT[%s]: %s", logfile.PrefixErr, kind, msg)
}

// buildChannels creates the runtime channels for cfg. Channels whose pwm
// the device lacks are skipped; one note per skipped channel is returned
// for reportNotes.
func (c *Controller) buildChannels(cfg config.Config) (chans []*channel, dropped []string) {
	devChans := map[int]profile.Channel{}
	for _, dc := range c.dev.Channels() {
		devChans[dc.Index] = dc
	}
	var out []*channel
	for _, cc := range cfg.Channels {
		dc, ok := devChans[cc.PWM]
		if !ok {
			dropped = append(dropped, fmt.Sprintf("channel %q uses pwm%d which the %s profile does not provide; channel ignored", cc.Name, cc.PWM, c.dev.Profile().Name()))
			continue
		}
		ch := &channel{cfg: cc, hasTach: dc.HasTach, cur: 255, rpm: -1, mode: ModeAuto, target: 255}
		// The real duty at start; 255 only when unreadable. Slew starts from
		// what the chip is actually doing, dry-run shows the real value.
		switch v, err := c.dev.ReadDuty(cc.PWM); {
		case err != nil:
			c.log.Printf("%s: pwm%d not readable at start (%v), assuming 255", cc.Name, cc.PWM, err)
		case v < 0 || v > 255:
			c.log.Printf("%s: pwm%d reads %d at start (outside 0..255), assuming 255", cc.Name, cc.PWM, v)
		default:
			ch.cur = v
		}
		ch.sensor = c.resolveSensor(cc)
		out = append(out, ch)
	}
	return out, dropped
}

// AlertConfigChannels is the alert kind for channel-set corrections made by
// the controller (SanitizeChannels additions, forced stop values, channels
// dropped because the device lacks their pwm). serve stamps "config" for
// parse warnings; a shared kind would let the earlier of the two silence the
// other for the cooldown period.
const AlertConfigChannels = "config-channels"

// reportNotes logs channel-set corrections and raises one
// AlertConfigChannels alert carrying all of them.
func (c *Controller) reportNotes(what string, notes []string) {
	if len(notes) == 0 {
		return
	}
	for _, n := range notes {
		c.log.Printf("config: %s", n)
	}
	c.raise(AlertConfigChannels, what+":\n"+strings.Join(notes, "\n"))
}

// SanitizeChannels applies the profile-specific safety corrections that a
// config file cannot express on its own (the config package does not know
// the detected profile). For n5pro:
//   - pwm1..3 (CPU, SSD, HDD) must be managed: a missing channel is added
//     from config.N5ProChannels. A channel that was written once and is
//     then ignored stays wherever the last write left it (the EC does not
//     regulate pwm3 after a write at all), and `n5-fangov failsafe` would skip
//     it as well.
//   - pwm3 with stop="auto" is forced to config.HDDStop for the same reason.
//
// It returns the corrected list and one note per correction. Other
// profiles are returned unchanged.
func SanitizeChannels(profileName string, chans []config.Channel) ([]config.Channel, []string) {
	out := config.CloneChannels(chans)
	if profileName != "n5pro" {
		return out, nil
	}
	var notes []string
	names := map[string]bool{}
	byPWM := map[int]int{}
	for i, ch := range out {
		names[ch.Name] = true
		byPWM[ch.PWM] = i
	}
	for _, def := range config.N5ProChannels() {
		i, ok := byPWM[def.PWM]
		if !ok {
			if names[def.Name] {
				def.Name = fmt.Sprintf("pwm%d", def.PWM)
			}
			notes = append(notes, fmt.Sprintf("n5pro: pwm%d (%s) not in config, built-in channel %q added (sensor %s, stop %s)",
				def.PWM, def.Name, def.Name, def.Sensor, def.Stop))
			out = append(out, def)
			names[def.Name] = true
			continue
		}
		if def.PWM == 3 && out[i].Stop == "auto" {
			notes = append(notes, fmt.Sprintf("n5pro: channel %q (pwm3) has stop=\"auto\", but the EC does not regulate pwm3 after a write; stop=%s used",
				out[i].Name, config.HDDStop))
			out[i].Stop = config.HDDStop
		}
	}
	return out, notes
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

// LastCycle returns the time (Options.Now) at which the last regulation
// cycle finished, or the zero time before the first one. serve uses it as
// the liveness signal for its independent watchdog pings: they continue
// only while cycles keep finishing, so a stuck loop still trips the
// systemd watchdog. Lock-free, safe while the loop holds its mutexes.
func (c *Controller) LastCycle() time.Time {
	ns := c.lastCycle.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Interval returns the active regulation interval.
func (c *Controller) Interval() time.Duration { return c.interval() }

// ---- loop -------------------------------------------------------------------

// Run executes the regulation loop until ctx is done, then calls Stop.
// It returns ctx.Err() when cancelled, ErrDeviceLost when the device stopped
// accepting writes for deviceLostCycles cycles, or a panic error: a panic
// anywhere in the loop is recovered here so that Stop (SafeStop) still
// runs and the daemon exits non-zero for systemd to restart it.
func (c *Controller) Run(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Printf("%sPANIC in regulation loop: %v\n%s", logfile.PrefixErr, r, debug.Stack())
			err = fmt.Errorf("control: panic in regulation loop: %v", r)
		}
		c.Stop()
	}()
	c.log.Printf("start: profile=%s hwmon=%s channels=%s interval=%s dryrun=%v",
		c.dev.Profile().Name(), c.dev.HwmonPath(), strings.Join(c.Channels(), ","), c.interval(), c.opts.DryRun)
	for {
		if err := c.cycle(); err != nil {
			return err
		}
		c.saveHistory()
		if !c.opts.Wait(ctx, c.interval()) {
			return ctx.Err()
		}
	}
}

// saveHistory persists the history store every historySaveEvery (loop
// goroutine only; Stop saves once more). Write failures are logged by
// the store itself, once.
func (c *Controller) saveHistory() {
	now := c.opts.Now()
	if now.Sub(c.lastSave) < historySaveEvery {
		return
	}
	c.lastSave = now
	_ = c.hist.Save()
}

func (c *Controller) interval() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.Daemon.Interval
}

// cycleState is what the four steps of a cycle hand each other: the
// config and channel set taken under the lock at the start, the overrides
// in effect, and what readSensors produced for the later steps.
type cycleState struct {
	n       int
	d       config.Daemon
	chans   []*channel
	ovr     map[string]int   // overrides in effect (copy)
	watched []*watchedSensor // watched sensors (dashboard)

	// readSensors: channels with an unknown temperature, extra and
	// watched readings for the snapshot.
	nBad        int
	extra       map[string]float64
	watchedVals map[string]float64
}

// cycle is one regulation step (port of the Bash loop body): it takes the
// config, channel set and overrides under the lock, then runs the four
// steps readSensors → computeTargets → checkStall → writeAndFinish. It
// returns ErrDeviceLost when the failsafe could not write any channel for
// deviceLostCycles consecutive cycles.
func (c *Controller) cycle() error {
	c.opts.Notify()
	n := c.n

	c.mu.Lock()
	c.applyPendingLocked()
	if c.watchDirty {
		c.watched = c.buildWatched(c.cfg.Dashboard.Sensors)
		c.watchDirty = false
	}
	d := c.cfg.Daemon
	chans := c.chans
	watched := c.watched
	ovr := make(map[string]int, len(c.overrides))
	for k, v := range c.overrides {
		ovr[k] = v
	}
	c.mu.Unlock()

	if n > 0 && n%resolveEvery == 0 {
		c.reresolve(chans)
	}

	cs := &cycleState{n: n, d: d, chans: chans, ovr: ovr, watched: watched}
	c.readSensors(cs)
	c.computeTargets(cs)
	c.checkStall(cs)
	return c.writeAndFinish(cs)
}

// readSensors reads every channel sensor, the extra and watched sensors
// and the tachometers, and raises the sensor alert on a transition.
// Every channel is judged on its own: a sensor that is unresolved,
// unreadable, implausible or frozen puts only its channel at the safe
// duty (mode sensor-error, see safeDuty); the other channels keep
// regulating. Only when every channel is affected does the cycle
// status become "sensor-error".
func (c *Controller) readSensors(cs *cycleState) {
	n, d, chans, watched := cs.n, cs.d, cs.chans, cs.watched
	for _, ch := range chans {
		ch.tempOK = false
		ch.sensorErr = ""
		if ch.sensor == nil {
			// unresolved at start or after a reload: retry every cycle
			ch.sensor = c.resolveSensor(ch.cfg)
		}
		if ch.sensor == nil {
			ch.sensorErr = "unresolved"
			continue
		}
		v, err := ch.sensor.Read()
		switch {
		case err != nil:
			ch.sensorErr = "read: " + err.Error()
		case !sensor.Plausible(v):
			ch.sensorErr = fmt.Sprintf("implausible %d mdegC", v)
		default:
			ch.temp, ch.tempOK = v, true
		}
		if ch.sensorErr != "" {
			c.logOnce("read."+ch.cfg.Name, "sensor %s (%s): %s", ch.cfg.Sensor, ch.cfg.Name, ch.sensorErr)
		} else {
			c.logClear("read." + ch.cfg.Name)
		}
	}
	if len(chans) > 0 && chans[0].tempOK && chans[0].cfg.Sensor == staleSensor {
		// stale detection on the first channel's sensor, and only when that
		// is k10temp (see staleSensor): whole-degree sources legitimately
		// report the same value for a long time.
		raw := chans[0].temp
		if c.haveRaw && raw == c.lastRaw {
			c.sameRaw++
		} else {
			c.sameRaw = 0
		}
		c.lastRaw, c.haveRaw = raw, true
		if c.sameRaw >= d.StaleCycles {
			chans[0].tempOK = false
			// The text names the threshold, not the running duration, so the
			// line is logged once per frozen episode, not once per cycle.
			chans[0].sensorErr = fmt.Sprintf("unchanged for %s or longer (%d mdegC) -> frozen?", time.Duration(d.StaleCycles)*d.Interval, raw)
			c.logOnce("stale", "sensor %s (%s): %s", chans[0].cfg.Sensor, chans[0].cfg.Name, chans[0].sensorErr)
		} else {
			c.logClear("stale")
		}
	}
	nBad := 0
	var newlyBad []*channel
	for _, ch := range chans {
		bad := !ch.tempOK
		switch {
		case bad:
			nBad++
			if ch.sensor != nil {
				// the device may have re-enumerated: resolve again (an
				// unresolved sensor was already retried above)
				if s := c.resolveSensor(ch.cfg); s != nil {
					ch.sensor = s
				}
			}
			if !ch.sensorBad {
				newlyBad = append(newlyBad, ch)
			}
		case ch.sensorBad:
			c.log.Printf("%s: sensor %s readable again, regulation resumed", ch.cfg.Name, ch.cfg.Sensor)
		}
		ch.sensorBad = bad
	}
	cs.extra = c.readExtra()
	cs.watchedVals = c.readWatched(watched, n)
	c.readRPMs(chans)

	if len(newlyBad) > 0 {
		// alert on the transition, not every cycle: a sensor that stays
		// absent (no HDDs, drivetemp not loaded) is one notification
		var lines []string
		for _, ch := range newlyBad {
			lines = append(lines, fmt.Sprintf("channel %q: sensor %s %s -> duty %d (%s)",
				ch.cfg.Name, ch.cfg.Sensor, ch.sensorErr, c.safeDuty(ch), c.safeDutyWhy(ch)))
		}
		scope := "the other channels keep regulating"
		if nBad == len(chans) {
			scope = "ALL channels affected, nothing is regulated"
		}
		c.raise("sensor", "sensor error: "+strings.Join(lines, "; ")+"; "+scope+" (journalctl -u n5-fangov)")
	}
	if nBad > 0 {
		// One line per change of the affected set, not per cycle: the
		// summary carries no live temperatures (k10temp moves every cycle).
		c.logOnce("sensor", "sensor error -> %d of %d channel(s) at safe duty (%s)", nBad, len(chans), c.sensorSummary(chans))
	} else {
		c.logClear("sensor")
	}
	cs.nBad = nBad
}

// computeTargets sets every channel's target and mode from the curve (at
// the hysteresis-held temperature, then the min_on hold), the override
// and the critical temperature (rules 4 and 5); a channel whose
// temperature is unknown goes to its safe duty. Critical is judged on the
// raw reading.
func (c *Controller) computeTargets(cs *cycleState) {
	chans, ovr := cs.chans, cs.ovr
	crit := false
	var critParts []string
	now := c.opts.Now()
	for _, ch := range chans {
		if !ch.tempOK {
			// unknown temperature: safe duty, no curve, no override
			// (critical cannot be judged without a reading); the held
			// temperature and a running hold are dropped, the next good
			// reading starts afresh
			ch.heldOK, ch.haveCurve, ch.holding = false, false, false
			ch.target = c.safeDuty(ch)
			ch.mode = ModeSensor
			continue
		}
		ch.mode = ModeAuto
		ch.target = minOnHold(ch, Interpolate(ch.cfg.Curve, heldTemp(ch)), now)
		if v, has := ovr[ch.cfg.Name]; has {
			// an override replaces the target; a hold that started before
			// it must not outlive it
			ch.holding = false
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
}

// checkStall runs the stall detection and recovery on every channel with
// a tachometer (rule 6): 0 RPM at a duty that should turn the fan for
// stall_cycles → 255 and alert; back to the curve after stallRecoverCycles
// with RPM > 0.
func (c *Controller) checkStall(cs *cycleState) {
	d, chans := cs.d, cs.chans
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
		} else if ch.rpm == 0 && ch.written && ch.cur >= d.StallMinDuty {
			// counted only after our first successful write: before that
			// cur is the chip's own value and 0 RPM may be its idle policy
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
}

// writeAndFinish slews and writes the targets (writePhase), runs the
// failsafe after repeated write errors, and publishes the snapshot,
// history and the periodic log line (finishCycle). It returns
// ErrDeviceLost from noteFailsafe.
func (c *Controller) writeAndFinish(cs *cycleState) error {
	n, d, chans, nBad := cs.n, cs.d, cs.chans, cs.nBad
	werr, wrote := c.writePhase(chans, d, n)
	status := StatusOK
	var lost error
	if werr {
		c.wrErr++
		c.log.Printf("pwm write error (%d consecutive)", c.wrErr)
		status = StatusWriteError
		if c.wrErr >= writeErrorsBeforeFailsafe {
			c.raise("write", "repeated pwm write errors -> all channels 255; check driver/EC (dmesg, journalctl -u n5-fangov)")
			c.reresolve(chans)
			lost = c.noteFailsafe(c.failsafeAll(chans, ModeFailsafe))
		}
	} else {
		c.fsFail = 0
		if wrote {
			// only a successful write proves the device is back; a cycle
			// without any write attempt says nothing
			c.wrErr = 0
		}
	}
	if nBad > 0 && nBad == len(chans) {
		status = StatusSensorError
	}
	c.finishCycle(chans, cs.extra, cs.watchedVals, status, d)
	return lost
}

// safeDuty is where a channel goes while its temperature is unknown: the
// fixed stop duty when one is configured (the operator's "nobody regulates
// this fan" value, e.g. 140 on the N5 Pro HDD channel), otherwise 255
// (rule 4). A manual override does not apply in that state.
func (c *Controller) safeDuty(ch *channel) int {
	if d, fixed := ch.cfg.StopDuty(); fixed {
		return d
	}
	return 255
}

func (c *Controller) safeDutyWhy(ch *channel) string {
	if _, fixed := ch.cfg.StopDuty(); fixed {
		return "configured stop duty"
	}
	return "full speed"
}

// writePhase slews every channel towards its target and writes it. A
// channel whose write fails has an unknown duty (cur = -1) and is written
// again next cycle, so a persistent failure surfaces even when the target
// never changes. Returns whether any write failed and whether any write
// succeeded. Hardware access is serialized with Stop through hwMu; after
// Stop nothing is written any more.
func (c *Controller) writePhase(chans []*channel, d config.Daemon, n int) (werr, wrote bool) {
	c.hwMu.Lock()
	defer c.hwMu.Unlock()
	if c.stopped.Load() {
		return false, false
	}
	for _, ch := range chans {
		if c.opts.DryRun {
			// the snapshot shows what the chip really does, not what we would write
			if v, err := c.dev.ReadDuty(ch.cfg.PWM); err == nil && v >= 0 && v <= 255 {
				ch.cur = v
			}
			continue
		}
		next := ch.target
		if n > 0 && ch.mode == ModeAuto && ch.cur >= 0 {
			next = Slew(ch.cur, ch.target, d.StepUp, d.StepDown)
		}
		if next != ch.cur || n%rewriteEvery == 0 {
			if err := c.write(ch, next, n); err != nil {
				c.log.Printf("%s: write pwm%d=%d failed: %v", ch.cfg.Name, ch.cfg.PWM, next, err)
				ch.cur = -1
				werr = true
			} else {
				ch.cur = next
				ch.written = true
				wrote = true
			}
		}
	}
	return werr, wrote
}

// noteFailsafe tracks consecutive cycles in which the failsafe wrote
// nothing at all; after deviceLostCycles it returns ErrDeviceLost.
func (c *Controller) noteFailsafe(allFailed bool) error {
	if !allFailed {
		c.fsFail = 0
		return nil
	}
	c.fsFail++
	if c.fsFail < deviceLostCycles {
		return nil
	}
	c.log.Printf("device unreachable: failsafe could not write any channel for %d cycles (driver reloaded / hwmon re-enumerated?) -> exiting for restart; ExecStopPost runs the failsafe, the new process re-detects the device", c.fsFail)
	c.raise("device", fmt.Sprintf("fan controller at %s unreachable for %d cycles, daemon restarts to re-detect it", c.dev.HwmonPath(), c.fsFail))
	return ErrDeviceLost
}

// finishCycle publishes the snapshot, appends history and logs periodically.
func (c *Controller) finishCycle(chans []*channel, extra, watched map[string]float64, status Status, d config.Daemon) {
	if c.opts.DryRun && status == StatusOK {
		status = StatusDryRun
	}
	now := c.opts.Now()
	c.mu.Lock()
	c.snap = c.buildSnapshotLocked(status, extra, watched, now)
	c.pushHistoryLocked(chans, watched, now)
	snap := c.snap
	c.mu.Unlock()
	c.writeState(snap)
	c.n++
	c.lastCycle.Store(now.UnixNano())
	line := c.statusLine(chans)
	c.opts.Status(string(status) + ": " + line)
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

// sensorSummary names the channels whose sensor failed and why, as
// "name=class" (unresolved, read error, implausible, frozen). Healthy
// channels and live readings stay out: the text is a logOnce key.
func (c *Controller) sensorSummary(chans []*channel) string {
	var parts []string
	for _, ch := range chans {
		if ch.tempOK {
			continue
		}
		parts = append(parts, ch.cfg.Name+"="+sensorClass(ch.sensorErr))
	}
	return strings.Join(parts, " ")
}

// sensorClass reduces a sensorErr text to its kind.
func sensorClass(sensorErr string) string {
	switch {
	case sensorErr == "":
		return "unknown"
	case sensorErr == "unresolved":
		return "unresolved"
	case strings.HasPrefix(sensorErr, "read: "):
		return "read error"
	case strings.HasPrefix(sensorErr, "implausible"):
		return "implausible"
	case strings.Contains(sensorErr, "frozen"):
		return "frozen"
	}
	return "error"
}

func (c *Controller) readRPMs(chans []*channel) {
	for _, ch := range chans {
		ch.rpm = -1
		if !ch.hasTach {
			continue
		}
		r, err := c.dev.ReadRPM(ch.cfg.PWM)
		if err != nil {
			// no RPM means no stall detection for this channel; say so once
			c.logOnce("rpm."+ch.cfg.Name, "%s: fan%d_input unreadable (%v), stall detection inactive", ch.cfg.Name, ch.cfg.PWM, err)
			continue
		}
		c.logClear("rpm." + ch.cfg.Name)
		if r >= 0 {
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
		if err != nil || !sensor.Plausible(v) {
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

// write writes duty to the channel (the device enters manual mode itself
// and verifies by read-back, rules 2 and 3). Dry-run only pretends. Caller
// holds hwMu. After the first cycle a pwmN_enable that is not "1" any more
// means something else wrote the enable file; it is logged once per
// occurrence because the device silently re-asserts manual mode.
func (c *Controller) write(ch *channel, duty int, n int) error {
	if c.opts.DryRun {
		return nil
	}
	if er, ok := c.dev.(enableReader); ok && n > 0 && ch.written {
		if en, err := er.ReadEnable(ch.cfg.PWM); err == nil && en != "1" {
			c.logOnce("enable."+ch.cfg.Name, "%s: pwm%d_enable was %q, re-asserting manual mode (external interference?)", ch.cfg.Name, ch.cfg.PWM, en)
		} else if err == nil {
			c.logClear("enable." + ch.cfg.Name)
		}
	}
	return c.dev.WriteDuty(ch.cfg.PWM, duty)
}

// failsafeAll drives every managed channel to 255 (rule 4). It returns
// true when not a single write succeeded (device gone). Serialized with
// Stop through hwMu; a no-op after Stop.
func (c *Controller) failsafeAll(chans []*channel, mode Mode) (allFailed bool) {
	c.hwMu.Lock()
	defer c.hwMu.Unlock()
	if c.stopped.Load() || len(chans) == 0 {
		return false
	}
	allFailed = true
	for _, ch := range chans {
		ch.mode = mode
		ch.target = 255
		if err := c.write(ch, 255, c.n); err != nil {
			c.log.Printf("%s: failsafe write pwm%d=255 failed: %v", ch.cfg.Name, ch.cfg.PWM, err)
			ch.cur = -1
			continue
		}
		allFailed = false
		if !c.opts.DryRun {
			ch.cur = 255
			ch.written = true
		}
	}
	return allFailed
}

// Stop returns every channel to its configured safe state (rule 7),
// removes the state file and saves the history. Safe to call more than
// once and concurrently with a running cycle: the hardware part takes
// only hwMu (never c.mu, so a panic inside a locked section cannot block
// it) and sets the stopped flag first, so a cycle that is in progress
// skips its write phase. The history file is written after hwMu is
// released — the store has its own lock, and a slow disk must not hold
// the hardware mutex.
func (c *Controller) Stop() {
	c.stopOnce.Do(func() {
		c.stopped.Store(true)
		c.safeStopLocked()
		_ = c.hist.Save()
	})
}

// safeStopLocked is Stop's hardware part under hwMu.
func (c *Controller) safeStopLocked() {
	c.hwMu.Lock()
	defer c.hwMu.Unlock()
	// c.chans is immutable after New; ch.cfg is only swapped by
	// applyPendingLocked, which also holds hwMu.
	chans := c.chans
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
	c.removeRunFile("state.json", "state.json")
}

// Failsafe puts every configured channel that exists on dev into its
// configured safe state (SafeStop). Used by `n5-fangov failsafe`
// (ExecStopPost) without a running controller; continues after errors and
// returns them joined. The channel list goes through SanitizeChannels, so
// on the N5 Pro pwm1..3 are always handled (built-in defaults for channels
// the config lacks) and pwm3 never gets "auto". logger may be nil; it
// receives one line per channel.
func Failsafe(dev profile.Device, cfg config.Config, logger Logger) error {
	present := map[int]bool{}
	for _, dc := range dev.Channels() {
		present[dc.Index] = true
	}
	chans, notes := SanitizeChannels(dev.Profile().Name(), cfg.Channels)
	if logger != nil {
		for _, n := range notes {
			logger.Printf("failsafe: config: %s", n)
		}
	}
	var errs []error
	for _, ch := range chans {
		if !present[ch.PWM] {
			if logger != nil {
				logger.Printf("failsafe: %s: pwm%d not exposed by %s, skipped", ch.Name, ch.PWM, dev.Profile().Name())
			}
			continue
		}
		if err := dev.SafeStop(ch.PWM, ch.Stop); err != nil {
			errs = append(errs, fmt.Errorf("%s (pwm%d, stop=%s): %w", ch.Name, ch.PWM, ch.Stop, err))
			if logger != nil {
				logger.Printf("failsafe: %s: pwm%d stop=%s FAILED: %v", ch.Name, ch.PWM, ch.Stop, err)
			}
			continue
		}
		if logger != nil {
			logger.Printf("failsafe: %s: pwm%d stop=%s ok", ch.Name, ch.PWM, ch.Stop)
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

// heldTemp returns the temperature the curve is evaluated at: with
// hysteresis N > 0 the held value follows the raw reading only when the
// reading differs from it by at least N degrees (N*1000 m°C); with N = 0
// (or no held value yet) the held value is the reading. Caller: the loop,
// with ch.tempOK.
func heldTemp(ch *channel) int {
	n := ch.cfg.Hysteresis
	if n <= 0 || !ch.heldOK {
		ch.held, ch.heldOK = ch.temp, true
		return ch.held
	}
	if d := ch.temp - ch.held; d >= n*1000 || -d >= n*1000 {
		ch.held = ch.temp
	}
	return ch.held
}

// minOnHold applies the minimum on-time to the curve output: a rise above
// the previous cycle's curve output holds the higher value for
// cfg.MinOn; while the hold runs the result is max(curve, held). A further
// rise above the held value replaces it and restarts the timer; a rise that
// stays below a running hold changes nothing (the fan already runs
// faster). The hold ends by time, by min_on = 0 and — in computeTargets —
// by a sensor error or an override. Returns the target for this cycle.
func minOnHold(ch *channel, curve int, now time.Time) int {
	d := ch.cfg.MinOn
	prev, havePrev := ch.lastCurve, ch.haveCurve
	ch.lastCurve, ch.haveCurve = curve, true
	if d <= 0 {
		ch.holding = false
		return curve
	}
	if ch.holding && !now.Before(ch.holdUntil) {
		ch.holding = false
	}
	if havePrev && curve > prev && (!ch.holding || curve > ch.holdTarget) {
		ch.holding = true
		ch.holdTarget = curve
		ch.holdStart = now
		ch.holdUntil = now.Add(d)
	}
	if ch.holding && ch.holdTarget > curve {
		return ch.holdTarget
	}
	return curve
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
	s.ExtraTemps = maps.Clone(s.ExtraTemps)
	s.Watched = maps.Clone(s.Watched)
	s.Alerts = maps.Clone(s.Alerts)
	return s
}

// History returns the raw tier (one point per cycle) not older than since,
// oldest first. The raw tier holds history.RawSpan at most, so a larger
// since yields the whole tier.
func (c *Controller) History(since time.Duration) []HistoryPoint {
	if since > history.RawSpan {
		since = history.RawSpan
	}
	return c.hist.Range(since, 0)
}

// HistoryRange returns the history tier for span (raw, 1-min or 5-min
// means) with ts > since; see history.Store.Range.
func (c *Controller) HistoryRange(span time.Duration, since int64) []HistoryPoint {
	return c.hist.Range(span, since)
}

// SetOverride pins a channel to duty (manual mode) until ClearOverride.
// Critical temperature and stall handling still apply. Channels the chip
// does not regulate by itself (hddLike) refuse duties below MinHDDOverride.
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
	if c.hddLike(ch.cfg) && duty < MinHDDOverride {
		return fmt.Errorf("channel %q (fixed stop duty / not chip-regulated): manual duty must be at least %d", name, MinHDDOverride)
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
	c.removeRunFile("override."+name, name+": override file")
	return nil
}

// removeRunFile removes a run-dir file; a failure other than "already
// gone" is logged, because loadOverrides would restore a leftover
// override file at the next start.
func (c *Controller) removeRunFile(name, what string) {
	if c.opts.RunDir == "" {
		return
	}
	if err := os.Remove(filepath.Join(c.opts.RunDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		c.log.Printf("%s: remove: %v", what, err)
	}
}

// Overrides returns a copy of the active overrides.
func (c *Controller) Overrides() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.overrides)
}

// Reload parses rawTOML and applies it. Daemon parameters, curves, sensors,
// critical, stop, hysteresis and min_on values are swapped atomically
// between cycles (a changed min_on re-times a running hold). When the
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
	// The same profile corrections as at start, so a config that lacks an
	// N5 Pro channel matches the completed set (no spurious restart).
	var notes []string
	cfg.Channels, notes = SanitizeChannels(c.dev.Profile().Name(), cfg.Channels)
	c.mu.Lock()
	base := c.cfg
	if c.pending != nil {
		base = *c.pending
	}
	if cfg.Daemon.Profile != base.Daemon.Profile || !sameChannelSet(cfg.Channels, base.Channels) {
		c.mu.Unlock()
		// Nothing is applied: the corrections belong to the config the
		// restart will read, not to this daemon — no log, no alert.
		return ErrRestartRequired
	}
	// Channel structs belong to the loop goroutine; the swap happens at the
	// start of the next cycle (applyPendingLocked).
	clone := cfg.Clone()
	c.pending = &clone
	c.log.Printf("config reload accepted (interval=%s, %d channels), applied on next cycle", cfg.Daemon.Interval, len(cfg.Channels))
	c.mu.Unlock()
	c.reportNotes("config corrected on reload", notes)
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
	// Resolve changed sensors first (sysfs lookups, must not run under the
	// hardware lock), then swap config and sensors in one short critical
	// section. ch.cfg is only written here and read by Stop under hwMu.
	ups := make([]chanUpdate, len(c.chans))
	firstSensorChanged := false
	for i, ch := range c.chans {
		cc := byName[ch.cfg.Name]
		ups[i].cfg = cc
		if cc.Sensor != ch.cfg.Sensor {
			ups[i].sensor = c.resolveSensor(cc)
			ups[i].newSensor = true
			if i == 0 {
				firstSensorChanged = true
			}
		}
	}
	c.swapChannelConfig(ups)
	c.retimeHolds(ups)
	if firstSensorChanged {
		c.haveRaw, c.sameRaw = false, 0
	}
	if !sameStrings(cfg.Dashboard.Sensors, old.Dashboard.Sensors) {
		c.watchDirty = true
	}
	if cfg.Daemon.Interval != old.Daemon.Interval {
		c.hist.SetInterval(cfg.Daemon.Interval)
	}
	c.log.Printf("config applied")
}

// chanUpdate is one channel's pending config swap (applyPendingLocked).
type chanUpdate struct {
	cfg       config.Channel
	sensor    SensorReader // resolved outside the hardware lock; nil = unresolved
	newSensor bool         // sensor id changed: install sensor (even nil)
	// oldMinOn is the min_on in effect before the swap (retimeHolds).
	oldMinOn time.Duration
}

// swapChannelConfig installs ups under hwMu. Stop reads ch.cfg under the
// same lock, so it never sees a half-applied channel set. A changed sensor
// id drops the hysteresis-held temperature and a running min_on hold
// (they belong to the old reading).
func (c *Controller) swapChannelConfig(ups []chanUpdate) {
	c.hwMu.Lock()
	defer c.hwMu.Unlock()
	for i, ch := range c.chans {
		ups[i].oldMinOn = ch.cfg.MinOn
		if ups[i].newSensor {
			ch.sensor = ups[i].sensor
			ch.heldOK, ch.haveCurve, ch.holding = false, false, false
		}
		ch.cfg = ups[i].cfg
	}
}

// retimeHolds applies a changed min_on to a running hold: holdUntil =
// max(holdStart + newD, now), so a shorter value shortens it (at the
// latest to this cycle) and min_on = 0 ends it. Loop goroutine only.
func (c *Controller) retimeHolds(ups []chanUpdate) {
	now := c.opts.Now()
	for i, ch := range c.chans {
		if !ch.holding || ch.cfg.MinOn == ups[i].oldMinOn {
			continue
		}
		if ch.cfg.MinOn <= 0 {
			ch.holding = false
			continue
		}
		if until := ch.holdStart.Add(ch.cfg.MinOn); until.After(now) {
			ch.holdUntil = until
		} else {
			ch.holdUntil = now
		}
	}
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

// hddLike reports whether a channel must never run at a low manual duty:
// it has a fixed stop duty (the chip is not expected to regulate it on
// its own), or it is pwm3 on the N5 Pro (the EC does not regulate it after
// a write regardless of the configured stop).
func (c *Controller) hddLike(cc config.Channel) bool {
	if _, fixed := cc.StopDuty(); fixed {
		return true
	}
	return c.dev.Profile().Name() == "n5pro" && cc.PWM == 3
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

func (c *Controller) buildSnapshot(status Status) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buildSnapshotLocked(status, nil, nil, c.opts.Now())
}

func (c *Controller) buildSnapshotLocked(status Status, extra, watched map[string]float64, now time.Time) Snapshot {
	s := Snapshot{
		TS:         now.Unix(),
		Status:     status,
		Profile:    c.dev.Profile().Name(),
		Verified:   c.dev.Profile().Verified(),
		HwmonPath:  c.dev.HwmonPath(),
		DryRun:     c.opts.DryRun,
		Channels:   make([]ChannelState, 0, len(c.chans)),
		ExtraTemps: extra,
		Watched:    watched,
		Alerts:     maps.Clone(c.alerts),
		Uptime:     int64(now.Sub(c.started).Seconds()),
	}
	if s.ExtraTemps == nil {
		s.ExtraTemps = map[string]float64{}
	}
	if s.Watched == nil {
		s.Watched = map[string]float64{}
	}
	for _, ch := range c.chans {
		cs := ChannelState{
			Name: ch.cfg.Name, PWM: ch.cfg.PWM, Sensor: ch.cfg.Sensor,
			Temp: -999, Duty: ch.cur, Target: ch.target, RPM: ch.rpm, Mode: ch.mode,
		}
		if ch.tempOK {
			cs.Temp = float64(ch.temp) / 1000
			if ch.cfg.Hysteresis > 0 && ch.heldOK && ch.held != ch.temp {
				cs.HeldTemp = float64(ch.held) / 1000
			}
		}
		if ch.holding && ch.mode == ModeAuto {
			cs.HoldUntil = ch.holdUntil.Unix()
		}
		s.Channels = append(s.Channels, cs)
	}
	return s
}

func (c *Controller) pushHistoryLocked(chans []*channel, watched map[string]float64, now time.Time) {
	p := HistoryPoint{TS: now.Unix(), Temp: map[string]float64{}, Duty: map[string]int{}, RPM: map[string]int{}}
	if len(watched) > 0 {
		p.Extra = maps.Clone(watched)
	}
	for _, ch := range chans {
		if ch.tempOK {
			p.Temp[ch.cfg.Name] = float64(ch.temp) / 1000
		}
		p.Duty[ch.cfg.Name] = ch.cur
		if ch.rpm >= 0 {
			p.RPM[ch.cfg.Name] = ch.rpm
		}
	}
	c.hist.Push(p)
}

func (c *Controller) writeState(s Snapshot) {
	if c.opts.RunDir == "" || c.stopped.Load() {
		// after Stop the file is gone and must not come back
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

// writeFileAtomic writes a run-dir file (state.json, override.<name>)
// atomically, world-readable: they hold nothing confidential.
func writeFileAtomic(path string, data []byte) error {
	return fsutil.WriteAtomic(path, data, 0o644)
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
		if err != nil || v < 0 || v > 255 || (c.hddLike(ch.cfg) && v < MinHDDOverride) {
			c.log.Printf("%s: override file invalid (%q), removed", ch.cfg.Name, strings.TrimSpace(string(b)))
			c.removeRunFile("override."+ch.cfg.Name, ch.cfg.Name+": override file")
			continue
		}
		c.overrides[ch.cfg.Name] = v
		c.log.Printf("%s: manual override %d restored from %s", ch.cfg.Name, v, c.opts.RunDir)
	}
}

// ---- alerts -------------------------------------------------------------------

// raise delivers an alert of kind unless one of the same kind was delivered
// within AlertCooldown. Stamps live in memory and in RunDir/alert.<kind> so
// a restart does not repeat the alert. The in-memory check uses the
// Options.Now readings themselves (monotonic in production); a stamp file
// that lies in the future — the wall clock stepped back after the stamp
// was written — counts as expired and is overwritten, like serve's
// start-up cooldown does, instead of silencing the kind until the clock
// catches up.
func (c *Controller) raise(kind, msg string) {
	nowT := c.opts.Now()
	now := nowT.Unix()
	c.mu.Lock()
	cool := c.cfg.Daemon.AlertCooldown
	suppressed := false
	if at, ok := c.alertAt[kind]; ok {
		if el := nowT.Sub(at); el >= 0 && el < cool {
			suppressed = true
		}
	} else if last, seen := c.alerts[kind]; seen {
		// stamp file from a previous process: wall clock only
		if d := now - last; d >= 0 && d < int64(cool.Seconds()) {
			suppressed = true
		}
	}
	if suppressed {
		c.mu.Unlock()
		return
	}
	c.alerts[kind] = now
	c.alertAt[kind] = nowT
	c.mu.Unlock()
	if c.opts.RunDir != "" {
		stamp := filepath.Join(c.opts.RunDir, "alert."+kind)
		if err := os.WriteFile(stamp, []byte(strconv.FormatInt(now, 10)+"\n"), 0o644); err != nil {
			c.logOnce("stamp."+kind, "alert stamp %s: %v (cooldown kept in memory only)", stamp, err)
		} else {
			c.logClear("stamp." + kind)
		}
	}
	c.log.Printf("%sALERT[%s]: %s", logfile.PrefixErr, kind, msg)
	if c.opts.SyncAlerts {
		c.alert.Alert(kind, msg)
		return
	}
	go c.deliver(kind, msg)
}

// deliver runs a sink on its own goroutine. The Sink contract says "never
// panic", but a panic there must cost the alert, not the regulation loop.
func (c *Controller) deliver(kind, msg string) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Printf("%salert sink panicked delivering %s: %v", logfile.PrefixErr, kind, r)
		}
	}()
	c.alert.Alert(kind, msg)
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
