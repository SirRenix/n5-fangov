package hwmon_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/SirRenix/pvefand/internal/hwmon"
	"github.com/SirRenix/pvefand/internal/hwmon/hwmontest"
)

func TestNewRootFromEnv(t *testing.T) {
	t.Setenv("PVEFAND_SYSFS", "/tmp/fake")
	if got := hwmon.New().Root; got != "/tmp/fake" {
		t.Fatalf("Root = %q, want /tmp/fake", got)
	}
	t.Setenv("PVEFAND_SYSFS", "")
	if got := hwmon.New().Root; got != "/sys" {
		t.Fatalf("Root = %q, want /sys", got)
	}
}

func TestListFindsAllDevices(t *testing.T) {
	fs := &hwmon.FS{Root: hwmontest.N5Pro(t)}
	devs, err := fs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 15 {
		t.Fatalf("got %d devices, want 15", len(devs))
	}
	// ordered by index, not lexically (hwmon10 must come after hwmon9)
	if filepath.Base(devs[9].Path) != "hwmon9" || filepath.Base(devs[10].Path) != "hwmon10" {
		t.Fatalf("not ordered numerically: %s, %s", devs[9].Path, devs[10].Path)
	}
	var ec *hwmon.Device
	for i := range devs {
		if devs[i].Name == "minisforum_n5_it5571" {
			ec = &devs[i]
		}
	}
	if ec == nil {
		t.Fatal("minisforum_n5_it5571 not found")
	}
	if filepath.Base(ec.Path) != "hwmon14" {
		t.Fatalf("EC at %s, want hwmon14", ec.Path)
	}
}

func TestFind(t *testing.T) {
	fs := &hwmon.FS{Root: hwmontest.N5Pro(t)}
	if got := fs.FindByName("nvme"); len(got) != 3 {
		t.Fatalf("FindByName(nvme) = %d, want 3", len(got))
	}
	if got := fs.FindByName("drivetemp"); len(got) != 4 {
		t.Fatalf("FindByName(drivetemp) = %d, want 4", len(got))
	}
	if got := fs.FindByName("nope"); len(got) != 0 {
		t.Fatalf("FindByName(nope) = %d, want 0", len(got))
	}
	if got := fs.FindByRegexp(regexp.MustCompile(`^spd5118$`)); len(got) != 2 {
		t.Fatalf("FindByRegexp(spd5118) = %d, want 2", len(got))
	}
	if got := fs.FindByRegexp(regexp.MustCompile(`^nct67\d\d$`)); len(got) != 0 {
		t.Fatalf("FindByRegexp(nct67) = %d, want 0", len(got))
	}
}

func TestReadHelpers(t *testing.T) {
	fs := &hwmon.FS{Root: hwmontest.N5Pro(t)}
	ec := fs.FindByName("minisforum_n5_it5571")[0]
	if v, err := fs.ReadInt(filepath.Join(ec.Path, "pwm1")); err != nil || v != 85 {
		t.Fatalf("ReadInt(pwm1) = %d, %v; want 85", v, err)
	}
	if s, err := fs.ReadString(filepath.Join(ec.Path, "fan1_label")); err != nil || s != "CPU Fan" {
		t.Fatalf("ReadString(fan1_label) = %q, %v", s, err)
	}
	// relative paths resolve against Root
	if v, err := fs.ReadInt("class/hwmon/hwmon10/temp1_input"); err != nil || v != 37125 {
		t.Fatalf("ReadInt(relative) = %d, %v", v, err)
	}
	if _, err := fs.ReadInt(filepath.Join(ec.Path, "fan1_label")); err == nil {
		t.Fatal("ReadInt on a label must fail")
	}
	if _, err := fs.ReadInt(filepath.Join(ec.Path, "missing")); err == nil {
		t.Fatal("ReadInt on a missing file must fail")
	}
	if !fs.Exists(filepath.Join(ec.Path, "pwm4_enable")) || fs.Exists(filepath.Join(ec.Path, "pwm5")) {
		t.Fatal("Exists wrong")
	}
}

func TestWriteVerify(t *testing.T) {
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	fs := &hwmon.FS{Root: root}
	p := filepath.Join(root, "class", "hwmon", "hwmon14", "pwm1")
	if err := fs.WriteVerify(p, "7"); err != nil {
		t.Fatal(err)
	}
	// short value must fully replace the longer old content ("85\n")
	b, _ := os.ReadFile(p)
	if string(b) != "7" {
		t.Fatalf("file content %q, want %q", b, "7")
	}
	if v, _ := fs.ReadInt(p); v != 7 {
		t.Fatalf("read back %d", v)
	}
	// never create files
	if err := fs.WriteVerify(filepath.Join(root, "class", "hwmon", "hwmon14", "pwm9"), "1"); err == nil {
		t.Fatal("WriteVerify must not create files")
	}
	if fs.Exists(filepath.Join(root, "class", "hwmon", "hwmon14", "pwm9")) {
		t.Fatal("pwm9 was created")
	}
}

func TestVerifyValue(t *testing.T) {
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	fs := &hwmon.FS{Root: root}
	p := filepath.Join(root, "class", "hwmon", "hwmon14", "pwm2_enable")
	// the kernel strips whitespace; numeric compare tolerates leading zeros
	if err := fs.WriteVerify(p, "1\n"); err != nil {
		t.Fatalf("trailing newline must verify: %v", err)
	}
	if err := fs.WriteVerify(p, "01"); err != nil {
		t.Fatalf("numeric compare: %v", err)
	}
	// a regular file cannot refuse a write the way the EC can, so provoke the
	// mismatch path by tampering with the file after a successful write
	if err := os.WriteFile(p, []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := fs.VerifyValue(p, "1")
	if !errors.Is(err, hwmon.ErrVerify) {
		t.Fatalf("mismatch not reported as ErrVerify: %v", err)
	}
	if err := fs.VerifyValue(filepath.Join(root, "nope"), "1"); err == nil || errors.Is(err, hwmon.ErrVerify) {
		t.Fatalf("missing file must be a read error, not ErrVerify: %v", err)
	}
}
