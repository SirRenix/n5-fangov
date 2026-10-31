package debver

import "testing"

// The chain from the driver's version scheme (packaging/UPSTREAM of the
// driver repository), ascending: every element is less than every later one.
func TestDriverVersionChain(t *testing.T) {
	chain := []string{"0.2.0", "0.2.1", "0.2.1+sr1~rc1", "0.2.1+sr1~rc2", "0.2.1+sr1", "0.2.1+sr2~rc1", "0.2.1+sr2", "0.2.2", "0.10.0"}
	for i := range chain {
		for j := range chain {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := Compare(chain[i], chain[j]); got != want {
				t.Errorf("Compare(%q, %q) = %d, want %d", chain[i], chain[j], got, want)
			}
		}
	}
}

// Cases checked against dpkg --compare-versions semantics (deb-version(7));
// dpkg_test.go compares a larger list against dpkg itself.
func TestCompareTable(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0~rc1", "1.0", -1},
		{"1.0~~", "1.0~", -1},
		{"1.0~", "1.0", -1},
		{"1.0", "1.0+1", -1},
		{"1.0a", "1.0+", -1}, // letters before other characters
		{"1.0", "1.0a", -1},
		{"1.01", "1.1", 0}, // digit runs are numeric
		{"0.2.1+sr1~rc1-1", "0.2.1+sr1-1", -1},
		{"0.2.1-1", "0.2.1+sr1~rc1-1", -1},
		{"0.2.1-2", "0.2.1-10", -1}, // revision compared like a version
		{"1:0.1", "0.9", 1},         // epoch first
		{"0:1.0", "1.0", 0},
		{"0.5.0~rc1", "0.4.2", 1},
		{"0.5.0~rc1", "0.5.0", -1},
		{"7.0.14-20-pve", "7.0.14-19-pve", 1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := Compare(c.b, c.a); got != -c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d (antisymmetry)", c.b, c.a, got, -c.want)
		}
	}
	if !Less("0.2.1+sr1~rc1", "0.2.1+sr1") || Less("0.2.1+sr1", "0.2.1") {
		t.Fatal("Less")
	}
}

func TestUpstreamOf(t *testing.T) {
	for in, want := range map[string]string{
		"0.2.1+sr1~rc1-1": "0.2.1+sr1~rc1",
		"0.2.1-1":         "0.2.1",
		"0.2.1":           "0.2.1",
		"1:2.0-3":         "2.0",
	} {
		if got := UpstreamOf(in); got != want {
			t.Errorf("UpstreamOf(%q) = %q, want %q", in, got, want)
		}
	}
}
