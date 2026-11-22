package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/cfgver"
	"github.com/SirRenix/n5-fangov/internal/web"
)

func init() {
	register("config", command{run: cmdConfig})
}

// versionLine is a version as GET /api/config/versions answers it.
type versionLine struct {
	ID              int         `json:"id"`
	TS              int64       `json:"ts"`
	By              string      `json:"by"`
	Reason          string      `json:"reason"`
	Sections        []string    `json:"sections"`
	Diff            []cfgver.Op `json:"diff"`
	Changes         []string    `json:"changes"`
	Removed         int         `json:"removed"`
	Added           int         `json:"added"`
	PasswordChanged bool        `json:"password_changed"`
	RestartKeys     []string    `json:"restart_keys"`
	CurrentSHA      string      `json:"config_sha256"`
	Warnings        []string    `json:"warnings"`
}

const configUsage = "usage: n5-fangov config versions | diff ID | restore ID [--yes]"

// cmdConfig is the config versions from the shell (DESIGN "Config
// versions"):
//
//	config versions      list, newest first
//	config diff ID       the diff current → version ID (password_hash redacted)
//	config restore ID    restore version ID (through the daemon only; asks first,
//	                     --yes does not)
//
// With the daemon it asks over the socket; without it versions and diff
// read <state dir>/config-versions and the config file, restore is refused.
func cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, configUsage)
		return exitUsage
	}
	id, yes := 0, false
	rest := args[:0:0]
	for _, a := range args {
		if a == "--yes" {
			yes = true
			continue
		}
		rest = append(rest, a)
	}
	if args = rest; len(args) == 0 || yes && args[0] != "restore" {
		fmt.Fprintln(os.Stderr, configUsage)
		return exitUsage
	}
	if args[0] == "diff" || args[0] == "restore" {
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, configUsage)
			return exitUsage
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 {
			fmt.Fprintf(os.Stderr, "config %s: %q is not a version number\n", args[0], args[1])
			return exitUsage
		}
		id = n
	} else if len(args) != 1 {
		fmt.Fprintln(os.Stderr, configUsage)
		return exitUsage
	}
	var err error
	switch args[0] {
	case "versions":
		err = configVersions(os.Stdout, runDir(), stateDir(), defaultConfigPath)
	case "diff":
		err = configDiff(os.Stdout, runDir(), stateDir(), defaultConfigPath, id)
	case "restore":
		if !yes && !stdinIsTerminal() {
			// a script without --yes: EOF would read as "no" and exit 0, an open
			// pipe would block
			fmt.Fprintln(os.Stderr, "config restore: standard input is not a terminal; pass --yes to restore without the question (nothing restored)")
			return exitFail
		}
		err = configRestore(os.Stdout, os.Stdin, runDir(), id, yes)
	default:
		fmt.Fprintln(os.Stderr, configUsage)
		return exitUsage
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return exitFail
	}
	return exitOK
}

// stdinIsTerminal reports a character device on standard input (tests
// replace it).
var stdinIsTerminal = func() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func whoOf(by string) string {
	if by == "" {
		return "-"
	}
	return by
}

// offlineStore opens the version directory without the daemon.
func offlineStore(state string) (*cfgver.Store, string, error) {
	dir := configVersionsPath(state)
	if dir == "" {
		return nil, "", errors.New("no state directory")
	}
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, "", fmt.Errorf("daemon not running and %s not readable: %w (run as root)", dir, err)
		}
		return nil, "", fmt.Errorf("daemon not running and no versions in %s: %w", dir, err)
	}
	return cfgver.Open(dir, nil), dir + " (daemon not running)", nil
}

func configVersions(w io.Writer, run, state, cfgPath string) error {
	var list []versionLine
	source := "daemon"
	err := newAPI(run).get("/api/config/versions", &list)
	if errors.Is(err, errNoDaemon) {
		s, src, oerr := offlineStore(state)
		if oerr != nil {
			return oerr
		}
		source = src
		vs, lerr := s.List()
		if lerr != nil {
			return lerr
		}
		cur, _ := os.ReadFile(cfgPath)
		list = nil
		for _, v := range vs {
			vl := versionLine{ID: v.ID, TS: v.TS, By: v.By, Reason: v.Reason}
			if _, raw, gerr := s.Get(v.ID); gerr == nil {
				next, _ := s.Next(v.ID)
				if next == nil {
					next = cur
				}
				vl.Sections = cfgver.Sections(string(raw), string(next))
			}
			list = append(list, vl)
		}
	} else if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintf(w, "no config versions (source: %s)\n", source)
		return nil
	}
	for _, v := range list {
		fmt.Fprintf(w, "%4d  %s  %-14s %-22s %s\n", v.ID, time.Unix(v.TS, 0).Format("2006-01-02 15:04:05"), whoOf(v.By), v.Reason, strings.Join(v.Sections, ", "))
	}
	fmt.Fprintf(w, "%d version(s), source: %s; `n5-fangov config diff ID` shows one against the current file\n", len(list), source)
	return nil
}

func configDiff(w io.Writer, run, state, cfgPath string, id int) error {
	var v versionLine
	err := newAPI(run).get(fmt.Sprintf("/api/config/versions/%d", id), &v)
	if errors.Is(err, errNoDaemon) {
		s, _, oerr := offlineStore(state)
		if oerr != nil {
			return oerr
		}
		meta, raw, gerr := s.Get(id)
		if gerr != nil {
			return gerr
		}
		cur, _ := os.ReadFile(cfgPath)
		_, d, changed := web.VersionDiff(raw, cur)
		v = versionLine{ID: meta.ID, TS: meta.TS, By: meta.By, Reason: meta.Reason, Diff: d, PasswordChanged: changed}
		v.Removed, v.Added = cfgver.Count(d)
	} else if err != nil {
		return err
	}
	fmt.Fprintf(w, "version %d, %s, %s, %s — diff current → version (- current, + version)\n", v.ID, time.Unix(v.TS, 0).Format("2006-01-02 15:04:05"), whoOf(v.By), v.Reason)
	if v.Removed == 0 && v.Added == 0 {
		fmt.Fprintln(w, "identical to the current file")
	}
	for _, o := range v.Diff {
		if o.Op != " " {
			fmt.Fprintf(w, "%s %s\n", o.Op, o.Line)
		}
	}
	if v.PasswordChanged {
		fmt.Fprintln(w, "note: the version's password_hash differs from the current one; a restore resets the password")
	}
	return nil
}

// configRestore shows what a restore changes (lines, sections, restart-only
// keys, the warnings, a password change), asks unless yes, then restores
// through the daemon and names the version that undoes it.
func configRestore(w io.Writer, in io.Reader, run string, id int, yes bool) error {
	a := newAPI(run)
	var v versionLine
	err := a.get(fmt.Sprintf("/api/config/versions/%d", id), &v)
	if errors.Is(err, errNoDaemon) {
		return errors.New("the daemon is not running: restore refused (it validates, writes and reloads through the daemon); start it first")
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "restore version %d (%s, %s): %d line(s) go, %d come", id, whoOf(v.By), v.Reason, v.Removed, v.Added)
	if len(v.Changes) > 0 {
		fmt.Fprintf(w, " in %s", strings.Join(v.Changes, ", "))
	}
	fmt.Fprintln(w)
	if len(v.RestartKeys) > 0 {
		fmt.Fprintf(w, "applies after restart: %s\n", strings.Join(v.RestartKeys, ", "))
	}
	for _, x := range v.Warnings {
		fmt.Fprintln(w, "warning:", x)
	}
	if v.PasswordChanged {
		fmt.Fprintln(w, "warning: this also resets the web password to the one of that version (after a restart)")
	}
	if !yes {
		fmt.Fprint(w, "Restore? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(w, "nothing restored")
			return nil
		}
	}
	var res struct {
		Version         int      `json:"version"`
		RestartRequired bool     `json:"restart_required"`
		RestartKeys     []string `json:"restart_keys"`
		Warnings        []string `json:"warnings"`
	}
	// if_current: the restore applies only to the file the summary above described
	if err := a.do(http.MethodPost, fmt.Sprintf("/api/config/versions/%d/restore", id), map[string]any{"if_current": v.CurrentSHA}, &res); err != nil {
		if strings.Contains(err.Error(), "409") {
			return fmt.Errorf("the config changed after the summary above, nothing restored; run the command again: %w", err)
		}
		return err
	}
	fmt.Fprintf(w, "restored version %d", id)
	if res.Version > 0 {
		fmt.Fprintf(w, " (the text before is version %d: `n5-fangov config restore %d` undoes it)", res.Version, res.Version)
	}
	fmt.Fprintln(w)
	if len(res.RestartKeys) > 0 {
		fmt.Fprintf(w, "applies after restart: %s (systemctl restart n5-fangov)\n", strings.Join(res.RestartKeys, ", "))
	}
	if res.RestartRequired {
		fmt.Fprintln(w, "restart required: the channel set or profile changed (systemctl restart n5-fangov)")
	}
	for _, x := range res.Warnings {
		fmt.Fprintln(w, "warning:", x)
	}
	return nil
}
