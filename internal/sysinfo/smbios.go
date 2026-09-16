package sysinfo

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// SMBIOS structure table parsing (DMTF DSP0134). The kernel exports the
// raw table as /sys/firmware/dmi/tables/DMI and the entry point next to
// it; both are root-readable regular files, so no /dev/mem is needed —
// the daemon's sandbox (PrivateDevices=yes) reads them fine.

// SMBIOSVersion returns "major.minor" from an entry point ("_SM3_" 64-bit
// or "_SM_" 32-bit anchor), "" when the blob is neither.
func SMBIOSVersion(ep []byte) string {
	switch {
	case len(ep) >= 9 && string(ep[:5]) == "_SM3_":
		return fmt.Sprintf("%d.%d", ep[7], ep[8])
	case len(ep) >= 8 && string(ep[:4]) == "_SM_":
		return fmt.Sprintf("%d.%d", ep[6], ep[7])
	}
	return ""
}

// SMBIOS structure types used here.
const (
	smbiosPhysicalMemoryArray = 16
	smbiosMemoryDevice        = 17
	smbiosEndOfTable          = 127
)

// smbiosStruct is one structure: formatted area plus its string set.
type smbiosStruct struct {
	Type    byte
	Handle  uint16
	Data    []byte // formatted area incl. the 4-byte header
	Strings []string
}

// str returns string number n (1-based) of the structure, "" for 0 or
// out of range.
func (s smbiosStruct) str(n byte) string {
	if n == 0 || int(n) > len(s.Strings) {
		return ""
	}
	return trimSpaces(s.Strings[n-1])
}

func (s smbiosStruct) u8(off int) byte {
	if off < len(s.Data) {
		return s.Data[off]
	}
	return 0
}

func (s smbiosStruct) u16(off int) uint16 {
	if off+2 <= len(s.Data) {
		return binary.LittleEndian.Uint16(s.Data[off:])
	}
	return 0
}

func (s smbiosStruct) u32(off int) uint32 {
	if off+4 <= len(s.Data) {
		return binary.LittleEndian.Uint32(s.Data[off:])
	}
	return 0
}

// walkSMBIOS splits a structure table into structures. It stops at the
// end-of-table structure or when the data runs out; a truncated last
// structure is dropped, not an error.
func walkSMBIOS(table []byte) ([]smbiosStruct, error) {
	var out []smbiosStruct
	pos := 0
	for pos+4 <= len(table) {
		typ, ln := table[pos], int(table[pos+1])
		if ln < 4 || pos+ln > len(table) {
			return out, fmt.Errorf("smbios: structure type %d at %d has length %d", typ, pos, ln)
		}
		s := smbiosStruct{Type: typ, Handle: binary.LittleEndian.Uint16(table[pos+2:]), Data: table[pos : pos+ln]}
		p := pos + ln
		// String set: NUL-terminated strings, ended by an extra NUL.
		for p < len(table) {
			if table[p] == 0 {
				p++ // empty set or the terminating NUL
				if len(s.Strings) == 0 && p < len(table) && table[p] == 0 {
					p++
				}
				break
			}
			end := p
			for end < len(table) && table[end] != 0 {
				end++
			}
			s.Strings = append(s.Strings, string(table[p:end]))
			p = end + 1
			if p < len(table) && table[p] == 0 {
				p++ // double NUL closes the set
				break
			}
		}
		out = append(out, s)
		if typ == smbiosEndOfTable {
			break
		}
		pos = p
	}
	return out, nil
}

// memoryTypes maps SMBIOS type 17 "Memory Type" (offset 0x12).
var memoryTypes = map[byte]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "DRAM", 0x04: "EDRAM", 0x05: "VRAM", 0x06: "SRAM", 0x07: "RAM",
	0x08: "ROM", 0x09: "Flash", 0x0A: "EEPROM", 0x0B: "FEPROM", 0x0C: "EPROM", 0x0D: "CDRAM", 0x0E: "3DRAM",
	0x0F: "SDRAM", 0x10: "SGRAM", 0x11: "RDRAM", 0x12: "DDR", 0x13: "DDR2", 0x14: "DDR2 FB-DIMM",
	0x18: "DDR3", 0x19: "FBD2", 0x1A: "DDR4", 0x1B: "LPDDR", 0x1C: "LPDDR2", 0x1D: "LPDDR3", 0x1E: "LPDDR4",
	0x1F: "Logical non-volatile device", 0x20: "HBM", 0x21: "HBM2", 0x22: "DDR5", 0x23: "LPDDR5", 0x24: "HBM3",
}

// formFactors maps SMBIOS type 17 "Form Factor" (offset 0x0E).
var formFactors = map[byte]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "SIMM", 0x04: "SIP", 0x05: "Chip", 0x06: "DIP", 0x07: "ZIP",
	0x08: "Proprietary Card", 0x09: "DIMM", 0x0A: "TSOP", 0x0B: "Row of chips", 0x0C: "RIMM", 0x0D: "SODIMM",
	0x0E: "SRIMM", 0x0F: "FB-DIMM", 0x10: "Die",
}

// errNoMemoryDevice is returned when the table holds no type 17 at all
// (a firmware that does not describe its memory).
var errNoMemoryDevice = errors.New("smbios: no memory device (type 17) structures")

// ParseMemoryModules returns the populated memory slots of an SMBIOS
// structure table (type 17 records with a size; empty slots are skipped).
// ECC comes from the physical memory array (type 16, error correction
// single-bit/multi-bit/CRC) the module belongs to, or from a total width
// above the data width.
func ParseMemoryModules(table []byte) ([]MemoryModule, error) {
	structs, err := walkSMBIOS(table)
	if err != nil && len(structs) == 0 {
		return nil, err
	}
	arrayECC := map[uint16]bool{}
	for _, s := range structs {
		if s.Type == smbiosPhysicalMemoryArray {
			// Memory Error Correction (DSP0134 7.17.3): 03 none, 04 parity,
			// 05 single-bit ECC, 06 multi-bit ECC, 07 CRC. Only 05/06 correct
			// errors; CRC detects them.
			ecc := s.u8(0x06)
			arrayECC[s.Handle] = ecc == 0x05 || ecc == 0x06
		}
	}
	var out []MemoryModule
	seen := false
	for _, s := range structs {
		if s.Type != smbiosMemoryDevice {
			continue
		}
		seen = true
		size := memorySize(s)
		if size <= 0 {
			continue // empty slot, or unknown size
		}
		m := MemoryModule{
			Slot:         s.str(s.u8(0x10)),
			Bank:         s.str(s.u8(0x11)),
			SizeBytes:    size,
			Type:         memoryTypes[s.u8(0x12)],
			FormFactor:   formFactors[s.u8(0x0E)],
			SpeedMTs:     int(s.u16(0x15)),
			Manufacturer: s.str(s.u8(0x17)),
			Part:         s.str(s.u8(0x1A)),
			ECC:          arrayECC[s.u16(0x04)] || (s.u16(0x08) > s.u16(0x0A) && s.u16(0x0A) != 0 && s.u16(0x08) != 0xFFFF),
		}
		if m.Type == "" {
			m.Type = fmt.Sprintf("type 0x%02x", s.u8(0x12))
		}
		// SMBIOS 2.7+: configured speed (0x20, what the module runs at)
		// wins over the nominal speed (0x15); 3.3+: a value of 0xFFFF in
		// either field means "see the extended field" (0x58 configured,
		// 0x54 nominal) for speeds above 65534 MT/s.
		switch v := s.u16(0x20); {
		case v == 0xFFFF && s.u32(0x58) != 0:
			m.SpeedMTs = int(s.u32(0x58))
		case v != 0 && v != 0xFFFF:
			m.SpeedMTs = int(v)
		}
		if m.SpeedMTs == 0xFFFF {
			if v := s.u32(0x54); v != 0 {
				m.SpeedMTs = int(v)
			}
		}
		if m.SpeedMTs == 0xFFFF {
			m.SpeedMTs = 0 // unknown
		}
		if m.Manufacturer == "Unknown" || m.Manufacturer == "Not Specified" {
			m.Manufacturer = ""
		}
		out = append(out, m)
	}
	if !seen {
		return nil, errNoMemoryDevice
	}
	return out, err
}

// memorySize decodes the type 17 size: 0 = empty, 0xFFFF = unknown,
// 0x7FFF = use the extended size (MB), bit 15 set = KB units, else MB.
func memorySize(s smbiosStruct) int64 {
	v := s.u16(0x0C)
	switch v {
	case 0, 0xFFFF:
		return 0
	case 0x7FFF:
		return int64(s.u32(0x1C)) << 20
	}
	if v&0x8000 != 0 {
		return int64(v&0x7FFF) << 10
	}
	return int64(v) << 20
}

// trimSpaces drops surrounding blanks (part numbers are space-padded).
func trimSpaces(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return s[i:j]
}
