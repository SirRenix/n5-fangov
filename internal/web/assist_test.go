package web

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
)

// fakeAssist is the assist store: hdd has a table, everything else not.
type fakeAssist struct {
	mu    sync.Mutex
	state map[string]bool
	calls []string
	err   error
}

func (a *fakeAssist) SetEnabled(ch string, on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, fmt.Sprintf("%s=%v", ch, on))
	if a.err != nil {
		return a.err
	}
	if _, ok := a.state[ch]; !ok {
		return fmt.Errorf("channel %q has no valid [[assist]] table: %w", ch, fs.ErrNotExist)
	}
	a.state[ch] = on
	return nil
}

func assistEnv(t *testing.T, auth AuthConfig) (*env, *fakeAssist) {
	t.Helper()
	e := newEnv(t, auth)
	a := &fakeAssist{state: map[string]bool{"hdd": false}}
	e.withDeps(t, auth, func(d *Deps) { d.Assist = a })
	return e, a
}

func TestPutAssist(t *testing.T) {
	e, a := assistEnv(t, AuthConfig{})
	r := e.do(t, "PUT", "/api/assist/hdd", `{"enabled":true}`, csrf)
	wantCode(t, r, 200)
	var b struct {
		OK      bool   `json:"ok"`
		Channel string `json:"channel"`
		Enabled bool   `json:"enabled"`
	}
	decode(t, r.body, &b)
	if !b.OK || b.Channel != "hdd" || !b.Enabled || !a.state["hdd"] {
		t.Errorf("on: %+v %v", b, a.state)
	}
	wantCode(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":false}`, csrf), 200)
	if a.state["hdd"] {
		t.Error("off not passed on")
	}
	// no table: 404; bad name, missing or extra member, wrong type: 400
	wantError(t, e.do(t, "PUT", "/api/assist/cpu", `{"enabled":true}`, csrf), 404, "no valid [[assist]] table")
	wantError(t, e.do(t, "PUT", "/api/assist/HDD!", `{"enabled":true}`, csrf), 400, "invalid channel name")
	wantError(t, e.do(t, "PUT", "/api/assist/hdd", `{}`, csrf), 400, `"enabled"`)
	wantError(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":"yes"}`, csrf), 400, "invalid JSON")
	wantError(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":true,"boost":5}`, csrf), 400, "invalid JSON")
	wantError(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":true,"x":"`+strings.Repeat("a", maxJSONBody)+`"}`, csrf), 413, "body exceeds")
	// CSRF header required for a cookie/basic caller; GET is not a route
	wantCode(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":true}`, nil), 403)
	wantCode(t, e.do(t, "GET", "/api/assist/hdd", "", nil), 405)
	// store failures: written but not applied 500, I/O 500
	a.err = &ReloadError{Err: errors.New("not applied")}
	wantError(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":true}`, csrf), 500, "not applied")
	a.err = fmt.Errorf("write: %w", ErrStore)
	wantCode(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":true}`, csrf), 500)
	// without a store: 501
	e2 := newEnv(t, AuthConfig{})
	wantCode(t, e2.do(t, "PUT", "/api/assist/hdd", `{"enabled":true}`, csrf), 501)
}

// TestPutAssistScope: the switch is a control route — a read token gets
// 403, a control token passes, anonymous callers 401.
func TestPutAssistScope(t *testing.T) {
	e, _ := tokenEnv(t)
	a := &fakeAssist{state: map[string]bool{"hdd": false}}
	file := e.srv.deps.TokenFile
	e.withDeps(t, adminBasic, func(d *Deps) { d.TokenFile = file; d.Assist = a })
	read, _ := mint(t, e, "reader", "read", time.Hour)
	ctl, _ := mint(t, e, "ctl", "control", time.Hour)
	wantCode(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":true}`, bearer(read)), 403)
	wantCode(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":true}`, bearer(ctl)), 200)
	wantCode(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":true}`, csrf), 401)
	if !a.state["hdd"] {
		t.Error("control token did not switch")
	}
}

// TestStateAssist: GET /api/state carries the assist member for a channel
// with a table and omits it otherwise; anonymous callers get enabled and
// boost only (the source ids name the hardware).
func TestStateAssist(t *testing.T) {
	e := newEnv(t, adminBasic)
	e.svc.mu.Lock()
	e.svc.snap.Channels[1].Mode = control.ModeAuto
	e.svc.snap.Channels[1].Assist = &control.AssistState{Enabled: true, Boost: 45, Source: "hwmon:amdgpu:temp1", Sources: []control.AssistSourceState{
		{Sensor: "hwmon:amdgpu:temp1", Temp: 58.7, Boost: 45, Start: 50, Full: 58}, {Sensor: "nvme:max", Temp: 44, Boost: 0, Start: 46, Full: 50},
	}}
	e.svc.hist = append(e.svc.hist, control.HistoryPoint{TS: 1789500010, Temp: map[string]float64{"hdd": 40}, Duty: map[string]int{"hdd": 150}, RPM: map[string]int{}, Assist: map[string]int{"hdd": 45}})
	e.svc.mu.Unlock()

	var full struct {
		Channels []map[string]any `json:"channels"`
	}
	decode(t, e.do(t, "GET", "/api/state", "", basicAuth("admin", "pw")).body, &full)
	if _, has := full.Channels[0]["assist"]; has {
		t.Errorf("cpu without a table carries assist: %v", full.Channels[0])
	}
	as, _ := full.Channels[1]["assist"].(map[string]any)
	srcs, _ := as["sources"].([]any)
	if as["enabled"] != true || as["boost"] != 45.0 || as["source"] != "hwmon:amdgpu:temp1" || len(srcs) != 2 {
		t.Errorf("signed in: %v", as)
	}
	if len(srcs) > 0 {
		if s0, _ := srcs[0].(map[string]any); s0["start"] != 50.0 || s0["full"] != 58.0 {
			t.Errorf("source thresholds: %v", s0)
		}
	}
	var anon struct {
		Channels []map[string]any `json:"channels"`
	}
	decode(t, e.do(t, "GET", "/api/state", "", nil).body, &anon)
	pa, _ := anon.Channels[1]["assist"].(map[string]any)
	if pa["enabled"] != true || pa["boost"] != 45.0 || len(pa) != 2 {
		t.Errorf("anonymous: %v", pa)
	}
	if _, has := anon.Channels[0]["assist"]; has {
		t.Error("anonymous cpu carries assist")
	}
	// history: the boost per channel for both kinds of caller
	for _, hdr := range []map[string]string{nil, basicAuth("admin", "pw")} {
		body := e.do(t, "GET", "/api/history?minutes=120", "", hdr).body
		if !strings.Contains(body, `"assist":{"hdd":45}`) || strings.Count(body, `"assist"`) != 1 {
			t.Errorf("history: %s", body)
		}
	}
}
