package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

func init() {
	register("alerts", command{run: cmdAlerts})
}

// cmdAlerts mirrors the dashboard's Alerts tab:
//
//	alerts status     transport (configured / effective), tools, PVE template state, last alert per kind, recent alerts
//	alerts test       send a "test" alert now (no cooldown) and report the delivery result
//	alerts template   install/update the PVE notification template pair
//
// With the daemon running, status and test go through the unix socket
// (GET /api/alerts, POST /api/alerts/test) — the same sink chain the
// controller uses, and the test lands in the dashboard's history.
// Without it the same manager runs on the config file. template asks the
// daemon first (POST /api/alerts/template); when that fails for anything
// but "no PVE" the files are written directly from here — this process
// runs as root outside the service sandbox and may create the directory
// on a fresh box, which the daemon cannot (ProtectSystem=strict).
func cmdAlerts(args []string) int {
	fs := flag.NewFlagSet("alerts", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	usage := func() int {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov alerts status | test | template")
		return exitUsage
	}
	if fs.NArg() != 1 {
		return usage()
	}
	c := alertsClient{cfgPath: *cfgPath, dir: runDir()}
	switch fs.Arg(0) {
	case "status":
		return c.status()
	case "test":
		return c.test()
	case "template":
		return c.template()
	}
	return usage()
}

type alertsClient struct {
	cfgPath string
	dir     string // run dir (socket)
}

// alertsResp is GET /api/alerts: the status plus last-per-kind and the
// recent list.
type alertsResp struct {
	Transport     string `json:"transport"`
	Effective     string `json:"effective"`
	MailTo        string `json:"mail_to"`
	PVEAvailable  bool   `json:"pve_available"`
	MailAvailable bool   `json:"mail_available"`
	Template      struct {
		Installed bool   `json:"installed"`
		Current   bool   `json:"current"`
		Writable  bool   `json:"writable"`
		Path      string `json:"path"`
		Reason    string `json:"reason"`
	} `json:"template"`
	Cooldown string `json:"cooldown"`
	Kinds    []struct {
		Kind        string `json:"kind"`
		Description string `json:"description"`
	} `json:"kinds"`
	Last   map[string]int64 `json:"last"`
	Recent []struct {
		TS   int64  `json:"ts"`
		Kind string `json:"kind"`
		Msg  string `json:"msg"`
	} `json:"recent"`
}

// offline builds the manager on the config file and the state directory
// (history file when present).
func (c alertsClient) offline() *alertManager {
	cfg, warns, _ := loadConfig(c.cfgPath)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "config: %s\n", w)
	}
	m := newAlertManager(c.cfgPath, alertsPath(stateDir()), cfgAlert(cfg), nil)
	cool := daemonOfCooldown(cfg)
	m.cooldown = func() time.Duration { return cool }
	return m
}

func (c alertsClient) status() int {
	var r alertsResp
	err := newAPI(c.dir).get("/api/alerts", &r)
	switch {
	case err == nil:
		printAlertStatus(r, "daemon")
		return exitOK
	case !errors.Is(err, errNoDaemon):
		fmt.Fprintln(os.Stderr, "alerts status:", err)
		return exitFail
	}
	m := c.offline()
	s := m.Status()
	r.Transport, r.Effective, r.MailTo = s.Transport, s.Effective, s.MailTo
	r.PVEAvailable, r.MailAvailable, r.Cooldown = s.PVEAvailable, s.MailAvailable, s.Cooldown
	r.Template.Installed, r.Template.Current, r.Template.Writable = s.Template.Installed, s.Template.Current, s.Template.Writable
	r.Template.Path, r.Template.Reason = s.Template.Path, s.Template.Reason
	for _, k := range s.Kinds {
		r.Kinds = append(r.Kinds, struct {
			Kind        string `json:"kind"`
			Description string `json:"description"`
		}{k.Kind, k.Description})
	}
	r.Last = m.Last()
	for _, rec := range m.Recent(10) {
		r.Recent = append(r.Recent, struct {
			TS   int64  `json:"ts"`
			Kind string `json:"kind"`
			Msg  string `json:"msg"`
		}{rec.TS, rec.Kind, rec.Msg})
	}
	printAlertStatus(r, "config file (daemon not running)")
	return exitOK
}

func printAlertStatus(r alertsResp, source string) {
	fmt.Printf("transport:    %s -> %s (source: %s)\n", r.Transport, r.Effective, source)
	if r.Transport == "mail" || r.Transport == "auto" {
		fmt.Printf("mail_to:      %s\n", r.MailTo)
	}
	fmt.Printf("available:    pve-notify=%s  mail=%s\n", yesNo(r.PVEAvailable), yesNo(r.MailAvailable))
	tmpl := "not installed"
	switch {
	case r.Template.Installed && r.Template.Current:
		tmpl = "installed, current"
	case r.Template.Installed:
		tmpl = "installed, OUTDATED (run: n5-fangov alerts template)"
	}
	if !r.Template.Writable && r.Template.Reason != "" {
		tmpl += "; " + r.Template.Reason
	}
	fmt.Printf("PVE template: %s (%s)\n", tmpl, r.Template.Path)
	if r.Cooldown != "" {
		fmt.Printf("cooldown:     %s per kind\n", r.Cooldown)
	}
	if len(r.Last) > 0 {
		kinds := make([]string, 0, len(r.Last))
		for k := range r.Last {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		fmt.Println()
		for _, k := range kinds {
			fmt.Printf("last %-16s %s\n", k+":", time.Unix(r.Last[k], 0).Format("2006-01-02 15:04"))
		}
	}
	if len(r.Recent) > 0 {
		fmt.Println()
		fmt.Println("recent (newest first):")
		for _, rec := range r.Recent {
			fmt.Printf("  %s  %-16s %s\n", time.Unix(rec.TS, 0).Format("2006-01-02 15:04"), rec.Kind, firstLine(rec.Msg))
		}
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (c alertsClient) test() int {
	var resp struct {
		Transport string `json:"transport"`
		Error     string `json:"error"`
	}
	// The daemon delivers synchronously for up to testDeliveryLimit (perl
	// or mail); the default 5 s client would give up first and a retry
	// would meet 409 "test in progress".
	a := newAPI(c.dir)
	a.c.Timeout = testDeliveryLimit + 5*time.Second
	_, err := a.doRaw("POST", "/api/alerts/test", "", nil, &resp)
	switch {
	case err == nil:
		fmt.Printf("test alert sent via %s (daemon); it is listed in the dashboard's recent alerts\n", resp.Transport)
		return exitOK
	case !errors.Is(err, errNoDaemon):
		fmt.Fprintln(os.Stderr, "alerts test:", err)
		return exitFail
	}
	m := c.offline()
	eff, err := m.Test()
	if err != nil {
		fmt.Fprintf(os.Stderr, "alerts test: delivery via %s failed: %v\n", eff, err)
		return exitFail
	}
	fmt.Printf("test alert sent via %s (daemon not running: sent from this process)\n", eff)
	return exitOK
}

func (c alertsClient) template() int {
	var resp struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	}
	status, err := newAPI(c.dir).doRaw("POST", "/api/alerts/template", "", nil, &resp)
	switch {
	case err == nil:
		fmt.Printf("PVE notification template installed in %s (daemon)\n", resp.Path)
		printTemplateHint()
		return exitOK
	case status == 501:
		fmt.Fprintln(os.Stderr, "alerts template:", err)
		return exitFail
	case !errors.Is(err, errNoDaemon):
		fmt.Fprintf(os.Stderr, "alerts template: daemon: %v; writing the files from here\n", err)
	}
	path, err := installAlertTemplate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "alerts template:", err)
		return exitFail
	}
	fmt.Printf("PVE notification template installed in %s\n", path)
	printTemplateHint()
	return exitOK
}

func printTemplateHint() {
	fmt.Println(strings.Join([]string{
		"The Proxmox side: Datacenter -> Notifications -> Notification Matchers: a matcher on",
		"  field  type = n5-fangov  (or kind = <alert kind>) routes the alerts to a target of your choice.",
		"Without a matcher they follow the default matcher (severity warning -> mail-to-root).",
	}, "\n"))
}
