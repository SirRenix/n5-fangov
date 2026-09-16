// Package version holds the build version string (set via -ldflags at release time).
package version

import "strings"

// Version is the semantic version of this build. A pre-release carries a
// hyphenated suffix ("0.3.0-beta.1"); `make` overrides it with `git describe`
// through -X, which yields "v0.3.0-beta.1-3-gabcdef" on commits after a tag.
var Version = "0.3.0-rc1"

// Prerelease returns the text after the first "-" of Version ("beta.1"),
// or "" for a release build. The UI shows it as a badge, GET /api/about
// and /api/version carry it verbatim.
func Prerelease() string {
	v := strings.TrimPrefix(Version, "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[i+1:]
	}
	return ""
}
