package sysinfo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// pciDevice is one entry of /sys/bus/pci/devices, enriched with the names
// lspci -mm prints for it.
type pciDevice struct {
	Addr   string // 0000:c7:00.0
	Class  uint32 // 0x038000
	Vendor uint16
	Device uint16
	Driver string // basename of the driver link, "" when unbound
	// From lspci -mm (empty without lspci).
	ClassName  string
	VendorName string
	DeviceName string
}

// name is the device name: lspci's, else "vendor:device" hex.
func (d *pciDevice) name() string {
	if d.DeviceName != "" {
		return d.DeviceName
	}
	return fmt.Sprintf("PCI device %04x:%04x", d.Vendor, d.Device)
}

// vendorName is lspci's vendor, else a short name for the common ids.
func (d *pciDevice) vendorName() string {
	if d.VendorName != "" {
		return shortVendor(d.VendorName)
	}
	switch d.Vendor {
	case 0x1002, 0x1022:
		return "AMD"
	case 0x8086:
		return "Intel"
	case 0x10de:
		return "NVIDIA"
	}
	return fmt.Sprintf("%04x", d.Vendor)
}

// shortVendor trims lspci's long vendor strings to the bracketed short
// form when present ("Advanced Micro Devices, Inc. [AMD/ATI]" → "AMD/ATI").
func shortVendor(v string) string {
	if i := strings.LastIndexByte(v, '['); i >= 0 && strings.HasSuffix(v, "]") {
		return v[i+1 : len(v)-1]
	}
	return v
}

// lspciTimeout bounds the lspci run.
const lspciTimeout = 5 * time.Second

// pciDevices enumerates /sys/bus/pci/devices and merges lspci names.
func (c *Collector) pciDevices(fail func(string, error)) []pciDevice {
	dir := c.sys("bus", "pci", "devices")
	entries, err := os.ReadDir(dir)
	if err != nil {
		fail("pci", err)
		return nil
	}
	var out []pciDevice
	for _, e := range entries {
		addr := e.Name()
		if !pciAddr.MatchString(addr) {
			continue
		}
		p := filepath.Join(dir, addr)
		d := pciDevice{Addr: addr, Driver: linkBase(filepath.Join(p, "driver"))}
		d.Class = uint32(hexFile(filepath.Join(p, "class")))
		d.Vendor = uint16(hexFile(filepath.Join(p, "vendor")))
		d.Device = uint16(hexFile(filepath.Join(p, "device")))
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	if c.o.LSPCI == "-" {
		return out
	}
	names, err := c.runLspci()
	if err != nil {
		fail("lspci", fmt.Errorf("%w (device names from ids only)", err))
		return out
	}
	for i := range out {
		if n, ok := names[out[i].Addr]; ok {
			out[i].ClassName, out[i].VendorName, out[i].DeviceName = n.Class, n.Vendor, n.Device
		}
	}
	return out
}

// hexFile parses a sysfs hex attribute ("0x038000").
func hexFile(path string) uint64 {
	s := strings.TrimPrefix(readFile(path), "0x")
	n, _ := strconv.ParseUint(s, 16, 32)
	return n
}

// findPCI returns the device with the given address (domain optional).
func findPCI(devs []pciDevice, addr string) *pciDevice {
	if addr == "" {
		return nil
	}
	for i := range devs {
		if devs[i].Addr == addr || devs[i].Addr == "0000:"+addr {
			return &devs[i]
		}
	}
	return nil
}

// lspciNames is what lspci -mm says about one slot.
type lspciNames struct{ Class, Vendor, Device string }

// runLspci runs `lspci -mm -D` and parses it.
func (c *Collector) runLspci() (map[string]lspciNames, error) {
	bin := c.o.LSPCI
	if bin == "" {
		p, err := exec.LookPath("lspci")
		if err != nil {
			return nil, err
		}
		bin = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspciTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-mm", "-D").Output()
	if err != nil {
		return nil, err
	}
	return ParseLspciMM(string(out)), nil
}

// ParseLspciMM parses `lspci -mm` output (with or without -D): one line
// per slot — the slot, then quoted class, vendor and device, optional
// -rXX/-pXX, and quoted subsystem vendor/device (possibly empty). Keys are
// the slot with the domain prefix as printed; findPCI tolerates both.
func ParseLspciMM(s string) map[string]lspciNames {
	out := map[string]lspciNames{}
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		slot, rest, ok := strings.Cut(ln, " ")
		if !ok {
			continue
		}
		q := quotedFields(rest)
		if len(q) < 3 {
			continue
		}
		out[slot] = lspciNames{Class: q[0], Vendor: q[1], Device: q[2]}
	}
	return out
}

// quotedFields extracts the "…" fields of a line in order; bare tokens
// (-r03, -p00) are skipped. lspci escapes a quote inside a name as \".
func quotedFields(s string) []string {
	var out []string
	var b strings.Builder
	in, esc := false, false
	for _, r := range s {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case in && r == '\\':
			esc = true
		case r == '"':
			if in {
				out = append(out, b.String())
				b.Reset()
			}
			in = !in
		case in:
			b.WriteRune(r)
		}
	}
	return out
}
