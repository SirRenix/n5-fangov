package web

import (
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
)

func TestHistoryTiersByMinutes(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	for _, c := range []struct {
		minutes string
		span    time.Duration
	}{
		{"", 120 * time.Minute},
		{"120", 2 * time.Hour},
		{"180", 3 * time.Hour},
		{"1440", 24 * time.Hour},
		{"10080", 7 * 24 * time.Hour},
	} {
		q := ""
		if c.minutes != "" {
			q = "?minutes=" + c.minutes
		}
		r := e.do(t, "GET", "/api/history"+q, "", nil)
		wantCode(t, r, 200)
		e.svc.mu.Lock()
		span, from := e.svc.histSpan, e.svc.histFrom
		e.svc.mu.Unlock()
		if span != c.span || from != 0 {
			t.Errorf("minutes=%q: HistoryRange(%s, %d), want (%s, 0)", c.minutes, span, from, c.span)
		}
	}
	// since reaches the store
	r := e.do(t, "GET", "/api/history?minutes=1440&since=1789499990", "", nil)
	wantCode(t, r, 200)
	e.svc.mu.Lock()
	from := e.svc.histFrom
	e.svc.mu.Unlock()
	if from != 1789499990 {
		t.Errorf("since not passed through: %d", from)
	}
	if strings.Contains(r.body, `"ts":1789499990`) || !strings.Contains(r.body, `"ts":1789500000`) {
		t.Errorf("since filter: %s", r.body)
	}
}

func TestHistoryCSV(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.svc.mu.Lock()
	e.svc.hist = []control.HistoryPoint{
		{TS: 1789499990, Temp: map[string]float64{"cpu": 35.5, "hdd": 31}, Duty: map[string]int{"cpu": 85, "hdd": 140}, RPM: map[string]int{"cpu": 1990, "hdd": 900}, Extra: map[string]float64{"hwmon:amdgpu:temp1": 48.5, "ec:system": 32}},
		{TS: 1789500000, Temp: map[string]float64{"cpu": 36.0}, Duty: map[string]int{"cpu": 85, "hdd": 140}, RPM: map[string]int{"cpu": 2000, "hdd": 900}},
	}
	e.svc.mu.Unlock()
	r := e.do(t, "GET", "/api/history.csv?minutes=1440", "", nil)
	wantAttachment(t, r, "text/csv", `^n5-fangov-history-[A-Za-z0-9.-]+-\d{8}-\d{6}\.csv$`)
	lines := strings.Split(strings.TrimRight(r.body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("csv lines: %q", lines)
	}
	// snapshot order (cpu, hdd) without Deps.Channels; extras sorted
	if lines[0] != "ts,time,cpu_temp,cpu_duty,cpu_rpm,hdd_temp,hdd_duty,hdd_rpm,ec:system,hwmon:amdgpu:temp1" {
		t.Errorf("header: %s", lines[0])
	}
	local := time.Unix(1789499990, 0).Format(time.RFC3339)
	if lines[1] != "1789499990,"+local+",35.5,85,1990,31,140,900,32,48.5" {
		t.Errorf("row 1: %s", lines[1])
	}
	if !strings.HasSuffix(lines[2], ",36,85,2000,,140,900,,") {
		t.Errorf("row 2 (absent cells): %s", lines[2])
	}
	e.svc.mu.Lock()
	span := e.svc.histSpan
	e.svc.mu.Unlock()
	if span != 24*time.Hour {
		t.Errorf("csv span %s", span)
	}
	// Deps.Channels sets the column order
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Channels = func() []string { return []string{"hdd", "cpu"} } })
	r = e.do(t, "GET", "/api/history.csv", "", nil)
	wantCode(t, r, 200)
	if !strings.HasPrefix(r.body, "ts,time,hdd_temp,hdd_duty,hdd_rpm,cpu_temp,") {
		t.Errorf("Deps.Channels order: %s", strings.SplitN(r.body, "\n", 2)[0])
	}
	// bounds as for /api/history
	wantError(t, e.do(t, "GET", "/api/history.csv?minutes=10081", "", nil), 400, "minutes must be 1..10080")
	wantError(t, e.do(t, "GET", "/api/history.csv?minutes=0", "", nil), 400, "minutes must be")
}

func TestHistoryCSVProtected(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
	// anonymous: silent 401, history itself stays public (filtered)
	wantError(t, e.do(t, "GET", "/api/history.csv", "", nil), 401, "authentication")
	wantCode(t, e.do(t, "GET", "/api/history", "", nil), 200)
	r := e.do(t, "GET", "/api/history.csv", "", basicAuth("admin", "pw"))
	wantAttachment(t, r, "text/csv", `\.csv$`)
	if !strings.HasPrefix(r.body, "ts,time,cpu_temp,") {
		t.Errorf("csv body: %.60q", r.body)
	}
}

func TestSchedulesAPI(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	wantError(t, e.do(t, "GET", "/api/schedules", "", nil), 501, "no scheduler")
	status := map[string]any{
		"entries":  []map[string]any{{"preset": "night", "from": "22:00", "to": "07:00", "days": []string{}, "fallback": false, "active": true}},
		"active":   0,
		"next":     map[string]any{"ts": 1789520400, "preset": ""},
		"last":     nil,
		"timezone": "UTC +00:00",
	}
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Schedules = func() any { return status } })
	r := e.do(t, "GET", "/api/schedules", "", nil)
	wantCode(t, r, 200)
	var got map[string]any
	decode(t, r.body, &got)
	if got["active"].(float64) != 0 || got["last"] != nil || got["timezone"] != "UTC +00:00" {
		t.Errorf("schedules: %s", r.body)
	}
	if ents := got["entries"].([]any); len(ents) != 1 || ents[0].(map[string]any)["preset"] != "night" {
		t.Errorf("entries: %s", r.body)
	}
	// protected with basic auth
	e2, _, _, _ := storesEnv(t, adminBasic)
	e2.withDeps(t, adminBasic, func(d *Deps) { d.Schedules = func() any { return status } })
	wantError(t, e2.do(t, "GET", "/api/schedules", "", nil), 401, "authentication")
	wantCode(t, e2.do(t, "GET", "/api/schedules", "", basicAuth("admin", "pw")), 200)
}
