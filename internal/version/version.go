// Package version holds the build version string (set via -ldflags at release time).
package version

import "strings"

// Version is the semantic version of this build. A pre-release carries a
// hyphenated suffix ("0.3.0-beta.1"); `make` overrides it with `git describe`
// through -X, which yields "v0.3.0-beta.1-3-gabcdef" on commits after a tag.
var Version = "0.4.0-rc2"

// A leading "v" (a tag name handed to -X by mistake, or git describe) is
// never part of the version: `n5-fangov version`, /api/version, the webhook
// User-Agent and the alert texts print "0.4.0-rc2"; the dashboard adds the
// "v" for display (DESIGN §12, release-gate finding 2026-09-18).
func init() { Version = strings.TrimPrefix(Version, "v") }

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
