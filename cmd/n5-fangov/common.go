package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// runDir returns the runtime directory: N5FANGOV_RUN_DIR or /run/n5-fangov.
func runDir() string {
	if d := os.Getenv("N5FANGOV_RUN_DIR"); d != "" {
		return d
	}
	return defaultRunDir
}

func socketPath(dir string) string { return filepath.Join(dir, socketName) }
func statePath(dir string) string  { return filepath.Join(dir, stateFileName) }

// api is a thin client for the daemon's HTTP API over the unix socket.
type api struct {
	c    *http.Client
	sock string
	base string
}

func newAPI(dir string) *api {
	sock := socketPath(dir)
	c := ipcClient(sock)
	c.Timeout = 5 * time.Second
	return &api{c: c, sock: sock, base: "http://n5-fangov"}
}

// errNoDaemon is returned when the socket does not answer (daemon not
// running, socket missing, connection refused).
var errNoDaemon = errors.New("daemon not reachable")

// errPermission is returned when the socket exists but this process may not
// open it. The runtime directory is 0750 root:root by design, so this is the
// "not root" case and must not be mistaken for a stopped daemon.
var errPermission = errors.New("permission denied")

// classifyDialErr turns a transport error from the unix socket into one of
// the two sentinels. EACCES/EPERM anywhere in the chain (net.OpError ->
// os.SyscallError -> syscall.Errno) means "run as root"; everything else is
// treated as "daemon not reachable" so callers can fall back to state.json.
func classifyDialErr(sock string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w on %s — run as root (the socket is root-only by design)", errPermission, sock)
	}
	return fmt.Errorf("%w: %v", errNoDaemon, err)
}

// get decodes a JSON GET response into out.
func (a *api) get(path string, out any) error {
	return a.do(http.MethodGet, path, nil, out)
}

// do performs a request with an optional JSON body and decodes into out (may be nil).
func (a *api) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.c.Do(req)
	if err != nil {
		return classifyDialErr(a.sock, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(data))
		var je struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &je) == nil && je.Error != "" {
			msg = je.Error
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// daemonRunning reports whether the socket answers /api/version.
func daemonRunning(dir string) bool {
	if _, err := os.Stat(socketPath(dir)); err != nil {
		return false
	}
	var v any
	return newAPI(dir).get("/api/version", &v) == nil
}

// parseDuty accepts "191" or "75%" and returns a duty 0..255.
func parseDuty(s string) (int, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "%") {
		p, err := strconv.Atoi(strings.TrimSuffix(s, "%"))
		if err != nil || p < 0 || p > 100 {
			return 0, fmt.Errorf("percentage must be 0..100: %q", s)
		}
		return (p*255 + 50) / 100, nil
	}
	d, err := strconv.Atoi(s)
	if err != nil || d < 0 || d > 255 {
		return 0, fmt.Errorf("duty must be 0..255 or NN%%: %q", s)
	}
	return d, nil
}

// pct converts a duty 0..255 to a percentage.
func pct(duty int) int { return (duty*100 + 127) / 255 }

// journalLines returns the last n lines of the unit's journal.
func journalLines(unit string, n int) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", strconv.Itoa(n), "--no-pager", "-o", "short").Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}

// systemctlValue runs `systemctl show -p KEY --value UNIT`.
func systemctlValue(unit, key string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "-p", key, "--value", unit).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// unitActive reports whether `systemctl is-active UNIT` says "active".
func unitActive(unit string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
	return strings.TrimSpace(string(out)) == "active"
}

// fmtTemp prints a temperature or "?" for the unknown marker.
func fmtTemp(t float64) string {
	if t <= -900 {
		return "    ?"
	}
	return fmt.Sprintf("%5.1f", t)
}

// fmtRPM prints an rpm or "-" when the channel has no tach.
func fmtRPM(rpm int) string {
	if rpm < 0 {
		return "-"
	}
	return strconv.Itoa(rpm)
}

// fmtDuration prints seconds as 1d02h03m / 2h03m / 3m04s.
func fmtDuration(sec int64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%02dh%02dm", int(d.Hours())/24, int(d.Hours())%24, int(d.Minutes())%60)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}
