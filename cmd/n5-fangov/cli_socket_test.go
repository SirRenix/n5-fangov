package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/ipc"
)

// Table tests for the everyday CLI commands (AUDIT 7 / Anhang B: status,
// set, auto, curve, log were at 0 %). A fake daemon answers on the unix
// socket in N5FANGOV_RUN_DIR; output is captured from os.Stdout/os.Stderr.

// fakeDaemon is the API subset the CLI talks to, served over the socket.
type fakeDaemon struct {
	mu        sync.Mutex
	snap      control.Snapshot
	overrides map[string]int
	cleared   []string
	logLines  []string
	logLinesN []int // the ?lines= values seen
	logClears int
	configRaw string
	srv       *http.Server
	dir       string
	// extra installs further routes (token_cli_test.go); nil = none.
	extra func(*http.ServeMux)
}

func (d *fakeDaemon) handler() http.Handler {
	mux := http.NewServeMux()
	jsonErr := func(w http.ResponseWriter, code int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "0.0.0-test"})
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		_ = json.NewEncoder(w).Encode(d.snap)
	})
	mux.HandleFunc("PUT /api/override/{name}", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Duty *int `json:"duty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Duty == nil {
			jsonErr(w, 400, "duty required")
			return
		}
		name := r.PathValue("name")
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, c := range d.snap.Channels {
			if c.Name == name {
				d.overrides[name] = *b.Duty
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": name, "duty": *b.Duty, "mode": "manual"})
				return
			}
		}
		jsonErr(w, 404, "unknown channel "+name)
	})
	mux.HandleFunc("DELETE /api/override/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, c := range d.snap.Channels {
			if c.Name == name {
				d.cleared = append(d.cleared, name)
				delete(d.overrides, name)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": name, "mode": "auto"})
				return
			}
		}
		jsonErr(w, 404, "unknown channel "+name)
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"raw": d.configRaw, "config": map[string]any{}})
	})
	mux.HandleFunc("GET /api/log", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		d.mu.Lock()
		defer d.mu.Unlock()
		d.logLinesN = append(d.logLinesN, n)
		lines := d.logLines
		if n > 0 && n < len(lines) {
			lines = lines[len(lines)-n:]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"lines": lines, "source": "journal"})
	})
	mux.HandleFunc("DELETE /api/log", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.logClears++
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "cleared": true})
	})
	if d.extra != nil {
		d.extra(mux)
	}
	return mux
}

// startFakeDaemon serves the fake on <dir>/n5-fangov.sock and points
// N5FANGOV_RUN_DIR at dir. stop() removes the socket (daemon "down").
func startFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix socket CLI tests run on Linux (the build container)")
	}
	// unix socket paths are short-limited; MkdirTemp under the default
	// temp dir keeps them under ~40 bytes
	dir, err := os.MkdirTemp("", "n5cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("N5FANGOV_RUN_DIR", dir)
	d := &fakeDaemon{
		dir:       dir,
		overrides: map[string]int{},
		configRaw: "[daemon]\ninterval = \"10s\"\n\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45,85],[80,255]]\ncritical = 88\nstop = \"auto\"\n\n[[channel]]\nname = \"hdd\"\npwm = 3\nsensor = \"drivetemp:max\"\ncurve = [[36,105],[46,255]]\ncritical = 56\nstop = 140\n",
		logLines:  []string{"log line 1", "log line 2", "log line 3", "log line 4"},
		snap: control.Snapshot{
			TS: 1789500000, Status: "ok", Profile: "n5pro", Verified: true, HwmonPath: "/sys/class/hwmon/hwmon14", Uptime: 3725,
			Channels: []control.ChannelState{
				{Name: "cpu", PWM: 1, Sensor: "k10temp", Temp: 36.0, Duty: 85, Target: 85, RPM: 2000, Mode: control.ModeAuto},
				{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Temp: 31.5, Duty: 140, Target: 140, RPM: 900, Mode: control.ModeManual},
				{Name: "pcie", PWM: 4, Sensor: "k10temp", Temp: -999, Duty: 0, Target: 0, RPM: -1, Mode: control.ModeAuto},
			},
			ExtraTemps: map[string]float64{"ec:system": 32.5},
			Alerts:     map[string]int64{"stall": 1789490000},
		},
	}
	d.start(t)
	return d
}

func (d *fakeDaemon) start(t *testing.T) {
	t.Helper()
	ln, err := ipc.Listen(socketPath(d.dir))
	if err != nil {
		t.Fatal(err)
	}
	d.srv = &http.Server{Handler: d.handler()}
	go func() { _ = d.srv.Serve(ln) }()
	t.Cleanup(func() { d.stop() })
	if !daemonRunning(d.dir) {
		t.Fatal("fake daemon not reachable over the socket")
	}
}

// stop closes the server and removes the socket file, so the CLI sees
// "daemon not reachable" (errNoDaemon) rather than a hanging connect.
func (d *fakeDaemon) stop() {
	if d.srv != nil {
		_ = d.srv.Close()
		d.srv = nil
	}
	_ = os.Remove(socketPath(d.dir))
}

// captureOutput runs f with os.Stdout and os.Stderr redirected to pipes and
// returns both texts with the exit code.
func captureOutput(t *testing.T, f func() int) (stdout, stderr string, code int) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, errR) }()
	func() {
		defer func() {
			os.Stdout, os.Stderr = oldOut, oldErr
			outW.Close()
			errW.Close()
		}()
		code = f()
	}()
	wg.Wait()
	return outBuf.String(), errBuf.String(), code
}

func TestCLIStatus(t *testing.T) {
	d := startFakeDaemon(t)
	out, errOut, code := captureOutput(t, func() int { return cmdStatus(nil) })
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		"n5-fangov: ok  profile n5pro (verified)  uptime 1h02m  unit ",
		"hwmon: /sys/class/hwmon/hwmon14",
		"channel", "sensor", "temp", "duty", "rpm", "mode",
		"cpu", "k10temp", "36.0 C", "85", "33%", "2000", "auto",
		"hdd", "drivetemp:max", "31.5 C", "140", "55%", "900", "manual",
		"pcie", "? C", // unknown temperature and no tach
		"extra: ec:system 32.5 C",
		"last alert stall:     " + time.Unix(1789490000, 0).Format("2006-01-02 15:04"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "source:") || strings.Contains(out, "DRY-RUN") {
		t.Errorf("live status must not name a fallback source or dry-run:\n%s", out)
	}
	// the tach-less channel prints "-" for rpm and "?" for the temperature
	pcie := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "pcie") {
			pcie = line
		}
	}
	if fields := strings.Fields(pcie); len(fields) != 8 || fields[2] != "?" || fields[6] != "-" || fields[7] != "auto" {
		t.Errorf("pcie line: %q", pcie)
	}
	// extra argument → usage
	if _, errOut, code := captureOutput(t, func() int { return cmdStatus([]string{"x"}) }); code != exitUsage || !strings.Contains(errOut, "usage: n5-fangov status") {
		t.Errorf("status x: exit %d, stderr %q", code, errOut)
	}

	// daemon down, state.json present: fallback with age and source line
	d.stop()
	d.snap.DryRun = true
	d.snap.TS = 1 // very old
	data, _ := json.Marshal(d.snap)
	if err := os.WriteFile(statePath(d.dir), data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = captureOutput(t, func() int { return cmdStatus(nil) })
	if code != exitOK {
		t.Fatalf("fallback exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"source: state.json (", " old, daemon socket unavailable)", "DRY-RUN", "cpu"} {
		if !strings.Contains(out, want) {
			t.Errorf("fallback output lacks %q:\n%s", want, out)
		}
	}
	// corrupt state.json → exit 1 naming the file
	if err := os.WriteFile(statePath(d.dir), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := captureOutput(t, func() int { return cmdStatus(nil) }); code != exitFail || !strings.Contains(errOut, "state.json") {
		t.Errorf("corrupt state.json: exit %d, stderr %q", code, errOut)
	}
	// neither socket nor state.json → exit 1 with the not-running hint
	os.Remove(statePath(d.dir))
	if _, errOut, code := captureOutput(t, func() int { return cmdStatus(nil) }); code != exitFail || !strings.Contains(errOut, "daemon not running?") {
		t.Errorf("nothing available: exit %d, stderr %q", code, errOut)
	}
}

func TestCLISet(t *testing.T) {
	d := startFakeDaemon(t)
	cases := []struct {
		name   string
		args   []string
		code   int
		duty   int    // recorded override on success
		stdout string // substring on success
		stderr string // substring on failure
	}{
		{"duty", []string{"cpu", "100"}, exitOK, 100, "cpu: manual override duty 100 (39%); critical temperature and stall protection stay active", ""},
		{"duty_0", []string{"cpu", "0"}, exitOK, 0, "cpu: manual override duty 0 (0%)", ""},
		{"duty_255", []string{"cpu", "255"}, exitOK, 255, "cpu: manual override duty 255 (100%)", ""},
		{"percent", []string{"cpu", "50%"}, exitOK, 128, "cpu: manual override duty 128 (50%)", ""},
		{"percent_0", []string{"cpu", "0%"}, exitOK, 0, "duty 0 (0%)", ""},
		{"percent_100", []string{"cpu", "100%"}, exitOK, 255, "duty 255 (100%)", ""},
		{"padded", []string{"cpu", " 42 "}, exitOK, 42, "duty 42 (16%)", ""},
		{"duty_256", []string{"cpu", "256"}, exitUsage, -1, "", "duty must be 0..255 or NN%"},
		{"duty_300", []string{"cpu", "300"}, exitUsage, -1, "", "duty must be 0..255"},
		{"duty_negative", []string{"cpu", "-1"}, exitUsage, -1, "", "duty must be 0..255"},
		{"duty_text", []string{"cpu", "abc"}, exitUsage, -1, "", "duty must be 0..255"},
		{"percent_101", []string{"cpu", "101%"}, exitUsage, -1, "", "percentage must be 0..100"},
		{"percent_text", []string{"cpu", "x%"}, exitUsage, -1, "", "percentage must be 0..100"},
		{"one_arg", []string{"cpu"}, exitUsage, -1, "", "usage: n5-fangov set <channel> <duty|NN%>"},
		{"three_args", []string{"cpu", "1", "2"}, exitUsage, -1, "", "usage: n5-fangov set"},
		{"unknown_channel", []string{"nope", "10"}, exitFail, -1, "", "HTTP 404: unknown channel nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d.mu.Lock()
			d.overrides = map[string]int{}
			d.mu.Unlock()
			out, errOut, code := captureOutput(t, func() int { return cmdSet(c.args) })
			if code != c.code {
				t.Fatalf("exit %d, want %d (stdout %q, stderr %q)", code, c.code, out, errOut)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if c.code == exitOK {
				if d.overrides[c.args[0]] != c.duty || !strings.Contains(out, c.stdout) {
					t.Errorf("override %v, stdout %q", d.overrides, out)
				}
				return
			}
			if len(d.overrides) != 0 {
				t.Errorf("override sent on failure: %v", d.overrides)
			}
			if !strings.Contains(errOut, c.stderr) {
				t.Errorf("stderr %q lacks %q", errOut, c.stderr)
			}
		})
	}
	// daemon down: argument errors still come first (exit 2), a valid call
	// fails with the not-reachable text (exit 1)
	d.stop()
	if _, errOut, code := captureOutput(t, func() int { return cmdSet([]string{"cpu", "999"}) }); code != exitUsage || !strings.Contains(errOut, "0..255") {
		t.Errorf("down + bad duty: %d %q", code, errOut)
	}
	if _, errOut, code := captureOutput(t, func() int { return cmdSet([]string{"cpu", "100"}) }); code != exitFail || !strings.Contains(errOut, "daemon not reachable") {
		t.Errorf("down: %d %q", code, errOut)
	}
}

func TestCLIAuto(t *testing.T) {
	d := startFakeDaemon(t)
	out, errOut, code := captureOutput(t, func() int { return cmdAuto([]string{"cpu"}) })
	if code != exitOK || out != "cpu: back to curve\n" || errOut != "" {
		t.Errorf("auto cpu: %d %q %q", code, out, errOut)
	}
	// all: every channel of the snapshot, in snapshot order
	out, errOut, code = captureOutput(t, func() int { return cmdAuto([]string{"all"}) })
	if code != exitOK || out != "cpu: back to curve\nhdd: back to curve\npcie: back to curve\n" || errOut != "" {
		t.Errorf("auto all: %d %q %q", code, out, errOut)
	}
	d.mu.Lock()
	if strings.Join(d.cleared, ",") != "cpu,cpu,hdd,pcie" {
		t.Errorf("cleared = %v", d.cleared)
	}
	d.mu.Unlock()
	// unknown channel → exit 1 with the server's text; usage errors → 2
	if _, errOut, code := captureOutput(t, func() int { return cmdAuto([]string{"nope"}) }); code != exitFail || !strings.Contains(errOut, "auto nope: ") || !strings.Contains(errOut, "unknown channel nope") {
		t.Errorf("auto nope: %d %q", code, errOut)
	}
	for _, args := range [][]string{nil, {"cpu", "hdd"}} {
		if _, errOut, code := captureOutput(t, func() int { return cmdAuto(args) }); code != exitUsage || !strings.Contains(errOut, "usage: n5-fangov auto <channel|all>") {
			t.Errorf("auto %v: %d %q", args, code, errOut)
		}
	}
	// daemon down: both forms fail with exit 1
	d.stop()
	for _, args := range [][]string{{"cpu"}, {"all"}} {
		if _, errOut, code := captureOutput(t, func() int { return cmdAuto(args) }); code != exitFail || !strings.Contains(errOut, "daemon not reachable") {
			t.Errorf("auto %v down: %d %q", args, code, errOut)
		}
	}
}

func TestCLICurve(t *testing.T) {
	d := startFakeDaemon(t)
	out, errOut, code := captureOutput(t, func() int { return cmdCurve(nil) })
	if code != exitOK || errOut != "" {
		t.Fatalf("curve: %d stderr %q", code, errOut)
	}
	for _, want := range []string{
		"config from daemon; interval 10s",
		"cpu      pwm1  sensor k10temp          critical 88 C  stop auto",
		"45 C -> 85 (33%)   80 C -> 255 (100%)",
		"hdd      pwm3  sensor drivetemp:max    critical 56 C  stop 140",
		"36 C -> 105 (41%)   46 C -> 255 (100%)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("curve output lacks %q:\n%s", want, out)
		}
	}
	// warnings of the daemon's text go to stderr, the curve still prints
	d.mu.Lock()
	d.configRaw += "\n[[channel]]\nname = \"bad\"\npwm = 9\nsensor = \"k10temp\"\n"
	d.mu.Unlock()
	out, errOut, code = captureOutput(t, func() int { return cmdCurve(nil) })
	if code != exitOK || !strings.Contains(errOut, "warning: channel.bad.pwm") || !strings.Contains(out, "cpu      pwm1") || strings.Contains(out, "bad ") {
		t.Errorf("curve with warning: %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	if _, errOut, code := captureOutput(t, func() int { return cmdCurve([]string{"x"}) }); code != exitUsage || !strings.Contains(errOut, "usage: n5-fangov curve") {
		t.Errorf("curve x: %d %q", code, errOut)
	}
	// daemon down: the file fallback is the fixed /etc path
	d.stop()
	if _, err := os.Stat(defaultConfigPath); err == nil {
		t.Skipf("%s exists on this host; the file fallback would print it", defaultConfigPath)
	}
	if _, errOut, code := captureOutput(t, func() int { return cmdCurve(nil) }); code != exitFail || !strings.Contains(errOut, "daemon not reachable") || !strings.Contains(errOut, defaultConfigPath) {
		t.Errorf("curve down: %d %q", code, errOut)
	}
}

// TestCLILog covers the file-backed paths (lines, export, clear via the
// daemon and in place) and, when the host has no journalctl, the API
// fallback for the journal-only configuration.
func TestCLILog(t *testing.T) {
	d := startFakeDaemon(t)
	root := t.TempDir()
	t.Setenv("N5FANGOV_LOG_ROOT", root)
	logPath := filepath.Join(root, "n5-fangov.log")
	if err := os.WriteFile(logPath, []byte("file 1\nfile 2\nfile 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgFile := filepath.Join(root, "config.toml")
	if err := os.WriteFile(cfgFile, []byte("[log]\nfile = \""+filepath.ToSlash(logPath)+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgJournal := filepath.Join(root, "journal.toml")
	if err := os.WriteFile(cfgJournal, []byte("[log]\nfile = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, int) {
		return captureOutput(t, func() int { return cmdLog(args) })
	}
	// newest N lines of the file
	if out, errOut, code := run("--config", cfgFile, "-n", "2"); code != exitOK || out != "file 2\nfile 3\n" || errOut != "" {
		t.Errorf("log -n 2: %d %q %q", code, out, errOut)
	}
	if out, _, code := run("--config", cfgFile, "1"); code != exitOK || out != "file 3\n" {
		t.Errorf("log 1: %d %q", code, out)
	}
	if out, _, code := run("--config", cfgFile); code != exitOK || out != "file 1\nfile 2\nfile 3\n" {
		t.Errorf("log (default 50): %d %q", code, out)
	}
	// usage errors
	for _, args := range [][]string{{"--config", cfgFile, "0"}, {"--config", cfgFile, "x"}} {
		if _, errOut, code := run(args...); code != exitUsage || !strings.Contains(errOut, "n must be a positive integer") {
			t.Errorf("log %v: %d %q", args, code, errOut)
		}
	}
	if _, errOut, code := run("--config", cfgFile, "1", "2"); code != exitUsage || !strings.Contains(errOut, "usage: n5-fangov log") {
		t.Errorf("log 1 2: %d %q", code, errOut)
	}
	if _, errOut, code := run("--config", cfgFile, "--clear", "5"); code != exitUsage || !strings.Contains(errOut, "usage: n5-fangov log") {
		t.Errorf("log --clear 5: %d %q", code, errOut)
	}
	if _, _, code := run("--bogus"); code != exitUsage {
		t.Errorf("log --bogus: %d", code)
	}
	// export to a file and to stdout
	exp := filepath.Join(root, "export.txt")
	if out, _, code := run("--config", cfgFile, "--export", exp); code != exitOK || !strings.Contains(out, "exported to "+exp) {
		t.Errorf("log --export: %d %q", code, out)
	}
	if b, _ := os.ReadFile(exp); string(b) != "file 1\nfile 2\nfile 3\n" {
		t.Errorf("export content %q", b)
	}
	if out, _, code := run("--config", cfgFile, "--export", "-"); code != exitOK || out != "file 1\nfile 2\nfile 3\n" {
		t.Errorf("log --export -: %d %q", code, out)
	}
	// clear through the daemon (its bookkeeping is reset in the same step)
	if out, _, code := run("--config", cfgFile, "--clear"); code != exitOK || !strings.Contains(out, "cleared by the daemon") {
		t.Errorf("log --clear (daemon): %d %q", code, out)
	}
	d.mu.Lock()
	clears := d.logClears
	d.mu.Unlock()
	if clears != 1 {
		t.Errorf("daemon clears = %d", clears)
	}
	if b, _ := os.ReadFile(logPath); len(b) == 0 {
		t.Errorf("the CLI must not truncate when the daemon cleared (fake keeps the file): %q", b)
	}
	// journal-only config: --clear is refused
	if _, errOut, code := run("--config", cfgJournal, "--clear"); code != exitFail || !strings.Contains(errOut, "no log file configured") {
		t.Errorf("log --clear (journal): %d %q", code, errOut)
	}
	// daemon down: clear truncates in place
	d.stop()
	if out, _, code := run("--config", cfgFile, "--clear"); code != exitOK || !strings.Contains(out, logPath+" cleared (rotated files and the journal untouched)") {
		t.Errorf("log --clear (in place): %d %q", code, out)
	}
	if b, _ := os.ReadFile(logPath); len(b) != 0 {
		t.Errorf("file not truncated: %q", b)
	}
	// a missing log file with the daemon down and no journalctl → exit 1
	// naming both failures
	if _, err := exec.LookPath("journalctl"); err == nil {
		t.Skip("journalctl present on this host: the journal path is taken instead of the API fallback")
	}
	os.Remove(logPath)
	if _, errOut, code := run("--config", cfgFile, "-n", "1"); code != exitFail || !strings.Contains(errOut, "journalctl:") || !strings.Contains(errOut, "api: daemon not reachable") {
		t.Errorf("log without file/journal/daemon: %d %q", code, errOut)
	}
	// daemon back: journal-only config falls through to GET /api/log?lines=N
	d.start(t)
	if out, errOut, code := run("--config", cfgJournal, "-n", "2"); code != exitOK || out != "log line 3\nlog line 4\n" {
		t.Errorf("log via API: %d %q %q", code, out, errOut)
	}
	if out, _, code := run("--config", cfgJournal, "5000"); code != exitOK || out != "log line 1\nlog line 2\nlog line 3\nlog line 4\n" {
		t.Errorf("log 5000 via API: %d %q", code, out)
	}
	d.mu.Lock()
	seen := d.logLinesN
	d.mu.Unlock()
	if len(seen) != 2 || seen[0] != 2 || seen[1] != 5000 {
		t.Errorf("?lines= seen by the daemon: %v", seen)
	}
	// export without a file: the journal store, which fails without journalctl
	if _, errOut, code := run("--config", cfgJournal, "--export", "-"); code != exitFail || !strings.Contains(errOut, "log: export:") {
		t.Errorf("log --export (journal, no journalctl): %d %q", code, errOut)
	}
}

// TestCLIDispatch: run() routes to the registered commands and reports
// unknown ones with usage; every command in `order` is registered and has
// a help line.
func TestCLIDispatch(t *testing.T) {
	if _, errOut, code := captureOutput(t, func() int { return run(nil) }); code != exitUsage || !strings.Contains(errOut, "usage: n5-fangov <subcommand>") {
		t.Errorf("no args: %d %q", code, errOut)
	}
	if _, errOut, code := captureOutput(t, func() int { return run([]string{"bogus"}) }); code != exitUsage || !strings.Contains(errOut, `unknown subcommand "bogus"`) {
		t.Errorf("unknown: %d %q", code, errOut)
	}
	for _, a := range []string{"-h", "--help", "help"} {
		if out, _, code := captureOutput(t, func() int { return run([]string{a}) }); code != exitOK || !strings.Contains(out, "usage: n5-fangov <subcommand>") || !strings.Contains(out, "N5FANGOV_RUN_DIR") {
			t.Errorf("%s: %d %q", a, code, out)
		}
	}
	for _, a := range []string{"version", "-v", "--version"} {
		if out, _, code := captureOutput(t, func() int { return run([]string{a}) }); code != exitOK || !strings.HasPrefix(out, "n5-fangov ") {
			t.Errorf("%s: %d %q", a, code, out)
		}
	}
	for _, n := range order {
		if _, ok := commands[n]; !ok {
			t.Errorf("%s listed in order but not registered", n)
		}
		if helpText[n] == "" {
			t.Errorf("%s has no help line", n)
		}
	}
	out, _, _ := captureOutput(t, func() int { return run([]string{"help"}) })
	for _, n := range order {
		if !strings.Contains(out, "  "+n+" ") {
			t.Errorf("usage lacks %q", n)
		}
	}
	// a usage error of a subcommand is passed through (exit 2)
	if _, _, code := captureOutput(t, func() int { return run([]string{"set"}) }); code != exitUsage {
		t.Errorf("set without args via run: %d", code)
	}
}
