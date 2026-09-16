// Package sysinfo reads a hardware and OS inventory of the host for the
// dashboard's System card and tab (DESIGN.md "System inventory"). Every
// source is read on its own; a source that fails leaves its section empty
// and adds one line to Info.Errors — Collect never fails as a whole.
//
// Sources (all read-only): /sys/class/dmi/id (machine), /proc/cpuinfo and
// /sys/devices/system/cpu (CPU), /proc/meminfo plus the SMBIOS table under
// /sys/firmware/dmi/tables (memory and its modules), /sys/bus/pci/devices
// with names from `lspci -mm` (GPU, NPU, NIC models, storage controllers),
// /sys/class/accel (NPU), /sys/class/drm (GPU), /sys/class/net (physical
// NICs), /sys/block (disks), /sys/module (driver versions), /etc/os-release,
// /proc/sys/kernel/osrelease, /proc/uptime, /proc/loadavg.
//
// The sysfs root follows N5FANGOV_SYSFS like internal/hwmon (rule 10); the
// proc root and the os-release path are overridable for tests as well.
package sysinfo

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Info is the inventory as GET /api/system serves it.
type Info struct {
	Host          Host          `json:"host"`
	Machine       Machine       `json:"machine"`
	CPU           CPU           `json:"cpu"`
	Memory        Memory        `json:"memory"`
	GPUs          []GPU         `json:"gpus"`
	NPUs          []NPU         `json:"npus"`
	NICs          []NIC         `json:"nics"`
	Storage       Storage       `json:"storage"`
	FanController FanController `json:"fan_controller"`
	// Collected is when the live parts were read (unix seconds);
	// StaticAt when the cached parts were last read.
	Collected int64 `json:"collected"`
	StaticAt  int64 `json:"static_at"`
	// Errors lists the sources that could not be read ("source: reason").
	Errors []string `json:"errors"`
}

// Host is the OS side: names, kernel, uptime, load.
type Host struct {
	Hostname string  `json:"hostname"`
	OS       string  `json:"os"`
	Kernel   string  `json:"kernel"`
	UptimeS  int64   `json:"uptime_s"`
	Load1    float64 `json:"load1"`
	Load5    float64 `json:"load5"`
	Load15   float64 `json:"load15"`
}

// Machine is the DMI identity.
type Machine struct {
	Vendor      string `json:"vendor"`
	Product     string `json:"product"`
	Board       string `json:"board"`
	BoardVendor string `json:"board_vendor"`
	BIOSVersion string `json:"bios_version"`
	BIOSDate    string `json:"bios_date"`
}

// CPU is the processor summary.
type CPU struct {
	Model   string `json:"model"`
	Sockets int    `json:"sockets"`
	Cores   int    `json:"cores"`
	Threads int    `json:"threads"`
	MaxMHz  int    `json:"max_mhz"`
}

// MemoryModule is one populated SMBIOS type 17 slot.
type MemoryModule struct {
	Slot         string `json:"slot"`
	Bank         string `json:"bank"`
	SizeBytes    int64  `json:"size_bytes"`
	Type         string `json:"type"`
	FormFactor   string `json:"form_factor"`
	SpeedMTs     int    `json:"speed_mts"`
	Manufacturer string `json:"manufacturer"`
	Part         string `json:"part"`
	ECC          bool   `json:"ecc"`
}

// Memory is /proc/meminfo (live) plus the SMBIOS modules (cached).
type Memory struct {
	TotalBytes     int64 `json:"total_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
	SwapTotalBytes int64 `json:"swap_total_bytes"`
	SwapFreeBytes  int64 `json:"swap_free_bytes"`
	// InstalledBytes is the sum of the module sizes (0 without SMBIOS).
	InstalledBytes int64          `json:"installed_bytes"`
	SMBIOS         string         `json:"smbios"` // "3.7" from the entry point, "" unknown
	Modules        []MemoryModule `json:"modules"`
}

// GPU is a display controller.
type GPU struct {
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
	PCI    string `json:"pci"`
	Driver string `json:"driver"`
}

// NPU is a compute accelerator (/sys/class/accel or a PCI device whose
// name says Neural Processing Unit).
type NPU struct {
	Name          string `json:"name"`
	PCI           string `json:"pci"`
	Driver        string `json:"driver"`
	DriverVersion string `json:"driver_version"`
	Accel         string `json:"accel"` // accel0
}

// NIC is a physical network interface (has a device link in /sys/class/net).
type NIC struct {
	Name      string `json:"name"`
	PCI       string `json:"pci"` // "" for USB and platform NICs
	Model     string `json:"model"`
	Driver    string `json:"driver"`
	SpeedMbit int    `json:"speed_mbit"` // -1 when unknown (link down)
	State     string `json:"state"`
	Duplex    string `json:"duplex"`
	MAC       string `json:"mac"`
	MTU       int    `json:"mtu"`
}

// Controller is a PCI storage controller.
type Controller struct {
	Kind   string `json:"kind"` // nvme | sata | sas | scsi | raid
	Name   string `json:"name"`
	PCI    string `json:"pci"`
	Driver string `json:"driver"`
}

// Disk is a physical block device.
type Disk struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	SizeBytes  int64  `json:"size_bytes"`
	Rotational bool   `json:"rotational"`
	Transport  string `json:"transport"` // nvme | sata | usb | virtio | mmc | scsi
}

// Storage groups controllers and disks.
type Storage struct {
	Controllers []Controller `json:"controllers"`
	Disks       []Disk       `json:"disks"`
}

// FanController is the detected profile and its kernel driver. Profile and
// Hwmon come from the caller (the running device); Module and
// ModuleVersion are resolved from sysfs.
type FanController struct {
	Profile       string `json:"profile"`
	Hwmon         string `json:"hwmon"`
	Module        string `json:"module"`
	ModuleVersion string `json:"module_version"`
}

// Options configure a Collector. Zero values mean the real host.
type Options struct {
	// Sysfs is the /sys root; "" → N5FANGOV_SYSFS or /sys.
	Sysfs string
	// Proc is the /proc root; "" → /proc.
	Proc string
	// OSRelease is the os-release path; "" → /etc/os-release.
	OSRelease string
	// LSPCI is the lspci binary; "" → looked up in PATH, "-" → never run
	// (names come from the vendor/device ids only).
	LSPCI string
	// Hostname replaces os.Hostname (tests).
	Hostname func() (string, error)
	// Fan is the running fan controller (profile name and hwmon path).
	Fan FanController
	// Now replaces time.Now (tests).
	Now func() time.Time
	// CacheTTL is how long the static parts are reused; 0 → 10 minutes.
	CacheTTL time.Duration
}

// DefaultCacheTTL is how long the static inventory is reused.
const DefaultCacheTTL = 10 * time.Minute

// Collector caches the static parts (machine, CPU, memory modules, PCI
// devices, disks, driver versions) for CacheTTL and reads the live parts
// (memory usage, uptime, load, NIC link state) on every Collect.
type Collector struct {
	o        Options
	mu       sync.Mutex
	static   Info
	staticAt time.Time
}

// NewCollector fills the defaults and returns a Collector.
func NewCollector(o Options) *Collector {
	if o.Sysfs == "" {
		o.Sysfs = os.Getenv("N5FANGOV_SYSFS")
	}
	if o.Sysfs == "" {
		o.Sysfs = "/sys"
	}
	if o.Proc == "" {
		o.Proc = "/proc"
	}
	if o.OSRelease == "" {
		o.OSRelease = "/etc/os-release"
	}
	if o.Hostname == nil {
		o.Hostname = os.Hostname
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	return &Collector{o: o}
}

// Collect returns a one-shot inventory without a cache.
func Collect(o Options) Info { return NewCollector(o).Collect() }

// Collect returns the inventory: cached static parts (refreshed after
// CacheTTL) with the live parts read now.
func (c *Collector) Collect() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.o.Now()
	if c.staticAt.IsZero() || now.Sub(c.staticAt) >= c.o.CacheTTL || now.Before(c.staticAt) {
		c.static = c.collectStatic()
		c.staticAt = now
		c.static.StaticAt = now.Unix()
	}
	out := c.static
	// Copies of the slices the live part edits (NICs) so the cache stays clean.
	out.NICs = append([]NIC(nil), c.static.NICs...)
	out.Errors = append([]string(nil), c.static.Errors...)
	out.Collected = now.Unix()
	c.collectLive(&out)
	if out.Errors == nil {
		out.Errors = []string{}
	}
	return out
}

// Invalidate drops the cached static parts (the next Collect re-reads).
func (c *Collector) Invalidate() {
	c.mu.Lock()
	c.staticAt = time.Time{}
	c.mu.Unlock()
}

// ---- static -----------------------------------------------------------------

func (c *Collector) collectStatic() Info {
	var in Info
	fail := func(src string, err error) {
		if err != nil {
			in.Errors = append(in.Errors, src+": "+err.Error())
		}
	}
	in.Host.OS = c.osRelease(fail)
	in.Host.Kernel = c.readProc("sys/kernel/osrelease")
	if hn, err := c.o.Hostname(); err == nil {
		in.Host.Hostname = hn
	} else {
		fail("hostname", err)
	}
	in.Machine = c.machine()
	in.CPU = c.cpu(fail)
	in.Memory.Modules, in.Memory.SMBIOS = c.memoryModules(fail)
	for _, m := range in.Memory.Modules {
		in.Memory.InstalledBytes += m.SizeBytes
	}
	pci := c.pciDevices(fail)
	in.GPUs = c.gpus(pci)
	in.NPUs = c.npus(pci)
	in.NICs = c.nics(pci, fail)
	in.Storage.Controllers = c.controllers(pci)
	in.Storage.Disks = c.disks(fail)
	in.FanController = c.fanController()
	// Never null in JSON.
	if in.GPUs == nil {
		in.GPUs = []GPU{}
	}
	if in.NPUs == nil {
		in.NPUs = []NPU{}
	}
	if in.NICs == nil {
		in.NICs = []NIC{}
	}
	if in.Storage.Controllers == nil {
		in.Storage.Controllers = []Controller{}
	}
	if in.Storage.Disks == nil {
		in.Storage.Disks = []Disk{}
	}
	if in.Memory.Modules == nil {
		in.Memory.Modules = []MemoryModule{}
	}
	return in
}

// ---- live -------------------------------------------------------------------

func (c *Collector) collectLive(in *Info) {
	fail := func(src string, err error) {
		if err != nil {
			in.Errors = append(in.Errors, src+": "+err.Error())
		}
	}
	if up := c.readProc("uptime"); up != "" {
		if f, err := strconv.ParseFloat(strings.Fields(up)[0], 64); err == nil {
			in.Host.UptimeS = int64(f)
		}
	} else {
		fail("uptime", fmt.Errorf("%s unreadable", c.proc("uptime")))
	}
	if la := strings.Fields(c.readProc("loadavg")); len(la) >= 3 {
		in.Host.Load1, _ = strconv.ParseFloat(la[0], 64)
		in.Host.Load5, _ = strconv.ParseFloat(la[1], 64)
		in.Host.Load15, _ = strconv.ParseFloat(la[2], 64)
	} else {
		fail("loadavg", fmt.Errorf("%s unreadable", c.proc("loadavg")))
	}
	c.meminfo(&in.Memory, fail)
	for i := range in.NICs {
		c.nicLive(&in.NICs[i])
	}
}

// meminfo fills the live memory figures from /proc/meminfo (kB values).
func (c *Collector) meminfo(m *Memory, fail func(string, error)) {
	f, err := os.Open(c.proc("meminfo"))
	if err != nil {
		fail("meminfo", err)
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fs := strings.Fields(v)
		if len(fs) == 0 {
			continue
		}
		n, err := strconv.ParseInt(fs[0], 10, 64)
		if err != nil {
			continue
		}
		n *= 1024
		switch k {
		case "MemTotal":
			m.TotalBytes = n
		case "MemAvailable":
			m.AvailableBytes = n
		case "SwapTotal":
			m.SwapTotalBytes = n
		case "SwapFree":
			m.SwapFreeBytes = n
		}
	}
	if m.TotalBytes == 0 {
		fail("meminfo", fmt.Errorf("MemTotal missing"))
	}
}

// ---- sources ----------------------------------------------------------------

func (c *Collector) sys(parts ...string) string {
	return filepath.Join(append([]string{c.o.Sysfs}, parts...)...)
}

func (c *Collector) proc(parts ...string) string {
	return filepath.Join(append([]string{c.o.Proc}, parts...)...)
}

// readFile returns the trimmed content or "".
func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (c *Collector) readSys(parts ...string) string  { return readFile(c.sys(parts...)) }
func (c *Collector) readProc(parts ...string) string { return readFile(c.proc(parts...)) }

// readInt parses a sysfs integer; ok=false when unreadable or not a number
// (a NIC's speed answers EINVAL while the link is down).
func readInt(path string) (int64, bool) {
	s := readFile(path)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// linkBase is the basename of a symlink's target ("" when not a link).
func linkBase(path string) string {
	t, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return filepath.Base(t)
}

func (c *Collector) osRelease(fail func(string, error)) string {
	b, err := os.ReadFile(c.o.OSRelease)
	if err != nil {
		fail("os-release", err)
		return ""
	}
	var name, id, ver string
	for _, ln := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(ln), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "PRETTY_NAME":
			return v
		case "NAME":
			name = v
		case "ID":
			id = v
		case "VERSION":
			ver = v
		}
	}
	if name == "" {
		name = id
	}
	return strings.TrimSpace(name + " " + ver)
}

func (c *Collector) machine() Machine {
	id := func(n string) string { return c.readSys("class", "dmi", "id", n) }
	return Machine{
		Vendor: id("sys_vendor"), Product: id("product_name"),
		Board: id("board_name"), BoardVendor: id("board_vendor"),
		BIOSVersion: id("bios_version"), BIOSDate: id("bios_date"),
	}
}

// cpu parses /proc/cpuinfo: model name, threads (processor lines),
// sockets (distinct physical id) and cores (distinct physical id + core
// id); the maximum clock comes from cpufreq.
func (c *Collector) cpu(fail func(string, error)) CPU {
	var out CPU
	f, err := os.Open(c.proc("cpuinfo"))
	if err != nil {
		fail("cpuinfo", err)
		return out
	}
	defer f.Close()
	sockets, cores := map[string]bool{}, map[string]bool{}
	phys, core := "0", ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ln := sc.Text()
		if ln == "" {
			if core != "" {
				cores[phys+":"+core] = true
			}
			phys, core = "0", ""
			continue
		}
		k, v, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			out.Threads++
		case "model name":
			if out.Model == "" {
				out.Model = v
			}
		case "physical id":
			phys = v
			sockets[v] = true
		case "core id":
			core = v
		}
	}
	if core != "" {
		cores[phys+":"+core] = true
	}
	out.Sockets, out.Cores = len(sockets), len(cores)
	if out.Sockets == 0 && out.Threads > 0 {
		out.Sockets = 1
	}
	if out.Cores == 0 {
		out.Cores = out.Threads
	}
	if out.Threads == 0 {
		fail("cpuinfo", fmt.Errorf("no processor entries"))
	}
	for _, p := range []string{c.sys("devices", "system", "cpu", "cpu0", "cpufreq", "cpuinfo_max_freq"), c.sys("devices", "system", "cpu", "cpufreq", "policy0", "cpuinfo_max_freq")} {
		if khz, ok := readInt(p); ok && khz > 0 {
			out.MaxMHz = int(khz / 1000)
			break
		}
	}
	return out
}

// memoryModules parses the SMBIOS table exported by the kernel.
func (c *Collector) memoryModules(fail func(string, error)) ([]MemoryModule, string) {
	table, err := os.ReadFile(c.sys("firmware", "dmi", "tables", "DMI"))
	if err != nil {
		fail("smbios", fmt.Errorf("%w (memory modules unknown)", err))
		return nil, ""
	}
	ver := ""
	if ep, err := os.ReadFile(c.sys("firmware", "dmi", "tables", "smbios_entry_point")); err == nil {
		ver = SMBIOSVersion(ep)
	}
	mods, err := ParseMemoryModules(table)
	if err != nil {
		fail("smbios", err)
	}
	return mods, ver
}

// ---- PCI-derived sections -----------------------------------------------------

// gpus: every display-class PCI device, with the drm card as confirmation.
func (c *Collector) gpus(pci []pciDevice) []GPU {
	var out []GPU
	seen := map[string]bool{}
	add := func(d *pciDevice) {
		if d == nil || seen[d.Addr] {
			return
		}
		seen[d.Addr] = true
		out = append(out, GPU{Name: d.name(), Vendor: d.vendorName(), PCI: d.Addr, Driver: d.Driver})
	}
	for _, e := range dirNames(c.sys("class", "drm")) {
		if !strings.HasPrefix(e, "card") || strings.Contains(e, "-") {
			continue // connectors (card1-DP-1) and renderD* are not devices
		}
		add(findPCI(pci, linkBase(c.sys("class", "drm", e, "device"))))
	}
	for i := range pci {
		if pci[i].Class>>16 == 0x03 {
			add(&pci[i])
		}
	}
	return out
}

// npus: /sys/class/accel devices first, then PCI devices whose name or
// driver marks them as an NPU.
func (c *Collector) npus(pci []pciDevice) []NPU {
	var out []NPU
	seen := map[string]bool{}
	add := func(d *pciDevice, accel string) {
		if d == nil || seen[d.Addr] {
			return
		}
		seen[d.Addr] = true
		out = append(out, NPU{Name: d.name(), PCI: d.Addr, Driver: d.Driver, DriverVersion: c.moduleVersion(d.Driver), Accel: accel})
	}
	for _, e := range dirNames(c.sys("class", "accel")) {
		if strings.HasPrefix(e, "accel") {
			add(findPCI(pci, linkBase(c.sys("class", "accel", e, "device"))), e)
		}
	}
	for i := range pci {
		d := &pci[i]
		if strings.Contains(strings.ToLower(d.name()), "neural") || d.Driver == "amdxdna" || d.Driver == "intel_vpu" || d.Driver == "ivpu" {
			add(d, "")
		}
	}
	return out
}

// nics: /sys/class/net entries with a device link (physical), sorted by name.
func (c *Collector) nics(pci []pciDevice, fail func(string, error)) []NIC {
	dir := c.sys("class", "net")
	entries, err := os.ReadDir(dir)
	if err != nil {
		fail("net", err)
		return nil
	}
	var out []NIC
	for _, e := range entries {
		name := e.Name()
		dev := filepath.Join(dir, name, "device")
		if _, err := os.Lstat(dev); err != nil {
			continue // lo, bridges, veth, tap, bonding_masters
		}
		n := NIC{Name: name, Driver: linkBase(filepath.Join(dev, "driver")), MAC: readFile(filepath.Join(dir, name, "address"))}
		if addr := linkBase(dev); pciAddr.MatchString(addr) {
			n.PCI = addr
			if d := findPCI(pci, addr); d != nil {
				n.Model = d.name()
				if n.Driver == "" {
					n.Driver = d.Driver
				}
			}
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// nicLive reads the link state of one NIC.
func (c *Collector) nicLive(n *NIC) {
	p := c.sys("class", "net", n.Name)
	n.State = readFile(filepath.Join(p, "operstate"))
	n.Duplex = readFile(filepath.Join(p, "duplex"))
	n.SpeedMbit = -1
	if v, ok := readInt(filepath.Join(p, "speed")); ok && v >= 0 {
		n.SpeedMbit = int(v)
	}
	if v, ok := readInt(filepath.Join(p, "mtu")); ok {
		n.MTU = int(v)
	}
}

// controllers: PCI mass-storage class devices.
func (c *Collector) controllers(pci []pciDevice) []Controller {
	var out []Controller
	for _, d := range pci {
		kind := ""
		switch d.Class >> 8 {
		case 0x0108:
			kind = "nvme"
		case 0x0106:
			kind = "sata"
		case 0x0107:
			kind = "sas"
		case 0x0100:
			kind = "scsi"
		case 0x0104:
			kind = "raid"
		default:
			continue
		}
		out = append(out, Controller{Kind: kind, Name: d.name(), PCI: d.Addr, Driver: d.Driver})
	}
	return out
}

// virtualBlock lists /sys/block prefixes that are not physical disks.
var virtualBlock = []string{"zd", "loop", "dm-", "ram", "md", "nbd", "drbd", "rbd", "zram", "sr", "fd"}

// disks: /sys/block entries that are physical devices.
func (c *Collector) disks(fail func(string, error)) []Disk {
	dir := c.sys("block")
	entries, err := os.ReadDir(dir)
	if err != nil {
		fail("block", err)
		return nil
	}
	var out []Disk
	for _, e := range entries {
		name := e.Name()
		if isVirtualBlock(name) {
			continue
		}
		p := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(p, "device")); err != nil {
			continue
		}
		d := Disk{Name: name, Model: strings.Join(strings.Fields(readFile(filepath.Join(p, "device", "model"))), " ")}
		if sectors, ok := readInt(filepath.Join(p, "size")); ok {
			d.SizeBytes = sectors * 512
		}
		rot, _ := readInt(filepath.Join(p, "queue", "rotational"))
		d.Rotational = rot == 1
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p
		}
		d.Transport = transportOf(name, real)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func isVirtualBlock(name string) bool {
	for _, p := range virtualBlock {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// transportOf classifies a block device by its name and its resolved
// device path (…/ata1/…, …/usb1/…).
func transportOf(name, real string) string {
	if strings.HasPrefix(name, "nvme") {
		return "nvme"
	}
	for _, t := range []struct{ marker, kind string }{{"/usb", "usb"}, {"/ata", "sata"}, {"/virtio", "virtio"}, {"/mmc", "mmc"}} {
		if strings.Contains(real, t.marker) {
			return t.kind
		}
	}
	return "scsi"
}

// fanController resolves the driver module of the hwmon device the
// profile runs on: <hwmon>/device/driver/module → /sys/module/<name>.
func (c *Collector) fanController() FanController {
	fc := c.o.Fan
	if fc.Hwmon == "" {
		return fc
	}
	hw := fc.Hwmon
	if !filepath.IsAbs(hw) {
		hw = c.sys(hw)
	}
	fc.Module = linkBase(filepath.Join(hw, "device", "driver", "module"))
	if fc.Module == "" {
		if name := readFile(filepath.Join(hw, "name")); name != "" {
			if _, err := os.Stat(c.sys("module", name)); err == nil {
				fc.Module = name
			}
		}
	}
	fc.ModuleVersion = c.moduleVersion(fc.Module)
	return fc
}

// moduleVersion is /sys/module/<name>/version ("" when the module has none).
func (c *Collector) moduleVersion(mod string) string {
	if mod == "" {
		return ""
	}
	return c.readSys("module", mod, "version")
}

// dirNames lists a directory's entry names (nil on error).
func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// pciAddr matches a full PCI address (domain:bus:device.function).
var pciAddr = regexp.MustCompile(`^[0-9a-fA-F]{4,}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F]$`)
