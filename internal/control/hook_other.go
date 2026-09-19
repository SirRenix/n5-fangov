//go:build !unix

package control

import (
	"os"
	"os/exec"
)

// fileOwner is unknown off unix; the ownership rule is skipped there.
func fileOwner(os.FileInfo) (int, bool) { return 0, false }

// setProcessGroup is a no-op off unix (the daemon only runs on Linux).
func setProcessGroup(*exec.Cmd) {}
