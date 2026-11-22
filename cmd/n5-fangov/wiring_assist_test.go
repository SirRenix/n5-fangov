package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/sensor"
	"github.com/SirRenix/n5-fangov/internal/web"
)

const assistConfig = `# hand-edited
[daemon]
interval = "10s"

[[channel]]
name = "cpu"
pwm = 1
sensor = "k10temp"
curve = [[45,85],[80,255]]
critical = 88
stop = "auto"

[[channel]]
name = "disks"
pwm = 3
sensor = "drivetemp:max"
curve = [[36,105],[46,255]]
critical = 56

# raise the disk fans with the GPU
[[assist]]
channel = "disks"
enabled = false   # switch
min_on = "2m"

[[assist.source]]
sensor = "hwmon:amdgpu:temp1"
start = 50
full = 58
boost = 45
`

type fakeAssistCtrl struct {
	mu    sync.Mutex
	calls []string
	n     int
	err   error
}

// ApplyAssists records the wanted channel's switch as it was handed over
// ("disks=on") and the number of tables.
func (f *fakeAssistCtrl) ApplyAssists(tables []config.Assist, want string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	on := "missing"
	for _, a := range tables {
		if a.Channel == want {
			on = map[bool]string{true: "on", false: "off"}[a.Enabled]
		}
	}
	f.calls = append(f.calls, want+"="+on)
	f.n = len(tables)
	return f.err
}

func writeAssistConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(assistConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAssistStore(t *testing.T) {
	cfgPath := writeAssistConfig(t)
	ctrl := &fakeAssistCtrl{}
	s := newAssistStore(cfgPath, ctrl, nil)
	if err := s.SetEnabled("disks", true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfgPath)
	if want := strings.Replace(assistConfig, "enabled = false   # switch", "enabled = true   # switch", 1); string(got) != want {
		t.Errorf("file:\n%s", got)
	}
	if len(ctrl.calls) != 1 || ctrl.calls[0] != "disks=on" {
		t.Errorf("controller calls %v", ctrl.calls)
	}
	if err := s.SetEnabled("disks", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != assistConfig {
		t.Errorf("off again:\n%s", got)
	}
	// a channel without a table: fs.ErrNotExist (404), nothing written
	if err := s.SetEnabled("cpu", true); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cpu: %v", err)
	}
	// written, but the table does not fit the running channels: ReloadError (500)
	ctrl.err = control.ErrNoAssist
	err := s.SetEnabled("disks", true)
	var re *web.ReloadError
	if !errors.As(err, &re) {
		t.Errorf("not applied: %v", err)
	}
	if cfg, _, _ := config.Load(cfgPath); !cfg.Assist("disks").Enabled {
		t.Error("file not written before the apply failed")
	}
}

// TestAssistStoreHandEditedTable (BACKLOG #63): a second table added to
// the file by hand. The switch hands the file's tables to the controller
// (not only its own switch), so the new table takes effect with it; the
// controller side (added, changed, removed, holds) is
// control.TestApplyAssistsHandEdited.
func TestAssistStoreHandEditedTable(t *testing.T) {
	cfgPath := writeAssistConfig(t)
	extra := "\n[[assist]]\nchannel = \"cpu\"\nenabled = true\n\n[[assist.source]]\nsensor = \"drivetemp:max\"\nstart = 40\nfull = 50\nboost = 10\n"
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(extra)
	_ = f.Close()
	ctrl := &fakeAssistCtrl{}
	if err := newAssistStore(cfgPath, ctrl, nil).SetEnabled("disks", true); err != nil {
		t.Fatalf("switch after a hand edit: %v", err)
	}
	if len(ctrl.calls) != 1 || ctrl.calls[0] != "disks=on" || ctrl.n != 2 {
		t.Errorf("handed over %v, %d tables; want disks=on and both tables", ctrl.calls, ctrl.n)
	}
}

// TestAssistStoreBrokenFile: a file with a syntax error is reported as
// such (400 through storeStatus, like the other single-key stores), not as
// "no table"; nothing is written, the daemon is not touched.
func TestAssistStoreBrokenFile(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	broken := "[daemon\n" + assistConfig
	if err := os.WriteFile(cfgPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	ctrl := &fakeAssistCtrl{}
	err := newAssistStore(cfgPath, ctrl, nil).SetEnabled("disks", true)
	if err == nil || errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("broken file: %v", err)
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != broken || len(ctrl.calls) != 0 {
		t.Errorf("written or applied: %d calls", len(ctrl.calls))
	}
}

// interleavingService runs between a writer's file write and its reload
// (the window in which the assist switch could slip in), then delegates.
type interleavingService struct {
	control.Service
	between func()
	next    control.Service
}

func (s interleavingService) Reload(raw []byte) error {
	if s.between != nil {
		s.between()
	}
	return s.next.Reload(raw)
}

// TestPresetApplyVsAssistSwitch: a preset apply writes the file (assist
// off), the switch turns assist on before the apply's reload runs, then the
// reload goes ahead with the text the apply wrote. The reload hook re-reads
// the file under the lock, so the daemon gets "on" like the file — not the
// stale "off" (deterministic: the switch runs inside Reload, before the
// hook).
func TestPresetApplyVsAssistSwitch(t *testing.T) {
	cfgPath := writeAssistConfig(t)
	inner := &fakeService{}
	ctrl := &fakeAssistCtrl{}
	store := newAssistStore(cfgPath, ctrl, nil)
	svc := interleavingService{
		between: func() {
			if err := store.SetEnabled("disks", true); err != nil {
				t.Errorf("switch: %v", err)
			}
		},
		next: hookedService{Service: inner, cfgPath: cfgPath},
	}
	p := dirPresetStore{dir: filepath.Join(t.TempDir(), "presets"), cfgPath: cfgPath, svc: svc, profile: "n5pro"}
	if err := p.Apply("n5pro-quiet"); err != nil {
		t.Fatal(err)
	}
	if inner.reloads() != 1 {
		t.Fatalf("%d reloads", inner.reloads())
	}
	cfg, _, err := config.Parse(inner.raws[0])
	if err != nil || cfg.Assist("disks") == nil || !cfg.Assist("disks").Enabled || cfg.Channel("disks").Critical != 66 {
		t.Errorf("the reload applied stale text: %v %+v", err, cfg.Assists)
	}
	file, _, _ := config.Load(cfgPath)
	if !file.Assist("disks").Enabled {
		t.Error("file lost the switch")
	}
	// without a path the hook reloads what it is given (tests, no file)
	inner2 := &fakeService{}
	_ = hookedService{Service: inner2}.Reload([]byte("x"))
	if string(inner2.raws[0]) != "x" {
		t.Errorf("no path: %q", inner2.raws[0])
	}
}

// TestSetupKeepsOnlyFittingAssist: setup carries an [[assist]] table over
// only when it still fits the new channel set and names the others as
// dropped (they are not written).
func TestSetupKeepsOnlyFittingAssist(t *testing.T) {
	prior := assistConfig + "\n[[channel]]\nname = \"ssd\"\npwm = 2\nsensor = \"nvme:max\"\ncurve = [[40,74],[70,255]]\ncritical = 75\n\n" +
		"[[assist]]\nchannel = \"ssd\"\n[[assist.source]]\nsensor = \"k10temp\"\nstart = 60\nfull = 70\nboost = 10\n"
	if _, warns, err := config.Parse([]byte(prior)); err != nil || len(warns) != 0 {
		t.Fatalf("prior: %v %v", err, warns)
	}
	raw, r := renderConfigKeep([]byte(prior), "n5pro", n5proChannels(), webSpec{Listen: "127.0.0.1:8010", Auth: "none", TLS: "off"})
	cfg, warns, err := config.Parse(raw)
	if err != nil || len(warns) != 0 {
		t.Fatalf("written: %v %v", err, warns)
	}
	// "disks" (pwm3) becomes the profile's "hdd": its table does not fit; ssd stays
	if len(cfg.Assists) != 1 || cfg.Assist("ssd") == nil || len(r.AssistDropped) != 1 || r.AssistDropped[0] != "disks" {
		t.Errorf("assists %+v, dropped %v", cfg.Assists, r.AssistDropped)
	}
	if kept := strings.Join(r.Kept, "\n"); !strings.Contains(kept, "[[assist]] ssd") || strings.Contains(kept, "[[assist]] disks") {
		t.Errorf("kept: %v", r.Kept)
	}
}

// TestPresetApplyKeepsAssist: a preset apply rewrites [[channel]] and
// leaves the [[assist]] tables byte-identical.
func TestPresetApplyKeepsAssist(t *testing.T) {
	cfgPath := writeAssistConfig(t)
	svc := &fakeService{}
	s := dirPresetStore{dir: filepath.Join(t.TempDir(), "presets"), cfgPath: cfgPath, svc: svc, profile: "n5pro"}
	if err := s.Apply("n5pro-quiet"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, _ := os.ReadFile(cfgPath)
	tail := assistConfig[strings.Index(assistConfig, "# raise the disk fans"):]
	if !strings.HasSuffix(string(got), tail) {
		t.Errorf("assist block changed:\n%s", got)
	}
	cfg, warns, err := config.Load(cfgPath)
	if err != nil || len(warns) != 0 || len(cfg.Assists) != 1 || cfg.Assist("disks") == nil || cfg.Channel("disks").Critical != 66 {
		t.Errorf("after apply: %v %v %+v", err, warns, cfg.Assists)
	}
}

func TestAssistChecks(t *testing.T) {
	cfg, _, _ := config.Parse([]byte(assistConfig))
	chans := channelSpecs(cfg)
	factory := func(id string) (sensor.Source, error) {
		if id == "hwmon:amdgpu:temp1" {
			return fakeSource{v: 35000}, nil
		}
		return nil, sensor.ErrNoDevice
	}
	res := assistChecks(assistSpecs(cfg), chans, factory)
	if len(res) != 1 || !res[0].ok || res[0].name != "assist disks" || res[0].detail != "1 source, off" {
		t.Errorf("ok line: %+v", res)
	}
	cfg.Assists[0].Enabled = true
	cfg.Assists[0].Sources = append(cfg.Assists[0].Sources, config.AssistSource{Sensor: "hwmon:gone:temp1", Start: 1, Full: 2, Boost: 1})
	res = assistChecks(assistSpecs(cfg), chans, factory)
	if len(res) != 2 || res[0].ok || !res[0].advisory || !strings.HasPrefix(res[0].detail, "2 sources, on; 1 of 2") || !strings.Contains(res[1].detail, "hwmon:gone:temp1") {
		t.Errorf("unreadable source: %+v", res)
	}
	res = assistChecks(assistSpecs(cfg), chans[:1], factory)
	if len(res) != 1 || res[0].ok || !strings.Contains(res[0].detail, "not managed") {
		t.Errorf("unmanaged channel: %+v", res)
	}
}

type fakeSource struct{ v int }

func (f fakeSource) ID() string         { return "fake" }
func (f fakeSource) Read() (int, error) { return f.v, nil }

func TestCLIAssist(t *testing.T) {
	d := startFakeDaemon(t)
	var mu sync.Mutex
	var bodies []string
	d.stop()
	d.extra = func(mux *http.ServeMux) {
		mux.HandleFunc("PUT /api/assist/{name}", func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			bodies = append(bodies, r.PathValue("name")+":"+map[bool]string{true: "true", false: "false"}[b["enabled"] == true])
			mu.Unlock()
			if r.PathValue("name") != "hdd" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":"channel \"cpu\" has no valid [[assist]] table"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"channel":"hdd","enabled":true}`))
		})
	}
	d.start(t)
	out, errOut, code := captureOutput(t, func() int { return cmdAssist([]string{"hdd", "on"}) })
	if code != exitOK || !strings.Contains(out, "hdd: assist on") {
		t.Errorf("on: %d %q %q", code, out, errOut)
	}
	_, _, code = captureOutput(t, func() int { return cmdAssist([]string{"hdd", "off"}) })
	if code != exitOK {
		t.Errorf("off: %d", code)
	}
	_, errOut, code = captureOutput(t, func() int { return cmdAssist([]string{"cpu", "on"}) })
	if code != exitFail || !strings.Contains(errOut, "HTTP 404") {
		t.Errorf("no table: %d %q", code, errOut)
	}
	mu.Lock()
	if strings.Join(bodies, ",") != "hdd:true,hdd:false,cpu:true" {
		t.Errorf("requests %v", bodies)
	}
	mu.Unlock()
	for _, args := range [][]string{nil, {"hdd"}, {"hdd", "yes"}, {"../x", "on"}} {
		if _, _, code := captureOutput(t, func() int { return cmdAssist(args) }); code != exitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
	// no daemon: a clear error, nothing written
	d.stop()
	_, errOut, code = captureOutput(t, func() int { return cmdAssist([]string{"hdd", "on"}) })
	if code != exitFail || !strings.Contains(errOut, "does not answer") || !strings.Contains(errOut, "nothing written") {
		t.Errorf("no daemon: %d %q", code, errOut)
	}
}

func TestCLIStatusAssistColumn(t *testing.T) {
	d := startFakeDaemon(t)
	d.mu.Lock()
	d.snap.Channels[1].Mode = control.ModeAuto
	d.snap.Channels[1].Assist = &control.AssistState{Enabled: true, Boost: 45, Source: "hwmon:amdgpu:temp1"}
	d.snap.Channels[2].Assist = &control.AssistState{Enabled: false}
	d.mu.Unlock()
	out, _, code := captureOutput(t, func() int { return cmdStatus(nil) })
	if code != exitOK {
		t.Fatal(code)
	}
	lines := strings.Split(out, "\n")
	find := func(prefix string) string {
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), prefix) {
				return l
			}
		}
		return ""
	}
	if !strings.Contains(find("channel"), "assist") || !strings.HasSuffix(strings.TrimSpace(find("hdd")), "+45 amdgpu") ||
		!strings.HasSuffix(strings.TrimSpace(find("pcie")), "off") || !strings.HasSuffix(strings.TrimSpace(find("cpu")), "-") {
		t.Errorf("assist column:\n%s", out)
	}
	if assistCell(&control.AssistState{Enabled: true}) != "0" {
		t.Error("on without a boost")
	}
}
