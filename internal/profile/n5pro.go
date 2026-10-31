package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/n5driver"
)

// N5ProHwmonName is the name the minisforum_n5_it5571 driver registers.
const N5ProHwmonName = n5driver.ModuleName

// ErrWriteDisabled is returned by the n5pro profile when the driver is
// loaded and registered its hwmon device but hides the pwm nodes (driver in
// read-only mode). Auto-detection does not fall back to monitor then: a
// box that has the N5 Pro driver and silently only monitors is a fault.
var ErrWriteDisabled = errors.New("profile n5pro: driver loaded read-only (pwm nodes hidden)")

// ErrDCRMismatch is returned by VerifyRegister when the EC duty register
// read through the driver's debugfs dump differs from the written duty. It does not wrap
// hwmon.ErrVerify: the driver accepted the value (the sysfs read-back
// passed), only the register shows something else. The daemon reports it
// as its own warning, not as a write error; validate records it.
var ErrDCRMismatch = errors.New("EC duty register differs from the written duty")

// RegisterVerifier is a Device with a hardware register behind its sysfs
// duty cache (n5pro with the driver's debugfs dump). VerifyRegister reads
// once and returns nil or ErrDCRMismatch; RegisterSettle is how long the
// chip may take to show a written duty (0: nothing to check).
type RegisterVerifier interface {
	VerifyRegister(ch, duty int) error
	RegisterSettle(ch int) time.Duration
}

// RegisterPoll is the pause between two register reads of WaitRegister.
var RegisterPoll = 200 * time.Millisecond

// WaitRegister reads the register of ch until it equals duty or deadline
// has passed, and returns the last result (nil or ErrDCRMismatch). For
// callers that may block (validate, the failsafe command); the daemon
// compares in a later cycle instead. now and sleep make it testable.
func WaitRegister(v RegisterVerifier, ch, duty int, deadline time.Time, now func() time.Time, sleep func(time.Duration)) error {
	for {
		err := v.VerifyRegister(ch, duty)
		if err == nil || !now().Before(deadline) {
			return err
		}
		sleep(RegisterPoll)
	}
}

// n5pro is the profile for the Minisforum N5 Pro EC (IT5571) through the
// minisforum_n5_it5571 driver. Its hardware status (internal/hwstatus)
// depends on board, BIOS and EC firmware, not on the profile.
type n5pro struct{}

// N5Pro returns the n5pro profile.
func N5Pro() Profile { return n5pro{} }

func (n5pro) Name() string  { return "n5pro" }
func (n5pro) Title() string { return "Minisforum N5 Pro (IT5571 EC)" }
func (n5pro) Notes() string {
	return "Channels: pwm1 CPU, pwm2 SSD, pwm3 HDD, pwm4 PCIe (no tachometer); mapping measured 2026-09-14 " +
		"on an N5 Pro (F8NAA, BIOS 1.05). Writing pwmN_enable=1 makes the driver set the duty to 255 first. " +
		"After the hand-back to EC automatic the EC regulates the HDD channel (pwm3) to a target speed from an EC table keyed on EC 0x06 (derived from firmware); " +
		"whether that follows disk temperature is open, so n5-fangov keeps a fixed HDD stop duty. " +
		"The driver's pwmN is a cache, not the EC register."
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
// channels are present. It never writes. A driver that registered its
// hwmon device without any pwm node runs read-only: ErrWriteDisabled,
// with what sysfs says about the reason.
func (p n5pro) Detect(fs *hwmon.FS) (Device, error) {
	devs := fs.FindByName(N5ProHwmonName)
	if len(devs) == 0 {
		return nil, fmt.Errorf("%w: no hwmon named %s", ErrNotFound, N5ProHwmonName)
	}
	hw := devs[0]
	if !fs.Exists(filepath.Join(hw.Path, "pwm1")) && !fs.Exists(filepath.Join(hw.Path, "pwm1_enable")) {
		st := n5driver.Read(fs.Root)
		why := strings.Join(st.Findings(), "; ")
		if why == "" {
			why = "the driver hides the pwm nodes"
		}
		return nil, fmt.Errorf("%w at %s: %s. Fix the driver (package %s: options minisforum_n5_it5571 experimental_write=1 on a board in its write allowlist) or set [daemon] profile = \"monitor\" to run sensors only",
			ErrWriteDisabled, hw.Path, why, n5driver.PackageName)
	}
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
		hwVerify:  func(ch, duty int) error { return verifyDCR(n5driver.DCRPath(fs.Root), ch, duty) },
		settle:    n5proSettle,
	}, nil
}

// N5ProSettleHDD and N5ProSettle are the times the N5 Pro EC gets to show a
// written duty in its register (maintenance window 1, 2026-10-17): DCR5
// (PCIe) takes it within 0.22 s (Z1), the HDD channel ramps DCR3 and DCR4
// one after the other to the value (about 1.5 s and 4.6 s, Z2), DCR1/DCR2
// lag as well (C6: read at once 116 instead of 188, correct seconds later).
const (
	N5ProSettleHDD = 5 * time.Second
	N5ProSettle    = 2 * time.Second
)

func n5proSettle(ch int) time.Duration {
	if ch == 3 {
		return N5ProSettleHDD
	}
	return N5ProSettle
}

// verifyDCR is the optional second read-back of a duty write: the driver's
// pwmN only returns its own cache, so the read-back in hwmon.WriteVerify
// cannot see whether the EC holds the value. When the driver exposes the
// read-only register dump (0.2.1+sr1, debug_regs=1, validation only) the
// channel's DCR must equal the written duty once the EC had time to take it
// (n5proSettle). Without the file, or when it cannot be read
// (n5driver.ReadDCR, time-bounded) or parsed, nothing is checked: the dump
// is never required. One read, no wait; callers decide when to read
// (daemon: in a later cycle, validate/failsafe: WaitRegister).
func verifyDCR(path string, ch, duty int) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	d, err := n5driver.ReadDCR(path)
	if err != nil {
		return nil
	}
	v, ok := d.Channel(ch)
	if !ok || v == duty {
		return nil
	}
	n, _ := n5driver.ChannelDCR(ch)
	return fmt.Errorf("%w: pwm%d: EC register DCR%d reads %d after writing %d (%s)", ErrDCRMismatch, ch, n, v, duty, path)
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
