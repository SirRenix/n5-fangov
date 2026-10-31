package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/config"
)

// The deploy/ tree was only eyeballed so far (AUDIT 7): the unit file's
// watchdog and sandbox lines are checked against the constants the daemon
// runs with, the PVE template copies against the embedded ones, and the
// shell scripts against bash -n.

var deployDir = filepath.Join("..", "..", "deploy")

// unitDirectives parses a systemd unit into section → key → values (a key
// may repeat).
func unitDirectives(t *testing.T, path string) map[string]map[string][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]map[string][]string{}
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			if out[section] == nil {
				out[section] = map[string][]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			t.Fatalf("%s: unparsable line %q", path, line)
		}
		out[section][strings.TrimSpace(k)] = append(out[section][strings.TrimSpace(k)], strings.TrimSpace(v))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDeployUnitFile(t *testing.T) {
	u := unitDirectives(t, filepath.Join(deployDir, "n5-fangov.service"))
	svc := u["Service"]
	if svc == nil {
		t.Fatal("no [Service] section")
	}
	one := func(key string) string {
		t.Helper()
		v := svc[key]
		if len(v) != 1 {
			t.Fatalf("%s: %d values, want exactly one: %v", key, len(v), v)
		}
		return v[0]
	}

	// WatchdogSec must hold two cycles at the largest interval the config
	// parser lets through (the daemon pings once per cycle).
	wd, err := strconv.Atoi(one("WatchdogSec"))
	if err != nil {
		t.Fatal(err)
	}
	if watchdog := time.Duration(wd) * time.Second; watchdog < 2*config.MaxInterval {
		t.Errorf("WatchdogSec=%d < 2 × MaxInterval %s", wd, config.MaxInterval)
	}
	if wd != 60 {
		t.Errorf("WatchdogSec=%d; the MaxInterval comment in config.go says 60", wd)
	}
	if one("Type") != "notify" {
		t.Errorf("Type=%s, want notify (READY=1 / WATCHDOG=1)", one("Type"))
	}

	// every directory the daemon writes must be writable under
	// ProtectSystem=strict ("-" prefix = optional)
	rw := map[string]bool{}
	for _, line := range svc["ReadWritePaths"] {
		for _, p := range strings.Fields(line) {
			rw[strings.TrimPrefix(p, "-")] = true
		}
	}
	// config + presets + tls; socket, state.json, override.*; [log].file;
	// sessions.json, alerts.json; pwmN via the class symlink and via the
	// resolved platform device path; PVE notification templates
	for _, want := range []string{
		path.Dir(config.DefaultPath),
		defaultRunDir,
		path.Dir(config.DefaultLogFile),
		defaultStateDir,
		"/sys/class/hwmon",
		"/sys/devices/platform",
		path.Dir(alert.TemplatePath),
	} {
		if !rw[want] {
			t.Errorf("ReadWritePaths lacks %s (have %v)", want, svc["ReadWritePaths"])
		}
	}
	if !strings.HasPrefix(config.DefaultPresetDir, path.Dir(config.DefaultPath)+"/") {
		t.Errorf("preset dir %s is not under the config dir", config.DefaultPresetDir)
	}
	if strings.TrimPrefix(config.LogRoot, "/") != "var/log/" || !strings.HasPrefix(config.DefaultLogFile, config.LogRoot) {
		t.Errorf("DefaultLogFile %s not under LogRoot %s", config.DefaultLogFile, config.LogRoot)
	}
	// the systemd-managed directories match the defaults the code uses
	for key, want := range map[string]string{
		"RuntimeDirectory": path.Base(defaultRunDir),
		"StateDirectory":   path.Base(defaultStateDir),
		"LogsDirectory":    path.Base(path.Dir(config.DefaultLogFile)),
	} {
		if got := one(key); got != want {
			t.Errorf("%s=%s, want %s", key, got, want)
		}
	}
	if one("RuntimeDirectoryMode") != "0750" || one("StateDirectoryMode") != "0700" {
		t.Errorf("directory modes: run %s state %s", one("RuntimeDirectoryMode"), one("StateDirectoryMode"))
	}
	// sandbox: no capabilities, syscall filter, sysfs writable, no
	// privilege escalation
	if v := one("CapabilityBoundingSet"); v != "" {
		t.Errorf("CapabilityBoundingSet=%q, want empty (no capability)", v)
	}
	if v := one("SystemCallFilter"); v == "" {
		t.Error("SystemCallFilter not set")
	}
	if one("ProtectKernelTunables") != "no" {
		t.Error("ProtectKernelTunables must stay \"no\" (pwm writes under /sys)")
	}
	if one("ProtectSystem") != "strict" || one("NoNewPrivileges") != "yes" || one("PrivateDevices") != "yes" {
		t.Errorf("sandbox: ProtectSystem=%s NoNewPrivileges=%s PrivateDevices=%s", one("ProtectSystem"), one("NoNewPrivileges"), one("PrivateDevices"))
	}
	if fam := one("RestrictAddressFamilies"); !strings.Contains(fam, "AF_UNIX") || !strings.Contains(fam, "AF_INET") || !strings.Contains(fam, "AF_NETLINK") {
		t.Errorf("RestrictAddressFamilies=%s", fam)
	}
	// the Exec lines name registered subcommands
	for key, want := range map[string]string{
		"ExecStartPre": "check --quiet",
		"ExecStart":    "serve",
		"ExecStopPost": "failsafe",
	} {
		v := one(key)
		if !strings.HasPrefix(v, "/usr/bin/n5-fangov ") || strings.TrimPrefix(v, "/usr/bin/n5-fangov ") != want {
			t.Errorf("%s=%q, want /usr/bin/n5-fangov %s", key, v, want)
		}
		sub := strings.Fields(want)[0]
		if _, ok := commands[sub]; !ok {
			t.Errorf("%s uses unregistered subcommand %q", key, sub)
		}
	}
	// restart policy: the failsafe runs after every end, systemd restarts
	if one("Restart") != "always" || one("KillSignal") != "SIGTERM" {
		t.Errorf("Restart=%s KillSignal=%s", one("Restart"), one("KillSignal"))
	}
	unit := u["Unit"]
	if len(unit["OnFailure"]) != 1 || unit["OnFailure"][0] != "n5-fangov-onfailure.service" {
		t.Errorf("OnFailure=%v", unit["OnFailure"])
	}
	if _, err := os.Stat(filepath.Join(deployDir, "n5-fangov-onfailure.service")); err != nil {
		t.Errorf("OnFailure unit missing: %v", err)
	}
	if len(unit["Conflicts"]) != 1 || unit["Conflicts"][0] != "n5-fand.service" {
		t.Errorf("Conflicts=%v (n5-fand must never regulate alongside)", unit["Conflicts"])
	}
}

// TestDeployTemplatesMatchEmbedded: deploy/pve-notification/*.hbs (what
// install.sh copies) are byte-identical to the templates embedded in the
// binary (what `alerts template` writes).
func TestDeployTemplatesMatchEmbedded(t *testing.T) {
	embedded := alert.TemplateFiles()
	if len(embedded) != 2 {
		t.Fatalf("embedded templates: %d, want subject + body", len(embedded))
	}
	dir := filepath.Join(deployDir, "pve-notification")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".hbs") {
			continue
		}
		seen[e.Name()] = true
		want, ok := embedded[e.Name()]
		if !ok {
			t.Errorf("%s: in deploy/ but not embedded", e.Name())
			continue
		}
		got, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: deploy copy differs from the embedded template", e.Name())
		}
	}
	for name := range embedded {
		if !seen[name] {
			t.Errorf("%s: embedded but missing in deploy/pve-notification", name)
		}
	}
}

// TestDeployShellSyntax runs bash -n over the shell scripts when bash is
// available (the Alpine builder has none; the bookworm race image does).
func TestDeployShellSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed: shell syntax check skipped (runs in the bookworm image)")
	}
	for _, f := range []string{"install.sh", "uninstall.sh", "n5-fangov-onfailure", "emergency.example.sh"} {
		path := filepath.Join(deployDir, f)
		head, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(head, []byte("#!/")) {
			t.Errorf("%s: no shebang", f)
		}
		out, err := exec.Command(bash, "-n", path).CombinedOutput()
		if err != nil {
			t.Errorf("bash -n %s: %v\n%s", f, err, out)
		}
	}
	// the Debian maintainer scripts are POSIX sh; bash -n in posix mode
	for _, f := range []string{"preinst", "postinst", "prerm", "postrm"} {
		path := filepath.Join(deployDir, "debian", f)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if out, err := exec.Command(bash, "--posix", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("bash --posix -n debian/%s: %v\n%s", f, err, out)
		}
	}
}

// TestDeployPackagePinsGateFindings pins the release-gate contract of DESIGN
// §12 (2026-09-18): the postinst removes install.sh's identical unit copies
// under /etc/systemd/system (and re-links the enable symlink), the package
// carries md5sums, and the release workflow packages the built binary
// without rebuilding it (deb-only) and asserts it equals the uploaded asset.
func TestDeployPackagePinsGateFindings(t *testing.T) {
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(deployDir, "..", rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	post := read("deploy/debian/postinst")
	for _, want := range []string{
		`for u in n5-fangov.service n5-fangov-onfailure.service; do`,
		`cmp -s "$R/etc/systemd/system/$u" "$R/lib/systemd/system/$u"`,
		`rm -f "$R/etc/systemd/system/$u"`,
		`systemctl reenable n5-fangov.service`,
		`differs from the packaged unit`,
		// maintenance window 1 (C1): an upgrade keeps a disabled unit
		// disabled; a fresh install, also after `apt remove` (preinst
		// marker, BACKLOG #40), enables it
		`if [ -z "$2" ] || [ -f "$FRESH" ]; then`,
		`if [ "$fresh" = 0 ] || [ -f "$R/etc/systemd/system/n5-fangov.service" ]; then`,
		`if [ "$had_unit" = 0 ]; then`,
		`stays disabled as before`,
	} {
		if !strings.Contains(post, want) {
			t.Errorf("postinst: missing %q", want)
		}
	}
	// the enable state is read before the installer copy is removed, and
	// enable is never unconditional any more
	if i, j := strings.Index(post, "was_enabled=1"), strings.Index(post, `rm -f "$R/etc/systemd/system/$u"`); i < 0 || j < 0 || i > j {
		t.Error("postinst: enable state must be read before the installer unit copy is removed")
	}
	if strings.Count(post, "systemctl enable n5-fangov.service") != 1 {
		t.Error("postinst: exactly one (fresh-install) systemctl enable expected")
	}
	mk := read("Makefile")
	for _, want := range []string{"deb: build\n\t$(MAKE) deb-only", "deb-only:", "DEBIAN/md5sums", "test -s DEBIAN/md5sums"} {
		if !strings.Contains(mk, want) {
			t.Errorf("Makefile: missing %q", want)
		}
	}
	wf := read(".github/workflows/release.yml")
	for _, want := range []string{`make deb-only VERSION="$VER"`, `differs from the release asset`, `md5sums missing`, `version literal carries a prefix`} {
		if !strings.Contains(wf, want) {
			t.Errorf("release.yml: missing %q", want)
		}
	}
	if strings.Contains(wf, `make deb VERSION="$VERSION"`) {
		t.Error("release.yml still rebuilds the binary for the .deb with the tag as version")
	}
}

// TestDeployTokensBackupOnRemoval pins the 0.4.1 hardening: postrm (remove
// and purge -- apt purge runs both, the state directory goes with remove)
// and uninstall.sh back up tokens.json to /var/backups/n5-fangov before
// the state directory is removed, and both installers ship the emergency
// example script under /usr/share/doc/n5-fangov/examples/.
func TestDeployTokensBackupOnRemoval(t *testing.T) {
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(deployDir, "..", rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	postrm := read("deploy/debian/postrm")
	for _, want := range []string{"backup_tokens()", "/var/backups/n5-fangov/tokens.json.$(date", "chmod 0600", "mkdir -p -m 0700 /var/backups/n5-fangov 2>/dev/null || true"} {
		if !strings.Contains(postrm, want) {
			t.Errorf("postrm lacks %q", want)
		}
	}
	// the backup call precedes the rm -rf of the state directory in both branches
	for _, branch := range []string{"remove)", "purge)"} {
		i := strings.Index(postrm, branch)
		if i < 0 {
			t.Fatalf("postrm: no %s branch", branch)
		}
		rest := postrm[i:]
		b, r := strings.Index(rest, "backup_tokens\n"), strings.Index(rest, "/var/lib/n5-fangov\n")
		if b < 0 || r < 0 || b > r {
			t.Errorf("postrm %s: backup_tokens at %d, rm of the state dir at %d", branch, b, r)
		}
	}
	un := read("deploy/uninstall.sh")
	b, r := strings.Index(un, "/var/backups/n5-fangov/tokens.json.$(date"), strings.Index(un, "rm -rf /var/lib/n5-fangov")
	if b < 0 || r < 0 || b > r || !strings.Contains(un, "chmod 0600") {
		t.Errorf("uninstall.sh: backup at %d, rm of the state dir at %d", b, r)
	}
	for _, f := range []string{"deploy/install.sh", "Makefile"} {
		if !strings.Contains(read(f), "emergency.example.sh") || !strings.Contains(read(f), "/usr/share/doc/n5-fangov/examples/emergency.example.sh") {
			t.Errorf("%s does not ship the emergency example under examples/", f)
		}
	}
	// the example names the fixed hook path and the install line, sets
	// -eu and lets the last command's status through (no trailing exit 0)
	ex := read("deploy/emergency.example.sh")
	for _, want := range []string{"N5_CHANNEL", "N5_SENSOR", "N5_PART", "N5_TEMP", "N5_CEILING", "N5_RPM", "N5_CYCLES", "logger -t n5-fangov-emergency", "# systemctl poweroff",
		"/etc/n5-fangov/emergency.sh", "install -m 0750 -o root -g root", "emergency = true", "\nset -eu\n"} {
		if !strings.Contains(ex, want) {
			t.Errorf("emergency.example.sh lacks %q", want)
		}
	}
	if strings.Contains(ex, "\nexit 0") || strings.Contains(ex, "emergency_command") {
		t.Errorf("emergency.example.sh masks the exit status or names the removed key")
	}
}

// TestMaintainerScriptsEnableState runs preinst, postinst and prerm against a
// temporary root (DPKG_ROOT) and a stub systemctl (BACKLOG #40, operator
// 2026-10-17): a first install enables the unit, an upgrade keeps a
// disabled unit disabled and an enabled one enabled, and a new install
// after `apt remove` enables it again like a first install.
func TestMaintainerScriptsEnableState(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not installed (runs in the bookworm image)")
	}
	deb, err := filepath.Abs(filepath.Join(deployDir, "debian"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	stub := filepath.Join(dir, "bin")
	enabled := filepath.Join(dir, "enabled")
	logf := filepath.Join(dir, "calls")
	for _, d := range []string{root, stub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	systemctl := "#!/bin/sh\necho \"$*\" >> " + logf + "\ncase \"$1\" in\n" +
		"is-enabled) [ -f " + enabled + " ] && exit 0; exit 1;;\n" +
		"is-active) exit 3;;\n" +
		"enable|reenable) : > " + enabled + ";;\n" +
		"disable) rm -f " + enabled + ";;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(stub, "systemctl"), []byte(systemctl), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(script string, args ...string) string {
		t.Helper()
		cmd := exec.Command(sh, append([]string{filepath.Join(deb, script)}, args...)...)
		cmd.Env = append(os.Environ(), "DPKG_ROOT="+root, "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", script, args, err, out)
		}
		return string(out)
	}
	isEnabled := func() bool { _, err := os.Stat(enabled); return err == nil }
	enables := func() int {
		b, _ := os.ReadFile(logf)
		n := 0
		for _, l := range strings.Split(string(b), "\n") {
			if l == "enable n5-fangov.service" {
				n++
			}
		}
		return n
	}
	fresh := filepath.Join(root, "var", "lib", "n5-fangov", ".fresh-install")

	// first install: enabled
	run("preinst", "install")
	run("postinst", "configure", "")
	if !isEnabled() || enables() != 1 {
		t.Fatalf("first install: enabled %v, enable calls %d", isEnabled(), enables())
	}
	// the operator disables it for a maintenance window; an upgrade keeps that
	if err := os.Remove(enabled); err != nil {
		t.Fatal(err)
	}
	run("preinst", "upgrade", "0.5.0~rc3")
	out := run("postinst", "configure", "0.5.0~rc3")
	if isEnabled() || enables() != 1 || !strings.Contains(out, "stays disabled as before") {
		t.Fatalf("upgrade of a disabled unit: enabled %v, enable calls %d\n%s", isEnabled(), enables(), out)
	}
	// apt remove (prerm disables, postrm removes the state dir), then a new
	// install: enabled again, the marker is gone
	run("prerm", "remove")
	if err := os.RemoveAll(filepath.Join(root, "var", "lib", "n5-fangov")); err != nil {
		t.Fatal(err)
	}
	run("preinst", "install", "0.5.0~rc3")
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("preinst install left no marker: %v", err)
	}
	run("postinst", "configure", "0.5.0~rc3")
	if !isEnabled() || enables() != 2 {
		t.Fatalf("install after remove: enabled %v, enable calls %d", isEnabled(), enables())
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatalf("marker left after configure: %v", err)
	}
	// upgrade of an enabled unit: stays enabled, no further enable
	run("preinst", "upgrade", "0.5.0~rc3")
	run("postinst", "configure", "0.5.0~rc3")
	if !isEnabled() || enables() != 2 {
		t.Fatalf("upgrade of an enabled unit: enabled %v, enable calls %d", isEnabled(), enables())
	}
}

// TestOnfailureCause runs n5-fangov-onfailure against a stub systemctl
// (maintenance window 1, C4): a failed ExecStartPre (check, exit 1) with
// Result=exit-code and ExecMainStatus=0 is named as such, not as "process
// exited with code 0"; a main process exit and a signal keep their texts.
func TestOnfailureCause(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed (runs in the bookworm image)")
	}
	script, err := filepath.Abs(filepath.Join(deployDir, "n5-fangov-onfailure"))
	if err != nil {
		t.Fatal(err)
	}
	const okPre = `{ path=/usr/bin/n5-fangov ; argv[]=/usr/bin/n5-fangov check --quiet ; ignore_errors=no ; start_time=[Sat 2026-10-17 12:32:40 CEST] ; stop_time=[Sat 2026-10-17 12:32:40 CEST] ; pid=18500 ; code=exited ; status=0 }`
	const badPre = `{ path=/usr/bin/n5-fangov ; argv[]=/usr/bin/n5-fangov check --quiet ; ignore_errors=no ; start_time=[Sat 2026-10-17 12:32:40 CEST] ; stop_time=[Sat 2026-10-17 12:32:40 CEST] ; pid=18500 ; code=exited ; status=1 }`
	const okPost = `{ path=/usr/bin/n5-fangov ; argv[]=/usr/bin/n5-fangov failsafe ; ignore_errors=no ; start_time=[Sat 2026-10-17 12:32:41 CEST] ; stop_time=[Sat 2026-10-17 12:32:41 CEST] ; pid=18501 ; code=exited ; status=0 }`
	for _, tc := range []struct {
		name                   string
		result, mcode, mstatus string
		pre                    string
		want                   string
	}{
		{"check failed", "exit-code", "0", "0", badPre, "[failed] start check failed: ExecStartPre /usr/bin/n5-fangov check --quiet (exited with code 1); the daemon was not started"},
		{"check killed", "exit-code", "0", "0", strings.Replace(badPre, "code=exited ; status=1", "code=killed ; status=9/KILL", 1), "[failed] start check failed: ExecStartPre /usr/bin/n5-fangov check --quiet (killed by signal 9/KILL)"},
		{"main exit", "exit-code", "1", "3", okPre, "[failed] process exited with code 3"},
		{"signal", "signal", "2", "9", okPre, "[failed] process ended by signal (status 9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := filepath.Join(dir, "bin")
			if err := os.Mkdir(stub, 0o755); err != nil {
				t.Fatal(err)
			}
			logf := filepath.Join(dir, "log")
			props := map[string]string{"Result": tc.result, "ExecMainCode": tc.mcode, "ExecMainStatus": tc.mstatus, "ExecStartPre": tc.pre, "ExecStopPost": okPost, "NRestarts": "5"}
			var sc strings.Builder
			sc.WriteString("#!/bin/sh\ncase \"$1\" in\nis-active) echo failed; exit 3;;\nshow)\n  case \"$3\" in\n")
			for k, v := range props {
				sc.WriteString("  " + k + ") cat <<'EOV'\n" + v + "\nEOV\n;;\n")
			}
			sc.WriteString("  esac;;\nesac\nexit 0\n")
			files := map[string]string{
				"systemctl": sc.String(),
				"sleep":     "#!/bin/sh\nexit 0\n",
				"dkms":      "#!/bin/sh\nexit 0\n",
				"logger":    "#!/bin/sh\necho \"$*\" >> " + logf + "\n",
				"timeout":   "#!/bin/sh\necho \"alert $*\" >> " + logf + "\n",
			}
			for n, body := range files {
				if err := os.WriteFile(filepath.Join(stub, n), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(bash, script)
			cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "N5FANGOV_RUN_DIR="+filepath.Join(dir, "run"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("onfailure: %v\n%s", err, out)
			}
			got, _ := os.ReadFile(logf)
			if !strings.Contains(string(got), tc.want) {
				t.Fatalf("want %q in\n%s", tc.want, got)
			}
			if strings.Contains(string(got), "code 0") {
				t.Fatalf("cause names code 0:\n%s", got)
			}
		})
	}
}
