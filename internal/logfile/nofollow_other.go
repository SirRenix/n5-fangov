//go:build !unix

package logfile

// openNoFollow: no O_NOFOLLOW outside unix; the Lstat check remains.
const openNoFollow = 0
