package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SirRenix/ventula/internal/control"
	"github.com/SirRenix/ventula/internal/hwmon"
	"github.com/SirRenix/ventula/internal/profile"
	"github.com/SirRenix/ventula/internal/version"
)

func init() {
	register("serve", command{run: cmdServe})
}

// cmdServe runs the daemon: config → profile → sensors → controller → API
// (unix socket always, TCP when [web].listen is set) → READY=1 after the
// first cycle → wait for SIGTERM/SIGINT → SafeStop.
//
// Exit codes: 0 clean stop, 1 fatal setup or runtime error, 2 usage.
func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	dryRun := fs.Bool("dry-run", false, "read sensors and log decisions, never write pwm")
	rdir := fs.String("run-dir", runDir(), "runtime directory (socket, state.json, alert stamps)")
	listen := fs.String("listen", "", "override [web].listen (\"none\" disables the TCP listener)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "serve: unexpected arguments")
		return exitUsage
	}

	// journald adds timestamps; keep the lines bare.
	log.SetFlags(0)
	log.SetOutput(os.Stdout)
	log.Printf("ventula %s starting (config %s, run-dir %s, dry-run %v)", version.Version, *cfgPath, *rdir, *dryRun)

	alerter := newAlerter()

	// The run dir first: the start-up alerts below keep their cooldown
	// stamps there (a restart loop must not send one notification per try).
	if err := os.MkdirAll(*rdir, 0o755); err != nil {
		log.Printf("run-dir %s: %v", *rdir, err)
		return exitFail
	}

	cfg, warns, err := loadConfig(*cfgPath)
	for _, w := range warns {
		log.Printf("config: %s", w)
	}
	if len(warns) > 0 {
		sendAlertCooled(*rdir, alerter, "config", fmt.Sprintf("%d config problem(s), built-in defaults in effect:\n%s",
			len(warns), strings.Join(warns, "\n")))
	}
	if err != nil {
		// Read or syntax error: defaults are in cfg, keep going (rule 8).
		log.Printf("config: continuing with built-in defaults: %v", err)
	}
	dspec := daemonOf(cfg)
	chans := channelSpecs(cfg)
	if len(chans) == 0 {
		log.Printf("config: no [[channel]] tables; nothing to regulate (monitoring only, N5 Pro: built-in channels)")
	}

	hw := hwmon.New()
	dev, err := detectDevice(hw, dspec.Profile)
	if err != nil {
		log.Printf("profile %q: %v", dspec.Profile, err)
		sendAlertCooled(*rdir, alerter, "profile", fmt.Sprintf("fan controller not detected (profile %q): %v\nNo regulation is running; fans stay in BIOS/EC control.", dspec.Profile, err))
		return exitFail
	}
	p := dev.Profile()
	log.Printf("profile %s (%s) at %s, %d channel(s), verified=%v", p.Name(), p.Title(), dev.HwmonPath(), len(dev.Channels()), p.Verified())
	if !p.Verified() {
		log.Printf("WARNING: profile %s is from documentation only, not verified on hardware", p.Name())
	}
	for _, c := range chans {
		if !hasChannel(dev, c.PWM) {
			log.Printf("config: channel %q uses pwm%d which profile %s does not expose", c.Name, c.PWM, p.Name())
		}
	}

	factory := newSensorFactory(hw, dev)
	ctrl, err := newController(cfg, dev, factory, alerter, controlOpts{DryRun: *dryRun, RunDir: *rdir})
	if err != nil {
		log.Printf("controller: %v", err)
		sendAlertCooled(*rdir, alerter, "start", "ventula could not start the controller: "+err.Error())
		return exitFail
	}

	ws := newWebServer(webDeps{
		Service:    ctrl,
		ConfigPath: *cfgPath,
		PresetDir:  defaultPresetDir,
		Device:     dev,
		Sysfs:      hw,
		Web:        webOf(cfg),
	})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	errc := make(chan error, 3)

	// Unix socket (CLI, no auth/CSRF).
	sock := socketPath(*rdir)
	go func() { errc <- wrapErr("ipc", serveIPC(ctx, sock, ws.Socket)) }()

	// TCP (web UI), optional.
	addr := webOf(cfg).Listen
	if *listen != "" {
		addr = *listen
	}
	if addr == "none" {
		addr = ""
	}
	if addr != "" {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Printf("web: listen %s: %v (web UI disabled, socket still works)", addr, err)
			sendAlert(alerter, "web", "web UI listener failed on "+addr+": "+err.Error())
		} else {
			log.Printf("web: listening on %s", ln.Addr())
			go func() { errc <- wrapErr("web", ws.ServeTCP(ctx, ln)) }()
		}
	}

	// Regulation loop. Run performs the SafeStop itself when it returns.
	ctrlDone := make(chan error, 1)
	go func() {
		err := wrapErr("controller", runController(ctx, ctrl))
		ctrlDone <- err
		errc <- err
	}()

	// Watchdog pings independent of the cycle: the loop sends WATCHDOG=1
	// once per cycle, but the interval may be up to 30 s against
	// WatchdogSec=60, so a single slow sensor read would already cost a
	// restart. The ticker pings every watchdogPing while the loop is alive
	// (last cycle finished within watchdogSlack × interval) and falls
	// silent when it is stuck, so the watchdog still fires then.
	go func() {
		t := time.NewTicker(watchdogPing)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if watchdogAlive(controllerLastCycle(ctrl), controllerInterval(ctrl), now) {
					notifyWatchdog()
				}
			}
		}
	}()

	// READY=1 once the first cycle produced a snapshot (or after a grace
	// period, so a stuck first read still lets systemd's watchdog take over
	// instead of TimeoutStartSec killing a half-started daemon).
	go func() {
		grace := 2*dspec.Interval + 5*time.Second
		if waitFirstCycle(ctx, ctrl, grace) {
			log.Printf("first cycle done, READY")
		} else if ctx.Err() == nil {
			log.Printf("first cycle not finished after %s, signalling READY anyway", grace)
		}
		if ctx.Err() == nil {
			notifyReady()
		}
	}()

	exit := exitOK
	select {
	case <-ctx.Done():
		log.Printf("signal received, stopping")
	case err := <-errc:
		if err != nil && ctx.Err() == nil {
			log.Printf("fatal: %v", err)
			exit = exitFail
		}
		cancel()
	}
	notifyStopping()

	// Wait for the loop to leave its cycle and run its own SafeStop; if it
	// does not come back in time, stop from here (idempotent).
	select {
	case <-ctrlDone:
	case <-time.After(15 * time.Second):
		log.Printf("controller did not stop within 15s, forcing safe stop")
	}
	stopController(ctrl)
	log.Printf("stopped (exit %d)", exit)
	return exit
}

// watchdogPing is how often serve sends WATCHDOG=1 on its own while the
// loop is alive; watchdogSlack × interval is how long a cycle may be
// overdue before the loop counts as stuck.
const (
	watchdogPing  = 10 * time.Second
	watchdogSlack = 3
)

// watchdogAlive reports whether the loop finished a cycle recently enough
// (within watchdogSlack × interval before now) for serve to keep pinging
// the systemd watchdog on the loop's behalf. Zero lastCycle (no cycle yet)
// is not alive: before READY=1 the watchdog is not armed, after it a
// never-finishing first cycle must trip it.
func watchdogAlive(lastCycle time.Time, interval time.Duration, now time.Time) bool {
	if lastCycle.IsZero() || interval <= 0 {
		return false
	}
	return now.Sub(lastCycle) <= watchdogSlack*interval
}

// waitFirstCycle polls the snapshot until the first cycle replaced the
// initial "starting" snapshot (which already carries a timestamp).
func waitFirstCycle(ctx context.Context, s control.Service, max time.Duration) bool {
	deadline := time.Now().Add(max)
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		if snap := s.Snapshot(); snap.TS > 0 && snap.Status != "starting" {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			if time.Now().After(deadline) {
				return false
			}
		}
	}
}

// hasChannel reports whether the device exposes pwm index idx.
func hasChannel(dev profile.Device, idx int) bool {
	for _, c := range dev.Channels() {
		if c.Index == idx {
			return true
		}
	}
	return false
}

func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}
