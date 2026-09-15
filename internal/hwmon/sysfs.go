package hwmon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ClassDir returns <Root>/class/hwmon.
func (fs *FS) ClassDir() string {
	return filepath.Join(fs.Root, "class", "hwmon")
}

// Abs resolves p against Root when p is relative. Absolute paths (as returned
// by List) are used as-is.
func (fs *FS) Abs(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(fs.Root, p)
}

// List enumerates <Root>/class/hwmon/hwmon* and reads each device's name.
// Devices are returned ordered by their numeric index. Entries without a
// readable name file are skipped. An empty class directory is not an error;
// a missing one is.
func (fs *FS) List() ([]Device, error) {
	dir := fs.ClassDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("hwmon: list %s: %w", dir, err)
	}
	type numbered struct {
		n   int
		dev Device
	}
	var found []numbered
	for _, e := range entries {
		n, ok := hwmonIndex(e.Name())
		if !ok {
			continue
		}
		p := filepath.Join(dir, e.Name())
		name, err := fs.ReadString(filepath.Join(p, "name"))
		if err != nil || name == "" {
			continue
		}
		found = append(found, numbered{n, Device{Path: p, Name: name}})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
	out := make([]Device, 0, len(found))
	for _, f := range found {
		out = append(out, f.dev)
	}
	return out, nil
}

// hwmonIndex parses "hwmon14" -> 14.
func hwmonIndex(name string) (int, bool) {
	if !strings.HasPrefix(name, "hwmon") {
		return 0, false
	}
	n, err := strconv.Atoi(name[len("hwmon"):])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// FindByName returns all devices whose name file equals name.
func (fs *FS) FindByName(name string) []Device {
	devs, err := fs.List()
	if err != nil {
		return nil
	}
	var out []Device
	for _, d := range devs {
		if d.Name == name {
			out = append(out, d)
		}
	}
	return out
}

// FindByRegexp returns all devices whose name matches re.
func (fs *FS) FindByRegexp(re *regexp.Regexp) []Device {
	devs, err := fs.List()
	if err != nil {
		return nil
	}
	var out []Device
	for _, d := range devs {
		if re.MatchString(d.Name) {
			out = append(out, d)
		}
	}
	return out
}

// Exists reports whether path exists (file or directory).
func (fs *FS) Exists(path string) bool {
	_, err := os.Stat(fs.Abs(path))
	return err == nil
}

// ReadString reads a sysfs attribute and returns its content with surrounding
// whitespace (including the trailing newline) removed.
func (fs *FS) ReadString(path string) (string, error) {
	b, err := os.ReadFile(fs.Abs(path))
	if err != nil {
		return "", fmt.Errorf("hwmon: read %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// ReadInt reads a sysfs attribute as a decimal integer.
func (fs *FS) ReadInt(path string) (int, error) {
	s, err := fs.ReadString(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("hwmon: %s: not an integer: %q", path, s)
	}
	return n, nil
}

// ErrVerify is wrapped by WriteVerify when the read-back differs from the
// written value.
var ErrVerify = errors.New("hwmon: read-back mismatch")

// WriteVerify writes value to a sysfs attribute and reads it back. The file
// is opened write-only with truncate (like `echo > attr`; a no-op on sysfs,
// required on the regular files of a fake tree) and never created. Integers
// are compared numerically, everything else as trimmed strings.
func (fs *FS) WriteVerify(path, value string) error {
	abs := fs.Abs(path)
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("hwmon: open %s for write: %w", path, err)
	}
	_, werr := f.WriteString(value)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("hwmon: write %q to %s: %w", value, path, werr)
	}
	if cerr != nil {
		return fmt.Errorf("hwmon: close %s after write: %w", path, cerr)
	}
	return fs.VerifyValue(abs, value)
}

// VerifyValue reads path and returns ErrVerify (wrapped) unless its content
// equals want. Used by WriteVerify; also useful to re-check a channel's
// mode without writing.
func (fs *FS) VerifyValue(path, want string) error {
	got, err := fs.ReadString(path)
	if err != nil {
		return fmt.Errorf("hwmon: verify %s: %w", path, err)
	}
	if !sameValue(strings.TrimSpace(want), got) {
		return fmt.Errorf("%w: %s: want %q, read %q", ErrVerify, path, want, got)
	}
	return nil
}

func sameValue(want, got string) bool {
	if want == got {
		return true
	}
	a, err1 := strconv.Atoi(want)
	b, err2 := strconv.Atoi(got)
	return err1 == nil && err2 == nil && a == b
}
