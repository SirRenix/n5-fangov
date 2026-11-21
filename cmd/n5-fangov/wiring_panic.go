// wiring_panic.go holds the cmd side of the full cooling (control/panic.go):
// the refusal for a read-only fan driver, the health page line and the
// `panic` CLI.
package main

import (
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/events"
	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/n5driver"
	"github.com/SirRenix/n5-fangov/internal/profile"
)

func init() {
	register("panic", command{run: cmdPanic})
}

// panicRefusal is the controller's PanicRefusal on the N5 Pro: a driver
// that does not expose the pwm nodes (read-only) cannot be driven to 255.
// Other profiles: nil (profile monitor is the controller's own check).
func panicRefusal(hw *hwmon.FS, dev profile.Device) func() string {
	if hw == nil || dev == nil || dev.Profile().Name() != "n5pro" {
		return nil
	}
	return func() string {
		if m := n5driver.Read(hw.Root).Mode(); m != "write-enabled" {
			return "fan driver " + m + ": the fans cannot be driven"
		}
		return ""
	}
}

// panicLine is the health page line of a running full cooling: "until
// 14:32 (admin)" in the daemon's local time; "" when none.
func panicLine(p *control.PanicState) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("until %s (%s)", time.Unix(p.Until, 0).Format("15:04"), events.Who(p.By))
}

// withPanic adds the live full cooling line to the (cached) health
// document as "panic"; the cached map is not changed.
func (v *healthView) withPanic(h map[string]any) map[string]any {
	if v.svc == nil {
		return h
	}
	line := panicLine(v.svc.Snapshot().Panic)
	if line == "" {
		return h
	}
	out := maps.Clone(h)
	out["panic"] = "full cooling " + line
	return out
}

// cmdPanic is the full cooling from the shell, through the daemon only:
//
//	panic          start, or 10 min from now when running
//	panic stop     end it early
//	panic status   running or not, the end and who started it
//
// Without the daemon there is nothing to drive the fans: the failsafe's
// safe duty applies, and no command writes full duty without the daemon.
func cmdPanic(args []string) int {
	fs := flag.NewFlagSet("panic", flag.ContinueOnError)
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) > 1 {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov panic [stop|status]")
		return exitUsage
	}
	sub := "start"
	if len(pos) == 1 {
		sub = pos[0]
	}
	a := newAPI(runDir())
	var resp struct {
		Active bool  `json:"active"`
		Until  int64 `json:"until"`
	}
	switch sub {
	case "start", "stop":
		err = a.do("POST", "/api/panic", map[string]string{"action": sub}, &resp)
	case "status":
		var snap control.Snapshot
		if err = a.get("/api/state", &snap); err == nil {
			if snap.Panic == nil {
				fmt.Println("full cooling: off" + refusedNote(snap.PanicRefused))
				return exitOK
			}
			fmt.Printf("full cooling: on until %s (%s left), started by %s\n", time.Unix(snap.Panic.Until, 0).Format("15:04:05"), leftOf(snap.Panic.Until), snap.Panic.By)
			return exitOK
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: n5-fangov panic [stop|status]")
		return exitUsage
	}
	switch {
	case errors.Is(err, errNoDaemon):
		fmt.Fprintln(os.Stderr, "panic: the daemon is not running: nothing drives the fans (the failsafe's safe duty applies); start it with systemctl start n5-fangov")
		return exitFail
	case err != nil:
		fmt.Fprintln(os.Stderr, "panic:", err)
		return exitFail
	case sub == "stop":
		fmt.Println("full cooling: off; the channels return to their curves (step_down) or overrides")
	default:
		fmt.Printf("full cooling: every channel at 255 until %s (%s)\n", time.Unix(resp.Until, 0).Format("15:04:05"), leftOf(resp.Until))
	}
	return exitOK
}

// leftOf is the time from now to until, in whole seconds.
func leftOf(until int64) string {
	return (time.Until(time.Unix(until, 0)).Round(time.Second)).String()
}

func refusedNote(why string) string {
	if why == "" {
		return ""
	}
	return " (a start would be refused: " + why + ")"
}
