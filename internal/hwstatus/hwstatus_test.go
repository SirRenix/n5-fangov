package hwstatus

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/n5driver"
)

// The embedded table must parse (init panics otherwise) and carry its
// provenance; the reference row must be there.
func TestEmbeddedTableParses(t *testing.T) {
	tb, m, err := Parse(embedded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Source, "RESULTS.md") || len(m.SourceSHA256) != 64 || len(m.Generated) != len("2026-10-09") {
		t.Fatalf("meta = %+v", m)
	}
	found := false
	for _, e := range tb {
		if !e.Status.Valid() || e.Origin == "" {
			t.Errorf("row %+v", e)
		}
		if e.Product == "N5 PRO" && e.Board == "F8NAA" && e.BIOS == "1.05" && e.EC == "0.14" {
			found = true
		}
	}
	if !found {
		t.Fatal("reference row N5 PRO/F8NAA/1.05/0.14 missing")
	}
	if len(Table()) != len(tb) || Source() != m {
		t.Fatal("Table/Source differ from the parsed file")
	}
}

// When the driver repository sits next to this one (local checkout), the
// embedded copy must be generated from the current RESULTS.md.
func TestEmbeddedTableInSyncWithResults(t *testing.T) {
	p := filepath.Join("..", "..", "..", "n5pro-ec", "befunde", "RESULTS.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skip("RESULTS.md not next to this checkout")
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != Source().SourceSHA256 {
		t.Fatalf("results.tsv is stale: RESULTS.md sha256 %s, embedded from %s; run tools/gen-hwstatus.sh", got, Source().SourceSHA256)
	}
}

func TestParseRejects(t *testing.T) {
	head := "# source: x\n# generated: 2026-10-09\n"
	for name, body := range map[string]string{
		"fields":             head + "a\tb\tc\n",
		"status":             head + "N5\tF8NAA\t*\t*\tverified\teigen\td\tn\n",
		"origin":             head + "N5\tF8NAA\t*\t*\ttested\tsomeone\td\tn\n",
		"empty":              head,
		"no meta":            "N5\tF8NAA\t*\t*\ttested\teigen\td\tn\n",
		"key":                head + "N5\t\t*\t*\ttested\teigen\td\tn\n",
		"upstream validated": head + "N5\tF8NAA\t*\t*\tvalidated\tupstream\td\tn\n",
		"report tested":      head + "N5\tF8NAA\t*\t*\ttested\tBericht\td\tn\n",
	} {
		if _, _, err := Parse(body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLookup(t *testing.T) {
	tb := []Entry{
		{Product: "N5 PRO", Board: "F8NAA", BIOS: "1.05", EC: "0.14", Status: Experimental, Origin: "own"},
		{Product: "N5 PRO", Board: "F8NAA", BIOS: "1.05", EC: "*", Status: Tested, Origin: "own"},
		{Product: "N5", Board: "F8NAA", BIOS: "*", EC: "*", Status: Validated, Origin: "own"},
		{Product: "N5 PRO", Board: "F8NAB", BIOS: "*", EC: "*", Status: Experimental, Origin: "report"},
	}
	cases := []struct {
		id   Identity
		want Status
		row  bool
	}{
		{Identity{Product: "N5 PRO", Board: "F8NAA", BIOS: "1.05", EC: "0.14"}, Experimental, true}, // exact beats EC "*"
		{Identity{Product: "N5 PRO", Board: "F8NAA", BIOS: "1.05", EC: "0.15"}, Tested, true},
		{Identity{Product: "N5 PRO", Board: "F8NAA", BIOS: "1.06", EC: "0.14"}, Unknown, false},
		{Identity{Product: "N5 PRO", Board: "F8NAA", BIOS: "", EC: "0.14"}, Unknown, false},
		{Identity{Product: "N5", Board: "F8NAA", BIOS: "1.04", EC: ""}, Validated, true},
		{Identity{Product: "N5 PRO", Board: "F8NAB", BIOS: "1.01", EC: "0.15"}, Experimental, true},
		{Identity{Product: "Other", Board: "X"}, Unknown, false},
	}
	for _, c := range cases {
		r := lookupIn(tb, c.id)
		if r.Status != c.want || (r.Entry != nil) != c.row || r.Reason == "" || r.Key != c.id.Key() {
			t.Errorf("%s: %+v, want %s (row %v)", c.id.Key(), r, c.want, c.row)
		}
	}
	if !Validated.Verified() || !Tested.Verified() || Experimental.Verified() || Unknown.Verified() {
		t.Fatal("Verified mapping")
	}
}

func TestForProfileAndIdentity(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "class", "dmi", "id")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"product_name": "N5 PRO", "board_name": "F8NAA", "board_version": "1.0",
		"sys_vendor": "Micro Computer (HK) Tech Limited", "bios_version": "1.05",
		"bios_date": "03/31/2026", "ec_firmware_release": "0.14",
	} {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	id := ReadIdentity(root)
	if id.Key() != "N5 PRO / F8NAA / BIOS 1.05 / EC 0.14" || id.SysVendor == "" || id.BoardVersion != "1.0" {
		t.Fatalf("identity %+v", id)
	}
	if r := ForProfile("n5pro", id); r.Status != Experimental || r.Entry == nil {
		t.Fatalf("n5pro on the reference identity: %+v", r)
	}
	for _, p := range []string{"nct67xx", "it87xx", "monitor"} {
		if r := ForProfile(p, id); r.Status != Unknown || r.Entry != nil {
			t.Errorf("%s: %+v", p, r)
		}
	}
	if empty := ReadIdentity(t.TempDir()); empty.Key() != "? / ? / BIOS ? / EC ?" {
		t.Fatalf("empty identity key %q", empty.Key())
	}
}

// The write gate mirror names only rows of the table, and the N5 Air rows
// stay read-only (G3, decision 4).
func TestWriteCapable(t *testing.T) {
	rows := map[[2]string]bool{}
	for _, e := range Table() {
		rows[[2]string{e.Product, e.Board}] = true
	}
	for b := range writeBoards {
		if !rows[b] {
			t.Errorf("write board %v is not a row of results.tsv", b)
		}
	}
	for _, c := range []struct {
		product, board string
		want           bool
	}{
		{"N5 PRO", "F8NAA", true}, {"N5", "F8NAA", true},
		{"N5A", "F8NAB", false}, {"N5 AIR", "F8NAB", false}, {"N5 PRO", "F8NAB", false}, {"Other", "X", false},
	} {
		if got := WriteCapable(Identity{Product: c.product, Board: c.board, SysVendor: n5driver.SysVendor}); got != c.want {
			t.Errorf("WriteCapable(%s / %s) = %v", c.product, c.board, got)
		}
		// the driver compares sys_vendor exactly (rc2 review R4)
		for _, v := range []string{"", "Example Vendor", n5driver.SysVendor + " "} {
			if WriteCapable(Identity{Product: c.product, Board: c.board, SysVendor: v}) {
				t.Errorf("WriteCapable(%s / %s, vendor %q) = true", c.product, c.board, v)
			}
		}
	}
}
