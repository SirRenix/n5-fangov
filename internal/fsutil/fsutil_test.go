package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := WriteAtomic(p, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(p, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "two\n" {
		t.Fatalf("content %q %v", b, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode %04o, want 0600", st.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
	// ModeKeep: CreateTemp's 0600 stays
	if err := WriteAtomic(p, []byte("three\n"), ModeKeep); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0o600 {
			t.Errorf("ModeKeep mode %04o", st.Mode().Perm())
		}
	}
}

// TestWriteAtomicFailureLeavesTarget: a missing directory fails before
// anything is created; a target that cannot be replaced keeps its content
// and no temp file survives.
func TestWriteAtomicFailureLeavesTarget(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAtomic(filepath.Join(dir, "nope", "f"), []byte("x"), 0o644); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// rename over a directory fails: the target must be untouched
	d := filepath.Join(dir, "d")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "child"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(d, []byte("x"), 0o644); err == nil {
		t.Fatal("rename over a non-empty directory succeeded")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "keep\n" {
		t.Errorf("unrelated file changed: %q", b)
	}
}

func TestStage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "preset.toml")
	tmp, err := Stage(p, []byte("[[channel]]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(tmp) != dir || !strings.HasPrefix(filepath.Base(tmp), ".preset.toml.") {
		t.Errorf("staged at %s", tmp)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("Stage renamed the file")
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "[[channel]]\n" {
		t.Errorf("content %q", b)
	}
}
