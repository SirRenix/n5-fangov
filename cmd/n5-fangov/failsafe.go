package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
)

func init() {
	register("failsafe", command{run: cmdFailsafe})
	register("alert", command{run: cmdAlert, hidden: true})
}

// cmdFailsafe puts every configured channel into its safe state (DESIGN
// rule 7) without the daemon. It is the ExecStopPost of the unit and runs
// after every end of the daemon, including SIGKILL and watchdog kills where
// the daemon's own Stop never ran. It never fails loudly: exit is always 0,
// what happened is printed (journal).
func cmdFailsafe(args []string) int {
	fs := flag.NewFlagSet("failsafe", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	if err := fs.Parse(args); err != nil {
		return exitOK
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("failsafe: panic: %v (fans stay in whatever state the driver left them)\n", r)
		}
	}()

	result := os.Getenv("SERVICE_RESULT")
	if result != "" {
		fmt.Printf("failsafe: after daemon end (SERVICE_RESULT=%s EXIT_CODE=%s EXIT_STATUS=%s)\n",
			result, os.Getenv("EXIT_CODE"), os.Getenv("EXIT_STATUS"))
	}

	cfg, warns, _ := loadConfig(*cfgPath)
	for _, w := range warns {
		fmt.Printf("failsafe: config: %s\n", w)
	}

	// Detect before looking at the channel list: on the N5 Pro the safe
	// state covers pwm1..3 even when the config has no or fewer channels
	// (the daemon manages them regardless, see control.SanitizeChannels).
	hw := hwmon.New()
	dev, err := detectDevice(hw, daemonOf(cfg).Profile)
	if err != nil {
		fmt.Printf("failsafe: no fan controller (%v): driver unloaded or absent, the EC/BIOS has control\n", err)
		return exitOK
	}
	if len(channelSpecs(cfg)) == 0 && dev.Profile().Name() != "n5pro" {
		fmt.Println("failsafe: no channels configured, nothing to do")
		return exitOK
	}

	logger := log.New(os.Stdout, "", 0)
	if err := failsafeDevice(dev, cfg, logger); err != nil {
		fmt.Printf("failsafe: safe state on %s INCOMPLETE: %v\n", dev.Profile().Name(), err)
	} else {
		fmt.Printf("failsafe: safe state set on %s\n", dev.Profile().Name())
	}

	// The daemon's snapshot no longer describes reality.
	_ = os.Remove(statePath(runDir()))
	return exitOK
}

// cmdAlert is the hidden entry the onfailure unit uses:
//
//	n5-fangov alert <type> <message...>
//
// It delegates to internal/alert (PVE::Notify → mail → log) and always exits 0.
func cmdAlert(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov alert <type> <message>")
		return exitUsage
	}
	sendAlert(newAlerter(), args[0], strings.Join(args[1:], " "))
	return exitOK
}
