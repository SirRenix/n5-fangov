package profile

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
)

// N5ProHwmonName is the name the minisforum_n5_it5571 driver registers.
const N5ProHwmonName = "minisforum_n5_it5571"

// n5pro is the hardware-verified profile for the Minisforum N5 Pro EC.
type n5pro struct{}

// N5Pro returns the n5pro profile.
func N5Pro() Profile { return n5pro{} }

func (n5pro) Name() string   { return "n5pro" }
func (n5pro) Title() string  { return "Minisforum N5 Pro (IT5571 EC)" }
func (n5pro) Verified() bool { return true }
func (n5pro) Notes() string {
	return "Hardware-verified on a Minisforum N5 Pro (BIOS 1.05, driver minisforum_n5_it5571, 2026-09-14). " +
		"Channels: pwm1 CPU, pwm2 SSD, pwm3 HDD, pwm4 PCIe (no tachometer). " +
		"Writing pwmN_enable=1 makes the driver set the duty to 255 first. " +
		"EC does not resume automatic regulation of the HDD channel (pwm3) after any write - " +
		"use a fixed stop duty; measured 2026-09-14 on BIOS 1.05"
}

// n5proChannels is the fixed channel table; fanN_label of the driver is
// informational only ("PCIe Fan (no tach)").
var n5proChannels = []Channel{
	{Index: 1, Label: "CPU Fan", HasTach: true},
	{Index: 2, Label: "SSD Fan", HasTach: true},
	{Index: 3, Label: "HDD Fan", HasTach: true},
	{Index: 4, Label: "PCIe Fan", HasTach: false},
}

// Detect looks for the EC hwmon device and checks that all four pwm
// channels are present. It never writes.
func (p n5pro) Detect(fs *hwmon.FS) (Device, error) {
	devs := fs.FindByName(N5ProHwmonName)
	if len(devs) == 0 {
		return nil, fmt.Errorf("%w: no hwmon named %s", ErrNotFound, N5ProHwmonName)
	}
	hw := devs[0]
	for _, c := range n5proChannels {
		for _, a := range []string{fmt.Sprintf("pwm%d", c.Index), fmt.Sprintf("pwm%d_enable", c.Index)} {
			if !fs.Exists(filepath.Join(hw.Path, a)) {
				return nil, fmt.Errorf("profile n5pro: %s lacks %s", hw.Path, a)
			}
		}
	}
	return &pwmDevice{
		prof:      p,
		fs:        fs,
		path:      hw.Path,
		chans:     append([]Channel(nil), n5proChannels...),
		extra:     n5proExtraTemps(fs, hw.Path),
		autoValue: func(int) string { return "2" },
	}, nil
}

// n5proExtraTemps maps the EC's temp1..4 to ids derived from their labels:
// "CPU Temp" -> ec:cpu, "System Temp" -> ec:system, "Board Temp" -> ec:board,
// "Ambient Temp" -> ec:ambient. A missing label falls back to ec:tempN.
func n5proExtraTemps(fs *hwmon.FS, path string) map[string]string {
	out := map[string]string{}
	for n := 1; n <= 4; n++ {
		input := filepath.Join(path, fmt.Sprintf("temp%d_input", n))
		if !fs.Exists(input) {
			continue
		}
		id := fmt.Sprintf("temp%d", n)
		if label, err := fs.ReadString(filepath.Join(path, fmt.Sprintf("temp%d_label", n))); err == nil && label != "" {
			id = ecLabelID(label)
		}
		out["ec:"+id] = input
	}
	return out
}

// ecLabelID turns "System Temp" into "system".
func ecLabelID(label string) string {
	s := strings.ToLower(strings.TrimSpace(label))
	s = strings.TrimSuffix(s, " temp")
	s = strings.TrimSuffix(s, " temperature")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_':
			b.WriteRune('_')
		}
	}
	return b.String()
}
