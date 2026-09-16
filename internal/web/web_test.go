package web

import (
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
)

// ---- fakes ---------------------------------------------------------------

type fakeService struct {
	mu        sync.Mutex
	snap      control.Snapshot
	hist      []control.HistoryPoint
	histSince time.Duration // last History() argument
	histSpan  time.Duration // last HistoryRange() span
	histFrom  int64         // last HistoryRange() since
	overrides map[string]int
	fixedMode map[string]control.Mode // wins over the override-derived mode
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
		fixedMode: map[string]control.Mode{},
	}
}

// Snapshot mirrors the controller: an override shows as manual on the next
// read, unless fixedMode pins the channel (critical/stall keep their mode).
func (f *fakeService) Snapshot() control.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snap
	s.Channels = append([]control.ChannelState(nil), s.Channels...)
	for i := range s.Channels {
		if _, ok := f.overrides[s.Channels[i].Name]; ok {
			s.Channels[i].Mode = control.ModeManual
		}
		if m, ok := f.fixedMode[s.Channels[i].Name]; ok {
			s.Channels[i].Mode = m
		}
	}
	return s
}
func (f *fakeService) History(since time.Duration) []control.HistoryPoint {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.histSince = since
	return f.hist
}

// HistoryRange mirrors the store: the fake keeps one tier, since is strict.
func (f *fakeService) HistoryRange(span time.Duration, since int64) []control.HistoryPoint {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.histSpan, f.histFrom = span, since
	var out []control.HistoryPoint
	for _, p := range f.hist {
		if p.TS > since {
			out = append(out, p)
		}
	}
	return out
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
	return map[string]any{
		"daemon": map[string]any{"interval": "10s"},
		"web":    map[string]any{"auth": "basic", "user": "admin", "password_hash": currentHash(string(c.raw))},
	}, nil
}

type fakePresets struct {
	list     []Preset
	applied  []string
	saved    []string
	applyErr error
	saveErr  error
}

func (p *fakePresets) List() ([]Preset, error) { return p.list, nil }
func (p *fakePresets) Apply(name string) error {
	if p.applyErr != nil {
		return p.applyErr
	}
	p.applied = append(p.applied, name)
	return nil
}
func (p *fakePresets) Save(name string) error {
	if p.saveErr != nil {
		return p.saveErr
	}
	p.saved = append(p.saved, name)
	return nil
}

const sampleTOML = "[daemon]\ninterval = \"10s\"\n\n[web]\nlisten = \"127.0.0.1:8010\"\n\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45,85],[80,255]]\ncritical = 88\nstop = \"auto\"\n"

type env struct {
	svc      *fakeService
	cfg      *fakeConfig
	presets  *fakePresets
	logs     []string
	srv      *Server
	ts       *httptest.Server
	validate func([]byte) ([]string, error)
	logged   []string
	logMu    sync.Mutex
}

func (e *env) deps(auth AuthConfig) Deps {
	return Deps{
		Service: e.svc,
		Config:  e.cfg,
		Validate: func(raw []byte) ([]string, error) {
			if e.validate != nil {
				return e.validate(raw)
			}
			return nil, nil
		},
		Presets: e.presets,
		Log:     &fakeLogStore{lines: e.logs, clearErr: fmt.Errorf("journal-only log source: %w", errors.ErrUnsupported)},
		Profiles: func() []ProfileInfo {
			return []ProfileInfo{{Name: "n5pro", Title: "Minisforum N5 Pro (IT5571 EC)", Verified: true, Active: true}, {Name: "nct67xx", Title: "Nuvoton NCT67xx", Notes: "untested"}}
		},
		Sensors: func() []SensorInfo {
			t := 38.2
			return []SensorInfo{{ID: "k10temp", Description: "AMD Tctl", Temp: &t}, {ID: "hwmon:<name>:tempN", Description: "pattern"}}
		},
		Version:      "1.2.3-test",
		Auth:         auth,
		AllowedHosts: []string{"n5host.lan", "Fans.Example:8010"},
		Logf: func(format string, args ...any) {
			e.logMu.Lock()
			e.logged = append(e.logged, fmt.Sprintf(format, args...))
			e.logMu.Unlock()
		},
	}
}

func newEnv(t *testing.T, auth AuthConfig) *env {
	t.Helper()
	e := &env{
		svc:     newFakeService(),
		cfg:     &fakeConfig{raw: []byte(sampleTOML)},
		presets: &fakePresets{list: []Preset{{Name: "quiet", Channels: []string{"cpu", "hdd"}}}},
		logs:    []string{"line1", "line2", "line3"},
	}
	e.srv = New(e.deps(auth))
	e.ts = httptest.NewServer(e.srv.Handler())
	// Closed in the background: after a 413 net/http keeps the server
	// connection open for rstAvoidanceDelay (500 ms) and Close waits for
	// it — that, not PBKDF2, was the half second behind every "too large"
	// test. Nothing in the tests reads from the server after cleanup.
	t.Cleanup(func() { go e.ts.Close() })
	return e
}

type resp struct {
	code int
	body string
	hdr  http.Header
}

func (e *env) do(t *testing.T, method, path, body string, hdr map[string]string) resp {
	t.Helper()
	r, err := e.try(method, path, body, hdr)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// try is do without the test binding: safe to call from a goroutine (a
// t.Fatal outside the test goroutine is undefined behaviour; AUDIT hoch 2).
func (e *env) try(method, path, body string, hdr map[string]string) (resp, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rd)
	if err != nil {
		return resp{}, err
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	res, err := e.ts.Client().Do(req)
	if err != nil {
		return resp{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{code: res.StatusCode, body: string(b), hdr: res.Header}, nil
}

// waitTimeout waits for wg or gives up after d (false).
func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

var csrf = map[string]string{CSRFHeader: "1"}

// fastHash is the stored PBKDF2 form at config.PBKDF2MinIter (1000)
// iterations instead of the production 210000: same syntax, same verifier
// path, 200x cheaper per basicAuth request (AUDIT 7: five of the six
// slowest tests were PBKDF2 cost). Tests that check the production form
// (TestVerifyPasswordFormats, the legacy cases) call PasswordHash.
func fastHash(user, password string) string {
	salt := make([]byte, config.PBKDF2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, config.PBKDF2MinIter, config.PBKDF2KeyLen)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%s$%d$%s$%s", config.PBKDF2Prefix, config.PBKDF2MinIter, hex.EncodeToString(salt), hex.EncodeToString(key))
}

func basicAuth(u, p string) map[string]string {
	req, _ := http.NewRequest("GET", "/", nil)
	req.SetBasicAuth(u, p)
	return map[string]string{CSRFHeader: "1", "Authorization": req.Header.Get("Authorization")}
}

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
	var m map[string]any
	decode(t, r.body, &m)
	msg, _ := m["error"].(string)
	if !strings.Contains(msg, contains) {
		t.Fatalf("error %q does not contain %q", msg, contains)
	}
}

// withDeps rebuilds the env's server with f applied to the deps.
func (e *env) withDeps(t *testing.T, auth AuthConfig, f func(*Deps)) {
	t.Helper()
	d := e.deps(auth)
	f(&d)
	e.srv = New(d)
	e.ts.Close()
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
}

var attachmentName = regexp.MustCompile(`^attachment; filename="([^"]+)"$`)

func wantAttachment(t *testing.T, r resp, ctype, namePattern string) string {
	t.Helper()
	wantCode(t, r, 200)
	if ct := r.hdr.Get("Content-Type"); !strings.HasPrefix(ct, ctype) {
		t.Errorf("content-type %q, want %q", ct, ctype)
	}
	m := attachmentName.FindStringSubmatch(r.hdr.Get("Content-Disposition"))
	if m == nil {
		t.Fatalf("content-disposition %q", r.hdr.Get("Content-Disposition"))
	}
	if ok, _ := regexp.MatchString(namePattern, m[1]); !ok {
		t.Errorf("filename %q does not match %s", m[1], namePattern)
	}
	if r.hdr.Get("Cache-Control") != "no-store" || r.hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("export headers: %v", r.hdr)
	}
	return m[1]
}

var adminBasic = AuthConfig{Mode: "basic", User: "admin", PasswordHash: fastHash("admin", "pw")}

// storesEnv is newEnv plus the store fakes (account, alerts, dashboard,
// deletable presets, session file in a temp dir).
func storesEnv(t *testing.T, auth AuthConfig) (*env, *fakeAccount, *fakeAlerts, *fakeDashboard) {
	t.Helper()
	e := newEnv(t, auth)
	acc := &fakeAccount{cfg: auth}
	al := &fakeAlerts{status: AlertStatus{Transport: "auto", Effective: "mail", MailTo: "root", MailAvailable: true, Cooldown: "10m", Kinds: []AlertKind{{Kind: "stall", Description: "fan stopped"}}}, recent: []AlertRecord{{TS: 1789490000, Kind: "stall", Msg: "hdd stalled"}}}
	db := &fakeDashboard{sensors: []string{"hwmon:amdgpu:temp1"}}
	e.withDeps(t, auth, func(d *Deps) {
		d.SessionFile = filepath.Join(t.TempDir(), "sessions.json")
		d.Account = acc
		d.Alerts = al
		d.Dashboard = db
		d.Presets = &fakePresetDeleter{fakePresets: fakePresets{list: []Preset{{Name: "quiet", Channels: []string{"cpu"}}, {Name: "n5pro-balanced", Builtin: true, Channels: []string{"cpu"}}}}}
		d.About = About{Name: "n5-fangov", Version: "0.3.0-beta.1", Prerelease: "beta.1", License: "GPL-2.0-only", Credits: []Credit{{Name: "x", URL: "https://example.invalid", Note: "n"}}}
	})
	return e, acc, al, db
}

// login signs in and returns the request headers for later calls (cookie +
// CSRF) and the raw cookie.
func (e *env) login(t *testing.T, user, pass string, remember bool) (map[string]string, *http.Cookie, resp) {
	t.Helper()
	r := e.do(t, "POST", "/api/login", fmt.Sprintf(`{"user":%q,"password":%q,"remember":%v}`, user, pass, remember), csrf)
	if r.code != 200 {
		return nil, nil, r
	}
	ck := sessionCookieOf(t, r)
	return map[string]string{CSRFHeader: "1", "Cookie": ck.Name + "=" + ck.Value}, ck, r
}

func sessionCookieOf(t *testing.T, r resp) *http.Cookie {
	t.Helper()
	for _, ck := range (&http.Response{Header: r.hdr}).Cookies() {
		if ck.Name == sessionCookie {
			return ck
		}
	}
	t.Fatalf("no %s cookie in %v", sessionCookie, r.hdr.Values("Set-Cookie"))
	return nil
}

func (e *env) logLines() string {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	return strings.Join(e.logged, "\n")
}

func keys(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	decode(t, body, &m)
	return m
}

// serveAs runs one request through the TCP handler with a chosen remote
// address (the httptest server always connects from 127.0.0.1).
func serveAs(e *env, remote, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:8010"+path, strings.NewReader(body))
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
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
	if e.svc.histSpan != 120*time.Minute {
		t.Errorf("default span = %v", e.svc.histSpan)
	}
	var pts []map[string]any
	decode(t, r.body, &pts)
	if len(pts) != 2 || pts[1]["temp"].(map[string]any)["cpu"].(float64) != 36.0 {
		t.Errorf("history = %v", pts)
	}
	r = e.do(t, "GET", "/api/history?minutes=30", "", nil)
	wantCode(t, r, 200)
	if e.svc.histSpan != 30*time.Minute {
		t.Errorf("span = %v", e.svc.histSpan)
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

// TestHistorySince: the UI polls incrementally with since=<last ts>; only
// strictly newer points come back.
func TestHistorySince(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "GET", "/api/history?minutes=120&since=1789499990", "", nil)
	wantCode(t, r, 200)
	var pts []map[string]any
	decode(t, r.body, &pts)
	if len(pts) != 1 || pts[0]["ts"].(float64) != 1789500000 {
		t.Fatalf("since filter: %v", pts)
	}
	r = e.do(t, "GET", "/api/history?since=1789500000", "", nil)
	if strings.TrimSpace(r.body) != "[]" {
		t.Errorf("nothing newer → %q", r.body)
	}
	r = e.do(t, "GET", "/api/history?since=0", "", nil)
	decode(t, r.body, &pts)
	if len(pts) != 2 {
		t.Errorf("since=0 → %d points", len(pts))
	}
	wantError(t, e.do(t, "GET", "/api/history?since=-1", "", nil), 400, "since")
	wantError(t, e.do(t, "GET", "/api/history?since=x", "", nil), 400, "since")
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
	decode(t, r.body, &m)
	if m["mode"] != "auto" {
		t.Errorf("mode after clear = %v", m["mode"])
	}
}

// TestOverrideModeFromSnapshot: the response reports what the channel
// is doing, not a constant "manual" — a critical channel stays critical.
func TestOverrideModeFromSnapshot(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.svc.fixedMode["cpu"] = control.ModeCritical
	r := e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, csrf)
	wantCode(t, r, 200)
	var m map[string]any
	decode(t, r.body, &m)
	if m["mode"] != "critical" {
		t.Fatalf("mode = %v, want critical", m["mode"])
	}
	r = e.do(t, "DELETE", "/api/override/cpu", "", csrf)
	decode(t, r.body, &m)
	if m["mode"] != "critical" {
		t.Fatalf("mode after delete = %v, want critical", m["mode"])
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

// TestBodyTooLarge: oversized bodies answer 413 on every body endpoint.
func TestBodyTooLarge(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	big := sampleTOML + "# " + strings.Repeat("x", maxBody) + "\n"
	wantError(t, e.do(t, "PUT", "/api/config", big, csrf), 413, "exceeds")
	if len(e.cfg.saved) != 0 || len(e.svc.reloaded) != 0 {
		t.Fatal("oversized config was saved or reloaded")
	}
	pad := `{"duty":10,"percent":` + strings.Repeat("1", maxOverrideBody) + `}`
	wantError(t, e.do(t, "PUT", "/api/override/cpu", pad, csrf), 413, "exceeds")
	if len(e.svc.overrides) != 0 {
		t.Fatal("oversized override was applied")
	}
	// Just under the limit is still fine.
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, csrf), 200)
}

func TestCSRFHeaderRequired(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, nil), 403, CSRFHeader)
	wantError(t, e.do(t, "DELETE", "/api/override/cpu", "", map[string]string{CSRFHeader: "yes"}), 403, CSRFHeader)
	wantError(t, e.do(t, "PUT", "/api/config", sampleTOML, nil), 403, CSRFHeader)
	wantError(t, e.do(t, "POST", "/api/presets/quiet/apply", "", nil), 403, CSRFHeader)
	wantError(t, e.do(t, "PUT", "/api/presets/quiet", "", nil), 403, CSRFHeader)
	if len(e.svc.overrides) != 0 || len(e.presets.applied) != 0 || len(e.presets.saved) != 0 || len(e.cfg.saved) != 0 {
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
	hash := fastHash("admin", "s3cret")
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: hash})
	// GET state open without auth.
	wantCode(t, e.do(t, "GET", "/api/state", "", nil), 200)
	// Write without auth → 401.
	r := e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, csrf)
	wantError(t, r, 401, "authentication")
	if r.hdr.Get("WWW-Authenticate") != "" {
		t.Error("unexpected WWW-Authenticate challenge (UI shows its own prompt)")
	}
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong")), 401, "authentication")
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("other", "s3cret")), 401, "authentication")
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, map[string]string{CSRFHeader: "1", "Authorization": "Bearer xyz"}), 401, "authentication")
	// CSRF checked before auth.
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, map[string]string{"Authorization": basicAuth("admin", "s3cret")["Authorization"]}), 403, CSRFHeader)
	if len(e.svc.overrides) != 0 {
		t.Fatal("override set without valid auth")
	}
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "s3cret")), 200)
	if e.svc.overrides["cpu"] != 10 {
		t.Fatal("override not set with valid auth")
	}
	// Mode is case-insensitive; misconfigured hash fails closed.
	e2 := newEnv(t, AuthConfig{Mode: "Basic", User: "admin", PasswordHash: "nothex"})
	wantError(t, e2.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "s3cret")), 401, "authentication")
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

// TestAuthRequiredOnAllWrites: every state-changing endpoint answers 401
// without credentials when auth = basic, and nothing changes.
func TestAuthRequiredOnAllWrites(t *testing.T) {
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: fastHash("admin", "pw")})
	wantError(t, e.do(t, "DELETE", "/api/override/hdd", "", csrf), 401, "authentication")
	wantError(t, e.do(t, "PUT", "/api/config", sampleTOML, csrf), 401, "authentication")
	wantError(t, e.do(t, "POST", "/api/presets/quiet/apply", "", csrf), 401, "authentication")
	wantError(t, e.do(t, "PUT", "/api/presets/quiet", "", csrf), 401, "authentication")
	if len(e.svc.cleared) != 0 || len(e.cfg.saved) != 0 || len(e.svc.reloaded) != 0 || len(e.presets.applied) != 0 || len(e.presets.saved) != 0 {
		t.Fatal("state changed without auth")
	}
	ok := basicAuth("admin", "pw")
	wantCode(t, e.do(t, "DELETE", "/api/override/hdd", "", ok), 200)
	wantCode(t, e.do(t, "PUT", "/api/config", sampleTOML, ok), 200)
	wantCode(t, e.do(t, "POST", "/api/presets/quiet/apply", "", ok), 200)
	wantCode(t, e.do(t, "PUT", "/api/presets/quiet", "", ok), 200)
}

// TestProtectedReadsNeedAuth (v0.3 visibility model): with auth = basic
// every read that is not public needs credentials; state/history stay open
// (reduced) for dashboards, the UI files and version/about/session too.
func TestProtectedReadsNeedAuth(t *testing.T) {
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: fastHash("admin", "pw")})
	wantError(t, e.do(t, "GET", "/api/config", "", nil), 401, "authentication")
	wantError(t, e.do(t, "GET", "/api/log", "", nil), 401, "authentication")
	wantError(t, e.do(t, "GET", "/api/presets", "", nil), 401, "authentication")
	wantError(t, e.do(t, "GET", "/api/sensors", "", nil), 401, "authentication")
	wantError(t, e.do(t, "GET", "/api/profiles", "", nil), 401, "authentication")
	wantCode(t, e.do(t, "GET", "/api/state", "", nil), 200)
	wantCode(t, e.do(t, "GET", "/api/history", "", nil), 200)
	wantCode(t, e.do(t, "GET", "/api/version", "", nil), 200)
	wantCode(t, e.do(t, "GET", "/api/about", "", nil), 200)
	wantCode(t, e.do(t, "GET", "/api/session", "", nil), 200)
	wantCode(t, e.do(t, "GET", "/", "", nil), 200)
	wantCode(t, e.do(t, "GET", "/app.js", "", nil), 200)
	ok := basicAuth("admin", "pw")
	wantCode(t, e.do(t, "GET", "/api/config", "", ok), 200)
	wantCode(t, e.do(t, "GET", "/api/log", "", ok), 200)
	wantCode(t, e.do(t, "GET", "/api/presets", "", ok), 200)
	// auth = none: everything readable.
	e2 := newEnv(t, AuthConfig{})
	wantCode(t, e2.do(t, "GET", "/api/config", "", nil), 200)
	wantCode(t, e2.do(t, "GET", "/api/log", "", nil), 200)
}

// TestConfigHashRedaction: the hash never leaves the daemon; a PUT that
// carries the placeholder keeps the stored hash, a real value replaces it.
func TestConfigHashRedaction(t *testing.T) {
	hash := fastHash("admin", "pw")
	withHash := strings.Replace(sampleTOML, "[web]\n", "[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \""+hash+"\"\n", 1)
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: hash})
	pc := &fakeParsedConfig{fakeConfig{raw: []byte(withHash)}}
	e.cfg = &pc.fakeConfig
	d := e.deps(AuthConfig{Mode: "basic", User: "admin", PasswordHash: hash})
	d.Config = pc
	e.srv = New(d)
	e.ts.Close()
	e.ts = httptest.NewServer(e.srv.Handler())
	defer e.ts.Close()
	ok := basicAuth("admin", "pw")

	r := e.do(t, "GET", "/api/config", "", ok)
	wantCode(t, r, 200)
	if strings.Contains(r.body, hash) {
		t.Fatalf("hash leaked: %s", r.body)
	}
	var m map[string]any
	decode(t, r.body, &m)
	raw := m["raw"].(string)
	if !strings.Contains(raw, `password_hash = "`+RedactedHash+`"`) {
		t.Fatalf("raw not redacted: %s", raw)
	}
	if m["config"].(map[string]any)["web"].(map[string]any)["password_hash"] != RedactedHash {
		t.Fatalf("parsed not redacted: %v", m["config"])
	}
	// Socket handler redacts as well (one code path).
	sock := httptest.NewServer(e.srv.SocketHandler())
	defer sock.Close()
	res, err := http.Get(sock.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(b), hash) {
		t.Fatalf("hash leaked over socket: %s", b)
	}

	// PUT the redacted text back (what the UI does): stored hash survives.
	edited := strings.Replace(raw, "critical = 88", "critical = 90", 1)
	wantCode(t, e.do(t, "PUT", "/api/config", edited, ok), 200)
	saved := string(pc.saved[len(pc.saved)-1])
	if !strings.Contains(saved, `password_hash = "`+hash+`"`) || strings.Contains(saved, RedactedHash) || !strings.Contains(saved, "critical = 90") {
		t.Fatalf("placeholder not substituted: %s", saved)
	}
	if string(e.svc.reloaded[len(e.svc.reloaded)-1]) != saved {
		t.Fatal("reload got a different text than the file")
	}
	// A new real hash is written as given.
	newHash := fastHash("admin", "other")
	wantCode(t, e.do(t, "PUT", "/api/config", strings.Replace(raw, RedactedHash, newHash, 1), ok), 200)
	if !strings.Contains(string(pc.saved[len(pc.saved)-1]), newHash) {
		t.Fatal("new hash not written")
	}
	// Empty hash is not redacted (nothing to hide) and round-trips as empty.
	pc.raw = []byte(strings.Replace(sampleTOML, "[web]\n", "[web]\npassword_hash = \"\"\n", 1))
	r = e.do(t, "GET", "/api/config", "", ok)
	wantCode(t, r, 200)
	if strings.Contains(r.body, RedactedHash) {
		t.Fatalf("empty hash redacted: %s", r.body)
	}
	if RedactRaw("password_hash = \"abc\" # x\n  password_hash=\"def\"\nother = \"abc\"\n") != "password_hash = \"<unchanged>\" # x\n  password_hash=\"<unchanged>\"\nother = \"abc\"\n" {
		t.Fatal("RedactRaw shape")
	}
}

// TestRedactRawForms: single-quoted literals, tabs/odd spacing,
// the dotted key web.password_hash and an inline table are all redacted;
// the PUT placeholder substitution keeps the quote style.
func TestRedactRawForms(t *testing.T) {
	hash := fastHash("admin", "pw")
	cases := map[string]string{
		"double":   "[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"" + hash + "\"\n",
		"single":   "[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = '" + hash + "'\n",
		"spacing":  "[web]\nauth = \"basic\"\nuser = \"admin\"\n\t password_hash\t=\t'" + hash + "'   # note\n",
		"dotted":   "web.auth = \"basic\"\nweb.user = \"admin\"\nweb.password_hash = \"" + hash + "\"\n",
		"inline":   "web = { auth = \"basic\", user = \"admin\", password_hash = \"" + hash + "\" }\n",
		"nospaces": "[web]\npassword_hash=\"" + hash + "\"\n",
	}
	for name, src := range cases {
		out := RedactRaw(src)
		if strings.Contains(out, hash) {
			t.Errorf("%s: hash leaked: %s", name, out)
		}
		if !strings.Contains(out, RedactedHash) {
			t.Errorf("%s: placeholder missing: %s", name, out)
		}
		if got := currentHash(src); got != hash {
			t.Errorf("%s: currentHash = %q", name, got)
		}
	}
	// quote style survives redaction and the placeholder substitution
	if got := RedactRaw("password_hash = 'abc'\n"); got != "password_hash = '<unchanged>'\n" {
		t.Errorf("single-quote redaction: %q", got)
	}
	back := mapHashLiterals("web.password_hash\t= '<unchanged>'\npassword_hash = \"<unchanged>\"\npassword_hash = \"keep\"\n",
		func(v string) (string, bool) { return hash, v == RedactedHash })
	if back != "web.password_hash\t= '"+hash+"'\npassword_hash = \""+hash+"\"\npassword_hash = \"keep\"\n" {
		t.Errorf("placeholder substitution: %q", back)
	}
	// a short bogus hash is only touched in line form, never as a bare
	// substring (it would hit unrelated text)
	if got := RedactRaw("[web]\npassword_hash = \"ab\"\n[[channel]]\nname = \"ab\"\n"); got != "[web]\npassword_hash = \"<unchanged>\"\n[[channel]]\nname = \"ab\"\n" {
		t.Errorf("short hash: %q", got)
	}
	// unparsable text: the regex still covers the line forms
	if got := RedactRaw("[[[\npassword_hash = '" + hash + "'\n"); strings.Contains(got, hash) {
		t.Errorf("broken TOML leaked: %q", got)
	}
	if currentHash("[[[\npassword_hash = '"+hash+"'\n") != hash {
		t.Errorf("currentHash on broken TOML")
	}
	// PUT with a single-quoted placeholder restores the stored hash
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: hash})
	e.cfg.raw = []byte(cases["single"] + "\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45,85],[80,255]]\ncritical = 88\nstop = \"auto\"\n")
	ok := basicAuth("admin", "pw")
	body := strings.Replace(string(e.cfg.raw), hash, RedactedHash, 1)
	wantCode(t, e.do(t, "PUT", "/api/config", body, ok), 200)
	saved := string(e.cfg.saved[len(e.cfg.saved)-1])
	if !strings.Contains(saved, "password_hash = '"+hash+"'") || strings.Contains(saved, RedactedHash) {
		t.Errorf("single-quoted placeholder not substituted: %s", saved)
	}
}

// TestConfigValidateBeforeSave: a syntax error answers 400 with the
// warnings under "errors" and nothing is written or reloaded; field
// warnings do not block but are reported.
func TestConfigValidateBeforeSave(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.validate = func(raw []byte) ([]string, error) {
		if strings.Contains(string(raw), "[[[") {
			return []string{"toml: expected key"}, errors.New("toml: bare keys cannot contain '['")
		}
		if strings.Contains(string(raw), "critical = 999") {
			return []string{"channel.cpu.critical: 999 outside 81..150, default 90 used"}, nil
		}
		return nil, nil
	}
	r := e.do(t, "PUT", "/api/config", "[[[", csrf)
	wantCode(t, r, 400)
	var m map[string]any
	decode(t, r.body, &m)
	if !strings.Contains(m["error"].(string), "bare keys") || len(m["errors"].([]any)) != 1 {
		t.Fatalf("400 body = %v", m)
	}
	if len(e.cfg.saved) != 0 || len(e.svc.reloaded) != 0 {
		t.Fatal("invalid config reached Save or Reload")
	}
	r = e.do(t, "PUT", "/api/config", strings.Replace(sampleTOML, "critical = 88", "critical = 999", 1), csrf)
	wantCode(t, r, 200)
	decode(t, r.body, &m)
	if w := m["warnings"].([]any); len(w) != 1 || !strings.Contains(w[0].(string), "critical") {
		t.Fatalf("warnings = %v", m["warnings"])
	}
	if len(e.cfg.saved) != 1 || len(e.svc.reloaded) != 1 {
		t.Fatal("config with warnings must be saved and reloaded")
	}
	r = e.do(t, "PUT", "/api/config", sampleTOML, csrf)
	decode(t, r.body, &m)
	if w, ok := m["warnings"].([]any); !ok || len(w) != 0 {
		t.Fatalf("clean config warnings = %v", m["warnings"])
	}
}

// TestHostHeader: DNS rebinding sends a foreign Host; only IP literals,
// localhost and configured names are served.
func TestHostHeader(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	for _, h := range []string{"evil.example", "evil.example:8010", "n5host.lan.evil", "fans.example.evil:8010", "127.0.0.1.evil"} {
		r := e.do(t, "GET", "/api/state", "", map[string]string{"Host": h})
		wantError(t, r, 421, "host header")
		r = e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, map[string]string{"Host": h, CSRFHeader: "1"})
		wantCode(t, r, 421)
	}
	if len(e.svc.overrides) != 0 {
		t.Fatal("override applied with foreign Host")
	}
	// An absent Host (HTTP/1.0) cannot be sent through the Go client; check
	// the predicate directly.
	if e.srv.hostAllowed("") || e.srv.hostAllowed(":8010") || e.srv.hostAllowed("evil.example") {
		t.Fatal("hostAllowed accepts empty or foreign host")
	}
	for _, h := range []string{"127.0.0.1", "127.0.0.1:8010", "198.51.100.20:8010", "[::1]:8010", "[fd00::1]", "localhost", "LOCALHOST:8010", "n5host.lan", "N5HOST.LAN:8010", "n5host.lan.", "fans.example", "fans.example:443"} {
		wantCode(t, e.do(t, "GET", "/api/version", "", map[string]string{"Host": h}), 200)
	}
	// Default httptest client (Host = 127.0.0.1:port) passes.
	wantCode(t, e.do(t, "GET", "/api/version", "", nil), 200)
	// Socket handler has no Host check (ipc.Client uses http://n5-fangov/).
	sock := httptest.NewServer(e.srv.SocketHandler())
	defer sock.Close()
	req, _ := http.NewRequest("GET", sock.URL+"/api/version", nil)
	req.Host = "n5-fangov"
	res, err := sock.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("socket with Host n5-fangov = %d", res.StatusCode)
	}
	// "*" disables the check.
	d := e.deps(AuthConfig{})
	d.AllowedHosts = []string{"*"}
	wild := httptest.NewServer(New(d).Handler())
	defer wild.Close()
	req, _ = http.NewRequest("GET", wild.URL+"/api/version", nil)
	req.Host = "whatever.example"
	res, err = wild.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("wildcard allowed_hosts = %d", res.StatusCode)
	}
}

// TestAuthRateLimit: repeated failures from one IP are delayed with a
// growing back-off, logged, and reset by a success.
func TestAuthRateLimit(t *testing.T) {
	// legacy hash: cheap to verify, the limiter is what is under test
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: LegacyPasswordHash("admin", "pw")})
	now := time.Unix(1789500000, 0)
	var slept []time.Duration
	var mu sync.Mutex
	e.srv.limiter.now = func() time.Time { return now }
	e.srv.limiter.sleep = func(d time.Duration) { mu.Lock(); slept = append(slept, d); mu.Unlock() }
	for i := 0; i < 9; i++ {
		wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong")), 401)
	}
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}
	mu.Lock()
	got := append([]time.Duration(nil), slept...)
	mu.Unlock()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("delays = %v, want %v", got, want)
	}
	// Capped at 2 s.
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong")), 401)
	mu.Lock()
	last := slept[len(slept)-1]
	mu.Unlock()
	if last != 2*time.Second {
		t.Fatalf("cap = %v", last)
	}
	e.logMu.Lock()
	nlog := len(e.logged)
	sample := ""
	if nlog > 0 {
		sample = e.logged[nlog-1]
	}
	e.logMu.Unlock()
	if nlog != 10 || !strings.Contains(sample, "auth failure") || !strings.Contains(sample, `user "admin"`) || strings.Contains(sample, "wrong") {
		t.Fatalf("log = %d entries, last %q", nlog, sample)
	}
	// Success resets: the next failure is immediate again.
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")), 200)
	mu.Lock()
	n := len(slept)
	mu.Unlock()
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong")), 401)
	mu.Lock()
	if len(slept) != n {
		t.Fatalf("delay after reset: %v", slept[n:])
	}
	mu.Unlock()
	// Quiet period resets too.
	for i := 0; i < 6; i++ {
		e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong"))
	}
	now = now.Add(limitReset + time.Second)
	mu.Lock()
	n = len(slept)
	mu.Unlock()
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong")), 401)
	mu.Lock()
	if len(slept) != n {
		t.Fatalf("delay after quiet period: %v", slept[n:])
	}
	mu.Unlock()
	// Table stays bounded.
	l := newAuthLimiter()
	l.sleep = func(time.Duration) {}
	for i := 0; i < limitEntries+100; i++ {
		l.fail(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	if len(l.byIP) > limitEntries {
		t.Fatalf("limiter table grew to %d", len(l.byIP))
	}
	if delayFor(limitFree+1) != limitBase || delayFor(100) != limitMax || delayFor(0) != 0 {
		t.Fatal("delayFor shape")
	}
}

func TestAuthorizedConstantTime(t *testing.T) {
	s := New(Deps{Auth: AuthConfig{Mode: "basic", User: "admin", PasswordHash: fastHash("admin", "pw")}})
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

// M3b: the stored hash may be the legacy sha256("user:password") hex or
// the salted PBKDF2 form; both verify, wrong password/user fail on both,
// and a stored value that parses as neither fails closed.
func TestVerifyPasswordFormats(t *testing.T) {
	legacy := LegacyPasswordHash("admin", "pw")
	modern := PasswordHash("admin", "pw")
	if len(legacy) != 64 || !strings.HasPrefix(modern, "pbkdf2$210000$") {
		t.Fatalf("forms: %q %q", legacy, modern)
	}
	if PasswordHash("admin", "pw") == modern {
		t.Fatal("PBKDF2 hash is not salted (two calls agree)")
	}
	for name, stored := range map[string]string{"legacy": legacy, "pbkdf2": modern} {
		if !VerifyPassword("admin", "pw", stored) {
			t.Errorf("%s: valid password rejected", name)
		}
		if VerifyPassword("admin", "pw2", stored) || VerifyPassword("admin", "", stored) {
			t.Errorf("%s: wrong password accepted", name)
		}
		e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: stored})
		wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")), 200)
		wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "PW")), 401, "authentication")
		wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("other", "pw")), 401, "authentication")
	}
	// the legacy form binds the user name into the digest: a legacy hash
	// for "other" does not verify "admin" even with the right password
	if VerifyPassword("admin", "pw", LegacyPasswordHash("other", "pw")) {
		t.Error("legacy hash of another user accepted")
	}
	for _, bad := range []string{"", "nothex", strings.Repeat("g", 64), "pbkdf2$210000$zz$" + strings.Repeat("ab", 32), "pbkdf2$1$" + strings.Repeat("0f", 16) + "$" + strings.Repeat("ab", 32)} {
		if VerifyPassword("admin", "pw", bad) {
			t.Errorf("stored %q verified", bad)
		}
	}
	// a hand-made PBKDF2 value with a different iteration count verifies too
	custom := "pbkdf2$1000$" + strings.Repeat("0f", 16) + "$"
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: custom + strings.Repeat("00", 32)})
	wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")), 401, "authentication")
}

// M3c: once limitConcurrent failed attempts of one IP are sleeping, further
// attempts from that IP get an immediate 429 — no sleep, no hash
// computation, no extra failure count. When the sleepers return, the IP is
// served again.
func TestAuthConcurrencyCap(t *testing.T) {
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: LegacyPasswordHash("admin", "pw")})
	release := make(chan struct{})
	var sleeping sync.WaitGroup
	e.srv.limiter.sleep = func(d time.Duration) {
		sleeping.Done()
		<-release
	}
	// use up the free attempts
	for i := 0; i < limitFree; i++ {
		wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong")), 401)
	}
	// limitConcurrent attempts park in the sleep
	sleeping.Add(limitConcurrent)
	codes := make(chan int, limitConcurrent)
	for i := 0; i < limitConcurrent; i++ {
		go func() {
			r, err := e.try("PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "wrong"))
			if err != nil {
				codes <- -1 // reported by the test goroutine below
				return
			}
			codes <- r.code
		}()
	}
	if !waitTimeout(&sleeping, 10*time.Second) {
		close(release)
		t.Fatal("the parked attempts never reached the limiter sleep")
	}
	if !e.srv.limiter.busy(remoteIPOf(e)) {
		t.Fatal("limiter not busy with all sleepers parked")
	}
	// the next one is refused at once, even with the right password
	r := e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw"))
	wantError(t, r, 429, "too many")
	if len(e.svc.overrides) != 0 {
		t.Fatal("override applied while the IP was throttled")
	}
	nlog := func() int {
		e.logMu.Lock()
		defer e.logMu.Unlock()
		return len(e.logged)
	}
	if n := nlog(); n != limitFree { // the parked ones log after their sleep
		t.Errorf("refused attempt logged as a failure: %d log lines", n)
	}
	// anonymous requests (no Authorization header) are not affected
	wantCode(t, e.do(t, "GET", "/api/state", "", nil), 200)
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, csrf), 401)
	close(release)
	for i := 0; i < limitConcurrent; i++ {
		if c := <-codes; c != 401 {
			t.Errorf("parked attempt answered %d", c)
		}
	}
	if n := nlog(); n != limitFree+limitConcurrent {
		t.Errorf("failure count after release: %d log lines, want %d", n, limitFree+limitConcurrent)
	}
	if e.srv.limiter.busy(remoteIPOf(e)) {
		t.Fatal("limiter still busy after the sleepers returned")
	}
	e.srv.limiter.sleep = func(time.Duration) {}
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth("admin", "pw")), 200)
}

// remoteIPOf is the client IP the test server sees for e.do.
func remoteIPOf(e *env) string {
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(e.ts.URL, "http://"))
	return host
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
	// PUT with an empty body saves the current curves.
	wantCode(t, e.do(t, "PUT", "/api/presets/night-1_0", "", csrf), 200)
	if len(e.presets.saved) != 1 || e.presets.saved[0] != "night-1_0" {
		t.Fatalf("saved = %v", e.presets.saved)
	}
	// Name rule equals config's ^[a-z0-9_-]{1,64}$.
	for _, bad := range []string{".hidden", "a%2Fb", "Night", "night.1", "a b", strings.Repeat("a", 65)} {
		wantError(t, e.do(t, "PUT", "/api/presets/"+bad, "", csrf), 400, "invalid preset name")
		wantError(t, e.do(t, "POST", "/api/presets/"+bad+"/apply", "", csrf), 400, "invalid preset name")
	}
	wantCode(t, e.do(t, "PUT", "/api/presets/"+strings.Repeat("a", 64), "", csrf), 200)
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
	var m struct {
		Lines  []string `json:"lines"`
		Source string   `json:"source"`
	}
	decode(t, r.body, &m)
	if len(m.Lines) != 3 || m.Source != "journal" {
		t.Fatalf("lines = %v source %q", m.Lines, m.Source)
	}
	r = e.do(t, "GET", "/api/log?lines=2", "", nil)
	decode(t, r.body, &m)
	if len(m.Lines) != 2 || m.Lines[0] != "line2" {
		t.Fatalf("lines=2 → %v", m.Lines)
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
	var v map[string]any
	decode(t, r.body, &v)
	if v["version"] != "1.2.3-test" || v["name"] != "n5-fangov" || v["tls"] != false {
		t.Fatalf("version = %v", v)
	}
	r = e.do(t, "GET", "/api/sensors", "", nil)
	wantCode(t, r, 200)
	var ss []map[string]any
	decode(t, r.body, &ss)
	if len(ss) != 2 || ss[0]["id"] != "k10temp" || ss[0]["temp"].(float64) != 38.2 {
		t.Fatalf("sensors = %v", ss)
	}
	if _, has := ss[1]["temp"]; has {
		t.Fatalf("pattern entry must not carry temp: %v", ss[1])
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
	if !strings.Contains(r.body, "<title>n5-fangov</title>") || !strings.Contains(r.body, `src="app.js"`) || !strings.Contains(r.body, `href="app.css"`) {
		t.Errorf("index.html content unexpected: %.200s", r.body)
	}
	if strings.Contains(r.body, "<script>") || strings.Contains(r.body, "onclick=") || strings.Contains(r.body, "style=") {
		t.Errorf("index.html carries inline script/style, which the CSP blocks")
	}
	if csp := r.hdr.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	r = e.do(t, "GET", "/app.js", "", nil)
	wantCode(t, r, 200)
	if !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/javascript") || !strings.Contains(r.body, "X-N5-Fangov-Csrf") {
		t.Errorf("app.js: %q %.100s", r.hdr.Get("Content-Type"), r.body)
	}
	if len(r.body) > 96*1024 {
		t.Errorf("app.js is %d bytes, budget 96 KB (DESIGN \"Dashboard\")", len(r.body))
	}
	// UI assumptions the server honours: since-polling, {"lines"} log wrapper,
	// "channel" key, "<unchanged>" hash placeholder passes through untouched,
	// v0.2 endpoints (log export/clear, settings export/import, tls flag),
	// 0.3.1 endpoints (history tiers + CSV, tokens, schedules, webhook alerts).
	for _, want := range []string{"since=", "b.lines", "cfg.channel", "warnings", "/api/log/export", "/api/config/export", "/api/config/import", "b.source", ".tls", "/api/tls/regenerate", "/api/tls/upload", "/api/tls/reset", "/api/tls/cert.", "force_required", "c.warnings", "c.fallback", "Math.ceil((new Date(iso)", "/api/system",
		"/api/history.csv?minutes=", "/api/tokens", "/api/schedules", "webhook_url", "webhook_format", "held_temp", "hold_until", "hysteresis", "min_on", "temp_c", "window.n5mock"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	// the mock is the fourth static file: same content type, its own budget, never referenced by index.html
	r = e.do(t, "GET", "/mock.js", "", nil)
	wantCode(t, r, 200)
	if !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/javascript") || !strings.Contains(r.body, "window.n5mock") {
		t.Errorf("mock.js: %q %.100s", r.hdr.Get("Content-Type"), r.body)
	}
	if len(r.body) > 40*1024 {
		t.Errorf("mock.js is %d bytes, budget 40 KB (DESIGN \"Dashboard\")", len(r.body))
	}
	if html, _ := staticFS.ReadFile("static/index.html"); strings.Contains(string(html), "mock.js") {
		t.Errorf("index.html references mock.js; the production page must never request it")
	}
	if js, _ := staticFS.ReadFile("static/app.js"); !strings.Contains(string(js), `src: 'mock.js'`) || !strings.Contains(string(js), "Q.get('mock') === '1'") {
		t.Errorf("app.js must insert mock.js only behind ?mock=1")
	}
	r = e.do(t, "GET", "/app.css", "", nil)
	wantCode(t, r, 200)
	if !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/css") {
		t.Errorf("app.css content-type %q", r.hdr.Get("Content-Type"))
	}
	wantCode(t, e.do(t, "GET", "/index.html", "", nil), 200)
	wantCode(t, e.do(t, "GET", "/other.txt", "", nil), 404)
	wantCode(t, e.do(t, "GET", "/static/app.js", "", nil), 404)
	wantCode(t, e.do(t, "GET", "/static/mock.js", "", nil), 404)
	wantCode(t, e.do(t, "GET", "/mock.js.map", "", nil), 404)
}

// addrListener reports a different address than the wrapped listener, to
// exercise the non-loopback warning without binding a LAN port in tests.
type addrListener struct {
	net.Listener
	addr net.Addr
}

func (l addrListener) Addr() net.Addr { return l.addr }

// TestServeWarnsNonLoopbackWithoutAuth: binding a LAN address with
// auth = none is allowed but logged loudly; basic auth or loopback are quiet.
func TestServeWarnsNonLoopbackWithoutAuth(t *testing.T) {
	run := func(auth AuthConfig, addr net.Addr) []string {
		e := newEnv(t, auth)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		var l net.Listener = ln
		if addr != nil {
			l = addrListener{ln, addr}
		}
		ctx, cancel := context.WithCancel(context.Background())
		errc := make(chan error, 1)
		go func() { errc <- e.srv.Serve(ctx, l) }()
		res, err := http.Get("http://" + ln.Addr().String() + "/api/version")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		cancel()
		<-errc
		e.logMu.Lock()
		defer e.logMu.Unlock()
		return append([]string(nil), e.logged...)
	}
	has := func(logs []string, s string) bool {
		for _, l := range logs {
			if strings.Contains(l, s) {
				return true
			}
		}
		return false
	}
	lan := &net.TCPAddr{IP: net.IPv4zero, Port: 8010}
	if logs := run(AuthConfig{}, lan); !has(logs, "non-loopback") || !has(logs, "without auth") {
		t.Errorf("no warning for 0.0.0.0 without auth: %v", logs)
	}
	if logs := run(AuthConfig{}, &net.TCPAddr{IP: net.ParseIP("198.51.100.20"), Port: 8010}); !has(logs, "non-loopback") {
		t.Errorf("no warning for LAN IP without auth: %v", logs)
	}
	if logs := run(AuthConfig{Mode: "basic", User: "a", PasswordHash: fastHash("a", "b")}, lan); has(logs, "non-loopback") {
		t.Errorf("warning although basic auth: %v", logs)
	}
	if logs := run(AuthConfig{}, nil); has(logs, "non-loopback") {
		t.Errorf("warning on loopback: %v", logs)
	}
}

// TestTabsHaveHandlers (AUDIT hoch #1): every role="tab" in index.html must
// have an entry in selectTab's dispatch map in app.js — a missing entry threw
// a TypeError on every switch to the Manual tab and, with ?tab=manual, kept
// the boot sequence from reaching schedule().
func TestTabsHaveHandlers(t *testing.T) {
	html, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	tabs := regexp.MustCompile(`role="tab" id="tab-([a-z]+)"`).FindAllStringSubmatch(string(html), -1)
	if len(tabs) < 5 {
		t.Fatalf("found only %d tabs in index.html", len(tabs))
	}
	// the dispatch map is the object literal that ends with `})[id]`
	i := strings.Index(string(js), "})[id]")
	if i < 0 {
		t.Fatal("dispatch map `})[id]` not found in app.js")
	}
	start := strings.LastIndex(string(js)[:i], "(({")
	if start < 0 {
		t.Fatal("dispatch map start `(({` not found in app.js")
	}
	m := string(js)[start:i]
	for _, tb := range tabs {
		if !regexp.MustCompile(`(?m)(^|[\s{,])` + tb[1] + `:`).MatchString(m) {
			t.Errorf("tab %q has no handler in selectTab's dispatch map", tb[1])
		}
	}
}

// TestPrimaryButtonContrast (AUDIT hoch 5): the primary button's text must
// keep WCAG AA contrast (4.5:1) on --info in both themes. The dark theme
// uses var(--bg) as text, the light theme white.
func TestPrimaryButtonContrast(t *testing.T) {
	css, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	s := string(css)
	if !strings.Contains(s, ".btn.primary{background:var(--info);border-color:var(--info);color:var(--bg)}") || !strings.Contains(s, ":root[data-theme=light] .btn.primary{color:#fff}") {
		t.Fatal("primary button rules changed; update this test with the new colours")
	}
	find := func(block, name string) string {
		m := regexp.MustCompile(name + `:(#[0-9a-fA-F]{6})`).FindStringSubmatch(block)
		if m == nil {
			t.Fatalf("%s not found", name)
		}
		return m[1]
	}
	dark := s[:strings.Index(s, ":root[data-theme=light]")]
	light := s[strings.Index(s, ":root[data-theme=light]"):]
	if c := contrast(find(dark, "--info"), find(dark, "--bg")); c < 4.5 {
		t.Errorf("dark primary button: %.2f:1 < 4.5", c)
	}
	if c := contrast(find(light, "--info"), "#ffffff"); c < 4.5 {
		t.Errorf("light primary button: %.2f:1 < 4.5", c)
	}
}

// contrast is the WCAG 2.x contrast ratio of two #rrggbb colours.
func contrast(a, b string) float64 {
	lum := func(hex string) float64 {
		var rgb [3]float64
		for i := 0; i < 3; i++ {
			v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
			c := float64(v) / 255
			if c <= 0.03928 {
				c /= 12.92
			} else {
				c = math.Pow((c+0.055)/1.055, 2.4)
			}
			rgb[i] = c
		}
		return 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2]
	}
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// ---- auth logging ------------------------------------------------------------

// TestAnonymous401Silent: a request without Authorization gets a 401 that is
// neither logged nor counted; a presented wrong credential is both.
func TestAnonymous401Silent(t *testing.T) {
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")})
	var slept []time.Duration
	var mu sync.Mutex
	e.srv.limiter.sleep = func(d time.Duration) { mu.Lock(); slept = append(slept, d); mu.Unlock() }
	nslept := func() int { mu.Lock(); defer mu.Unlock(); return len(slept) }
	for i := 0; i < limitFree+3; i++ {
		wantError(t, e.do(t, "GET", "/api/config", "", nil), 401, "authentication")
		wantError(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, csrf), 401, "authentication")
	}
	e.logMu.Lock()
	n := len(e.logged)
	e.logMu.Unlock()
	if n != 0 || nslept() != 0 {
		t.Fatalf("anonymous 401 logged %d times, delays %d", n, nslept())
	}
	e.srv.limiter.mu.Lock()
	entries := len(e.srv.limiter.byIP)
	e.srv.limiter.mu.Unlock()
	if entries != 0 {
		t.Fatalf("anonymous 401 counted: %d limiter entries", entries)
	}
	// a presented credential still counts and is logged
	wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "nope")), 401)
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, map[string]string{CSRFHeader: "1", "Authorization": "Bearer abc"}), 401)
	e.logMu.Lock()
	n = len(e.logged)
	last := ""
	if n > 0 {
		last = e.logged[n-1]
	}
	e.logMu.Unlock()
	if n != 2 || !strings.Contains(last, "auth failure") || !strings.Contains(last, "2 recent failures") {
		t.Fatalf("presented credential: %d log lines, last %q", n, last)
	}
	// and the login that follows the silent 401 works without any delay
	wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", "pw")), 200)
	if nslept() != 0 {
		t.Fatalf("delays after two failures: %d", nslept())
	}
}

// TestStoresNotImplementedWithoutDeps: nil stores → 501 on every new endpoint;
// a preset store without Delete → 501.
func TestStoresNotImplementedWithoutDeps(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/account", ""},
		{"POST", "/api/account/password", `{"current_password":"pw","new_password":"longenough"}`},
		{"POST", "/api/account/user", `{"current_password":"pw","user":"x"}`},
		{"POST", "/api/account/sessions/revoke", `{"others":true}`},
		{"GET", "/api/alerts", ""},
		{"PUT", "/api/alerts", `{"transport":"log","mail_to":"root"}`},
		{"POST", "/api/alerts/test", ""},
		{"POST", "/api/alerts/template", ""},
		{"GET", "/api/dashboard", ""},
		{"PUT", "/api/dashboard", `{"sensors":[]}`},
		{"DELETE", "/api/presets/quiet", ""},
		{"GET", "/api/system", ""},
	} {
		if r := e.do(t, c.method, c.path, c.body, csrf); r.code != 501 {
			t.Errorf("%s %s = %d %s, want 501", c.method, c.path, r.code, r.body)
		}
	}
}

// TestQuerySemicolonNotLogged: a request with ';' in the query
// produces no ErrorLog line on the daemon's server (through the
// handshake filter into Logf) nor on a stock server around the socket
// handler, and ';' acts as a separator. (Go 1.25 no longer logs the
// pre-1.25 warning itself; the wrapper keeps that explicit.)
func TestQuerySemicolonNotLogged(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- e.srv.Serve(ctx, ln) }()
	for _, p := range []string{"/?a;b", "/api/version?x;y=1", "/api/history?minutes=5;since=0"} {
		res, err := http.Get("http://" + ln.Addr().String() + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
	// a ';'-separated pair is parsed: minutes=99999 is out of range → 400
	res, err := http.Get("http://" + ln.Addr().String() + "/api/history?since=0;minutes=99999")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Errorf("';' not treated as a separator: %d", res.StatusCode)
	}
	cancel()
	select {
	case <-errc:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
	if l := e.logLines(); strings.Contains(l, "semicolon") || strings.Contains(l, "http:") {
		t.Errorf("server error line reached the log: %q", l)
	}
	var buf bytes.Buffer
	ts := httptest.NewUnstartedServer(e.srv.SocketHandler())
	ts.Config.ErrorLog = log.New(&buf, "", 0)
	ts.Start()
	defer ts.Close()
	res, err = http.Get(ts.URL + "/?a;b")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if buf.Len() != 0 {
		t.Errorf("socket handler logged: %q", buf.String())
	}
}

// TestAuthLogUserTruncated: a kilobyte user name in a failed login or
// basic credential reaches the log as 64 characters, not as a flood.
func TestAuthLogUserTruncated(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
	e.srv.limiter.sleep = func(time.Duration) {}
	long := strings.Repeat("u", 3000)
	wantCode(t, e.do(t, "POST", "/api/login", fmt.Sprintf(`{"user":%q,"password":"x"}`, long), csrf), 401)
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":10}`, basicAuth(long, "x")), 401)
	e.logMu.Lock()
	defer e.logMu.Unlock()
	if len(e.logged) != 2 {
		t.Fatalf("log lines = %d", len(e.logged))
	}
	for _, l := range e.logged {
		if len(l) > 400 || !strings.Contains(l, `user "`+strings.Repeat("u", 64)+`"`) {
			t.Errorf("line not truncated: %d bytes: %.120s", len(l), l)
		}
	}
}

// TestVersionLimits: GET /api/version carries the bounds the dashboard
// validates against, taken from the server's constants.
func TestVersionLimits(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "GET", "/api/version", "", nil)
	wantCode(t, r, 200)
	var v struct {
		Limits Limits `json:"limits"`
	}
	decode(t, r.body, &v)
	want := Limits{MinHDDOverride: control.MinHDDOverride, CriticalMin: 30, CriticalMax: config.MaxCritical, CurvePointsMax: config.MaxCurvePts, DashboardSensorsMax: config.MaxDashboardSensors, PasswordMin: config.MinPasswordLen, PasswordMax: config.MaxPasswordLen, HysteresisMax: config.HysteresisMax, MinOnMaxS: 3600}
	if v.Limits != want {
		t.Fatalf("limits = %+v, want %+v", v.Limits, want)
	}
	for _, k := range []string{`"min_hdd_override":60`, `"critical_min":30`, `"critical_max":150`, `"curve_points_max":8`, `"dashboard_sensors_max":8`, `"password_min":8`, `"password_max":128`, `"hysteresis_max":10`, `"min_on_max_s":3600`} {
		if !strings.Contains(r.body, k) {
			t.Errorf("missing %s in %s", k, r.body)
		}
	}
}

// TestMethodNotAllowedJSON: a wrong method on an API path answers the JSON
// error document (with the mux's Allow header) on TCP and on the socket.
func TestMethodNotAllowedJSON(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	r := e.do(t, "POST", "/api/state", "", csrf)
	wantError(t, r, 405, "method not allowed")
	if ct := r.hdr.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type %q", ct)
	}
	if r.hdr.Get("Allow") == "" {
		t.Errorf("Allow header missing")
	}
	sock := httptest.NewServer(e.srv.SocketHandler())
	defer sock.Close()
	req, _ := http.NewRequest("DELETE", sock.URL+"/api/version", nil)
	res, err := sock.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 405 || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Errorf("socket 405: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	// the static side is untouched
	wantCode(t, e.do(t, "GET", "/", "", nil), 200)
}

// TestStoreErrorsAre500: a write failure of a store (read-only file
// system, permission) answers 500; a validation refusal stays 400.
func TestStoreErrorsAre500(t *testing.T) {
	e, _, al, db := storesEnv(t, AuthConfig{})
	ro := &fs.PathError{Op: "rename", Path: "/etc/n5-fangov/config.toml", Err: syscall.EROFS}
	e.cfg.saveErr = ro
	wantError(t, e.do(t, "PUT", "/api/config", sampleTOML, csrf), 500, "config not written")
	e.cfg.saveErr = errors.New("toml: line 3: expected key")
	wantError(t, e.do(t, "PUT", "/api/config", sampleTOML, csrf), 400, "config rejected")
	e.cfg.saveErr = nil

	al.configErr = fmt.Errorf("write %s: %w", "/etc/n5-fangov/config.toml", fs.ErrPermission)
	wantCode(t, e.do(t, "PUT", "/api/alerts", `{"transport":"log","mail_to":"root"}`, csrf), 500)
	al.configErr = errors.New(`transport "nope" unknown`)
	wantCode(t, e.do(t, "PUT", "/api/alerts", `{"transport":"nope","mail_to":"root"}`, csrf), 400)

	db.err = fmt.Errorf("write: %w", syscall.ENOSPC)
	wantCode(t, e.do(t, "PUT", "/api/dashboard", `{"sensors":["ec:x"]}`, csrf), 500)
	db.err = errors.New("sensor id \"x\" is not valid")
	wantCode(t, e.do(t, "PUT", "/api/dashboard", `{"sensors":["x"]}`, csrf), 400)

	ps := &fakePresets{list: []Preset{}, saveErr: &fs.PathError{Op: "open", Path: "/etc/n5-fangov/presets/x.toml", Err: syscall.EACCES}}
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Presets = ps })
	wantCode(t, e.do(t, "PUT", "/api/presets/x", "", csrf), 500)
	ps.saveErr = errors.New("current config has no channels to save")
	wantCode(t, e.do(t, "PUT", "/api/presets/x", "", csrf), 400)
	ps.applyErr = fmt.Errorf("current config: %w", ErrStore)
	wantCode(t, e.do(t, "POST", "/api/presets/x/apply", "", csrf), 500)
	if !isStoreError(&fs.PathError{Err: syscall.EROFS}) || isStoreError(errors.New("no")) || !isStoreError(fmt.Errorf("x: %w", ErrStore)) {
		t.Error("isStoreError classification")
	}
}
