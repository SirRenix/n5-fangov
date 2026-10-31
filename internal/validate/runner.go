package validate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/n5driver"
)

// Readings are the environment values of one sample; NaN = not available,
// Fan[n] = -1 unreadable.
type Readings struct {
	Fan     map[int]int // fan1..fan3
	Tctl    float64
	NVMeMax float64
	HDDMax  float64
	ECCPU   float64
	ECSys   float64
	ECBoard float64
	ECAmb   float64
	Load1   float64
}

// Env is what a run needs from the machine: the real N5 Pro (cmd) or the
// simulator of --dry-run.
type Env interface {
	Now() time.Time
	// Sleep waits d; false when ctx is done.
	Sleep(ctx context.Context, d time.Duration) bool
	EnterManual(ch int) error
	WriteDuty(ch, duty int) error
	// Auto sends the channel's EC automatic command (pwmN_enable = 2).
	Auto(ch int) error
	// Restore puts the channel into its configured stop state.
	Restore(ch int) error
	// DCR reads the duty registers; an error means "not available".
	DCR() (n5driver.DCR, error)
	Read() Readings
}

// Config of one run.
type Config struct {
	Channel  int   // pwm channel under test
	OwnTach  int   // its tach (0 = none)
	Tachs    []int // all tachs (1..3)
	Hold     time.Duration
	Sample   time.Duration
	Observe  time.Duration
	Window   time.Duration // G4 window
	Repeat   int           // T2/T3 repetitions
	RunT1    bool
	RunT2T3  bool
	RunGates bool // sample the G3/G4 window first
	// Settle is the settle time of the channel after a write (TESTSPEC 0.4
	// §2.1); 0 = SettleTime(Channel).
	Settle time.Duration
	// Tolerance is how far a sample may lie from its due time (§2, #11); 0
	// = GridTolerance.
	Tolerance time.Duration
	// Guard, when set, is asked at every sample whether another pwm writer
	// became active (G1 for the whole run); a non-empty answer aborts.
	Guard func() string
}

// HandbackObserve is how long a T2 sub-run logs after the auto command:
// Observe plus SettleHoldS, so a steady state that starts at Observe (T3
// "reached within 15 min") can still be seen for its full 120 s, and "no
// steady state within 15 min" (T3 FAIL) can be decided at all.
func (c Config) HandbackObserve() time.Duration {
	return c.Observe + SettleHoldS*time.Second
}

// SettleFor is the settle time used for the channel under test.
func (c Config) SettleFor() time.Duration {
	if c.Settle > 0 {
		return c.Settle
	}
	return SettleTime(c.Channel)
}

func (c Config) tolerance() time.Duration {
	if c.Tolerance > 0 {
		return c.Tolerance
	}
	return GridTolerance
}

// ErrRestoreUnverified is wrapped by Env.Restore when the stop value was
// written but the read-back of the EC register differs. Close counts the
// channel as restored and reports the difference separately.
var ErrRestoreUnverified = errors.New("stop value written, EC register differs")

// lostAfter is the number of consecutive samples without a value after
// which a limit sensor that was available at the gate aborts the run.
const lostAfter = 3

// SpecConfig returns the TESTSPEC timing (version SpecVersion) for channel ch.
func SpecConfig(ch, ownTach int, tachs []int) Config {
	return Config{Channel: ch, OwnTach: ownTach, Tachs: tachs,
		Hold: T1StepHoldS * time.Second, Sample: SampleS * time.Second, Observe: ObserveS * time.Second,
		Window: GateWindowS * time.Second, Repeat: RunsRequired, RunT1: true, RunT2T3: true, RunGates: true,
		Settle: SettleTime(ch), Tolerance: GridTolerance}
}

// Runner executes the writing tests against an Env and records every
// sample and event. It never writes a duty below MinDuty and restores
// every channel it touched (Close).
type Runner struct {
	Env     Env
	Cfg     Config
	Samples []Sample
	Events  []string
	Abort   string // why the run stopped early ("" = it did not)
	// LimitSensors says which abort-limit sensors (tctl, nvme, hdd) had a
	// value in the gate window; Gates fills it. A sensor marked true that
	// then stays unreadable for lostAfter samples aborts the run.
	LimitSensors map[string]bool
	// RestoreWarnings lists channels whose stop value was written but whose
	// EC register differs afterwards (ErrRestoreUnverified).
	RestoreWarnings []string
	// RestoreOverrides are restore values that differ from the configured
	// one, with the reason (TESTSPEC 0.4 §4 #13: the channel of a sensor
	// that tripped a temperature limit is left at 255).
	RestoreOverrides map[int]string
	// Gaps counts slots of the 5-s grid without a judged sample (§2).
	Gaps int

	start      time.Time
	touched    map[int]bool
	wrote      bool           // the run has put a channel into manual mode or written a duty
	restoreErr map[int]string // last restore error per channel still touched
	nanRun     map[string]int
	dcrOK      bool
	dcrErr     error
	hot        int    // channel of the sensor that tripped a temperature limit (0 = none)
	hotWhy     string // that limit
}

// NewRunner starts the run clock.
func NewRunner(env Env, cfg Config) *Runner {
	r := &Runner{Env: env, Cfg: cfg, start: env.Now(), touched: map[int]bool{}, restoreErr: map[int]string{}, nanRun: map[string]int{}}
	_, r.dcrErr = env.DCR()
	r.dcrOK = r.dcrErr == nil
	return r
}

// DCRAvailable reports whether the duty registers can be read.
func (r *Runner) DCRAvailable() (bool, error) { return r.dcrOK, r.dcrErr }

func (r *Runner) event(format string, args ...any) {
	r.Events = append(r.Events, r.Env.Now().UTC().Format(time.RFC3339)+" "+fmt.Sprintf(format, args...))
}

// Event records an event from the caller (gate results, operator input).
func (r *Runner) Event(format string, args ...any) { r.event(format, args...) }

// Sample records one unscheduled row (t_s = real time since the start) and
// checks the safety limits of TESTSPEC §4. The returned string is the abort
// reason ("" = within limits).
func (r *Runner) Sample(phase string, written int) (Sample, string) {
	return r.sample(phase, written, time.Time{})
}

// sample records one row due at due (zero = unscheduled). A row read more
// than the grid tolerance from its due time is marked OffGrid (a gap, not
// judged); its real time is recorded either way.
func (r *Runner) sample(phase string, written int, due time.Time) (Sample, string) {
	now := r.Env.Now()
	rd := r.Env.Read()
	s := Sample{Time: now, TS: now.Sub(r.start).Seconds(), Phase: phase, Ch: r.Cfg.Channel, Written: written,
		DCR: [5]int{-1, -1, -1, -1, -1}, Fan: [3]int{-1, -1, -1},
		ECCPU: rd.ECCPU, ECSys: rd.ECSys, ECBoard: rd.ECBoard, ECAmb: rd.ECAmb, HDDMax: rd.HDDMax, NVMeMax: rd.NVMeMax, Tctl: rd.Tctl, Load1: rd.Load1}
	if !due.IsZero() {
		off := now.Sub(due)
		if off < 0 {
			off = -off
		}
		if off <= r.Cfg.tolerance() {
			s.TS = due.Sub(r.start).Seconds()
		} else {
			s.OffGrid = true
			r.Gaps++
			r.event("gap: %s sample due %s read %+.2f s off (> %s): recorded, not judged", phase, due.UTC().Format(time.RFC3339), now.Sub(due).Seconds(), r.Cfg.tolerance())
		}
	}
	var dcr n5driver.DCR
	haveDCR := false
	if r.dcrOK {
		if d, err := r.Env.DCR(); err == nil {
			dcr, haveDCR = d, true
			for n := 1; n <= 5; n++ {
				s.DCR[n-1] = d.Reg(n)
			}
		}
	}
	for n := 1; n <= 3; n++ {
		if v, ok := rd.Fan[n]; ok {
			s.Fan[n-1] = v
		}
	}
	r.Samples = append(r.Samples, s)
	if r.Cfg.Guard != nil {
		if why := r.Cfg.Guard(); why != "" {
			return s, "another pwm writer became active: " + why
		}
	}
	for _, ls := range []struct {
		name string
		v    float64
	}{{"tctl", rd.Tctl}, {"nvme", rd.NVMeMax}, {"hdd", rd.HDDMax}} {
		if !r.LimitSensors[ls.name] {
			continue
		}
		if !math.IsNaN(ls.v) {
			r.nanRun[ls.name] = 0
			continue
		}
		r.nanRun[ls.name]++
		if r.nanRun[ls.name] >= lostAfter {
			return s, fmt.Sprintf("limit sensor %s unreadable in %d consecutive samples (its limit cannot be enforced)", ls.name, lostAfter)
		}
	}
	hot := func(ch int, why string) (Sample, string) {
		if r.hot == 0 {
			r.hot, r.hotWhy = ch, why
		}
		return s, why
	}
	switch {
	case !math.IsNaN(rd.Tctl) && rd.Tctl >= LimitTctlC:
		return hot(1, fmt.Sprintf("CPU Tctl %.1f C >= %.0f C", rd.Tctl, LimitTctlC))
	case !math.IsNaN(rd.NVMeMax) && rd.NVMeMax >= LimitNVMeC:
		return hot(2, fmt.Sprintf("NVMe %.1f C >= %.0f C", rd.NVMeMax, LimitNVMeC))
	case !math.IsNaN(rd.HDDMax) && rd.HDDMax >= LimitHDDC:
		return hot(3, fmt.Sprintf("HDD %.1f C >= %.0f C", rd.HDDMax, LimitHDDC))
	}
	for _, n := range r.Cfg.Tachs {
		v, ok := rd.Fan[n]
		if !ok || v != 0 {
			continue
		}
		// tach 0 aborts while the channel's duty is > 0; without the
		// registers the duty is assumed > 0 (EC automatic never stops them)
		if haveDCR {
			if d, ok := dcr.Channel(n); ok && d == 0 {
				continue
			}
		}
		return s, fmt.Sprintf("fan%d reports 0 rpm while its duty is > 0", n)
	}
	return s, ""
}

// hold samples on the grid of the phase (TESTSPEC 0.4 §2, #11): anchor =
// now (the start of the phase: the write, the auto command), samples due
// at anchor + k*Cfg.Sample for d. A slot whose due time has passed by more
// than the tolerance before it could be read is skipped (a gap). each gets
// every recorded sample and its due time in seconds after the anchor.
// false on abort or cancel.
func (r *Runner) hold(ctx context.Context, phase string, written int, d time.Duration, each func(Sample, float64)) bool {
	step := r.Cfg.Sample
	n := int(d / step)
	if n < 1 {
		n = 1
	}
	anchor := r.Env.Now()
	tol := r.Cfg.tolerance()
	for k := 1; k <= n; k++ {
		due := anchor.Add(time.Duration(k) * step)
		now := r.Env.Now()
		if now.Sub(due) > tol {
			r.Gaps++
			r.event("gap: %s slot due %s skipped (the previous sample overran it by %.2f s)", phase, due.UTC().Format(time.RFC3339), now.Sub(due).Seconds())
			continue
		}
		if wait := due.Sub(now); wait > 0 {
			if !r.Env.Sleep(ctx, wait) {
				r.stop("cancelled (signal)")
				return false
			}
		} else if ctx.Err() != nil {
			r.stop("cancelled (signal)")
			return false
		}
		s, abort := r.sample(phase, written, due)
		if each != nil {
			each(s, (time.Duration(k) * step).Seconds())
		}
		if abort != "" {
			r.stop(abort)
			return false
		}
	}
	return true
}

// Stop ends the run early with reason (first reason wins), e.g. when the
// caller's own sample hit a limit of TESTSPEC §4.
func (r *Runner) Stop(reason string) { r.stop(reason) }

func (r *Runner) stop(reason string) {
	if r.Abort == "" {
		r.Abort = reason
		r.event("ABORT %s", reason)
	}
}

var errLowDuty = errors.New("validate: duty below 100 is never written (TESTSPEC §4)")

func (r *Runner) write(ch, duty int) error {
	if duty < MinDuty || duty > 255 {
		return fmt.Errorf("%w: %d", errLowDuty, duty)
	}
	r.touched[ch], r.wrote = true, true
	if err := r.Env.WriteDuty(ch, duty); err != nil {
		r.event("write pwm%d=%d FAILED: %v", ch, duty, err)
		return err
	}
	r.event("write pwm%d=%d", ch, duty)
	return nil
}

func (r *Runner) manual(ch int) error {
	r.touched[ch], r.wrote = true, true
	if err := r.Env.EnterManual(ch); err != nil {
		r.event("pwm%d manual FAILED: %v", ch, err)
		return err
	}
	r.event("pwm%d manual (driver sets 255)", ch)
	return nil
}

func (r *Runner) auto(ch int) error {
	r.touched[ch] = true
	if err := r.Env.Auto(ch); err != nil {
		r.event("pwm%d auto FAILED: %v", ch, err)
		return err
	}
	r.event("pwm%d auto (EC automatic command)", ch)
	return nil
}

// hotChannel is the channel a temperature abort leaves at 255 (TESTSPEC 0.4
// §4, #13): only in a run that wrote; a run that never wrote leaves every
// channel as it found it.
func (r *Runner) hotChannel() int {
	if r.hot == 0 || !r.wrote {
		return 0
	}
	return r.hot
}

// Close restores every channel the run touched to its stop state. After a
// temperature abort the channel of that sensor goes to 255 and stays there
// (§4, #13). It is safe to call more than once and runs on every exit path
// of the caller.
func (r *Runner) Close() {
	if h := r.hotChannel(); h > 0 {
		r.touched[h] = true
	}
	for ch := 1; ch <= 4; ch++ {
		if !r.touched[ch] {
			continue
		}
		if r.restore(ch, "") {
			delete(r.touched, ch)
		}
	}
}

// restore puts one channel into its stop state and logs the outcome; false
// means the channel is not restored (Close tries again, Unrestored reports
// it). A written stop value whose register reads back differently counts
// as restored and is listed in RestoreWarnings.
func (r *Runner) restore(ch int, when string) bool {
	if ch == r.hotChannel() {
		why := "255 (temperature abort: " + r.hotWhy + "; TESTSPEC §4)"
		err := r.Env.EnterManual(ch)
		if err == nil {
			err = r.Env.WriteDuty(ch, 255)
		}
		if err != nil {
			r.event("restore pwm%d=255%s after a temperature abort FAILED: %v - set it by hand", ch, when, err)
			r.restoreErr[ch] = err.Error()
			return false
		}
		if r.RestoreOverrides == nil {
			r.RestoreOverrides = map[int]string{}
		}
		r.RestoreOverrides[ch] = why
		r.event("restore pwm%d=255%s ok: %s", ch, when, why)
		delete(r.restoreErr, ch)
		return true
	}
	err := r.Env.Restore(ch)
	switch {
	case err == nil:
		r.event("restore pwm%d%s ok", ch, when)
		delete(r.restoreErr, ch)
		return true
	case errors.Is(err, ErrRestoreUnverified):
		r.event("restore pwm%d%s WRITTEN, REGISTER DIFFERS: %v", ch, when, err)
		r.RestoreWarnings = append(r.RestoreWarnings, fmt.Sprintf("pwm%d: %v", ch, err))
		delete(r.restoreErr, ch)
		return true
	default:
		r.event("restore pwm%d%s FAILED: %v - set it by hand (pwm%d_enable)", ch, when, err, ch)
		r.restoreErr[ch] = err.Error()
		return false
	}
}

// Unrestored lists the channels this run touched and could not put back
// into their stop state, with the last error ("pwm3: ...").
func (r *Runner) Unrestored() []string {
	var out []string
	for ch := 1; ch <= 4; ch++ {
		if r.touched[ch] {
			out = append(out, fmt.Sprintf("pwm%d: %s", ch, orText(r.restoreErr[ch], "not restored")))
		}
	}
	return out
}

// Gates samples the G3/G4 window (no writes) and evaluates both.
func (r *Runner) Gates(ctx context.Context) (g3, g4 Gate) {
	g3 = Gate{ID: "G3", Condition: "load average (1 min) < 1.0"}
	g4 = Gate{ID: "G4", Condition: "over 5 min: EC system (0x04), EC ambient (0x06) and HDD temperature each change < 1 C; every abort-limit sensor (Tctl, NVMe, HDD) readable; Tctl is a limit only (TESTSPEC 0.4 #20)"}
	if r.Abort != "" {
		g3.Result, g3.Detail = "FAIL", "not sampled: "+r.Abort
		g4.Result, g4.Detail = "FAIL", "not sampled: "+r.Abort
		return g3, g4
	}
	var rows []Sample
	// every sample is checked against the limits (TESTSPEC §4), the first
	// one of the window included
	first, abort := r.Sample("gate", -1)
	rows = append(rows, first)
	ok := false
	if abort != "" {
		r.stop(abort)
	} else {
		ok = r.hold(ctx, "gate", -1, r.Cfg.Window, func(s Sample, _ float64) {
			if !s.OffGrid {
				rows = append(rows, s)
			}
		})
	}
	if math.IsNaN(first.Load1) {
		g3.Result, g3.Detail = "FAIL", "load average not readable"
	} else if first.Load1 < GateLoad1 {
		g3.Result, g3.Detail = "PASS", fmt.Sprintf("load1 %.2f", first.Load1)
	} else {
		g3.Result, g3.Detail = "FAIL", fmt.Sprintf("load1 %.2f", first.Load1)
	}
	if !ok {
		g4.Result, g4.Detail = "FAIL", "window not completed: "+r.Abort
		return g3, g4
	}
	ev := EvalG4(rows)
	r.LimitSensors = ev.Limit
	g4.Detail = fmt.Sprintf("over %s: %v", r.Cfg.Window, ev.Parts)
	if ev.Pass {
		g4.Result = "PASS"
	} else {
		g4.Result = "FAIL"
	}
	return g3, g4
}

// Gate is one precondition of TESTSPEC §3.
type Gate struct {
	ID        string `json:"id"`
	Condition string `json:"condition"`
	Result    string `json:"result"` // PASS, FAIL, PARTIAL, SKIPPED
	Detail    string `json:"detail"`
}

func (r *Runner) chDCR(s Sample) int {
	n, ok := n5driver.ChannelDCR(r.Cfg.Channel)
	if !ok {
		return -1
	}
	return regDCR(s, n)
}

// regDCR is DCRn of the sample, -1 when not read.
func regDCR(s Sample, n int) int {
	if n < 1 || n > len(s.DCR) || s.DCR[n-1] < 0 {
		return -1
	}
	return s.DCR[n-1]
}

// inputs appends the table inputs of pwm1..pwm3 of s to m.
func inputs(m map[int][]float64, s Sample) {
	for c := 1; c <= 3; c++ {
		m[c] = append(m[c], TableInput(c, s))
	}
}

// lastInput is the table input of channel ch in the last recorded sample
// (the reference before a write or an auto command); NaN without one.
func (r *Runner) lastInput(ch int) float64 {
	if len(r.Samples) == 0 {
		return math.NaN()
	}
	return TableInput(ch, r.Samples[len(r.Samples)-1])
}

// RunT1 is the mapping test of Cfg.Channel.
func (r *Runner) RunT1(ctx context.Context) Result {
	ch := r.Cfg.Channel
	if !r.dcrOK {
		return NotImplemented("T1", ch, "not run: duty registers not readable ("+errText(r.dcrErr)+"); a verdict needs them, so nothing was written")
	}
	if r.Abort != "" {
		return NotImplemented("T1", ch, "not run: "+r.Abort)
	}
	rest := map[int][]int{}
	restIn := map[int][]float64{}
	r.event("T1 pwm%d: rest reference", ch)
	if !r.hold(ctx, "t1-rest", -1, r.Cfg.Hold, func(s Sample, _ float64) {
		if s.OffGrid {
			return
		}
		for i, f := range s.Fan {
			rest[i+1] = append(rest[i+1], f)
		}
		inputs(restIn, s)
	}) {
		return NotImplemented("T1", ch, "aborted: "+r.Abort)
	}
	if err := r.manual(ch); err != nil {
		return NotImplemented("T1", ch, "aborted: manual mode failed: "+err.Error())
	}
	settle := r.Cfg.SettleFor().Seconds()
	var steps []T1Step
	for _, duty := range T1Steps {
		if err := r.write(ch, duty); err != nil {
			r.stop("write failed: " + err.Error())
			return NotImplemented("T1", ch, "aborted: "+r.Abort)
		}
		st := T1Step{Duty: duty, Fans: map[int][]int{}, SettleS: settle, Inputs: map[int][]float64{}}
		ok := r.hold(ctx, fmt.Sprintf("t1-%d", duty), duty, r.Cfg.Hold, func(s Sample, due float64) {
			if s.OffGrid {
				st.Gaps++
				return
			}
			// -1 = no DCR in this sample: EvalT1 needs every judged sample
			st.DCR = append(st.DCR, r.chDCR(s))
			if ch == 3 { // DCR4 is judged too (TESTSPEC §5.1)
				st.DCR4 = append(st.DCR4, regDCR(s, 4))
			}
			st.DueS = append(st.DueS, due)
			for i, f := range s.Fan {
				st.Fans[i+1] = append(st.Fans[i+1], f)
			}
			inputs(st.Inputs, s)
		})
		steps = append(steps, st)
		if !ok {
			return NotImplemented("T1", ch, "aborted: "+r.Abort)
		}
	}
	if r.restore(ch, " after T1") {
		delete(r.touched, ch)
	}
	return EvalT1Inputs(ch, r.Cfg.OwnTach, rest, restIn, steps, r.Cfg.Tachs)
}

// observe samples after an auto command and returns the DCR series of every
// judged register of the channel (JudgedRegs), T = due time in seconds after
// the command, and the channel's table input per sample.
func (r *Runner) observe(ctx context.Context, phase string, d time.Duration) (map[int][]DCRSample, []float64, bool) {
	out := map[int][]DCRSample{}
	var in []float64
	regs := JudgedRegs(r.Cfg.Channel)
	ok := r.hold(ctx, phase, -1, d, func(s Sample, due float64) {
		in = append(in, TableInput(r.Cfg.Channel, s))
		if s.OffGrid {
			return
		}
		for _, n := range regs {
			if v := regDCR(s, n); v >= 0 {
				out[n] = append(out[n], DCRSample{T: due, DCR: v})
			}
		}
	})
	return out, in, ok
}

// RunHandback is T2 with T3 from the same data: per repetition the
// sub-runs H (255) and M (min(A+48, 200)), each 60 s held, then the auto
// command and Cfg.HandbackObserve() of samples. pwm3 is evaluated on DCR3
// and DCR4 (each with its own A; M from DCR3's A). pwm4 has no EC automatic:
// T2/T3 are N/A and nothing is written (TESTSPEC §5.2, §6). A table input
// that crosses a step in the window of A or between a write and the end of
// its log makes the sub-runs INCONCLUSIVE (§5.2, #17); hold-phase samples
// after the settle time that differ from the written value are listed.
func (r *Runner) RunHandback(ctx context.Context) (Result, Result) {
	ch := r.Cfg.Channel
	if ch == PCIeChannel {
		return NotApplicableResult("T2", ch, ReasonNoPCIeAuto), NotApplicableResult("T3", ch, ReasonNoPCIeAuto)
	}
	if !r.dcrOK {
		reason := "not run: duty registers not readable (" + errText(r.dcrErr) + "); a verdict needs them, so nothing was written"
		return NotImplemented("T2", ch, reason), NotImplemented("T3", ch, reason)
	}
	if r.Abort != "" {
		return NotImplemented("T2", ch, "not run: "+r.Abort), NotImplemented("T3", ch, "not run: "+r.Abort)
	}
	steps := TableSteps(ch)
	// A: the channel's auto value. After the daemon's stop the HDD channel
	// sits at its fixed stop duty, so hand it back first and wait for the
	// register to settle (T3 rule, at most Observe).
	r.event("T2 pwm%d: auto value A", ch)
	refA := r.lastInput(ch)
	if err := r.auto(ch); err != nil {
		r.stop("auto command failed: " + err.Error())
		return NotImplemented("T2", ch, "aborted: "+r.Abort), NotImplemented("T3", ch, "aborted: "+r.Abort)
	}
	regs := JudgedRegs(ch)
	pre, preIn, ok := r.observe(ctx, "t2-pre", r.Cfg.Observe)
	if !ok || len(pre[regs[0]]) == 0 {
		return NotImplemented("T2", ch, "aborted: "+orText(r.Abort, "no DCR sample")), NotImplemented("T3", ch, "aborted: "+orText(r.Abort, "no DCR sample"))
	}
	crossA := Crossing(refA, preIn, steps)
	if crossA != "" {
		crossA = InputName(ch) + " in the window of A: " + crossA
		r.event("T2 pwm%d: table input crossed a step in the window of A (%s): every sub-run is INCONCLUSIVE", ch, crossA)
	}
	// A per register (last sample of the pre window); a register without
	// samples has no A (its sub-runs then cannot pass: no DCR samples)
	autoA := map[int]int{}
	var aText []string
	for _, n := range regs {
		if p := pre[n]; len(p) > 0 {
			autoA[n] = p[len(p)-1].DCR
			aText = append(aText, fmt.Sprintf("%s A=%d", regName(n), autoA[n]))
		} else {
			aText = append(aText, regName(n)+" A=-")
		}
	}
	a := autoA[regs[0]]
	m := MValue(a)
	r.event("T2 pwm%d: %s, H=255, M=%d", ch, strings.Join(aText, ", "), m)
	settle := r.Cfg.SettleFor().Seconds()
	var runs [][]SubResult
	for rep := 1; rep <= r.Cfg.Repeat; rep++ {
		var subs []SubResult
		for _, sub := range []struct {
			label string
			duty  int
		}{{"H", 255}, {"M", m}} {
			if err := r.manual(ch); err != nil {
				r.stop("manual mode failed: " + err.Error())
				break
			}
			ref := r.lastInput(ch)
			if err := r.write(ch, sub.duty); err != nil {
				r.stop("write failed: " + err.Error())
				break
			}
			var in []float64
			hold := map[int][]DCRSample{}
			if !r.hold(ctx, fmt.Sprintf("t2-r%d-%s-hold", rep, sub.label), sub.duty, r.Cfg.Hold, func(s Sample, due float64) {
				in = append(in, TableInput(ch, s))
				if s.OffGrid {
					return
				}
				for _, n := range regs {
					if v := regDCR(s, n); v >= 0 {
						hold[n] = append(hold[n], DCRSample{T: due, DCR: v})
					}
				}
			}) {
				break
			}
			if err := r.auto(ch); err != nil {
				r.stop("auto command failed: " + err.Error())
				break
			}
			smp, obsIn, ok := r.observe(ctx, fmt.Sprintf("t2-r%d-%s-auto", rep, sub.label), r.Cfg.HandbackObserve())
			cross := crossA
			if cross == "" {
				if x := Crossing(ref, append(in, obsIn...), steps); x != "" {
					cross = InputName(ch) + ": " + x
				}
			}
			var hr []HandbackRun
			for _, n := range regs {
				av, known := autoA[n]
				reg := ""
				if len(regs) > 1 {
					reg = regName(n)
				}
				hr = append(hr, HandbackRun{Label: sub.label, Reg: reg, Written: sub.duty, Samples: smp[n], TimingKnown: true, Auto: av, AutoKnown: known,
					Hold: hold[n], SettleS: settle, InputCrossed: cross})
			}
			subs = append(subs, EvalHandbackRegs(hr))
			if !ok {
				break
			}
		}
		if len(subs) > 0 {
			runs = append(runs, subs)
		}
		if r.Abort != "" {
			break
		}
	}
	t2, t3 := AggregateHandback(ch, runs, []string{"H", "M"})
	if r.Abort != "" {
		t2.Verdict, t3.Verdict = Inconclusive, Inconclusive
		t2.Reasons = append([]string{"aborted: " + r.Abort}, t2.Reasons...)
		t3.Reasons = append([]string{"aborted: " + r.Abort}, t3.Reasons...)
	}
	vals := map[string]int{"A": a, "M": m}
	if len(regs) > 1 {
		for _, n := range regs {
			if v, ok := autoA[n]; ok {
				vals["A_"+regName(n)] = v
			}
		}
	}
	t2.Values = vals
	if r.restore(ch, " after T2") {
		delete(r.touched, ch)
	}
	return t2, t3
}

func errText(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

func orText(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
