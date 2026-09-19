package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/logfile"
	"github.com/SirRenix/n5-fangov/internal/profile"
	"github.com/SirRenix/n5-fangov/internal/schedule"
	"github.com/SirRenix/n5-fangov/internal/version"
)

func init() {
	register("serve", command{run: cmdServe})
}

// cmdServe runs the daemon: config → profile → sensors → controller → API
// (unix socket always, TCP when [web].listen is set) → READY=1 after the
// first cycle → wait for SIGTERM/SIGINT → SafeStop. The work is done by
// four stages that share a serveState: serveConfig, serveDevice, serveWeb,
// serveRun (DESIGN "Layout").
//
// Exit codes: 0 clean stop, 1 fatal setup or runtime error, 2 usage.
func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	dryRun := fs.Bool("dry-run", false, "read sensors and log decisions, never write pwm")
	rdir := fs.String("run-dir", runDir(), "runtime directory (socket, state.json, alert stamps)")
	listen := fs.String("listen", "", "override [web].listen (\"none\" disables the TCP listener)")
	sdir := fs.String("state-dir", stateDir(), "state directory (sessions.json, tokens.json, alerts.json); unwritable = no persistence")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "serve: unexpected arguments")
		return exitUsage
	}
	// Relative paths would be taken from the working directory ("/" under
	// systemd, ProtectSystem=strict): make every directory flag absolute
	// once, here, like newTLSManager does for the config path.
	for _, p := range []*string{cfgPath, rdir, sdir} {
		if abs, err := filepath.Abs(*p); err == nil {
			*p = abs
		}
	}
	st := &serveState{cfgPath: *cfgPath, dryRun: *dryRun, rdir: *rdir, listen: *listen, sdir: *sdir}
	if !serveConfig(st) {
		return exitFail
	}
	if st.logFile != nil {
		defer st.logFile.Close()
	}
	if !serveDevice(st) {
		return exitFail
	}
	serveWeb(st)
	return serveRun(st)
}

// serveState is what the stages of cmdServe hand each other: the parsed
// flags, then what each stage produced for the next.
type serveState struct {
	// Flags (directory paths made absolute).
	cfgPath string
	dryRun  bool
	rdir    string // run dir (socket, state.json, alert stamps)
	listen  string // --listen override ("" = the file's [web].listen)
	sdir    string // state dir flag

	// serveConfig: config, alert chain, log file.
	cfg      config.Config
	dspec    daemonSpec
	chans    []chanSpec
	state    string // ensured state dir; "" = no persistence
	alertMgr *alertManager
	alerter  alert.Sink      // what serve and the controller deliver through
	store    logStore        // the log read side behind /api/log
	logFile  *logfile.Writer // nil without [log].file; closed by cmdServe

	// serveDevice: hardware and controller.
	hw      *hwmon.FS
	dev     profile.Device
	factory sensorFactory
	ctrl    *control.Controller

	// serveWeb: listener decision, certificate manager, stores, web server.
	wspec  webSpec
	addr   string // TCP listen address; "" = no TCP listener
	useTLS bool
	tlsMgr *tlsManager
	hosts  []string
	ws     webServer
	sched  *scheduler // preset schedules; Run starts after READY
}

// serveConfig sets up the logger, creates the run dir, loads the config
// (rule 8: problems are warnings, defaults stay in effect), builds the
// alert chain and opens the log file. false = fatal (run dir).
func serveConfig(st *serveState) bool {
	// journald adds timestamps; keep the lines bare.
	log.SetFlags(0)
	log.SetOutput(os.Stdout)
	log.Printf("n5-fangov %s starting (config %s, run-dir %s, state-dir %s, dry-run %v)", version.Version, st.cfgPath, st.rdir, st.sdir, st.dryRun)

	// The run dir first: the start-up alerts below keep their cooldown
	// stamps there (a restart loop must not send one notification per try).
	if err := os.MkdirAll(st.rdir, 0o755); err != nil {
		log.Printf("run-dir %s: %v", st.rdir, err)
		return false
	}

	cfg, warns, err := loadConfig(st.cfgPath)
	for _, w := range warns {
		log.Printf("config: %s", w)
	}

	// State dir (sessions, alert history) and the alert chain: the
	// configured transport behind a swappable sink (the panel and every
	// config reload re-apply [alert]), wrapped in the ring the panel reads.
	st.state = ensureStateDir(st.sdir)
	st.alertMgr = newAlertManager(st.cfgPath, alertsPath(st.state), cfg.Alert, nil)
	st.alerter = st.alertMgr.sink()
	log.Printf("alerts: transport %s (%s)%s, history %s", cfg.Alert.Transport, st.alertMgr.effective, transportNote(cfg.Alert), orMemory(alertsPath(st.state)))
	if len(warns) > 0 {
		// Asynchronous like the controller's alerts: a PVE::Notify delivery
		// may take 30 s and must not delay the first cycle and READY=1.
		startAlert(st.rdir, st.alerter, "config", fmt.Sprintf("%d config problem(s), built-in defaults in effect:\n%s",
			len(warns), strings.Join(warns, "\n")))
	}
	if err != nil {
		// Read or syntax error: defaults are in cfg, keep going (rule 8).
		log.Printf("config: continuing with built-in defaults: %v", err)
	}

	// Log file ([log].file): the standard logger writes to stdout (journald)
	// and, time-stamped, to the rotating file. A file that cannot be opened
	// is a warning, not a stop: the journal stays.
	st.store = journalLogStore{}
	if lspec := logOf(cfg); lspec.File != "" {
		if lf, err := newLogFile(lspec); err != nil {
			log.Printf("log file: %v (journal only)", err)
		} else {
			teeLog(lf)
			st.logFile = lf
			st.store = lf
			log.Printf("n5-fangov %s: log file %s (rotate above %d MiB, keep %d)", version.Version, lspec.File, lspec.MaxSizeMB, lspec.MaxFiles)
		}
	}
	st.cfg = cfg
	st.dspec = daemonOf(cfg)
	st.chans = channelSpecs(cfg)
	if len(st.chans) == 0 {
		log.Printf("config: no [[channel]] tables; nothing to regulate (monitoring only, N5 Pro: built-in channels)")
	}
	return true
}

// serveDevice detects the fan controller for the configured profile and
// builds the controller. false = fatal (no device, controller set-up),
// with the alert already sent.
func serveDevice(st *serveState) bool {
	st.hw = hwmon.New()
	dev, err := detectDevice(st.hw, st.dspec.Profile)
	if err != nil {
		log.Printf("profile %q: %v", st.dspec.Profile, err)
		sendAlertCooled(st.rdir, st.alerter, "profile", fmt.Sprintf("fan controller not detected (profile %q): %v\nNo regulation is running; fans stay in BIOS/EC control.", st.dspec.Profile, err))
		return false
	}
	st.dev = dev
	p := dev.Profile()
	log.Printf("profile %s (%s) at %s, %d channel(s), verified=%v", p.Name(), p.Title(), dev.HwmonPath(), len(dev.Channels()), p.Verified())
	if !p.Verified() {
		log.Printf("%sWARNING: profile %s is from documentation only, not verified on hardware", logWarning, p.Name())
	}
	for _, c := range st.chans {
		if !hasChannel(dev, c.PWM) {
			log.Printf("config: channel %q uses pwm%d which profile %s does not expose", c.Name, c.PWM, p.Name())
		}
	}

	st.factory = newSensorFactory(st.hw, dev)
	ctrl, err := newController(st.cfg, dev, st.factory, st.alerter, controlOpts{DryRun: st.dryRun, RunDir: st.rdir, HistoryFile: historyPath(st.state), DiskKind: diskKindResolver(st.hw)})
	if err != nil {
		log.Printf("controller: %v", err)
		sendAlertCooled(st.rdir, st.alerter, "start", "n5-fangov could not start the controller: "+err.Error())
		return false
	}
	st.ctrl = ctrl
	return true
}

// serveWeb decides the TCP listener address and TLS mode, sets up the
// certificate manager, the stores behind the dashboard APIs and the web
// server. Nothing here is fatal: a TLS failure disables the web UI, the
// CLI socket keeps working.
func serveWeb(st *serveState) {
	cfgPath, rdir, alerter, ctrl := st.cfgPath, st.rdir, st.alerter, st.ctrl

	// TCP listener address and TLS mode. --listen overrides the file; the
	// rule "non-loopback is never plain HTTP" is re-applied to the override.
	wspec := webOf(st.cfg)
	addr := wspec.Listen
	modeOverridden := false // the effective tls mode differs from the file's (the manager must not pin it)
	if st.listen != "" {
		addr = st.listen
		wspec.Listen = addr
		if addr != "none" && !isLoopbackListen(addr) && wspec.TLS == "off" {
			log.Printf("web: --listen %s is not loopback, tls \"off\" replaced by \"auto\"", addr)
			wspec.TLS = "auto"
			modeOverridden = true
		}
	}
	if addr == "none" {
		addr = ""
	}
	// Certificate manager: loads or creates the pair for the configured
	// mode, serves it through a hot-swappable store and backs /api/tls
	// (dashboard certificate panel, `n5-fangov cert` via the socket).
	hosts := tlsHosts(wspec)
	tlsMgr := newTLSManager(cfgPath, wspec, hosts)
	useTLS := false
	if addr == "" {
		tlsMgr.mode = "off" // no TCP listener: nothing to certify, /api/tls says so
		modeOverridden = modeOverridden || wspec.TLS != "off"
	} else {
		certPath, fellBack, err := tlsMgr.loadForServe()
		switch {
		case err != nil:
			// No plain-HTTP fallback: a LAN listener without TLS would carry
			// basic auth in clear text.
			log.Printf("web: TLS (%s): %v — web UI disabled, the CLI socket still works", wspec.TLS, err)
			startAlert(rdir, alerter, "web", fmt.Sprintf("web UI disabled: TLS (%s) could not be set up on %s: %v", wspec.TLS, addr, err))
			addr = ""
		case fellBack != nil:
			// The configured pair is unreadable or unusable; the listener
			// stays up on the automatic certificate so the panel (or `cert
			// reset`/`cert upload`) can repair it. The config keeps tls = "file".
			log.Printf("web: TLS file pair %s / %s unusable: %v — FALLBACK to the automatic certificate %s (mode %q); fix with the certificate panel, `n5-fangov cert upload` or `cert reset`",
				wspec.CertFile, wspec.KeyFile, fellBack, certPath, modeFallback)
			startAlert(rdir, alerter, "tls", fmt.Sprintf("custom certificate unreadable, serving the automatic certificate\n%s / %s: %v\nThe dashboard on %s stays up with the self-signed certificate %s; a browser that does not trust that one refuses the LAN name under HSTS - reach the dashboard by IP or import the certificate (n5-fangov cert export). Repair: certificate panel, `n5-fangov cert upload CERT KEY` or `n5-fangov cert reset`.",
				wspec.CertFile, wspec.KeyFile, fellBack, addr, certPath))
			useTLS = true
		case wspec.TLS == "auto":
			log.Printf("web: TLS auto, certificate %s (download and trust it from the dashboard or: n5-fangov cert export)", certPath)
			useTLS = true
		case wspec.TLS == "file":
			log.Printf("web: TLS from %s / %s", wspec.CertFile, wspec.KeyFile)
			if merr := tlsCheckKeyMode(wspec.KeyFile); merr != nil {
				log.Printf("%sWARNING: %v", logWarning, merr)
			}
			useTLS = true
		}
	}
	if modeOverridden {
		tlsMgr.ownsConfig = false
	}

	// The stores behind the dashboard APIs. Every config write of theirs
	// goes through the tls pin like the others; svc wraps Reload so a PUT
	// /api/config, an import or a preset apply re-applies [alert]
	// ([dashboard] is the controller's).
	st.alertMgr.pin = tlsMgr.pinConfig
	st.alertMgr.cooldown = func() time.Duration { return controllerConfig(ctrl).Daemon.AlertCooldown }
	// The scheduler applies [[schedule]] presets through the same preset
	// store the API uses; every successful Reload hands it the new list
	// (hookedService), so Set below sees the start-up config and later
	// ones. Run starts in serveRun after READY.
	sched := newScheduler(nil, func(msg string) { sendAlertCooled(rdir, alerter, "schedule", msg) }, log.Printf, time.Now)
	svc := hookedService{Service: ctrl, alerts: st.alertMgr, sched: sched}
	presets := dirPresetStore{dir: defaultPresetDir, cfgPath: cfgPath, svc: svc, pin: tlsMgr.pinConfig, profile: st.dev.Profile().Name()}
	sched.apply = presets.Apply
	sched.Set(schedule.FromConfig(st.cfg.Schedules))
	accounts := newAccountStore(cfgPath, wspec, tlsMgr.pinConfig)
	dashboard := newDashboardStore(cfgPath, ctrl, st.factory, tlsMgr.pinConfig)

	st.ws = newWebServer(webDeps{
		Service:     svc,
		ConfigPath:  cfgPath,
		PresetDir:   defaultPresetDir,
		Device:      st.dev,
		Sysfs:       st.hw,
		Web:         wspec,
		Log:         st.store,
		Bundle:      fileBundle{cfgPath: cfgPath, presetDir: defaultPresetDir, reload: svc.Reload, pin: tlsMgr.pinConfig, logf: log.Printf},
		TLS:         useTLS,
		TLSMgr:      tlsMgr,
		TLSHosts:    hosts,
		ConfigPin:   tlsMgr.pinConfig,
		SessionFile: sessionsPath(st.state),
		TokenFile:   tokensPath(st.state),
		Account:     accounts,
		Alerts:      st.alertMgr,
		Dashboard:   dashboard,
		System:      newSystemCollector(st.hw, st.dev),
		Schedules:   sched,
		Channels:    ctrl.Channels,
	})
	st.sched = sched
	log.Printf("web: sessions %s, tokens %s", orMemory(sessionsPath(st.state)), orMemory(tokensPath(st.state)))
	st.wspec, st.addr, st.useTLS, st.tlsMgr, st.hosts = wspec, addr, useTLS, tlsMgr, hosts
}

// serveRun starts the goroutines (unix socket, TCP listener, regulation
// loop, watchdog ticker, READY=1), waits for a signal or a fatal error and
// stops the controller. Returns the exit code.
func serveRun(st *serveState) int {
	rdir, alerter, ctrl := st.rdir, st.alerter, st.ctrl
	ws, addr, useTLS, tlsMgr := st.ws, st.addr, st.useTLS, st.tlsMgr

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	errc := make(chan error, 3)

	// Unix socket (CLI, no auth/CSRF).
	sock := socketPath(rdir)
	go func() { errc <- wrapErr("ipc", serveIPC(ctx, sock, ws.Socket)) }()

	// TCP (web UI), optional.
	if addr != "" {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Printf("web: listen %s: %v (web UI disabled, socket still works)", addr, err)
			startAlert(rdir, alerter, "web", "web UI listener failed on "+addr+": "+err.Error())
		} else if useTLS {
			log.Printf("web: listening on https://%s", ln.Addr())
			go func() { errc <- wrapErr("web", ws.ServeTLS(ctx, ln, tlsMgr)) }()
		} else {
			log.Printf("web: listening on http://%s", ln.Addr())
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
		grace := 2*st.dspec.Interval + 5*time.Second
		if waitFirstCycle(ctx, ctrl, grace) {
			log.Printf("first cycle done, READY")
		} else if ctx.Err() == nil {
			log.Printf("first cycle not finished after %s, signalling READY anyway", grace)
		}
		if ctx.Err() == nil {
			notifyReady()
			// Preset schedules: one evaluation now (applies the active
			// entry), then every schedulerTick.
			if st.sched != nil {
				st.sched.Run(ctx)
			}
		}
	}()

	exit := exitOK
	select {
	case <-ctx.Done():
		log.Printf("signal received, stopping")
	case err := <-errc:
		if err != nil && ctx.Err() == nil {
			log.Printf("%sfatal: %v", logError, err)
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
		if snap := s.Snapshot(); snap.TS > 0 && snap.Status != control.StatusStarting {
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
