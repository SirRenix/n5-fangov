package profile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
	"github.com/SirRenix/n5-fangov/internal/n5driver"
	"github.com/SirRenix/n5-fangov/internal/profile"
)

// n5proCopy returns a writable clone of the N5 Pro test host tree plus its FS.
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
		if p.Title() == "" || p.Notes() == "" {
			t.Errorf("%s: empty title or notes", p.Name())
		}
	}
	if !strings.Contains(profile.N5Pro().Notes(),
		"After the hand-back to EC automatic the EC regulates the HDD channel (pwm3) to a target speed from an EC table keyed on EC 0x06 (derived from firmware); whether that follows disk temperature is open, so n5-fangov keeps a fixed HDD stop duty.") {
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
	if dev.Profile().Name() != "n5pro" {
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
	if dev.Profile().Name() != "nct67xx" {
		t.Fatalf("auto picked %s", dev.Profile().Name())
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
	if dev.Profile().Name() != "monitor" {
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

// Gate (a): the N5 Pro driver loaded without pwm nodes is a fault, not a
// silent fallback to monitor; the error says why (force, experimental_write).
func TestAutoWriteDisabledIsNotMonitor(t *testing.T) {
	root, fs := n5proCopy(t)
	for _, a := range []string{"pwm1", "pwm1_enable", "pwm2", "pwm2_enable", "pwm3", "pwm3_enable", "pwm4", "pwm4_enable"} {
		if err := os.Remove(filepath.Join(root, "class", "hwmon", "hwmon14", a)); err != nil {
			t.Fatal(err)
		}
	}
	params := filepath.Join(root, "module", "minisforum_n5_it5571", "parameters")
	if err := os.MkdirAll(params, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"force": "N", "experimental_write": "N"} {
		if err := os.WriteFile(filepath.Join(params, k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"auto", "", "n5pro"} {
		dev, err := profile.Detect(fs, want)
		if !errors.Is(err, profile.ErrWriteDisabled) {
			t.Fatalf("%q: dev=%v err=%v, want ErrWriteDisabled", want, dev, err)
		}
		if !strings.Contains(err.Error(), "experimental_write=N") || !strings.Contains(err.Error(), `profile = "monitor"`) {
			t.Errorf("%q: message lacks reason or way out: %v", want, err)
		}
	}
	if err := os.WriteFile(filepath.Join(params, "force"), []byte("Y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := profile.Detect(fs, "auto"); err == nil || !strings.Contains(err.Error(), "force-loaded") {
		t.Fatalf("force=Y not reported: %v", err)
	}
	// an explicit monitor profile still works on such a box
	if dev, err := profile.Detect(fs, "monitor"); err != nil || dev.Profile().Name() != "monitor" {
		t.Fatalf("explicit monitor: %v", err)
	}
}

// Gate (f): with the driver's debugfs dump present the EC register can be
// compared with a written duty (VerifyRegister); without it nothing is
// checked. WriteDuty itself does not read the register any more: the EC
// takes a duty with a delay (maintenance window 1, C6/Z1/Z2).
func TestVerifyRegisterChecksDCRWhenReadable(t *testing.T) {
	root, fs := n5proCopy(t)
	dev, err := profile.Detect(fs, "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	rv, ok := dev.(profile.RegisterVerifier)
	if !ok {
		t.Fatal("n5pro device is no RegisterVerifier")
	}
	if rv.RegisterSettle(3) < 5*time.Second || rv.RegisterSettle(1) < time.Second || rv.RegisterSettle(4) < time.Second {
		t.Fatalf("settle times %s/%s/%s, want >= 5 s HDD, >= 1 s else", rv.RegisterSettle(3), rv.RegisterSettle(1), rv.RegisterSettle(4))
	}
	if err := rv.VerifyRegister(3, 140); err != nil {
		t.Fatalf("no dcr file: %v", err)
	}
	dir := filepath.Join(root, "kernel", "debug", "minisforum_n5_it5571")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dcr := filepath.Join(dir, "dcr")
	// DCR3 (0x1805) = 0x8c = 140, DCR1 = 0x55
	if err := os.WriteFile(dcr, []byte("1800: 00 ff 00 55 4a 8c 8c 8c 00 00 00 06 00 00 00 00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// a write never fails on the register, whatever it shows right after
	if err := dev.WriteDuty(1, 200); err != nil {
		t.Fatalf("WriteDuty with a deviating register: %v", err)
	}
	if err := rv.VerifyRegister(3, 140); err != nil {
		t.Fatalf("DCR matches: %v", err)
	}
	// a mismatch is ErrDCRMismatch, not a read-back error of the driver
	err = rv.VerifyRegister(1, 200)
	if !errors.Is(err, profile.ErrDCRMismatch) || errors.Is(err, hwmon.ErrVerify) || !strings.Contains(err.Error(), "DCR1 reads 85 after writing 200") {
		t.Fatalf("DCR mismatch: %v", err)
	}
	if err := os.WriteFile(dcr, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rv.VerifyRegister(1, 200); err != nil {
		t.Fatalf("unparsable dump must not count as a deviation: %v", err)
	}
}

// WaitRegister against a fake dcr dump: a register that reaches the value
// within the deadline (HDD ramp, Z2) is no deviation; one that never does
// is reported after the deadline, with the last value read.
func TestWaitRegisterSettles(t *testing.T) {
	root, fs := n5proCopy(t)
	dev, err := profile.Detect(fs, "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	rv := dev.(profile.RegisterVerifier)
	dir := filepath.Join(root, "kernel", "debug", "minisforum_n5_it5571")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dcr := filepath.Join(dir, "dcr")
	// the EC ramps DCR3 0x57 -> 0x6a -> 0x8c (140) over three polls
	ramp := []string{"57", "6a", "8c"}
	write := func(v string) {
		if err := os.WriteFile(dcr, []byte("1800: 00 ff 00 55 4a "+v+" "+v+" 8c 00 00 00 06 00 00 00 00\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(ramp[0])
	now := time.Date(2026, 10, 17, 12, 33, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	polls := 0
	sleep := func(d time.Duration) {
		polls++
		now = now.Add(d)
		if polls < len(ramp) {
			write(ramp[polls])
		}
	}
	if err := profile.WaitRegister(rv, 3, 140, now.Add(rv.RegisterSettle(3)), clock, sleep); err != nil {
		t.Fatalf("ramping register: %v", err)
	}
	if polls != 2 {
		t.Fatalf("polls %d, want 2", polls)
	}
	// never reaches 200: reported once the deadline has passed
	start := now
	err = profile.WaitRegister(rv, 1, 200, now.Add(time.Second), clock, sleep)
	if !errors.Is(err, profile.ErrDCRMismatch) || now.Sub(start) < time.Second {
		t.Fatalf("stuck register: %v after %s", err, now.Sub(start))
	}
}

// Gate (a), second half: a board from the hardware table without the EC
// driver (not built for this kernel, removed) does not pass as monitor.
func TestDetectAutoDriverMissingOnN5Board(t *testing.T) {
	root, fs := n5proCopy(t)
	if err := os.RemoveAll(filepath.Join(root, "class", "hwmon", "hwmon14")); err != nil {
		t.Fatal(err)
	}
	// anonymised DMI of the fixture: monitor as before
	if dev, err := profile.Detect(fs, "auto"); err != nil || dev.Profile().Name() != "monitor" {
		t.Fatalf("other board: %v", err)
	}
	for k, v := range map[string]string{"product_name": "N5 PRO", "board_name": "F8NAA"} {
		if err := os.WriteFile(filepath.Join(root, "class", "dmi", "id", k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// N5 PRO/F8NAA with the fixture's vendor string: the driver keeps it
	// read-only (exact sys_vendor, patch 0002), so auto runs as monitor
	// (rc2 review R4)
	if dev, err := profile.Detect(fs, "auto"); err != nil || dev.Profile().Name() != "monitor" {
		t.Fatalf("N5 PRO/F8NAA with another vendor: dev %v err %v", dev, err)
	}
	if err := os.WriteFile(filepath.Join(root, "class", "dmi", "id", "sys_vendor"), []byte(n5driver.SysVendor+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := profile.Detect(fs, "auto")
	if !errors.Is(err, profile.ErrDriverMissing) || !strings.Contains(err.Error(), "N5 PRO / F8NAA") || !strings.Contains(err.Error(), `profile = "monitor"`) {
		t.Fatalf("N5 board without driver: %v", err)
	}
	if dev, err := profile.Detect(fs, "monitor"); err != nil || dev.Profile().Name() != "monitor" {
		t.Fatalf("explicit monitor: %v", err)
	}
}

// G3 (audit 2026-10-16, decision 4): boards the driver keeps read-only (N5
// Air, N5 PRO/F8NAB) run as monitor under auto, with the driver missing or
// loaded read-only, as in 0.4.2; the upstream N5/F8NAA (write-capable)
// still stops with ErrDriverMissing.
func TestDetectAutoReadOnlyBoardsMonitor(t *testing.T) {
	setDMI := func(t *testing.T, root, product, board string) {
		t.Helper()
		for k, v := range map[string]string{"product_name": product, "board_name": board, "sys_vendor": n5driver.SysVendor} {
			if err := os.WriteFile(filepath.Join(root, "class", "dmi", "id", k), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, b := range [][2]string{{"N5A", "F8NAB"}, {"N5 AIR", "F8NAB"}, {"N5 PRO", "F8NAB"}} {
		// driver missing
		root, fs := n5proCopy(t)
		if err := os.RemoveAll(filepath.Join(root, "class", "hwmon", "hwmon14")); err != nil {
			t.Fatal(err)
		}
		setDMI(t, root, b[0], b[1])
		if dev, err := profile.Detect(fs, "auto"); err != nil || dev.Profile().Name() != "monitor" {
			t.Fatalf("%v without driver: dev %v err %v", b, dev, err)
		}
		// driver loaded read-only (pwm nodes hidden)
		root, fs = n5proCopy(t)
		for _, a := range []string{"pwm1", "pwm1_enable", "pwm2", "pwm2_enable", "pwm3", "pwm3_enable", "pwm4", "pwm4_enable"} {
			if err := os.Remove(filepath.Join(root, "class", "hwmon", "hwmon14", a)); err != nil {
				t.Fatal(err)
			}
		}
		setDMI(t, root, b[0], b[1])
		if dev, err := profile.Detect(fs, "auto"); err != nil || dev.Profile().Name() != "monitor" {
			t.Fatalf("%v read-only driver: dev %v err %v", b, dev, err)
		}
		// the explicit n5pro profile still says why it cannot write
		if _, err := profile.Detect(fs, "n5pro"); !errors.Is(err, profile.ErrWriteDisabled) {
			t.Fatalf("%v explicit n5pro: %v", b, err)
		}
	}
	root, fs := n5proCopy(t)
	if err := os.RemoveAll(filepath.Join(root, "class", "hwmon", "hwmon14")); err != nil {
		t.Fatal(err)
	}
	setDMI(t, root, "N5", "F8NAA")
	if _, err := profile.Detect(fs, "auto"); !errors.Is(err, profile.ErrDriverMissing) {
		t.Fatalf("N5/F8NAA without driver: %v", err)
	}
}
