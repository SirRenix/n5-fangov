//go:build !unix

package ringfile

import "time"

// lockFile is a no-op where flock does not exist; n5-fangov runs on Linux.
func lockFile(string, time.Duration) (func(), error) { return func() {}, nil }
