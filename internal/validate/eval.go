// Package validate implements the N5 Pro EC test specification
// (n5pro-ec/docs/TESTSPEC.md, version SpecVersion): the verdict rules of
// T1 (mapping, with the settle time of §2.1 and the table-input crossing of
// §5.1), T2 (hand-back), T3 (settling) and the aggregation over runs, the G4
// gate, the reasons for T4/T5 (evaluated by n5-ectest report, §5.5) and N/A
// for PCIe, the run procedure on the 5-s grid (§2) against an Env (real hardware or the
// simulator of --dry-run), the replay of raw logs, and the result bundle of
// §8.
//
// Verdicts are computed from the recorded numbers only (TESTSPEC §1 rule 4).
package validate

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// SpecVersion is the TESTSPEC version this code implements.
const SpecVersion = "0.4"

// Verdict of a test or sub-run.
type Verdict string

const (
	Pass         Verdict = "PASS"
	Fail         Verdict = "FAIL"
	Inconclusive Verdict = "INCONCLUSIVE"
	// Reported is used for T6, which has no verdict (TESTSPEC §5:
	// reported values only).
	Reported Verdict = "REPORTED"
	// NotApplicable is a test that does not apply to the channel
	// (TESTSPEC §1 rule 2: PCIe T2-T5, no EC automatic on DCR5).
	NotApplicable Verdict = "N/A"
)

// ReasonNoPCIeAuto is the N/A reason of T2-T5 on pwm4 (TESTSPEC §6).
const ReasonNoPCIeAuto = "no EC automatic on DCR5 (firmware 0.14)"

// PCIeChannel is the pwm channel without EC automatic (DCR5).
const PCIeChannel = 4

// HDDInputStepC is the only table step of the HDD tables C/D: EC 0x06
// (ambient) < 34 C -> 1200 rpm, >= 34 C -> 1600 rpm (TESTSPEC §6).
const HDDInputStepC = 34.0

// Spec numbers (TESTSPEC §4, §5).
const (
	LeaveCounts    = 16  // T2 PASS: DCR differs from the written value by >= 16
	HoldCounts     = 2   // T2 FAIL: DCR within +-2 of the written value
	LeaveWithinS   = 60  // T2 PASS: ... within 60 s after the auto command
	ObserveS       = 900 // T2/T3: log 15 min after the auto command
	SettleBandC    = 2   // T3: DCR constant +-2 ...
	SettleHoldS    = 120 // ... for 120 s
	T1OwnStepRPM   = 200 // T1: own tach changes >= 200 rpm per step
	T1OtherRPM     = 50  // T1: other tachs +-50 rpm
	RunsRequired   = 3   // T2-T5 are run 3x
	MinDuty        = 100 // duty below 100 is never written (§4)
	MaxM           = 200 // T2: M = min(A + 48, 200)
	MOffset        = 48
	LimitTctlC     = 85.0
	LimitNVMeC     = 70.0
	LimitHDDC      = 50.0
	T1StepHoldS    = 60
	SampleS        = 5
	GateLoad1      = 1.0
	GateTempDeltaC = 1.0 // G4: EC system (0x04), EC ambient (0x06) and HDD span < 1 C (0.4 #20; Tctl is a limit only)
	GateWindowS    = 300
	// SettleWriteS is the settle time of pwm1-pwm3 after a write (DCR1-DCR4),
	// SettlePCIeS the one of pwm4 (DCR5), TESTSPEC 0.4 §2.1 (#21): a DCR
	// sample due before it is recorded, not judged.
	SettleWriteS = 10
	SettlePCIeS  = 1
	// CrossMarginC: a table input crosses a step boundary S when it lies at
	// least this far beyond S on the other side (§5.1, §5.2, #17, #24).
	CrossMarginC = 2.0
)

// GridTolerance is how far a sample may be read from its due time and still
// be judged at it (TESTSPEC 0.4 §2 sampling, #11); a slot outside it is a gap.
const GridTolerance = 500 * time.Millisecond

// SettleTime is the settle time of channel ch after a write (§2.1).
func SettleTime(ch int) time.Duration {
	if ch == PCIeChannel {
		return SettlePCIeS * time.Second
	}
	return SettleWriteS * time.Second
}

// TableSteps are the step boundaries S of the EC table of channel ch (§6);
// nil for pwm4 (no EC automatic).
func TableSteps(ch int) []float64 {
	switch ch {
	case 1:
		return []float64{25, 45, 55, 65, 75, 82, 87}
	case 2:
		return []float64{60}
	case 3:
		return []float64{HDDInputStepC}
	}
	return nil
}

// TableInput is the EC table input of channel ch in s (§6): EC 0x09 for
// pwm1, EC 0x05 for pwm2, EC 0x06 for pwm3; NaN for pwm4 or when unknown.
func TableInput(ch int, s Sample) float64 {
	switch ch {
	case 1:
		return s.ECCPU
	case 2:
		return s.ECBoard
	case 3:
		return s.ECAmb
	}
	return math.NaN()
}

// InputName names the table input of channel ch.
func InputName(ch int) string {
	switch ch {
	case 1:
		return "EC 0x09"
	case 2:
		return "EC 0x05"
	case 3:
		return "EC 0x06"
	}
	return "none"
}

// Crossing reports whether a table input crossed a step boundary of steps:
// some value lies >= CrossMarginC beyond S on the other side than ref. ""
// means no crossing (or no reference); otherwise the text names S and the
// value.
func Crossing(ref float64, vals []float64, steps []float64) string {
	if math.IsNaN(ref) {
		for _, v := range vals {
			if !math.IsNaN(v) {
				ref = v
				break
			}
		}
	}
	if math.IsNaN(ref) {
		return ""
	}
	for _, s := range steps {
		above := ref >= s
		for _, v := range vals {
			if math.IsNaN(v) {
				continue
			}
			if above && v <= s-CrossMarginC {
				return fmt.Sprintf("%.1f C below step %.0f C (reference %.1f C)", v, s, ref)
			}
			if !above && v >= s+CrossMarginC {
				return fmt.Sprintf("%.1f C above step %.0f C (reference %.1f C)", v, s, ref)
			}
		}
	}
	return ""
}

func meanF(v []float64) float64 {
	sum, n := 0.0, 0
	for _, x := range v {
		if !math.IsNaN(x) {
			sum += x
			n++
		}
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

// JudgedRegs are the duty registers judged for pwm channel ch (TESTSPEC
// §2): DCR1, DCR2, DCR3 and DCR4 (HDD group, both judged), DCR5. The first
// one is the primary register (A, M and the T1 own tach refer to it).
func JudgedRegs(ch int) []int {
	switch ch {
	case 1, 2:
		return []int{ch}
	case 3:
		return []int{3, 4}
	case 4:
		return []int{5}
	}
	return nil
}

func regName(n int) string { return fmt.Sprintf("DCR%d", n) }

// T1Steps are the duties of the mapping test, in order.
var T1Steps = []int{255, 217, 179, 140}

// Criterion is one named check with the numbers it was computed from.
// Met is nil when the data does not allow a decision.
type Criterion struct {
	Name   string         `json:"name"`
	Met    *bool          `json:"met"`
	Detail string         `json:"detail"`
	Values map[string]any `json:"values,omitempty"`
}

func boolp(b bool) *bool { return &b }

// Result is the verdict of one test.
type Result struct {
	ID       string      `json:"id"`
	Channel  int         `json:"channel,omitempty"` // pwm channel, 0 for channel-independent tests
	Verdict  Verdict     `json:"verdict"`
	Reasons  []string    `json:"reasons,omitempty"`
	Criteria []Criterion `json:"criteria,omitempty"`
	Runs     []RunResult `json:"runs,omitempty"`
	Values   any         `json:"values,omitempty"`
}

// RunResult is one repetition of T2/T3 with its sub-runs.
type RunResult struct {
	Run     int         `json:"run"`
	Verdict Verdict     `json:"verdict"`
	Reason  string      `json:"reason,omitempty"`
	Subs    []SubResult `json:"sub_runs"`
}

// DCRSample is one duty-register reading of the channel under test;
// T is seconds since the auto command (TimingKnown) or since the first
// sample (replay without a hand-back timestamp).
type DCRSample struct {
	T   float64 `json:"t_s"`
	DCR int     `json:"dcr"`
}

// HandbackRun is the data of one T2 sub-run: the written value, then the
// DCR after the auto command.
type HandbackRun struct {
	Label       string      `json:"label"`              // "H", "M" or "replay"
	Reg         string      `json:"register,omitempty"` // "DCR3", ... ("" = the channel's register)
	Written     int         `json:"written"`
	Samples     []DCRSample `json:"samples"`
	TimingKnown bool        `json:"timing_known"` // T is relative to the auto command
	// Auto is the channel's auto value A read before the sub-runs
	// (AutoKnown false: not measured, e.g. replay). A written value closer
	// than LeaveCounts to A cannot show "left the written value" when the EC
	// returns to A, so such a sub-run is INCONCLUSIVE for T2.
	Auto      int  `json:"auto"`
	AutoKnown bool `json:"auto_known"`
	// Hold is the DCR in the hold phase, T in seconds after the write;
	// samples at or after SettleS that differ from Written are listed as
	// hold deviations, they do not change the verdict (TESTSPEC 0.4 §5.2
	// step 3, #21).
	Hold    []DCRSample `json:"hold,omitempty"`
	SettleS float64     `json:"settle_s,omitempty"`
	// InputCrossed, when set, says how the channel's table input crossed a
	// step between the write and the end of the log (or in the window of A):
	// T2 and T3 are INCONCLUSIVE (§5.2, #17).
	InputCrossed string `json:"table_input_crossed,omitempty"`
}

// SubResult is the evaluation of one HandbackRun for T2 and T3.
type SubResult struct {
	Label   string `json:"label"`
	Reg     string `json:"register,omitempty"`
	Written int    `json:"written"`
	// T2
	Left       bool     `json:"left_written_value"`    // some sample differs by >= LeaveCounts
	LeftAtS    *float64 `json:"left_at_s"`             // first such sample (nil: never or timing unknown)
	MaxDev     int      `json:"max_deviation"`         // largest |DCR - written|
	HeldS      float64  `json:"held_within_2_s"`       // observed time with every sample within +-2
	ObservedS  float64  `json:"observed_s"`            // span of the samples
	T2         Verdict  `json:"t2"`                    // sub-run verdict
	T2Reason   string   `json:"t2_reason"`             //
	Settled    bool     `json:"settled"`               // T3
	SettleAtS  *float64 `json:"settle_at_s"`           // start of the first steady window
	FinalValue *int     `json:"final_value,omitempty"` // value of the steady window
	T3         Verdict  `json:"t3"`
	T3Reason   string   `json:"t3_reason"`
	// HoldDeviations: hold-phase samples after the settle time whose DCR
	// differs from Written (reported, not judged; §5.2 step 3).
	HoldDeviations []string `json:"hold_deviations,omitempty"`
	held           bool     // the sub-run has hold-phase data (runner, not replay)
	InputCrossed   string   `json:"table_input_crossed,omitempty"`
	// More than one judged register (pwm3: DCR3 and DCR4, TESTSPEC §5.2):
	// the fields above are DCR3's numbers with the combined verdicts,
	// Others holds DCR4's evaluation. RegsAgree is false when the verdicts
	// of the registers differ (nil: one register); RegNotes lists every
	// difference, including final values (reported, not judged).
	Others    []SubResult `json:"other_registers,omitempty"`
	RegsAgree *bool       `json:"registers_agree,omitempty"`
	RegNotes  []string    `json:"register_notes,omitempty"`
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// EvalHandback evaluates one sub-run for T2 and T3.
func EvalHandback(r HandbackRun) SubResult {
	s := SubResult{Label: r.Label, Reg: r.Reg, Written: r.Written, T2: Inconclusive, T3: Inconclusive}
	if len(r.Samples) == 0 {
		s.T2Reason, s.T3Reason = "no DCR samples", "no DCR samples"
		return s
	}
	smp := append([]DCRSample(nil), r.Samples...)
	sort.SliceStable(smp, func(i, j int) bool { return smp[i].T < smp[j].T })
	first, last := smp[0].T, smp[len(smp)-1].T
	s.ObservedS = last - first
	if r.TimingKnown {
		s.ObservedS = last
	}
	allWithin2 := true
	for _, x := range smp {
		d := absInt(x.DCR - r.Written)
		if d > s.MaxDev {
			s.MaxDev = d
		}
		if d >= LeaveCounts && !s.Left {
			s.Left = true
			if r.TimingKnown {
				t := x.T
				s.LeftAtS = &t
			}
		}
		if d > HoldCounts {
			allWithin2 = false
		}
	}
	if allWithin2 {
		s.HeldS = s.ObservedS
	}
	switch {
	case s.Left && s.LeftAtS != nil && *s.LeftAtS <= LeaveWithinS:
		s.T2, s.T2Reason = Pass, fmt.Sprintf("left the written value %d by >= %d counts after %.0f s", r.Written, LeaveCounts, *s.LeftAtS)
	case s.Left && s.LeftAtS != nil:
		s.T2Reason = fmt.Sprintf("left the written value only after %.0f s (> %d s)", *s.LeftAtS, LeaveWithinS)
	case s.Left:
		s.T2Reason = fmt.Sprintf("left the written value %d (max deviation %d counts), time since the auto command not recorded", r.Written, s.MaxDev)
	// FAIL needs the whole log of the sub-run (TESTSPEC §5.2: 1020 s =
	// ObserveS + SettleHoldS), not only the 900 s of T3's limit
	case allWithin2 && r.TimingKnown && s.ObservedS >= ObserveS+SettleHoldS:
		s.T2, s.T2Reason = Fail, fmt.Sprintf("DCR stayed within +-%d of %d for %.0f s", HoldCounts, r.Written, s.ObservedS)
	case allWithin2:
		s.T2Reason = fmt.Sprintf("DCR within +-%d of the written value, but observed for %.0f s only (need %d s after the auto command)", HoldCounts, s.ObservedS, ObserveS+SettleHoldS)
	default:
		s.T2Reason = fmt.Sprintf("DCR moved by %d counts (between %d and %d)", s.MaxDev, HoldCounts+1, LeaveCounts-1)
	}
	if r.AutoKnown && absInt(r.Written-r.Auto) < LeaveCounts {
		s.T2 = Inconclusive
		s.T2Reason = fmt.Sprintf("written value %d too close to the auto value A=%d (< %d counts): leaving it cannot be told from returning to A (%s)", r.Written, r.Auto, LeaveCounts, s.T2Reason)
	}
	// T3: first window of SettleHoldS seconds with every sample within
	// +-SettleBandC of the window's first sample.
	for i := range smp {
		start := smp[i].T
		end := start + SettleHoldS
		if end > last {
			break
		}
		ok := true
		for j := i; j < len(smp) && smp[j].T <= end; j++ {
			if absInt(smp[j].DCR-smp[i].DCR) > SettleBandC {
				ok = false
				break
			}
		}
		if ok {
			s.Settled = true
			t, v := start, smp[i].DCR
			s.SettleAtS, s.FinalValue = &t, &v
			break
		}
	}
	switch {
	case s.Settled && r.TimingKnown && *s.SettleAtS <= ObserveS:
		s.T3, s.T3Reason = Pass, fmt.Sprintf("steady at %d (+-%d for %d s) from %.0f s after the auto command", *s.FinalValue, SettleBandC, SettleHoldS, *s.SettleAtS)
	case s.Settled && r.TimingKnown:
		s.T3, s.T3Reason = Fail, fmt.Sprintf("steady state only from %.0f s (> %d s)", *s.SettleAtS, ObserveS)
	case s.Settled:
		s.T3Reason = fmt.Sprintf("steady at %d over the observed samples, time since the auto command not recorded", *s.FinalValue)
	case r.TimingKnown && last >= ObserveS+SettleHoldS:
		s.T3, s.T3Reason = Fail, fmt.Sprintf("no steady state within %d s", ObserveS)
	default:
		s.T3Reason = fmt.Sprintf("no steady window in %.0f s of data", s.ObservedS)
	}
	s.held = len(r.Hold) > 0
	for _, x := range r.Hold {
		if x.T+1e-9 >= r.SettleS && x.DCR != r.Written {
			p := ""
			if r.Reg != "" {
				p = r.Reg + " "
			}
			s.HoldDeviations = append(s.HoldDeviations, fmt.Sprintf("%s%.0f s after the write: %d (written %d)", p, x.T, x.DCR, r.Written))
		}
	}
	if r.InputCrossed != "" {
		s.InputCrossed = r.InputCrossed
		why := "table input crossed a step (" + r.InputCrossed + ")"
		s.T2, s.T2Reason = Inconclusive, why+"; "+s.T2Reason
		s.T3, s.T3Reason = Inconclusive, why+"; "+s.T3Reason
	}
	return s
}

// EvalHandbackRegs evaluates one sub-run over every judged register of the
// channel (TESTSPEC §5.2, pwm3: DCR3 and DCR4, each with its own A). runs[0]
// is the primary register; the result carries its numbers, the others are
// in Others. Per test: the same verdict in every register is the verdict;
// different verdicts give INCONCLUSIVE ("DCR3/DCR4 disagree"). Different T3
// final values are noted, not judged (each register regulates on its own
// tach, TESTSPEC §6).
func EvalHandbackRegs(runs []HandbackRun) SubResult {
	if len(runs) == 0 {
		return SubResult{T2: Inconclusive, T3: Inconclusive, T2Reason: "no register", T3Reason: "no register"}
	}
	all := make([]SubResult, len(runs))
	for i, r := range runs {
		all[i] = EvalHandback(r)
	}
	s := all[0]
	if len(all) == 1 {
		return s
	}
	s.Others = all[1:]
	name := func(x SubResult, i int) string {
		if x.Reg != "" {
			return x.Reg
		}
		return fmt.Sprintf("register %d", i+1)
	}
	var names []string
	for i, x := range all {
		names = append(names, name(x, i))
	}
	pair := strings.Join(names, "/")
	agree := true
	merge := func(test string, get func(SubResult) (Verdict, string)) (Verdict, string) {
		var vs []Verdict
		var parts []string
		for i, x := range all {
			v, why := get(x)
			vs = append(vs, v)
			parts = append(parts, fmt.Sprintf("%s %s (%s)", name(x, i), v, why))
		}
		v := combine(vs)
		same := true
		for _, x := range vs {
			same = same && x == vs[0]
		}
		if !same {
			agree = false
			var short []string
			for i, x := range vs {
				short = append(short, fmt.Sprintf("%s %s", names[i], x))
			}
			s.RegNotes = append(s.RegNotes, test+": "+strings.Join(short, ", "))
			return Inconclusive, pair + " disagree: " + strings.Join(parts, "; ")
		}
		return v, strings.Join(parts, "; ")
	}
	s.T2, s.T2Reason = merge("T2", func(x SubResult) (Verdict, string) { return x.T2, x.T2Reason })
	s.T3, s.T3Reason = merge("T3", func(x SubResult) (Verdict, string) { return x.T3, x.T3Reason })
	for _, o := range all[1:] {
		s.Left = s.Left && o.Left
	}
	// final values: reported, not judged
	var finals []string
	differ := false
	for i, x := range all {
		if x.FinalValue == nil {
			finals = append(finals, names[i]+" -")
			continue
		}
		finals = append(finals, fmt.Sprintf("%s %d", names[i], *x.FinalValue))
		if all[0].FinalValue != nil && *x.FinalValue != *all[0].FinalValue {
			differ = true
		}
	}
	if differ {
		s.RegNotes = append(s.RegNotes, "T3 final values differ (reported, not judged): "+strings.Join(finals, ", "))
	}
	s.RegsAgree = boolp(agree)
	return s
}

// combine is the verdict over parts that must all agree (TESTSPEC §5:
// PASS only if all pass, FAIL only if all fail, else INCONCLUSIVE).
func combine(vs []Verdict) Verdict {
	if len(vs) == 0 {
		return Inconclusive
	}
	all := func(v Verdict) bool {
		for _, x := range vs {
			if x != v {
				return false
			}
		}
		return true
	}
	switch {
	case all(Pass):
		return Pass
	case all(Fail):
		return Fail
	}
	return Inconclusive
}

// AggregateHandback builds the T2 and T3 results for one channel from the
// repetitions (each with the sub-runs H and M). want is the number of
// sub-run labels a complete repetition has ("H", "M").
func AggregateHandback(ch int, runs [][]SubResult, want []string) (t2, t3 Result) {
	t2 = Result{ID: "T2", Channel: ch}
	t3 = Result{ID: "T3", Channel: ch}
	var v2, v3 []Verdict
	leftMet, leftSeen := 0, 0
	for i, subs := range runs {
		labels := map[string]bool{}
		var s2, s3 []Verdict
		for _, s := range subs {
			labels[s.Label] = true
			s2 = append(s2, s.T2)
			s3 = append(s3, s.T3)
			leftSeen++
			if s.Left {
				leftMet++
			}
		}
		var missing []string
		for _, l := range want {
			if !labels[l] {
				missing = append(missing, l)
			}
		}
		r2 := RunResult{Run: i + 1, Verdict: combine(s2), Subs: subs}
		r3 := RunResult{Run: i + 1, Verdict: combine(s3), Subs: subs}
		if len(missing) > 0 {
			reason := "sub-run " + strings.Join(missing, ", ") + " missing (one direction only)"
			r2.Verdict, r2.Reason = Inconclusive, reason
			r3.Verdict, r3.Reason = Inconclusive, reason
		} else if r2.Verdict == Inconclusive {
			r2.Reason = "mixed or undecided sub-runs"
		}
		t2.Runs = append(t2.Runs, r2)
		t3.Runs = append(t3.Runs, r3)
		v2 = append(v2, r2.Verdict)
		v3 = append(v3, r3.Verdict)
	}
	t2.Verdict, t3.Verdict = combine(v2), combine(v3)
	if len(runs) < RunsRequired {
		t2.Verdict, t3.Verdict = Inconclusive, Inconclusive
		reason := fmt.Sprintf("n=%d of %d runs", len(runs), RunsRequired)
		t2.Reasons = append(t2.Reasons, reason)
		t3.Reasons = append(t3.Reasons, reason)
	}
	for _, r := range t2.Runs {
		if r.Reason != "" {
			t2.Reasons = append(t2.Reasons, fmt.Sprintf("run %d: %s", r.Run, r.Reason))
		}
		for _, s := range r.Subs {
			t2.Reasons = append(t2.Reasons, fmt.Sprintf("run %d %s: %s", r.Run, s.Label, s.T2Reason))
			t3.Reasons = append(t3.Reasons, fmt.Sprintf("run %d %s: %s", r.Run, s.Label, s.T3Reason))
		}
	}
	if leftSeen > 0 {
		t2.Criteria = append(t2.Criteria, Criterion{
			Name:   "left written value (|DCR - written| >= 16)",
			Met:    boolp(leftMet == leftSeen),
			Detail: fmt.Sprintf("%d of %d sub-runs", leftMet, leftSeen),
		})
	}
	// hold phase after the settle time (§5.2 step 3, #21): listed, never
	// part of the verdict
	var dev, crossed []string
	anyHold := false
	for _, r := range t2.Runs {
		for _, s := range r.Subs {
			anyHold = anyHold || s.held
			all := append([]SubResult{s}, s.Others...)
			for _, x := range all {
				for _, d := range x.HoldDeviations {
					dev = append(dev, fmt.Sprintf("run %d %s: %s", r.Run, s.Label, d))
				}
			}
			if s.InputCrossed != "" {
				crossed = append(crossed, fmt.Sprintf("run %d %s: %s", r.Run, s.Label, s.InputCrossed))
			}
		}
	}
	if anyHold {
		t2.Criteria = append(t2.Criteria, Criterion{Name: "hold phase: DCR equals the written value after the settle time (reported, not judged)",
			Detail: strings.Join(dev, "; "), Values: map[string]any{"hold_deviations": len(dev)}})
		c := Criterion{Name: "table input did not cross a step (write to end of log)", Met: boolp(len(crossed) == 0), Detail: strings.Join(crossed, "; ")}
		t2.Criteria = append(t2.Criteria, c)
		t3.Criteria = append(t3.Criteria, c)
	}
	// pwm3: DCR3 and DCR4 judged together (TESTSPEC §5.2); a disagreement
	// is reported in both tests
	regsCrit := func(test string) (Criterion, bool) {
		seen, agree := 0, 0
		var notes []string
		for _, r := range t2.Runs {
			for _, s := range r.Subs {
				if s.RegsAgree == nil {
					continue
				}
				seen++
				ok := true
				for _, n := range s.RegNotes {
					// "T2: ..." / "T3: ..." are disagreements of that test;
					// "T3 final values ..." is reported with T3, not judged
					if strings.HasPrefix(n, test+": ") {
						ok = false
					}
					if strings.HasPrefix(n, test) {
						notes = append(notes, fmt.Sprintf("run %d %s: %s", r.Run, s.Label, n))
					}
				}
				if ok {
					agree++
				}
			}
		}
		c := Criterion{Name: "registers agree (DCR3 and DCR4: same " + test + " verdict per sub-run)", Met: boolp(agree == seen),
			Detail: fmt.Sprintf("%d of %d sub-runs agree", agree, seen)}
		if len(notes) > 0 {
			c.Detail += "; " + strings.Join(notes, "; ")
		}
		return c, seen > 0
	}
	if c, ok := regsCrit("T2"); ok {
		t2.Criteria = append(t2.Criteria, c)
	}
	if c, ok := regsCrit("T3"); ok {
		t3.Criteria = append(t3.Criteria, c)
	}
	return t2, t3
}

// MValue is the second T2 value: min(A + 48, 200), never below MinDuty.
func MValue(a int) int {
	m := a + MOffset
	if m > MaxM {
		m = MaxM
	}
	if m < MinDuty {
		m = MinDuty
	}
	return m
}

// T1Step is the data of one mapping step.
type T1Step struct {
	Duty   int           `json:"duty"`
	DCR    []int         `json:"dcr"`            // channel DCR per sample (pwm3: DCR3; -1 / empty: unavailable)
	DCR4   []int         `json:"dcr4,omitempty"` // pwm3 only: DCR4 per sample, judged like DCR (TESTSPEC §5.1)
	Fans   map[int][]int `json:"fans"`           // fanN -> rpm per sample (-1 unreadable)
	OwnRPM float64       `json:"own_rpm"`        // mean of the last 3 own-tach samples (filled by EvalT1)
	// DueS is the due time of each sample in seconds after the write (§2
	// sampling); a DCR sample due before SettleS is recorded, not judged
	// (§2.1, #21). Without DueS every sample is judged.
	DueS    []float64 `json:"due_s,omitempty"`
	SettleS float64   `json:"settle_s"`
	// NotJudged lists the DCR samples inside the settle time (filled by
	// EvalT1; TESTSPEC 0.4 §8 verdict.json).
	NotJudged []string `json:"not_judged,omitempty"`
	// Gaps counts slots of this step without a sample on the 5-s grid.
	Gaps int `json:"gaps,omitempty"`
	// Inputs are the table inputs of the channels (pwm1..pwm3) per sample
	// (§5.1, #24); NaN = not read.
	Inputs map[int][]float64 `json:"-"`
}

// judged reports whether sample i of the step lies at or after the settle
// time.
func (s T1Step) judged(i int) bool {
	if i >= len(s.DueS) {
		return true
	}
	return s.DueS[i]+1e-9 >= s.SettleS
}

// EvalT1 evaluates the mapping test of channel ch without table inputs of
// the other channels (no crossing can be seen); see EvalT1Inputs.
func EvalT1(ch, own int, rest map[int][]int, steps []T1Step, fans []int) Result {
	return EvalT1Inputs(ch, own, rest, nil, steps, fans)
}

// EvalT1Inputs evaluates the mapping test of channel ch (TESTSPEC 0.4 §5.1).
// own is the channel's tach (0 = none, PCIe); rest is the per-tach rpm series
// at rest before the first write (noise reference), restIn the table inputs
// per channel at rest; fans lists the tachs (1..3). For pwm3 the DCR
// criterion covers DCR3 (T1Step.DCR) and DCR4 (T1Step.DCR4). Only samples
// due at or after the settle time are judged.
func EvalT1Inputs(ch, own int, rest map[int][]int, restIn map[int][]float64, steps []T1Step, fans []int) Result {
	res := Result{ID: "T1", Channel: ch}
	var reasons []string
	// DCR equals the written value in every judged sample of every step; a
	// judged sample without DCR (-1) or a step without samples leaves the
	// criterion undecided unless another judged sample already differs.
	type regSeries struct {
		name string
		get  func(T1Step) []int
	}
	regs := []regSeries{{"DCR", func(s T1Step) []int { return s.DCR }}}
	if ch == 3 {
		regs = []regSeries{{"DCR3", func(s T1Step) []int { return s.DCR }}, {"DCR4", func(s T1Step) []int { return s.DCR4 }}}
	}
	dcrKnown := true
	dcrOK := true
	var bad, missing []string
	notJudged := 0
	for _, rg := range regs {
		for si := range steps {
			st := &steps[si]
			series := rg.get(*st)
			if len(series) == 0 {
				dcrKnown = false
				missing = append(missing, fmt.Sprintf("step %d: no %s sample", st.Duty, rg.name))
				continue
			}
			nMiss, nJudged := 0, 0
			differs := false
			for i, v := range series {
				if !st.judged(i) {
					notJudged++
					val := "-"
					if v >= 0 {
						val = fmt.Sprint(v)
					}
					st.NotJudged = append(st.NotJudged, fmt.Sprintf("%s %s at %.0f s (settle time %.0f s)", rg.name, val, st.DueS[i], st.SettleS))
					continue
				}
				nJudged++
				switch {
				case v < 0:
					nMiss++
				case v != st.Duty && !differs:
					differs = true
					dcrOK = false
					bad = append(bad, fmt.Sprintf("step %d: %s %d", st.Duty, rg.name, v))
				}
			}
			if nJudged == 0 {
				dcrKnown = false
				missing = append(missing, fmt.Sprintf("step %d: no judged %s sample", st.Duty, rg.name))
			}
			if nMiss > 0 {
				dcrKnown = false
				missing = append(missing, fmt.Sprintf("step %d: %d of %d judged samples without %s", st.Duty, nMiss, nJudged, rg.name))
			}
		}
	}
	c := Criterion{Name: "DCR equals the written value in every judged sample of every step"}
	if ch == 3 {
		c.Name = "DCR3 and DCR4 equal the written value in every judged sample of every step"
	}
	if notJudged > 0 {
		c.Values = map[string]any{"not_judged_samples": notJudged}
	}
	switch {
	case !dcrOK:
		c.Met = boolp(false)
		c.Detail = strings.Join(append(bad, missing...), "; ")
	case dcrKnown:
		c.Met = boolp(true)
	default:
		c.Detail = "DCR not readable: " + strings.Join(missing, "; ")
	}
	res.Criteria = append(res.Criteria, c)
	// noise at rest
	noisy := []string{}
	for _, f := range fans {
		if lo, hi, ok := spread(rest[f]); ok && hi-lo > T1OtherRPM {
			noisy = append(noisy, fmt.Sprintf("fan%d %d rpm", f, hi-lo))
		}
	}
	nc := Criterion{Name: "tach noise at rest <= 50 rpm", Met: boolp(len(noisy) == 0), Detail: strings.Join(noisy, "; ")}
	res.Criteria = append(res.Criteria, nc)
	// other tachs +-50 around their rest mean
	follows := []string{}
	followFan := map[int]bool{}
	for _, f := range fans {
		if f == own {
			continue
		}
		base, ok := mean(rest[f])
		if !ok {
			continue
		}
		for _, st := range steps {
			for _, v := range st.Fans[f] {
				if v >= 0 && math.Abs(float64(v)-base) > T1OtherRPM {
					follows = append(follows, fmt.Sprintf("fan%d %d rpm at step %d (rest %.0f)", f, v, st.Duty, base))
					followFan[f] = true
					break
				}
			}
		}
	}
	res.Criteria = append(res.Criteria, Criterion{Name: "other tachs within +-50 rpm", Met: boolp(len(follows) == 0), Detail: strings.Join(follows, "; ")})
	// table inputs of the other channels with an EC automatic (#24): a
	// crossing explains a following tach of that channel
	crossed := map[int]string{}
	recorded := false
	for _, oc := range []int{1, 2, 3} {
		if oc == ch {
			continue
		}
		var vals []float64
		for _, st := range steps {
			vals = append(vals, st.Inputs[oc]...)
		}
		if !math.IsNaN(meanF(vals)) {
			recorded = true
		}
		if x := Crossing(meanF(restIn[oc]), vals, TableSteps(oc)); x != "" {
			crossed[oc] = fmt.Sprintf("pwm%d %s %s", oc, InputName(oc), x)
		}
	}
	ic := Criterion{Name: "table inputs of the other channels did not cross a step"}
	if recorded {
		ic.Met = boolp(len(crossed) == 0)
		var parts []string
		for _, oc := range []int{1, 2, 3} {
			if crossed[oc] != "" {
				parts = append(parts, crossed[oc])
			}
		}
		ic.Detail = strings.Join(parts, "; ")
	} else {
		ic.Detail = "table inputs not recorded"
	}
	res.Criteria = append(res.Criteria, ic)
	// own tach >= 200 rpm per step, decreasing duty -> decreasing rpm
	ownOK := true
	if own > 0 {
		var short []string
		// a step whose last 3 own-tach samples are all unreadable has no
		// mean; treating it as 0 rpm would fake a drop of >= 200 rpm
		known := make([]bool, len(steps))
		for i := range steps {
			s := steps[i].Fans[own]
			if len(s) > 3 {
				s = s[len(s)-3:]
			}
			steps[i].OwnRPM, known[i] = mean(s)
			if !known[i] {
				ownOK = false
				short = append(short, fmt.Sprintf("step %d: own tach not readable", steps[i].Duty))
			}
			if i == 0 || !known[i] || !known[i-1] {
				continue
			}
			d := steps[i-1].OwnRPM - steps[i].OwnRPM
			if d < T1OwnStepRPM {
				ownOK = false
				short = append(short, fmt.Sprintf("%d->%d: %.0f rpm", steps[i-1].Duty, steps[i].Duty, d))
			}
		}
		res.Criteria = append(res.Criteria, Criterion{Name: "own tach changes >= 200 rpm per step (downwards)", Met: boolp(ownOK), Detail: strings.Join(short, "; ")})
	}
	if ch == 3 {
		// DCR3 vs DCR4 sample by sample: reported (the verdict comes from
		// each register against the written value above)
		var diff []string
		compared := 0
		for _, st := range steps {
			for i := 0; i < len(st.DCR) && i < len(st.DCR4); i++ {
				a, b := st.DCR[i], st.DCR4[i]
				if a < 0 || b < 0 {
					continue
				}
				compared++
				if a != b {
					diff = append(diff, fmt.Sprintf("step %d sample %d: DCR3 %d, DCR4 %d", st.Duty, i+1, a, b))
				}
			}
		}
		rc := Criterion{Name: "DCR3 and DCR4 equal in every sample (reported)", Detail: strings.Join(diff, "; ")}
		if compared > 0 {
			rc.Met = boolp(len(diff) == 0)
		}
		res.Criteria = append(res.Criteria, rc)
	}
	// verdict in the order of §5.1
	var crossWhy []string
	for _, f := range fans {
		if !followFan[f] {
			continue
		}
		if x := crossed[f]; x != "" {
			crossWhy = append(crossWhy, fmt.Sprintf("table input of pwm%d crossed a step (%s)", f, x))
		}
	}
	if len(crossWhy) == 0 && len(follows) > 0 && len(crossed) > 0 {
		// a tach follows while the input of another channel than its own
		// crossed: not FAIL (§5.1: FAIL needs no crossing), and not covered
		// by the INCONCLUSIVE rule for the own input; judged INCONCLUSIVE
		for _, oc := range []int{1, 2, 3} {
			if crossed[oc] != "" {
				crossWhy = append(crossWhy, fmt.Sprintf("another tach follows while the table input of pwm%d crossed a step (%s)", oc, crossed[oc]))
			}
		}
	}
	switch {
	case !dcrOK:
		res.Verdict = Fail
		if ch == 3 {
			reasons = append(reasons, "DCR3 or DCR4 differs from the written value in a judged sample")
		} else {
			reasons = append(reasons, "DCR differs from the written value in a judged sample")
		}
	case len(follows) > 0 && len(noisy) == 0 && len(crossed) == 0:
		res.Verdict = Fail
		reasons = append(reasons, "another tach follows the written channel")
	case len(follows) > 0 && len(crossWhy) > 0:
		res.Verdict = Inconclusive
		reasons = append(reasons, crossWhy...)
	case !dcrKnown:
		res.Verdict = Inconclusive
		reasons = append(reasons, "DCR not readable: the verdict needs the duty registers (driver 0.2.1+sr1, debug_regs=1)")
	case len(noisy) > 0:
		res.Verdict = Inconclusive
		reasons = append(reasons, "tach noise > 50 rpm at rest")
	case !ownOK:
		res.Verdict = Inconclusive
		reasons = append(reasons, "own tach moved less than 200 rpm in a step, or was not readable at the end of a step")
	default:
		res.Verdict = Pass
		switch {
		case ch == 3:
			reasons = append(reasons, "DCR3 and DCR4 equal to the written value in every judged sample, own tach moved >= 200 rpm per step, other tachs within +-50 rpm")
		case own > 0:
			reasons = append(reasons, "DCR equal to the written value in every judged sample, own tach moved >= 200 rpm per step, other tachs within +-50 rpm")
		default:
			reasons = append(reasons, "DCR equal to the written value in every judged sample, all tachs within +-50 rpm (channel without tach)")
		}
	}
	res.Reasons = reasons
	res.Values = steps
	return res
}

func spread(v []int) (lo, hi int, ok bool) {
	for _, x := range v {
		if x < 0 {
			continue
		}
		if !ok || x < lo {
			lo = x
		}
		if !ok || x > hi {
			hi = x
		}
		ok = true
	}
	return
}

func mean(v []int) (float64, bool) {
	sum, n := 0, 0
	for _, x := range v {
		if x >= 0 {
			sum += x
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return float64(sum) / float64(n), true
}

// NotImplemented is the result of a test this tool cannot run yet.
func NotImplemented(id string, ch int, reason string) Result {
	return Result{ID: id, Channel: ch, Verdict: Inconclusive, Reasons: []string{reason}}
}

// NotApplicableResult is a test that does not apply to channel ch.
func NotApplicableResult(id string, ch int, reason string) Result {
	return Result{ID: id, Channel: ch, Verdict: NotApplicable, Reasons: []string{reason}}
}

// Reasons of T4/T5, which validate does not evaluate (TESTSPEC 0.4 §1 rule
// 4, §5.5: the tool is n5-ectest report; a hand evaluation is no result).
const (
	ReasonT4T5Manual   = "not evaluated by validate: TESTSPEC 0.4 §5.5 evaluates T4/T5 with n5-ectest report from samples.csv"
	ReasonHDDBelowStep = "HDD input EC 0x06 below 34 C"
)

// T4T5Result is the result of T4 or T5 for channel ch from the samples of
// this run (TESTSPEC §5, §6): N/A on pwm4 (no EC automatic); on pwm3
// INCONCLUSIVE "HDD input EC 0x06 below 34 C" when every recorded EC 0x06
// value lies below the table step (the input cannot have crossed it);
// otherwise INCONCLUSIVE "not implemented, run manually per §5.5".
func T4T5Result(id string, ch int, samples []Sample) Result {
	if ch == PCIeChannel {
		return NotApplicableResult(id, ch, ReasonNoPCIeAuto)
	}
	if ch == 3 {
		hi, n := math.Inf(-1), 0
		for _, s := range samples {
			if !math.IsNaN(s.ECAmb) {
				hi, n = math.Max(hi, s.ECAmb), n+1
			}
		}
		if n > 0 && hi < HDDInputStepC {
			r := NotImplemented(id, ch, fmt.Sprintf("%s (max %.1f C in %d samples of this run; table step at %.0f C, a crossing by 2 C needs >= %.0f C; TESTSPEC §6)",
				ReasonHDDBelowStep, hi, n, HDDInputStepC, HDDInputStepC+2))
			r.Values = map[string]any{"ec_amb_max": hi, "samples": n}
			return r
		}
		if n == 0 {
			return NotImplemented(id, ch, ReasonT4T5Manual+" (EC 0x06 not recorded in this run)")
		}
	}
	return NotImplemented(id, ch, ReasonT4T5Manual)
}

// G4Result is the evaluation of the G4 window.
type G4Result struct {
	Pass  bool
	Parts []string
	// Limit says which abort-limit sensors (tctl, nvme, hdd) had a value
	// in the window.
	Limit map[string]bool
}

// EvalG4 evaluates the G4 gate window (TESTSPEC 0.4 §3, #20 a): span (max -
// min) of EC system (0x04), EC ambient (0x06, HDD table input) and HDD max
// each < GateTempDeltaC, and every abort-limit sensor (Tctl, NVMe, HDD) with
// a value in the window. Tctl is not a stability criterion (a limit of §4
// only); its span is reported.
func EvalG4(rows []Sample) G4Result {
	g := G4Result{Pass: true, Limit: map[string]bool{}}
	// TESTSPEC §4 limits are only enforced when their sensor has a value:
	// a missing one blocks the writing tests instead of silently skipping
	// its limit (drivetemp not loaded, no NVMe hwmon, ...).
	for _, ls := range []struct {
		name string
		get  func(Sample) float64
	}{{"tctl", func(s Sample) float64 { return s.Tctl }}, {"nvme", func(s Sample) float64 { return s.NVMeMax }}, {"hdd", func(s Sample) float64 { return s.HDDMax }}} {
		seen := false
		for _, s := range rows {
			seen = seen || !math.IsNaN(ls.get(s))
		}
		g.Limit[ls.name] = seen
		if !seen {
			g.Parts = append(g.Parts, "limit sensor "+ls.name+" not available")
			g.Pass = false
		}
	}
	for _, c := range []struct {
		name string
		max  float64
		get  func(Sample) float64
	}{
		{"system (ec 0x04)", GateTempDeltaC, func(s Sample) float64 { return s.ECSys }},
		{"ambient (ec 0x06)", GateTempDeltaC, func(s Sample) float64 { return s.ECAmb }},
		{"hdd", GateTempDeltaC, func(s Sample) float64 { return s.HDDMax }},
		{"tctl", 0, func(s Sample) float64 { return s.Tctl }}, // reported only
	} {
		lo, hi, n := math.Inf(1), math.Inf(-1), 0
		for _, s := range rows {
			v := c.get(s)
			if math.IsNaN(v) {
				continue
			}
			lo, hi, n = math.Min(lo, v), math.Max(hi, v), n+1
		}
		if c.max == 0 {
			if n > 0 {
				g.Parts = append(g.Parts, fmt.Sprintf("tctl %.1f C (not a stability criterion: limit %.0f C only, TESTSPEC 0.4 #20)", hi-lo, LimitTctlC))
			}
			continue
		}
		if n == 0 {
			if c.name != "hdd" { // hdd is reported above as a limit sensor
				g.Parts = append(g.Parts, c.name+" not available")
			}
			g.Pass = false
			continue
		}
		d := hi - lo
		ok := d < c.max
		mark := "<"
		if !ok {
			mark = ">="
			g.Pass = false
		}
		g.Parts = append(g.Parts, fmt.Sprintf("%s %.1f C (%s %.0f C)", c.name, d, mark, c.max))
	}
	return g
}
