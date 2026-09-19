package control

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/profile"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// ---- fake profile / device -------------------------------------------------

type fakeProfile struct{ name string }

func (p fakeProfile) Name() string {
	if p.name == "" {
		return "fake"
	}
	return p.name
}
func (fakeProfile) Title() string                            { return "Fake device" }
func (fakeProfile) Verified() bool                           { return true }
func (fakeProfile) Notes() string                            { return "" }
func (fakeProfile) Detect(*hwmon.FS) (profile.Device, error) { return nil, errors.New("n/a") }

// fakeDev mirrors the real pwmDevice: WriteDuty enters manual mode itself,
// ReadEnable exists (enableReader), SafeStop is recorded.
type fakeDev struct {
	mu        sync.Mutex
	prof      fakeProfile
	chans     []profile.Channel
	duty      map[int]int
	enable    map[int]int
	rpm       map[int]int
	failWrite map[int]bool // WriteDuty fails for this pwm
	failEnter map[int]bool
	failRead  map[int]bool // ReadDuty fails for this pwm
	failRPM   map[int]bool // ReadRPM fails for this pwm
	failStop  bool         // SafeStop fails for every pwm
	calls     []string     // "manual:1", "write:1=85", "stop:3=140"
	extra     map[string]string
}

// newFakeDev returns a device whose channels sit at the N5 Pro EC idle
// duties (what ReadDuty reports at start).
func newFakeDev() *fakeDev {
	d := &fakeDev{
		chans: []profile.Channel{
			{Index: 1, Label: "CPU", HasTach: true},
			{Index: 2, Label: "SSD", HasTach: true},
			{Index: 3, Label: "HDD", HasTach: true},
			{Index: 4, Label: "PCIe", HasTach: false},
		},
		duty:      map[int]int{1: 85, 2: 74, 3: 105, 4: 0},
		enable:    map[int]int{1: 2, 2: 2, 3: 2, 4: 2},
		rpm:       map[int]int{1: 2000, 2: 2100, 3: 1200, 4: 0},
		failWrite: map[int]bool{},
		failEnter: map[int]bool{},
		failRead:  map[int]bool{},
		failRPM:   map[int]bool{},
	}
	return d
}

// newN5FakeDev is newFakeDev reporting profile name "n5pro".
func newN5FakeDev() *fakeDev {
	d := newFakeDev()
	d.prof = fakeProfile{name: "n5pro"}
	return d
}

func (d *fakeDev) Profile() profile.Profile      { return d.prof }
func (d *fakeDev) HwmonPath() string             { return "/sys/class/hwmon/hwmonX" }
func (d *fakeDev) Channels() []profile.Channel   { return d.chans }
func (d *fakeDev) ExtraTemps() map[string]string { return d.extra }

func (d *fakeDev) ReadRPM(ch int) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failRPM[ch] {
		return 0, errors.New("fan input unreadable")
	}
	return d.rpm[ch], nil
}

func (d *fakeDev) ReadDuty(ch int) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failRead[ch] {
		return 0, errors.New("pwm unreadable")
	}
	return d.duty[ch], nil
}

func (d *fakeDev) ReadEnable(ch int) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strconv.Itoa(d.enable[ch]), nil
}

func (d *fakeDev) EnterManual(ch int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.enterManualLocked(ch)
}

func (d *fakeDev) enterManualLocked(ch int) error {
	d.calls = append(d.calls, fmt.Sprintf("manual:%d", ch))
	if d.failEnter[ch] {
		return errors.New("enable write failed")
	}
	d.enable[ch] = 1
	return nil
}

func (d *fakeDev) WriteDuty(ch int, duty int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.enterManualLocked(ch); err != nil {
		return err
	}
	d.calls = append(d.calls, fmt.Sprintf("write:%d=%d", ch, duty))
	if d.failWrite[ch] {
		return errors.New("readback mismatch")
	}
	d.duty[ch] = duty
	return nil
}

func (d *fakeDev) SafeStop(ch int, stop string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, fmt.Sprintf("stop:%d=%s", ch, stop))
	if d.failStop {
		return errors.New("device gone")
	}
	return nil
}

func (d *fakeDev) setRPM(ch, rpm int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rpm[ch] = rpm
}

func (d *fakeDev) setDuty(ch, duty int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.duty[ch] = duty
}

func (d *fakeDev) getDuty(ch int) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.duty[ch]
}

func (d *fakeDev) setFailWrite(ch int, fail bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failWrite[ch] = fail
}

// setFailAll makes every write and SafeStop fail (device vanished).
func (d *fakeDev) setFailAll(fail bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.chans {
		d.failWrite[c.Index] = fail
	}
	d.failStop = fail
}

func (d *fakeDev) countCalls(prefix string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (d *fakeDev) resetCalls() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = nil
}

// ---- fake sensors -----------------------------------------------------------

type fakeSensor struct {
	mu        sync.Mutex
	id        string
	val       int
	err       error
	panicOnce bool // next Read panics (once)
}

func (s *fakeSensor) ID() string { return s.id }
func (s *fakeSensor) Read() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panicOnce {
		s.panicOnce = false
		panic("fake sensor exploded")
	}
	return s.val, s.err
}

func (s *fakeSensor) set(milli int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.val, s.err = milli, nil
}

func (s *fakeSensor) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

type fakeSensors struct {
	mu       sync.Mutex
	sensors  map[string]*fakeSensor
	resolves int
}

func newFakeSensors() *fakeSensors {
	return &fakeSensors{sensors: map[string]*fakeSensor{
		"k10temp":       {id: "k10temp", val: 36000},
		"nvme:max":      {id: "nvme:max", val: 44000},
		"drivetemp:max": {id: "drivetemp:max", val: 33000},
	}}
}

func (f *fakeSensors) factory(id string) (SensorReader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves++
	if s, ok := f.sensors[id]; ok {
		return s, nil
	}
	if strings.Contains(id, ",") {
		// a composite of registered parts, like sensor.Parse builds it:
		// the maximum of the parts, with the parts readable (PartsReader)
		c := &fakeComposite{id: id}
		for _, part := range strings.Split(id, ",") {
			s, ok := f.sensors[strings.TrimSpace(part)]
			if !ok {
				return nil, fmt.Errorf("unknown sensor %q in %q", part, id)
			}
			c.parts = append(c.parts, s)
		}
		return c, nil
	}
	return nil, fmt.Errorf("unknown sensor %q", id)
}

// fakeComposite is the test double of sensor's composite: Read is the
// maximum of the readable parts, Parts the readings of the last Read.
type fakeComposite struct {
	mu    sync.Mutex
	id    string
	parts []*fakeSensor
	last  []sensor.Part
}

func (c *fakeComposite) ID() string { return c.id }
func (c *fakeComposite) Read() (int, error) {
	best, ok := 0, false
	var last []sensor.Part
	var firstErr error
	for _, p := range c.parts {
		v, err := p.Read()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		last = append(last, sensor.Part{ID: p.id, Value: v})
		if !ok || v > best {
			best, ok = v, true
		}
	}
	c.mu.Lock()
	c.last = last
	c.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("sensor %s: no readable part: %w", c.id, firstErr)
	}
	return best, nil
}
func (c *fakeComposite) Parts() []sensor.Part {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sensor.Part(nil), c.last...)
}

func (f *fakeSensors) add(id string, milli int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sensors[id] = &fakeSensor{id: id, val: milli}
}

func (f *fakeSensors) get(id string) *fakeSensor {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sensors[id]
}

// ---- fake alerter, clock, logger -----------------------------------------

type fakeAlerter struct {
	mu    sync.Mutex
	kinds []string
	msgs  []string
}

func (a *fakeAlerter) Alert(kind, msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.kinds = append(a.kinds, kind)
	a.msgs = append(a.msgs, msg)
}

func (a *fakeAlerter) count(kind string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, k := range a.kinds {
		if k == kind {
			n++
		}
	}
	return n
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type testLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLogger) Printf(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
}

// count returns how many log lines contain sub.
func (l *testLogger) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

func (l *testLogger) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ---- harness ---------------------------------------------------------------

type harness struct {
	t       *testing.T
	c       *Controller
	dev     *fakeDev
	sensors *fakeSensors
	alerts  *fakeAlerter
	clock   *fakeClock
	log     *testLogger
	cfg     config.Config
	notify  int
}

// testN5Channels is the two-point channel set the controller tests were
// written against (the pre-0.3.1-rc3 N5ProChannels); the expected duties in
// this package derive from these curves, not from the preset setup writes.
func testN5Channels() []config.Channel {
	return []config.Channel{
		{Name: "cpu", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 88, Stop: "auto"},
		{Name: "ssd", PWM: 2, Sensor: "nvme:max", Curve: [][2]int{{48, 74}, {65, 255}}, Critical: 72, Stop: "auto"},
		{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{36, 105}, {46, 255}}, Critical: 56, Stop: "140"},
	}
}

func n5cfg() config.Config {
	cfg := config.Default()
	cfg.Channels = testN5Channels()
	return cfg
}

func newHarness(t *testing.T, cfg config.Config, mod func(*Options)) *harness {
	t.Helper()
	return newHarnessDev(t, cfg, newFakeDev(), mod)
}

// newHarnessDev is newHarness with a caller-provided device.
func newHarnessDev(t *testing.T, cfg config.Config, dev *fakeDev, mod func(*Options)) *harness {
	t.Helper()
	h := &harness{t: t, dev: dev, sensors: newFakeSensors(), alerts: &fakeAlerter{},
		clock: &fakeClock{t: time.Unix(1_789_500_000, 0)}, log: &testLogger{}, cfg: cfg}
	opts := Options{
		Logger:     h.log,
		Now:        h.clock.now,
		Notify:     func() { h.notify++ },
		Status:     func(string) {},
		SyncAlerts: true,
		Wait: func(ctx context.Context, d time.Duration) bool {
			h.clock.advance(d)
			select {
			case <-ctx.Done():
				return false
			default:
				return true
			}
		},
	}
	if mod != nil {
		mod(&opts)
	}
	c, err := New(cfg, h.dev, h.sensors.factory, h.alerts, opts)
	if err != nil {
		t.Fatal(err)
	}
	h.c = c
	return h
}

// cycles runs n cycles, advancing the clock by the interval after each.
func (h *harness) cycles(n int) {
	for i := 0; i < n; i++ {
		h.c.cycle()
		h.clock.advance(h.c.interval())
	}
}

func (h *harness) state(name string) ChannelState {
	h.t.Helper()
	for _, cs := range h.c.Snapshot().Channels {
		if cs.Name == name {
			return cs
		}
	}
	h.t.Fatalf("no channel %q in snapshot", name)
	return ChannelState{}
}

func (h *harness) expectDuty(name string, want int) {
	h.t.Helper()
	if got := h.state(name).Duty; got != want {
		h.t.Errorf("%s: duty %d, want %d (mode %s, target %d)", name, got, want, h.state(name).Mode, h.state(name).Target)
	}
}

func (h *harness) expectMode(name string, want Mode) {
	h.t.Helper()
	if got := h.state(name).Mode; got != want {
		h.t.Errorf("%s: mode %s, want %s", name, got, want)
	}
}
