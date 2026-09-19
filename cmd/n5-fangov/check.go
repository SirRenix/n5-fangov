package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/hwmon"
)

func init() {
	register("check", command{run: cmdCheck})
}

// emergencyHookPath is the fixed path of the emergency hook `check`
// reports on; a test overrides it.
var emergencyHookPath = control.DefaultEmergencyHook

// checkResult is one line of the self-check report.
type checkResult struct {
	ok       bool
	advisory bool // a failed advisory check does not change the exit code
	name     string
	detail   string
}

// cmdCheck is the self-check used interactively and as ExecStartPre.
// Exit 1 when any non-advisory check fails. --quiet prints only failures.
//
// Fatal (DESIGN rule 8: only what stops serve itself) are an unreadable
// config file, a profile/device that is not detected and a pwm the daemon
// would write but cannot open. Everything else is advisory: a missing or
// syntactically broken config (serve runs on the built-in defaults),
// invalid values, a pwm the profile does not expose (serve ignores that
// channel), an unresolvable sensor (serve isolates that channel at its
// safe duty), and a network-reachable web listener without auth.
func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	quiet := fs.Bool("quiet", false, "print only failures")
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	afterUpdate := fs.Bool("after-update", false, "kernel gate for the apt hook: DKMS module present for every installed kernel (n5pro)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *afterUpdate {
		return cmdCheckAfterUpdate(*cfgPath, runDir())
	}
	results := runChecks(*cfgPath, runDir())
	rc := exitOK
	for _, r := range results {
		mark := "ok  "
		switch {
		case r.ok:
		case r.advisory:
			mark = "warn"
		default:
			mark = "FAIL"
			rc = exitFail
		}
		if *quiet && r.ok {
			continue
		}
		fmt.Printf("[%s] %-22s %s\n", mark, r.name, r.detail)
	}
	if !*quiet {
		if rc == exitOK {
			fmt.Println("check: all good")
		} else {
			fmt.Println("check: FAILED")
		}
	}
	return rc
}

func runChecks(cfgPath, dir string) []checkResult {
	var res []checkResult
	add := func(ok bool, name, detail string) { res = append(res, checkResult{ok: ok, name: name, detail: detail}) }
	adv := func(ok bool, name, detail string) {
		res = append(res, checkResult{ok: ok, advisory: true, name: name, detail: detail})
	}

	// 1. config: only an unreadable file is fatal. Missing file, invalid
	// values and a TOML syntax error are what serve survives on defaults.
	cfg, warns, err := loadConfig(cfgPath)
	switch {
	case err != nil && configUnreadable(err):
		add(false, "config", cfgPath+": "+err.Error())
	case err != nil:
		adv(false, "config", cfgPath+": syntax error, serve starts with the built-in defaults: "+err.Error())
	case len(warns) > 0:
		adv(false, "config", fmt.Sprintf("%s: %d warning(s), defaults substituted:", cfgPath, len(warns)))
		for _, w := range warns {
			adv(false, "  ", w)
		}
	default:
		add(true, "config", cfgPath+" parses without warnings")
	}
	dspec := daemonOf(cfg)
	wspec := webOf(cfg)

	// 2. profile
	hw := hwmon.New()
	dev, err := detectDevice(hw, dspec.Profile)
	if err != nil {
		add(false, "profile", fmt.Sprintf("%q not detected: %v", dspec.Profile, err))
		return res
	}
	p := dev.Profile()
	add(true, "profile", fmt.Sprintf("%s (%s) at %s", p.Name(), p.Title(), dev.HwmonPath()))
	if !p.Verified() {
		adv(false, "profile verified", p.Name()+" is documentation-only, not hardware-verified")
	}

	// 3. channels: the set serve will actually manage (the same
	// corrections as control.New: on the N5 Pro pwm1..3 are always
	// present, pwm3 never "auto"). pwm files must open for writing (no
	// write happens) -- fatal, serve would fail every write. A pwm the
	// profile lacks is ignored by serve: advisory.
	configured := map[string]bool{}
	for _, c := range channelSpecs(cfg) {
		configured[c.Name] = true
	}
	chans, notes := sanitizeChannelSpecs(p.Name(), channelSpecs(cfg))
	for _, n := range notes {
		adv(false, "channels", n)
	}
	if len(chans) == 0 {
		adv(false, "channels", "no [[channel]] configured; daemon will only monitor")
	}
	for _, c := range chans {
		if !hasChannel(dev, c.PWM) {
			adv(false, "channel "+c.Name, fmt.Sprintf("pwm%d not exposed by profile %s; serve ignores the channel", c.PWM, p.Name()))
			continue
		}
		problems := []string{}
		for _, f := range []string{fmt.Sprintf("pwm%d", c.PWM), fmt.Sprintf("pwm%d_enable", c.PWM)} {
			path := filepath.Join(dev.HwmonPath(), f)
			if err := writable(path); err != nil {
				problems = append(problems, err.Error())
			}
		}
		if len(problems) > 0 {
			add(false, "channel "+c.Name, strings.Join(problems, "; "))
			continue
		}
		rpm := "no tach"
		if r, err := dev.ReadRPM(c.PWM); err == nil {
			rpm = fmt.Sprintf("%d rpm", r)
		}
		add(true, "channel "+c.Name, fmt.Sprintf("pwm%d writable, %s, sensor %s", c.PWM, rpm, c.Sensor))
	}
	// 3b. critical above the ceiling: accepted, but the ceiling acts first
	// (DESIGN "Ceilings and emergency") -- advisory, check passes. The
	// effective ceiling counts: a configured one that sits below critical
	// is the same finding as a built-in one.
	for _, c := range chans {
		built := builtinCeiling(hw, c.Sensor)
		if ceil := effectiveCeiling(hw, c.Sensor, c.Ceiling); c.Critical > ceil {
			which := "built-in"
			if ceil < built {
				which = "configured"
			}
			adv(false, "channel "+c.Name, fmt.Sprintf("critical %d above the %s ceiling %d — the ceiling acts first", c.Critical, which, ceil))
		}
	}
	// 3c. emergency hook: the fixed path's state (DESIGN "Ceilings and
	// emergency"); a missing or refused hook is a warning only when
	// [daemon] emergency = true, otherwise an information line.
	if st := control.HookStatus(emergencyHookPath); st.OK || !dspec.Emergency {
		add(true, "emergency hook", emergencyHookPath+" ("+st.State+")")
	} else {
		adv(false, "emergency hook", emergencyHookPath+" ("+st.State+"); emergency = true but nothing would run")
	}

	// 4. sensors resolve and read plausibly. Advisory: serve holds a
	// channel whose sensor fails at its safe duty and regulates the rest.
	factory := newSensorFactory(hw, dev)
	for _, c := range chans {
		origin := ""
		if !configured[c.Name] {
			origin = " (built-in channel, not in the config)"
		}
		src, err := factory(c.Sensor)
		if err != nil {
			adv(false, "sensor "+c.Sensor, fmt.Sprintf("%v; channel %q%s stays at duty %s in mode sensor-error, the other channels regulate",
				err, c.Name, origin, safeDutyOf(c)))
			continue
		}
		t, err := readTempC(src)
		if err != nil {
			adv(false, "sensor "+c.Sensor, fmt.Sprintf("read: %v; channel %q%s stays at duty %s in mode sensor-error, the other channels regulate",
				err, c.Name, origin, safeDutyOf(c)))
			continue
		}
		add(true, "sensor "+c.Sensor, fmt.Sprintf("%.1f C", t))
	}

	// 4b. web listener: reachable from the network without auth is not a
	// start problem, but worth a line every time check runs.
	switch {
	case wspec.Listen == "":
		add(true, "web", "no TCP listener")
	case wspec.Auth == "none" && !isLoopbackListen(wspec.Listen):
		adv(false, "web", fmt.Sprintf("listen %s is reachable from the network with auth = \"none\"; anyone on the network can change fan duties. Set [web].auth = \"basic\" or bind to 127.0.0.1", wspec.Listen))
	default:
		add(true, "web", fmt.Sprintf("listen %s, auth %s, tls %s", wspec.Listen, wspec.Auth, wspec.TLS))
	}
	// Legacy password hash (unsalted sha256): still accepted, re-hashed by
	// the daemon after the next successful sign-in; `passwd` does it now.
	if wspec.Auth == "basic" && isLegacyHash(wspec.PasswordHash) {
		adv(false, "web auth", "legacy password hash (unsalted sha256), run: n5-fangov passwd")
	}
	// tls = "file" with a missing file: serve disables the web listener
	// (no plain-HTTP fallback) and keeps regulating — advisory.
	if wspec.Listen != "" && wspec.TLS == "file" {
		for _, f := range []string{wspec.CertFile, wspec.KeyFile} {
			if !isFile(f) {
				adv(false, "web tls", f+": not a file; serve disables the web UI (the CLI socket keeps working)")
			}
		}
	}
	// 4c. log file (advisory: serve creates the directory itself and falls
	// back to the journal when that fails).
	if lf := logOf(cfg).File; lf != "" {
		if isDir(filepath.Dir(lf)) {
			add(true, "log", lf)
		} else {
			adv(false, "log", filepath.Dir(lf)+" does not exist yet; serve creates it (the unit's LogsDirectory= does too)")
		}
	} else {
		add(true, "log", "journal only ([log].file empty)")
	}
	// 4d. alert transport (advisory): "pve" without the Proxmox stack, or
	// "mail" without mail(1), degrades to the next transport; say so.
	aspec := alertOf(cfg)
	pve, mail := alertToolsAvailable()
	switch {
	case aspec.Transport == "pve" && !pve:
		adv(false, "alerts", fmt.Sprintf("[alert].transport = \"pve\" but PVE::Notify (or perl) is absent; alerts go to %s instead", alertEffective(aspec)))
	case aspec.Transport == "mail" && !mail:
		adv(false, "alerts", fmt.Sprintf("[alert].transport = \"mail\" but mail(1) is absent; alerts go to %s instead", alertEffective(aspec)))
	case aspec.Transport == "off":
		adv(false, "alerts", "[alert].transport = \"off\": alerts are only written to the journal")
	case aspec.Transport == "webhook":
		add(true, "alerts", fmt.Sprintf("transport webhook (%s, %s)", alert.RedactURL(aspec.WebhookURL), aspec.WebhookFormat))
	default:
		add(true, "alerts", fmt.Sprintf("transport %s (%s)", aspec.Transport, alertEffective(aspec)))
	}

	// 5. socket answers when the unit is active (skipped during ExecStartPre,
	// where the unit is "activating")
	if unitActive(unitName) {
		if daemonRunning(dir) {
			add(true, "socket", socketPath(dir)+" answers")
		} else {
			add(false, "socket", socketPath(dir)+" does not answer although the unit is active")
		}
	} else {
		adv(true, "socket", "unit not active, socket check skipped")
	}

	// 6. dkms (advisory, n5pro only): module survives a kernel update only if
	// DKMS built it for the running kernel.
	if p.Name() == "n5pro" {
		ok, detail := dkmsInstalled("minisforum-n5-it5571")
		adv(ok, "dkms", detail)
	}
	return res
}

// writable opens path for writing without writing anything.
func writable(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

// dkmsInstalled reports whether `dkms status PKG` lists the running kernel as installed.
func dkmsInstalled(pkg string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	kernel, _ := exec.CommandContext(ctx, "uname", "-r").Output()
	k := strings.TrimSpace(string(kernel))
	out, err := exec.CommandContext(ctx, "dkms", "status", pkg).Output()
	if err != nil {
		return false, "dkms status " + pkg + ": " + err.Error() + " (module may not be rebuilt after a kernel update)"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, k) && strings.Contains(line, "installed") {
			return true, strings.TrimSpace(line)
		}
	}
	return false, pkg + " not installed for kernel " + k + " (run: dkms status " + pkg + ")"
}

// ---------------------------------------------------------------------------
// check --after-update (apt hook, kernel gate)

// DKMS package and kernel object of the N5 Pro EC driver.
const (
	dkmsPackage        = "minisforum-n5-it5571"
	dkmsKernelObject   = "minisforum_n5_it5571.ko"
	dkmsFallbackVer    = "0.2.0"
	defaultModulesRoot = "/lib/modules"
	defaultDKMSSrcRoot = "/usr/src"
)

// cmdCheckAfterUpdate is run by /etc/apt/apt.conf.d/90n5-fangov after every
// dpkg run. On a machine that uses the n5pro profile every kernel the box
// can boot into must carry the DKMS module, otherwise the next reboot
// starts without the EC driver: ExecStartPre fails, the fans stay in BIOS
// control and the onfailure alert fires — this check says it earlier,
// while the old kernel still runs.
//
// "Can boot into" is the running kernel plus what
// `proxmox-boot-tool kernel list` selects (manually, automatically,
// pinned); without the tool: the running kernel and the newest installed
// one. Older kernels that are merely still installed (apt keeps two) get
// an info line only — a module missing there is not worth a notification.
// Exit 1 and a "kernel" alert (cooldown) when a relevant kernel lacks the
// module; other profiles: ok, exit 0.
func cmdCheckAfterUpdate(cfgPath, dir string) int {
	cfg, _, _ := loadConfig(cfgPath)
	profileName := daemonOf(cfg).Profile
	detected := ""
	if dev, err := detectDevice(hwmon.New(), profileName); err == nil {
		detected = dev.Profile().Name()
	}
	if !wantsN5Pro(profileName, detected, dkmsSourceVersion(defaultDKMSSrcRoot) != "") {
		fmt.Println("n5-fangov: kernel gate not applicable (profile is not n5pro)")
		return exitOK
	}
	kernels, missing, err := scanKernelModules(defaultModulesRoot, dkmsKernelObject)
	if err != nil {
		fmt.Printf("n5-fangov: kernel gate: %v\n", err)
		return exitFail
	}
	if len(kernels) == 0 {
		fmt.Printf("n5-fangov: kernel gate: no kernels found under %s\n", defaultModulesRoot)
		return exitOK
	}
	ver := dkmsSourceVersion(defaultDKMSSrcRoot)
	if ver == "" {
		ver = dkmsFallbackVer
	}
	toolOut, toolErr := bootToolKernelList()
	relevant, source := relevantKernels(runningKernel(), kernels, toolOut, toolErr)
	if len(missing) == 0 {
		fmt.Printf("n5-fangov: fan driver module present for %d kernel(s): %s\n", len(kernels), strings.Join(kernels, " "))
		return exitOK
	}
	var alertLines, infoLines []string
	for _, k := range missing {
		if relevant[k] {
			alertLines = append(alertLines, kernelMissingLine(k, ver))
		} else {
			infoLines = append(infoLines, kernelMissingInfo(k, ver))
		}
	}
	for _, l := range infoLines {
		fmt.Println(l)
	}
	if len(alertLines) == 0 {
		fmt.Printf("n5-fangov: fan driver module present for every bootable kernel (%s)\n", source)
		return exitOK
	}
	for _, l := range alertLines {
		fmt.Println(l)
	}
	msg := fmt.Sprintf("Fan driver module %s is missing for %d bootable kernel(s) (%s). A reboot into such a kernel leaves the fans in BIOS/EC control (n5-fangov will not start).\n%s",
		dkmsKernelObject, len(alertLines), source, strings.Join(alertLines, "\n"))
	sendAlertCooled(dir, newAlerter(), "kernel", msg)
	return exitFail
}

// kernelMissingLine is the line printed per bootable kernel without the module.
func kernelMissingLine(kernel, ver string) string {
	return fmt.Sprintf("kernel %s: fan driver module missing - run: dkms install %s/%s -k %s", kernel, dkmsPackage, ver, kernel)
}

// kernelMissingInfo is the line for an installed kernel the box does not
// boot into (no alert).
func kernelMissingInfo(kernel, ver string) string {
	return fmt.Sprintf("kernel %s: fan driver module missing (not selected for boot, info only; dkms install %s/%s -k %s if you intend to boot it)", kernel, dkmsPackage, ver, kernel)
}

// bootToolKernelList runs `proxmox-boot-tool kernel list`; a func var so
// tests inject output. The error is what exec returns when the tool is
// absent or fails.
var bootToolKernelList = func() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "proxmox-boot-tool", "kernel", "list").Output()
	return string(out), err
}

// runningKernel is `uname -r`; a func var for tests.
var runningKernel = func() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "uname", "-r").Output()
	return strings.TrimSpace(string(out))
}

// bootToolSections are the `proxmox-boot-tool kernel list` sections whose
// entries the box may boot into.
var bootToolSections = []string{"Manually selected kernels:", "Automatically selected kernels:", "Pinned kernel:"}

// parseBootToolKernels extracts the kernel versions listed under the
// bootToolSections headings ("None." entries ignored).
func parseBootToolKernels(out string) []string {
	var kernels []string
	inSection := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasSuffix(t, ":") {
			inSection = false
			for _, s := range bootToolSections {
				if t == s {
					inSection = true
				}
			}
			continue
		}
		if inSection && t != "None." && t != "None" {
			kernels = append(kernels, t)
		}
	}
	return kernels
}

// relevantKernels is the set of installed kernels the box may boot into:
// the running one plus the boot tool's selection, or — without the tool —
// the running one plus the newest installed by version. source describes
// which rule applied (for the output).
func relevantKernels(running string, installed []string, toolOut string, toolErr error) (map[string]bool, string) {
	isInstalled := map[string]bool{}
	for _, k := range installed {
		isInstalled[k] = true
	}
	rel := map[string]bool{}
	if running != "" {
		rel[running] = true
	}
	if toolErr == nil {
		n := 0
		for _, k := range parseBootToolKernels(toolOut) {
			if isInstalled[k] {
				rel[k] = true
				n++
			}
		}
		if n > 0 {
			return rel, "running kernel + proxmox-boot-tool selection"
		}
	}
	newest := ""
	for _, k := range installed {
		if newest == "" || kernelLess(newest, k) {
			newest = k
		}
	}
	if newest != "" {
		rel[newest] = true
	}
	return rel, "running kernel + newest installed"
}

// kernelLess orders kernel version strings ("6.14.8-2-pve" < "6.17.4-1-pve")
// by their numeric runs; a shorter numeric prefix sorts first.
func kernelLess(a, b string) bool {
	an, bn := numericRuns(a), numericRuns(b)
	for i := 0; i < len(an) && i < len(bn); i++ {
		if an[i] != bn[i] {
			return an[i] < bn[i]
		}
	}
	return len(an) < len(bn)
}

// numericRuns is the sequence of integers in s ("6.17.4-1-pve" → 6 17 4 1).
func numericRuns(s string) []int {
	var out []int
	cur, in := 0, false
	for _, c := range s {
		if c >= '0' && c <= '9' {
			cur = cur*10 + int(c-'0')
			in = true
			continue
		}
		if in {
			out = append(out, cur)
			cur, in = 0, false
		}
	}
	if in {
		out = append(out, cur)
	}
	return out
}

// wantsN5Pro decides whether the kernel gate applies: the config names the
// profile, or auto-detection found it, or (driver not loaded right now)
// the DKMS source tree is installed.
func wantsN5Pro(profile, detected string, dkmsSrcPresent bool) bool {
	switch profile {
	case "n5pro":
		return true
	case "auto", "":
		return detected == "n5pro" || dkmsSrcPresent
	}
	return false
}

// scanKernelModules walks root (/lib/modules): every directory that holds
// build/ or modules.dep is an installed kernel; the module must exist as
// updates/dkms/<ko> (also compressed: .xz/.zst/.gz). Returns the kernels
// and those without the module, both sorted.
func scanKernelModules(root, ko string) (kernels, missing []string, err error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		kdir := filepath.Join(root, e.Name())
		if !isDir(filepath.Join(kdir, "build")) && !isFile(filepath.Join(kdir, "modules.dep")) {
			continue
		}
		kernels = append(kernels, e.Name())
		found := false
		for _, suffix := range []string{"", ".xz", ".zst", ".gz"} {
			if isFile(filepath.Join(kdir, "updates", "dkms", ko+suffix)) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, e.Name())
		}
	}
	sort.Strings(kernels)
	sort.Strings(missing)
	return kernels, missing, nil
}

// dkmsSourceVersion returns the newest version of the DKMS source tree
// <root>/<dkmsPackage>-<version>, or "" when none is installed.
func dkmsSourceVersion(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var vers []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), dkmsPackage+"-") {
			vers = append(vers, strings.TrimPrefix(e.Name(), dkmsPackage+"-"))
		}
	}
	if len(vers) == 0 {
		return ""
	}
	sort.Slice(vers, func(i, j int) bool { return versionLess(vers[i], vers[j]) })
	return vers[len(vers)-1]
}

// versionLess compares dotted numeric versions ("0.2.0" < "0.10.0").
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
