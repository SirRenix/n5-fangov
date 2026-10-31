package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/n5driver"
	"github.com/SirRenix/n5-fangov/internal/profile"
	"github.com/SirRenix/n5-fangov/internal/sensor"
	"github.com/SirRenix/n5-fangov/internal/validate"
	"github.com/SirRenix/n5-fangov/internal/version"
)

func init() {
	register("validate", command{run: cmdValidate})
}

// validateConfirm is the text the operator types before anything is written.
const validateConfirm = "VALIDATE START"

// validateTests are the test ids in run order.
var validateTests = []string{"T0", "T6", "T1", "T2", "T3", "C1", "T4", "T5"}

// validateIO lets tests replace stdin, the environment checks and the clock.
var (
	validateStdin   io.Reader = os.Stdin
	validateSession           = func() session { return detectSession(os.Getpid(), os.Getenv, stdinTerminal()) }
	validateDaemon            = func() bool { return daemonRunning(runDir()) || unitRunning(unitName) }
	// validateOtherWriters lists the active pwm writers (G1, and the
	// guard asked at every sample of the run).
	validateOtherWriters = func() []string {
		var active []string
		if validateDaemon() {
			active = append(active, "n5-fangov")
		}
		for _, u := range []string{"n5-fand", "fancontrol"} {
			if unitRunning(u) {
				active = append(active, u)
			}
		}
		return active
	}
	validateNow   = time.Now
	validateSleep = sleepCtx
)

// cmdValidate runs TESTSPEC (validate.SpecVersion) for one channel of the N5 Pro EC and writes
// the result bundle (§8). See docs/05-cli.md "validate".
func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file (stop values of the channels)")
	out := fs.String("out", ".", "directory for the result bundle")
	tests := fs.String("tests", strings.Join(validateTests, ","), "comma-separated test ids")
	boot := fs.String("boot", "", "boot type of this boot as you know it: cold or warm (recorded; T0 needs it)")
	console := fs.Bool("i-am-at-the-console", false, "you sit at the device (required in a remote session, e.g. SSH also under sudo, or without a local console in the process chain)")
	dry := fs.Bool("dry-run", false, "simulate against the built-in EC model and the sysfs tree (N5FANGOV_SYSFS); writes nothing")
	replay := fs.Bool("replay", false, "evaluate raw logs: validate --replay PWMTEST.txt I2EC.txt (prints verdict JSON)")
	hold := fs.Duration("hold", validate.T1StepHoldS*time.Second, "time per step (--dry-run only)")
	sample := fs.Duration("sample", validate.SampleS*time.Second, "sample interval (--dry-run only)")
	observe := fs.Duration("observe", validate.ObserveS*time.Second, "log time after the auto command (--dry-run only)")
	window := fs.Duration("gate-window", validate.GateWindowS*time.Second, "G4 window (--dry-run only)")
	repeat := fs.Int("repeat", validate.RunsRequired, "T2/T3 repetitions (--dry-run only)")
	restoreFlag := fs.String("restore", "", "restore values overriding the configured stop values: pwmN=auto|D (D >= 100), comma-separated (TESTSPEC §4)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return exitUsage
	}
	if *replay {
		if len(pos) != 2 {
			fmt.Fprintln(os.Stderr, "usage: n5-fangov validate --replay PWMTEST.txt I2EC.txt")
			return exitUsage
		}
		return validateReplay(pos[0], pos[1])
	}
	if !*dry {
		var bad []string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "hold", "sample", "observe", "gate-window", "repeat":
				bad = append(bad, "--"+f.Name)
			}
		})
		if len(bad) > 0 {
			fmt.Fprintf(os.Stderr, "validate: %s only with --dry-run; a real run uses the TESTSPEC %s timing\n", strings.Join(bad, ", "), validate.SpecVersion)
			return exitUsage
		}
	}
	if len(pos) > 1 || (len(pos) == 0 && !*dry) {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov validate <pwmN|channel> [--tests T0,T1,...] [--boot cold|warm] [--out DIR] [--i-am-at-the-console] | --dry-run [pwmN] | --replay PWMTEST I2EC")
		return exitUsage
	}
	if *boot != "" && *boot != "cold" && *boot != "warm" {
		fmt.Fprintln(os.Stderr, "validate: --boot is cold or warm")
		return exitUsage
	}
	want := map[string]bool{}
	for _, t := range strings.Split(*tests, ",") {
		t = strings.ToUpper(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		known := false
		for _, k := range validateTests {
			known = known || k == t
		}
		if !known {
			fmt.Fprintf(os.Stderr, "validate: unknown test %q (known: %s)\n", t, strings.Join(validateTests, ","))
			return exitUsage
		}
		want[t] = true
	}
	if *sample <= 0 || *hold < *sample || *observe < *sample || *window < 0 || *repeat < 1 {
		fmt.Fprintln(os.Stderr, "validate: --sample > 0, --hold and --observe >= --sample, --repeat >= 1")
		return exitUsage
	}
	overrides, err := parseRestoreFlag(*restoreFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		return exitUsage
	}
	chArg := "pwm3"
	if len(pos) == 1 {
		chArg = pos[0]
	}

	hw := hwmon.New()
	cfg, _, _ := loadConfig(*cfgPath)
	v := &validation{hw: hw, want: want, boot: *boot, out: *out, dry: *dry, console: *console}
	if *dry {
		ch, err := strconv.Atoi(strings.TrimPrefix(chArg, "pwm"))
		if err != nil || ch < 1 || ch > 4 {
			fmt.Fprintln(os.Stderr, "validate: --dry-run takes pwm1..pwm4")
			return exitUsage
		}
		sim := validate.NewSim(time.Now().UTC().Truncate(time.Second))
		v.env, v.ch = sim, ch
		v.cfg = validate.Config{Channel: ch, OwnTach: tachOf(ch), Tachs: []int{1, 2, 3}, Hold: *hold, Sample: *sample, Observe: *observe, Window: *window, Repeat: *repeat,
			Settle: validate.SettleTime(ch), Tolerance: validate.GridTolerance}
		if !prepareValidateOut(*out) {
			return exitFail
		}
		return v.run()
	}

	// ---- real hardware: refusals first, nothing is written before the
	// confirmation ----
	if os.Geteuid() != 0 && os.Getenv("N5FANGOV_SYSFS") == "" {
		fmt.Fprintln(os.Stderr, "validate: must run as root")
		return exitFail
	}
	// the bundle directory before any gate (TESTSPEC 0.4 §3, #23): a run
	// whose results cannot be stored is not started, nothing is written
	if !prepareValidateOut(*out) {
		return exitFail
	}
	if validateDaemon() {
		fmt.Fprintln(os.Stderr, "validate: the n5-fangov daemon is running or (re)starting. Stop it first: systemctl stop n5-fangov")
		fmt.Fprintln(os.Stderr, "          (TESTSPEC G1: no other pwm writer; there is no override)")
		return exitFail
	}
	sess := validateSession()
	v.session = sess
	if v.writing() {
		if sess.stdin == "" {
			fmt.Fprintln(os.Stderr, "validate: standard input is not a terminal. The confirmation must be typed at the device (TESTSPEC G5); piped input is refused.")
			return exitFail
		}
		if !*console && len(sess.remote) > 0 {
			fmt.Fprintf(os.Stderr, "validate: this session is remote (%s). Writes are only run with the operator at the device (TESTSPEC G5).\n", strings.Join(sess.remote, "; "))
			fmt.Fprintln(os.Stderr, "          Run it on the console, or add --i-am-at-the-console if you sit at the device.")
			return exitFail
		}
		if !*console && !sess.console {
			fmt.Fprintf(os.Stderr, "validate: %s is not a local console (no VT or serial login in this process chain). Writes are only run with the operator at the device (TESTSPEC G5).\n", sess.stdin)
			fmt.Fprintln(os.Stderr, "          Run it on the console (tty1, KVM), or add --i-am-at-the-console if you sit at the device.")
			return exitFail
		}
	}
	dev, err := detectDevice(hw, "n5pro")
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate: needs the n5pro profile with pwm nodes: %v\n", err)
		return exitFail
	}
	_, chn, err := resolveTestChannel(dev, channelSpecs(cfg), chArg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		return exitFail
	}
	stops := validateStops(dev, channelSpecs(cfg))
	for ch, s := range overrides {
		if _, ok := stops[ch]; ok {
			stops[ch] = s
		}
	}
	// one pwm writer at a time: serve, test and validate share this lock
	release, lockCheck, err := acquireWriterLockChecked(runDir(), "validate")
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate: %v\n", err)
		return exitFail
	}
	defer release()
	// third refusal (TESTSPEC 0.4 §3, #10): an earlier run left a channel
	// unrestored, or its state cannot be read - no bundle
	if why := openRestoreState(); why != "" {
		fmt.Fprintf(os.Stderr, "validate: %s\n", why)
		fmt.Fprintln(os.Stderr, "          Check the channels (systemctl start n5-fangov, or n5-fangov failsafe), then remove the state file. Nothing was run or written.")
		return exitFail
	}
	probe := newG1Probe(hw, n5driver.Read(hw.Root).HwmonPath, lockCheck)
	env := &liveEnv{dev: dev, hw: hw, factory: newSensorFactory(hw, dev), stops: stops, dcrPath: n5driver.DCRPath(hw.Root), probe: probe}
	env.init()
	v.env, v.ch = env, chn.Index
	v.cfg = validate.SpecConfig(chn.Index, tachOf(chn.Index), []int{1, 2, 3})
	v.cfg.Guard = func() string { return strings.Join(append(validateOtherWriters(), probe.check()...), ", ") }
	v.stops = stops
	v.probe = probe
	return v.run()
}

// prepareValidateOut creates --out with its parents and checks that it is
// writable (validate.PrepareOut). On an error it says so and the run ends
// before the gates, with nothing written (maintenance window 1, D1-b).
func prepareValidateOut(out string) bool {
	if err := validate.PrepareOut(out); err != nil {
		fmt.Fprintf(os.Stderr, "validate: %v\n", err)
		fmt.Fprintln(os.Stderr, "          The result bundle could not be stored; nothing was run or written. Choose another --out.")
		return false
	}
	return true
}

// validateStops is the stop value Restore writes per channel: the one the
// daemon's own stop uses (testStopPolicy), raised to validate.MinDuty when
// it is a fixed duty below that. validate never writes a duty below 100
// (TESTSPEC §4), not even when restoring; the daemon sets its own values
// again when it runs.
func validateStops(dev profile.Device, chans []chanSpec) map[int]string {
	stops := map[int]string{}
	for _, c := range dev.Channels() {
		s := testStopPolicy(dev.Profile().Name(), chans, c.Index)
		if d, err := strconv.Atoi(s); err == nil && d < validate.MinDuty {
			s = strconv.Itoa(validate.MinDuty)
		}
		stops[c.Index] = s
	}
	return stops
}

func tachOf(ch int) int {
	if ch >= 1 && ch <= 3 {
		return ch
	}
	return 0
}

// validation is one run of cmdValidate.
type validation struct {
	hw      *hwmon.FS
	env     validate.Env
	cfg     validate.Config
	ch      int
	want    map[string]bool
	boot    string
	out     string
	dry     bool
	console bool
	session session
	stops   map[int]string // restore value per channel (hardware runs)
	probe   *g1Probe       // G1 measurement beyond systemd (hardware runs)
	marker  bool           // validateMarkerName written by this run
}

// writing reports whether a selected test writes to the EC. On pwm4 only T1
// writes: T2/T3 are N/A there and RunHandback writes nothing (TESTSPEC §6).
func (v *validation) writing() bool {
	if v.ch == validate.PCIeChannel {
		return v.want["T1"]
	}
	return v.want["T1"] || v.want["T2"] || v.want["T3"]
}

func (v *validation) run() (rc int) {
	// A write to a closed stdout (`validate ... | tee log` and the reader
	// killed by the same Ctrl-C) must fail with EPIPE instead of killing
	// the process by SIGPIPE before the channels are restored; Ctrl-Z must
	// not stop it. The stop signals are caught after the prompt
	// (stopContext).
	ignoreRunSignals()
	id := collectIdentity(v.hw)
	started := v.env.Now()
	mode := "hardware"
	if v.dry {
		mode = "dry-run (simulated EC; nothing is written)"
	}
	fmt.Printf("=== n5-fangov validate  pwm%d  TESTSPEC %s  %s  %s ===\n", v.ch, validate.SpecVersion, mode, started.Format(time.RFC3339))
	fmt.Printf("machine: %s, driver %s %s, hardware status %s\n", id.DMI.Key(), n5driver.ModuleName, orDash(id.Driver.Version), id.HWStatus.Status)

	r := validate.NewRunner(v.env, v.cfg)
	dcrOK, dcrErr := r.DCRAvailable()

	if !v.dry && v.writing() {
		fmt.Println()
		fmt.Println("This command WRITES duty values to the EC (one channel, duty >= 100 only).")
		fmt.Println("Every channel it touches is put back into its stop state at the end, on abort and on a signal.")
		fmt.Println("Limits (abort): CPU Tctl 85 C, NVMe 70 C, HDD 50 C, a tach at 0 rpm.")
		if !dcrOK {
			fmt.Printf("Duty registers not readable (%v): T1-T3 are skipped, nothing will be written.\n", dcrErr)
		} else if v.ch == validate.PCIeChannel {
			fmt.Printf("Duration: about %s for the gate window and T1; T2/T3 are N/A on pwm4 (%s).\n",
				v.cfg.Window+time.Duration(len(validate.T1Steps)+1)*v.cfg.Hold, validate.ReasonNoPCIeAuto)
		} else {
			fmt.Printf("Duration: about %s for the gate window and T1, %s for T2/T3 (%d runs).\n",
				v.cfg.Window+time.Duration(len(validate.T1Steps)+1)*v.cfg.Hold,
				time.Duration(v.cfg.Repeat)*2*(v.cfg.Hold+v.cfg.HandbackObserve())+v.cfg.Observe, v.cfg.Repeat)
		}
		for _, ch := range sortedKeys(v.stops) {
			fmt.Printf("Restore value pwm%d: %s\n", ch, v.stops[ch])
		}
		fmt.Printf("Type exactly  %s  to continue: ", validateConfirm)
		line, _ := bufio.NewReader(validateStdin).ReadString('\n')
		if strings.TrimSpace(line) != validateConfirm {
			fmt.Println("\nAborted, nothing written.")
			return exitFail
		}
		r.Event("operator confirmation typed at %s (console flag %v, local console %v, remote: %s)", v.session.stdin, v.console, v.session.console, orDash(strings.Join(v.session.remote, "; ")))
		// the state file before anything can be written: removed only when
		// every channel is restored (TESTSPEC 0.4 §3 third refusal, #10)
		if err := writeValidateMarker(v.ch, v.stops, v.out); err != nil {
			fmt.Fprintf(os.Stderr, "validate: state file %s: %v - nothing written\n", validateMarkerPath(), err)
			return exitFail
		}
		v.marker = true
	}

	// SIGINT/TERM/HUP/QUIT cancel the run; every path below restores
	// (r.Close) before it prints
	ctx, cancel := stopContext()
	defer cancel()
	defer func() {
		p := recover()
		r.Close() // every exit path: channels back to their stop values, before any output
		if p != nil {
			r.Event("PANIC %v", p)
			fmt.Printf("!! panic: %v\n", p)
			rc = exitFail
		}
	}()
	// closeOnAbort restores at once when the run stopped early, before
	// anything else is printed
	closeOnAbort := func() bool {
		if r.Abort != "" || ctx.Err() != nil {
			r.Close()
			return true
		}
		return false
	}

	// T6 + gates: no writes
	// the limits of TESTSPEC §4 apply to this sample too: an abort here
	// stops the run before the gate window and before any write
	first, firstAbort := r.Sample("boot-state", -1)
	if firstAbort != "" {
		r.Stop(firstAbort)
	}
	var gates []validate.Gate
	gates = append(gates, v.gateG1(), v.gateG2())
	g7 := validate.Gate{ID: "G7", Condition: "board in the driver's write table, sys_vendor as measured for that board, module not loaded with force", Result: "SKIPPED"}
	switch {
	case v.dry:
		g7.Detail = "dry-run, nothing written"
	case !v.writing():
		g7.Detail = "no writing test selected"
	default:
		g7 = gateG7(id)
	}
	// G3/G4 are SKIPPED without a writing test, without readable duty
	// registers and after a failed G7 (TESTSPEC 0.4 §3, #12)
	g3, g4 := validate.Gate{ID: "G3", Result: "SKIPPED"}, validate.Gate{ID: "G4", Result: "SKIPPED"}
	switch {
	case !v.writing():
		g3.Detail, g4.Detail = "no writing test selected", "no writing test selected"
	case !dcrOK:
		g3.Detail, g4.Detail = "duty registers not readable: nothing is written", "duty registers not readable: nothing is written"
	case g7.Result == "FAIL":
		g3.Detail, g4.Detail = "G7 failed: nothing is written", "G7 failed: nothing is written"
	default:
		fmt.Printf("gate G3/G4: sampling %s without writes ...\n", v.cfg.Window)
		g3, g4 = r.Gates(ctx)
	}
	g5 := validate.Gate{ID: "G5", Condition: "operator at the device", Result: "PASS", Detail: "confirmation typed at " + v.session.stdin}
	switch {
	case v.dry:
		g5.Result, g5.Detail = "SKIPPED", "dry-run, nothing written"
	case !v.writing():
		g5.Result, g5.Detail = "SKIPPED", "no writing test selected"
	case len(v.session.remote) > 0:
		g5.Detail += ", remote session (" + strings.Join(v.session.remote, "; ") + ") with --i-am-at-the-console"
	case !v.session.console:
		g5.Detail += ", no local console in the process chain, --i-am-at-the-console given"
	default:
		g5.Detail += ", local console"
	}
	g6, ecTables := gateG6(v.hw.Root, dcrOK, dcrErr)
	gates = append(gates, g3, g4, g5, g6, g7)
	closeOnAbort() // a signal in the gate window: restore before the gate lines
	for _, g := range gates {
		r.Event("gate %s %s: %s", g.ID, g.Result, g.Detail)
		fmt.Printf("gate %-2s %-8s %s\n", g.ID, g.Result, g.Detail)
	}
	// G6 is not blocking: without the duty registers T1-T3 write nothing
	// anyway, and the curve tables have no safe read path (TESTSPEC §3 G6).
	blocked := ""
	for _, g := range gates {
		if g.Result == "FAIL" && g.ID != "G6" {
			blocked = g.ID + " failed: " + g.Detail
			break
		}
	}
	writeSkipped := v.writing() && (blocked != "" || !dcrOK)

	var results []validate.Result
	if v.want["T0"] {
		results = append(results, v.t0(id))
	}
	if v.want["T6"] {
		results = append(results, validate.Result{ID: "T6", Verdict: validate.Reported,
			Reasons: []string{fmt.Sprintf("snapshot before any write by this run; uptime %.0f s, boot type %q; writes earlier in this boot are not visible", id.UptimeS, v.boot)},
			Values:  map[string]any{"sample": sampleMap(first), "dcr": id.DCR, "pwm_cache": pwmCache(v.hw, id.Driver)}})
	}
	// notRun is a writing test that did not run; on pwm4 T2/T3 are N/A
	// whatever stopped the run (no EC automatic on DCR5, TESTSPEC §6)
	notRun := func(t, reason string) validate.Result {
		if v.ch == validate.PCIeChannel && (t == "T2" || t == "T3") {
			return validate.NotApplicableResult(t, v.ch, validate.ReasonNoPCIeAuto)
		}
		return validate.NotImplemented(t, v.ch, reason)
	}
	if blocked != "" && v.writing() {
		reason := "not run: gate " + blocked
		for _, t := range []string{"T1", "T2", "T3"} {
			if v.want[t] {
				results = append(results, notRun(t, reason))
			}
		}
	} else {
		if v.want["T1"] && !closeOnAbort() {
			fmt.Println("T1 mapping ...")
			results = append(results, r.RunT1(ctx))
		} else if v.want["T1"] {
			results = append(results, validate.NotImplemented("T1", v.ch, "not run: "+orText(r.Abort, "cancelled (signal)")))
		}
		if (v.want["T2"] || v.want["T3"]) && !closeOnAbort() {
			fmt.Println("T2/T3 hand-back and settling ...")
			t2, t3 := r.RunHandback(ctx)
			if v.want["T2"] {
				results = append(results, t2)
			}
			if v.want["T3"] {
				results = append(results, t3)
			}
		} else {
			for _, t := range []string{"T2", "T3"} {
				if v.want[t] {
					results = append(results, notRun(t, "not run: "+orText(r.Abort, "cancelled (signal)")))
				}
			}
		}
		closeOnAbort()
	}
	if v.want["C1"] {
		// C1 is decided with the module unloaded (f8a mode A, TESTSPEC 0.4
		// §5.3); the driver's ecram becomes a C1 path only after a
		// byte-compare against f8a (#9-C1 a)
		why := "C1 is decided with the module unloaded: f8a-snapshot.sh before and after the run (TESTSPEC §5.3); validate runs with the driver loaded"
		if ecTables != nil {
			why += "; the curve tables read through the driver's ecram (G6) are no C1 path before the byte-compare against f8a mode A (TESTSPEC 0.4 #9)"
		} else {
			why += " and without the driver's ecram file"
		}
		results = append(results, validate.NotImplemented("C1", 0, why))
	}
	// T4/T5 are judged on the tach target band as a function of the table
	// input (TESTSPEC §5.5, §6) and are run by hand; validate reports why it
	// has no verdict (pwm3: EC 0x06 below the 34 C step), N/A on pwm4
	for _, t := range []string{"T4", "T5"} {
		if v.want[t] {
			results = append(results, validate.T4T5Result(t, v.ch, r.Samples))
		}
	}
	r.Close()
	unrestored := r.Unrestored()

	finished := v.env.Now()
	// restore values as used: the configured/--restore value, or 255 after
	// a temperature abort (TESTSPEC 0.4 §4, #13)
	restoreValues := map[int]string{}
	for ch, s := range v.stops {
		restoreValues[ch] = s
	}
	for ch, s := range r.RestoreOverrides {
		restoreValues[ch] = s
	}
	if v.marker {
		if len(unrestored) == 0 {
			if err := os.Remove(validateMarkerPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
				r.Event("state file %s not removed: %v", validateMarkerPath(), err)
			}
		} else {
			r.Event("state file %s kept: channels not restored; the next run is refused until it is removed", validateMarkerPath())
		}
	}
	red := bundleRedactor(v.hw.Root, id, os.Getenv)
	meta := map[string]any{
		"redaction":     validate.RedactionRules,
		"settle_time_s": map[string]float64{"pwm1": validate.SettleTime(1).Seconds(), "pwm2": validate.SettleTime(2).Seconds(), "pwm3": validate.SettleTime(3).Seconds(), "pwm4": validate.SettleTime(4).Seconds()},
		"grid":          map[string]any{"sample_s": v.cfg.Sample.Seconds(), "tolerance_s": validate.GridTolerance.Seconds(), "gaps": r.Gaps},
		"result_role":   resultRole,
		"ec_ram_tables": ecTables,
		"spec_version":  validate.SpecVersion, "tool": "n5-fangov", "tool_version": version.Version,
		"mode": mode, "channel": v.ch, "started": started.UTC().Format(time.RFC3339), "finished": finished.UTC().Format(time.RFC3339),
		"boot_type": v.boot, "identity": id, "gates": gates, "aborted": r.Abort,
		"timing": map[string]any{"hold_s": v.cfg.Hold.Seconds(), "sample_s": v.cfg.Sample.Seconds(), "observe_s": v.cfg.Observe.Seconds(),
			"handback_observe_s": v.cfg.HandbackObserve().Seconds(), "gate_window_s": v.cfg.Window.Seconds(), "repeat": v.cfg.Repeat},
		"limit_sensors":    r.LimitSensors,
		"restore_values":   restoreValues,
		"restore_failed":   unrestored,
		"restore_warnings": r.RestoreWarnings,
		"session":          map[string]any{"stdin": v.session.stdin, "local_console": v.session.console, "remote": v.session.remote, "console_flag": v.console},
		"spec_deviations":  specDeviations,
	}
	verdict := map[string]any{"spec_version": validate.SpecVersion, "channel": v.ch, "tests": results}
	dir := filepath.Join(v.out, validate.DirName(started, id.DMI.Board, id.DMI.BIOS, fmt.Sprintf("validate-pwm%d", v.ch)))
	if v.dry {
		dir += "-dryrun"
	}
	b := validate.Bundle{Meta: meta, Samples: r.Samples, Events: r.Events, Verdict: verdict, Redact: red.String}
	if err := b.Write(dir); err != nil {
		fmt.Fprintf(os.Stderr, "validate: bundle: %v\n", err)
		rc = exitFail
	} else {
		fmt.Printf("\nbundle: %s\n", dir)
	}
	fmt.Println()
	for _, res := range results {
		line := ""
		if len(res.Reasons) > 0 {
			line = res.Reasons[0]
		}
		fmt.Printf("%-3s %-12s %s\n", res.ID, res.Verdict, line)
	}
	for _, w := range r.RestoreWarnings {
		fmt.Printf("\nWARNING: restore %s\n", w)
	}
	if len(unrestored) > 0 {
		// loud on both streams: a channel left at a test duty must not go
		// unnoticed
		for _, u := range unrestored {
			msg := fmt.Sprintf("!! RESTORE FAILED %s - set the channel by hand (pwmN_enable = 2, or the stop duty) or start the daemon", u)
			fmt.Println(msg)
			fmt.Fprintln(os.Stderr, "validate: "+msg)
		}
		rc = exitFail
	}
	if r.Abort != "" {
		fmt.Printf("\nABORTED: %s\n", r.Abort)
		return exitFail
	}
	if writeSkipped {
		fmt.Printf("\nWriting tests not run: %s\n", orText(blocked, "duty registers not readable ("+errText(dcrErr)+")"))
		return exitFail
	}
	return rc
}

// specDeviations are the places where this tool knowingly differs from
// TESTSPEC validate.SpecVersion (§1 rule 6); every bundle carries the list
// in meta.json, empty when there is none. Under 0.4 the list is empty: G4
// (#20), the settle time (#21), the 5-s grid (#11), the crossing rules
// (#17, #24), G7, the third refusal, the redaction and the restore after a
// temperature abort are implemented; the two deviations #27 (a) names for
// a validate frozen at 0.3 are therefore not needed. T4/T5 are evaluated
// by n5-ectest report (§1 rule 4), C1 by f8a mode A (§5.3): validate
// reports both INCONCLUSIVE with that reason, which is spec text.
var specDeviations = []string{}

// resultRole states what a validate bundle is under TESTSPEC 0.4: §1 rule 4
// names n5-ectest as the tool, §9 (#27) uses validate as the second
// implementation of the D4 cross-check.
const resultRole = "cross-check (TESTSPEC 0.4 §1 rule 4, §9 #27): second implementation for D4; the tool of record for G1-G7, T0-T5 is n5-ectest"

// t0 checks the identity fields of TESTSPEC §2.
func (v *validation) t0(id machineIdentity) validate.Result {
	fields := map[string]string{
		"product_name": id.DMI.Product, "board_name": id.DMI.Board, "board_version": id.DMI.BoardVersion,
		"bios_version": id.DMI.BIOS, "bios_date": id.DMI.BIOSDate, "ec_firmware_release": id.DMI.EC, "sys_vendor": id.DMI.SysVendor,
		"kernel": id.Kernel, "driver_version": id.Driver.Version, "driver_srcversion": id.Driver.SrcVersion,
		"tool_version": version.Version, "spec_version": validate.SpecVersion, "boot_type": v.boot,
	}
	if id.UptimeS > 0 {
		fields["uptime"] = strconv.FormatFloat(id.UptimeS, 'f', 0, 64)
	} else {
		fields["uptime"] = ""
	}
	if len(id.Driver.Params) > 0 {
		fields["driver_parameters"] = "present"
	} else {
		fields["driver_parameters"] = ""
	}
	var missing []string
	for _, k := range []string{"product_name", "board_name", "board_version", "sys_vendor", "bios_version", "bios_date", "ec_firmware_release", "kernel", "driver_version", "driver_srcversion", "driver_parameters", "tool_version", "spec_version", "boot_type", "uptime"} {
		if fields[k] == "" {
			missing = append(missing, k)
		}
	}
	res := validate.Result{ID: "T0", Values: fields}
	if len(missing) > 0 {
		res.Verdict = validate.Inconclusive
		res.Reasons = []string{"missing: " + strings.Join(missing, ", ")}
		if v.boot == "" {
			res.Reasons = append(res.Reasons, "give --boot cold|warm")
		}
	} else {
		res.Verdict = validate.Pass
	}
	return res
}

// gateG1: no other pwm writer.
func (v *validation) gateG1() validate.Gate {
	g := validate.Gate{ID: "G1", Condition: "no other pwm writer: n5-fangov (also restarting), n5-fand, fancontrol inactive by systemd state, process (comm/argv[0]) and open pwm files; pwm cache as expected and writer lock held, checked at every sample"}
	if v.dry {
		g.Result, g.Detail = "SKIPPED", "dry-run"
		return g
	}
	if active := append(validateOtherWriters(), v.probe.check()...); len(active) > 0 {
		g.Result, g.Detail = "FAIL", "active or (re)starting: "+strings.Join(active, ", ")
	} else {
		g.Result, g.Detail = "PASS", "none active (systemd units, processes, open pwm files, pwm cache, writer-lock inode); checked again at every sample, writer lock held"
	}
	return g
}

// gateG2: no scrub, resilver or backup running.
func (v *validation) gateG2() validate.Gate {
	g := validate.Gate{ID: "G2", Condition: "no scrub/resilver/backup"}
	if v.dry {
		g.Result, g.Detail = "SKIPPED", "dry-run"
		return g
	}
	var found []string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	zs, zerr := exec.CommandContext(ctx, "zpool", "status").Output()
	switch {
	case zerr != nil && errors.Is(zerr, exec.ErrNotFound):
		g.Detail = "no zpool; "
	case zerr != nil:
		found = append(found, "zpool status failed: "+zerr.Error())
	default:
		s := string(zs)
		for _, k := range []string{"scrub in progress", "resilver in progress"} {
			if strings.Contains(s, k) {
				found = append(found, k)
			}
		}
	}
	if ents, err := os.ReadDir(procRoot); err == nil {
		for _, e := range ents {
			if _, err := strconv.Atoi(e.Name()); err != nil {
				continue
			}
			if what := backupProcess(readProc(e.Name(), "comm"), readProcRaw(e.Name(), "cmdline")); what != "" {
				found = append(found, what+" (pid "+e.Name()+")")
			}
		}
	}
	if len(found) > 0 {
		g.Result, g.Detail = "FAIL", g.Detail+strings.Join(found, ", ")
	} else {
		g.Result, g.Detail = "PASS", g.Detail+"no scrub, resilver, vzdump or proxmox-backup-client"
	}
	return g
}

// backupNames are the backup programs G2 looks for (TESTSPEC §3).
var backupNames = []string{"vzdump", "proxmox-backup-client"}

// backupProcess names a backup process from /proc/<pid>/comm and the raw
// /proc/<pid>/cmdline, "" when it is none. comm alone is not enough: it is
// cut to 15 characters ("proxmox-backup-", which a PBS daemon shares), and a
// PVE worker renames itself to "task UPID:<node>:...:<type>:<id>:<user>:"
// (PVE::RESTEnvironment, $0 = "task $upid"), so a vzdump job started by the
// scheduler or the GUI shows comm "task UPID:<node>". The command line keeps
// the program path (also behind an interpreter: perl -T /usr/bin/vzdump)
// and the full worker title with the task type.
func backupProcess(comm, cmdline string) string {
	for _, n := range backupNames {
		if comm == n {
			return "process " + n
		}
	}
	args := strings.Split(strings.TrimRight(cmdline, "\x00"), "\x00")
	for i, a := range args {
		if strings.HasPrefix(a, "task UPID:") {
			// "task UPID:node:pid:pstart:starttime:type:id:user:"
			if f := strings.Split(strings.TrimPrefix(a, "task "), ":"); len(f) > 5 {
				for _, n := range backupNames {
					if f[5] == n {
						return "PVE task " + n
					}
				}
			}
			continue
		}
		if i > 2 { // program, interpreter flags, script: the first three suffice
			continue
		}
		base := a
		if k := strings.LastIndexByte(base, '/'); k >= 0 {
			base = base[k+1:]
		}
		for _, n := range backupNames {
			if base == n {
				return "process " + n
			}
		}
	}
	return ""
}

// readProcRaw reads a /proc file without trimming (cmdline keeps its NULs).
func readProcRaw(rel ...string) string {
	b, err := os.ReadFile(filepath.Join(append([]string{procRoot}, rel...)...))
	if err != nil {
		return ""
	}
	return string(b)
}

func pwmCache(hw *hwmon.FS, st n5driver.State) map[string]string {
	out := map[string]string{}
	if st.HwmonPath == "" {
		return out
	}
	for ch := 1; ch <= 4; ch++ {
		for _, a := range []string{fmt.Sprintf("pwm%d", ch), fmt.Sprintf("pwm%d_enable", ch)} {
			if s, err := hw.ReadString(filepath.Join(st.HwmonPath, a)); err == nil {
				out[a] = s
			}
		}
	}
	return out
}

func sampleMap(s validate.Sample) map[string]any {
	m := map[string]any{"t": s.Time.UTC().Format(time.RFC3339), "dcr1": s.DCR[0], "dcr2": s.DCR[1], "dcr3": s.DCR[2], "dcr4": s.DCR[3], "dcr5": s.DCR[4],
		"fan1": s.Fan[0], "fan2": s.Fan[1], "fan3": s.Fan[2]}
	for k, x := range map[string]float64{"ec_cpu": s.ECCPU, "ec_sys": s.ECSys, "ec_board": s.ECBoard, "ec_amb": s.ECAmb, "hdd_max": s.HDDMax, "nvme_max": s.NVMeMax, "tctl": s.Tctl, "load1": s.Load1} {
		if !math.IsNaN(x) {
			m[k] = x
		}
	}
	return m
}

func errText(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

// validateReplay prints T2/T3 computed from raw logs (no hardware).
func validateReplay(pwmPath, i2ecPath string) int {
	pwm, err := os.ReadFile(pwmPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		return exitFail
	}
	i2ec, err := os.ReadFile(i2ecPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		return exitFail
	}
	res, err := validate.Replay(string(pwm), string(i2ec))
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		return exitFail
	}
	b, _ := json.MarshalIndent(map[string]any{"spec_version": validate.SpecVersion, "mode": "replay", "sources": []string{pwmPath, i2ecPath}, "tests": res}, "", "  ")
	fmt.Println(string(b))
	return exitOK
}

// ---- real hardware environment ----------------------------------------

// liveEnv is validate.Env on the N5 Pro through the n5pro profile.
type liveEnv struct {
	dev     profile.Device
	hw      *hwmon.FS
	factory sensorFactory
	stops   map[int]string
	dcrPath string

	tctl, nvme, hdd sensor.Source
	ec              map[string]string
	probe           *g1Probe // G1: re-read the pwm cache after every own operation
}

func (e *liveEnv) init() {
	if s, err := e.factory("k10temp"); err == nil {
		e.tctl = s
	}
	if s, err := e.factory("nvme:max"); err == nil {
		e.nvme = s
	}
	if s, err := e.factory("drivetemp:max"); err == nil {
		e.hdd = s
	}
	e.ec = e.dev.ExtraTemps()
}

func (e *liveEnv) Now() time.Time                                  { return validateNow() }
func (e *liveEnv) Sleep(ctx context.Context, d time.Duration) bool { return validateSleep(ctx, d) }
func (e *liveEnv) EnterManual(ch int) error {
	defer e.probe.rebase()
	return e.dev.EnterManual(ch)
}

// WriteDuty writes through the profile. The EC register is not compared
// here: the samples record it and the test judges it (T1), after the hold.
func (e *liveEnv) WriteDuty(ch, duty int) error {
	defer e.probe.rebase()
	err := e.dev.WriteDuty(ch, duty)
	if errors.Is(err, profile.ErrDCRMismatch) {
		return nil
	}
	return err
}
func (e *liveEnv) Auto(ch int) error {
	defer e.probe.rebase()
	return e.dev.SafeStop(ch, "auto")
}

// Restore writes the channel's stop value (validateStops: never below
// 100). A fixed duty is then compared with the EC register until it shows
// the value or the channel's settle time has passed (profile.WaitRegister;
// the HDD channel ramps DCR3/DCR4 for about 4.6 s, maintenance window 1);
// a register that still differs is reported as
// validate.ErrRestoreUnverified, not hidden.
func (e *liveEnv) Restore(ch int) error {
	defer e.probe.rebase()
	stop := e.stops[ch]
	if stop == "" {
		stop = "auto"
	}
	err := e.dev.SafeStop(ch, stop)
	if errors.Is(err, profile.ErrDCRMismatch) {
		err = nil
	}
	if err != nil {
		return err
	}
	want, cerr := strconv.Atoi(stop)
	rv, ok := e.dev.(profile.RegisterVerifier)
	if cerr != nil || !ok || rv.RegisterSettle(ch) <= 0 {
		return nil
	}
	sleep := func(d time.Duration) { validateSleep(context.Background(), d) }
	// read-back after the settle time of TESTSPEC 0.4 §2.1 (#21), not the
	// daemon's shorter one
	if verr := profile.WaitRegister(rv, ch, want, validateNow().Add(validate.SettleTime(ch)), validateNow, sleep); verr != nil {
		return fmt.Errorf("%w: %v", validate.ErrRestoreUnverified, verr)
	}
	return nil
}

func (e *liveEnv) DCR() (n5driver.DCR, error) { return n5driver.ReadDCR(e.dcrPath) }

func (e *liveEnv) temp(s sensor.Source) float64 {
	if s == nil {
		return math.NaN()
	}
	v, err := readTempC(s)
	if err != nil {
		return math.NaN()
	}
	return v
}

func (e *liveEnv) ecTemp(id string) float64 {
	p, ok := e.ec[id]
	if !ok {
		return math.NaN()
	}
	mc, err := readIntPath(e.hw, p)
	if err != nil {
		return math.NaN()
	}
	return float64(mc) / 1000
}

func (e *liveEnv) Read() validate.Readings {
	rd := validate.Readings{Fan: map[int]int{}, Tctl: e.temp(e.tctl), NVMeMax: e.temp(e.nvme), HDDMax: e.temp(e.hdd),
		ECCPU: e.ecTemp("ec:cpu"), ECSys: e.ecTemp("ec:system"), ECBoard: e.ecTemp("ec:board"), ECAmb: e.ecTemp("ec:ambient"), Load1: math.NaN()}
	for f := 1; f <= 3; f++ {
		if v, err := e.dev.ReadRPM(f); err == nil {
			rd.Fan[f] = v
		} else {
			rd.Fan[f] = -1
		}
	}
	if fl := strings.Fields(readProc("loadavg")); len(fl) > 0 {
		if x, err := strconv.ParseFloat(fl[0], 64); err == nil {
			rd.Load1 = x
		}
	}
	return rd
}
