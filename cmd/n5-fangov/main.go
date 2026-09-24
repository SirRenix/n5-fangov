// Command n5-fangov is a guarded fan controller for Proxmox VE and Debian.
//
// main.go only dispatches. Every subcommand lives in its own file and
// registers itself from init(); the adapters to the internal packages live
// in the wiring*.go files.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/SirRenix/n5-fangov/internal/version"
)

// Exit codes shared by all subcommands.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// Default paths; overridable per subcommand via flags or N5FANGOV_RUN_DIR.
const (
	defaultConfigPath = "/etc/n5-fangov/config.toml"
	defaultPresetDir  = "/etc/n5-fangov/presets"
	defaultRunDir     = "/run/n5-fangov"
	socketName        = "n5-fangov.sock"
	stateFileName     = "state.json"
	unitName          = "n5-fangov"
)

// command is one subcommand: run returns the process exit code; the usage
// line comes from helpText.
type command struct {
	run    func(args []string) int
	hidden bool // not listed in usage (e.g. "alert" used by the onfailure unit)
}

// commands is filled by init() in the subcommand files.
var commands = map[string]command{}

// register adds a subcommand. Called from init() only.
func register(name string, c command) { commands[name] = c }

// order lists every public subcommand in usage order.
var order = []string{
	"setup", "serve", "status", "set", "auto", "curve", "log",
	"check", "detect", "system", "test", "failsafe",
	"passwd", "cert", "alerts", "token", "export", "import", "version",
}

// helpText is the usage line per subcommand.
var helpText = map[string]string{
	"setup":    "[--listen local|lan|HOST:PORT] [--user U] [--password-file F | --password -] [--yes] [--fresh]  write the config for this machine (keeps alerts, schedules, hysteresis …; --fresh: defaults)",
	"serve":    "[--config PATH] [--dry-run] [--run-dir DIR] [--state-dir DIR] [--listen ADDR]  run the daemon",
	"status":   "                       show channels, temperatures, duty, rpm, mode",
	"set":      "<ch> <duty|NN%>        manual override for one channel",
	"auto":     "<ch|all>               return channel(s) to the curve",
	"curve":    "                       print the configured curves",
	"log":      "[-n N] [--export FILE] [--clear]  log file (journal when no file is configured)",
	"check":    "[--quiet] [--after-update]  self-check; --after-update: DKMS module for every kernel (apt hook)",
	"detect":   "                       list profiles with detection result (read-only)",
	"system":   "[--json]               hardware inventory: machine, CPU, memory modules, GPU/NPU, NICs, disks (live via the daemon)",
	"test":     "<ch> [--force]         channel verification run (writes duty, daemon must be stopped)",
	"failsafe": "                       put all configured channels into their safe state",
	"passwd":   "[--user U] [--password-file F | --password -]  set the web user/password (auth = basic), restart to apply; the dashboard (signed in) changes both live",
	"cert":     "info | export [--der] [FILE] | regen [--new-key] | upload CERT KEY | reset  dashboard certificate (live via the daemon)",
	"alerts":   "status | test | template  alert transport, test alert, PVE notification template (live via the daemon)",
	"token":    "create NAME [--scope read|control|admin] [--ttl DAYS] | list | revoke ID  API tokens for scripts and agents (via the daemon)",
	"export":   "[FILE]                 settings bundle (config + presets, hash redacted) as JSON",
	"import":   "FILE                   restore a settings bundle (validated first), reload the daemon",
	"version":  "                       print version",
}

func init() {
	register("version", command{run: cmdVersion})
	register("help", command{run: func([]string) int { usage(os.Stdout); return exitOK }, hidden: true})
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitUsage
	}
	name := args[0]
	switch name {
	case "-h", "--help", "-help":
		usage(os.Stdout)
		return exitOK
	case "-v", "--version":
		name = "version"
	}
	c, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "n5-fangov: unknown subcommand %q\n", name)
		usage(os.Stderr)
		return exitUsage
	}
	return c.run(args[1:])
}

func cmdVersion([]string) int {
	fmt.Println("n5-fangov", version.Version)
	return exitOK
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: n5-fangov <subcommand> [args]")
	fmt.Fprintln(w)
	for _, n := range order {
		if c, ok := commands[n]; ok && c.hidden {
			continue
		}
		fmt.Fprintf(w, "  %-9s %s\n", n, helpText[n])
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Environment: N5FANGOV_RUN_DIR (default /run/n5-fangov), N5FANGOV_STATE_DIR (default /var/lib/n5-fangov), N5FANGOV_SYSFS (default /sys)")
}
