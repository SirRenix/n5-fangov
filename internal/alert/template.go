package alert

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// The PVE notification template pair (subject + body) is embedded here so
// the daemon and the CLI can install and compare it without any file from
// the package. deploy/pve-notification/ holds the same two files for
// install.sh (shell cannot read the binary's embed); `make verify-deploy`
// diffs the two copies.
//
//go:embed templates/*.hbs
var templateFS embed.FS

// TemplatePath is the PVE override directory for notification templates:
// files there take precedence over /usr/share/pve-manager/templates/default
// and live on pmxcfs (cluster-wide, modes ignored).
const TemplatePath = "/etc/pve/notification-templates/default"

// TemplateFiles returns the embedded template files by name
// (n5-fangov-subject.txt.hbs, n5-fangov-body.txt.hbs).
func TemplateFiles() map[string][]byte {
	out := map[string][]byte{}
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if b, err := templateFS.ReadFile(path.Join("templates", e.Name())); err == nil {
			out[e.Name()] = b
		}
	}
	return out
}

// templateNames lists the embedded template file names, sorted.
func templateNames() []string {
	files := TemplateFiles()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// TemplateStatus compares the installed template pair in TemplatePath with
// the embedded one: installed = both files exist, current = both equal
// the embedded text, writable = a temp file can be created and removed in
// the directory (pmxcfs, sandbox ReadWritePaths). reason explains a
// false writable (no PVE, directory missing, permission).
func TemplateStatus() (installed, current, writable bool, reason string) {
	return templateStatusIn(TemplatePath, pveNotifyPM)
}

func templateStatusIn(dir, notifyPM string) (installed, current, writable bool, reason string) {
	files := TemplateFiles()
	installed, current = true, true
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			installed, current = false, false
			continue
		}
		if !bytes.Equal(got, want) {
			current = false
		}
	}
	if _, err := os.Stat(notifyPM); err != nil {
		return installed, current, false, "not a Proxmox VE host (PVE::Notify absent)"
	}
	st, err := os.Stat(dir)
	switch {
	case err != nil:
		return installed, current, false, dir + " does not exist; create it as root: mkdir -p " + dir + " (or run: n5-fangov alerts template)"
	case !st.IsDir():
		return installed, current, false, dir + " is not a directory"
	}
	f, err := os.CreateTemp(dir, ".n5-fangov-probe-*")
	if err != nil {
		return installed, current, false, fmt.Sprintf("%s not writable: %v", dir, err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return installed, current, false, fmt.Sprintf("%s: probe file could not be removed: %v", dir, err)
	}
	return installed, current, true, ""
}

// InstallTemplate writes the embedded template pair into TemplatePath
// (temp file + rename per file, mode 0640 — pmxcfs ignores modes anyway)
// and returns the directory. errors.ErrUnsupported when PVE::Notify is
// absent (not a Proxmox host). The directory is created when missing;
// inside the daemon's sandbox that fails on a fresh box (ProtectSystem=
// strict) and the error says to run `n5-fangov alerts template` as root.
func InstallTemplate() (string, error) {
	return installTemplateIn(TemplatePath, pveNotifyPM)
}

func installTemplateIn(dir, notifyPM string) (string, error) {
	if _, err := os.Stat(notifyPM); err != nil {
		return "", fmt.Errorf("PVE notification templates: not a Proxmox VE host (%s absent): %w", notifyPM, errors.ErrUnsupported)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w (run `n5-fangov alerts template` as root outside the service sandbox)", dir, err)
	}
	files := TemplateFiles()
	for _, name := range templateNames() {
		if err := writeTemplate(filepath.Join(dir, name), files[name]); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// writeTemplate writes data to path atomically inside its directory. The
// chmod is best effort: pmxcfs reports every file as 0640 root:www-data
// and refuses chmod.
func writeTemplate(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("%s: %w", path, err)
	}
	_ = os.Chmod(name, 0o640)
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
