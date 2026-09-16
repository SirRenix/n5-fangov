package sysinfo

import (
	"encoding/binary"
	"testing"
)

// smbiosBuilder assembles a synthetic structure table.
type smbiosBuilder struct{ b []byte }

// add appends one structure: formatted area (type/length/handle filled
// in) followed by its string set. Values past the given length are
// dropped, so a shorter record tests version-dependent field handling.
func (sb *smbiosBuilder) add(typ byte, length int, handle uint16, fields map[int][]byte, strs ...string) {
	rec := make([]byte, length)
	rec[0], rec[1] = typ, byte(length)
	binary.LittleEndian.PutUint16(rec[2:], handle)
	for off, v := range fields {
		if off+len(v) <= length {
			copy(rec[off:], v)
		}
	}
	sb.b = append(sb.b, rec...)
	if len(strs) == 0 {
		sb.b = append(sb.b, 0, 0)
		return
	}
	for _, s := range strs {
		sb.b = append(sb.b, s...)
		sb.b = append(sb.b, 0)
	}
	sb.b = append(sb.b, 0)
}

func u16(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }

// syntheticSMBIOS: two arrays (ECC and non-ECC), three memory devices —
// a 48 GB DDR5 SODIMM via the extended size field (SMBIOS 2.8 record), a
// 16 GB DDR4 DIMM in a 2.6-length record (no extended size, no
// configured speed) and an empty slot — plus the end-of-table marker.
func syntheticSMBIOS() []byte {
	var sb smbiosBuilder
	sb.add(16, 0x17, 0x1000, map[int][]byte{0x04: {0x03}, 0x05: {0x03}, 0x06: {0x06}, 0x0D: u16(2)})
	sb.add(16, 0x17, 0x1001, map[int][]byte{0x04: {0x03}, 0x05: {0x03}, 0x06: {0x03}, 0x0D: u16(1)})
	sb.add(17, 0x28, 0x1100, map[int][]byte{
		0x04: u16(0x1000), 0x06: u16(0xFFFE), 0x08: u16(72), 0x0A: u16(64), 0x0C: u16(0x7FFF),
		0x0E: {0x0D}, 0x10: {1}, 0x11: {2}, 0x12: {0x22}, 0x15: u16(5600), 0x17: {3}, 0x18: {4}, 0x1A: {5},
		0x1C: u32(49152), 0x20: u16(5200),
	}, "DIMM 0", "P0 CHANNEL A", "Example Memory", "00000000", "EX-DDR5-48G              ")
	sb.add(17, 0x1C, 0x1101, map[int][]byte{
		0x04: u16(0x1001), 0x06: u16(0xFFFE), 0x08: u16(64), 0x0A: u16(64), 0x0C: u16(16384),
		0x0E: {0x09}, 0x10: {1}, 0x11: {2}, 0x12: {0x1A}, 0x15: u16(3200), 0x17: {3}, 0x1A: {4},
	}, "DIMM 1", "P0 CHANNEL B", "Unknown", "EX-DDR4-16G")
	sb.add(17, 0x28, 0x1102, map[int][]byte{0x04: u16(0x1000), 0x0C: u16(0), 0x10: {1}}, "DIMM 2")
	sb.add(127, 4, 0x7F00, nil)
	return sb.b
}

func syntheticEntryPoint() []byte {
	ep := make([]byte, 24)
	copy(ep, "_SM3_")
	ep[6], ep[7], ep[8], ep[10] = 0x18, 3, 7, 1
	binary.LittleEndian.PutUint32(ep[12:], 2215)
	return ep
}

func TestParseMemoryModules(t *testing.T) {
	mods, err := ParseMemoryModules(syntheticSMBIOS())
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 2 {
		t.Fatalf("got %d modules, want 2 (empty slot skipped): %+v", len(mods), mods)
	}
	a, b := mods[0], mods[1]
	want := MemoryModule{Slot: "DIMM 0", Bank: "P0 CHANNEL A", SizeBytes: 48 << 30, Type: "DDR5", FormFactor: "SODIMM", SpeedMTs: 5200, Manufacturer: "Example Memory", Part: "EX-DDR5-48G", ECC: true}
	if a != want {
		t.Errorf("module A = %+v\nwant       %+v", a, want)
	}
	want = MemoryModule{Slot: "DIMM 1", Bank: "P0 CHANNEL B", SizeBytes: 16 << 30, Type: "DDR4", FormFactor: "DIMM", SpeedMTs: 3200, Manufacturer: "", Part: "EX-DDR4-16G", ECC: false}
	if b != want {
		t.Errorf("module B = %+v\nwant       %+v", b, want)
	}
}

func TestParseMemoryModulesEdgeCases(t *testing.T) {
	if _, err := ParseMemoryModules(nil); err == nil {
		t.Error("empty table: want an error")
	}
	var sb smbiosBuilder
	sb.add(0, 0x18, 1, nil, "Example BIOS", "1.05")
	sb.add(127, 4, 2, nil)
	if _, err := ParseMemoryModules(sb.b); err != errNoMemoryDevice {
		t.Errorf("no type 17: err = %v", err)
	}
	// KB-unit size and an unknown memory type code.
	sb = smbiosBuilder{}
	sb.add(17, 0x1C, 1, map[int][]byte{0x0C: u16(0x8000 | 512), 0x12: {0x7E}, 0x15: u16(0xFFFF)})
	mods, err := ParseMemoryModules(sb.b)
	if err != nil || len(mods) != 1 || mods[0].SizeBytes != 512<<10 || mods[0].Type != "type 0x7e" || mods[0].SpeedMTs != 0 {
		t.Errorf("KB module: %+v, %v", mods, err)
	}
	// Truncated table: the parsed part is returned with the error.
	full := syntheticSMBIOS()
	mods, err = ParseMemoryModules(full[:len(full)-20])
	if err == nil || len(mods) == 0 {
		t.Errorf("truncated: mods=%d err=%v", len(mods), err)
	}
}

func TestSMBIOSVersion(t *testing.T) {
	if v := SMBIOSVersion(syntheticEntryPoint()); v != "3.7" {
		t.Errorf("v3 entry point: %q", v)
	}
	ep := make([]byte, 31)
	copy(ep, "_SM_")
	ep[6], ep[7] = 2, 8
	if v := SMBIOSVersion(ep); v != "2.8" {
		t.Errorf("v2 entry point: %q", v)
	}
	if v := SMBIOSVersion([]byte("junk")); v != "" {
		t.Errorf("junk: %q", v)
	}
}

// TestMemoryECCOnlyCorrecting: error correction 0x07 (CRC) is detection,
// not correction; 0x05/0x06 are ECC.
func TestMemoryECCOnlyCorrecting(t *testing.T) {
	for code, want := range map[byte]bool{0x03: false, 0x04: false, 0x05: true, 0x06: true, 0x07: false} {
		var sb smbiosBuilder
		sb.add(16, 0x17, 0x1000, map[int][]byte{0x04: {0x03}, 0x05: {0x03}, 0x06: {code}, 0x0D: u16(1)})
		sb.add(17, 0x1C, 0x1100, map[int][]byte{0x04: u16(0x1000), 0x08: u16(64), 0x0A: u16(64), 0x0C: u16(8192), 0x12: {0x1A}, 0x15: u16(3200)})
		sb.add(127, 4, 0x7F00, nil)
		mods, err := ParseMemoryModules(sb.b)
		if err != nil || len(mods) != 1 {
			t.Fatalf("code %#x: %v %+v", code, err, mods)
		}
		if mods[0].ECC != want {
			t.Errorf("error correction %#x: ECC=%v, want %v", code, mods[0].ECC, want)
		}
	}
}

// TestMemorySpeedExtendedConfigured: a configured speed of 0xFFFF points
// at the extended configured speed (0x58), which wins over a plain
// nominal speed (0x15).
func TestMemorySpeedExtendedConfigured(t *testing.T) {
	var sb smbiosBuilder
	sb.add(17, 0x5C, 0x1100, map[int][]byte{0x0C: u16(8192), 0x12: {0x22}, 0x15: u16(6400), 0x20: u16(0xFFFF), 0x54: u32(0), 0x58: u32(70000)})
	sb.add(127, 4, 0x7F00, nil)
	mods, err := ParseMemoryModules(sb.b)
	if err != nil || len(mods) != 1 {
		t.Fatalf("%v %+v", err, mods)
	}
	if mods[0].SpeedMTs != 70000 {
		t.Errorf("speed = %d, want 70000 (extended configured speed)", mods[0].SpeedMTs)
	}
	// both 0xFFFF and no extended values: unknown
	sb = smbiosBuilder{}
	sb.add(17, 0x5C, 0x1100, map[int][]byte{0x0C: u16(8192), 0x12: {0x22}, 0x15: u16(0xFFFF), 0x20: u16(0xFFFF)})
	sb.add(127, 4, 0x7F00, nil)
	if mods, _ := ParseMemoryModules(sb.b); len(mods) != 1 || mods[0].SpeedMTs != 0 {
		t.Errorf("unknown speed = %+v", mods)
	}
}
