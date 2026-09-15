//go:build !unix

package ipc

// setUmask is a no-op where there is no umask; returns 0.
func setUmask(int) int { return 0 }
