package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/SirRenix/pvefand/internal/hwmon"
)

func init() {
	register("detect", command{run: cmdDetect})
}

// cmdDetect prints every profile with its detection result, the channels of
// detected devices (with current duty/rpm) and the sensors that resolve.
// Strictly read-only; it never enters manual mode or writes a duty.
func cmdDetect(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: pvefand detect")
		return exitUsage
	}
	hw := hwmon.New()
	fmt.Printf("sysfs root: %s\n\n", hw.Root)

	if devs, err := listHwmon(hw); err != nil {
		fmt.Printf("hwmon devices: %v\n\n", err)
	} else {
		fmt.Println("hwmon devices:")
		for _, d := range devs {
			fmt.Printf("  %-32s %s\n", d.Name, d.Path)
		}
		fmt.Println()
	}

	rc := exitFail // becomes 0 as soon as one profile detects
	fmt.Println("profiles:")
	for _, p := range allProfiles() {
		verified := "verified on hardware"
		if !p.Verified() {
			verified = "from documentation, untested"
		}
		dev, err := p.Detect(hw)
		if err != nil {
			fmt.Printf("  %-8s %-40s %s\n           not detected: %v\n", p.Name(), p.Title(), verified, err)
			continue
		}
		rc = exitOK
		fmt.Printf("  %-8s %-40s %s\n           DETECTED at %s\n", p.Name(), p.Title(), verified, dev.HwmonPath())
		if n := strings.TrimSpace(p.Notes()); n != "" {
			for _, line := range strings.Split(n, "\n") {
				fmt.Printf("           note: %s\n", line)
			}
		}
		for _, c := range dev.Channels() {
			duty := "duty ?"
			if d, err := dev.ReadDuty(c.Index); err == nil {
				duty = fmt.Sprintf("duty %3d (%3d%%)", d, pct(d))
			}
			rpm := "no tach"
			if c.HasTach {
				if r, err := dev.ReadRPM(c.Index); err == nil {
					rpm = fmt.Sprintf("%d rpm", r)
				} else {
					rpm = "rpm ?"
				}
			}
			fmt.Printf("           pwm%d %-14s %s  %s\n", c.Index, c.Label, duty, rpm)
		}
		if extra := dev.ExtraTemps(); len(extra) > 0 {
			keys := make([]string, 0, len(extra))
			for k := range extra {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				val := "?"
				if mc, err := readIntPath(hw, extra[k]); err == nil {
					val = fmt.Sprintf("%.1f C", float64(mc)/1000)
				}
				fmt.Printf("           %-18s %s  (%s)\n", k, val, extra[k])
			}
		}
		// Known lists only ids that resolve now (description carries the
		// current reading) plus the two generic patterns at the end.
		fmt.Println("           sensors:")
		for _, s := range knownSensors(hw, dev) {
			fmt.Printf("             %-22s %s\n", s.ID, s.Description)
		}
	}
	if rc != exitOK {
		fmt.Println("\nno fan controller detected; the daemon would refuse to start")
	}
	return rc
}
