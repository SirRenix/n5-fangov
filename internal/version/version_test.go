package version

import "testing"

// Prerelease is the text after the first "-" of Version, with a leading
// "v" (git describe) stripped; a release build yields "".
func TestPrerelease(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	cases := []struct {
		version, want string
	}{
		{"0.3.0", ""},
		{"0.3.0-beta.1", "beta.1"},
		{"v0.3.0-rc1-3-gabc", "rc1-3-gabc"}, // git describe after a tag: everything after the first "-"
		{"v0.3.0", ""},
		{"0.0.0-dev", "dev"},
		{"", ""},
		{"v", ""},
		{"-x", "x"},
	}
	for _, c := range cases {
		Version = c.version
		if got := Prerelease(); got != c.want {
			t.Errorf("Prerelease() with Version %q = %q, want %q", c.version, got, c.want)
		}
	}
	// the build default must be a parseable semantic version with an
	// optional pre-release suffix (no "v" prefix: the Makefile adds none)
	Version = old
	if old == "" || old[0] == 'v' {
		t.Errorf("default Version %q must be bare (no v prefix)", old)
	}
}
