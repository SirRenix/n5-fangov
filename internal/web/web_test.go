package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/pvefand/internal/control"
)

// ---- fakes ---------------------------------------------------------------

type fakeService struct {
	mu        sync.Mutex
	snap      control.Snapshot
	hist      []control.HistoryPoint
	histSince time.Duration
	overrides map[string]int
	cleared   []string
	reloadErr error
	reloaded  [][]byte
}

func newFakeService() *fakeService {
	return &fakeService{
		snap: control.Snapshot{
			TS: 1789500000, Status: "ok", Profile: "n5pro", Verified: true, HwmonPath: "/sys/class/hwmon/hwmon14",
			Channels: []control.ChannelState{
				{Name: "cpu", PWM: 1, Sensor: "k10temp", Temp: 36.0, Duty: 85, Target: 85, RPM: 2000, Mode: control.ModeAuto},
				{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Temp: 31.5, Duty: 60, Target: 60, RPM: 900, Mode: control.ModeManual},
			},
			ExtraTemps: map[string]float64{"ec:system": 32.0},
			Alerts:     map[string]int64{"stall": 1789490000},
			Uptime:     3600,
		},
		hist: []control.HistoryPoint{
			{TS: 1789499990, Temp: map[string]float64{"cpu": 35.5}, Duty: map[string]int{"cpu": 85}, RPM: map[string]int{"cpu": 1990}},
			{TS: 1789500000, Temp: map[string]float64{"cpu": 36.0}, Duty: map[string]int{"cpu": 85}, RPM: map[string]int{"cpu": 2000}},
		},
		overrides: map[string]int{},
	}
}

func (f *fakeService) Snapshot() control.Snapshot { f.mu.Lock(); defer f.mu.Unlock(); return f.snap }
func (f *fakeService) History(since time.Duration) []control.HistoryPoint {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.histSince = since
	return f.hist
}
func (f *fakeService) SetOverride(ch string, duty int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch == "hdd" {
		return errors.New("hdd refuses override in fake")
	}
	f.overrides[ch] = duty
	return nil
}
func (f *fakeService) ClearOverride(ch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, ch)
	delete(f.overrides, ch)
	return nil
}
func (f *fakeService) Reload(raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloaded = append(f.reloaded, raw)
	return f.reloadErr
}

type fakeConfig struct {
	raw     []byte
	saveErr error
	saved   [][]byte
}

func (c *fakeConfig) Raw() ([]byte, error) { return c.raw, nil }
func (c *fakeConfig) Save(b []byte) error {
	if c.saveErr != nil {
		return c.saveErr
	}
	c.saved = append(c.saved, b)
	c.raw = b
	return nil
}

type fakeParsedConfig struct{ fakeConfig }

func (c *fakeParsedConfig) Parsed() (any, error) {
	return map[string]any{"daemon": map[string]any{"interval": "10s"}}, nil
}

type fakePresets struct {
	list     []Preset
	applied  []string
	saved    []string
	applyErr error
}

func (p *fakePresets) List() ([]Preset, error) { return p.list, nil }
func (p *fakePresets) Apply(name string) error {
	if p.applyErr != nil {
		return p.applyErr
	}
	p.applied = append(p.applied, name)
	return nil
}
func (p *fakePresets) Save(name string) error { p.saved = append(p.saved, name); return nil }

const sampleTOML = "[daemon]\ninterval = \"10s\"\n\n[web]\nlisten = \"127.0.0.1:8010\"\n\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45,85],[80,255]]\ncritical = 88\nstop = \"auto\"\n"

type env struct {
	svc     *fakeService
	cfg     *fakeConfig
	presets *fakePresets
	logs    []string
	srv     *Server
	ts      *httptest.Server
}

func newEnv(t *testing.T, auth AuthConfig) *env {
	t.Helper()
	e := &env{
		svc:     newFakeService(),
		cfg:     &fakeConfig{raw: []byte(sampleTOML)},
		presets: &fakePresets{list: []Preset{{Name: "quiet", Channels: []string{"cpu", "hdd"}}}},
		logs:    []string{"line1", "line2", "line3"},
	}
	e.srv = New(Deps{
		Service: e.svc,
		Config:  e.cfg,
		Presets: e.presets,
		Log: func(n int) ([]string, error) {
			if n < len(e.logs) {
				return e.logs[len(e.logs)-n:], nil
			}
			return e.logs, nil
		},
		Profiles: func() []ProfileInfo {
			return []ProfileInfo{{Name: "n5pro", Title: "Minisforum N5 Pro (IT5571 EC)", Verified: true, Active: true}, {Name: "nct67xx", Title: "Nuvoton NCT67xx", Notes: "untested"}}
		},
		Sensors: func() []SensorInfo { return []SensorInfo{{ID: "k10temp", Description: "AMD Tctl"}} },
		Version: "1.2.3-test",
		Auth:    auth,
	})
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

type resp struct {
	code int
	body string
	hdr  http.Header
}

func (e *env) do(t *testing.T, method, path, body string, hdr map[string]string) resp {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{code: res.StatusCode, body: string(b), hdr: res.Header}
}

var csrf = map[string]string{CSRFHeader: "1"}

func decode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
}

func wantCode(t *testing.T, r resp, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("status = %d, want %d; body %s", r.code, code, r.body)
	}
}

func wantError(t *testing.T, r resp, code int, contains string) {
	t.Helper()
	wantCode(t, r, code)
	var m map[string]string
	decode(t, r.body, &m)
	if !strings.Contains(m["error"], contains) {
		t.Fatalf("error %q does not contain %q", m["error"], contains)
	}
}

// ---- tests ---------------------------------------------------------------

func TestStateJSONShape(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "GET", "/api/state", "", nil)
	wantCode(t, r, 200)
	if ct := r.hdr.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type %q", ct)
	}
	var m map[string]any
	decode(t, r.body, &m)
	for _, k := range []string{"ts", "status", "profile", "verified", "hwmon_path", "dry_run", "channels", "extra_temps", "alerts", "uptime_s"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	chs := m["channels"].([]any)
	if len(chs) != 2 {
		t.Fatalf("channels = %d", len(chs))
	}
	c0 := chs[0].(map[string]any)
	if c0["name"] != "cpu" || c0["mode"] != "auto" || c0["duty"].(float64) != 85 || c0["rpm"].(float64) != 2000 || c0["target"].(float64) != 85 {
		t.Errorf("channel 0 = %v", c0)
	}
	if m["alerts"].(map[string]any)["stall"].(float64) != 1789490000 {
		t.Errorf("alerts = %v", m["alerts"])
	}
}

func TestStateNilMapsBecomeEmpty(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.svc.snap.Channels, e.svc.snap.Alerts, e.svc.snap.ExtraTemps = nil, nil, nil
	r := e.do(t, "GET", "/api/state", "", nil)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"channels":[]`) || !strings.Contains(r.body, `"alerts":{}`) || !strings.Contains(r.body, `"extra_temps":{}`) {
		t.Fatalf("nil maps not normalised: %s", r.body)
	}
}

func TestHistoryQuery(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "GET", "/api/history", "", nil)
	wantCode(t, r, 200)
	if e.svc.histSince != 120*time.Minute {
		t.Errorf("default since = %v", e.svc.histSince)
	}
	var pts []map[string]any
	decode(t, r.body, &pts)
	if len(pts) != 2 || pts[1]["temp"].(map[string]any)["cpu"].(float64) != 36.0 {
		t.Errorf("history = %v", pts)
	}
	r = e.do(t, "GET", "/api/history?minutes=30", "", nil)
	wantCode(t, r, 200)
	if e.svc.histSince != 30*time.Minute {
		t.Errorf("since = %v", e.svc.histSince)
	}
	wantError(t, e.do(t, "GET", "/api/history?minutes=0", "", nil), 400, "minutes")
	wantError(t, e.do(t, "GET", "/api/history?minutes=abc", "", nil), 400, "minutes")
	wantError(t, e.do(t, "GET", "/api/history?minutes=99999", "", nil), 400, "minutes")
	e.svc.hist = nil
	r = e.do(t, "GET", "/api/history", "", nil)
	if strings.TrimSpace(r.body) != "[]" {
		t.Errorf("nil history body = %q", r.body)
	}
}

func TestOverridePutDelete(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "PUT", "/api/override/cpu", `{"duty":191}`, csrf)
	wantCode(t, r, 200)
	if e.svc.overrides["cpu"] != 191 {
		t.Fatalf("override = %v", e.svc.overrides)
	}
	var m map[string]any
	decode(t, r.body, &m)
	if m["duty"].(float64) != 191 || m["mode"] != "manual" {
		t.Errorf("response = %v", m)
	}

	r = e.do(t, "PUT", "/api/override/cpu", `{"percent":75}`, csrf)
	wantCode(t, r, 200)
	if e.svc.overrides["cpu"] != 191 { // 75% of 255 = 191.25 → 191
		t.Fatalf("percent override = %v", e.svc.overrides)
	}
	r = e.do(t, "PUT", "/api/override/cpu", `{"percent":100}`, csrf)
	wantCode(t, r, 200)
	if e.svc.overrides["cpu"] != 255 {
		t.Fatalf("100%% = %v", e.svc.overrides)
	}

	r = e.do(t, "DELETE", "/api/override/cpu", "", csrf)
	wantCode(t, r, 200)
	if len(e.svc.cleared) != 1 || e.svc.cleared[0] != "cpu" {
		t.Fatalf("cleared = %v", e.svc.cleared)
	}
	if _, ok := e.svc.overrides["cpu"]; ok {
		t.Fatal("override not cleared")
	}
}

func TestOverrideValidation(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	cases := []struct {
		path, body string
		code       int
		msg        string
	}{
		{"/api/override/cpu", `{"duty":256}`, 400, "0..255"},
		{"/api/override/cpu", `{"duty":-1}`, 400, "0..255"},
		{"/api/override/cpu", `{"duty":12.5}`, 400, "integer"},
		{"/api/override/cpu", `{"percent":101}`, 400, "0..100"},
		{"/api/override/cpu", `{"duty":10,"percent":10}`, 400, "not both"},
		{"/api/override/cpu", `{}`, 400, "required"},
		{"/api/override/cpu", `{"foo":1}`, 400, "invalid JSON"},
		{"/api/override/cpu", `not json`, 400, "invalid JSON"},
		{"/api/override/nope", `{"duty":10}`, 404, "unknown channel"},
		{"/api/override/Bad-Name", `{"duty":10}`, 400, "invalid channel name"},
		{"/api/override/hdd", `{"duty":10}`, 400, "refuses"},
	}
	for _, c := range cases {
		wantError(t, e.do(t, "PUT", c.path, c.body, csrf), c.code, c.msg)
	}
	wantError(t, e.do(t, "DELETE", "/api/override/nope", "", csrf), 404, "unknown channel")
	if len(e.svc.overrides) != 0 {
		t.Fatalf("overrides set despite validation errors: %v", e.svc.overrides)
	}
}

func TestCSRFHeaderRequired(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, nil), 403, CSRFHeader)
	wantError(t, e.do(t, "DELETE", "/api/override/cpu", "", map[string]string{CSRFHeader: "yes"}), 403, CSRFHeader)
	wantError(t, e.do(t, "PUT", "/api/config", sampleTOML, nil), 403, CSRFHeader)
	wantError(t, e.do(t, "POST", "/api/presets/quiet/apply", "", nil), 403, CSRFHeader)
	if len(e.svc.overrides) != 0 || len(e.presets.applied) != 0 || len(e.cfg.saved) != 0 {
		t.Fatal("state changed without CSRF header")
	}
	// GET stays open.
	wantCode(t, e.do(t, "GET", "/api/state", "", nil), 200)
	// Socket handler: no CSRF needed.
	sock := httptest.NewServer(e.srv.SocketHandler())
	defer sock.Close()
	req, _ := http.NewRequest("PUT", sock.URL+"/api/override/cpu", strings.NewReader(`{"duty":42}`))
	res, err := sock.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || e.svc.overrides["cpu"] != 42 {
		t.Fatalf("socket handler: %d %v", res.StatusCode, e.svc.overrides)
	}
}

func TestBasicAuth(t *testing.T) {
	hash := PasswordHash("admin", "s3cret")
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: hash})
	// GET open without auth.
	wantCode(t, e.do(t, "GET", "/api/state", "", nil), 200)
	// Write without auth → 401.
	r := e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, csrf)
	wantError(t, r, 401, "authentication")
	if r.hdr.Get("WWW-Authenticate") != "" {
		t.Error("unexpected WWW-Authenticate challenge (UI shows its own prompt)")
	}
	auth := func(u, p string) map[string]string {
		req, _ := http.NewRequest("GET", "/", nil)
		req.SetBasicAuth(u, p)
		return map[string]string{CSRFHeader: "1", "Authorization": req.Header.Get("Authorization")}
	}
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, auth("admin", "wrong")), 401, "authentication")
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, auth("other", "s3cret")), 401, "authentication")
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, map[string]string{CSRFHeader: "1", "Authorization": "Bearer xyz"}), 401, "authentication")
	// CSRF checked before auth.
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, map[string]string{"Authorization": auth("admin", "s3cret")["Authorization"]}), 403, CSRFHeader)
	if len(e.svc.overrides) != 0 {
		t.Fatal("override set without valid auth")
	}
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, auth("admin", "s3cret")), 200)
	if e.svc.overrides["cpu"] != 10 {
		t.Fatal("override not set with valid auth")
	}
	// Mode is case-insensitive; misconfigured hash fails closed.
	e2 := newEnv(t, AuthConfig{Mode: "Basic", User: "admin", PasswordHash: "nothex"})
	wantError(t, e2.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, auth("admin", "s3cret")), 401, "authentication")
	// Socket handler ignores auth.
	sock := httptest.NewServer(e.srv.SocketHandler())
	defer sock.Close()
	req, _ := http.NewRequest("DELETE", sock.URL+"/api/override/cpu", nil)
	res, err := sock.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("socket delete = %d", res.StatusCode)
	}
}

func TestAuthorizedConstantTime(t *testing.T) {
	s := New(Deps{Auth: AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")}})
	mk := func(u, p string) *http.Request {
		r := httptest.NewRequest("PUT", "/api/config", nil)
		r.SetBasicAuth(u, p)
		return r
	}
	if !s.authorized(mk("admin", "pw")) {
		t.Fatal("valid credentials rejected")
	}
	for _, c := range [][2]string{{"admin", "pw2"}, {"admi", "pw"}, {"", ""}, {"admin", ""}, {"admin:pw", ""}} {
		if s.authorized(mk(c[0], c[1])) {
			t.Fatalf("accepted %q/%q", c[0], c[1])
		}
	}
	if s.authorized(httptest.NewRequest("PUT", "/api/config", nil)) {
		t.Fatal("accepted request without Authorization header")
	}
}

func TestConfigGetPut(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "GET", "/api/config", "", nil)
	wantCode(t, r, 200)
	var m map[string]any
	decode(t, r.body, &m)
	if m["raw"] != sampleTOML {
		t.Fatalf("raw = %q", m["raw"])
	}
	if _, ok := m["config"]; ok {
		t.Fatal("config key present without ParsedConfigStore")
	}

	newTOML := strings.Replace(sampleTOML, "critical = 88", "critical = 90", 1)
	r = e.do(t, "PUT", "/api/config", newTOML, csrf)
	wantCode(t, r, 200)
	decode(t, r.body, &m)
	if m["restart_required"] != false {
		t.Errorf("restart_required = %v", m["restart_required"])
	}
	if len(e.cfg.saved) != 1 || string(e.cfg.saved[0]) != newTOML {
		t.Fatalf("saved = %q", e.cfg.saved)
	}
	if len(e.svc.reloaded) != 1 || string(e.svc.reloaded[0]) != newTOML {
		t.Fatalf("reloaded = %q", e.svc.reloaded)
	}
	r = e.do(t, "GET", "/api/config", "", nil)
	decode(t, r.body, &m)
	if m["raw"] != newTOML {
		t.Fatal("GET after PUT does not return new config")
	}

	// Restart-required path.
	e.svc.reloadErr = control.ErrRestartRequired
	r = e.do(t, "PUT", "/api/config", sampleTOML, csrf)
	wantCode(t, r, 202)
	decode(t, r.body, &m)
	if m["restart_required"] != true {
		t.Errorf("restart_required = %v", m["restart_required"])
	}
	if len(e.cfg.saved) != 2 {
		t.Fatal("config not saved on restart-required")
	}

	// Other reload error → 500 (file already saved).
	e.svc.reloadErr = errors.New("boom")
	wantError(t, e.do(t, "PUT", "/api/config", sampleTOML, csrf), 500, "boom")

	// Save rejects → 400, no reload.
	e.svc.reloadErr = nil
	n := len(e.svc.reloaded)
	e.cfg.saveErr = errors.New("interval out of range")
	wantError(t, e.do(t, "PUT", "/api/config", sampleTOML, csrf), 400, "interval out of range")
	if len(e.svc.reloaded) != n {
		t.Fatal("reload called although save failed")
	}
	e.cfg.saveErr = nil
	wantError(t, e.do(t, "PUT", "/api/config", "  \n", csrf), 400, "empty")
}

func TestConfigParsed(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	pc := &fakeParsedConfig{fakeConfig{raw: []byte(sampleTOML)}}
	e.srv = New(Deps{Service: e.svc, Config: pc})
	ts := httptest.NewServer(e.srv.Handler())
	defer ts.Close()
	e.ts = ts
	r := e.do(t, "GET", "/api/config", "", nil)
	wantCode(t, r, 200)
	var m map[string]any
	decode(t, r.body, &m)
	if m["config"].(map[string]any)["daemon"].(map[string]any)["interval"] != "10s" {
		t.Fatalf("parsed config = %v", m["config"])
	}
}

func TestPresets(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "GET", "/api/presets", "", nil)
	wantCode(t, r, 200)
	var list []Preset
	decode(t, r.body, &list)
	if len(list) != 1 || list[0].Name != "quiet" || len(list[0].Channels) != 2 {
		t.Fatalf("presets = %v", list)
	}
	wantCode(t, e.do(t, "POST", "/api/presets/quiet/apply", "", csrf), 200)
	if len(e.presets.applied) != 1 || e.presets.applied[0] != "quiet" {
		t.Fatalf("applied = %v", e.presets.applied)
	}
	wantCode(t, e.do(t, "PUT", "/api/presets/night-1.0", "", csrf), 200)
	if len(e.presets.saved) != 1 || e.presets.saved[0] != "night-1.0" {
		t.Fatalf("saved = %v", e.presets.saved)
	}
	wantError(t, e.do(t, "PUT", "/api/presets/.hidden", "", csrf), 400, "invalid preset name")
	wantError(t, e.do(t, "POST", "/api/presets/a%2Fb/apply", "", csrf), 400, "invalid preset name")
	e.presets.applyErr = errors.New("no such preset")
	wantError(t, e.do(t, "POST", "/api/presets/missing/apply", "", csrf), 400, "no such preset")
	e.presets.applyErr = control.ErrRestartRequired
	wantCode(t, e.do(t, "POST", "/api/presets/other/apply", "", csrf), 202)
	// nil list → [] not null
	e.presets.list = nil
	r = e.do(t, "GET", "/api/presets", "", nil)
	if strings.TrimSpace(r.body) != "[]" {
		t.Fatalf("nil presets = %q", r.body)
	}
}

func TestLog(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "GET", "/api/log", "", nil)
	wantCode(t, r, 200)
	var m map[string][]string
	decode(t, r.body, &m)
	if len(m["lines"]) != 3 {
		t.Fatalf("lines = %v", m["lines"])
	}
	r = e.do(t, "GET", "/api/log?lines=2", "", nil)
	decode(t, r.body, &m)
	if len(m["lines"]) != 2 || m["lines"][0] != "line2" {
		t.Fatalf("lines=2 → %v", m["lines"])
	}
	wantError(t, e.do(t, "GET", "/api/log?lines=0", "", nil), 400, "lines")
	wantError(t, e.do(t, "GET", "/api/log?lines=x", "", nil), 400, "lines")
}

func TestProfilesVersionSensors(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "GET", "/api/profiles", "", nil)
	wantCode(t, r, 200)
	var ps []ProfileInfo
	decode(t, r.body, &ps)
	if len(ps) != 2 || !ps[0].Verified || !ps[0].Active || ps[1].Verified {
		t.Fatalf("profiles = %+v", ps)
	}
	r = e.do(t, "GET", "/api/version", "", nil)
	wantCode(t, r, 200)
	var v map[string]string
	decode(t, r.body, &v)
	if v["version"] != "1.2.3-test" || v["name"] != "pvefand" {
		t.Fatalf("version = %v", v)
	}
	r = e.do(t, "GET", "/api/sensors", "", nil)
	wantCode(t, r, 200)
	var ss []SensorInfo
	decode(t, r.body, &ss)
	if len(ss) != 1 || ss[0].ID != "k10temp" {
		t.Fatalf("sensors = %v", ss)
	}
}

func TestNotImplementedWhenDepsMissing(t *testing.T) {
	ts := httptest.NewServer(NewHandler(Deps{Service: newFakeService()}))
	defer ts.Close()
	for _, p := range []string{"/api/config", "/api/presets", "/api/log", "/api/profiles", "/api/sensors"} {
		res, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 501 {
			t.Errorf("%s = %d, want 501", p, res.StatusCode)
		}
	}
	res, _ := http.Get(ts.URL + "/api/nope")
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("unknown endpoint = %d", res.StatusCode)
	}
}

func TestStaticIndex(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "GET", "/", "", nil)
	wantCode(t, r, 200)
	if !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/html") {
		t.Errorf("content-type %q", r.hdr.Get("Content-Type"))
	}
	if !strings.Contains(r.body, "<title>pvefand</title>") || !strings.Contains(r.body, `src="app.js"`) || !strings.Contains(r.body, `href="app.css"`) {
		t.Errorf("index.html content unexpected: %.200s", r.body)
	}
	if csp := r.hdr.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	r = e.do(t, "GET", "/app.js", "", nil)
	wantCode(t, r, 200)
	if !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/javascript") || !strings.Contains(r.body, "X-Pvefand-Csrf") {
		t.Errorf("app.js: %q %.100s", r.hdr.Get("Content-Type"), r.body)
	}
	if len(r.body) > 40*1024 {
		t.Errorf("app.js is %d bytes, budget 40 KB", len(r.body))
	}
	r = e.do(t, "GET", "/app.css", "", nil)
	wantCode(t, r, 200)
	if !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/css") {
		t.Errorf("app.css content-type %q", r.hdr.Get("Content-Type"))
	}
	wantCode(t, e.do(t, "GET", "/index.html", "", nil), 200)
	wantCode(t, e.do(t, "GET", "/other.txt", "", nil), 404)
	wantCode(t, e.do(t, "GET", "/static/app.js", "", nil), 404)
}
