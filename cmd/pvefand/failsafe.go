package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/SirRenix/pvefand/internal/hwmon"
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
	chans := channelSpecs(cfg)
	if len(chans) == 0 {
		fmt.Println("failsafe: no channels configured, nothing to do")
		return exitOK
	}

	hw := hwmon.New()
	dev, err := detectDevice(hw, daemonOf(cfg).Profile)
	if err != nil {
		fmt.Printf("failsafe: no fan controller (%v): driver unloaded or absent, the EC/BIOS has control\n", err)
		return exitOK
	}

	parts := make([]string, 0, len(chans))
	for _, c := range chans {
		if !hasChannel(dev, c.PWM) {
			parts = append(parts, fmt.Sprintf("%s=pwm%d not exposed", c.Name, c.PWM))
			continue
		}
		if err := dev.SafeStop(c.PWM, c.Stop); err != nil {
			parts = append(parts, fmt.Sprintf("%s=ERROR(%v)", c.Name, err))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", c.Name, c.Stop))
	}
	fmt.Printf("failsafe: safe state set on %s: %s\n", dev.Profile().Name(), strings.Join(parts, " "))

	// The daemon's snapshot no longer describes reality.
	_ = os.Remove(statePath(runDir()))
	return exitOK
}

// cmdAlert is the hidden entry the onfailure unit uses:
//
//	pvefand alert <type> <message...>
//
// It delegates to internal/alert (PVE::Notify → mail → log) and always exits 0.
func cmdAlert(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pvefand alert <type> <message>")
		return exitUsage
	}
	sendAlert(newAlerter(), args[0], strings.Join(args[1:], " "))
	return exitOK
}
