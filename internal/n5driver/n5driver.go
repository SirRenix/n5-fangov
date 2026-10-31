// Package n5driver reads the state of the minisforum_n5_it5571 kernel module
// (N5 Pro EC driver) from sysfs and debugfs only: module version, parameters,
// taint, which hwmon attributes it exposes, and the optional read-only EC
// register dump (driver 0.2.1+sr1, module parameter debug_regs=1). Nothing
// here writes and nothing reads the kernel log.
package n5driver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Names of the driver in its different roles.
const (
	ModuleName  = "minisforum_n5_it5571"      // kernel module and hwmon name
	DKMSName    = "minisforum-n5-it5571"      // DKMS module (/usr/src/<name>-<ver>)
	PackageName = "minisforum-n5-it5571-dkms" // Debian package
	// SysVendor is the DMI sys_vendor the driver (0.2.1+sr1) requires for
	// writes and for the dcr file, measured on the reference N5 Pro.
	SysVendor = "Micro Computer (HK) Tech Limited"
)

// State is what sysfs says about the driver right now.
type State struct {
	Loaded     bool              `json:"loaded"`      // /sys/module/<name> exists
	Version    string            `json:"version"`     // MODULE_VERSION ("" when the module declares none)
	SrcVersion string            `json:"srcversion"`  // hash of the module source
	Taint      string            `json:"taint"`       // module taint letters ("OE")
	Params     map[string]string `json:"parameters"`  // force, experimental_write, debug_regs (Y/N)
	HwmonPath  string            `json:"hwmon_path"`  // "" when no hwmon device of that name
	Attrs      []string          `json:"hwmon_attrs"` // attribute file names, sorted
	PWMVisible bool              `json:"pwm_visible"` // pwm1 and pwm1_enable present
	DCRPath    string            `json:"dcr_path"`    // the debugfs dcr file when it exists
}

// Param returns a module parameter ("" when absent).
func (s State) Param(name string) string { return s.Params[name] }

func yes(v string) bool { return v == "Y" || v == "y" || v == "1" }

// Forced reports force=Y: the driver bound on a system it does not know.
// From 0.2.1+sr1 such a binding is always read-only.
func (s State) Forced() bool { return yes(s.Param("force")) }

// WriteDisabled reports a loaded driver with a hwmon device but without pwm
// nodes: the driver runs read-only (no experimental_write, force-loaded,
// vendor mismatch, or a board outside the write allowlist).
func (s State) WriteDisabled() bool { return s.HwmonPath != "" && !s.PWMVisible }

// Mode is a one-word summary: not-loaded, no-hwmon, write-enabled,
// write-disabled.
func (s State) Mode() string {
	switch {
	case !s.Loaded && s.HwmonPath == "":
		return "not-loaded"
	case s.HwmonPath == "":
		return "no-hwmon"
	case s.PWMVisible:
		return "write-enabled"
	}
	return "write-disabled"
}

// Findings lists what an operator should know about the driver state, one
// English sentence each. Empty for a loaded, write-enabled, non-forced
// driver without debug_regs.
func (s State) Findings() []string {
	var out []string
	if s.Forced() {
		out = append(out, "force=Y: the driver bound without a known DMI match; from driver 0.2.1+sr1 that binding is always read-only")
	}
	if s.WriteDisabled() {
		out = append(out, "write disabled: the driver hides the pwm nodes ("+s.writeDisabledWhy()+"); fans stay under EC control and n5-fangov cannot regulate")
	}
	if yes(s.Param("debug_regs")) {
		out = append(out, "debug_regs=Y: register dump for validation only, it must not stay in modprobe.d")
	}
	return out
}

func (s State) writeDisabledWhy() string {
	switch {
	case s.Forced():
		return "force-loaded"
	case s.Param("experimental_write") != "" && !yes(s.Param("experimental_write")):
		return "experimental_write=N"
	}
	return "board not in the driver's write allowlist, DMI vendor mismatch, or experimental_write off"
}

// Read collects the state below sysRoot ("/sys"). debugfs is expected at
// <sysRoot>/kernel/debug.
func Read(sysRoot string) State {
	st := State{Params: map[string]string{}}
	mod := filepath.Join(sysRoot, "module", ModuleName)
	if fi, err := os.Stat(mod); err == nil && fi.IsDir() {
		st.Loaded = true
	}
	st.Version = readTrim(filepath.Join(mod, "version"))
	st.SrcVersion = readTrim(filepath.Join(mod, "srcversion"))
	st.Taint = readTrim(filepath.Join(mod, "taint"))
	if ents, err := os.ReadDir(filepath.Join(mod, "parameters")); err == nil {
		for _, e := range ents {
			st.Params[e.Name()] = readTrim(filepath.Join(mod, "parameters", e.Name()))
		}
	}
	st.HwmonPath = findHwmon(sysRoot)
	if st.HwmonPath != "" {
		if ents, err := os.ReadDir(st.HwmonPath); err == nil {
			for _, e := range ents {
				if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
					st.Attrs = append(st.Attrs, e.Name())
				}
			}
			sort.Strings(st.Attrs)
		}
		st.PWMVisible = exists(filepath.Join(st.HwmonPath, "pwm1")) && exists(filepath.Join(st.HwmonPath, "pwm1_enable"))
	}
	if p := DCRPath(sysRoot); exists(p) {
		st.DCRPath = p
	}
	return st
}

// findHwmon returns <sysRoot>/class/hwmon/hwmonN whose name is the module's.
func findHwmon(sysRoot string) string {
	dir := filepath.Join(sysRoot, "class", "hwmon")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(dir, n)
		if readTrim(filepath.Join(p, "name")) == ModuleName {
			return p
		}
	}
	return ""
}

// DCRPath is the debugfs file of driver 0.2.1+sr1 with debug_regs=1.
func DCRPath(sysRoot string) string {
	return filepath.Join(sysRoot, "kernel", "debug", ModuleName, "dcr")
}

// DCR is the EC PWM register block I2EC 0x1800..0x180f: 0x1800 C0CPRS,
// 0x1801 CTR, 0x1802+n = DCRn. Channel map of the N5 Pro: DCR1 CPU (pwm1),
// DCR2 SSD (pwm2), DCR3 = DCR4 HDD (pwm3), DCR5 PCIe (pwm4).
type DCR [16]byte

// Reg returns DCRn (0..7).
func (d DCR) Reg(n int) int { return int(d[2+n]) }

// ChannelDCR maps an n5pro pwm channel to its DCR number.
func ChannelDCR(pwm int) (int, bool) {
	switch pwm {
	case 1, 2, 3:
		return pwm, true
	case 4:
		return 5, true
	}
	return 0, false
}

// Channel returns the duty register of pwm channel 1..4.
func (d DCR) Channel(pwm int) (int, bool) {
	n, ok := ChannelDCR(pwm)
	if !ok {
		return 0, false
	}
	return d.Reg(n), true
}

// String renders the dump like the driver ("1800: 00 ff ...").
func (d DCR) String() string {
	var b strings.Builder
	b.WriteString("1800:")
	for _, v := range d {
		fmt.Fprintf(&b, " %02x", v)
	}
	return b.String()
}

// ParseDCR reads the dcr file format ("1800: 00 ff 00 55 ..."). A dump of
// the upstream tool n5_i2ec_read with more lines is accepted; lines outside
// 0x1800..0x180f are ignored, a partial line ("1803: 55 4a 57 57 8c") fills
// from its address. DCR1..DCR5 (0x1803..0x1807) must be covered.
func ParseDCR(s string) (DCR, error) {
	var d DCR
	var seen [16]bool
	for _, line := range strings.Split(s, "\n") {
		addr, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		a, err := strconv.ParseUint(strings.TrimSpace(addr), 16, 16)
		if err != nil || a < 0x1800 || a > 0x180f {
			continue
		}
		off := int(a - 0x1800)
		for _, f := range strings.Fields(rest) {
			if off >= len(d) {
				break
			}
			v, err := strconv.ParseUint(f, 16, 8)
			if err != nil {
				return d, fmt.Errorf("n5driver: dcr: bad byte %q", f)
			}
			d[off] = byte(v)
			seen[off] = true
			off++
		}
	}
	for off := 3; off <= 7; off++ {
		if !seen[off] {
			return d, fmt.Errorf("n5driver: dcr: register 0x%04x missing (need 0x1803..0x1807, DCR1..DCR5)", 0x1800+off)
		}
	}
	return d, nil
}

// DCRTimeout bounds one read of the dcr file. The driver gives every EC
// access its own wait budget, but the read also waits for the Super-I/O
// region and the driver lock (review H-2) without one; a read that does
// not return must not hold validate with a channel in manual mode or the
// daemon's write phase.
var DCRTimeout = 5 * time.Second

// ErrDCRTimeout is returned by ReadDCR when the read did not return in
// DCRTimeout, or while an earlier read that timed out is still blocked.
var ErrDCRTimeout = errors.New("n5driver: dcr read timed out")

// dcrBlocked is set while a timed-out read is still blocked in the kernel:
// further reads fail at once instead of stacking up blocked goroutines.
var dcrBlocked atomic.Bool

// ReadDCR reads and parses the debugfs dcr file. Each read makes the driver
// do 16 I2EC reads under its lock (no writes to the EC data register). The
// read runs on its own goroutine and is abandoned after DCRTimeout
// (ErrDCRTimeout); that goroutine finishes on its own when the driver
// returns.
func ReadDCR(path string) (DCR, error) {
	if dcrBlocked.Load() {
		return DCR{}, fmt.Errorf("%w: an earlier read of %s is still blocked", ErrDCRTimeout, path)
	}
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := os.ReadFile(path)
		ch <- res{b, err}
	}()
	t := time.NewTimer(DCRTimeout)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			return DCR{}, r.err
		}
		return ParseDCR(string(r.b))
	case <-t.C:
		dcrBlocked.Store(true)
		go func() {
			<-ch
			dcrBlocked.Store(false)
		}()
		return DCR{}, fmt.Errorf("%w after %s: %s", ErrDCRTimeout, DCRTimeout, path)
	}
}

// ECRAMPath is the read-only debugfs file of driver 0.2.1+sr2 (patch 0008,
// debug_regs=1): EC RAM 0x00..0xff as 16 rows "00: xx xx ...".
func ECRAMPath(sysRoot string) string {
	return filepath.Join(sysRoot, "kernel", "debug", ModuleName, "ecram")
}

// ECRAM is the EC RAM 0x00..0xff.
type ECRAM [256]byte

// Hex renders the bytes lo..hi (inclusive) as "xx xx ...".
func (e ECRAM) Hex(lo, hi int) string {
	var parts []string
	for a := lo; a <= hi && a < len(e); a++ {
		parts = append(parts, fmt.Sprintf("%02x", e[a]))
	}
	return strings.Join(parts, " ")
}

// ParseECRAM reads the ecram file format: 16 rows "00: xx ... (16 bytes)".
// Every one of the 256 bytes must be present (the driver prints all or
// nothing).
func ParseECRAM(s string) (ECRAM, error) {
	var e ECRAM
	var seen [256]bool
	for _, line := range strings.Split(s, "\n") {
		addr, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		a, err := strconv.ParseUint(strings.TrimSpace(addr), 16, 16)
		if err != nil || a > 0xff {
			continue
		}
		off := int(a)
		for _, f := range strings.Fields(rest) {
			if off > 0xff {
				return e, fmt.Errorf("n5driver: ecram: row %02x longer than the EC RAM", a)
			}
			v, err := strconv.ParseUint(f, 16, 8)
			if err != nil {
				return e, fmt.Errorf("n5driver: ecram: bad byte %q", f)
			}
			e[off], seen[off] = byte(v), true
			off++
		}
	}
	for i, ok := range seen {
		if !ok {
			return e, fmt.Errorf("n5driver: ecram: byte 0x%02x missing (incomplete dump)", i)
		}
	}
	return e, nil
}

// ReadECRAM reads and parses the ecram file, abandoned after DCRTimeout
// like ReadDCR (the driver bounds a dump to 2 s).
func ReadECRAM(path string) (ECRAM, error) {
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := os.ReadFile(path)
		ch <- res{b, err}
	}()
	t := time.NewTimer(DCRTimeout)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			return ECRAM{}, r.err
		}
		return ParseECRAM(string(r.b))
	case <-t.C:
		return ECRAM{}, fmt.Errorf("n5driver: ecram read timed out after %s: %s", DCRTimeout, path)
	}
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
