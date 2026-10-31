package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/hwstatus"
	"github.com/SirRenix/n5-fangov/internal/n5driver"
	"github.com/SirRenix/n5-fangov/internal/version"
)

func init() {
	register("identify", command{run: cmdIdentify})
}

// procRoot is /proc; tests point it at a fixture.
var procRoot = "/proc"

// dcrInfo is the optional register dump of the driver (debug_regs=1).
type dcrInfo struct {
	Path     string         `json:"path,omitempty"`
	Dump     string         `json:"dump,omitempty"`
	Channels map[string]int `json:"channels,omitempty"` // pwm1..pwm4 -> DCR value
	Error    string         `json:"error,omitempty"`    // why there is no dump
}

// machineIdentity is what `identify` prints and `validate` records in
// meta.json (TESTSPEC §2 identity).
type machineIdentity struct {
	Version     string            `json:"n5fangov_version"`
	DMI         hwstatus.Identity `json:"dmi"`
	Kernel      string            `json:"kernel"`
	KernelTaint string            `json:"kernel_taint"` // /proc/sys/kernel/tainted
	BootID      string            `json:"boot_id"`
	UptimeS     float64           `json:"uptime_s"`
	Driver      n5driver.State    `json:"driver"`
	DCR         dcrInfo           `json:"dcr"`
	Profile     string            `json:"profile_detected"`
	ProfileErr  string            `json:"profile_error,omitempty"`
	HWStatus    hwstatus.Result   `json:"hw_status"`
	HWTable     hwstatus.Meta     `json:"hw_status_table"`
}

func readProc(rel ...string) string {
	b, err := os.ReadFile(filepath.Join(append([]string{procRoot}, rel...)...))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// collectIdentity reads everything read-only: DMI, kernel, taint, boot id,
// uptime, the driver's sysfs state, the register dump when the driver
// offers one, the detected profile and the hardware status.
func collectIdentity(hw *hwmon.FS) machineIdentity {
	id := machineIdentity{
		Version:     version.Version,
		DMI:         hwstatus.ReadIdentity(hw.Root),
		Kernel:      readProc("sys", "kernel", "osrelease"),
		KernelTaint: readProc("sys", "kernel", "tainted"),
		BootID:      readProc("sys", "kernel", "random", "boot_id"),
		Driver:      n5driver.Read(hw.Root),
		HWTable:     hwstatus.Source(),
	}
	if f := strings.Fields(readProc("uptime")); len(f) > 0 {
		id.UptimeS, _ = strconv.ParseFloat(f[0], 64)
	}
	id.DCR = readDCRInfo(hw.Root)
	if dev, err := detectDevice(hw, "auto"); err == nil {
		id.Profile = dev.Profile().Name()
	} else {
		id.ProfileErr = err.Error()
	}
	pname := id.Profile
	if pname == "" && id.Driver.HwmonPath != "" {
		pname = "n5pro" // the driver is there, only read-only: its board still has a status
	}
	id.HWStatus = hwstatus.ForProfile(pname, id.DMI)
	return id
}

func readDCRInfo(sysRoot string) dcrInfo {
	p := n5driver.DCRPath(sysRoot)
	if _, err := os.Stat(p); err != nil {
		return dcrInfo{Error: "no " + p + " (driver 0.2.1+sr1 with debug_regs=1 only; validation, not for operation)"}
	}
	d, err := n5driver.ReadDCR(p)
	if err != nil {
		return dcrInfo{Path: p, Error: err.Error()}
	}
	out := dcrInfo{Path: p, Dump: d.String(), Channels: map[string]int{}}
	for ch := 1; ch <= 4; ch++ {
		if v, ok := d.Channel(ch); ok {
			out.Channels[fmt.Sprintf("pwm%d", ch)] = v
		}
	}
	return out
}

// cmdIdentify prints the machine's identity for reports and issues. It
// never writes: DMI, /proc, sysfs and (when present) the driver's
// read-only debugfs dump.
func cmdIdentify(args []string) int {
	fs := flag.NewFlagSet("identify", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov identify [--json]")
		return exitUsage
	}
	id := collectIdentity(hwmon.New())
	if *asJSON {
		b, _ := json.MarshalIndent(id, "", "  ")
		fmt.Println(string(b))
		return exitOK
	}
	or := func(s string) string {
		if s == "" {
			return "?"
		}
		return s
	}
	d := id.DMI
	fmt.Printf("n5-fangov       %s\n", id.Version)
	fmt.Printf("product         %s (vendor %s)\n", or(d.Product), or(d.SysVendor))
	fmt.Printf("board           %s, version %s\n", or(d.Board), or(d.BoardVersion))
	fmt.Printf("BIOS            %s (%s)\n", or(d.BIOS), or(d.BIOSDate))
	fmt.Printf("EC firmware     %s\n", or(d.EC))
	fmt.Printf("kernel          %s, taint %s\n", or(id.Kernel), or(id.KernelTaint))
	fmt.Printf("uptime          %.0f s, boot id %s\n", id.UptimeS, or(id.BootID))
	dr := id.Driver
	if dr.Loaded || dr.HwmonPath != "" {
		fmt.Printf("driver          %s %s, srcversion %s, taint %s, %s\n", n5driver.ModuleName, or(dr.Version), or(dr.SrcVersion), or(dr.Taint), dr.Mode())
		var ps []string
		for _, k := range []string{"force", "experimental_write", "debug_regs"} {
			if v, ok := dr.Params[k]; ok {
				ps = append(ps, k+"="+v)
			}
		}
		fmt.Printf("parameters      %s\n", or(strings.Join(ps, " ")))
		fmt.Printf("hwmon           %s: %s\n", or(dr.HwmonPath), strings.Join(dr.Attrs, " "))
		for _, f := range dr.Findings() {
			fmt.Printf("note            %s\n", f)
		}
	} else {
		fmt.Printf("driver          %s not loaded\n", n5driver.ModuleName)
	}
	if id.DCR.Dump != "" {
		fmt.Printf("dcr             %s  (pwm1 %d, pwm2 %d, pwm3 %d, pwm4 %d)\n", id.DCR.Dump, id.DCR.Channels["pwm1"], id.DCR.Channels["pwm2"], id.DCR.Channels["pwm3"], id.DCR.Channels["pwm4"])
	} else {
		fmt.Printf("dcr             unavailable: %s\n", id.DCR.Error)
	}
	if id.Profile != "" {
		fmt.Printf("profile         %s\n", id.Profile)
	} else {
		fmt.Printf("profile         none: %s\n", id.ProfileErr)
	}
	fmt.Printf("hardware status %s\n", id.HWStatus.Describe())
	fmt.Printf("status table    %s, generated %s\n", id.HWTable.Source, id.HWTable.Generated)
	return exitOK
}
