package n5driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ecramDump renders 256 bytes like the driver's ecram file (patch 0008).
func ecramDump(f func(int) byte) string {
	var b strings.Builder
	for row := 0; row < 16; row++ {
		fmt.Fprintf(&b, "%02x:", row*16)
		for i := 0; i < 16; i++ {
			fmt.Fprintf(&b, " %02x", f(row*16+i))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestParseECRAM(t *testing.T) {
	dump := ecramDump(func(a int) byte { return byte(a) })
	e, err := ParseECRAM(dump)
	if err != nil {
		t.Fatal(err)
	}
	if e[0x10] != 0x10 || e[0xff] != 0xff || e.Hex(0x30, 0x32) != "30 31 32" {
		t.Fatalf("parsed %v", e.Hex(0, 0x20))
	}
	// incomplete: the last row missing
	if _, err := ParseECRAM(strings.Join(strings.Split(dump, "\n")[:15], "\n")); err == nil || !strings.Contains(err.Error(), "byte 0xf0 missing") {
		t.Fatalf("incomplete: %v", err)
	}
	if _, err := ParseECRAM(strings.Replace(dump, " 05", " zz", 1)); err == nil {
		t.Fatal("bad byte accepted")
	}
	root := t.TempDir()
	p := ECRAMPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(dump), 0o400); err != nil {
		t.Fatal(err)
	}
	if e, err := ReadECRAM(p); err != nil || e[0x81] != 0x81 {
		t.Fatalf("ReadECRAM: %v", err)
	}
}
