package main

// TESTSPEC 0.4 parts of `validate` that live outside the run itself: the
// G1 measurement beyond systemd (§3, #5), G6 through the driver's ecram file
// (§3, #9), G7 (§3, #7), the third refusal (§3, #10: a run that left a
// channel unrestored), the redaction of the bundle (header, §8, #8) and the
// --restore override (§4, #2).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/hwstatus"
	"github.com/SirRenix/n5-fangov/internal/n5driver"
	"github.com/SirRenix/n5-fangov/internal/validate"
	"github.com/SirRenix/n5-fangov/internal/version"
)

// ---- G1: processes, open pwm files, pwm cache, lock inode ----------------

// knownRegulators are the pwm writers G1 looks for by comm and argv[0]
// (n5-fangov only as "serve": the CLI's other commands do not write).
var knownRegulators = []string{"n5-fand", "fancontrol"}

// g1Probe measures G1 beyond the systemd state (TESTSPEC 0.4 §3 G1, #5):
// processes of the known regulators by exact comm/argv[0], open file
// descriptors on the driver's pwm attributes, the pwm cache against what
// this run last left there, and the writer-lock inode.
type g1Probe struct {
	self      int
	hw        *hwmon.FS
	hwmonPath string            // driver hwmon dir ("" = no pwm checks)
	expect    map[string]string // pwmN / pwmN_enable -> value after the last own operation
	lockCheck func() string
}

func newG1Probe(hw *hwmon.FS, hwmonPath string, lockCheck func() string) *g1Probe {
	p := &g1Probe{self: os.Getpid(), hw: hw, hwmonPath: hwmonPath, lockCheck: lockCheck}
	p.rebase()
	return p
}

// pwmAttrs reads pwm1..4 and pwm1..4_enable.
func (p *g1Probe) pwmAttrs() map[string]string {
	out := map[string]string{}
	if p == nil || p.hwmonPath == "" {
		return out
	}
	for ch := 1; ch <= 4; ch++ {
		for _, a := range []string{fmt.Sprintf("pwm%d", ch), fmt.Sprintf("pwm%d_enable", ch)} {
			if s, err := p.hw.ReadString(filepath.Join(p.hwmonPath, a)); err == nil {
				out[a] = s
			}
		}
	}
	return out
}

// rebase takes the current pwm cache as expected; called after every own
// operation (manual, write, auto, restore).
func (p *g1Probe) rebase() {
	if p != nil {
		p.expect = p.pwmAttrs()
	}
}

// check lists every sign of another writer ("" entries never).
func (p *g1Probe) check() []string {
	if p == nil {
		return nil
	}
	var out []string
	out = append(out, p.processes()...)
	out = append(out, p.pwmFDs()...)
	now := p.pwmAttrs()
	keys := make([]string, 0, len(p.expect))
	for k := range p.expect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v, ok := now[k]; ok && v != p.expect[k] {
			out = append(out, fmt.Sprintf("%s changed from %s to %s (not written by this run)", k, p.expect[k], v))
		}
	}
	if p.lockCheck != nil {
		if why := p.lockCheck(); why != "" {
			out = append(out, why)
		}
	}
	return out
}

// procIDs lists the numeric entries of procRoot except this process.
func (p *g1Probe) procIDs() []string {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == p.self {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

// processes finds the known regulators by comm and argv[0], and a running
// `n5-fangov serve`.
func (p *g1Probe) processes() []string {
	var out []string
	for _, pid := range p.procIDs() {
		comm := readProc(pid, "comm")
		args := strings.Split(strings.TrimRight(readProcRaw(pid, "cmdline"), "\x00"), "\x00")
		argv0 := ""
		if len(args) > 0 {
			argv0 = filepath.Base(args[0])
		}
		if why := regulatorProcess(comm, argv0, args); why != "" {
			out = append(out, why+" (pid "+pid+")")
		}
	}
	return out
}

// regulatorProcess names a pwm writer by comm/argv[0], "" for none.
func regulatorProcess(comm, argv0 string, args []string) string {
	for _, n := range knownRegulators {
		if comm == n || argv0 == n {
			return "process " + n
		}
	}
	if comm == "n5-fangov" || argv0 == "n5-fangov" {
		// "serve" anywhere: flags with values (--config FILE) come first
		for _, a := range args[1:] {
			if a == "serve" {
				return "process n5-fangov serve"
			}
		}
	}
	return ""
}

// pwmFDs finds open file descriptors of other processes on the driver's
// pwm attributes.
func (p *g1Probe) pwmFDs() []string {
	if p.hwmonPath == "" {
		return nil
	}
	real, err := filepath.EvalSymlinks(p.hwmonPath)
	if err != nil {
		real = p.hwmonPath
	}
	prefixes := []string{real + "/pwm", p.hwmonPath + "/pwm"}
	var out []string
	for _, pid := range p.procIDs() {
		dir := filepath.Join(procRoot, pid, "fd")
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			l, err := os.Readlink(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			for _, pre := range prefixes {
				if strings.HasPrefix(l, pre) {
					out = append(out, fmt.Sprintf("pid %s (%s) holds %s open", pid, readProc(pid, "comm"), filepath.Base(l)))
					break
				}
			}
		}
	}
	return out
}

// ---- G6: curve tables through the driver's ecram file --------------------

// curveTables are the EC RAM ranges of the curve tables (TESTSPEC §3 G6).
var curveTables = [][2]int{{0x10, 0x27}, {0x30, 0x38}, {0x70, 0x81}}

// gateG6 records the duty registers (first sample) and, with the driver's
// ecram file (0.2.1+sr2, debug_regs=1), the curve tables: PASS when read,
// FAIL when the file is there but the read fails or the duty registers are
// not readable, PARTIAL without ecram (the tables then come from a mode-A
// snapshot taken after boot, before the module was loaded; the run record
// names it). Never blocking. tables is the hex of each range when read.
func gateG6(sysRoot string, dcrOK bool, dcrErr error) (validate.Gate, map[string]string) {
	g := validate.Gate{ID: "G6", Condition: "register snapshot before the first write: duty registers and EC RAM curve tables 0x10-0x27, 0x30-0x38, 0x70-0x81"}
	if !dcrOK {
		g.Result, g.Detail = "FAIL", "duty registers not readable: "+errText(dcrErr)
		return g, nil
	}
	p := n5driver.ECRAMPath(sysRoot)
	if _, err := os.Stat(p); err != nil {
		g.Result = "PARTIAL"
		g.Detail = "duty registers recorded (first sample); no " + p + " (driver 0.2.1+sr2 with debug_regs=1): the curve tables come from a mode-A snapshot (tools/f8a/f8a-snapshot.sh) taken after boot and before the module was loaded - name it in the run record; whether something wrote earlier in this boot is not visible"
		return g, nil
	}
	e, err := n5driver.ReadECRAM(p)
	if err != nil {
		g.Result, g.Detail = "FAIL", "duty registers recorded; curve tables not read: "+err.Error()
		return g, nil
	}
	tables := map[string]string{}
	for _, r := range curveTables {
		tables[fmt.Sprintf("0x%02x-0x%02x", r[0], r[1])] = e.Hex(r[0], r[1])
	}
	g.Result = "PASS"
	g.Detail = "duty registers (first sample) and curve tables (driver ecram, meta.json ec_ram_tables) recorded; whether something wrote earlier in this boot is not visible"
	return g, tables
}

// ---- G7: board, vendor and driver state allow writes ---------------------

// gateG7 checks TESTSPEC 0.4 §3 G7 (#7): the board is in the driver's write
// table (hwstatus.WriteCapable: product/board and the measured sys_vendor),
// the module is loaded and not with force.
func gateG7(id machineIdentity) validate.Gate {
	g := validate.Gate{ID: "G7", Condition: "board in the driver's write table, sys_vendor as measured for that board, module not loaded with force"}
	var why []string
	d := id.DMI
	if !hwstatus.WriteCapable(d) {
		why = append(why, fmt.Sprintf("DMI %q/%q with sys_vendor %q is not in the driver's write table (N5 PRO/F8NAA, N5/F8NAA with %q)", d.Product, d.Board, d.SysVendor, n5driver.SysVendor))
	}
	dr := id.Driver
	switch f := dr.Param("force"); {
	case !dr.Loaded:
		why = append(why, "driver "+n5driver.ModuleName+" not loaded")
	case f == "":
		why = append(why, "module parameter force not readable")
	case dr.Forced():
		why = append(why, "module loaded with force=Y")
	}
	if len(why) > 0 {
		g.Result, g.Detail = "FAIL", strings.Join(why, "; ")
		return g
	}
	g.Result, g.Detail = "PASS", fmt.Sprintf("%s/%s, sys_vendor %q, driver %s force=%s", d.Product, d.Board, d.SysVendor, orDash(dr.Version), dr.Param("force"))
	return g
}

// ---- third refusal: an earlier run left a channel unrestored -------------

// validateMarkerName is validate's own state file in the run dir: written
// before the first write, removed when every channel is restored. A file
// that is still there (validate killed with SIGKILL, power loss before the
// restore, a restore that failed) refuses the next run (TESTSPEC 0.4 §3,
// #10).
const validateMarkerName = "validate-restore.json"

// ectestStatePath is the state file of n5-ectest (its writing runs); an open
// one refuses validate as well. A var for tests.
var ectestStatePath = "/run/n5-ectest/restore.json"

type validateMarker struct {
	Tool     string         `json:"tool"`
	Version  string         `json:"version"`
	PID      int            `json:"pid"`
	Started  string         `json:"started"`
	Channel  int            `json:"channel"`
	Restore  map[int]string `json:"restore_values"`
	Bundle   string         `json:"out"`
	Note     string         `json:"note"`
	Failures []string       `json:"restore_failed,omitempty"`
}

func validateMarkerPath() string { return filepath.Join(runDir(), validateMarkerName) }

// openRestoreState returns why a run must not start ("" = it may): an own
// marker or an open n5-ectest state file, or one that cannot be read.
func openRestoreState() string {
	p := validateMarkerPath()
	b, err := os.ReadFile(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Sprintf("state file of an earlier run not readable: %v", err)
	default:
		var m validateMarker
		if json.Unmarshal(b, &m) != nil {
			return fmt.Sprintf("state file %s of an earlier run not readable (not JSON)", p)
		}
		return fmt.Sprintf("%s says an earlier validate run (pid %d, %s, pwm%d) did not confirm the restore of its channels %v", p, m.PID, m.Started, m.Channel, m.Restore)
	}
	b, err = os.ReadFile(ectestStatePath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ""
	case err != nil:
		return fmt.Sprintf("n5-ectest state file %s not readable: %v", ectestStatePath, err)
	}
	var st struct {
		State    string            `json:"state"`
		PID      int               `json:"pid"`
		Channels map[string]string `json:"channels"`
	}
	if json.Unmarshal(b, &st) != nil {
		return fmt.Sprintf("n5-ectest state file %s not readable (not JSON)", ectestStatePath)
	}
	if st.State != "restored" && st.State != "discarded" {
		return fmt.Sprintf("%s says an n5-ectest run (pid %d) left pwm channel(s) %v not restored (run: n5-ectest restore)", ectestStatePath, st.PID, st.Channels)
	}
	return ""
}

// writeValidateMarker records this run before its first write.
func writeValidateMarker(ch int, stops map[int]string, out string) error {
	m := validateMarker{Tool: "n5-fangov validate", Version: version.Version, PID: os.Getpid(), Started: time.Now().UTC().Format(time.RFC3339),
		Channel: ch, Restore: stops, Bundle: out,
		Note: "removed by validate when every channel is restored; still here = check the channels (systemctl start n5-fangov, or n5-fangov failsafe), then delete this file"}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runDir(), 0o750); err != nil {
		return err
	}
	return os.WriteFile(validateMarkerPath(), append(b, '\n'), 0o600)
}

// ---- redaction -----------------------------------------------------------

// bundleRedactor builds the redactor of a bundle (TESTSPEC 0.4 #8): the host
// names, DMI serials and UUID, user names and SSH_* values of this machine,
// plus the pattern rules; the identity strings are protected.
func bundleRedactor(sysRoot string, id machineIdentity, getenv func(string) string) *validate.Redactor {
	var hosts []string
	if h, err := os.Hostname(); err == nil {
		hosts = append(hosts, h)
	}
	hosts = append(hosts, readProc("sys", "kernel", "hostname"))
	r := validate.NewRedactor(hosts...)
	d := id.DMI
	r.Protect(id.Kernel, id.Driver.Version, id.Driver.SrcVersion, d.Product, d.Board, d.BoardVersion, d.SysVendor, d.BIOS, d.BIOSDate, d.EC)
	for _, f := range []string{"product_serial", "board_serial", "chassis_serial", "product_uuid"} {
		if b, err := os.ReadFile(filepath.Join(sysRoot, "class", "dmi", "id", f)); err == nil {
			v := strings.TrimSpace(string(b))
			if v != "" && !strings.EqualFold(v, "Default string") && !strings.EqualFold(v, "To be filled by O.E.M.") {
				r.Literal(v, "[serial]")
			}
		}
	}
	for _, k := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		r.Literal(getenv(k), "[ssh]")
	}
	for _, k := range []string{"USER", "SUDO_USER", "LOGNAME"} {
		if u := getenv(k); u != "root" {
			r.Literal(u, "[user]")
		}
	}
	return r
}

// ---- --restore -----------------------------------------------------------

// parseRestoreFlag reads "pwm3=140,pwm4=auto" (TESTSPEC 0.4 §4: auto or a
// duty >= 100).
func parseRestoreFlag(s string) (map[int]string, error) {
	out := map[int]string{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		ch, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(k), "pwm"))
		if !ok || err != nil || ch < 1 || ch > 4 {
			return nil, fmt.Errorf("--restore: %q is not pwmN=auto|D", part)
		}
		v = strings.TrimSpace(v)
		if v != "auto" {
			d, err := strconv.Atoi(v)
			if err != nil || d < validate.MinDuty || d > 255 {
				return nil, fmt.Errorf("--restore: pwm%d=%s: auto or a duty %d..255", ch, v, validate.MinDuty)
			}
		}
		out[ch] = v
	}
	return out, nil
}
