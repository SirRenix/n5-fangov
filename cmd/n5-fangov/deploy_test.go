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
	for _, f := range []string{"postinst", "prerm", "postrm"} {
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
		`cmp -s "/etc/systemd/system/$u" "/lib/systemd/system/$u"`,
		`rm -f "/etc/systemd/system/$u"`,
		`systemctl reenable n5-fangov.service`,
		`differs from the packaged unit`,
	} {
		if !strings.Contains(post, want) {
			t.Errorf("postinst: missing %q", want)
		}
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
