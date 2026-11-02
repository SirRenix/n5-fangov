package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
)

func TestHealthzState(t *testing.T) {
	ch := func(m control.Mode) []control.ChannelState {
		return []control.ChannelState{{Name: "cpu", Mode: control.ModeAuto}, {Name: "hdd", Mode: m}}
	}
	grace := 30 * time.Second
	for _, c := range []struct {
		name   string
		snap   control.Snapshot
		age    time.Duration
		cycle  time.Duration
		code   int
		status string
	}{
		{"ok", control.Snapshot{Status: control.StatusOK, Channels: ch(control.ModeAuto)}, time.Hour, 5 * time.Second, 200, "ok"},
		{"dry run", control.Snapshot{Status: control.StatusDryRun}, time.Hour, 5 * time.Second, 200, "ok"},
		{"manual and critical are working protection", control.Snapshot{Status: control.StatusOK, Channels: ch(control.ModeCritical)}, time.Hour, 5 * time.Second, 200, "ok"},
		{"starting within grace", control.Snapshot{Status: control.StatusStarting}, 10 * time.Second, 0, 200, "starting"},
		{"starting beyond grace", control.Snapshot{Status: control.StatusStarting}, 31 * time.Second, 0, 503, "stuck"},
		{"loop stopped after the first cycles", control.Snapshot{Status: control.StatusOK, Channels: ch(control.ModeAuto)}, time.Hour, 31 * time.Second, 503, "stuck"},
		{"write error", control.Snapshot{Status: control.StatusWriteError}, time.Hour, 5 * time.Second, 503, "degraded"},
		{"all sensors failed", control.Snapshot{Status: control.StatusSensorError}, time.Hour, 5 * time.Second, 503, "degraded"},
		{"one sensor failed", control.Snapshot{Status: control.StatusOK, Channels: ch(control.ModeSensor)}, time.Hour, 5 * time.Second, 503, "degraded"},
		{"stall", control.Snapshot{Status: control.StatusOK, Channels: ch(control.ModeStall)}, time.Hour, 5 * time.Second, 503, "degraded"},
		{"failsafe wins", control.Snapshot{Status: control.StatusWriteError, Channels: ch(control.ModeFailsafe)}, time.Hour, time.Hour, 503, "failsafe"},
	} {
		code, st := healthzState(c.snap, c.age, c.cycle, grace)
		if code != c.code || st != c.status {
			t.Errorf("%s: %d %s, want %d %s", c.name, code, st, c.code, c.status)
		}
	}
}

func TestHealthzPublicWithoutReadings(t *testing.T) {
	e := newEnv(t, adminBasic)
	r := e.do(t, "GET", "/healthz", "", nil)
	wantCode(t, r, 200)
	if strings.TrimSpace(r.body) != `{"status":"ok"}` {
		t.Errorf("body carries only the state: %s", r.body)
	}
	if r.hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("cache: %q", r.hdr.Get("Cache-Control"))
	}
	req, _ := http.NewRequest("HEAD", e.ts.URL+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("HEAD: %v %v", err, resp)
	}
	resp.Body.Close()
	e.svc.mu.Lock()
	e.svc.snap.Status = control.StatusWriteError
	e.svc.mu.Unlock()
	r = e.do(t, "GET", "/healthz", "", nil)
	wantCode(t, r, 503)
	if !strings.Contains(r.body, `"degraded"`) {
		t.Errorf("degraded: %s", r.body)
	}
}

func TestDriverAndHealthEndpoints(t *testing.T) {
	e := newEnv(t, adminBasic)
	ok := basicAuth("admin", "pw")
	wantError(t, e.do(t, "GET", "/api/driver", "", ok), 501, "no driver")
	wantError(t, e.do(t, "GET", "/api/health", "", ok), 501, "no health")
	e.withDeps(t, adminBasic, func(d *Deps) {
		d.Driver = func() any {
			return DriverInfo{Module: "minisforum_n5_it5571", Loaded: true, Version: "0.2.1+sr1", Mode: "write-enabled", Expected: "0.2.1+sr2", Pending: true}
		}
		d.Health = func() any {
			return map[string]any{"summary": "all good", "checks": []HealthItem{{Level: "ok", Name: "config", Detail: "parses"}}}
		}
	})
	r := e.do(t, "GET", "/api/driver", "", ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"pending":true`) || !strings.Contains(r.body, `"expected":"0.2.1+sr2"`) {
		t.Errorf("driver = %s", r.body)
	}
	r = e.do(t, "GET", "/api/health", "", ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"level":"ok"`) || !strings.Contains(r.body, `"summary":"all good"`) {
		t.Errorf("health = %s", r.body)
	}
	// both name the hardware: protected
	wantCode(t, e.do(t, "GET", "/api/driver", "", nil), 401)
	wantCode(t, e.do(t, "GET", "/api/health", "", nil), 401)
}
