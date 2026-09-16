// Package fsutil holds the one atomic file writer every store in the daemon
// uses (config, presets, sessions, alert ring, PVE templates, TLS keys,
// override and state files, bundle import). One implementation, one
// semantics: unpredictable temp file in the target directory, write,
// fsync, chmod, close, rename; nothing of the target changes on error and
// the temp file is removed.
package fsutil

import (
	"os"
	"path/filepath"
)

// ModeKeep as perm leaves the temp file's mode as os.CreateTemp made it
// (0600) instead of calling chmod — for file systems that refuse chmod
// (pmxcfs reports every file as 0640 root:www-data).
const ModeKeep os.FileMode = 0

// WriteAtomic writes data to path via a temp file in the same directory
// and renames it over path. perm is applied to the temp file before the
// rename (ModeKeep: no chmod).
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := Stage(path, data, perm)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Stage writes data to a fresh temp file next to path (write, fsync,
// chmod, close) and returns its name; the caller renames it — the bundle
// import stages every file before the first rename so a failure leaves
// nothing half-written. On error the temp file is removed.
func Stage(path string, data []byte, perm os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if perm != ModeKeep {
		if err := f.Chmod(perm); err != nil {
			return fail(err)
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}
