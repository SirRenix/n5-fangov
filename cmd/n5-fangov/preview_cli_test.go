package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/web"
)

type fakePreviewCtrl struct {
	cfg   config.Config
	draft config.Config
	span  time.Duration
}

func (f *fakePreviewCtrl) Config() config.Config { return f.cfg.Clone() }
func (f *fakePreviewCtrl) Preview(d config.Config, span time.Duration) (control.PreviewResult, error) {
	f.draft, f.span = d, span
	return control.PreviewResult{Span: "24h"}, nil
}

func TestPreviewer(t *testing.T) {
	cfg, _, err := config.Parse([]byte(assistConfig))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Channels[1].Ceiling = 50
	ctrl := &fakePreviewCtrl{cfg: cfg}
	p := previewer{ctrl: ctrl, presets: dirPresetStore{dir: filepath.Join(t.TempDir(), "presets"), profile: "n5pro"}}
	// preset: merged by pwm like an apply (running names, ceiling kept)
	if _, err := p.Preview(web.PreviewRequest{Preset: "n5pro-quiet", Span: control.PreviewSpanWeek}); err != nil {
		t.Fatal(err)
	}
	if d := ctrl.draft.Channel("disks"); d == nil || d.Critical != 66 || d.Ceiling != 50 || len(ctrl.draft.Channels) != 2 || ctrl.span != control.PreviewSpanWeek {
		t.Errorf("preset draft: %+v (%d channels)", d, len(ctrl.draft.Channels))
	}
	if ctrl.cfg.Channel("disks").Critical != 56 {
		t.Error("the running config was changed")
	}
	// a draft channel replaces its running one, the ceiling stays; assist switched
	draft := config.Channel{Name: "disks", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{30, 90}, {50, 255}}, Critical: 58, Stop: "140"}
	if _, err := p.Preview(web.PreviewRequest{Channels: []config.Channel{draft}, Assist: map[string]bool{"disks": true}}); err != nil {
		t.Fatal(err)
	}
	if d := ctrl.draft.Channel("disks"); d.Critical != 58 || d.Ceiling != 50 || !ctrl.draft.Assist("disks").Enabled || ctrl.cfg.Assist("disks").Enabled {
		t.Errorf("draft: %+v, assist %+v", d, ctrl.draft.Assists)
	}
	if _, err := p.Preview(web.PreviewRequest{Assist: map[string]bool{"cpu": true}}); !errors.Is(err, web.ErrBadInput) {
		t.Errorf("assist without a table: %v", err)
	}
	if _, err := p.Preview(web.PreviewRequest{Preset: "nope"}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("unknown preset: %v", err)
	}
}

// previewAnswer is the canned answer of the fake daemon.
var previewAnswer = control.PreviewResult{Span: "24h", Step: 60, Note: "no feedback", Order: []string{"hdd", "ssd"},
	Channels: map[string]control.PreviewChannel{
		"hdd": {Summary: &control.PreviewSummary{
			Actual:  control.PreviewLine{Mean: 99.4, P95: 106, Max: 120},
			Preview: control.PreviewLine{Mean: 118, P95: 150, Max: 160, Min200: 12, AssistMin: 184}}, Measured: control.PreviewMeasured,
			Notes: []string{"assist source nvme:max not in history: it adds 0"}},
		"ssd": {Error: "sensor not in history"},
	}}

func TestCLIPreview(t *testing.T) {
	d := startFakeDaemon(t)
	var mu sync.Mutex
	var bodies []map[string]any
	d.stop()
	d.extra = func(mux *http.ServeMux) {
		mux.HandleFunc("POST /api/preview", func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			bodies = append(bodies, b)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(previewAnswer)
		})
	}
	d.start(t)
	out, errOut, code := captureOutput(t, func() int { return cmdPreview(nil) })
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"channel  actual Ø/P95/max   preview Ø/P95/max   min>=200   assist min",
		"hdd        99 / 106 / 120    118 / 150 / 160      0 → 12      0 → 184",
		"ssd      no preview: sensor not in history",
		"no feedback", "hdd: Measured, not predicted",
		"         note: assist source nvme:max not in history: it adds 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	_, _, code = captureOutput(t, func() int {
		return cmdPreview([]string{"--preset", "n5pro-cool", "--assist", "hdd=on", "--assist", "ssd=off", "--span", "7d"})
	})
	mu.Lock()
	last := bodies[len(bodies)-1]
	mu.Unlock()
	as, _ := last["assist"].(map[string]any)
	if code != exitOK || last["preset"] != "n5pro-cool" || last["span"] != "7d" || as["hdd"] != true || as["ssd"] != false {
		t.Errorf("request %v (exit %d)", last, code)
	}
	out, _, code = captureOutput(t, func() int { return cmdPreview([]string{"--json"}) })
	var back control.PreviewResult
	if code != exitOK || json.Unmarshal([]byte(out), &back) != nil || back.Channels["ssd"].Error != "sensor not in history" {
		t.Errorf("--json: %d %s", code, out)
	}
	for _, args := range [][]string{{"--span", "1h"}, {"--assist", "hdd"}, {"--assist", "hdd=yes"}, {"--preset", "../x"}, {"extra"}, {"--bogus"}} {
		if _, _, code := captureOutput(t, func() int { return cmdPreview(args) }); code != exitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
	d.stop()
	_, errOut, code = captureOutput(t, func() int { return cmdPreview(nil) })
	if code != exitFail || !strings.Contains(errOut, "does not answer") || !strings.Contains(errOut, "no files are read") {
		t.Errorf("no daemon: %d %q", code, errOut)
	}
}
