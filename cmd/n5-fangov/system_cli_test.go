package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/sysinfo"
)

func TestPrintSystem(t *testing.T) {
	in := sysinfo.Info{
		Host:    sysinfo.Host{Hostname: "n5host", OS: "Debian GNU/Linux 13 (trixie)", Kernel: "7.0.12-1-pve", UptimeS: 90061, Load1: 0.64, Load5: 0.7, Load15: 0.66},
		Machine: sysinfo.Machine{Vendor: "Example Vendor", Product: "N5-class mini server", Board: "EXB-01", BoardVendor: "Example Boards", BIOSVersion: "1.05", BIOSDate: "03/31/2026"},
		CPU:     sysinfo.CPU{Model: "Example 12-core APU", Sockets: 1, Cores: 12, Threads: 24, MaxMHz: 5157},
		Memory: sysinfo.Memory{TotalBytes: 64 << 30, AvailableBytes: 40 << 30, SwapTotalBytes: 8 << 30, SwapFreeBytes: 8 << 30, InstalledBytes: 96 << 30, SMBIOS: "3.7",
			Modules: []sysinfo.MemoryModule{{Slot: "DIMM 0", Bank: "P0 CHANNEL A", SizeBytes: 48 << 30, Type: "DDR5", FormFactor: "SODIMM", SpeedMTs: 5600, Manufacturer: "Example Memory", Part: "EX-DDR5-48G", ECC: true}}},
		GPUs: []sysinfo.GPU{{Name: "Example iGPU", Vendor: "EXS", PCI: "0000:c7:00.0", Driver: "amdgpu"}},
		NPUs: []sysinfo.NPU{{Name: "Example NPU", PCI: "0000:c8:00.1", Driver: "amdxdna", DriverVersion: "2.23.0", Accel: "accel0"}},
		NICs: []sysinfo.NIC{{Name: "nic0", PCI: "0000:c5:00.0", Model: "EX 5GbE", Driver: "r8169", SpeedMbit: -1, State: "down", MAC: "02:00:00:00:00:01", MTU: 1500},
			{Name: "nic1", PCI: "0000:c4:00.0", Model: "EX 10G", Driver: "atlantic", SpeedMbit: 2500, State: "up", Duplex: "full", MAC: "02:00:00:00:00:02", MTU: 1500}},
		Storage: sysinfo.Storage{Controllers: []sysinfo.Controller{{Kind: "nvme", Name: "Example NVMe controller", PCI: "0000:c2:00.0", Driver: "nvme"}},
			Disks: []sysinfo.Disk{{Name: "nvme0n1", Model: "Example NVMe 1000GB", SizeBytes: 1000204886016, Transport: "nvme"}, {Name: "sda", Model: "Example HDD 20TB", SizeBytes: 20000588955648, Rotational: true, Transport: "sata"}}},
		FanController: sysinfo.FanController{Profile: "n5pro", Hwmon: "/sys/class/hwmon/hwmon14", Module: "minisforum_n5_it5571", ModuleVersion: "0.2.0"},
		Errors:        []string{"lspci: not found (device names from ids only)"},
	}
	var b bytes.Buffer
	printSystem(&b, in, "daemon")
	out := b.String()
	for _, want := range []string{
		"host:      n5host · Debian GNU/Linux 13 (trixie) · kernel 7.0.12-1-pve · up 1d01h01m · load 0.64 0.70 0.66",
		"machine:   Example Vendor N5-class mini server · board EXB-01 (Example Boards) · BIOS 1.05 (03/31/2026)",
		"cpu:       Example 12-core APU · 1 socket(s), 12 cores, 24 threads · max 5157 MHz",
		"memory:    used 24.0 GiB of 64.0 GiB (37 %) · swap 0 B of 8.0 GiB · installed 96.0 GiB (1 module(s), SMBIOS 3.7)",
		"DIMM 0   P0 CHANNEL A   48.0 GiB  DDR5 SODIMM 5600 MT/s ECC  Example Memory EX-DDR5-48G",
		"gpu:       Example iGPU (EXS, 0000:c7:00.0, driver amdgpu)",
		"npu:       Example NPU (0000:c8:00.1, driver amdxdna 2.23.0, accel0)",
		"nic0     down ", "nic1     up 2.5 Gbit/s full", "02:00:00:00:00:02  mtu 1500  0000:c4:00.0",
		"nvme  Example NVMe controller", "nvme0n1  931.5 GiB  SSD  nvme   Example NVMe 1000GB", "sda       18.2 TiB  HDD  sata   Example HDD 20TB",
		"2 disk(s), 19.1 TiB",
		"fan ctl:   profile n5pro at /sys/class/hwmon/hwmon14 · module minisforum_n5_it5571 0.2.0",
		"note:      lspci: not found", "(source: daemon)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// Empty inventory: nothing panics, placeholders appear.
	b.Reset()
	printSystem(&b, sysinfo.Info{}, "collected locally (daemon not running)")
	if out := b.String(); !strings.Contains(out, "gpu/npu:   none found") || !strings.Contains(out, "no physical interfaces") || !strings.Contains(out, "0 disk(s), 0 B") || strings.Contains(out, "fan ctl") {
		t.Errorf("empty output:\n%s", out)
	}
}

func TestFmtBytesMbit(t *testing.T) {
	for _, c := range []struct {
		b    int64
		want string
	}{{0, "0 B"}, {1023, "1023 B"}, {1536, "2 KiB"}, {5 << 20, "5 MiB"}, {48 << 30, "48.0 GiB"}, {20000588955648, "18.2 TiB"}} {
		if got := fmtBytes(c.b); got != c.want {
			t.Errorf("fmtBytes(%d) = %q, want %q", c.b, got, c.want)
		}
	}
	for _, c := range []struct {
		m    int
		want string
	}{{100, "100 Mbit/s"}, {1000, "1 Gbit/s"}, {2500, "2.5 Gbit/s"}, {10000, "10 Gbit/s"}} {
		if got := fmtMbit(c.m); got != c.want {
			t.Errorf("fmtMbit(%d) = %q, want %q", c.m, got, c.want)
		}
	}
	if usage := helpText["system"]; !strings.Contains(usage, "--json") {
		t.Errorf("usage line: %q", usage)
	}
	if _, ok := commands["system"]; !ok {
		t.Error("system not registered")
	}
}
