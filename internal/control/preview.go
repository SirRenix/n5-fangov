package control

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/history"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// Curve preview (DESIGN "Preview"): how would a channel have regulated over
// the recorded history with a draft config? The preview runs the loop's
// own rules — regulate (curve at the hysteresis-held temperature, min_on,
// assist, critical) and ceilingStep — on a channel copy without hardware,
// one history point after the other. It knows no feedback (more air would
// have lowered the temperatures, so it rather shows too much duty), no
// ramp (the target is shown, not the slewed duty) and nothing of the past
// modes (overrides, failsafe, sensor errors are in the actual line only).
//
// The input is the history tier, not the cycles: in production a 1-min
// point is the mean of six 10-s cycles (a 5-min point of thirty), so the
// preview applies hysteresis and min_on to means — short peaks are
// smoothed, a hold may start or end a cycle earlier or later than in the
// loop. The equality tests run the loop at a 60-s interval, where one
// cycle is one point, for an exact comparison.

// Preview spans: the history tier they read and its step.
const (
	PreviewSpanDay  = 24 * time.Hour
	PreviewSpanWeek = 7 * 24 * time.Hour
)

// PreviewNote is the note every preview carries; PreviewNoteWeek is added
// for the 7 d span.
const (
	PreviewNote     = "Target duty from the recorded temperatures with the rules of the daemon; no feedback: more air would have lowered the temperatures, so the preview shows rather too much duty, never too little. The ramp (step_up/step_down) is not modelled. Past overrides, failsafe and sensor errors are in the actual line only."
	PreviewNoteWeek = " 7 d works on 5-min means: hysteresis and min_on barely act, short peaks vanish."
	// PreviewMeasured is shown for an HDD channel with assist on in the
	// preview: a measurement, not a prediction (there is no heat model).
	PreviewMeasured = "Measured, not predicted: HDD fan 106 → 150 lowered the NVMe by 2.3–2.7 K and the drives by 0.5–0.9 K after 8–15 min (reference N5 Pro, 2026-11-16)."
)

// ErrPreviewBusy is returned while another preview runs (one at a time).
var ErrPreviewBusy = errors.New("a preview is already being computed")

// PreviewPoint is one point of a channel's series: the recorded duty
// (actual, nil when unknown), the preview target (nil where the history
// has no temperature for it) and the assist share of the preview.
type PreviewPoint struct {
	TS      int64 `json:"ts"`
	Actual  *int  `json:"actual"`
	Preview *int  `json:"preview"`
	Boost   int   `json:"boost"`
}

// PreviewLine summarises one line: mean, P95 (nearest rank), maximum,
// minutes at or above 200 and minutes with an assist boost (actual: the
// recorded boost).
type PreviewLine struct {
	Mean      float64 `json:"mean"`
	P95       int     `json:"p95"`
	Max       int     `json:"max"`
	Min200    int     `json:"min_ge_200"`
	AssistMin int     `json:"assist_min"`
}

// PreviewSummary holds both lines.
type PreviewSummary struct {
	Actual  PreviewLine `json:"actual"`
	Preview PreviewLine `json:"preview"`
}

// PreviewChannel is one channel of the result. Error explains a channel
// that could not be computed ("sensor not in history"); Measured carries
// PreviewMeasured for an HDD channel with assist on.
type PreviewChannel struct {
	Series   []PreviewPoint  `json:"series"`
	Summary  *PreviewSummary `json:"summary,omitempty"`
	Error    string          `json:"error,omitempty"`
	Measured string          `json:"measured,omitempty"`
	// Notes are limits of this channel's computation the operator should
	// see: an assist source missing from the history (it adds 0 there), a
	// composite sensor whose ceiling is judged on the recorded maximum.
	Notes []string `json:"notes,omitempty"`
}

// PreviewResult is the answer of POST /api/preview.
type PreviewResult struct {
	Span     string                    `json:"span"` // "24h" | "7d"
	Step     int                       `json:"step"` // seconds between points
	Note     string                    `json:"note"`
	Order    []string                  `json:"order"` // channel order of the config
	Channels map[string]PreviewChannel `json:"channels"`
}

// PreviewInput is what ComputePreview needs; everything is a copy.
type PreviewInput struct {
	Config  config.Config     // the draft: channels and [[assist]] tables to compute
	Running map[string]string // running channel name → its sensor id (recorded as temp[name])
	Points  []history.Point   // the tier of the span, oldest first
	Span    time.Duration     // PreviewSpanDay or PreviewSpanWeek
	// Ceiling yields the effective ceilings of a channel (the loop's
	// ceilingOf); HDD reports an HDD channel (for PreviewMeasured).
	Ceiling func(config.Channel) (int, []sensor.PartCeiling)
	HDD     func(config.Channel) bool
}

// previewStep is the tier step of a span in seconds.
func previewStep(span time.Duration) int {
	if span > history.Min1Span {
		return int(history.Min5Bucket / time.Second)
	}
	return int(history.Min1Bucket / time.Second)
}

// ComputePreview computes every channel of in.Config over in.Points. Pure.
func ComputePreview(in PreviewInput) PreviewResult {
	step := previewStep(in.Span)
	res := PreviewResult{Span: "24h", Step: step, Note: PreviewNote, Channels: map[string]PreviewChannel{}, Order: []string{}}
	if in.Span > history.Min1Span {
		res.Span = "7d"
		res.Note += PreviewNoteWeek
	}
	byID := map[string]string{} // sensor id → running channel recording it as temp[name]
	for name, id := range in.Running {
		byID[id] = name
	}
	for _, cc := range in.Config.Channels {
		res.Order = append(res.Order, cc.Name)
		pc := previewChannel(cc, in.Config.Assist(cc.Name), in, byID, step)
		if a := in.Config.Assist(cc.Name); a != nil && a.Enabled && pc.Error == "" && in.HDD != nil && in.HDD(cc) {
			pc.Measured = PreviewMeasured
		}
		res.Channels[cc.Name] = pc
	}
	return res
}

// reading returns a recorded temperature in millidegrees: a running
// channel's sensor from temp[<channel>], any other id from extra[<id>].
func reading(p history.Point, id string, byID map[string]string) (int, bool) {
	var v float64
	var ok bool
	if name, isChan := byID[id]; isChan {
		v, ok = p.Temp[name]
	} else {
		v, ok = p.Extra[id]
	}
	if !ok {
		return 0, false
	}
	return int(math.Round(v * 1000)), true
}

func previewChannel(cc config.Channel, ac *config.Assist, in PreviewInput, byID map[string]string, step int) PreviewChannel {
	fresh := func() *channel {
		ch := &channel{cfg: cc, mode: ModeAuto, cur: -1, rpm: -1}
		if in.Ceiling != nil {
			ch.ceiling, ch.partCeil = in.Ceiling(cc)
		} else {
			ch.ceiling = sensor.CeilingDefault
		}
		if ac != nil {
			ch.assist = newAssistRT(*ac)
		}
		return ch
	}
	out := PreviewChannel{Series: make([]PreviewPoint, 0, len(in.Points))}
	ch := fresh()
	gap := int64(step * 3 / 2)
	var prev int64
	seen := false
	var act, pre []int
	actAssist, preAssist := 0, 0
	srcs := map[string]*srcSeen{}
	if ac != nil {
		for _, s := range ac.Sources {
			srcs[s.Sensor] = &srcSeen{}
		}
	}
	for _, p := range in.Points {
		if prev != 0 && p.TS-prev > gap {
			// a gap (restart, outage): the loop starts afresh, so does the preview
			ch = fresh()
		}
		prev = p.TS
		pt := PreviewPoint{TS: p.TS}
		if d, ok := p.Duty[cc.Name]; ok && d >= 0 {
			v := d
			pt.Actual = &v
			act = append(act, d)
			if p.Assist[cc.Name] > 0 {
				actAssist++
			}
		}
		if a := ch.assist; a != nil {
			for _, s := range a.srcs {
				v, ok := reading(p, s.cfg.Sensor, byID)
				ok = ok && sensor.Plausible(v)
				s.observe(v, ok, a.cfg.Hysteresis)
				switch seen := srcs[s.cfg.Sensor]; {
				case !ok:
					seen.missing++
				case seen.first == 0:
					seen.first = p.TS
				}
			}
			a.maxOfSources()
		}
		t, ok := reading(p, cc.Sensor, byID)
		if !ok || !sensor.Plausible(t) {
			// no temperature at this point: like a sensor error in the loop
			ch.forget()
			out.Series = append(out.Series, pt)
			continue
		}
		seen = true
		ch.temp, ch.tempOK = t, true
		ch.parts = []sensor.Part{{ID: cc.Sensor, Value: t}}
		regulate(ch, time.Unix(p.TS, 0), 0, false)
		ch.ceilingStep()
		v := ch.target
		pt.Preview = &v
		pt.Boost, _ = ch.assistBoost()
		if pt.Boost > 0 {
			preAssist++
		}
		pre = append(pre, v)
		out.Series = append(out.Series, pt)
	}
	out.Notes = previewNotes(cc, ac, srcs, in)
	switch {
	case len(in.Points) == 0:
		out.Error = "no history yet"
		out.Notes = nil
		return out
	case !seen:
		out.Error = "sensor not in history"
		return out
	}
	mins := step / 60
	out.Summary = &PreviewSummary{Actual: summarise(act, actAssist, mins), Preview: summarise(pre, preAssist, mins)}
	return out
}

// srcSeen tracks one assist source over the points: the first point it is
// recorded at (0 = never) and how many points it is missing at.
type srcSeen struct {
	first   int64
	missing int
}

// previewNotes lists the limits of one channel's computation:
//   - an assist source of an enabled table that the history lacks at some
//     points adds 0 there — never recorded, or missing at N points with the
//     time it was first recorded (assist sources are recorded only since
//     the table exists);
//   - a composite sensor with parts of different ceilings (an NVMe and an
//     HDD): the history keeps only the maximum of the parts, so the ceiling
//     is judged on it against the lowest part ceiling and the preview may
//     show 255 where the loop, which judges every part on its own, would not.
func previewNotes(cc config.Channel, ac *config.Assist, srcs map[string]*srcSeen, in PreviewInput) []string {
	var notes []string
	if ac != nil && ac.Enabled {
		for _, s := range ac.Sources {
			st := srcs[s.Sensor]
			switch {
			case st == nil || st.missing == 0:
			case st.first == 0:
				notes = append(notes, fmt.Sprintf("assist source %s not in history: it adds 0", s.Sensor))
			default:
				notes = append(notes, fmt.Sprintf("assist source %s missing at %d of %d points (adds 0 there); first recorded %s",
					s.Sensor, st.missing, len(in.Points), time.Unix(st.first, 0).Format("2006-01-02 15:04")))
			}
		}
	}
	if in.Ceiling != nil && len(cc.Sensors()) > 1 {
		low, parts := in.Ceiling(cc)
		for _, p := range parts {
			if p.Ceiling != low {
				notes = append(notes, fmt.Sprintf("ceiling judged on the recorded maximum of %s against its lowest part ceiling %d °C (the history keeps no per-part readings): the preview may show 255 where the daemon would not", cc.Sensor, low))
				break
			}
		}
	}
	return notes
}

// summarise builds a PreviewLine from the values of one line; every value
// stands for mins minutes.
func summarise(vals []int, assistPoints, mins int) PreviewLine {
	l := PreviewLine{AssistMin: assistPoints * mins}
	if len(vals) == 0 {
		return l
	}
	sum := 0
	for _, v := range vals {
		sum += v
		if v >= 200 {
			l.Min200 += mins
		}
	}
	s := slices.Clone(vals)
	slices.Sort(s)
	l.Mean = math.Round(float64(sum)/float64(len(vals))*10) / 10
	l.Max = s[len(s)-1]
	l.P95 = s[int(math.Ceil(0.95*float64(len(s))))-1]
	return l
}

// Preview computes the preview of draft (the running config with the
// operator's changes) over the recorded history of span. It reads the
// running config and the history store under their own locks and touches
// no loop state; the ceilings come from the loop's ceilingOf. One at a
// time: a second call while one runs gets ErrPreviewBusy.
func (c *Controller) Preview(draft config.Config, span time.Duration) (PreviewResult, error) {
	if !c.previewing.CompareAndSwap(false, true) {
		return PreviewResult{}, ErrPreviewBusy
	}
	defer c.previewing.Store(false)
	running := map[string]string{}
	for _, cc := range c.Config().Channels {
		running[cc.Name] = cc.Sensor
	}
	if span != PreviewSpanWeek {
		span = PreviewSpanDay
	}
	return ComputePreview(PreviewInput{
		Config:  draft,
		Running: running,
		Points:  c.hist.Range(span, 0),
		Span:    span,
		Ceiling: c.ceilingOf,
		HDD: func(cc config.Channel) bool {
			return sensor.BuiltinCeiling(cc.Sensor, c.opts.DiskKind) == sensor.CeilingHDD
		},
	}), nil
}
