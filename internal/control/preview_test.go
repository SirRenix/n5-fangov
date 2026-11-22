package control

import (
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/history"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// TestPreviewMatchesLoop: the preview of the running config over the
// recorded 1-min tier equals the targets the loop computed on the same
// per-minute readings (the loop runs at a 60 s interval here, so every
// minute is one cycle and the 1-min means are the readings). Targets are
// pre-slew, so the ramp is out of the comparison. The data walks through
// hysteresis, min_on, assist with its own hysteresis and hold, critical
// and the ceiling.
func TestPreviewMatchesLoop(t *testing.T) {
	h := newAssistHarness(t, previewCfg())
	targets, modes := map[int64]map[string]int{}, map[string]bool{}
	runLoop(h, 0, 180, nil, targets, modes)
	if !modes["critical"] || !modes["auto"] {
		t.Fatalf("data did not reach the rules: modes %v", modes)
	}
	res, err := h.c.Preview(h.c.Config(), PreviewSpanDay)
	if err != nil {
		t.Fatal(err)
	}
	if res.Span != "24h" || res.Step != 60 {
		t.Errorf("span %s step %d", res.Span, res.Step)
	}
	for _, name := range []string{"cpu", "ssd", "hdd"} {
		if pc := res.Channels[name]; pc.Error != "" || len(pc.Series) != 180 {
			t.Fatalf("%s: %q, %d points", name, pc.Error, len(pc.Series))
		}
	}
	if n, boosted := comparePreview(t, res, targets); n != 3*180 || boosted == 0 {
		t.Errorf("%d points compared, %d boosted", n, boosted)
	}
}

// previewCfg is the config of the preview-vs-loop tests: 60 s cycles, the
// hdd channel with hysteresis, min_on, a ceiling below critical and assist.
func previewCfg() config.Config {
	cfg := assistCfg(true, 2, 3*time.Minute)
	cfg.Daemon.Interval = time.Minute
	cfg.Daemon.StaleCycles = 600
	cfg.Channels[2].Hysteresis = 2
	cfg.Channels[2].MinOn = 3 * time.Minute
	cfg.Channels[2].Critical = 56
	cfg.Channels[2].Ceiling = 50 // below critical: the ceiling acts first
	cfg.Channels[0].MinOn = 2 * time.Minute
	return cfg
}

// runLoop runs minutes from..from+n-1 of the shared data through the loop
// of h (one 60-s cycle per minute) and records every channel's target by
// the snapshot's ts; hddFails(i) makes the hdd sensor unreadable in minute
// i (mode sensor-error, no temperature in the history).
func runLoop(h *harness, from, n int, hddFails func(int) bool, targets map[int64]map[string]int, modes map[string]bool) {
	cpu, nvme, hdd, gpu := h.sensors.get("k10temp"), h.sensors.get("nvme:max"), h.sensors.get("drivetemp:max"), h.sensors.get(gpuID)
	for i := from; i < from+n; i++ {
		x := float64(i)
		cpu.set(int(45000 + 20000*math.Sin(x/7) + 37*x))
		nvme.set(int(44000 + 5000*math.Sin(x/23)))
		gpu.set(int(42000 + 16000*math.Sin(x/11)))
		d := 38000 + 9000*math.Sin(x/17)
		if i >= 120 && i < 140 {
			d = 46000 + 600*x - 72000 // 46 → 57.4 °C: ceiling, then critical, then back
		}
		hdd.set(int(d))
		if hddFails != nil && hddFails(i) {
			hdd.fail(errors.New("drive gone"))
		}
		h.cycles(1)
		snap := h.c.Snapshot()
		m := map[string]int{}
		for _, cs := range snap.Channels {
			m[cs.Name] = cs.Target
			if modes != nil {
				modes[string(cs.Mode)] = true
			}
		}
		targets[snap.TS] = m
	}
}

// comparePreview checks every computed preview point against the loop's
// target at the same ts; points without a preview (no temperature) are
// skipped. It returns the points compared and those with a boost.
func comparePreview(t *testing.T, res PreviewResult, targets map[int64]map[string]int) (n, boosted int) {
	t.Helper()
	for _, name := range res.Order {
		diff := 0
		for _, p := range res.Channels[name].Series {
			if p.Preview == nil {
				continue
			}
			want, ok := targets[p.TS][name]
			if !ok {
				t.Fatalf("%s at %d: no loop target", name, p.TS)
			}
			n++
			if p.Boost > 0 {
				boosted++
			}
			if *p.Preview != want {
				diff++
				if diff <= 3 {
					t.Errorf("%s at %d: preview %d, loop %d", name, p.TS, *p.Preview, want)
				}
			}
		}
		if diff > 0 {
			t.Errorf("%s: %d points differ", name, diff)
		}
	}
	return n, boosted
}

// TestPreviewMatchesLoopErrorsAndGap: sensor errors (no temperature in
// the history: forget in both) and a restart with a 10-min gap (a fresh
// controller and a fresh preview channel) — the preview still equals the
// loop wherever it has a temperature.
func TestPreviewMatchesLoopErrorsAndGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	cfg := previewCfg()
	h := newHarness(t, cfg, func(o *Options) { o.HistoryFile = path })
	h.sensors.add(gpuID, 35000)
	targets := map[int64]map[string]int{}
	fails := func(i int) bool { return i >= 30 && i < 35 || i >= 125 && i < 128 }
	runLoop(h, 0, 90, fails, targets, nil)
	h.c.Stop() // saves the history; the restart below loads it
	h.clock.advance(10 * time.Minute)
	c2, err := New(cfg, h.dev, h.sensors.factory, h.alerts, Options{Logger: h.log, Now: h.clock.now, Notify: func() {}, Status: func(string) {}, SyncAlerts: true, HistoryFile: path})
	if err != nil {
		t.Fatal(err)
	}
	h.c = c2
	runLoop(h, 90, 90, fails, targets, nil)
	res, err := c2.Preview(c2.Config(), PreviewSpanDay)
	if err != nil {
		t.Fatal(err)
	}
	missing := 0
	for _, p := range res.Channels["hdd"].Series {
		if p.Preview == nil {
			missing++
		}
	}
	if missing != 8 || len(res.Channels["hdd"].Series) != 180 {
		t.Errorf("hdd: %d points without a preview (want the 8 failed minutes), %d points", missing, len(res.Channels["hdd"].Series))
	}
	if n, _ := comparePreview(t, res, targets); n != 3*180-8 {
		t.Errorf("%d points compared", n)
	}
}

// TestPreviewDraftMatchesLoop: the preview of a draft (and of a preset's
// channels) over the history of the running config equals a loop that ran
// the draft on the same readings.
func TestPreviewDraftMatchesLoop(t *testing.T) {
	run := previewCfg()
	h := newAssistHarness(t, run)
	runLoop(h, 0, 180, nil, map[int64]map[string]int{}, nil)

	draft := run.Clone()
	draft.Channels[2].Curve = [][2]int{{30, 90}, {40, 150}, {48, 255}}
	draft.Channels[2].Hysteresis, draft.Channels[2].MinOn = 3, 5*time.Minute
	draft.Channels[1].Critical = 47
	p, ok := config.BuiltinPresetByName("n5pro-quiet")
	if !ok {
		t.Fatal("no built-in preset")
	}
	pchans, _, err := config.ParseChannels(p.Raw)
	if err != nil || len(pchans) != 3 {
		t.Fatalf("preset: %v", err)
	}
	preset := run.Clone()
	preset.Channels = pchans // same names and pwms as the test channels
	for name, d := range map[string]config.Config{"draft": draft, "preset": preset} {
		t.Run(name, func(t *testing.T) {
			hd := newAssistHarness(t, d)
			targets := map[int64]map[string]int{}
			runLoop(hd, 0, 180, nil, targets, nil)
			res, err := h.c.Preview(d, PreviewSpanDay)
			if err != nil {
				t.Fatal(err)
			}
			if n, _ := comparePreview(t, res, targets); n != 3*180 {
				t.Errorf("%d points compared", n)
			}
		})
	}
}

// ---- rules on synthetic points ---------------------------------------------

const p0 int64 = 1_789_500_000

// pts builds 1-min points with temp[hdd] from temps (NaN = no reading,
// math.Inf = a gap: the minute is left out) and the GPU in extra.
func pts(temps []float64, gpu []float64) []history.Point {
	var out []history.Point
	for i, v := range temps {
		if math.IsInf(v, 0) {
			continue
		}
		p := history.Point{TS: p0 + int64(i)*60, Temp: map[string]float64{}, Duty: map[string]int{"hdd": 100}, RPM: map[string]int{}}
		if !math.IsNaN(v) {
			p.Temp["hdd"] = v
		}
		if gpu != nil && !math.IsNaN(gpu[i]) {
			p.Extra = map[string]float64{gpuID: gpu[i]}
		}
		out = append(out, p)
	}
	return out
}

func hddOnly(mod func(*config.Channel)) config.Config {
	cfg := config.Default()
	ch := config.Channel{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{36, 105}, {46, 255}}, Critical: 56, Stop: "140"}
	if mod != nil {
		mod(&ch)
	}
	cfg.Channels = []config.Channel{ch}
	return cfg
}

func previewOf(t *testing.T, cfg config.Config, p []history.Point, ceil int) PreviewChannel {
	t.Helper()
	in := PreviewInput{Config: cfg, Running: map[string]string{"hdd": "drivetemp:max"}, Points: p, Span: PreviewSpanDay}
	if ceil > 0 {
		in.Ceiling = func(config.Channel) (int, []sensor.PartCeiling) { return ceil, nil }
	}
	return ComputePreview(in).Channels["hdd"]
}

func previews(t *testing.T, pc PreviewChannel) []int {
	t.Helper()
	if pc.Error != "" {
		t.Fatalf("error %q", pc.Error)
	}
	var out []int
	for _, p := range pc.Series {
		if p.Preview == nil {
			out = append(out, -1)
			continue
		}
		out = append(out, *p.Preview)
	}
	return out
}

func wantSeq(t *testing.T, what string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: %v, want %v", what, got, want)
			return
		}
	}
}

func TestPreviewRules(t *testing.T) {
	// curve [[36,105],[46,255]]: 15 duty per degree
	nan, gap := math.NaN(), math.Inf(1)
	t.Run("hysteresis", func(t *testing.T) {
		cfg := hddOnly(func(c *config.Channel) { c.Hysteresis = 2 })
		wantSeq(t, "held", previews(t, previewOf(t, cfg, pts([]float64{40, 41, 42, 41}, nil), 0)), []int{165, 165, 195, 195})
	})
	t.Run("min_on", func(t *testing.T) {
		cfg := hddOnly(func(c *config.Channel) { c.MinOn = 2 * time.Minute })
		wantSeq(t, "hold", previews(t, previewOf(t, cfg, pts([]float64{40, 42, 40, 40, 40}, nil), 0)), []int{165, 195, 195, 165, 165})
	})
	t.Run("assist on and off", func(t *testing.T) {
		cfg := hddOnly(nil)
		cfg.Assists = []config.Assist{{Channel: "hdd", Enabled: true, Sources: []config.AssistSource{{Sensor: gpuID, Start: 50, Full: 58, Boost: 45}}}}
		temps, g := []float64{36, 36, 36}, []float64{40, 54, 60}
		pc := previewOf(t, cfg, pts(temps, g), 0)
		wantSeq(t, "on", previews(t, pc), []int{105, 128, 150})
		if pc.Series[2].Boost != 45 || pc.Summary.Preview.AssistMin != 2 {
			t.Errorf("boost %d, assist minutes %d", pc.Series[2].Boost, pc.Summary.Preview.AssistMin)
		}
		cfg.Assists[0].Enabled = false
		wantSeq(t, "off", previews(t, previewOf(t, cfg, pts(temps, g), 0)), []int{105, 105, 105})
	})
	t.Run("critical", func(t *testing.T) {
		wantSeq(t, "critical", previews(t, previewOf(t, hddOnly(nil), pts([]float64{40, 56, 40}, nil), 0)), []int{165, 255, 165})
	})
	t.Run("ceiling", func(t *testing.T) {
		cfg := hddOnly(func(c *config.Channel) { c.Critical = 70 })
		// 255 at 50, held until 3 °C below (47 is not below 47), then the curve
		wantSeq(t, "ceiling", previews(t, previewOf(t, cfg, pts([]float64{40, 50, 48, 47, 44}, nil), 50)), []int{165, 255, 255, 255, 225})
	})
	t.Run("gap", func(t *testing.T) {
		cfg := hddOnly(func(c *config.Channel) { c.Hysteresis = 5 })
		// without a gap 42 stays held at 40; after a gap (restart) it is taken as is
		wantSeq(t, "no gap", previews(t, previewOf(t, cfg, pts([]float64{40, 42}, nil), 0)), []int{165, 165})
		wantSeq(t, "gap", previews(t, previewOf(t, cfg, pts([]float64{40, gap, gap, 42}, nil), 0)), []int{165, 195})
		// a point without a reading: no preview there, the held value restarts
		wantSeq(t, "missing reading", previews(t, previewOf(t, cfg, pts([]float64{40, nan, 42}, nil), 0)), []int{165, -1, 195})
	})
	t.Run("sensor not in history", func(t *testing.T) {
		cfg := hddOnly(func(c *config.Channel) { c.Sensor = "disk:sdz" })
		pc := previewOf(t, cfg, pts([]float64{40, 41}, nil), 0)
		if pc.Error != "sensor not in history" || pc.Summary != nil {
			t.Errorf("%+v", pc)
		}
		// another sensor that is recorded in extra is computed from there
		cfg = hddOnly(func(c *config.Channel) { c.Sensor = gpuID })
		wantSeq(t, "extra", previews(t, previewOf(t, cfg, pts([]float64{40, 41}, []float64{40, 46}), 0)), []int{165, 255})
		if pc := ComputePreview(PreviewInput{Config: hddOnly(nil), Span: PreviewSpanDay}).Channels["hdd"]; pc.Error != "no history yet" {
			t.Errorf("empty history: %q", pc.Error)
		}
	})
}

func TestPreviewSummaryAndWeek(t *testing.T) {
	cfg := hddOnly(nil)
	p := pts([]float64{36, 40, 44, 46, 46}, nil) // 105 165 225 255 255
	p[0].Duty["hdd"], p[1].Duty["hdd"] = 210, -1 // a write failure (-1) is no value
	p[2].Assist = map[string]int{"hdd": 20}
	pc := previewOf(t, cfg, p, 0)
	s := pc.Summary
	if s.Preview.Mean != 201 || s.Preview.P95 != 255 || s.Preview.Max != 255 || s.Preview.Min200 != 3 || s.Preview.AssistMin != 0 {
		t.Errorf("preview line %+v", s.Preview)
	}
	if s.Actual.Mean != 127.5 || s.Actual.Max != 210 || s.Actual.Min200 != 1 || s.Actual.AssistMin != 1 || pc.Series[1].Actual != nil {
		t.Errorf("actual line %+v", s.Actual)
	}
	res := ComputePreview(PreviewInput{Config: cfg, Running: map[string]string{"hdd": "drivetemp:max"}, Points: p, Span: PreviewSpanWeek})
	if res.Span != "7d" || res.Step != 300 || res.Channels["hdd"].Summary.Preview.Min200 != 15 {
		t.Errorf("week: %s %d %+v", res.Span, res.Step, res.Channels["hdd"].Summary)
	}
}

// TestPreviewMeasuredNote: an HDD channel with assist on carries the
// measured line, labelled as measured; without assist or for another
// channel kind it does not.
func TestPreviewMeasuredNote(t *testing.T) {
	cfg := hddOnly(nil)
	cfg.Assists = []config.Assist{{Channel: "hdd", Enabled: true, Sources: []config.AssistSource{{Sensor: gpuID, Start: 50, Full: 58, Boost: 45}}}}
	in := PreviewInput{Config: cfg, Running: map[string]string{"hdd": "drivetemp:max"}, Points: pts([]float64{40}, []float64{55}), Span: PreviewSpanDay,
		HDD: func(c config.Channel) bool { return c.Sensor == "drivetemp:max" }}
	if m := ComputePreview(in).Channels["hdd"].Measured; m != PreviewMeasured || m[:len("Measured, not predicted")] != "Measured, not predicted" {
		t.Errorf("measured %q", m)
	}
	in.Config.Assists[0].Enabled = false
	if m := ComputePreview(in).Channels["hdd"].Measured; m != "" {
		t.Errorf("assist off: %q", m)
	}
}

// TestPreviewNotes: an enabled assist source that the history lacks is
// reported (never recorded, or missing at some points with the time it was
// first recorded); a composite of parts with different ceilings is noted
// as judged on its recorded maximum.
func TestPreviewNotes(t *testing.T) {
	cfg := hddOnly(nil)
	cfg.Assists = []config.Assist{{Channel: "hdd", Enabled: true, Sources: []config.AssistSource{{Sensor: gpuID, Start: 50, Full: 58, Boost: 45}, {Sensor: "hwmon:gone:temp1", Start: 50, Full: 58, Boost: 45}}}}
	nan := math.NaN()
	pc := previewOf(t, cfg, pts([]float64{40, 40, 40, 40}, []float64{nan, nan, 55, 56}), 0)
	if len(pc.Notes) != 2 || !strings.Contains(pc.Notes[0], "assist source "+gpuID+" missing at 2 of 4 points") || !strings.Contains(pc.Notes[0], "first recorded") ||
		pc.Notes[1] != "assist source hwmon:gone:temp1 not in history: it adds 0" {
		t.Errorf("notes %q", pc.Notes)
	}
	cfg.Assists[0].Enabled = false // off: the sources do not matter
	if pc := previewOf(t, cfg, pts([]float64{40}, nil), 0); len(pc.Notes) != 0 {
		t.Errorf("assist off: %q", pc.Notes)
	}
	mixed := hddOnly(func(c *config.Channel) { c.Sensor = "nvme:max,drivetemp:max" })
	in := PreviewInput{Config: mixed, Running: map[string]string{"hdd": mixed.Channels[0].Sensor}, Points: pts([]float64{40}, nil), Span: PreviewSpanDay,
		Ceiling: func(c config.Channel) (int, []sensor.PartCeiling) {
			return 65, sensor.PartCeilings(c.Sensor, 0, nil)
		}}
	if n := ComputePreview(in).Channels["hdd"].Notes; len(n) != 1 || !strings.Contains(n[0], "lowest part ceiling 65") {
		t.Errorf("mixed composite: %q", n)
	}
	in.Config = hddOnly(func(c *config.Channel) { c.Sensor = "drivetemp:max,disk:sda" }) // two HDD parts: one ceiling, no note
	in.Running = map[string]string{"hdd": in.Config.Channels[0].Sensor}
	in.Ceiling = func(c config.Channel) (int, []sensor.PartCeiling) {
		return 65, []sensor.PartCeiling{{ID: "drivetemp:max", Ceiling: 65}, {ID: "disk:sda", Ceiling: 65}}
	}
	if n := ComputePreview(in).Channels["hdd"].Notes; len(n) != 0 {
		t.Errorf("same-kind composite: %q", n)
	}
}

func TestPreviewOneAtATime(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.c.previewing.Store(true)
	if _, err := h.c.Preview(h.c.Config(), PreviewSpanDay); !errors.Is(err, ErrPreviewBusy) {
		t.Errorf("concurrent: %v", err)
	}
	h.c.previewing.Store(false)
	if _, err := h.c.Preview(h.c.Config(), PreviewSpanDay); err != nil {
		t.Errorf("after: %v", err)
	}
}

// TestPreviewReplay16Nov: the 10 s history of the measurement of 16.11.
// (testdata of the assist work), folded into the 1-min tier by the history
// store, through the preview with the design's HDD assist values: ≈ 150
// under load, 99 at idle (t < 2770 s, before the GPU warm-up; the same
// window as TestAssistReplay16Nov).
func TestPreviewReplay16Nov(t *testing.T) {
	rows := loadReplay(t, "testdata/assist-replay-20261116.csv")
	clock := &fakeClock{t: time.Unix(p0, 0)}
	st := history.New("", 10*time.Second, clock.now, nil)
	for _, r := range rows {
		st.Push(history.Point{TS: p0 + int64(r.t), Temp: map[string]float64{"hdd": float64(r.drive) / 1000}, Duty: map[string]int{"hdd": r.duty}, RPM: map[string]int{},
			Extra: map[string]float64{gpuID: float64(r.gpu) / 1000, "nvme:max": float64(r.nvme) / 1000}})
	}
	clock.advance(time.Duration(rows[len(rows)-1].t+10) * time.Second)
	cfg := hddOnly(func(c *config.Channel) {
		c.Curve = [][2]int{{40, 99}, {41, 106}, {42, 106}, {55, 255}}
		c.Critical = 60
	})
	cfg.Assists = []config.Assist{{Channel: "hdd", Enabled: true, Hysteresis: 2, MinOn: 2 * time.Minute, Sources: []config.AssistSource{
		{Sensor: gpuID, Start: 50, Full: 58, Boost: 45}, {Sensor: "nvme:max", Start: 46, Full: 50, Boost: 45}}}}
	pc := previewOf(t, cfg, st.Range(PreviewSpanDay, 0), 0)
	if pc.Error != "" {
		t.Fatal(pc.Error)
	}
	type win struct {
		name     string
		from, to int64
		n        int
		sum      float64
		max      int
	}
	wins := []*win{{name: "idle", from: 0, to: 2770}, {name: "A0", from: 3460, to: 4061}, {name: "B", from: 4661, to: 5262}, {name: "A1", from: 5862, to: 6462}}
	for _, p := range pc.Series {
		if p.Preview == nil {
			continue
		}
		off := p.TS - p0
		for _, w := range wins {
			// a 1-min bucket counts when it lies inside the window
			if off >= w.from && off+60 <= w.to {
				w.n++
				w.sum += float64(*p.Preview)
				w.max = max(w.max, *p.Preview)
			}
		}
	}
	for _, w := range wins {
		if w.n == 0 {
			t.Fatalf("%s empty", w.name)
		}
		t.Logf("preview replay %-4s n=%2d mean %.1f max %d", w.name, w.n, w.sum/float64(w.n), w.max)
	}
	if w := wins[0]; w.max != 99 || w.sum/float64(w.n) != 99 {
		t.Errorf("idle: mean %.1f max %d, want 99", w.sum/float64(w.n), w.max)
	}
	for _, w := range wins[1:] {
		if m := w.sum / float64(w.n); m < 145 || m > 155 {
			t.Errorf("%s: mean %.1f, want ≈ 150", w.name, m)
		}
	}
}
