package n5driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, v string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadState(t *testing.T) {
	root := t.TempDir()
	if st := Read(root); st.Loaded || st.Mode() != "not-loaded" || len(st.Findings()) != 0 {
		t.Fatalf("empty tree: %+v", st)
	}
	mod := filepath.Join(root, "module", ModuleName)
	write(t, filepath.Join(mod, "version"), "0.2.1+sr1~rc1")
	write(t, filepath.Join(mod, "srcversion"), "886E3C25669AF187D2E96F0")
	write(t, filepath.Join(mod, "taint"), "OE")
	write(t, filepath.Join(mod, "parameters", "force"), "N")
	write(t, filepath.Join(mod, "parameters", "experimental_write"), "Y")
	write(t, filepath.Join(mod, "parameters", "debug_regs"), "N")
	hw := filepath.Join(root, "class", "hwmon", "hwmon7")
	write(t, filepath.Join(root, "class", "hwmon", "hwmon3", "name"), "k10temp")
	write(t, filepath.Join(hw, "name"), ModuleName)
	write(t, filepath.Join(hw, "fan1_input"), "2000")
	st := Read(root)
	if !st.Loaded || st.Version != "0.2.1+sr1~rc1" || st.Taint != "OE" || st.HwmonPath != hw {
		t.Fatalf("state %+v", st)
	}
	if st.Mode() != "write-disabled" || !st.WriteDisabled() {
		t.Fatalf("no pwm nodes: mode %s", st.Mode())
	}
	if f := strings.Join(st.Findings(), "|"); !strings.Contains(f, "write disabled") || strings.Contains(f, "force") {
		t.Fatalf("findings %q", f)
	}
	write(t, filepath.Join(hw, "pwm1"), "255")
	write(t, filepath.Join(hw, "pwm1_enable"), "2")
	write(t, filepath.Join(mod, "parameters", "debug_regs"), "Y")
	write(t, DCRPath(root), "1800: 00 ff 00 55 4a 57 57 8c 00 00 00 06 00 00 00 00")
	st = Read(root)
	if st.Mode() != "write-enabled" || st.DCRPath == "" || len(st.Attrs) != 4 {
		t.Fatalf("state %+v", st)
	}
	if f := strings.Join(st.Findings(), "|"); !strings.Contains(f, "debug_regs=Y") {
		t.Fatalf("findings %q", f)
	}
	write(t, filepath.Join(mod, "parameters", "force"), "Y")
	if !Read(root).Forced() {
		t.Fatal("force=Y not seen")
	}
}

func TestParseDCR(t *testing.T) {
	d, err := ParseDCR("1800: 00 ff 00 55 4a 57 57 8c 00 00 00 06 00 00 00 00\n1810: 00 00\n")
	if err != nil {
		t.Fatal(err)
	}
	for pwm, want := range map[int]int{1: 0x55, 2: 0x4a, 3: 0x57, 4: 0x8c} {
		if got, ok := d.Channel(pwm); !ok || got != want {
			t.Errorf("pwm%d = %#x, want %#x", pwm, got, want)
		}
	}
	if d.Reg(4) != 0x57 || d.String()[:22] != "1800: 00 ff 00 55 4a 5" {
		t.Fatalf("Reg/String: %s", d)
	}
	// partial line from a hand log, starting at 0x1803
	if p, err := ParseDCR("1803: 55 4a 77 77 8c"); err != nil || p.Reg(3) != 0x77 || p.Reg(5) != 0x8c {
		t.Fatalf("partial: %v %s", err, p)
	}
	for _, bad := range []string{"", "1800: 00 ff", "1800: 00 ff 00 zz 4a 57 57 8c", "garbage"} {
		if _, err := ParseDCR(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, ok := ChannelDCR(5); ok {
		t.Fatal("pwm5 mapped")
	}
}
