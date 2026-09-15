package profile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SirRenix/pvefand/internal/hwmon"
	"github.com/SirRenix/pvefand/internal/hwmon/hwmontest"
	"github.com/SirRenix/pvefand/internal/profile"
)

// n5proCopy returns a writable clone of the n5host tree plus its FS.
func n5proCopy(t *testing.T) (string, *hwmon.FS) {
	t.Helper()
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	return root, &hwmon.FS{Root: root}
}

func TestAllAndNames(t *testing.T) {
	want := []string{"n5pro", "nct67xx", "it87xx", "monitor"}
	got := profile.Names()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for _, p := range profile.All() {
		if p.Verified() != (p.Name() == "n5pro") {
			t.Errorf("%s: Verified() = %v", p.Name(), p.Verified())
		}
		if p.Title() == "" || p.Notes() == "" {
			t.Errorf("%s: empty title or notes", p.Name())
		}
	}
	if !strings.Contains(profile.N5Pro().Notes(),
		"EC does not resume automatic regulation of the HDD channel (pwm3) after any write - use a fixed stop duty; measured 2026-09-14 on BIOS 1.05") {
		t.Error("n5pro notes lack the HDD channel caveat")
	}
	for _, name := range []string{"nct67xx", "it87xx"} {
		p, _ := profile.ByName(name)
		if !strings.Contains(strings.ToLower(p.Notes()), "untested") {
			t.Errorf("%s notes must say untested", name)
		}
	}
}

func TestN5ProDetect(t *testing.T) {
	fs := &hwmon.FS{Root: hwmontest.N5Pro(t)}
	dev, err := profile.Detect(fs, "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Profile().Name() != "n5pro" || !dev.Profile().Verified() {
		t.Fatalf("wrong profile %s", dev.Profile().Name())
	}
	if filepath.Base(dev.HwmonPath()) != "hwmon14" {
		t.Fatalf("HwmonPath = %s", dev.HwmonPath())
	}
	chans := dev.Channels()
	if len(chans) != 4 {
		t.Fatalf("got %d channels, want 4", len(chans))
	}
	wantLabels := map[int]string{1: "CPU Fan", 2: "SSD Fan", 3: "HDD Fan", 4: "PCIe Fan"}
	for _, c := range chans {
		if c.Label != wantLabels[c.Index] {
			t.Errorf("pwm%d label %q, want %q", c.Index, c.Label, wantLabels[c.Index])
		}
		if c.HasTach != (c.Index != 4) {
			t.Errorf("pwm%d HasTach = %v", c.Index, c.HasTach)
		}
	}
	// reads against the read-only tree
	if rpm, err := dev.ReadRPM(1); err != nil || rpm != 2009 {
		t.Errorf("ReadRPM(1) = %d, %v; want 2009", rpm, err)
	}
	if rpm, err := dev.ReadRPM(4); err != nil || rpm != -1 {
		t.Errorf("ReadRPM(4) = %d, %v; want -1 (no tach)", rpm, err)
	}
	if duty, err := dev.ReadDuty(3); err != nil || duty != 135 {
		t.Errorf("ReadDuty(3) = %d, %v; want 135", duty, err)
	}
	if _, err := dev.ReadDuty(5); err == nil {
		t.Error("ReadDuty(5) must fail")
	}
	extra := dev.ExtraTemps()
	for id, file := range map[string]string{"ec:cpu": "temp1_input", "ec:system": "temp2_input", "ec:board": "temp3_input", "ec:ambient": "temp4_input"} {
		if filepath.Base(extra[id]) != file {
			t.Errorf("ExtraTemps[%s] = %q, want .../%s", id, extra[id], file)
		}
	}
	if len(extra) != 4 {
		t.Errorf("ExtraTemps has %d entries: %v", len(extra), extra)
	}
}

func TestDetectAutoPrefersN5Pro(t *testing.T) {
	fs := &hwmon.FS{Root: hwmontest.N5Pro(t)}
	dev, err := profile.Detect(fs, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Profile().Name() != "n5pro" {
		t.Fatalf("auto picked %s", dev.Profile().Name())
	}
	if _, err := profile.Detect(fs, "nct67xx"); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("nct67xx on n5pro tree: %v", err)
	}
	if _, err := profile.Detect(fs, "bogus"); err == nil {
		t.Fatal("unknown profile must fail")
	}
}

// snapshot records every file's content so a test can prove detection did
// not write anything.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDetectNeverWrites(t *testing.T) {
	root, fs := n5proCopy(t)
	before := snapshot(t, root)
	for _, want := range []string{"auto", "n5pro", "monitor", "nct67xx", "it87xx"} {
		_, _ = profile.Detect(fs, want)
	}
	after := snapshot(t, root)
	if len(before) != len(after) {
		t.Fatalf("file count changed %d -> %d", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed during detection", p)
		}
	}
}

func TestN5ProWriteDutyVerifies(t *testing.T) {
	root, fs := n5proCopy(t)
	dev, err := profile.Detect(fs, "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	if err := dev.WriteDuty(1, 200); err != nil {
		t.Fatal(err)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon14", "pwm1"); got != "200" {
		t.Fatalf("pwm1 = %q", got)
	}
	if d, _ := dev.ReadDuty(1); d != 200 {
		t.Fatalf("ReadDuty = %d", d)
	}
	for _, bad := range []int{-1, 256} {
		if err := dev.WriteDuty(1, bad); err == nil {
			t.Errorf("WriteDuty(%d) must fail", bad)
		}
	}
	// pwm4 is in auto (enable=2): WriteDuty must switch to manual first
	if err := dev.WriteDuty(4, 120); err != nil {
		t.Fatal(err)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon14", "pwm4_enable"); got != "1" {
		t.Fatalf("pwm4_enable = %q, want 1 (rule 2)", got)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon14", "pwm4"); got != "120" {
		t.Fatalf("pwm4 = %q", got)
	}
}

func TestN5ProWriteDutyMismatch(t *testing.T) {
	root, fs := n5proCopy(t)
	dev, err := profile.Detect(fs, "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	// make pwm2 unwritable: replace it with a directory so the open fails
	p := filepath.Join(root, "class", "hwmon", "hwmon14", "pwm2")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := dev.WriteDuty(2, 100); err == nil {
		t.Fatal("write to an unwritable attribute must fail")
	}
}

func TestN5ProEnterManualIdempotent(t *testing.T) {
	root, fs := n5proCopy(t)
	dev, err := profile.Detect(fs, "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "class", "hwmon", "hwmon14", "pwm1_enable")
	// pwm1_enable is already 1: must not be rewritten (mtime/content stays)
	if err := os.WriteFile(p, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dev.EnterManual(1); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "1\n" {
		t.Fatalf("pwm1_enable rewritten although already manual: %q", b)
	}
	// pwm4_enable is 2: must be written once, then left alone
	p4 := filepath.Join(root, "class", "hwmon", "hwmon14", "pwm4_enable")
	if err := dev.EnterManual(4); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p4)
	if string(b) != "1" {
		t.Fatalf("pwm4_enable = %q after EnterManual", b)
	}
	if err := os.WriteFile(p4, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dev.EnterManual(4); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p4)
	if string(b) != "1\n" {
		t.Fatalf("second EnterManual rewrote pwm4_enable: %q", b)
	}
}

func TestN5ProSafeStop(t *testing.T) {
	root, fs := n5proCopy(t)
	dev, err := profile.Detect(fs, "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	if err := dev.SafeStop(1, "auto"); err != nil {
		t.Fatal(err)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon14", "pwm1_enable"); got != "2" {
		t.Fatalf("pwm1_enable = %q, want 2", got)
	}
	// HDD channel: fixed stop duty, was already manual
	if err := dev.SafeStop(3, "140"); err != nil {
		t.Fatal(err)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon14", "pwm3_enable"); got != "1" {
		t.Fatalf("pwm3_enable = %q, want 1", got)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon14", "pwm3"); got != "140" {
		t.Fatalf("pwm3 = %q, want 140", got)
	}
	// fixed stop on a channel in auto mode must enter manual first
	if err := dev.SafeStop(4, "140"); err != nil {
		t.Fatal(err)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon14", "pwm4_enable"); got != "1" {
		t.Fatalf("pwm4_enable = %q, want 1", got)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon14", "pwm4"); got != "140" {
		t.Fatalf("pwm4 = %q, want 140", got)
	}
	for _, bad := range []string{"", "300", "-1", "full", "auto "} {
		if err := dev.SafeStop(2, bad); err == nil {
			t.Errorf("SafeStop(%q) must fail", bad)
		}
	}
	if err := dev.SafeStop(9, "auto"); err == nil {
		t.Error("SafeStop on unknown channel must fail")
	}
}

// nctTree builds a minimal synthetic nct6798 device: pwm1 with tach in
// SmartFan IV, pwm2 without tach in manual, plus a pwm3 without enable
// attribute that must be ignored.
func nctTree(t *testing.T) (string, *hwmon.FS) {
	t.Helper()
	root := t.TempDir()
	hwmontest.WriteAttr(t, root, "hwmon0", "name", "nct6798")
	hwmontest.WriteAttr(t, root, "hwmon0", "pwm1", "128")
	hwmontest.WriteAttr(t, root, "hwmon0", "pwm1_enable", "5")
	hwmontest.WriteAttr(t, root, "hwmon0", "fan1_input", "1200")
	hwmontest.WriteAttr(t, root, "hwmon0", "pwm2", "255")
	hwmontest.WriteAttr(t, root, "hwmon0", "pwm2_enable", "1")
	hwmontest.WriteAttr(t, root, "hwmon0", "pwm3", "0")
	hwmontest.WriteAttr(t, root, "hwmon0", "temp1_input", "40000")
	hwmontest.WriteAttr(t, root, "hwmon1", "name", "k10temp")
	hwmontest.WriteAttr(t, root, "hwmon1", "temp1_input", "50000")
	return root, &hwmon.FS{Root: root}
}

func TestNCT67xxDetectAndRestore(t *testing.T) {
	root, fs := nctTree(t)
	dev, err := profile.Detect(fs, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Profile().Name() != "nct67xx" || dev.Profile().Verified() {
		t.Fatalf("auto picked %s (verified=%v)", dev.Profile().Name(), dev.Profile().Verified())
	}
	chans := dev.Channels()
	if len(chans) != 2 || chans[0].Index != 1 || chans[1].Index != 2 {
		t.Fatalf("channels = %+v", chans)
	}
	if !chans[0].HasTach || chans[1].HasTach {
		t.Fatalf("HasTach wrong: %+v", chans)
	}
	if rpm, err := dev.ReadRPM(1); err != nil || rpm != 1200 {
		t.Fatalf("ReadRPM(1) = %d, %v", rpm, err)
	}
	if err := dev.WriteDuty(1, 90); err != nil {
		t.Fatal(err)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon0", "pwm1_enable"); got != "1" {
		t.Fatalf("pwm1_enable = %q after WriteDuty, want 1", got)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon0", "pwm1"); got != "90" {
		t.Fatalf("pwm1 = %q", got)
	}
	// "auto" restores the value captured at detection (5), not a constant
	if err := dev.SafeStop(1, "auto"); err != nil {
		t.Fatal(err)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon0", "pwm1_enable"); got != "5" {
		t.Fatalf("pwm1_enable = %q after SafeStop auto, want restored 5", got)
	}
	// pwm2 was already manual at detection: fall back to the documented auto value
	if err := dev.SafeStop(2, "auto"); err != nil {
		t.Fatal(err)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon0", "pwm2_enable"); got != "5" {
		t.Fatalf("pwm2_enable = %q after SafeStop auto, want fallback 5", got)
	}
}

func TestNCT67xxRestoresOtherOriginal(t *testing.T) {
	root, fs := nctTree(t)
	hwmontest.WriteAttr(t, root, "hwmon0", "pwm1_enable", "2")
	dev, err := profile.Detect(fs, "nct67xx")
	if err != nil {
		t.Fatal(err)
	}
	if err := dev.EnterManual(1); err != nil {
		t.Fatal(err)
	}
	if err := dev.SafeStop(1, "auto"); err != nil {
		t.Fatal(err)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon0", "pwm1_enable"); got != "2" {
		t.Fatalf("pwm1_enable = %q, want original 2", got)
	}
}

func TestIT87xxDetect(t *testing.T) {
	root := t.TempDir()
	hwmontest.WriteAttr(t, root, "hwmon3", "name", "it8688")
	hwmontest.WriteAttr(t, root, "hwmon3", "pwm1", "100")
	hwmontest.WriteAttr(t, root, "hwmon3", "pwm1_enable", "1")
	hwmontest.WriteAttr(t, root, "hwmon3", "fan1_input", "900")
	hwmontest.WriteAttr(t, root, "hwmon3", "fan1_label", "CPU")
	fs := &hwmon.FS{Root: root}
	dev, err := profile.Detect(fs, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Profile().Name() != "it87xx" {
		t.Fatalf("auto picked %s", dev.Profile().Name())
	}
	if ch := dev.Channels(); len(ch) != 1 || ch[0].Label != "CPU" || !ch[0].HasTach {
		t.Fatalf("channels = %+v", ch)
	}
	if err := dev.SafeStop(1, "auto"); err != nil {
		t.Fatal(err)
	}
	if got := hwmontest.ReadAttr(t, root, "hwmon3", "pwm1_enable"); got != "2" {
		t.Fatalf("pwm1_enable = %q, want fallback 2", got)
	}
}

func TestMonitorOnTreeWithoutPWM(t *testing.T) {
	root := t.TempDir()
	hwmontest.WriteAttr(t, root, "hwmon0", "name", "k10temp")
	hwmontest.WriteAttr(t, root, "hwmon0", "temp1_input", "45000")
	hwmontest.WriteAttr(t, root, "hwmon1", "name", "nvme")
	hwmontest.WriteAttr(t, root, "hwmon1", "temp1_input", "40000")
	fs := &hwmon.FS{Root: root}
	dev, err := profile.Detect(fs, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Profile().Name() != "monitor" || dev.Profile().Verified() {
		t.Fatalf("auto picked %s", dev.Profile().Name())
	}
	if len(dev.Channels()) != 0 {
		t.Fatalf("monitor must have no channels: %+v", dev.Channels())
	}
	if len(dev.ExtraTemps()) != 0 {
		t.Fatal("monitor must have no extra temps")
	}
	for name, err := range map[string]error{
		"EnterManual": dev.EnterManual(1),
		"WriteDuty":   dev.WriteDuty(1, 100),
		"SafeStop":    dev.SafeStop(1, "auto"),
	} {
		if !errors.Is(err, profile.ErrMonitorOnly) {
			t.Errorf("%s: %v, want ErrMonitorOnly", name, err)
		}
	}
	if _, err := dev.ReadRPM(1); err == nil {
		t.Error("ReadRPM must fail on monitor")
	}
	// a chip with pwm but without pwmN_enable is not controllable either
	hwmontest.WriteAttr(t, root, "hwmon2", "name", "nct6795")
	hwmontest.WriteAttr(t, root, "hwmon2", "pwm1", "128")
	dev, err = profile.Detect(fs, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Profile().Name() != "monitor" {
		t.Fatalf("pwm without enable must not be controllable, got %s", dev.Profile().Name())
	}
}

func TestMonitorNeedsSomeHwmon(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "class", "hwmon"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := &hwmon.FS{Root: root}
	if _, err := profile.Detect(fs, "auto"); err == nil {
		t.Fatal("empty hwmon class must fail auto-detection")
	}
	if _, err := profile.Detect(&hwmon.FS{Root: filepath.Join(root, "missing")}, "monitor"); err == nil {
		t.Fatal("missing sysfs must fail")
	}
}
