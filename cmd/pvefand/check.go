//go:build integrate

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SirRenix/pvefand/internal/hwmon"
)

func init() {
	register("check", command{run: cmdCheck})
}

// checkResult is one line of the self-check report.
type checkResult struct {
	ok       bool
	advisory bool // a failed advisory check does not change the exit code
	name     string
	detail   string
}

// cmdCheck is the self-check used interactively and as ExecStartPre.
// Exit 1 when any non-advisory check fails. --quiet prints only failures.
func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	quiet := fs.Bool("quiet", false, "print only failures")
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	results := runChecks(*cfgPath, runDir())
	rc := exitOK
	for _, r := range results {
		mark := "ok  "
		switch {
		case r.ok:
		case r.advisory:
			mark = "warn"
		default:
			mark = "FAIL"
			rc = exitFail
		}
		if *quiet && r.ok {
			continue
		}
		fmt.Printf("[%s] %-22s %s\n", mark, r.name, r.detail)
	}
	if !*quiet {
		if rc == exitOK {
			fmt.Println("check: all good")
		} else {
			fmt.Println("check: FAILED")
		}
	}
	return rc
}

func runChecks(cfgPath, dir string) []checkResult {
	var res []checkResult
	add := func(ok bool, name, detail string) { res = append(res, checkResult{ok: ok, name: name, detail: detail}) }
	adv := func(ok bool, name, detail string) {
		res = append(res, checkResult{ok: ok, advisory: true, name: name, detail: detail})
	}

	// 1. config
	cfg, warns, err := loadConfig(cfgPath)
	switch {
	case err != nil:
		add(false, "config", cfgPath+": "+err.Error())
	case len(warns) > 0:
		adv(false, "config", fmt.Sprintf("%s: %d warning(s), defaults substituted:", cfgPath, len(warns)))
		for _, w := range warns {
			adv(false, "  ", w)
		}
	default:
		add(true, "config", cfgPath+" parses without warnings")
	}
	chans := channelSpecs(cfg)
	dspec := daemonOf(cfg)

	// 2. profile
	hw := hwmon.New()
	dev, err := detectDevice(hw, dspec.Profile)
	if err != nil {
		add(false, "profile", fmt.Sprintf("%q not detected: %v", dspec.Profile, err))
		return res
	}
	p := dev.Profile()
	add(true, "profile", fmt.Sprintf("%s (%s) at %s", p.Name(), p.Title(), dev.HwmonPath()))
	if !p.Verified() {
		adv(false, "profile verified", p.Name()+" is documentation-only, not hardware-verified")
	}

	// 3. pwm files of configured channels writable (open O_WRONLY, no write)
	if len(chans) == 0 {
		adv(false, "channels", "no [[channel]] configured; daemon will only monitor")
	}
	for _, c := range chans {
		if !hasChannel(dev, c.PWM) {
			add(false, "channel "+c.Name, fmt.Sprintf("pwm%d not exposed by profile %s", c.PWM, p.Name()))
			continue
		}
		problems := []string{}
		for _, f := range []string{fmt.Sprintf("pwm%d", c.PWM), fmt.Sprintf("pwm%d_enable", c.PWM)} {
			path := filepath.Join(dev.HwmonPath(), f)
			if err := writable(path); err != nil {
				problems = append(problems, err.Error())
			}
		}
		if len(problems) > 0 {
			add(false, "channel "+c.Name, strings.Join(problems, "; "))
			continue
		}
		rpm := "no tach"
		if r, err := dev.ReadRPM(c.PWM); err == nil {
			rpm = fmt.Sprintf("%d rpm", r)
		}
		add(true, "channel "+c.Name, fmt.Sprintf("pwm%d writable, %s, sensor %s", c.PWM, rpm, c.Sensor))
	}

	// 4. sensors resolve and read plausibly
	factory := newSensorFactory(hw, dev)
	for _, c := range chans {
		src, err := factory(c.Sensor)
		if err != nil {
			add(false, "sensor "+c.Sensor, err.Error())
			continue
		}
		t, err := readTempC(src)
		if err != nil {
			add(false, "sensor "+c.Sensor, "read: "+err.Error())
			continue
		}
		add(true, "sensor "+c.Sensor, fmt.Sprintf("%.1f C", t))
	}

	// 5. socket answers when the unit is active (skipped during ExecStartPre,
	// where the unit is "activating")
	if unitActive(unitName) {
		if daemonRunning(dir) {
			add(true, "socket", socketPath(dir)+" answers")
		} else {
			add(false, "socket", socketPath(dir)+" does not answer although the unit is active")
		}
	} else {
		adv(true, "socket", "unit not active, socket check skipped")
	}

	// 6. dkms (advisory, n5pro only): module survives a kernel update only if
	// DKMS built it for the running kernel.
	if p.Name() == "n5pro" {
		ok, detail := dkmsInstalled("minisforum-n5-it5571")
		adv(ok, "dkms", detail)
	}
	return res
}

// writable opens path for writing without writing anything.
func writable(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

// dkmsInstalled reports whether `dkms status PKG` lists the running kernel as installed.
func dkmsInstalled(pkg string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	kernel, _ := exec.CommandContext(ctx, "uname", "-r").Output()
	k := strings.TrimSpace(string(kernel))
	out, err := exec.CommandContext(ctx, "dkms", "status", pkg).Output()
	if err != nil {
		return false, "dkms status " + pkg + ": " + err.Error() + " (module may not be rebuilt after a kernel update)"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, k) && strings.Contains(line, "installed") {
			return true, strings.TrimSpace(line)
		}
	}
	return false, pkg + " not installed for kernel " + k + " (run: dkms status " + pkg + ")"
}
