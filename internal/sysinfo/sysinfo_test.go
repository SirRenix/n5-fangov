package sysinfo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
)

// The static fixtures live in testdata/sysfs/n5pro (dmi, net, block, drm,
// accel, module) and internal/sysinfo/testdata (proc, os-release). PCI
// device directories (colons in their names) and every symlink are
// created here at test time: a Windows checkout cannot hold them.

// lspciSample is a captured `lspci -mm -D` excerpt with generic names.
const lspciSample = `0000:00:00.0 "Host bridge" "Example Silicon [EXS]" "Example Root Complex" -p00 "Example Silicon [EXS]" "Example Root Complex"
0000:c1:00.0 "SATA controller" "Example Bridge Co." "EXB58x AHCI SATA controller" -p01 "Example Bridge Co." "Device 0000"
0000:c2:00.0 "Non-Volatile memory controller" "Example Flash Corp" "Example NVMe SSD Controller" -p02 "Example Flash Corp" "Example NVMe SSD Controller"
0000:c4:00.0 "Ethernet controller" "Example Networks Corp." "EXN113 NBase-T 10G Ethernet Controller" -r03 -p00 "Example Networks Corp." "Device 0001"
0000:c5:00.0 "Ethernet controller" "Example Semiconductor Co., Ltd." "EX8126 5GbE Controller" -r01 -p00 "Example Semiconductor Co., Ltd." "Device 0123"
0000:c7:00.0 "Display controller" "Example Silicon [EXS/GFX]" "Example iGPU [Radeon-class 890M]" -rd1 -p00 "Unknown vendor 1f4c" "Device b020"
0000:c8:00.1 "Signal processing controller" "Example Silicon [EXS]" "Example Neural Processing Unit" -r10 -p00 "Example Silicon [EXS]" "Example Neural Processing Unit"
0000:c9:00.0 "USB controller" "Example Silicon [EXS]" "Device 151f" -p30 "Example Silicon [EXS]" "Device 15b9"
`

func TestParseLspciMM(t *testing.T) {
	m := ParseLspciMM(lspciSample)
	if len(m) != 8 {
		t.Fatalf("parsed %d slots, want 8", len(m))
	}
	if g := m["0000:c7:00.0"]; g.Class != "Display controller" || g.Vendor != "Example Silicon [EXS/GFX]" || g.Device != "Example iGPU [Radeon-class 890M]" {
		t.Errorf("gpu = %+v", g)
	}
	if n := m["0000:c4:00.0"]; n.Device != "EXN113 NBase-T 10G Ethernet Controller" {
		t.Errorf("nic = %+v", n)
	}
	// Without -D the slot has no domain; findPCI matches either form.
	m = ParseLspciMM(`c7:00.0 "Display controller" "V" "D \"quoted\" name" -p00 "" ""` + "\nshort\n\n")
	if d := m["c7:00.0"]; d.Device != `D "quoted" name` || d.Vendor != "V" {
		t.Errorf("escaped quote: %+v", d)
	}
	devs := []pciDevice{{Addr: "0000:c7:00.0"}}
	if findPCI(devs, "c7:00.0") == nil || findPCI(devs, "0000:c7:00.0") == nil || findPCI(devs, "") != nil || findPCI(devs, "0000:c7:00.1") != nil {
		t.Error("findPCI domain handling")
	}
	if shortVendor("Advanced Micro Devices, Inc. [AMD/ATI]") != "AMD/ATI" || shortVendor("Plain Vendor") != "Plain Vendor" {
		t.Error("shortVendor")
	}
}

// pciFixtures are the PCI devices of the fake tree (class as sysfs prints it).
var pciFixtures = []struct{ addr, class, vendor, device, driver string }{
	{"0000:00:00.0", "0x060000", "0x1022", "0x1507", ""},
	{"0000:c1:00.0", "0x010601", "0x197b", "0x0585", "ahci"},
	{"0000:c2:00.0", "0x010802", "0x15b7", "0x5006", "nvme"},
	{"0000:c4:00.0", "0x020000", "0x1d6a", "0x04c0", "atlantic"},
	{"0000:c5:00.0", "0x020000", "0x10ec", "0x8126", "r8169"},
	{"0000:c7:00.0", "0x038000", "0x1002", "0x150e", "amdgpu"},
	{"0000:c8:00.1", "0x118000", "0x1022", "0x17f0", "amdxdna"},
	{"0000:c9:00.0", "0x0c0330", "0x1022", "0x151f", "xhci_hcd"},
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// fakeTree copies the fixture tree and adds what the checkout cannot hold.
func fakeTree(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture needs symlinks and colons in names")
	}
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	for _, d := range pciFixtures {
		dir := filepath.Join(root, "bus", "pci", "devices", d.addr)
		writeFile(t, filepath.Join(dir, "class"), d.class+"\n")
		writeFile(t, filepath.Join(dir, "vendor"), d.vendor+"\n")
		writeFile(t, filepath.Join(dir, "device"), d.device+"\n")
		if d.driver != "" {
			symlink(t, "../../../../bus/pci/drivers/"+d.driver, filepath.Join(dir, "driver"))
		}
	}
	pci := "../../../bus/pci/devices/"
	symlink(t, pci+"0000:c5:00.0", filepath.Join(root, "class", "net", "nic0", "device"))
	symlink(t, pci+"0000:c4:00.0", filepath.Join(root, "class", "net", "nic1", "device"))
	symlink(t, pci+"0000:c8:00.1", filepath.Join(root, "class", "accel", "accel0", "device"))
	symlink(t, pci+"0000:c7:00.0", filepath.Join(root, "class", "drm", "card1", "device"))
	// A SATA disk: /sys/block/sda is a link into the ata host path.
	sda := filepath.Join(root, "devices", "pci0000:00", "0000:00:02.1", "0000:c1:00.0", "ata1", "host0", "target0:0:0", "0:0:0:0", "block", "sda")
	writeFile(t, filepath.Join(sda, "size"), "39063650304\n")
	writeFile(t, filepath.Join(sda, "queue", "rotational"), "1\n")
	writeFile(t, filepath.Join(sda, "device", "model"), "Example HDD 20TB\n")
	symlink(t, "../devices/pci0000:00/0000:00:02.1/0000:c1:00.0/ata1/host0/target0:0:0/0:0:0:0/block/sda", filepath.Join(root, "block", "sda"))
	// Fan controller: hwmon14 → platform device → driver → module.
	symlink(t, "../../../devices/platform/minisforum_n5_it5571", filepath.Join(root, "class", "hwmon", "hwmon14", "device"))
	symlink(t, "../../../bus/platform/drivers/minisforum_n5_it5571", filepath.Join(root, "devices", "platform", "minisforum_n5_it5571", "driver"))
	symlink(t, "../../../../module/minisforum_n5_it5571", filepath.Join(root, "bus", "platform", "drivers", "minisforum_n5_it5571", "module"))
	// SMBIOS tables.
	writeFile(t, filepath.Join(root, "firmware", "dmi", "tables", "DMI"), string(syntheticSMBIOS()))
	writeFile(t, filepath.Join(root, "firmware", "dmi", "tables", "smbios_entry_point"), string(syntheticEntryPoint()))
	return root
}

// fakeLspci writes a script that prints lspciSample.
func fakeLspci(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lspci")
	if err := os.WriteFile(p, []byte("#!/bin/sh\ncat <<'EOF'\n"+lspciSample+"EOF\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func testOptions(t *testing.T) Options {
	t.Helper()
	td := filepath.Join(hwmontest.ModuleRoot(t), "internal", "sysinfo", "testdata")
	return Options{
		Sysfs:     fakeTree(t),
		Proc:      filepath.Join(td, "proc"),
		OSRelease: filepath.Join(td, "os-release"),
		LSPCI:     fakeLspci(t),
		Hostname:  func() (string, error) { return "n5host", nil },
		Fan:       FanController{Profile: "n5pro", Hwmon: "class/hwmon/hwmon14"},
	}
}

func TestCollect(t *testing.T) {
	in := Collect(testOptions(t))
	if len(in.Errors) != 0 {
		t.Errorf("errors: %v", in.Errors)
	}
	// Host
	if in.Host.Hostname != "n5host" || in.Host.OS != "Debian GNU/Linux 13 (trixie)" || in.Host.Kernel != "7.0.12-1-pve" || in.Host.UptimeS != 1518590 || in.Host.Load1 != 0.64 || in.Host.Load15 != 0.66 {
		t.Errorf("host = %+v", in.Host)
	}
	// Machine
	if in.Machine != (Machine{Vendor: "Example Vendor", Product: "N5-class mini server", Board: "EXB-01", BoardVendor: "Example Boards Ltd", BIOSVersion: "1.05", BIOSDate: "03/31/2026"}) {
		t.Errorf("machine = %+v", in.Machine)
	}
	// CPU: 4 processors, 2 cores, 1 socket, 5157895 kHz
	if in.CPU != (CPU{Model: "Example Ryzen-class 2-core APU", Sockets: 1, Cores: 2, Threads: 4, MaxMHz: 5157}) {
		t.Errorf("cpu = %+v", in.CPU)
	}
	// Memory: live from meminfo, modules from SMBIOS
	m := in.Memory
	if m.TotalBytes != 65443408*1024 || m.AvailableBytes != 36733444*1024 || m.SwapTotalBytes != 8388604*1024 || m.SwapFreeBytes != 8388604*1024 {
		t.Errorf("meminfo = %+v", m)
	}
	if m.SMBIOS != "3.7" || len(m.Modules) != 2 || m.InstalledBytes != 64<<30 || m.Modules[0].Part != "EX-DDR5-48G" || !m.Modules[0].ECC {
		t.Errorf("modules = %+v", m)
	}
	// GPU
	if len(in.GPUs) != 1 || in.GPUs[0] != (GPU{Name: "Example iGPU [Radeon-class 890M]", Vendor: "EXS/GFX", PCI: "0000:c7:00.0", Driver: "amdgpu"}) {
		t.Errorf("gpus = %+v", in.GPUs)
	}
	// NPU
	if len(in.NPUs) != 1 || in.NPUs[0] != (NPU{Name: "Example Neural Processing Unit", PCI: "0000:c8:00.1", Driver: "amdxdna", DriverVersion: "2.23.0_20260412,0000000000000000000000000000000000000000", Accel: "accel0"}) {
		t.Errorf("npus = %+v", in.NPUs)
	}
	// NICs: physical only, sorted, link state live
	if len(in.NICs) != 2 {
		t.Fatalf("nics = %+v", in.NICs)
	}
	if in.NICs[0] != (NIC{Name: "nic0", PCI: "0000:c5:00.0", Model: "EX8126 5GbE Controller", Driver: "r8169", SpeedMbit: -1, State: "down", Duplex: "", MAC: "02:00:00:00:00:01", MTU: 1500}) {
		t.Errorf("nic0 = %+v", in.NICs[0])
	}
	if in.NICs[1] != (NIC{Name: "nic1", PCI: "0000:c4:00.0", Model: "EXN113 NBase-T 10G Ethernet Controller", Driver: "atlantic", SpeedMbit: 2500, State: "up", Duplex: "full", MAC: "02:00:00:00:00:02", MTU: 9000}) {
		t.Errorf("nic1 = %+v", in.NICs[1])
	}
	// Storage
	if len(in.Storage.Controllers) != 2 || in.Storage.Controllers[0].Kind != "sata" || in.Storage.Controllers[0].Driver != "ahci" || in.Storage.Controllers[1].Kind != "nvme" || in.Storage.Controllers[1].Name != "Example NVMe SSD Controller" {
		t.Errorf("controllers = %+v", in.Storage.Controllers)
	}
	if len(in.Storage.Disks) != 2 {
		t.Fatalf("disks = %+v (zd/loop/dm must be skipped)", in.Storage.Disks)
	}
	if in.Storage.Disks[0] != (Disk{Name: "nvme0n1", Model: "Example NVMe 1000GB", SizeBytes: 1953525168 * 512, Rotational: false, Transport: "nvme"}) {
		t.Errorf("nvme0n1 = %+v", in.Storage.Disks[0])
	}
	if in.Storage.Disks[1] != (Disk{Name: "sda", Model: "Example HDD 20TB", SizeBytes: 39063650304 * 512, Rotational: true, Transport: "sata"}) {
		t.Errorf("sda = %+v", in.Storage.Disks[1])
	}
	// Fan controller module via the driver link
	if in.FanController != (FanController{Profile: "n5pro", Hwmon: "class/hwmon/hwmon14", Module: "minisforum_n5_it5571", ModuleVersion: "0.2.0"}) {
		t.Errorf("fan = %+v", in.FanController)
	}
	if in.Collected == 0 || in.StaticAt == 0 {
		t.Error("timestamps missing")
	}
	// JSON: snake_case keys, no nulls except a disk's temp_c without a
	// sensor (the fixture has no block hwmons; TestDiskTemperatures adds them).
	b, _ := json.Marshal(in)
	for _, k := range []string{`"fan_controller"`, `"size_bytes"`, `"speed_mbit"`, `"max_mhz"`, `"driver_version"`, `"installed_bytes"`, `"errors":[]`, `"temp_c":null`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("json lacks %s", k)
		}
	}
	if s := strings.ReplaceAll(string(b), `"temp_c":null`, ""); strings.Contains(s, "null") {
		t.Errorf("json has null: %s", b)
	}
	if strings.Contains(string(b), `"sensor"`) {
		t.Errorf("sensor id must be omitted without a temperature: %s", b)
	}
}

// TestDiskTemperatures: every disk carries the live reading of the hwmon
// the disk:<dev> sensor uses (SATA: device/hwmon/hwmonN, NVMe:
// device/hwmonN), re-read on every Collect without touching the cache; a
// disk without a hwmon stays at null.
func TestDiskTemperatures(t *testing.T) {
	o := testOptions(t)
	sda := filepath.Join(o.Sysfs, "devices", "pci0000:00", "0000:00:02.1", "0000:c1:00.0", "ata1", "host0", "target0:0:0", "0:0:0:0", "block", "sda")
	writeFile(t, filepath.Join(sda, "device", "hwmon", "hwmon6", "name"), "drivetemp\n")
	writeFile(t, filepath.Join(sda, "device", "hwmon", "hwmon6", "temp1_input"), "38000\n")
	nvmeTemp := filepath.Join(o.Sysfs, "block", "nvme0n1", "device", "hwmon3", "temp1_input")
	writeFile(t, filepath.Join(o.Sysfs, "block", "nvme0n1", "device", "hwmon3", "name"), "nvme\n")
	writeFile(t, nvmeTemp, "43850\n")
	// a third disk without a sensor
	writeFile(t, filepath.Join(o.Sysfs, "block", "sdb", "device", "model"), "Example SSD\n")
	writeFile(t, filepath.Join(o.Sysfs, "block", "sdb", "size"), "1000\n")
	c := NewCollector(o)
	in := c.Collect()
	if len(in.Storage.Disks) != 3 {
		t.Fatalf("disks = %+v", in.Storage.Disks)
	}
	want := map[string]float64{"nvme0n1": 43.85, "sda": 38}
	for _, d := range in.Storage.Disks {
		w, ok := want[d.Name]
		switch {
		case !ok:
			if d.TempC != nil || d.Sensor != "" {
				t.Errorf("%s: temperature without a hwmon: %+v", d.Name, d)
			}
		case d.TempC == nil || *d.TempC != w || d.Sensor != "disk:"+d.Name:
			t.Errorf("%s: temp %v sensor %q, want %v disk:%s", d.Name, d.TempC, d.Sensor, w, d.Name)
		}
	}
	// live: a new reading shows on the next Collect from the cached static part
	writeFile(t, nvmeTemp, "50000\n")
	in2 := c.Collect()
	if in2.StaticAt != in.StaticAt {
		t.Fatal("static part must come from the cache")
	}
	if d := in2.Storage.Disks[0]; d.Name != "nvme0n1" || d.TempC == nil || *d.TempC != 50 {
		t.Errorf("second collect: %+v", d)
	}
	// the first result is untouched (no aliasing into the cache)
	if *in.Storage.Disks[0].TempC != 43.85 {
		t.Errorf("first result changed: %v", *in.Storage.Disks[0].TempC)
	}
	// an implausible reading counts as no sensor
	writeFile(t, nvmeTemp, "250000\n")
	if d := c.Collect().Storage.Disks[0]; d.TempC != nil || d.Sensor != "" {
		t.Errorf("implausible reading kept: %+v", d)
	}
	b, _ := json.Marshal(in)
	if !strings.Contains(string(b), `"temp_c":38,"sensor":"disk:sda"`) {
		t.Errorf("json: %s", b)
	}
}

// TestCollectDegraded: no lspci, no SMBIOS, no cpufreq, empty /proc — every
// section stays usable and each failure is one Errors line.
func TestCollectDegraded(t *testing.T) {
	o := testOptions(t)
	o.LSPCI = "-"
	os.RemoveAll(filepath.Join(o.Sysfs, "firmware"))
	os.RemoveAll(filepath.Join(o.Sysfs, "devices", "system"))
	in := Collect(o)
	if len(in.GPUs) != 1 || in.GPUs[0].Name != "PCI device 1002:150e" || in.GPUs[0].Vendor != "AMD" {
		t.Errorf("gpu without lspci = %+v", in.GPUs)
	}
	if len(in.NPUs) != 1 || in.NPUs[0].Accel != "accel0" || in.NPUs[0].Driver != "amdxdna" {
		t.Errorf("npu without lspci = %+v", in.NPUs)
	}
	if len(in.NICs) != 2 || in.NICs[1].Model != "PCI device 1d6a:04c0" {
		t.Errorf("nics without lspci = %+v", in.NICs)
	}
	if in.CPU.MaxMHz != 0 || in.CPU.Threads != 4 {
		t.Errorf("cpu = %+v", in.CPU)
	}
	if len(in.Memory.Modules) != 0 || in.Memory.InstalledBytes != 0 || in.Memory.TotalBytes == 0 {
		t.Errorf("memory = %+v", in.Memory)
	}
	if len(in.Errors) != 1 || !strings.HasPrefix(in.Errors[0], "smbios:") {
		t.Errorf("errors = %v", in.Errors)
	}
	// A missing lspci binary is an error line, not a failure.
	o.LSPCI = filepath.Join(t.TempDir(), "nope")
	in = Collect(o)
	if len(in.Errors) != 2 || !strings.HasPrefix(in.Errors[1], "lspci:") {
		t.Errorf("errors = %v", in.Errors)
	}
	// Empty proc: host and memory errors, nothing panics.
	o.Proc = t.TempDir()
	o.OSRelease = filepath.Join(o.Proc, "none")
	in = Collect(o)
	for _, want := range []string{"os-release:", "cpuinfo:", "uptime:", "loadavg:", "meminfo:"} {
		found := false
		for _, e := range in.Errors {
			found = found || strings.HasPrefix(e, want)
		}
		if !found {
			t.Errorf("errors lack %s: %v", want, in.Errors)
		}
	}
	if in.Errors == nil {
		t.Error("errors nil")
	}
}

// TestCollectorCache: static parts are reused for CacheTTL, live parts are
// read every time; Invalidate forces a re-read.
func TestCollectorCache(t *testing.T) {
	o := testOptions(t)
	now := time.Unix(1789500000, 0)
	o.Now = func() time.Time { return now }
	c := NewCollector(o)
	first := c.Collect()
	if first.StaticAt != now.Unix() || first.Collected != now.Unix() {
		t.Fatalf("stamps %d %d", first.StaticAt, first.Collected)
	}
	// Change a static and a live file.
	writeFile(t, filepath.Join(o.Sysfs, "class", "dmi", "id", "bios_version"), "1.06\n")
	writeFile(t, filepath.Join(o.Sysfs, "class", "net", "nic1", "speed"), "10000\n")
	now = now.Add(time.Minute)
	second := c.Collect()
	if second.Machine.BIOSVersion != "1.05" || second.StaticAt != first.StaticAt {
		t.Errorf("static re-read within TTL: %+v", second.Machine)
	}
	if second.NICs[1].SpeedMbit != 10000 || second.Collected != now.Unix() {
		t.Errorf("live not refreshed: %+v", second.NICs[1])
	}
	if first.NICs[1].SpeedMbit != 2500 {
		t.Errorf("cached copy modified: %+v", first.NICs[1])
	}
	now = now.Add(DefaultCacheTTL)
	third := c.Collect()
	if third.Machine.BIOSVersion != "1.06" || third.StaticAt != now.Unix() {
		t.Errorf("static not re-read after TTL: %+v", third.Machine)
	}
	writeFile(t, filepath.Join(o.Sysfs, "class", "dmi", "id", "bios_version"), "1.07\n")
	c.Invalidate()
	if c.Collect().Machine.BIOSVersion != "1.07" {
		t.Error("Invalidate did not force a re-read")
	}
}

func TestNewCollectorDefaults(t *testing.T) {
	t.Setenv("N5FANGOV_SYSFS", "/tmp/fake-sys")
	c := NewCollector(Options{})
	if c.o.Sysfs != "/tmp/fake-sys" || c.o.Proc != "/proc" || c.o.OSRelease != "/etc/os-release" || c.o.CacheTTL != DefaultCacheTTL || c.o.Now == nil || c.o.Hostname == nil {
		t.Errorf("defaults: %+v", c.o)
	}
	t.Setenv("N5FANGOV_SYSFS", "")
	if NewCollector(Options{}).o.Sysfs != "/sys" {
		t.Error("sysfs default")
	}
	if transportOf("nvme1n1", "/x") != "nvme" || transportOf("sdb", "/sys/devices/pci0000:00/0000:c9:00.3/usb1/1-2/1-2:1.0/host6/target6:0:0/6:0:0:0/block/sdb") != "usb" || transportOf("sdc", "/sys/devices/x/host0/target0:0:0/0:0:0:0/block/sdc") != "scsi" {
		t.Error("transportOf")
	}
	if !hwmon.VirtualBlock("zd16") || !hwmon.VirtualBlock("dm-3") || hwmon.VirtualBlock("sdz") || hwmon.VirtualBlock("nvme0n1") {
		t.Error("VirtualBlock")
	}
	if !errors.Is(errNoMemoryDevice, errNoMemoryDevice) {
		t.Error("sentinel")
	}
}

// TestNICsNeverNull: a box without physical interfaces serialises
// "nics": [] (the dashboard iterates it).
func TestNICsNeverNull(t *testing.T) {
	o := testOptions(t)
	o.LSPCI = "-"
	if err := os.RemoveAll(filepath.Join(o.Sysfs, "class", "net")); err != nil {
		t.Fatal(err)
	}
	in := Collect(o)
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if in.NICs == nil || !strings.Contains(string(b), `"nics":[]`) {
		t.Errorf("nics = %#v / %s", in.NICs, string(b))
	}
}

// TestLspciFailureRetried: a failed lspci run does not pin nameless PCI
// devices for CacheTTL; after LspciRetry the names are read again.
func TestLspciFailureRetried(t *testing.T) {
	o := testOptions(t)
	now := time.Unix(1789500000, 0)
	o.Now = func() time.Time { return now }
	good := o.LSPCI
	o.LSPCI = filepath.Join(t.TempDir(), "nope")
	c := NewCollector(o)
	first := c.Collect()
	if !hasPrefix(first.Errors, "lspci:") || first.GPUs[0].Name != "PCI device 1002:150e" {
		t.Fatalf("first = %v / %+v", first.Errors, first.GPUs)
	}
	c.o.LSPCI = good
	now = now.Add(LspciRetry - time.Second)
	if in := c.Collect(); !hasPrefix(in.Errors, "lspci:") {
		t.Errorf("retried before LspciRetry: %v", in.Errors)
	}
	now = now.Add(2 * time.Second)
	in := c.Collect()
	if hasPrefix(in.Errors, "lspci:") || in.GPUs[0].Name == "PCI device 1002:150e" || in.StaticAt != now.Unix() {
		t.Errorf("not retried after LspciRetry: %v / %+v", in.Errors, in.GPUs)
	}
	// a good result is then cached for CacheTTL again
	c.o.LSPCI = filepath.Join(t.TempDir(), "nope")
	now = now.Add(LspciRetry + time.Second)
	if in := c.Collect(); hasPrefix(in.Errors, "lspci:") {
		t.Errorf("good static part re-read before CacheTTL: %v", in.Errors)
	}
}

func hasPrefix(list []string, p string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// TestCollectNotBlockedByRefresh: while one Collect gathers the static
// part, a second one is served from the previous static part instead of
// waiting; the very first collection is waited for.
func TestCollectNotBlockedByRefresh(t *testing.T) {
	o := testOptions(t)
	now := time.Unix(1789500000, 0)
	var mu sync.Mutex
	o.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	// an lspci that blocks until the gate file appears
	lspci := filepath.Join(t.TempDir(), "lspci")
	fifo := filepath.Join(t.TempDir(), "gate")
	if err := os.WriteFile(lspci, []byte("#!/bin/sh\necho started >> \""+fifo+".started\"\nwhile [ ! -e \""+fifo+"\" ]; do sleep 0.05; done\necho '00:00.0 \"Host bridge\" \"Example\" \"Bridge\"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := NewCollector(o)
	if in := c.Collect(); in.StaticAt == 0 { // first collection with the default lspci fixture
		t.Fatal("first collection missing")
	}
	// second static collection blocks in lspci; a concurrent Collect must
	// return the cached part meanwhile
	c.o.LSPCI = lspci
	mu.Lock()
	now = now.Add(DefaultCacheTTL + time.Second)
	mu.Unlock()
	done := make(chan Info, 1)
	go func() { done <- c.Collect() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(fifo + ".started"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("blocking lspci never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	fast := make(chan Info, 1)
	go func() { fast <- c.Collect() }()
	select {
	case in := <-fast:
		if in.StaticAt != 1789500000 {
			t.Errorf("concurrent Collect got a static part from %d, want the cached one", in.StaticAt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent Collect blocked behind the refresh")
	}
	if err := os.WriteFile(fifo, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-done:
		if in.StaticAt != now.Unix() {
			t.Errorf("refresh result StaticAt = %d, want %d", in.StaticAt, now.Unix())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never finished")
	}
}
