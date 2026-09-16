package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
)

func init() {
	register("system", command{run: cmdSystem})
}

// cmdSystem prints the hardware inventory the dashboard's System tab
// shows: from the daemon (GET /api/system over the socket — cached static
// parts, live memory/load/link state) when it runs, else collected from
// this process (profile detection for the fan controller section).
func cmdSystem(args []string) int {
	fs := flag.NewFlagSet("system", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file (offline: profile to detect)")
	asJSON := fs.Bool("json", false, "print the JSON document instead of the summary")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov system [--json]")
		return exitUsage
	}
	var in systemInfo
	source := "daemon"
	err := newAPI(runDir()).get("/api/system", &in)
	switch {
	case err == nil:
	case !errors.Is(err, errNoDaemon):
		fmt.Fprintln(os.Stderr, "system:", err)
		return exitFail
	default:
		source = "collected locally (daemon not running)"
		cfg, _, _ := loadConfig(*cfgPath)
		hw := hwmon.New()
		dev, derr := detectDevice(hw, daemonOf(cfg).Profile)
		if derr != nil {
			dev = nil
		}
		in = collectSystem(hw, dev)
		if derr != nil {
			in.Errors = append(in.Errors, "fan controller: "+derr.Error())
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(in); err != nil {
			fmt.Fprintln(os.Stderr, "system:", err)
			return exitFail
		}
		return exitOK
	}
	printSystem(os.Stdout, in, source)
	return exitOK
}

// printSystem renders the summary the dashboard card shows, plus the
// per-device tables of the tab.
func printSystem(w io.Writer, in systemInfo, source string) {
	h, m, c := in.Host, in.Machine, in.CPU
	fmt.Fprintf(w, "host:      %s · %s · kernel %s · up %s · load %.2f %.2f %.2f\n", orDash(h.Hostname), orDash(h.OS), orDash(h.Kernel), fmtDuration(h.UptimeS), h.Load1, h.Load5, h.Load15)
	fmt.Fprintf(w, "machine:   %s · board %s · BIOS %s (%s)\n", orDash(strings.TrimSpace(m.Vendor+" "+m.Product)), orDash(strings.TrimSpace(m.Board+" "+paren(m.BoardVendor))), orDash(m.BIOSVersion), orDash(m.BIOSDate))
	fmt.Fprintf(w, "cpu:       %s · %d socket(s), %d cores, %d threads", orDash(c.Model), c.Sockets, c.Cores, c.Threads)
	if c.MaxMHz > 0 {
		fmt.Fprintf(w, " · max %d MHz", c.MaxMHz)
	}
	fmt.Fprintln(w)
	mem := in.Memory
	used := mem.TotalBytes - mem.AvailableBytes
	fmt.Fprintf(w, "memory:    used %s of %s (%d %%) · swap %s of %s", fmtBytes(used), fmtBytes(mem.TotalBytes), pctOf(used, mem.TotalBytes), fmtBytes(mem.SwapTotalBytes-mem.SwapFreeBytes), fmtBytes(mem.SwapTotalBytes))
	if mem.InstalledBytes > 0 {
		fmt.Fprintf(w, " · installed %s (%d module(s), SMBIOS %s)", fmtBytes(mem.InstalledBytes), len(mem.Modules), orDash(mem.SMBIOS))
	}
	fmt.Fprintln(w)
	for _, md := range mem.Modules {
		ecc := ""
		if md.ECC {
			ecc = " ECC"
		}
		fmt.Fprintf(w, "  %-8s %-14s %8s  %s %s %d MT/s%s  %s %s\n", md.Slot, md.Bank, fmtBytes(md.SizeBytes), md.Type, md.FormFactor, md.SpeedMTs, ecc, md.Manufacturer, md.Part)
	}
	for _, g := range in.GPUs {
		fmt.Fprintf(w, "gpu:       %s (%s, %s, driver %s)\n", g.Name, g.Vendor, g.PCI, orDash(g.Driver))
	}
	for _, n := range in.NPUs {
		fmt.Fprintf(w, "npu:       %s (%s, driver %s %s, %s)\n", n.Name, n.PCI, orDash(n.Driver), n.DriverVersion, orDash(n.Accel))
	}
	if len(in.GPUs)+len(in.NPUs) == 0 {
		fmt.Fprintln(w, "gpu/npu:   none found")
	}
	fmt.Fprintln(w, "network:")
	for _, n := range in.NICs {
		link := n.State
		if n.SpeedMbit > 0 {
			link = fmt.Sprintf("%s %s %s", n.State, fmtMbit(n.SpeedMbit), n.Duplex)
		}
		fmt.Fprintf(w, "  %-8s %-22s %-40s %-10s %s  mtu %d  %s\n", n.Name, link, trunc(n.Model, 40), orDash(n.Driver), n.MAC, n.MTU, n.PCI)
	}
	if len(in.NICs) == 0 {
		fmt.Fprintln(w, "  no physical interfaces")
	}
	fmt.Fprintln(w, "storage:")
	for _, ct := range in.Storage.Controllers {
		fmt.Fprintf(w, "  %-5s %-52s %-10s %s\n", ct.Kind, trunc(ct.Name, 52), orDash(ct.Driver), ct.PCI)
	}
	var total int64
	for _, d := range in.Storage.Disks {
		total += d.SizeBytes
		kind := "SSD"
		if d.Rotational {
			kind = "HDD"
		}
		fmt.Fprintf(w, "  %-8s %9s  %s  %-6s %s\n", d.Name, fmtBytes(d.SizeBytes), kind, d.Transport, d.Model)
	}
	fmt.Fprintf(w, "  %d disk(s), %s\n", len(in.Storage.Disks), fmtBytes(total))
	fc := in.FanController
	if fc.Profile != "" {
		fmt.Fprintf(w, "fan ctl:   profile %s at %s · module %s %s\n", fc.Profile, orDash(fc.Hwmon), orDash(fc.Module), fc.ModuleVersion)
	}
	for _, e := range in.Errors {
		fmt.Fprintf(w, "note:      %s\n", e)
	}
	fmt.Fprintf(w, "(source: %s)\n", source)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func paren(s string) string {
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func pctOf(part, whole int64) int {
	if whole <= 0 {
		return 0
	}
	return int(part * 100 / whole)
}

// fmtBytes prints binary units (GiB/TiB) with one decimal.
func fmtBytes(b int64) string {
	const k = 1024
	switch {
	case b >= k*k*k*k:
		return fmt.Sprintf("%.1f TiB", float64(b)/(k*k*k*k))
	case b >= k*k*k:
		return fmt.Sprintf("%.1f GiB", float64(b)/(k*k*k))
	case b >= k*k:
		return fmt.Sprintf("%.0f MiB", float64(b)/(k*k))
	case b >= k:
		return fmt.Sprintf("%.0f KiB", float64(b)/k)
	}
	return fmt.Sprintf("%d B", b)
}

// fmtMbit prints a link speed as Gbit/s or Mbit/s.
func fmtMbit(m int) string {
	if m >= 1000 {
		if m%1000 == 0 {
			return fmt.Sprintf("%d Gbit/s", m/1000)
		}
		return fmt.Sprintf("%.1f Gbit/s", float64(m)/1000)
	}
	return fmt.Sprintf("%d Mbit/s", m)
}
