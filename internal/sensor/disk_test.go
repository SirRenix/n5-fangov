package sensor_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// writeUnder creates <root>/<rel> with content plus a trailing newline.
func writeUnder(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// diskTree is a writable copy of the n5pro tree with the block-device
// hwmons a Windows checkout cannot hold (plain directories, no symlinks):
// sda (SATA, drivetemp under device/hwmon/hwmon6), nvme0n1 (controller
// hwmon under device/hwmon3), sdb without a sensor, and virtual devices.
func diskTree(t *testing.T) (*hwmon.FS, string) {
	t.Helper()
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	writeUnder(t, root, "block/sda/device/model", "Example HDD 20TB   ")
	writeUnder(t, root, "block/sda/queue/rotational", "1")
	writeUnder(t, root, "block/sda/device/hwmon/hwmon6/name", "drivetemp")
	writeUnder(t, root, "block/sda/device/hwmon/hwmon6/temp1_input", "38000")
	writeUnder(t, root, "block/nvme0n1/device/hwmon3/name", "nvme")
	writeUnder(t, root, "block/nvme0n1/device/hwmon3/temp1_input", "43850")
	writeUnder(t, root, "block/sdb/device/model", "Example SSD")
	writeUnder(t, root, "block/sdb/queue/rotational", "0")
	writeUnder(t, root, "block/sdb/size", "1000")
	writeUnder(t, root, "block/md0/device/hwmon/hwmon9/temp1_input", "99000")
	return &hwmon.FS{Root: root}, root
}

func TestDiskSensor(t *testing.T) {
	fs, root := diskTree(t)
	for id, want := range map[string]int{"disk:sda": 38000, "disk:nvme0n1": 43850} {
		src, err := sensor.Parse(id, fs, nil)
		if err != nil {
			t.Fatalf("Parse(%s): %v", id, err)
		}
		if src.ID() != id {
			t.Errorf("ID() = %q", src.ID())
		}
		if v, err := src.Read(); err != nil || v != want {
			t.Errorf("%s: %d, %v; want %d", id, v, err, want)
		}
	}
	// no hwmon, unknown device, malformed ids
	for _, id := range []string{"disk:sdb", "disk:sdz"} {
		if _, err := sensor.Parse(id, fs, nil); !errors.Is(err, sensor.ErrNoDevice) {
			t.Errorf("Parse(%s) = %v, want ErrNoDevice", id, err)
		}
	}
	for _, id := range []string{"disk:", "disk:SDA", "disk:../sda", "disk:sda/x", "disk:sd a"} {
		if _, err := sensor.Parse(id, fs, nil); err == nil || errors.Is(err, sensor.ErrNoDevice) {
			t.Errorf("Parse(%q) = %v, want a malformed-id error", id, err)
		}
	}
	// the sensor follows the file
	writeUnder(t, root, "block/sda/device/hwmon/hwmon6/temp1_input", "41000")
	src, _ := sensor.Parse("disk:sda", fs, nil)
	if v, _ := src.Read(); v != 41000 {
		t.Errorf("after write: %d", v)
	}
	// hwmon helpers
	if p, err := fs.DiskHwmon("sda"); err != nil || filepath.Base(p) != "hwmon6" {
		t.Errorf("DiskHwmon(sda) = %s, %v", p, err)
	}
	if _, err := fs.DiskHwmon("sdb"); !errors.Is(err, hwmon.ErrNoDiskHwmon) {
		t.Errorf("DiskHwmon(sdb) = %v", err)
	}
	if _, err := fs.DiskHwmon("nope"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("DiskHwmon(nope) = %v", err)
	}
	if v, err := fs.DiskTemp("nvme0n1"); err != nil || v != 43850 {
		t.Errorf("DiskTemp = %d, %v", v, err)
	}
	if got := fs.BlockDevices(); strings.Join(got, " ") != "nvme0n1 sda sdb" {
		t.Errorf("BlockDevices = %v", got)
	}
}

func TestDiskKnown(t *testing.T) {
	fs, _ := diskTree(t)
	infos := sensor.Known(fs, nil)
	byID := map[string]sensor.Info{}
	var order []string
	for _, i := range infos {
		byID[i.ID] = i
		if strings.HasPrefix(i.ID, "disk:") {
			order = append(order, i.ID)
		}
	}
	if strings.Join(order, " ") != "disk:nvme0n1 disk:sda" {
		t.Fatalf("disk ids: %v (sdb has no sensor, md0 is virtual)", order)
	}
	if d := byID["disk:sda"]; d.Kind != "hdd" || !strings.HasPrefix(d.Description, "Example HDD 20TB (sda, drivetemp)") || !strings.Contains(d.Description, "now 38.0 C") {
		t.Errorf("sda: %+v", d)
	}
	if d := byID["disk:nvme0n1"]; d.Kind != "ssd" || !strings.HasPrefix(d.Description, "Example NVMe 1000GB (nvme0n1, nvme)") {
		t.Errorf("nvme0n1: %+v", d)
	}
	if byID["k10temp"].Kind != "" || byID["nvme:max"].Kind != "" {
		t.Errorf("kind must be empty outside disk:*")
	}
	// the generic patterns still close the list
	if n := len(infos); infos[n-2].ID != "hwmon:<name>:tempN" || infos[n-1].ID != "ec:<label>" {
		t.Errorf("tail: %+v", infos[n-2:])
	}
	if sensor.DiskKind(fs, "sdb") != "ssd" || sensor.DiskKind(fs, "nope") != "" || sensor.DiskKind(fs, "nvme9n9") != "ssd" {
		t.Errorf("DiskKind")
	}
}

func TestComposite(t *testing.T) {
	fs, root := diskTree(t)
	src, err := sensor.Parse(" drivetemp:max , disk:nvme0n1 ", fs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if src.ID() != "drivetemp:max,disk:nvme0n1" {
		t.Errorf("canonical id: %q", src.ID())
	}
	if v, err := src.Read(); err != nil || v != 43850 {
		t.Errorf("max = %d, %v; want 43850", v, err)
	}
	// the unreadable part is ignored while one part reads
	writeUnder(t, root, "block/nvme0n1/device/hwmon3/temp1_input", "garbage")
	if v, err := src.Read(); err != nil || v != 38000 {
		t.Errorf("with a broken part: %d, %v; want 38000", v, err)
	}
	// a part that does not resolve now is skipped; a composite in which no
	// part resolves carries the first part's error
	if s, err := sensor.Parse("disk:sdz,k10temp", fs, nil); err != nil || s.ID() != "disk:sdz,k10temp" {
		t.Errorf("one unresolved part: %v", err)
	} else if v, _ := s.Read(); v != 37125 {
		t.Errorf("read of the resolved part: %d", v)
	}
	if _, err := sensor.Parse("disk:sdz,coretemp", fs, nil); !errors.Is(err, sensor.ErrNoDevice) {
		t.Errorf("no part resolves: %v", err)
	}
	// shape errors
	for _, id := range []string{"k10temp,", ",k10temp", "k10temp,,nvme:max", "k10temp,k10temp", "a,b,c,d,e"} {
		if _, err := sensor.Parse(id, fs, nil); err == nil {
			t.Errorf("Parse(%q) must fail", id)
		}
	}
	// every readable part fails → error
	writeUnder(t, root, "block/sda/device/hwmon/hwmon6/temp1_input", "x")
	all, _ := sensor.Parse("disk:sda,disk:nvme0n1", fs, nil)
	if _, err := all.Read(); err == nil {
		t.Error("no readable part must fail")
	}
}
