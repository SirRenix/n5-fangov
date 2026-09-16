package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/profile"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

func init() {
	register("test", command{run: cmdTest})
}

// Test parameters, mirroring n5pro-ec/scripts/06-pwm-test.sh.
const (
	testMaxCPU   = 85.0 // C, abort threshold k10temp
	testMaxNVMe  = 70.0 // C, abort threshold nvme:max
	testRPMTol   = 150  // rpm, "back to baseline" tolerance
	testSettle   = 20 * time.Second
	testStepHold = 45 * time.Second
	testSample   = 5 * time.Second
)

var testSteps = []int{255, 217, 179, 140}

// cmdTest drives ONE channel through 255/217/179/140 for 45 s each and logs
// every tach each 5 s. Only the tach of the written channel may move; a
// moving other tach means the profile's channel mapping is wrong. Any tach
// at 0 or a temperature limit aborts. On every exit path the channel is
// returned to its configured safe state and the rpm are compared with the
// baseline (±150).
//
// The daemon must not be running: this command does not coordinate with it.
func cmdTest(args []string) int {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	force := fs.Bool("force", false, "proceed even though the daemon socket answers (you must stop the daemon yourself)")
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	hold := fs.Duration("hold", testStepHold, "time per duty step (fixtures only)")
	sample := fs.Duration("sample", testSample, "tach sampling interval (fixtures only)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov test <channel|pwmN> [--force]")
		return exitUsage
	}
	if *sample <= 0 || *hold <= 0 || *sample > *hold {
		fmt.Fprintf(os.Stderr, "test: --sample (%s) and --hold (%s) must be positive and sample <= hold\n", *sample, *hold)
		return exitUsage
	}
	want := fs.Arg(0)

	if os.Geteuid() != 0 && os.Getenv("N5FANGOV_SYSFS") == "" {
		fmt.Fprintln(os.Stderr, "test: must run as root (writes pwm via sysfs)")
		return exitFail
	}

	dir := runDir()
	if daemonRunning(dir) || unitActive(unitName) {
		fmt.Fprintln(os.Stderr, "test: the n5-fangov daemon is running. Two regulators must never write the same channel.")
		fmt.Fprintln(os.Stderr, "      Stop it first:  systemctl stop n5-fangov   (ExecStopPost puts the fans into the safe state)")
		if !*force {
			fmt.Fprintln(os.Stderr, "      Refusing. --force skips this check but does NOT stop the daemon for you.")
			return exitFail
		}
		fmt.Fprintln(os.Stderr, "      --force given: continuing on your responsibility; the daemon will fight this test.")
	}

	cfg, warns, _ := loadConfig(*cfgPath)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "config: %s\n", w)
	}
	hw := hwmon.New()
	dev, err := detectDevice(hw, daemonOf(cfg).Profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test: profile: %v\n", err)
		return exitFail
	}
	spec, chn, err := resolveTestChannel(dev, channelSpecs(cfg), want)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test:", err)
		return exitFail
	}
	spec.Stop = testStopPolicy(dev.Profile().Name(), channelSpecs(cfg), chn.Index)

	t := &testRun{
		dev: dev, hw: hw, ch: chn, spec: spec,
		hold: *hold, sample: *sample,
		factory: newSensorFactory(hw, dev),
	}
	t.initSensors()
	return t.run()
}

// testStopPolicy picks the stop value the verification run restores pwm to.
// It is the stop of the channel that owns the pwm after
// control.SanitizeChannels: on the n5pro profile pwm3 is never "auto"
// (the EC does not regulate it after a write, stop "140" from the built-in
// defaults) and pwm1..3 missing from the config get their built-in stop.
// A pwm no channel owns falls back to "auto".
func testStopPolicy(profileName string, chans []chanSpec, pwm int) string {
	fixed, _ := sanitizeChannelSpecs(profileName, chans)
	for _, c := range fixed {
		if c.PWM == pwm {
			return c.Stop
		}
	}
	return "auto"
}

// resolveTestChannel accepts a channel name from the config or "pwmN"/"N".
// The returned spec's Stop is the configured value (or "auto"); the caller
// replaces it with testStopPolicy.
func resolveTestChannel(dev profile.Device, chans []chanSpec, want string) (chanSpec, profile.Channel, error) {
	idx := -1
	spec := chanSpec{Stop: "auto"}
	for _, c := range chans {
		if c.Name == want {
			spec, idx = c, c.PWM
		}
	}
	if idx < 0 {
		if n, err := strconv.Atoi(strings.TrimPrefix(want, "pwm")); err == nil {
			idx = n
			spec.Name = "pwm" + strconv.Itoa(n)
			spec.PWM = n
			for _, c := range chans {
				if c.PWM == n {
					spec = c
				}
			}
		}
	}
	if idx < 0 {
		return spec, profile.Channel{}, fmt.Errorf("unknown channel %q (use a configured name or pwmN)", want)
	}
	for _, c := range dev.Channels() {
		if c.Index == idx {
			return spec, c, nil
		}
	}
	return spec, profile.Channel{}, fmt.Errorf("pwm%d is not exposed by profile %s", idx, dev.Profile().Name())
}

type testRun struct {
	dev     profile.Device
	hw      *hwmon.FS
	ch      profile.Channel
	spec    chanSpec
	hold    time.Duration
	sample  time.Duration
	factory sensorFactory

	cpu, nvme sensor.Source // nil when not available
	ecKeys    []string      // profile extra temps, sorted
	tachs     []profile.Channel
}

func (t *testRun) initSensors() {
	if s, err := t.factory("k10temp"); err == nil {
		t.cpu = s
	} else if s, err := t.factory("coretemp"); err == nil {
		t.cpu = s
	}
	if s, err := t.factory("nvme:max"); err == nil {
		t.nvme = s
	}
	for k := range t.dev.ExtraTemps() {
		t.ecKeys = append(t.ecKeys, k)
	}
	sort.Strings(t.ecKeys)
	for _, c := range t.dev.Channels() {
		if c.HasTach {
			t.tachs = append(t.tachs, c)
		}
	}
}

func (t *testRun) temp(s sensor.Source) (float64, bool) {
	if s == nil {
		return 0, false
	}
	v, err := readTempC(s)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (t *testRun) rpms() map[int]int {
	out := make(map[int]int, len(t.tachs))
	for _, c := range t.tachs {
		r, err := t.dev.ReadRPM(c.Index)
		if err != nil {
			r = -1
		}
		out[c.Index] = r
	}
	return out
}

// reading is one sample of all tachs and the guard temperatures.
type reading struct {
	text         string
	rpm          map[int]int
	cpu, nvme    float64
	cpuOK, nvmOK bool
}

// take samples all tachs, cpu, nvme and EC temperatures and renders one line.
func (t *testRun) take(prefix string) reading {
	var b strings.Builder
	b.WriteString(prefix)
	rd := reading{rpm: t.rpms()}
	for _, c := range t.tachs {
		v := "n/a"
		if rd.rpm[c.Index] >= 0 {
			v = strconv.Itoa(rd.rpm[c.Index])
		}
		fmt.Fprintf(&b, " fan%d=%-5s", c.Index, v)
	}
	rd.cpu, rd.cpuOK = t.temp(t.cpu)
	rd.nvme, rd.nvmOK = t.temp(t.nvme)
	fmt.Fprintf(&b, "  CPU=%s  NVMe=%s", optTemp(rd.cpu, rd.cpuOK), optTemp(rd.nvme, rd.nvmOK))
	for _, k := range t.ecKeys {
		if mc, err := readIntPath(t.hw, t.dev.ExtraTemps()[k]); err == nil {
			fmt.Fprintf(&b, "  %s=%.0f", k, float64(mc)/1000)
		}
	}
	rd.text = b.String()
	return rd
}

func optTemp(v float64, ok bool) string {
	if !ok {
		return "n/a"
	}
	return fmt.Sprintf("%.0f C", v)
}

// sleepCtx sleeps d or returns false when ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (t *testRun) run() (rc int) {
	p := t.dev.Profile()
	fmt.Printf("=== n5-fangov test  channel %s (pwm%d, %s)  profile %s  %s ===\n\n",
		t.spec.Name, t.ch.Index, t.ch.Label, p.Name(), time.Now().Format(time.RFC3339))
	fmt.Println("This command WRITES duty values. It can slow down or stop a fan.")
	fmt.Println("Confirm before continuing:")
	fmt.Println("  - maintenance window: no scrubs, backups or large transfers")
	fmt.Println("  - no other regulator active (n5-fangov, n5-fand, fancontrol)")
	fmt.Println("  - a way back to the console exists (KVM) in case the fan does not recover")
	if !t.ch.HasTach {
		fmt.Printf("  - pwm%d has no tach: the expected finding is \"no tach changes\"\n", t.ch.Index)
	}
	if !p.Verified() {
		fmt.Printf("  - profile %s is NOT hardware-verified\n", p.Name())
	}
	fmt.Println()
	fmt.Print("Type exactly  TEST START  to continue: ")
	rd := bufio.NewReader(os.Stdin)
	answer, _ := rd.ReadString('\n')
	if strings.TrimSpace(answer) != "TEST START" {
		fmt.Println("Aborted, nothing written.")
		return exitFail
	}

	// Baseline (whatever regulates now: EC auto or the fixed stop duty).
	fmt.Println("\n--- baseline before any write ---")
	first := t.take("  ")
	fmt.Println(first.text)
	base := first.rpm
	if d, err := t.dev.ReadDuty(t.ch.Index); err == nil {
		fmt.Printf("  pwm%d duty %d (%d%%), stop policy %q\n", t.ch.Index, d, pct(d), t.spec.Stop)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	defer func() {
		// runs on every exit path including panics
		if r := recover(); r != nil {
			fmt.Printf("\n!! panic: %v\n", r)
			rc = exitFail
		}
		t.restore(base)
	}()

	fmt.Printf("\n--- pwm%d_enable = 1 (manual; n5pro driver starts at 255) ---\n", t.ch.Index)
	if err := t.dev.EnterManual(t.ch.Index); err != nil {
		fmt.Printf("  !! EnterManual: %v\n", err)
		return exitFail
	}
	if !sleepCtx(ctx, t.sample) {
		fmt.Println("  aborted by signal")
		return exitFail
	}
	fmt.Println(t.take("  after:").text)

	for _, duty := range testSteps {
		fmt.Printf("\n--- pwm%d = %d (~%d%%) for %s ---\n", t.ch.Index, duty, pct(duty), t.hold)
		if err := t.dev.WriteDuty(t.ch.Index, duty); err != nil {
			fmt.Printf("  !! WriteDuty %d: %v (read-back mismatch or EC refused)\n", duty, err)
			return exitFail
		}
		samples := int(t.hold / t.sample)
		if samples < 1 {
			samples = 1
		}
		for i := 1; i <= samples; i++ {
			if !sleepCtx(ctx, t.sample) {
				fmt.Println("  aborted by signal")
				return exitFail
			}
			s := t.take(fmt.Sprintf("  t+%02ds", int(t.sample.Seconds())*i))
			fmt.Println(s.text)
			if s.cpuOK && s.cpu >= testMaxCPU {
				fmt.Printf("  !! CPU >= %.0f C, aborting\n", testMaxCPU)
				return exitFail
			}
			if s.nvmOK && s.nvme >= testMaxNVMe {
				fmt.Printf("  !! NVMe >= %.0f C, aborting\n", testMaxNVMe)
				return exitFail
			}
			for _, c := range t.tachs {
				if s.rpm[c.Index] == 0 {
					fmt.Printf("  !! fan%d stopped (0 rpm), aborting\n", c.Index)
					return exitFail
				}
			}
		}
	}

	fmt.Println("\nSequence complete. Evaluate the tach columns:")
	if t.ch.HasTach {
		fmt.Printf("  - only fan%d may have moved between the steps\n", t.ch.Index)
	} else {
		fmt.Printf("  - pwm%d has no tach: no fan column may have moved\n", t.ch.Index)
	}
	fmt.Println("  - if another tach moved, the profile's channel mapping is wrong")
	return rc
}

// restore returns the channel to its configured safe state and compares the
// rpm with the baseline after a settle time.
func (t *testRun) restore(base map[int]int) {
	fmt.Printf("\n--- restore: SafeStop(pwm%d, %q) ---\n", t.ch.Index, t.spec.Stop)
	if err := t.dev.SafeStop(t.ch.Index, t.spec.Stop); err != nil {
		fmt.Printf("  !! SafeStop failed: %v\n", err)
		fmt.Printf("  !! Manual fallback: echo 2 > %s/pwm%d_enable  (or modprobe -r the driver)\n", t.dev.HwmonPath(), t.ch.Index)
	} else {
		fmt.Println("  SafeStop ok, driver confirmed")
	}
	if d, err := t.dev.ReadDuty(t.ch.Index); err == nil {
		fmt.Printf("  pwm%d duty now %d (%d%%)\n", t.ch.Index, d, pct(d))
	}
	fmt.Printf("  waiting %s, then comparing with the baseline (+-%d rpm)\n", testSettle, testRPMTol)
	settle := testSettle
	if t.hold < testStepHold {
		settle = t.sample // fixture run
	}
	time.Sleep(settle)
	now := t.rpms()
	ok := true
	for _, c := range t.tachs {
		b, n := base[c.Index], now[c.Index]
		d := n - b
		if d < 0 {
			d = -d
		}
		if b < 0 || n < 0 {
			fmt.Printf("  fan%d: %s rpm, baseline %s rpm: no reading\n", c.Index, fmtRPM(n), fmtRPM(b))
			continue
		}
		if d > testRPMTol {
			fmt.Printf("  !! fan%d: %d rpm, baseline %d rpm (delta %d): NOT back\n", c.Index, n, b, d)
			ok = false
		} else {
			fmt.Printf("  fan%d: %d rpm, baseline %d rpm (delta %d) ok\n", c.Index, n, b, d)
		}
	}
	if ok {
		fmt.Println("safe state restored: YES")
	} else {
		fmt.Println("safe state restored: NO. Watch the fans; a cold boot returns the EC to full control.")
	}
}
