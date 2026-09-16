package main

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

type fakeWatched struct{ ids []string }

func (f *fakeWatched) SetWatched(ids []string) { f.ids = ids }
func (f *fakeWatched) Watched() []string       { return f.ids }

func TestDashboardStore(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	ctrl := &fakeWatched{}
	factory := sensorFactory(func(id string) (sensor.Source, error) {
		switch {
		case id == "k10temp" || id == "ec:system":
			return nil, nil
		case strings.HasPrefix(id, "hwmon:") && strings.Count(id, ":") == 2:
			return nil, sensor.ErrNoDevice
		}
		return nil, errors.New("sensor: unknown id")
	})
	s := newDashboardStore(cfgPath, ctrl, factory, nil)
	if got := s.Sensors(); len(got) != 0 {
		t.Errorf("initial: %v", got)
	}
	warns, err := s.SetSensors([]string{" k10temp ", "hwmon:amdgpu:temp1", "k10temp", ""})
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], "hwmon:amdgpu:temp1") {
		t.Fatalf("set: %v %v", warns, err)
	}
	if !reflect.DeepEqual(ctrl.ids, []string{"k10temp", "hwmon:amdgpu:temp1"}) {
		t.Errorf("controller list: %v", ctrl.ids)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "[dashboard]\nsensors = [\"k10temp\", \"hwmon:amdgpu:temp1\"]") || !strings.Contains(string(raw), "# my config") {
		t.Errorf("file:\n%s", raw)
	}
	back, pw := parseConfig(raw)
	if len(pw) != 0 || !reflect.DeepEqual(back.Dashboard.Sensors, []string{"k10temp", "hwmon:amdgpu:temp1"}) {
		t.Errorf("re-parse: %v %v", pw, back.Dashboard.Sensors)
	}
	// unknown id form → error, nothing written
	if _, err := s.SetSensors([]string{"bogus"}); err == nil {
		t.Errorf("unparsable id must be refused")
	}
	if _, err := s.SetSensors([]string{"hwmon:x:temp1\"\n"}); err == nil {
		t.Errorf("quote/newline must be refused")
	}
	var many []string
	for i := 0; i < config.MaxDashboardSensors+1; i++ {
		many = append(many, "hwmon:x:temp"+string(rune('1'+i)))
	}
	if _, err := s.SetSensors(many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too many: %v", err)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(raw) {
		t.Errorf("refused updates must not write")
	}
	// empty list: sensors = [] and warnings is a non-nil empty slice
	warns, err = s.SetSensors(nil)
	if err != nil || warns == nil || len(warns) != 0 || len(ctrl.ids) != 0 {
		t.Errorf("clear: %v %v %v", warns, err, ctrl.ids)
	}
	raw, _ = os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "sensors = []") {
		t.Errorf("cleared file:\n%s", raw)
	}
	if tomlStringArray([]string{`a"b`, "c"}) != `["a\"b", "c"]` {
		t.Errorf("tomlStringArray: %s", tomlStringArray([]string{`a"b`, "c"}))
	}
}

// fakeService records Reload calls for the preset store and the hook. The
// mutex matters: the real controller
// serialises Reload on its own lock, and TestConfigWritersSerialised calls
// Apply from several goroutines (found by -race, AUDIT hoch 3).
type fakeService struct {
	control.Service
	mu   sync.Mutex
	raws [][]byte
	err  error
}

func (f *fakeService) Reload(raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raws = append(f.raws, raw)
	return f.err
}

// reloads is the number of Reload calls so far.
func (f *fakeService) reloads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.raws)
}

func TestHookedServiceReload(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	m := newAlertManager(cfgPath, "", config.Alert{Transport: "log", MailTo: "root"}, nil)
	inner := &fakeService{}
	sched := newScheduler(func(string) error { return nil }, nil, nil, nil)
	svc := hookedService{Service: inner, alerts: m, sched: sched}
	raw := []byte(storeConfig + "\n[alert]\ntransport = \"off\"\n\n[[schedule]]\npreset = \"night\"\nfrom = \"22:00\"\nto = \"07:00\"\n")
	if err := svc.Reload(raw); err != nil || inner.reloads() != 1 {
		t.Fatalf("reload: %v", err)
	}
	if m.sw.Get().Name() != "off" {
		t.Errorf("[alert] must be re-applied after a successful reload: %s", m.sw.Get().Name())
	}
	if st := sched.Status().(scheduleStatus); len(st.Entries) != 1 || st.Entries[0].Preset != "night" {
		t.Errorf("[[schedule]] must reach the scheduler after a successful reload: %+v", st.Entries)
	}
	// restart required: the sentinel passes through, but the file is the
	// truth for [alert] and [[schedule]] — both are taken from it
	inner.err = errRestartRequired()
	raw = []byte(storeConfig + "\n[alert]\ntransport = \"log\"\nmail_to = \"ops\"\n\n[[schedule]]\npreset = \"day\"\n")
	if err := svc.Reload(raw); !isRestartRequired(err) {
		t.Errorf("restart sentinel: %v", err)
	}
	if m.sw.Get().Name() != "log" || m.Status().MailTo != "ops" {
		t.Errorf("202 must still apply [alert] from the file: %s %+v", m.sw.Get().Name(), m.Status())
	}
	if st := sched.Status().(scheduleStatus); len(st.Entries) != 1 || st.Entries[0].Preset != "day" || !st.Entries[0].Fallback {
		t.Errorf("202 must still hand the schedules over: %+v", st.Entries)
	}
	// any other reload error: nothing applied
	inner.err = errors.New("channel cpu: pwm1_enable not writable")
	if err := svc.Reload([]byte(storeConfig + "\n[alert]\ntransport = \"off\"\n")); err == nil || isRestartRequired(err) {
		t.Errorf("reload error: %v", err)
	}
	if m.sw.Get().Name() != "log" {
		t.Errorf("a failed reload must not apply [alert]")
	}
	if st := sched.Status().(scheduleStatus); len(st.Entries) != 1 || st.Entries[0].Preset != "day" {
		t.Errorf("a failed reload must not change the schedules: %+v", st.Entries)
	}
}
