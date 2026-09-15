package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/SirRenix/ventula/internal/control"
)

func init() {
	register("status", command{run: cmdStatus})
	register("set", command{run: cmdSet})
	register("auto", command{run: cmdAuto})
	register("curve", command{run: cmdCurve})
	register("log", command{run: cmdLog})
}

// ---------------------------------------------------------------------------
// status

// loadSnapshot fetches the state from the daemon; when the socket does not
// answer it falls back to state.json and reports its age.
func loadSnapshot(dir string) (snap control.Snapshot, source string, err error) {
	if err = newAPI(dir).get("/api/state", &snap); err == nil {
		return snap, "daemon", nil
	}
	if !errors.Is(err, errNoDaemon) {
		return snap, "", err
	}
	data, rerr := os.ReadFile(statePath(dir))
	if rerr != nil {
		return snap, "", fmt.Errorf("%v; no %s either (daemon not running?)", err, statePath(dir))
	}
	if jerr := json.Unmarshal(data, &snap); jerr != nil {
		return snap, "", fmt.Errorf("%s: %v", statePath(dir), jerr)
	}
	age := "unknown age"
	if snap.TS > 0 {
		age = fmtDuration(time.Now().Unix()-snap.TS) + " old"
	}
	return snap, "state.json (" + age + ", daemon socket unavailable)", nil
}

func cmdStatus(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: ventula status")
		return exitUsage
	}
	dir := runDir()
	snap, source, err := loadSnapshot(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return exitFail
	}
	printSnapshot(snap, source, unitActive(unitName))
	return exitOK
}

func printSnapshot(s control.Snapshot, source string, active bool) {
	verified := "verified"
	if !s.Verified {
		verified = "UNVERIFIED profile"
	}
	unit := "active"
	if !active {
		unit = "not active"
	}
	extra := ""
	if s.DryRun {
		extra = "  DRY-RUN"
	}
	fmt.Printf("ventula: %s  profile %s (%s)  uptime %s  unit %s%s\n", s.Status, s.Profile, verified, fmtDuration(s.Uptime), unit, extra)
	if s.HwmonPath != "" {
		fmt.Printf("hwmon: %s\n", s.HwmonPath)
	}
	if source != "daemon" {
		fmt.Printf("source: %s\n", source)
	}
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "channel\tsensor\ttemp\tduty\t%\trpm\tmode\t")
	for _, c := range s.Channels {
		fmt.Fprintf(tw, "%s\t%s\t%s C\t%d\t%d%%\t%s\t%s\t\n",
			c.Name, c.Sensor, strings.TrimSpace(fmtTemp(c.Temp)), c.Duty, pct(c.Duty), fmtRPM(c.RPM), c.Mode)
	}
	tw.Flush()
	if len(s.ExtraTemps) > 0 {
		keys := make([]string, 0, len(s.ExtraTemps))
		for k := range s.ExtraTemps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %s C", k, strings.TrimSpace(fmtTemp(s.ExtraTemps[k]))))
		}
		fmt.Printf("\nextra: %s\n", strings.Join(parts, "  "))
	}
	if len(s.Alerts) > 0 {
		keys := make([]string, 0, len(s.Alerts))
		for k := range s.Alerts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Println()
		for _, k := range keys {
			fmt.Printf("last alert %-10s %s\n", k+":", time.Unix(s.Alerts[k], 0).Format("2006-01-02 15:04"))
		}
	}
}

// ---------------------------------------------------------------------------
// set / auto

func cmdSet(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ventula set <channel> <duty|NN%>")
		return exitUsage
	}
	ch := args[0]
	duty, err := parseDuty(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "set:", err)
		return exitUsage
	}
	a := newAPI(runDir())
	body := map[string]int{"duty": duty}
	if err := a.do("PUT", "/api/override/"+ch, body, nil); err != nil {
		fmt.Fprintln(os.Stderr, "set:", err)
		return exitFail
	}
	fmt.Printf("%s: manual override duty %d (%d%%); critical temperature and stall protection stay active\n", ch, duty, pct(duty))
	return exitOK
}

func cmdAuto(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: ventula auto <channel|all>")
		return exitUsage
	}
	a := newAPI(runDir())
	names := []string{args[0]}
	if args[0] == "all" {
		var snap control.Snapshot
		if err := a.get("/api/state", &snap); err != nil {
			fmt.Fprintln(os.Stderr, "auto:", err)
			return exitFail
		}
		names = names[:0]
		for _, c := range snap.Channels {
			names = append(names, c.Name)
		}
	}
	rc := exitOK
	for _, n := range names {
		if err := a.do("DELETE", "/api/override/"+n, nil, nil); err != nil {
			fmt.Fprintf(os.Stderr, "auto %s: %v\n", n, err)
			rc = exitFail
			continue
		}
		fmt.Printf("%s: back to curve\n", n)
	}
	return rc
}

// ---------------------------------------------------------------------------
// curve

func cmdCurve(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: ventula curve")
		return exitUsage
	}
	raw, source, err := currentConfigRaw(runDir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "curve:", err)
		return exitFail
	}
	cfg, warns := parseConfig(raw)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	d := daemonOf(cfg)
	fmt.Printf("config from %s; interval %s\n\n", source, d.Interval)
	for _, c := range channelSpecs(cfg) {
		fmt.Printf("%-8s pwm%d  sensor %-16s critical %d C  stop %s\n", c.Name, c.PWM, c.Sensor, c.Critical, c.Stop)
		pts := make([]string, 0, len(c.Curve))
		for _, p := range c.Curve {
			pts = append(pts, fmt.Sprintf("%d C -> %d (%d%%)", p[0], p[1], pct(p[1])))
		}
		fmt.Printf("         %s\n", strings.Join(pts, "   "))
	}
	return exitOK
}

// currentConfigRaw returns the TOML text the daemon runs with, falling back
// to the config file when the daemon does not answer.
func currentConfigRaw(dir string) ([]byte, string, error) {
	// GET /api/config answers {"raw": "<toml text>", "config": {...}}.
	var resp struct {
		Raw string `json:"raw"`
	}
	err := newAPI(dir).get("/api/config", &resp)
	if err == nil && resp.Raw != "" {
		return []byte(resp.Raw), "daemon", nil
	}
	data, rerr := os.ReadFile(defaultConfigPath)
	if rerr != nil {
		if err != nil {
			return nil, "", fmt.Errorf("%v; %v", err, rerr)
		}
		return nil, "", rerr
	}
	return data, defaultConfigPath, nil
}

// ---------------------------------------------------------------------------
// log

func cmdLog(args []string) int {
	n := 50
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: ventula log [n]")
		return exitUsage
	}
	if len(args) == 1 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 1 {
			fmt.Fprintln(os.Stderr, "log: n must be a positive integer")
			return exitUsage
		}
		n = v
	}
	lines, err := journalLines(unitName, n)
	if err == nil {
		for _, l := range lines {
			fmt.Println(l)
		}
		return exitOK
	}
	// journalctl unavailable (no systemd, no permission): ask the daemon.
	// GET /api/log answers {"lines": ["..."]}.
	var resp struct {
		Lines []string `json:"lines"`
	}
	if aerr := newAPI(runDir()).get("/api/log?lines="+strconv.Itoa(n), &resp); aerr != nil {
		fmt.Fprintf(os.Stderr, "log: journalctl: %v; api: %v\n", err, aerr)
		return exitFail
	}
	for _, l := range resp.Lines {
		fmt.Println(l)
	}
	return exitOK
}
