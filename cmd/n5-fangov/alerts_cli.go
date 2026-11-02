package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
)

func init() {
	register("alerts", command{run: cmdAlerts})
}

// cmdAlerts mirrors the dashboard's Alerts tab:
//
//	alerts status     transport (configured / effective), tools, PVE template state, last alert per kind, recent alerts
//	alerts test       send a "test" alert now (no cooldown) and report the delivery result
//	alerts template   install/update the PVE notification template pair
//	alerts ack ID...  acknowledge alerts (display only); alerts ack --all: every open one
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
	all := fs.Bool("all", false, "ack: every open alert")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return exitUsage
	}
	usage := func() int {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov alerts status | test | template | ack ID... | ack --all")
		return exitUsage
	}
	c := alertsClient{cfgPath: *cfgPath, dir: runDir()}
	if len(pos) >= 1 && pos[0] == "ack" {
		ids, ok := parseAckIDs(pos[1:])
		if !ok || *all == (len(ids) > 0) {
			return usage()
		}
		return c.ack(ids, *all)
	}
	if len(pos) != 1 || *all {
		return usage()
	}
	switch pos[0] {
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
	WebhookURL    string `json:"webhook_url"`
	WebhookFormat string `json:"webhook_format"`
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
	Recent []alertLine      `json:"recent"`
	Open   int              `json:"open"`
}

// alertLine is one entry of the recent list.
type alertLine struct {
	ID    uint64 `json:"id"`
	TS    int64  `json:"ts"`
	Kind  string `json:"kind"`
	Msg   string `json:"msg"`
	AckTS int64  `json:"ack_ts"`
	AckBy string `json:"ack_by"`
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
	r.WebhookURL, r.WebhookFormat = s.WebhookURL, s.WebhookFormat
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
		r.Recent = append(r.Recent, alertLine{rec.ID, rec.TS, rec.Kind, rec.Msg, rec.AckTS, rec.AckBy})
	}
	r.Open = m.Open()
	printAlertStatus(r, "config file (daemon not running)")
	return exitOK
}

func printAlertStatus(r alertsResp, source string) {
	fmt.Printf("transport:    %s -> %s (source: %s)\n", r.Transport, r.Effective, source)
	if r.Transport == "mail" || r.Transport == "auto" {
		fmt.Printf("mail_to:      %s\n", r.MailTo)
	}
	if r.Transport == "webhook" || r.WebhookURL != "" {
		// query and userinfo stay out of the terminal (Gotify keys)
		fmt.Printf("webhook:      %s (%s)\n", alert.RedactURL(r.WebhookURL), r.WebhookFormat)
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
		fmt.Printf("recent (newest first, %d open):\n", r.Open)
		for _, rec := range r.Recent {
			ack := "open"
			if rec.AckTS != 0 {
				ack = "ack " + rec.AckBy
			}
			fmt.Printf("  #%-4d %s  %-16s %-14s %s\n", rec.ID, time.Unix(rec.TS, 0).Format("2006-01-02 15:04"), rec.Kind, ack, firstLine(rec.Msg))
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

// parseAckIDs reads the alert IDs of "alerts ack" ("#12" and "12").
func parseAckIDs(args []string) ([]uint64, bool) {
	var ids []uint64
	for _, a := range args {
		n, err := strconv.ParseUint(strings.TrimPrefix(a, "#"), 10, 64)
		if err != nil || n == 0 {
			return nil, false
		}
		ids = append(ids, n)
	}
	return ids, true
}

// ack acknowledges through the daemon (POST /api/alerts/ack); without it
// directly in the state file. --all takes the newest ID the ring holds
// now as the limit, so an alert arriving meanwhile stays open.
func (c alertsClient) ack(ids []uint64, all bool) int {
	a := newAPI(c.dir)
	body := map[string]any{"ids": ids}
	if all {
		var r alertsResp
		err := a.get("/api/alerts", &r)
		switch {
		case errors.Is(err, errNoDaemon):
			return c.ackOffline(ids, all)
		case err != nil:
			fmt.Fprintln(os.Stderr, "alerts ack:", err)
			return exitFail
		case len(r.Recent) == 0:
			fmt.Println("no alerts")
			return exitOK
		}
		body = map[string]any{"all": true, "up_to": r.Recent[0].ID}
	}
	var resp struct {
		Acked int `json:"acked"`
		Open  int `json:"open"`
	}
	err := a.do("POST", "/api/alerts/ack", body, &resp)
	switch {
	case err == nil:
		fmt.Printf("%d alert(s) acknowledged, %d open (daemon)\n", resp.Acked, resp.Open)
		return exitOK
	case !errors.Is(err, errNoDaemon):
		fmt.Fprintln(os.Stderr, "alerts ack:", err)
		return exitFail
	}
	return c.ackOffline(ids, all)
}

// ackOffline acknowledges in the state file while the daemon is down.
// With the unit active (the socket did not answer in time) it refuses:
// the daemon would overwrite the file with its own copy.
func (c alertsClient) ackOffline(ids []uint64, all bool) int {
	if unitActive(unitName) {
		fmt.Fprintln(os.Stderr, "alerts ack: the daemon does not answer on its socket although the unit is active; nothing written (the daemon would overwrite alerts.json) - try again")
		return exitFail
	}
	m := c.offline()
	var n int
	if all {
		rec := m.Recent(1)
		if len(rec) == 0 {
			fmt.Println("no alerts")
			return exitOK
		}
		n = m.AckUpTo(rec[0].ID, "cli")
	} else {
		var unknown []uint64
		n, unknown = m.Ack(ids, "cli")
		if len(unknown) > 0 {
			fmt.Fprintf(os.Stderr, "alerts ack: unknown alert id(s) %v; nothing acknowledged\n", unknown)
			return exitFail
		}
	}
	// the ring logs a failed write only; read the file back to know
	if n > 0 && c.offline().Open() != m.Open() {
		fmt.Fprintf(os.Stderr, "alerts ack: %s not written\n", alertsPath(stateDir()))
		return exitFail
	}
	fmt.Printf("%d alert(s) acknowledged, %d open (state file, daemon not running)\n", n, m.Open())
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
