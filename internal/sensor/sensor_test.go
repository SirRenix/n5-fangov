package sensor_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
	"github.com/SirRenix/n5-fangov/internal/profile"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

func n5pro(t *testing.T) (*hwmon.FS, profile.Device) {
	t.Helper()
	fs := &hwmon.FS{Root: hwmontest.N5Pro(t)}
	dev, err := profile.Detect(fs, "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	return fs, dev
}

func TestReadKnownSources(t *testing.T) {
	fs, dev := n5pro(t)
	cases := map[string]int{
		"k10temp":             37125,
		"nvme:max":            43850,
		"drivetemp:max":       38000,
		"ec:system":           31000,
		"ec:cpu":              33000,
		"hwmon:amdgpu:temp1":  32000,
		"hwmon:nic1:temp2":    41000,
		"hwmon:hwmon12:temp1": 33000,
		" k10temp ":           37125,
	}
	for id, want := range cases {
		src, err := sensor.Parse(id, fs, dev)
		if err != nil {
			t.Errorf("Parse(%q): %v", id, err)
			continue
		}
		if src.ID() != strings.TrimSpace(id) {
			t.Errorf("ID() = %q", src.ID())
		}
		got, err := src.Read()
		if err != nil || got != want {
			t.Errorf("%s: Read() = %d, %v; want %d", id, got, err, want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	fs, dev := n5pro(t)
	bad := []string{"", "bogus", "coretemp", "hwmon:nope:temp1", "hwmon:amdgpu:temp9", "hwmon:amdgpu",
		"ec:nope", "nvme", "k10temp:max", "temp1"}
	for _, id := range bad {
		if _, err := sensor.Parse(id, fs, dev); err == nil {
			t.Errorf("Parse(%q) must fail", id)
		}
	}
	if _, err := sensor.Parse("ec:system", fs, nil); err == nil {
		t.Error("ec:* without device must fail")
	}
	if _, err := sensor.Parse("k10temp", fs, nil); err != nil {
		t.Errorf("k10temp must not need a device: %v", err)
	}
	if _, err := sensor.Parse("k10temp", nil, nil); err == nil {
		t.Error("nil FS must fail")
	}
	_, err := sensor.Parse("coretemp", fs, nil)
	if !errors.Is(err, sensor.ErrNoDevice) {
		t.Errorf("coretemp on AMD box: %v, want ErrNoDevice", err)
	}
}

func TestPlausibility(t *testing.T) {
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	fs := &hwmon.FS{Root: root}
	src, err := sensor.Parse("k10temp", fs, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"120001", "-20001", "999999999"} {
		hwmontest.WriteAttr(t, root, "hwmon10", "temp1_input", v)
		if _, err := src.Read(); !errors.Is(err, sensor.ErrImplausible) {
			t.Errorf("value %s: %v, want ErrImplausible", v, err)
		}
	}
	for _, v := range []string{"120000", "-20000", "0"} {
		hwmontest.WriteAttr(t, root, "hwmon10", "temp1_input", v)
		if _, err := src.Read(); err != nil {
			t.Errorf("value %s: %v, want ok", v, err)
		}
	}
	hwmontest.WriteAttr(t, root, "hwmon10", "temp1_input", "garbage")
	if _, err := src.Read(); err == nil {
		t.Error("garbage must fail")
	}
}

func TestMaxIgnoresVanishedDevices(t *testing.T) {
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	fs := &hwmon.FS{Root: root}
	src, err := sensor.Parse("nvme:max", fs, nil)
	if err != nil {
		t.Fatal(err)
	}
	// the hottest one (hwmon4, 43850) disappears -> next highest wins
	if err := os.RemoveAll(filepath.Join(root, "class", "hwmon", "hwmon4")); err != nil {
		t.Fatal(err)
	}
	if v, err := src.Read(); err != nil || v != 42850 {
		t.Fatalf("after removal: %d, %v; want 42850", v, err)
	}
	// an implausible device is ignored the same way
	hwmontest.WriteAttr(t, root, "hwmon5", "temp1_input", "250000")
	if v, err := src.Read(); err != nil || v != 38850 {
		t.Fatalf("with implausible: %d, %v; want 38850", v, err)
	}
	// all gone -> error
	os.RemoveAll(filepath.Join(root, "class", "hwmon", "hwmon3"))
	os.RemoveAll(filepath.Join(root, "class", "hwmon", "hwmon5"))
	if _, err := src.Read(); err == nil {
		t.Fatal("no readable device must fail")
	}
}

func TestCoretempMax(t *testing.T) {
	root := t.TempDir()
	hwmontest.WriteAttr(t, root, "hwmon0", "name", "coretemp")
	hwmontest.WriteAttr(t, root, "hwmon0", "temp1_input", "45000")
	hwmontest.WriteAttr(t, root, "hwmon0", "temp1_label", "Package id 0")
	hwmontest.WriteAttr(t, root, "hwmon0", "temp2_input", "47000")
	hwmontest.WriteAttr(t, root, "hwmon0", "temp3_input", "44000")
	hwmontest.WriteAttr(t, root, "hwmon0", "temp10_input", "52000")
	hwmontest.WriteAttr(t, root, "hwmon0", "temp1_crit", "100000")
	hwmontest.WriteAttr(t, root, "hwmon1", "name", "coretemp")
	hwmontest.WriteAttr(t, root, "hwmon1", "temp1_input", "49000")
	fs := &hwmon.FS{Root: root}
	src, err := sensor.Parse("coretemp", fs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := src.Read(); err != nil || v != 52000 {
		t.Fatalf("coretemp = %d, %v; want 52000", v, err)
	}
}

func TestKnown(t *testing.T) {
	fs, dev := n5pro(t)
	infos := sensor.Known(fs, dev)
	ids := map[string]string{}
	for _, i := range infos {
		ids[i.ID] = i.Description
	}
	for _, want := range []string{"k10temp", "nvme:max", "drivetemp:max", "ec:cpu", "ec:system", "ec:board", "ec:ambient",
		"hwmon:amdgpu:temp1", "hwmon:nic1:temp2", "hwmon:minisforum_n5_it5571:temp4", "hwmon:<name>:tempN", "ec:<label>"} {
		if _, ok := ids[want]; !ok {
			t.Errorf("Known lacks %s", want)
		}
	}
	if _, ok := ids["coretemp"]; ok {
		t.Error("coretemp must not be listed on an AMD box")
	}
	if !strings.Contains(ids["k10temp"], "37.1 C") {
		t.Errorf("k10temp description lacks reading: %q", ids["k10temp"])
	}
	if !strings.Contains(ids["hwmon:amdgpu:temp1"], "edge") {
		t.Errorf("label missing: %q", ids["hwmon:amdgpu:temp1"])
	}
	// every concrete id must round-trip through Parse
	for _, i := range infos {
		if strings.Contains(i.ID, "<") {
			continue
		}
		if _, err := sensor.Parse(i.ID, fs, dev); err != nil {
			t.Errorf("Known id %s does not parse: %v", i.ID, err)
		}
	}
	// without a device the ec ids are absent but nothing breaks
	for _, i := range sensor.Known(fs, nil) {
		if strings.HasPrefix(i.ID, "ec:") && !strings.Contains(i.ID, "<") {
			t.Errorf("ec id %s listed without device", i.ID)
		}
	}
}
