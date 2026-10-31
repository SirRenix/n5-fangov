package validate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// REPLAY of the 2026-09-14 measurement (unchanged copies of n5pro-ec
// befunde/raw/06-pwmtest-ch3-20260914-171352.txt and
// 06-i2ec-dcr-20260914.txt): after the hand-back of pwm3 the HDD duty
// register read 0x57 where 0x8c (140) had been written. T2's criterion
// "left the written value" is met; the verdict is INCONCLUSIVE (one
// direction, n=1, no timestamp of the auto command), never FAIL — the
// 2026-09-14 conclusion "the EC does not resume HDD regulation" came from
// a tach criterion and was wrong.
func TestReplay20260914(t *testing.T) {
	dir := filepath.Join("testdata", "replay-20260914")
	pwm, err := os.ReadFile(filepath.Join(dir, "06-pwmtest-ch3-20260914-171352.txt"))
	if err != nil {
		t.Fatal(err)
	}
	i2ec, err := os.ReadFile(filepath.Join(dir, "06-i2ec-dcr-20260914.txt"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePWMTest(string(pwm))
	if err != nil || p.Channel != 3 || p.LastDuty != 0x8c || !p.HandBack {
		t.Fatalf("pwm test log: %+v %v", p, err)
	}
	snaps, err := ParseI2EC(string(i2ec))
	if err != nil {
		t.Fatal(err)
	}
	// 17:20 full dump and 17:21 partial dump; the 17:29 manual write and
	// everything after it are not part of the hand-back
	if len(snaps) != 2 || snaps[0].Clock != "17:20" || snaps[1].Clock != "17:21" {
		t.Fatalf("snapshots: %+v", snaps)
	}
	for _, s := range snaps {
		if v, _ := s.DCR.Channel(3); v != 0x57 || s.DCR.Reg(4) != 0x57 {
			t.Fatalf("%s: DCR3/4 = %#x/%#x, want 0x57", s.Clock, v, s.DCR.Reg(4))
		}
	}
	res, err := Replay(string(pwm), string(i2ec))
	if err != nil {
		t.Fatal(err)
	}
	t2, t3 := res[0], res[1]
	if t2.ID != "T2" || t2.Channel != 3 || t2.Verdict != Inconclusive {
		t.Fatalf("T2 = %s %s, want INCONCLUSIVE", t2.ID, t2.Verdict)
	}
	if len(t2.Criteria) != 1 || t2.Criteria[0].Met == nil || !*t2.Criteria[0].Met || !strings.Contains(t2.Criteria[0].Name, "left written value") {
		t.Fatalf("left-written-value criterion: %+v", t2.Criteria)
	}
	sub := t2.Runs[0].Subs[0]
	if !sub.Left || sub.Written != 0x8c || sub.MaxDev != 0x8c-0x57 || sub.LeftAtS != nil || sub.T2 != Inconclusive {
		t.Fatalf("sub-run: %+v", sub)
	}
	joined := strings.Join(t2.Reasons, "\n")
	for _, want := range []string{"n=1 of 3 runs", "sub-run H, M missing (one direction only)", "time since the auto command not recorded", "last written duty 140"} {
		if !strings.Contains(joined, want) {
			t.Errorf("T2 reasons lack %q:\n%s", want, joined)
		}
	}
	if t3.ID != "T3" || t3.Verdict != Inconclusive {
		t.Fatalf("T3 = %s", t3.Verdict)
	}
	if b, err := json.Marshal(res); err != nil || !strings.Contains(string(b), `"verdict":"INCONCLUSIVE"`) {
		t.Fatalf("json: %v %s", err, b)
	}
}

func f(v float64) *float64 { return &v }

func TestEvalHandback(t *testing.T) {
	series := func(written int, vals map[float64]int, upto float64) []DCRSample {
		var out []DCRSample
		cur := written
		for ts := 5.0; ts <= upto; ts += 5 {
			if v, ok := vals[ts]; ok {
				cur = v
			}
			out = append(out, DCRSample{T: ts, DCR: cur})
		}
		return out
	}
	// leaves after 10 s, steady from then on: T2 PASS, T3 PASS
	s := EvalHandback(HandbackRun{Label: "H", Written: 255, Samples: series(255, map[float64]int{10: 0x57}, 900), TimingKnown: true})
	if s.T2 != Pass || s.T3 != Pass || *s.LeftAtS != 10 || *s.FinalValue != 0x57 {
		t.Fatalf("leave: %+v", s)
	}
	// stays within +-2 for the whole 1020-s log: T2 FAIL
	s = EvalHandback(HandbackRun{Label: "M", Written: 140, Samples: series(140, map[float64]int{300: 141}, ObserveS+SettleHoldS), TimingKnown: true})
	if s.T2 != Fail || s.Left {
		t.Fatalf("hold: %+v", s)
	}
	// within +-2, but the log ends at 900 s (abort between 900 and 1020 s): not FAIL
	s = EvalHandback(HandbackRun{Label: "M", Written: 140, Samples: series(140, nil, ObserveS), TimingKnown: true})
	if s.T2 != Inconclusive {
		t.Fatalf("hold 900 s only: %+v", s)
	}
	// leaves only after 90 s: INCONCLUSIVE
	s = EvalHandback(HandbackRun{Label: "H", Written: 255, Samples: series(255, map[float64]int{90: 200}, 900), TimingKnown: true})
	if s.T2 != Inconclusive || !s.Left || *s.LeftAtS != 90 {
		t.Fatalf("late: %+v", s)
	}
	// moves 10 counts: neither
	s = EvalHandback(HandbackRun{Label: "H", Written: 255, Samples: series(255, map[float64]int{20: 245}, 900), TimingKnown: true})
	if s.T2 != Inconclusive {
		t.Fatalf("small move: %+v", s)
	}
	// within +-2 but observed 5 min only: not FAIL
	s = EvalHandback(HandbackRun{Label: "H", Written: 140, Samples: series(140, nil, 300), TimingKnown: true})
	if s.T2 != Inconclusive {
		t.Fatalf("short: %+v", s)
	}
	// keeps oscillating: T3 FAIL
	osc := []DCRSample{}
	for ts := 5.0; ts <= 1100; ts += 5 {
		v := 100
		if int(ts/5)%2 == 0 {
			v = 120
		}
		osc = append(osc, DCRSample{T: ts, DCR: v})
	}
	if s = EvalHandback(HandbackRun{Label: "H", Written: 255, Samples: osc, TimingKnown: true}); s.T3 != Fail || s.T2 != Pass {
		t.Fatalf("oscillating: %+v", s)
	}
	if s = EvalHandback(HandbackRun{Label: "H", Written: 255}); s.T2 != Inconclusive || s.T3 != Inconclusive {
		t.Fatalf("empty: %+v", s)
	}
	_ = f
}

// T3 over the run's real observation length (Observe + SettleHoldS): a
// steady state from 800 s passes, one from 910 s fails, none at all fails.
func TestEvalHandbackT3Window(t *testing.T) {
	obs := float64(ObserveS + SettleHoldS)
	mk := func(steadyFrom float64) []DCRSample {
		var out []DCRSample
		for ts := 5.0; ts <= obs; ts += 5 {
			v := 100
			if ts < steadyFrom && int(ts/5)%2 == 0 {
				v = 130
			}
			out = append(out, DCRSample{T: ts, DCR: v})
		}
		return out
	}
	if s := EvalHandback(HandbackRun{Label: "H", Written: 255, Samples: mk(800), TimingKnown: true}); s.T3 != Pass || *s.SettleAtS > 800 {
		t.Fatalf("steady from 800 s: %s %s", s.T3, s.T3Reason)
	}
	if s := EvalHandback(HandbackRun{Label: "H", Written: 255, Samples: mk(910), TimingKnown: true}); s.T3 != Fail {
		t.Fatalf("steady from 910 s: %s %s", s.T3, s.T3Reason)
	}
	if s := EvalHandback(HandbackRun{Label: "H", Written: 255, Samples: mk(obs + 10), TimingKnown: true}); s.T3 != Fail {
		t.Fatalf("never steady: %s %s", s.T3, s.T3Reason)
	}
	// with only 900 s of data no FAIL is possible (the old run length)
	short := mk(obs + 10)
	short = short[:ObserveS/5]
	if s := EvalHandback(HandbackRun{Label: "H", Written: 255, Samples: short, TimingKnown: true}); s.T3 != Inconclusive {
		t.Fatalf("900 s of data: %s %s", s.T3, s.T3Reason)
	}
}

// A written value within 16 counts of A cannot show "left the written
// value": the sub-run is INCONCLUSIVE with that reason, never FAIL.
func TestEvalHandbackTooCloseToAuto(t *testing.T) {
	var smp []DCRSample
	for ts := 5.0; ts <= ObserveS+SettleHoldS; ts += 5 {
		smp = append(smp, DCRSample{T: ts, DCR: 200})
	}
	s := EvalHandback(HandbackRun{Label: "M", Written: 200, Samples: smp, TimingKnown: true, Auto: 200, AutoKnown: true})
	if s.T2 != Inconclusive || !strings.Contains(s.T2Reason, "too close to the auto value A=200") {
		t.Fatalf("M = A: %s %s", s.T2, s.T2Reason)
	}
	s = EvalHandback(HandbackRun{Label: "M", Written: 200, Samples: smp, TimingKnown: true, Auto: 150, AutoKnown: true})
	if s.T2 != Fail {
		t.Fatalf("M far from A: %s %s", s.T2, s.T2Reason)
	}
}

func TestAggregateHandback(t *testing.T) {
	p := SubResult{Label: "H", T2: Pass, T3: Pass, Left: true}
	pm := p
	pm.Label = "M"
	fl := SubResult{Label: "H", T2: Fail, T3: Pass}
	flm := fl
	flm.Label = "M"
	t2, t3 := AggregateHandback(3, [][]SubResult{{p, pm}, {p, pm}, {p, pm}}, []string{"H", "M"})
	if t2.Verdict != Pass || t3.Verdict != Pass {
		t.Fatalf("3x pass: %s %s", t2.Verdict, t3.Verdict)
	}
	t2, _ = AggregateHandback(3, [][]SubResult{{fl, flm}, {fl, flm}, {fl, flm}}, []string{"H", "M"})
	if t2.Verdict != Fail {
		t.Fatalf("3x fail: %s", t2.Verdict)
	}
	t2, _ = AggregateHandback(3, [][]SubResult{{p, pm}, {fl, flm}, {p, pm}}, []string{"H", "M"})
	if t2.Verdict != Inconclusive {
		t.Fatalf("mixed runs: %s", t2.Verdict)
	}
	t2, _ = AggregateHandback(3, [][]SubResult{{p, flm}, {p, flm}, {p, flm}}, []string{"H", "M"})
	if t2.Verdict != Inconclusive {
		t.Fatalf("mixed sub-runs: %s", t2.Verdict)
	}
	t2, _ = AggregateHandback(3, [][]SubResult{{p, pm}, {p, pm}}, []string{"H", "M"})
	if t2.Verdict != Inconclusive || !strings.Contains(strings.Join(t2.Reasons, ";"), "n=2 of 3") {
		t.Fatalf("n=2: %+v", t2)
	}
	for a, want := range map[int]int{0x55: 133, 0x57: 135, 180: 200, 30: 100} {
		if got := MValue(a); got != want {
			t.Errorf("MValue(%d) = %d, want %d", a, got, want)
		}
	}
}

func TestEvalT1(t *testing.T) {
	rest := map[int][]int{1: {2000, 2010}, 2: {2160, 2170}, 3: {1640, 1650}}
	mk := func(dcrOff int, follow bool) []T1Step {
		var out []T1Step
		rpm := map[int]int{255: 3540, 217: 3160, 179: 2725, 140: 2250}
		for _, d := range T1Steps {
			st := T1Step{Duty: d, DCR: []int{d + dcrOff, d + dcrOff}, DCR4: []int{d + dcrOff, d + dcrOff}, Fans: map[int][]int{1: {2005, 2003}, 2: {2165, 2166}, 3: {rpm[d], rpm[d]}}}
			if follow {
				st.Fans[1] = []int{rpm[d] - 1500, rpm[d] - 1500}
			}
			out = append(out, st)
		}
		return out
	}
	if r := EvalT1(3, 3, rest, mk(0, false), []int{1, 2, 3}); r.Verdict != Pass {
		t.Fatalf("pass: %+v", r)
	}
	if r := EvalT1(3, 3, rest, mk(5, false), []int{1, 2, 3}); r.Verdict != Fail {
		t.Fatalf("dcr off: %+v", r)
	}
	if r := EvalT1(3, 3, rest, mk(0, true), []int{1, 2, 3}); r.Verdict != Fail {
		t.Fatalf("other tach follows: %+v", r)
	}
	noisy := map[int][]int{1: {2000, 2100}, 2: {2160}, 3: {1640}}
	if r := EvalT1(3, 3, noisy, mk(0, false), []int{1, 2, 3}); r.Verdict != Inconclusive {
		t.Fatalf("noise: %+v", r)
	}
	nodcr := mk(0, false)
	for i := range nodcr {
		nodcr[i].DCR, nodcr[i].DCR4 = nil, nil
	}
	if r := EvalT1(3, 3, rest, nodcr, []int{1, 2, 3}); r.Verdict != Inconclusive {
		t.Fatalf("no dcr: %+v", r)
	}
	// one sample of one step without DCR: every sample is required
	gap := mk(0, false)
	gap[2].DCR = []int{gap[2].Duty, -1}
	if r := EvalT1(3, 3, rest, gap, []int{1, 2, 3}); r.Verdict != Inconclusive || r.Criteria[0].Met != nil {
		t.Fatalf("dcr gap: %+v", r)
	}
	// a differing sample still decides FAIL even with a gap elsewhere
	gap[1].DCR = []int{gap[1].Duty + 3, gap[1].Duty}
	if r := EvalT1(3, 3, rest, gap, []int{1, 2, 3}); r.Verdict != Fail {
		t.Fatalf("dcr gap + mismatch: %+v", r)
	}
	// own tach unreadable at the end of the last step: no fake drop, not PASS
	blind := mk(0, false)
	blind[3].Fans[3] = []int{-1, -1}
	if r := EvalT1(3, 3, rest, blind, []int{1, 2, 3}); r.Verdict != Inconclusive {
		t.Fatalf("own tach unreadable: %+v", r)
	}
	// PCIe: no own tach, DCR equal, others quiet
	if r := EvalT1(4, 0, rest, func() []T1Step {
		s := mk(0, false)
		for i := range s {
			s[i].Fans[3] = []int{1645, 1645}
		}
		return s
	}(), []int{1, 2, 3}); r.Verdict != Pass {
		t.Fatalf("pcie: %+v", r)
	}
}

// --dry-run path: the full procedure against the simulator on a virtual
// clock, then the bundle.
func TestRunnerSimulated(t *testing.T) {
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	sim := NewSim(start)
	r := NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	ctx := context.Background()
	g3, g4 := r.Gates(ctx)
	if g3.Result != "PASS" || g4.Result != "PASS" {
		t.Fatalf("gates %+v %+v", g3, g4)
	}
	t1 := r.RunT1(ctx)
	if t1.Verdict != Pass {
		t.Fatalf("T1 %+v", t1)
	}
	t2, t3 := r.RunHandback(ctx)
	if t2.Verdict != Pass || t3.Verdict != Pass || len(t2.Runs) != 3 {
		t.Fatalf("T2 %s T3 %s runs %d: %v", t2.Verdict, t3.Verdict, len(t2.Runs), t2.Reasons)
	}
	r.Close()
	if v, _ := sim.DCR(); v.Reg(3) != 140 {
		t.Fatalf("pwm3 not restored to its stop duty: %s", v)
	}
	for _, s := range r.Samples {
		if s.Written >= 0 && s.Written < MinDuty {
			t.Fatalf("duty %d written", s.Written)
		}
	}
	dir := filepath.Join(t.TempDir(), DirName(start, "F8NAA", "1.05", "validate-pwm3"))
	b := Bundle{Meta: map[string]any{"spec": SpecVersion}, Samples: r.Samples, Events: r.Events, Verdict: []Result{t1, t2, t3}}
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil || strings.Count(string(sums), "\n") != 4 {
		t.Fatalf("SHA256SUMS: %v %q", err, sums)
	}
	csv, _ := os.ReadFile(filepath.Join(dir, "samples.csv"))
	if !strings.HasPrefix(string(csv), strings.Join(CSVHeader, ",")+"\n") {
		t.Fatalf("csv header: %.200s", csv)
	}
	if !strings.HasSuffix(dir, "20261009-1200-F8NAA-1.05-validate-pwm3") {
		t.Fatalf("dir name %s", dir)
	}
	if err := b.Write(dir); err == nil {
		t.Fatal("an existing bundle directory must not be overwritten")
	}

	// PCIe has no EC automatic (TESTSPEC 0.3 §6): T2/T3 N/A, nothing written
	sim2 := NewSim(start)
	r2 := NewRunner(sim2, SpecConfig(4, 0, []int{1, 2, 3}))
	t2p, t3p := r2.RunHandback(ctx)
	r2.Close()
	if t2p.Verdict != NotApplicable || t3p.Verdict != NotApplicable || t2p.Reasons[0] != ReasonNoPCIeAuto || sim2.Writes != 0 || len(r2.Samples) != 0 {
		t.Fatalf("pcie T2 %s T3 %s %v, writes %d, samples %d", t2p.Verdict, t3p.Verdict, t2p.Reasons, sim2.Writes, len(r2.Samples))
	}
}

// Without the register dump nothing is written and the verdicts say why.
func TestRunnerNoDCRWritesNothing(t *testing.T) {
	sim := NewSim(time.Now())
	sim.NoDCR = true
	r := NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	t1 := r.RunT1(context.Background())
	t2, t3 := r.RunHandback(context.Background())
	r.Close()
	if sim.Writes != 0 || t1.Verdict != Inconclusive || t2.Verdict != Inconclusive || t3.Verdict != Inconclusive {
		t.Fatalf("writes %d, verdicts %s %s %s", sim.Writes, t1.Verdict, t2.Verdict, t3.Verdict)
	}
	if !strings.Contains(t1.Reasons[0], "nothing was written") {
		t.Fatalf("reason %q", t1.Reasons)
	}
}

// Limits of §4 abort the run and Close restores the channel.
func TestRunnerAbortsOnLimit(t *testing.T) {
	sim := NewSim(time.Now())
	r := NewRunner(sim, SpecConfig(1, 1, []int{1, 2, 3}))
	sim.Tctl = 86
	t1 := r.RunT1(context.Background())
	if t1.Verdict != Inconclusive || !strings.Contains(r.Abort, "Tctl") {
		t.Fatalf("abort: %q %+v", r.Abort, t1)
	}
	// cancellation: restore still runs
	sim.Tctl = 40
	r = NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.manual(3); err != nil {
		t.Fatal(err)
	}
	cancel()
	if r.RunT1(ctx).Verdict != Inconclusive || !strings.Contains(r.Abort, "cancelled") {
		t.Fatalf("cancel: %q", r.Abort)
	}
	r.Close()
	if v, _ := sim.DCR(); v.Reg(3) != 140 {
		t.Fatalf("not restored: %s", v)
	}
	if err := r.write(3, 99); err == nil {
		t.Fatal("duty 99 written")
	}
}

// T3 FAIL is reachable in a run: a channel that never settles fails all
// three repetitions (the run observes Observe + SettleHoldS).
func TestRunnerT3FailReachable(t *testing.T) {
	sim := NewSim(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	sim.Oscillate[1] = true
	r := NewRunner(sim, SpecConfig(1, 1, []int{1, 2, 3}))
	t2, t3 := r.RunHandback(context.Background())
	r.Close()
	if t3.Verdict != Fail {
		t.Fatalf("T3 %s: %v", t3.Verdict, t3.Reasons)
	}
	if t2.Verdict == Fail {
		t.Fatalf("T2 %s: %v", t2.Verdict, t2.Reasons)
	}
	if got := r.Samples[len(r.Samples)-1].TS - r.Samples[len(r.Samples)-1-int(r.Cfg.HandbackObserve()/r.Cfg.Sample)+1].TS; got < float64(ObserveS) {
		t.Fatalf("sub-run observed %.0f s", got)
	}
}

// G4 blocks when a limit sensor has no value; a sensor lost later aborts.
func TestRunnerLimitSensors(t *testing.T) {
	sim := NewSim(time.Now())
	sim.HDD = math.NaN()
	r := NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	_, g4 := r.Gates(context.Background())
	if g4.Result != "FAIL" || !strings.Contains(g4.Detail, "limit sensor hdd not available") {
		t.Fatalf("G4 %+v", g4)
	}
	sim = NewSim(time.Now())
	r = NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	if _, g4 := r.Gates(context.Background()); g4.Result != "PASS" {
		t.Fatalf("G4 %+v", g4)
	}
	sim.NVMe = math.NaN()
	if res := r.RunT1(context.Background()); res.Verdict != Inconclusive || !strings.Contains(r.Abort, "limit sensor nvme unreadable") {
		t.Fatalf("lost sensor: %q %+v", r.Abort, res)
	}
	r.Close()
	if len(r.Unrestored()) != 0 {
		t.Fatalf("unrestored %v", r.Unrestored())
	}
}

// The guard (G1 during the run) aborts as soon as another writer appears.
func TestRunnerGuardAborts(t *testing.T) {
	sim := NewSim(time.Now())
	cfg := SpecConfig(3, 3, []int{1, 2, 3})
	n := 0
	cfg.Guard = func() string {
		n++
		if n > 20 {
			return "n5-fangov activating"
		}
		return ""
	}
	r := NewRunner(sim, cfg)
	if res := r.RunT1(context.Background()); res.Verdict != Inconclusive || !strings.Contains(r.Abort, "n5-fangov activating") {
		t.Fatalf("guard: %q %+v", r.Abort, res)
	}
	r.Close()
	if v, _ := sim.DCR(); v.Reg(3) != 140 {
		t.Fatalf("not restored: %s", v)
	}
}

// A failed restore stays visible (Unrestored); a written stop value with a
// differing register is a warning, not a failure.
func TestRunnerRestoreOutcome(t *testing.T) {
	sim := NewSim(time.Now())
	r := NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	if err := r.manual(3); err != nil {
		t.Fatal(err)
	}
	sim.RestoreErr[3] = errors.New("EIO")
	r.Close()
	r.Close()
	if u := r.Unrestored(); len(u) != 1 || !strings.Contains(u[0], "pwm3: EIO") {
		t.Fatalf("unrestored %v", u)
	}
	sim.RestoreErr[3] = fmt.Errorf("%w: DCR3 reads 87", ErrRestoreUnverified)
	r.Close()
	if len(r.Unrestored()) != 0 || len(r.RestoreWarnings) != 1 || !strings.Contains(strings.Join(r.Events, "\n"), "restore pwm3 WRITTEN, REGISTER DIFFERS") {
		t.Fatalf("unverified: %v %v %v", r.Unrestored(), r.RestoreWarnings, r.Events)
	}
}

// The first sample of the gate window is checked against the limits like
// every other one (TESTSPEC §4 "in any sample"): Tctl over the limit there
// aborts at once, G4 fails and the window is not sampled further.
func TestGatesFirstSampleLimit(t *testing.T) {
	sim := NewSim(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	sim.Tctl = LimitTctlC + 1
	r := NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	g3, g4 := r.Gates(context.Background())
	// G3 is judged from that sample (load 0.2: PASS); G4 fails and blocks
	if g3.Result != "PASS" || g4.Result != "FAIL" || !strings.Contains(r.Abort, "Tctl") || len(r.Samples) != 1 {
		t.Fatalf("g3 %+v g4 %+v abort %q samples %d", g3, g4, r.Abort, len(r.Samples))
	}
	// an abort before the window: nothing is sampled, both gates fail
	r2 := NewRunner(NewSim(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)), SpecConfig(3, 3, []int{1, 2, 3}))
	r2.Stop("boot-state sample over a limit")
	g3, g4 = r2.Gates(context.Background())
	if g3.Result != "FAIL" || g4.Result != "FAIL" || len(r2.Samples) != 0 {
		t.Fatalf("pre-aborted: %+v %+v %d", g3, g4, len(r2.Samples))
	}
}

// G4 (TESTSPEC 0.4 §3, #20 a): EC system, EC ambient and HDD span < 1 C,
// every limit sensor present; Tctl is reported, never a stability criterion.
func TestEvalG4SplitThresholds(t *testing.T) {
	c := func(v float64) func(int) float64 { return func(int) float64 { return v } }
	alt := func(a, b float64) func(int) float64 {
		return func(i int) float64 {
			if i%2 == 0 {
				return a
			}
			return b
		}
	}
	rows := func(tctl, sys, amb, hdd, nvme func(int) float64) []Sample {
		var out []Sample
		for i := 0; i <= GateWindowS/SampleS; i++ {
			out = append(out, Sample{Tctl: tctl(i), ECSys: sys(i), ECAmb: amb(i), HDDMax: hdd(i), NVMeMax: nvme(i)})
		}
		return out
	}
	cases := []struct {
		name                    string
		tctl, sys, amb, hdd, nv func(int) float64
		pass                    bool
		detail                  string
	}{
		// window 1, D1 (2026-10-17): Tctl 13.5 C span, EC/HDD 0.0 C
		{"tctl 13.5 C (failed under 0.3)", alt(35, 48.5), c(33), c(22), c(38), c(45), true, "tctl 13.5 C (not a stability criterion"},
		{"system 1.0 C", c(40), alt(33, 34), c(22), c(38), c(45), false, "system (ec 0x04) 1.0 C (>= 1 C)"},
		{"system 0.9 C", c(40), alt(33, 33.9), c(22), c(38), c(45), true, "system (ec 0x04) 0.9 C (< 1 C)"},
		{"ambient 1.0 C", c(40), c(33), alt(22, 23), c(38), c(45), false, "ambient (ec 0x06) 1.0 C (>= 1 C)"},
		{"ambient missing", c(40), c(33), c(math.NaN()), c(38), c(45), false, "ambient (ec 0x06) not available"},
		{"hdd 1.0 C", c(40), c(33), c(22), alt(38, 39), c(45), false, "hdd 1.0 C (>= 1 C)"},
		{"nvme missing", c(40), c(33), c(22), c(38), c(math.NaN()), false, "limit sensor nvme not available"},
		{"tctl missing", c(math.NaN()), c(33), c(22), c(38), c(45), false, "limit sensor tctl not available"},
	}
	for _, tc := range cases {
		g := EvalG4(rows(tc.tctl, tc.sys, tc.amb, tc.hdd, tc.nv))
		if g.Pass != tc.pass || !strings.Contains(strings.Join(g.Parts, "; "), tc.detail) {
			t.Errorf("%s: pass %v %v", tc.name, g.Pass, g.Parts)
		}
	}
	if g := EvalG4(rows(c(40), c(33), c(22), c(38), c(math.NaN()))); g.Limit["nvme"] || !g.Limit["tctl"] || !g.Limit["hdd"] {
		t.Errorf("limit sensors %v", g.Limit)
	}
}

// wobbleEnv is the simulator with Tctl, EC system and EC CPU values per read.
type wobbleEnv struct {
	*Sim
	n               int
	tctl, sys, ecpu []float64
}

func (w *wobbleEnv) Read() Readings {
	rd := w.Sim.Read()
	if len(w.tctl) > 0 {
		rd.Tctl = w.tctl[w.n%len(w.tctl)]
	}
	if len(w.sys) > 0 {
		rd.ECSys = w.sys[w.n%len(w.sys)]
	}
	if len(w.ecpu) > 0 {
		rd.ECCPU = w.ecpu[min(w.n, len(w.ecpu)-1)]
	}
	w.n++
	return rd
}

// The runner's gate window: a Tctl that jumps by 13 C passes (0.4 #20), the
// EC system sensor does not.
func TestGatesTctlSplit(t *testing.T) {
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	r := NewRunner(&wobbleEnv{Sim: NewSim(start), tctl: []float64{35, 48}}, SpecConfig(3, 3, []int{1, 2, 3}))
	if _, g4 := r.Gates(context.Background()); g4.Result != "PASS" || !strings.Contains(g4.Detail, "tctl 13.0 C (not a stability criterion") {
		t.Fatalf("Tctl 13 C: %+v", g4)
	}
	r = NewRunner(&wobbleEnv{Sim: NewSim(start), sys: []float64{33, 34.5}}, SpecConfig(3, 3, []int{1, 2, 3}))
	if _, g4 := r.Gates(context.Background()); g4.Result != "FAIL" || !strings.Contains(g4.Detail, "system (ec 0x04) 1.5 C") {
		t.Fatalf("system 1.5 C: %+v", g4)
	}
}

// T1 on pwm3 judges DCR3 and DCR4 (TESTSPEC 0.3 §5.1).
func TestEvalT1DCR4(t *testing.T) {
	rest := map[int][]int{1: {2000, 2010}, 2: {2160, 2170}, 3: {1640, 1650}}
	rpm := map[int]int{255: 3540, 217: 3160, 179: 2725, 140: 2250}
	mk := func() []T1Step {
		var out []T1Step
		for _, d := range T1Steps {
			out = append(out, T1Step{Duty: d, DCR: []int{d, d}, DCR4: []int{d, d}, Fans: map[int][]int{1: {2005, 2003}, 2: {2165, 2166}, 3: {rpm[d], rpm[d]}}})
		}
		return out
	}
	r := EvalT1(3, 3, rest, mk(), []int{1, 2, 3})
	last := r.Criteria[len(r.Criteria)-1]
	if r.Verdict != Pass || last.Met == nil || !*last.Met {
		t.Fatalf("both equal: %+v", r)
	}
	// DCR4 differs in one sample: FAIL, the difference is listed
	st := mk()
	st[2].DCR4 = []int{179, 182}
	r = EvalT1(3, 3, rest, st, []int{1, 2, 3})
	last = r.Criteria[len(r.Criteria)-1]
	if r.Verdict != Fail || !strings.Contains(r.Criteria[0].Detail, "step 179: DCR4 182") || last.Met == nil || *last.Met || !strings.Contains(last.Detail, "DCR3 179, DCR4 182") {
		t.Fatalf("DCR4 differs: %+v", r)
	}
	if !strings.Contains(r.Reasons[0], "DCR3 or DCR4") {
		t.Fatalf("reason %v", r.Reasons)
	}
	// DCR4 not readable: not PASS
	st = mk()
	for i := range st {
		st[i].DCR4 = nil
	}
	r = EvalT1(3, 3, rest, st, []int{1, 2, 3})
	if r.Verdict != Inconclusive || r.Criteria[0].Met != nil || !strings.Contains(r.Criteria[0].Detail, "no DCR4 sample") {
		t.Fatalf("DCR4 missing: %+v", r)
	}
	// other channels ignore DCR4
	st = mk()
	for i := range st {
		st[i].DCR4 = nil
	}
	if r = EvalT1(1, 1, rest, func() []T1Step {
		s := st
		for i := range s {
			s[i].Fans[1], s[i].Fans[3] = []int{rpm[s[i].Duty], rpm[s[i].Duty]}, []int{1645, 1645}
		}
		return s
	}(), []int{1, 2, 3}); r.Verdict != Pass {
		t.Fatalf("pwm1 without DCR4: %+v", r)
	}
}

// T2/T3 on pwm3: DCR3 and DCR4 evaluated per register; a disagreement is
// INCONCLUSIVE and reported, final values are reported, not judged.
func TestEvalHandbackRegs(t *testing.T) {
	series := func(written int, vals map[float64]int) []DCRSample {
		var out []DCRSample
		cur := written
		for ts := 5.0; ts <= ObserveS+SettleHoldS; ts += 5 {
			if v, ok := vals[ts]; ok {
				cur = v
			}
			out = append(out, DCRSample{T: ts, DCR: cur})
		}
		return out
	}
	run := func(reg string, smp []DCRSample) HandbackRun {
		return HandbackRun{Label: "H", Reg: reg, Written: 255, Samples: smp, TimingKnown: true, Auto: 0x57, AutoKnown: true}
	}
	leave := series(255, map[float64]int{10: 0x57})
	stay := series(255, nil)
	s := EvalHandbackRegs([]HandbackRun{run("DCR3", leave), run("DCR4", leave)})
	if s.T2 != Pass || s.T3 != Pass || s.RegsAgree == nil || !*s.RegsAgree || len(s.RegNotes) != 0 || len(s.Others) != 1 {
		t.Fatalf("agree: %+v", s)
	}
	s = EvalHandbackRegs([]HandbackRun{run("DCR3", leave), run("DCR4", stay)})
	notes := strings.Join(s.RegNotes, "; ")
	if s.T2 != Inconclusive || !strings.HasPrefix(s.T2Reason, "DCR3/DCR4 disagree") || s.RegsAgree == nil || *s.RegsAgree || s.Left {
		t.Fatalf("disagree: %+v", s)
	}
	if !strings.Contains(notes, "T2: DCR3 PASS, DCR4 FAIL") || !strings.Contains(notes, "T3 final values differ (reported, not judged): DCR3 87, DCR4 255") {
		t.Fatalf("notes: %s", notes)
	}
	if s.T3 != Pass {
		t.Fatalf("T3 both steady: %s %s", s.T3, s.T3Reason)
	}
	s = EvalHandbackRegs([]HandbackRun{run("DCR3", stay), run("DCR4", stay)})
	if s.T2 != Fail {
		t.Fatalf("both stay: %s %s", s.T2, s.T2Reason)
	}
	// aggregation: the disagreement is a T2 criterion, not a T3 one
	d := EvalHandbackRegs([]HandbackRun{run("DCR3", leave), run("DCR4", stay)})
	dm := d
	dm.Label = "M"
	t2, t3 := AggregateHandback(3, [][]SubResult{{d, dm}, {d, dm}, {d, dm}}, []string{"H", "M"})
	c2 := t2.Criteria[len(t2.Criteria)-1]
	c3 := t3.Criteria[len(t3.Criteria)-1]
	if t2.Verdict != Inconclusive || c2.Met == nil || *c2.Met || !strings.Contains(c2.Name, "same T2 verdict") {
		t.Fatalf("aggregate T2: %s %+v", t2.Verdict, t2.Criteria)
	}
	if t3.Verdict != Pass || c3.Met == nil || !*c3.Met || !strings.Contains(c3.Detail, "T3 final values differ") {
		t.Fatalf("aggregate T3: %s %+v", t3.Verdict, t3.Criteria)
	}
}

// Runner on pwm3 with DCR4 stuck after the auto command: T2 INCONCLUSIVE
// (DCR3/DCR4 disagree), A per register recorded.
func TestRunnerDCR4Disagree(t *testing.T) {
	sim := NewSim(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	sim.KeepDCR4OnAuto = true
	r := NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	t2, t3 := r.RunHandback(context.Background())
	r.Close()
	if t2.Verdict != Inconclusive || !strings.Contains(strings.Join(t2.Reasons, "\n"), "DCR3/DCR4 disagree") {
		t.Fatalf("T2 %s: %v", t2.Verdict, t2.Reasons)
	}
	if v, _ := t2.Values.(map[string]int); v["A_DCR3"] != 0x57 || v["A_DCR4"] != 0x8c || v["M"] != MValue(0x57) {
		t.Fatalf("values %v", t2.Values)
	}
	if t3.Verdict != Pass {
		t.Fatalf("T3 %s: %v", t3.Verdict, t3.Reasons)
	}
	// without the fault both registers agree: PASS (as TestRunnerSimulated)
	sim = NewSim(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	r = NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	t2, _ = r.RunHandback(context.Background())
	r.Close()
	if t2.Verdict != Pass {
		t.Fatalf("agreeing registers: %s %v", t2.Verdict, t2.Reasons)
	}
}

// T4/T5 (TESTSPEC 0.3 §5, §5.5): not implemented in validate; reasons per
// channel, N/A on pwm4.
func TestT4T5Result(t *testing.T) {
	amb := func(vs ...float64) []Sample {
		var out []Sample
		for _, v := range vs {
			out = append(out, Sample{ECAmb: v})
		}
		return out
	}
	r := T4T5Result("T4", 3, amb(22, 22.5, math.NaN()))
	if r.Verdict != Inconclusive || !strings.HasPrefix(r.Reasons[0], ReasonHDDBelowStep+" (max 22.5 C") {
		t.Fatalf("hdd below: %+v", r)
	}
	if r = T4T5Result("T5", 3, amb(22, 34)); r.Verdict != Inconclusive || r.Reasons[0] != ReasonT4T5Manual {
		t.Fatalf("hdd at the step: %+v", r)
	}
	if r = T4T5Result("T5", 3, amb(math.NaN())); r.Verdict != Inconclusive || !strings.HasPrefix(r.Reasons[0], ReasonT4T5Manual+" (EC 0x06 not recorded") {
		t.Fatalf("hdd no input: %+v", r)
	}
	for _, ch := range []int{1, 2} {
		if r = T4T5Result("T4", ch, amb(22)); r.Verdict != Inconclusive || r.Reasons[0] != ReasonT4T5Manual {
			t.Fatalf("pwm%d: %+v", ch, r)
		}
	}
	if r = T4T5Result("T4", 4, nil); r.Verdict != NotApplicable || r.Reasons[0] != "no EC automatic on DCR5 (firmware 0.14)" {
		t.Fatalf("pwm4: %+v", r)
	}
}

// Maintenance window 1, D1-b: an --out whose parents do not exist yet is
// created before the run (with the parents), and leaves no probe file;
// one that cannot be created is an error before anything happens.
func TestPrepareOut(t *testing.T) {
	base := t.TempDir()
	out := filepath.Join(base, "n5-mw1", "runs")
	if err := PrepareOut(out); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(out)
	if err != nil || len(ents) != 0 {
		t.Fatalf("out after the probe: %v %v", ents, err)
	}
	if err := PrepareOut(out); err != nil {
		t.Fatalf("existing directory: %v", err)
	}
	// a file where a parent directory should be
	blocker := filepath.Join(base, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareOut(filepath.Join(blocker, "runs")); err == nil || !strings.Contains(err.Error(), "output directory") {
		t.Fatalf("blocked parent: %v", err)
	}
	// Write itself also creates missing parents
	dir := filepath.Join(base, "late", "parent", "bundle")
	if err := (Bundle{Meta: map[string]any{}, Verdict: map[string]any{}}).Write(dir); err != nil {
		t.Fatalf("Write without parents: %v", err)
	}
}
