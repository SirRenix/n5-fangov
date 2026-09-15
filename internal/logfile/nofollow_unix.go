//go:build unix

package logfile

import "syscall"

// openNoFollow is OR-ed into the open flags so a symlink placed at the log
// path between the Lstat check and the open is refused by the kernel (H2).
const openNoFollow = syscall.O_NOFOLLOW
