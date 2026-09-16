package alert

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTemplateFiles(t *testing.T) {
	files := TemplateFiles()
	if len(files) != 2 {
		t.Fatalf("embedded templates: %v", files)
	}
	for _, name := range []string{"n5-fangov-subject.txt.hbs", "n5-fangov-body.txt.hbs"} {
		if len(files[name]) == 0 {
			t.Errorf("%s missing or empty", name)
		}
	}
	// the deploy copies must be identical (install.sh uses them)
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join("..", "..", "deploy", "pve-notification", name))
		if err != nil {
			t.Errorf("deploy copy: %v", err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("deploy/pve-notification/%s differs from the embedded copy", name)
		}
	}
	if !strings.Contains(string(files["n5-fangov-body.txt.hbs"]), "{{when}}") {
		t.Errorf("body template must use {{when}} (see perlProgram)")
	}
}

func TestTemplateStatusAndInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes / rename semantics")
	}
	root := t.TempDir()
	notify := filepath.Join(root, "Notify.pm")
	dir := filepath.Join(root, "etc", "pve", "templates")

	// no PVE: unsupported, status says so
	if _, err := installTemplateIn(dir, notify); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("without Notify.pm: %v", err)
	}
	inst, cur, wr, reason := templateStatusIn(dir, notify)
	if inst || cur || wr || !strings.Contains(reason, "PVE::Notify absent") {
		t.Errorf("status without PVE: %v %v %v %q", inst, cur, wr, reason)
	}
	_ = os.WriteFile(notify, []byte("package PVE::Notify;\n"), 0o644)

	// PVE, directory missing: not writable (the daemon cannot create it), install creates it
	_, _, wr, reason = templateStatusIn(dir, notify)
	if wr || !strings.Contains(reason, "does not exist") {
		t.Errorf("missing dir: %v %q", wr, reason)
	}
	got, err := installTemplateIn(dir, notify)
	if err != nil || got != dir {
		t.Fatalf("install: %v %s", err, got)
	}
	inst, cur, wr, reason = templateStatusIn(dir, notify)
	if !inst || !cur || !wr || reason != "" {
		t.Errorf("after install: %v %v %v %q", inst, cur, wr, reason)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("temp files left behind: %v", entries)
	}
	st, _ := os.Stat(filepath.Join(dir, "n5-fangov-body.txt.hbs"))
	if st.Mode().Perm() != 0o640 {
		t.Errorf("mode %04o, want 0640", st.Mode().Perm())
	}

	// stale copy: installed but not current
	_ = os.WriteFile(filepath.Join(dir, "n5-fangov-body.txt.hbs"), []byte("old"), 0o640)
	inst, cur, _, _ = templateStatusIn(dir, notify)
	if !inst || cur {
		t.Errorf("stale: installed=%v current=%v", inst, cur)
	}
	if _, err := installTemplateIn(dir, notify); err != nil {
		t.Fatal(err)
	}
	if _, cur, _, _ = templateStatusIn(dir, notify); !cur {
		t.Errorf("reinstall must make it current")
	}

	// read-only directory: not writable, reason names it
	if os.Getuid() != 0 {
		_ = os.Chmod(dir, 0o500)
		defer os.Chmod(dir, 0o700)
		if _, _, wr, reason := templateStatusIn(dir, notify); wr || !strings.Contains(reason, "not writable") {
			t.Errorf("read-only dir: %v %q", wr, reason)
		}
	}
}
