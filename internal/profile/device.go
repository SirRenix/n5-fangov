package profile

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/SirRenix/pvefand/internal/hwmon"
)

// ErrNotFound is returned by Detect when no matching hwmon device exists.
var ErrNotFound = errors.New("profile: device not found")

// ErrMonitorOnly is returned by every write method of the monitor profile.
var ErrMonitorOnly = errors.New("profile: monitoring only, no fan control")

// pwmDevice is the shared implementation for hwmon chips that expose
// pwmN / pwmN_enable / fanN_input. Profiles differ only in detection, the
// channel table, the value that means "automatic" for pwmN_enable, and the
// extra temperature sensors they export.
type pwmDevice struct {
	prof  Profile
	fs    *hwmon.FS
	path  string
	chans []Channel
	extra map[string]string
	// autoValue returns the pwmN_enable value that hands the channel back
	// to the chip's own regulation (SafeStop "auto").
	autoValue func(ch int) string
}

func (d *pwmDevice) Profile() Profile    { return d.prof }
func (d *pwmDevice) HwmonPath() string   { return d.path }
func (d *pwmDevice) Channels() []Channel { return append([]Channel(nil), d.chans...) }

func (d *pwmDevice) ExtraTemps() map[string]string {
	out := make(map[string]string, len(d.extra))
	for k, v := range d.extra {
		out[k] = v
	}
	return out
}

func (d *pwmDevice) attr(name string) string { return filepath.Join(d.path, name) }

func (d *pwmDevice) channel(ch int) (Channel, error) {
	for _, c := range d.chans {
		if c.Index == ch {
			return c, nil
		}
	}
	return Channel{}, fmt.Errorf("profile %s: no channel pwm%d", d.prof.Name(), ch)
}

// ReadRPM returns fanN_input. Channels without tach return -1 and no error
// so a missing tach never trips the controller's error path.
func (d *pwmDevice) ReadRPM(ch int) (int, error) {
	c, err := d.channel(ch)
	if err != nil {
		return 0, err
	}
	if !c.HasTach {
		return -1, nil
	}
	return d.fs.ReadInt(d.attr(fmt.Sprintf("fan%d_input", ch)))
}

func (d *pwmDevice) ReadDuty(ch int) (int, error) {
	if _, err := d.channel(ch); err != nil {
		return 0, err
	}
	return d.fs.ReadInt(d.attr(fmt.Sprintf("pwm%d", ch)))
}

// ReadEnable returns the current pwmN_enable value as a string.
func (d *pwmDevice) ReadEnable(ch int) (string, error) {
	if _, err := d.channel(ch); err != nil {
		return "", err
	}
	return d.fs.ReadString(d.attr(fmt.Sprintf("pwm%d_enable", ch)))
}

// EnterManual writes pwmN_enable=1 only when the channel is not already in
// manual mode (a redundant write on the N5 Pro EC forces the fan to 255).
func (d *pwmDevice) EnterManual(ch int) error {
	cur, err := d.ReadEnable(ch)
	if err != nil {
		return err
	}
	if cur == "1" {
		return nil
	}
	return d.fs.WriteVerify(d.attr(fmt.Sprintf("pwm%d_enable", ch)), "1")
}

// WriteDuty ensures manual mode (DESIGN rule 2), writes pwmN and reads it
// back (rule 3).
func (d *pwmDevice) WriteDuty(ch int, duty int) error {
	if duty < 0 || duty > 255 {
		return fmt.Errorf("profile %s: duty %d out of range 0..255", d.prof.Name(), duty)
	}
	if err := d.EnterManual(ch); err != nil {
		return err
	}
	return d.fs.WriteVerify(d.attr(fmt.Sprintf("pwm%d", ch)), strconv.Itoa(duty))
}

// SafeStop hands the channel back to the chip ("auto") or pins it to a
// fixed duty ("0".."255").
func (d *pwmDevice) SafeStop(ch int, stop string) error {
	if _, err := d.channel(ch); err != nil {
		return err
	}
	if stop == "auto" {
		return d.fs.WriteVerify(d.attr(fmt.Sprintf("pwm%d_enable", ch)), d.autoValue(ch))
	}
	duty, err := strconv.Atoi(stop)
	if err != nil || duty < 0 || duty > 255 {
		return fmt.Errorf("profile %s: invalid stop value %q (want \"auto\" or 0..255)", d.prof.Name(), stop)
	}
	return d.WriteDuty(ch, duty)
}

var pwmFileRe = regexp.MustCompile(`^pwm([0-9]+)$`)

// scanChannels lists pwmN attributes of a hwmon device and builds the channel
// table: label from fanN_label when present, HasTach when fanN_input exists.
func scanChannels(fs *hwmon.FS, path string) ([]Channel, error) {
	entries, err := filepath.Glob(filepath.Join(path, "pwm*"))
	if err != nil {
		return nil, err
	}
	var chans []Channel
	for _, e := range entries {
		m := pwmFileRe.FindStringSubmatch(filepath.Base(e))
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if !fs.Exists(filepath.Join(path, fmt.Sprintf("pwm%d_enable", n))) {
			continue // not controllable without an enable attribute
		}
		label := fmt.Sprintf("PWM %d", n)
		if s, err := fs.ReadString(filepath.Join(path, fmt.Sprintf("fan%d_label", n))); err == nil && s != "" {
			label = s
		}
		chans = append(chans, Channel{
			Index:   n,
			Label:   label,
			HasTach: fs.Exists(filepath.Join(path, fmt.Sprintf("fan%d_input", n))),
		})
	}
	sort.Slice(chans, func(i, j int) bool { return chans[i].Index < chans[j].Index })
	return chans, nil
}
