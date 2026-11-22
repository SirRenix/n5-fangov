package main

import (
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
	Removed         int         `json:"removed"`
	Added           int         `json:"added"`
	PasswordChanged bool        `json:"password_changed"`
}

const configUsage = "usage: n5-fangov config versions | diff ID | restore ID"

// cmdConfig is the config versions from the shell (DESIGN "Config
// versions"):
//
//	config versions      list, newest first
//	config diff ID       the diff current → version ID (password_hash redacted)
//	config restore ID    restore version ID (through the daemon only)
//
// With the daemon it asks over the socket; without it versions and diff
// read <state dir>/config-versions and the config file, restore is refused.
func cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, configUsage)
		return exitUsage
	}
	id := 0
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
		err = configRestore(os.Stdout, runDir(), id)
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

func configRestore(w io.Writer, run string, id int) error {
	var res struct {
		Version         int      `json:"version"`
		RestartRequired bool     `json:"restart_required"`
		RestartKeys     []string `json:"restart_keys"`
		Warnings        []string `json:"warnings"`
	}
	err := newAPI(run).do(http.MethodPost, fmt.Sprintf("/api/config/versions/%d/restore", id), nil, &res)
	if errors.Is(err, errNoDaemon) {
		return errors.New("the daemon is not running: restore refused (it validates, writes and reloads through the daemon); start it first")
	}
	if err != nil {
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
