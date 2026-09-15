// Package hwmontest provides helpers for tests that need a fake sysfs tree.
// Tests must never modify testdata/; use Copy to get a writable clone.
package hwmontest

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// ModuleRoot walks up from the current working directory until it finds
// go.mod and returns that directory.
func ModuleRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// N5Pro returns the absolute path of the read-only fake tree
// testdata/sysfs/n5pro (mirrors n5host).
func N5Pro(t testing.TB) string {
	t.Helper()
	p := filepath.Join(ModuleRoot(t), "testdata", "sysfs", "n5pro")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fake sysfs tree missing: %v", err)
	}
	return p
}

// Copy clones the directory tree src into a fresh t.TempDir() and returns
// the clone's path. Symlinks are not followed.
func Copy(t testing.TB, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
	return dst
}

// WriteAttr creates <root>/class/hwmon/<dev>/<attr> with content plus a
// trailing newline, like a sysfs attribute. Parent directories are created.
func WriteAttr(t testing.TB, root, dev, attr, content string) {
	t.Helper()
	dir := filepath.Join(root, "class", "hwmon", dev)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, attr), []byte(content+"\n"), 0o644); err != nil {
		t.Fatalf("write %s/%s: %v", dev, attr, err)
	}
}

// ReadAttr returns the trimmed content of <root>/class/hwmon/<dev>/<attr>.
func ReadAttr(t testing.TB, root, dev, attr string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "class", "hwmon", dev, attr))
	if err != nil {
		t.Fatalf("read %s/%s: %v", dev, attr, err)
	}
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
