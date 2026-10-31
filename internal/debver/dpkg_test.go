package debver

import (
	"errors"
	"os/exec"
	"testing"
)

// dpkgVersions are real versions this tool meets (driver package, kernel
// packages, n5-fangov itself) plus edge cases of deb-version(7): epochs,
// revisions, "~" before end, "+" and "." after, letters before non-letters,
// leading zeros, hyphens inside the upstream part.
var dpkgVersions = []string{
	"0", "0.0", "0~", "0~~", "0~a", "00", "0a", "0+", "0.", "0-0", "0-1",
	"1", "1.0", "1.0~", "1.0~~", "1.0~rc1", "1.0~rc10", "1.0~rc2", "1.0+", "1.0+1", "1.0.", "1.0a", "1.0A", "1.0z", "1.0-0", "1.0-1", "1.0-1.1", "1.0-1~bpo1", "1.0-1+b1",
	"1.00", "1.01", "1.1", "1.10", "1.9", "1.0.0", "1:0", "1:1.0", "2:0.1", "0:1.0",
	"0.2.0", "0.2.1", "0.2.1+sr1~rc1", "0.2.1+sr1~rc2", "0.2.1+sr1", "0.2.1+sr2~rc1", "0.2.1+sr2", "0.2.2", "0.10.0",
	"0.2.1-1", "0.2.1+sr1~rc1-1", "0.2.1+sr1-1", "0.2.1+sr1-2",
	"0.4.2", "0.5.0~rc1", "0.5.0~rc4", "0.5.0", "0.5.0+1+gabc", "0.5.0~rc4+3+g505516f",
	"6.17.2-1-pve", "7.0.12-1", "7.0.12-1-pve", "7.0.14-19-pve", "7.0.14-20-pve", "7.0.14-20", "7.0.14-2-pve",
	"2.4.13-1", "2.4.13-1~bpo12+1", "1:2.4.13-1", "9.2p1-2+deb12u3", "10.0p1-1",
	"1.2-3-4", "1.2-3-4-5", "1abc2", "1.2.3a.4~b",
}

// TestCompareMatchesDpkg compares every pair with dpkg --compare-versions.
// It runs where dpkg exists (the Debian build container) and is skipped
// elsewhere.
func TestCompareMatchesDpkg(t *testing.T) {
	dpkg, err := exec.LookPath("dpkg")
	if err != nil {
		t.Skip("dpkg not found")
	}
	// rel asks dpkg whether "a op b" holds: exit 0 yes, 1 no, else error.
	rel := func(a, op, b string) bool {
		err := exec.Command(dpkg, "--compare-versions", a, op, b).Run()
		var ee *exec.ExitError
		switch {
		case err == nil:
			return true
		case errors.As(err, &ee) && ee.ExitCode() == 1:
			return false
		default:
			t.Fatalf("dpkg --compare-versions %q %s %q: %v", a, op, b, err)
			return false
		}
	}
	pairs := 0
	for i, a := range dpkgVersions {
		for _, b := range dpkgVersions[i:] {
			want := 1
			switch {
			case rel(a, "lt", b):
				want = -1
			case rel(a, "eq", b):
				want = 0
			}
			if got := Compare(a, b); got != want {
				t.Errorf("Compare(%q, %q) = %d, dpkg says %d", a, b, got, want)
			}
			if got := Compare(b, a); got != -want {
				t.Errorf("Compare(%q, %q) = %d, dpkg says %d", b, a, got, -want)
			}
			pairs++
		}
	}
	t.Logf("%d versions, %d pairs checked against %s", len(dpkgVersions), pairs, dpkg)
}
