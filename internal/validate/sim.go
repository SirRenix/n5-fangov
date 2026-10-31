package validate

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/n5driver"
)

// Sim is the Env of `validate --dry-run`: an in-memory model of the N5 Pro
// EC on a virtual clock. Nothing touches sysfs. Its numbers come from the
// 2026-09-14 measurement (BEFUNDE Phase 6): duty -> rpm per channel, the
// EC automatic values 0x55/0x4a/0x57/0x8c, and the observed hand-back
// behaviour (CPU/SSD back to their auto value, HDD to 0x57 after a delay,
// PCIe keeps the last written duty). It is a fixture for the tool's own
// tests and a rehearsal of the procedure, not a statement about any EC.
type Sim struct {
	mu     sync.Mutex
	now    time.Time
	dcr    [8]int // DCR0..7
	manual [5]bool
	autoAt [5]time.Time // when the auto command was sent
	// AutoValue is the register value a channel reaches after the auto
	// command, AutoDelay how long that takes; KeepOnAuto keeps the last
	// written duty instead (PCIe on 2026-09-14).
	AutoValue  [5]int
	AutoDelay  [5]time.Duration
	KeepOnAuto [5]bool
	// Oscillate makes a channel alternate between AutoValue and
	// AutoValue+20 every 5 s after the auto command (never steady: T3 FAIL).
	Oscillate [5]bool
	// KeepDCR4OnAuto makes DCR4 keep its last value after the auto command
	// of pwm3 while DCR3 goes to AutoValue (DCR3/DCR4 disagree, tests).
	KeepDCR4OnAuto bool
	// StopDuty is what Restore writes ("auto" when 0); RestoreErr, when set,
	// is returned by Restore instead (tests).
	StopDuty   [5]int
	RestoreErr [5]error
	// Temperatures and load; NoDCR makes DCR() fail like a driver without
	// debug_regs.
	Tctl, NVMe, HDD, ECCPU, ECSys, ECBoard, ECAmb, Load1 float64
	NoDCR                                                bool
	// Writes counts the duty writes (tests).
	Writes int
	// Latency delays a written duty (and the 255 of manual mode) in the
	// register of a channel, like the EC (maintenance window 1: DCR5 within
	// 0.22 s, DCR3/DCR4 up to 4.8 s).
	Latency [5]time.Duration
	pending [5]pendingDuty
	// SleepHook, when set, returns how far a Sleep(d) advances the clock
	// (timer jitter and overruns in tests).
	SleepHook func(d time.Duration) time.Duration
}

type pendingDuty struct {
	v   int
	at  time.Time
	set bool
}

// NewSim returns the model with the 2026-09-14 values; the clock starts at
// start.
func NewSim(start time.Time) *Sim {
	s := &Sim{now: start,
		AutoValue:  [5]int{0, 0x55, 0x4a, 0x57, 0x8c},
		AutoDelay:  [5]time.Duration{0, 5 * time.Second, 5 * time.Second, 10 * time.Second, 0},
		KeepOnAuto: [5]bool{false, false, false, false, true},
		StopDuty:   [5]int{0, 0, 0, 140, 0},
		Tctl:       40, NVMe: 45, HDD: 38, ECCPU: 37, ECSys: 33, ECBoard: 27, ECAmb: 22, Load1: 0.2,
	}
	s.dcr[1], s.dcr[2], s.dcr[3], s.dcr[4], s.dcr[5] = 0x55, 0x4a, 0x8c, 0x8c, 0x8c
	return s
}

// rpm curves (duty -> rpm) per tach, from the 14.09. pwm tests.
var simCurves = map[int][][2]float64{
	1: {{0x55, 2020}, {140, 3120}, {179, 3830}, {217, 4445}, {255, 5073}},
	2: {{0x4a, 2170}, {140, 3280}, {179, 3790}, {217, 4230}, {255, 4687}},
	3: {{87, 1237}, {105, 1650}, {140, 2250}, {179, 2725}, {217, 3160}, {255, 3540}},
}

func interp(c [][2]float64, x float64) float64 {
	if x <= c[0][0] {
		return c[0][1] * x / c[0][0]
	}
	for i := 1; i < len(c); i++ {
		if x <= c[i][0] {
			a, b := c[i-1], c[i]
			return a[1] + (b[1]-a[1])*(x-a[0])/(b[0]-a[0])
		}
	}
	return c[len(c)-1][1]
}

func dcrIndex(ch int) int {
	n, _ := n5driver.ChannelDCR(ch)
	return n
}

// settle applies pending auto transitions up to now (caller holds mu).
func (s *Sim) settle() {
	for ch := 1; ch <= 4; ch++ {
		if p := s.pending[ch]; p.set && !s.now.Before(p.at) {
			s.setDCR(ch, p.v)
			s.pending[ch] = pendingDuty{}
		}
	}
	for ch := 1; ch <= 4; ch++ {
		if s.manual[ch] || s.autoAt[ch].IsZero() || s.KeepOnAuto[ch] {
			continue
		}
		if el := s.now.Sub(s.autoAt[ch]); el >= s.AutoDelay[ch] {
			v := s.AutoValue[ch]
			if s.Oscillate[ch] && int(el/(5*time.Second))%2 == 1 {
				v += 20
			}
			if ch == 3 && s.KeepDCR4OnAuto {
				s.dcr[3] = v
				continue
			}
			s.setDCR(ch, v)
		}
	}
}

func (s *Sim) setDCR(ch, v int) {
	n := dcrIndex(ch)
	s.dcr[n] = v
	if ch == 3 {
		s.dcr[4] = v // DCR3 = DCR4 (HDD group)
	}
}

func (s *Sim) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

func (s *Sim) Sleep(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	if s.SleepHook != nil {
		d = s.SleepHook(d)
	}
	s.now = s.now.Add(d)
	s.settle()
	s.mu.Unlock()
	return true
}

func (s *Sim) EnterManual(ch int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch < 1 || ch > 4 {
		return fmt.Errorf("sim: no pwm%d", ch)
	}
	if !s.manual[ch] {
		s.manual[ch] = true
		s.setLater(ch, 255) // the driver sets 255 on pwmN_enable=1
	}
	return nil
}

func (s *Sim) WriteDuty(ch, duty int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.manual[ch] {
		return fmt.Errorf("sim: pwm%d not in manual mode (EBUSY)", ch)
	}
	s.Writes++
	s.setLater(ch, duty)
	return nil
}

// setLater writes the register now or after the channel's Latency.
func (s *Sim) setLater(ch, v int) {
	if s.Latency[ch] <= 0 {
		s.setDCR(ch, v)
		return
	}
	s.pending[ch] = pendingDuty{v: v, at: s.now.Add(s.Latency[ch]), set: true}
}

func (s *Sim) Auto(ch int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manual[ch] = false
	s.pending[ch] = pendingDuty{}
	s.autoAt[ch] = s.now
	s.settle()
	return nil
}

func (s *Sim) Restore(ch int) error {
	s.mu.Lock()
	stop, rerr := s.StopDuty[ch], s.RestoreErr[ch]
	s.mu.Unlock()
	if rerr != nil {
		return rerr
	}
	if stop == 0 {
		return s.Auto(ch)
	}
	if err := s.EnterManual(ch); err != nil {
		return err
	}
	return s.WriteDuty(ch, stop)
}

func (s *Sim) DCR() (n5driver.DCR, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.NoDCR {
		return n5driver.DCR{}, fmt.Errorf("sim: debugfs dcr not present")
	}
	var d n5driver.DCR
	d[1] = 0xff
	for i := 0; i < 8; i++ {
		d[2+i] = byte(s.dcr[i])
	}
	return d, nil
}

func (s *Sim) Read() Readings {
	s.mu.Lock()
	defer s.mu.Unlock()
	rd := Readings{Fan: map[int]int{}, Tctl: s.Tctl, NVMeMax: s.NVMe, HDDMax: s.HDD, ECCPU: s.ECCPU, ECSys: s.ECSys, ECBoard: s.ECBoard, ECAmb: s.ECAmb, Load1: s.Load1}
	for f := 1; f <= 3; f++ {
		rd.Fan[f] = int(math.Round(interp(simCurves[f], float64(s.dcr[f]))))
	}
	return rd
}
