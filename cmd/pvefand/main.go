// Command pvefand is a guarded fan controller for Proxmox VE and Debian.
//
// main.go only dispatches. Every subcommand lives in its own file and
// registers itself from init(). The files that wire the internal packages
// together (serve, status, set, auto, curve, log, check, detect, test,
// failsafe, alert) carry the build tag "integrate" until all packages are
// merged; without the tag those subcommands report "not available".
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/SirRenix/pvefand/internal/version"
)

// Exit codes shared by all subcommands.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// Default paths; overridable per subcommand via flags or PVEFAND_RUN_DIR.
const (
	defaultConfigPath = "/etc/pvefand/config.toml"
	defaultPresetDir  = "/etc/pvefand/presets"
	defaultRunDir     = "/run/pvefand"
	socketName        = "pvefand.sock"
	stateFileName     = "state.json"
	unitName          = "pvefand"
)

// command is one subcommand: run returns the process exit code.
type command struct {
	run    func(args []string) int
	help   string // "<args>  description", shown by usage
	hidden bool   // not listed in usage (e.g. "alert" used by the onfailure unit)
}

// commands is filled by init() in the subcommand files.
var commands = map[string]command{}

// register adds a subcommand. Called from init() only.
func register(name string, c command) { commands[name] = c }

// order lists every public subcommand in usage order. A name that appears
// here but is not registered belongs to a file excluded by build tag.
var order = []string{
	"serve", "status", "set", "auto", "curve", "log",
	"check", "detect", "test", "failsafe", "version",
}

// helpText is the usage line per subcommand (kept here so usage is complete
// even when the implementation file is excluded by the build tag).
var helpText = map[string]string{
	"serve":    "[--config PATH] [--dry-run] [--run-dir DIR] [--listen ADDR]  run the daemon",
	"status":   "                       show channels, temperatures, duty, rpm, mode",
	"set":      "<ch> <duty|NN%>        manual override for one channel",
	"auto":     "<ch|all>               return channel(s) to the curve",
	"curve":    "                       print the configured curves",
	"log":      "[n]                    last n journal lines (default 50)",
	"check":    "[--quiet]              self-check: config, profile, sysfs, socket, dkms",
	"detect":   "                       list profiles with detection result (read-only)",
	"test":     "<ch> [--force]         channel verification run (writes duty, daemon must be stopped)",
	"failsafe": "                       put all configured channels into their safe state",
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
		if _, known := helpText[name]; known {
			fmt.Fprintf(os.Stderr, "pvefand: subcommand %q is not available in this build (compiled without the integrate tag)\n", name)
			return exitUsage
		}
		fmt.Fprintf(os.Stderr, "pvefand: unknown subcommand %q\n", name)
		usage(os.Stderr)
		return exitUsage
	}
	return c.run(args[1:])
}

func cmdVersion([]string) int {
	fmt.Println("pvefand", version.Version)
	return exitOK
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: pvefand <subcommand> [args]")
	fmt.Fprintln(w)
	for _, n := range order {
		if c, ok := commands[n]; ok && c.hidden {
			continue
		}
		fmt.Fprintf(w, "  %-9s %s\n", n, helpText[n])
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Environment: PVEFAND_RUN_DIR (default /run/pvefand), PVEFAND_SYSFS (default /sys)")
}
