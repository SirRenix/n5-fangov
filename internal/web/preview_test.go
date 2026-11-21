package web

import (
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
)

// fakePreviewer records the requests and answers with err when set.
type fakePreviewer struct {
	mu   sync.Mutex
	reqs []PreviewRequest
	err  error
}

func (f *fakePreviewer) RunningChannels() []config.Channel {
	return []config.Channel{
		{Name: "cpu", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 88, Stop: "auto"},
		{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{36, 105}, {46, 255}}, Critical: 56, Stop: "140"},
	}
}

func (f *fakePreviewer) Preview(req PreviewRequest) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return nil, f.err
	}
	return control.PreviewResult{Span: "24h", Step: 60, Order: []string{"hdd"}, Channels: map[string]control.PreviewChannel{"hdd": {Series: []control.PreviewPoint{}}}}, nil
}

func (f *fakePreviewer) last() PreviewRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func previewEnv(t *testing.T, auth AuthConfig) (*env, *fakePreviewer) {
	t.Helper()
	e := newEnv(t, auth)
	p := &fakePreviewer{}
	e.withDeps(t, auth, func(d *Deps) { d.Preview = p })
	return e, p
}

func TestPostPreview(t *testing.T) {
	e, p := previewEnv(t, AuthConfig{})
	// no body: the running config over 24 h
	r := e.do(t, "POST", "/api/preview", "", csrf)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"span":"24h"`) {
		t.Errorf("body %s", r.body)
	}
	if q := p.last(); q.Span != control.PreviewSpanDay || q.Channels != nil || q.Preset != "" || q.Assist != nil {
		t.Errorf("empty request: %+v", q)
	}
	wantCode(t, e.do(t, "POST", "/api/preview", `{"span":"7d","assist":{"hdd":true},"preset":"n5pro-cool"}`, csrf), 200)
	if q := p.last(); q.Span != control.PreviewSpanWeek || !q.Assist["hdd"] || q.Preset != "n5pro-cool" {
		t.Errorf("7d request: %+v", q)
	}
	// a draft: pwm and stop from the running channel, validated like a preset
	wantCode(t, e.do(t, "POST", "/api/preview", `{"channels":[{"name":"hdd","sensor":"drivetemp:max","curve":[[30,87],[42,140],[50,200],[55,255]],"critical":60,"hysteresis":2,"min_on":"1m"}]}`, csrf), 200)
	if q := p.last(); len(q.Channels) != 1 || q.Channels[0].PWM != 3 || q.Channels[0].Stop != "140" || q.Channels[0].MinOn != time.Minute || q.Channels[0].Hysteresis != 2 {
		t.Errorf("draft: %+v", q.Channels)
	}
	n := len(p.reqs)
	for _, c := range []struct{ body, want string }{
		{`{"span":"1h"}`, "span"},
		{`{"channels":[{"name":"hdd","sensor":"drivetemp:max","curve":[[30,87]],"critical":60}],"preset":"x"}`, "either"},
		{`{"channels":[{"name":"hdd","sensor":"drivetemp:max","curve":[[50,87],[40,140]],"critical":60}]}`, "preview rejected"},
		{`{"channels":[{"name":"hdd","sensor":"drivetemp:max","curve":[[30,87,1],[40,140]],"critical":60}]}`, "preview rejected"},
		{`{"channels":[{"name":"pcie","sensor":"k10temp","curve":[[30,87],[40,140]],"critical":60}]}`, "preview rejected"},
		{`{"channels":[{"name":"hdd","pwm":2,"sensor":"drivetemp:max","curve":[[30,87],[40,140]],"critical":60}]}`, "preview rejected"},
		{`{"preset":"../x"}`, "invalid preset name"},
		{`{"assist":{"HDD!":true}}`, "invalid channel name"},
		{`{"bogus":1}`, "invalid JSON"},
		{`{} {}`, "trailing data"},
	} {
		wantError(t, e.do(t, "POST", "/api/preview", c.body, csrf), 400, c.want)
	}
	if len(p.reqs) != n {
		t.Errorf("a rejected request reached the previewer")
	}
	// the previewer's errors: no assist table 400, unknown preset 404, busy 429
	p.err = fmt.Errorf("channel %q has no [[assist]] table: %w", "cpu", ErrBadInput)
	wantError(t, e.do(t, "POST", "/api/preview", `{"assist":{"cpu":true}}`, csrf), 400, "no [[assist]] table")
	p.err = fmt.Errorf("unknown preset: %w", fs.ErrNotExist)
	wantCode(t, e.do(t, "POST", "/api/preview", `{"preset":"nope"}`, csrf), 404)
	p.err = control.ErrPreviewBusy
	wantCode(t, e.do(t, "POST", "/api/preview", "", csrf), 429)
	// without a previewer: 501
	wantCode(t, newEnv(t, AuthConfig{}).do(t, "POST", "/api/preview", "", csrf), 501)
}

// TestPostPreviewScope: a read token may preview (it writes nothing), an
// anonymous caller may not.
func TestPostPreviewScope(t *testing.T) {
	e, _ := tokenEnv(t)
	p := &fakePreviewer{}
	file := e.srv.deps.TokenFile
	e.withDeps(t, adminBasic, func(d *Deps) { d.TokenFile = file; d.Preview = p })
	read, _ := mint(t, e, "reader", "read", time.Hour)
	wantCode(t, e.do(t, "POST", "/api/preview", "", bearer(read)), 200)
	wantCode(t, e.do(t, "POST", "/api/preview", "", csrf), 401)
}
