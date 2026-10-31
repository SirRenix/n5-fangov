package validate

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// TESTSPEC 0.4 §2.1 (#21): a DCR sample due before the settle time of the
// channel (pwm1-pwm3 10 s, pwm4 1 s) is recorded and listed, not judged.
func TestT1SettleTime(t *testing.T) {
	// pwm3 takes a write after 7 s: the 5-s sample shows the old value, is
	// not judged, PASS
	sim := NewSim(t0)
	sim.Latency[3] = 7 * time.Second
	r := NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	res := r.RunT1(context.Background())
	r.Close()
	if res.Verdict != Pass {
		t.Fatalf("latency 7 s: %s %v %+v", res.Verdict, res.Reasons, res.Criteria[0])
	}
	steps := res.Values.([]T1Step)
	if len(steps[1].NotJudged) != 2 || !strings.Contains(steps[1].NotJudged[0], "DCR3 255 at 5 s (settle time 10 s)") || steps[1].SettleS != 10 {
		t.Fatalf("not judged: %v", steps[1].NotJudged)
	}
	if len(steps[1].DCR) != 12 {
		t.Fatalf("samples per step %d", len(steps[1].DCR))
	}
	// 12 s: the sample at 10 s is judged and differs: FAIL
	sim = NewSim(t0)
	sim.Latency[3] = 12 * time.Second
	r = NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	if res := r.RunT1(context.Background()); res.Verdict != Fail {
		t.Fatalf("latency 12 s: %s %v", res.Verdict, res.Reasons)
	}
	r.Close()
	// pwm4: 1 s, every 5-s sample is judged
	sim = NewSim(t0)
	sim.Latency[4] = 200 * time.Millisecond
	r = NewRunner(sim, SpecConfig(4, 0, []int{1, 2, 3}))
	res = r.RunT1(context.Background())
	r.Close()
	if res.Verdict != Pass || len(res.Values.([]T1Step)[0].NotJudged) != 0 {
		t.Fatalf("pwm4: %s %v", res.Verdict, res.Reasons)
	}
	if SettleTime(1) != 10*time.Second || SettleTime(2) != 10*time.Second || SettleTime(3) != 10*time.Second || SettleTime(4) != time.Second {
		t.Fatal("settle times differ from TESTSPEC 0.4 §2.1")
	}
}

// TESTSPEC 0.4 §2 (#11): samples are due on a 5-s grid from the phase
// anchor; a read within +-0.5 s is judged at its due time (t_s), one off
// by more is a gap; an overrun skips the missed slots.
func TestSamplingGrid(t *testing.T) {
	sim := NewSim(t0)
	sim.SleepHook = func(d time.Duration) time.Duration { return d + 400*time.Millisecond }
	r := NewRunner(sim, SpecConfig(1, 1, []int{1, 2, 3}))
	res := r.RunT1(context.Background())
	r.Close()
	if res.Verdict != Pass || r.Gaps != 0 {
		t.Fatalf("jitter 0.4 s: %s gaps %d %v", res.Verdict, r.Gaps, res.Reasons)
	}
	for _, s := range r.Samples {
		if d := s.Time.Sub(t0).Seconds() - s.TS; math.Abs(d-0.4) > 1e-6 {
			t.Fatalf("t_s %.3f is not the due time (real %s)", s.TS, s.Time.Sub(t0))
		}
	}
	// 0.7 s late: every sample is off the grid, recorded, not judged
	sim = NewSim(t0)
	sim.SleepHook = func(d time.Duration) time.Duration { return d + 700*time.Millisecond }
	r = NewRunner(sim, SpecConfig(1, 1, []int{1, 2, 3}))
	res = r.RunT1(context.Background())
	r.Close()
	if res.Verdict != Inconclusive || r.Gaps == 0 || !strings.Contains(strings.Join(r.Events, "\n"), "read +0.70 s off") {
		t.Fatalf("jitter 0.7 s: %s gaps %d", res.Verdict, r.Gaps)
	}
	// one sleep overruns by 12 s: the slots in between are gaps
	sim = NewSim(t0)
	n := 0
	sim.SleepHook = func(d time.Duration) time.Duration {
		n++
		if n == 20 {
			return d + 12*time.Second
		}
		return d
	}
	r = NewRunner(sim, SpecConfig(1, 1, []int{1, 2, 3}))
	r.RunT1(context.Background())
	r.Close()
	ev := strings.Join(r.Events, "\n")
	if r.Gaps < 2 || !strings.Contains(ev, "skipped (the previous sample overran it") {
		t.Fatalf("overrun: gaps %d\n%s", r.Gaps, ev)
	}
}

// TESTSPEC 0.4 §5.1 (#24): another tach that follows is FAIL only without
// a table-input crossing; with the crossing of its own channel's input
// INCONCLUSIVE.
func TestT1TableInputCrossing(t *testing.T) {
	rest := map[int][]int{1: {2000, 2010}, 2: {2160, 2170}, 3: {1640, 1650}}
	rpm := map[int]int{255: 3540, 217: 3160, 179: 2725, 140: 2250}
	mk := func(follow bool, cpu, ssd float64) []T1Step {
		var out []T1Step
		for _, d := range T1Steps {
			st := T1Step{Duty: d, DCR: []int{d, d}, DCR4: []int{d, d}, Fans: map[int][]int{1: {2005, 2003}, 2: {2165, 2166}, 3: {rpm[d], rpm[d]}},
				Inputs: map[int][]float64{1: {cpu, cpu}, 2: {ssd, ssd}, 3: {22, 22}}}
			if follow {
				st.Fans[1] = []int{2300, 2310}
			}
			out = append(out, st)
		}
		return out
	}
	restIn := map[int][]float64{1: {40, 40}, 2: {50, 50}, 3: {22, 22}}
	if r := EvalT1Inputs(3, 3, rest, restIn, mk(true, 40, 50), []int{1, 2, 3}); r.Verdict != Fail {
		t.Fatalf("follows without crossing: %s %v", r.Verdict, r.Reasons)
	}
	r := EvalT1Inputs(3, 3, rest, restIn, mk(true, 48, 50), []int{1, 2, 3})
	if r.Verdict != Inconclusive || !strings.HasPrefix(r.Reasons[0], "table input of pwm1 crossed a step (pwm1 EC 0x09 48.0 C above step 45 C") {
		t.Fatalf("crossing of pwm1: %s %v", r.Verdict, r.Reasons)
	}
	// 46 C is beyond 45 C by less than 2 C: no crossing, FAIL
	if r := EvalT1Inputs(3, 3, rest, restIn, mk(true, 46, 50), []int{1, 2, 3}); r.Verdict != Fail {
		t.Fatalf("46 C: %s %v", r.Verdict, r.Reasons)
	}
	// fan1 follows, only the SSD input crossed: not FAIL
	r = EvalT1Inputs(3, 3, rest, restIn, mk(true, 40, 63), []int{1, 2, 3})
	if r.Verdict != Inconclusive || !strings.Contains(r.Reasons[0], "table input of pwm2 crossed a step") {
		t.Fatalf("other crossing: %s %v", r.Verdict, r.Reasons)
	}
	// a crossing without a following tach changes nothing
	if r := EvalT1Inputs(3, 3, rest, restIn, mk(false, 48, 50), []int{1, 2, 3}); r.Verdict != Pass {
		t.Fatalf("crossing, nobody follows: %s %v", r.Verdict, r.Reasons)
	}
}

// TESTSPEC 0.4 §5.2 (#17): the own table input crossing a step in the window
// of A makes every sub-run INCONCLUSIVE.
func TestHandbackTableInputCrossing(t *testing.T) {
	// EC 0x09 40 C for the first samples, then 60 C (load episode: crosses
	// 45 and 55 C)
	ecpu := make([]float64, 40)
	for i := range ecpu {
		ecpu[i] = 40
	}
	ecpu = append(ecpu, 60)
	env := &wobbleEnv{Sim: NewSim(t0), ecpu: ecpu}
	r := NewRunner(env, SpecConfig(1, 1, []int{1, 2, 3}))
	r.Sample("boot-state", -1)
	t2, t3 := r.RunHandback(context.Background())
	r.Close()
	if t2.Verdict != Inconclusive || t3.Verdict != Inconclusive || !strings.Contains(strings.Join(t2.Reasons, "\n"), "table input crossed a step (EC 0x09 in the window of A") {
		t.Fatalf("crossing: T2 %s T3 %s %v", t2.Verdict, t3.Verdict, t2.Reasons)
	}
	// steady input: PASS as before
	env = &wobbleEnv{Sim: NewSim(t0), ecpu: []float64{40}}
	r = NewRunner(env, SpecConfig(1, 1, []int{1, 2, 3}))
	t2, _ = r.RunHandback(context.Background())
	r.Close()
	if t2.Verdict != Pass {
		t.Fatalf("steady: %s %v", t2.Verdict, t2.Reasons)
	}
}

// TESTSPEC 0.4 §5.2 step 3 (#21): hold-phase samples after the settle time
// that differ from the written value are listed, the verdict stays.
func TestHandbackHoldDeviations(t *testing.T) {
	sim := NewSim(t0)
	sim.Latency[1] = 12 * time.Second
	r := NewRunner(sim, SpecConfig(1, 1, []int{1, 2, 3}))
	t2, _ := r.RunHandback(context.Background())
	r.Close()
	var hold *Criterion
	for i := range t2.Criteria {
		if strings.HasPrefix(t2.Criteria[i].Name, "hold phase") {
			hold = &t2.Criteria[i]
		}
	}
	if hold == nil || !strings.Contains(hold.Detail, "M: 10 s after the write") || hold.Met != nil {
		t.Fatalf("hold criterion %+v", t2.Criteria)
	}
	if t2.Verdict != Pass {
		t.Fatalf("T2 %s %v", t2.Verdict, t2.Reasons)
	}
}

// TESTSPEC 0.4 §4 (#13): after a temperature abort the channel of that
// sensor goes to 255 and stays; the others get their restore value. A run
// that never wrote writes nothing.
func TestTemperatureAbortRestore255(t *testing.T) {
	sim := NewSim(t0)
	r := NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	if err := r.manual(3); err != nil {
		t.Fatal(err)
	}
	sim.NVMe = LimitNVMeC + 1
	if res := r.RunT1(context.Background()); res.Verdict != Inconclusive || !strings.Contains(r.Abort, "NVMe") {
		t.Fatalf("abort %q", r.Abort)
	}
	r.Close()
	d, _ := sim.DCR()
	if d.Reg(2) != 255 || d.Reg(3) != 140 {
		t.Fatalf("after the NVMe abort DCR2 %d (want 255), DCR3 %d (want the stop 140)", d.Reg(2), d.Reg(3))
	}
	if !strings.Contains(r.RestoreOverrides[2], "255 (temperature abort: NVMe") || len(r.Unrestored()) != 0 {
		t.Fatalf("overrides %v unrestored %v", r.RestoreOverrides, r.Unrestored())
	}
	sim = NewSim(t0)
	sim.Tctl = LimitTctlC
	r = NewRunner(sim, SpecConfig(3, 3, []int{1, 2, 3}))
	r.Gates(context.Background())
	r.Close()
	if sim.Writes != 0 || len(r.RestoreOverrides) != 0 {
		t.Fatalf("a run without writes wrote %d times", sim.Writes)
	}
}

func TestCrossing(t *testing.T) {
	for _, c := range []struct {
		ref  float64
		vals []float64
		want bool
	}{
		{40, []float64{47}, true}, {40, []float64{46.9}, false}, {50, []float64{43}, true},
		{50, []float64{44}, false}, {math.NaN(), []float64{40, 47}, true}, {math.NaN(), nil, false},
	} {
		if got := Crossing(c.ref, c.vals, TableSteps(1)) != ""; got != c.want {
			t.Errorf("Crossing(%v, %v) = %v", c.ref, c.vals, got)
		}
	}
}

// TESTSPEC 0.4 #8: host, MAC, IP, mail, user, SSH values and serials are
// redacted; identity strings that contain the host name stay.
func TestRedactor(t *testing.T) {
	r := NewRedactor("fanbox-a.example.test")
	r.Protect("7.0.14-20-fanbox-a")
	r.Literal("alice", "[user]")
	r.Literal("SER12345", "[serial]")
	r.Literal("192.0.2.1 50000 192.0.2.2 22", "[ssh]")
	in := `host fanbox-a.example.test fanbox-a kernel 7.0.14-20-fanbox-a mac 02:00:00:00:00:33 ip 198.51.100.3 ip6 2001:db8::1 mail root@fanbox-a.example.test user alice /home/bob serial SER12345 ssh 192.0.2.1 50000 192.0.2.2 22 time 12:00:05 getty@tty1.service dcr 1800: 00 ff 00 55 tests alicebox`
	out := r.String(in)
	for _, bad := range []string{"fanbox-a.example", "02:00:00", "198.51.100.3", "2001:db8::1", "alice ", "/home/bob", "SER12345", "192.0.2.1"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q left in %s", bad, out)
		}
	}
	for _, keep := range []string{"7.0.14-20-fanbox-a", "12:00:05", "getty@tty1.service", "1800: 00 ff 00 55", "alicebox", "host n5host n5host kernel"} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q missing in %s", keep, out)
		}
	}
}
