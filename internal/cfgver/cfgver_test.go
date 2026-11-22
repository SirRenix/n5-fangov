package cfgver

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T) (*Store, string, *[]string) {
	t.Helper()
	dir := t.TempDir()
	var logged []string
	s := Open(filepath.Join(dir, "config-versions"), func(f string, a ...any) { logged = append(logged, f) })
	clock := time.Unix(1_790_000_000, 0)
	s.SetClock(func() time.Time { clock = clock.Add(time.Minute); return clock })
	return s, filepath.Join(dir, "config.toml"), &logged
}

// write is a daemon write: Before, the file, Wrote. It returns Before's id.
func write(t *testing.T, s *Store, path, text, by, reason string, quiet bool) int {
	t.Helper()
	id := s.Before(path, by, reason, quiet)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Wrote([]byte(text))
	return id
}

func chain(t *testing.T, s *Store) []string {
	t.Helper()
	l, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(l) - 1; i >= 0; i-- {
		_, raw, _ := s.Get(l[i].ID)
		out = append(out, strings.TrimSpace(string(raw))+"|"+l[i].By+"|"+l[i].Reason)
	}
	return out
}

// TestRecordAndRing: every write stores the text before it with author
// and reason and returns its id; the same text as the newest is none (id
// 0); a quiet write (the scheduler) is none; the 11th pushes the oldest
// out, its file removed after the index is saved.
func TestRecordAndRing(t *testing.T) {
	s, path, _ := setup(t)
	if err := os.WriteFile(path, []byte("a = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Start(path) // first start: nothing to compare with, nothing stored
	if id := write(t, s, path, "a = 1\n", "user:admin", "editor", false); id != 1 {
		t.Errorf("first id %d", id)
	}
	if id := write(t, s, path, "a = 2\n", "schedule", "preset quiet (schedule)", true); id != 0 {
		t.Errorf("quiet write stored %d", id)
	}
	if id := write(t, s, path, "a = 3\n", "cli", "assist hdd on", false); id != 2 {
		t.Errorf("third id %d", id)
	}
	if got := chain(t, s); strings.Join(got, ",") != "a = 0|user:admin|editor,a = 2|cli|assist hdd on" {
		t.Fatalf("chain %v", got)
	}
	// identical to the newest: nothing, id 0
	_ = os.WriteFile(path, []byte("a = 2\n"), 0o600)
	s.Wrote([]byte("a = 2\n"))
	if id := s.Before(path, "cli", "again", false); id != 0 {
		t.Errorf("identical text stored as %d", id)
	}
	for i := 4; i <= 14; i++ {
		write(t, s, path, "a = "+strings.Repeat("x", i)+"\n", "cli", "editor", false)
	}
	l, _ := s.List()
	if len(l) != Keep || l[len(l)-1].ID != 3 {
		t.Fatalf("ring: %d versions, oldest %d", len(l), l[len(l)-1].ID)
	}
	if _, err := os.Stat(s.file(2)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dropped version file still there: %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{s.dir, s.file(3), filepath.Join(s.dir, "index.json"), s.known()} {
			st, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			want := os.FileMode(0o600)
			if st.IsDir() {
				want = 0o700
			}
			if st.Mode().Perm() != want {
				t.Errorf("%s: %v", p, st.Mode().Perm())
			}
		}
	}
}

// TestExternalEditChain: the daemon wrote T1; a hand edit H; the next
// daemon write by user:x. The chain keeps T1 (replaced by the hand edit)
// and H (replaced by user:x's editor change), so both can be restored. At
// start the same: T1 as External; H follows with the next write. A quiet
// (scheduled) write after a hand edit still keeps both.
func TestExternalEditChain(t *testing.T) {
	s, path, _ := setup(t)
	_ = os.WriteFile(path, []byte("t0\n"), 0o600)
	s.Start(path)
	write(t, s, path, "t1\n", "cli", "editor", false)
	_ = os.WriteFile(path, []byte("h\n"), 0o600) // by hand
	id := write(t, s, path, "t2\n", "user:x", "editor", false)
	want := []string{"t0|cli|editor", "t1||external edit", "h|user:x|editor"}
	if got := chain(t, s); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("before a write:\n got %v\nwant %v", got, want)
	}
	if id != 3 {
		t.Errorf("the write's own version is %d, want 3 (the hand edit text)", id)
	}
	// at start
	_ = os.WriteFile(path, []byte("h2\n"), 0o600)
	s.Start(path)
	s.Start(path) // known now: nothing more
	if got := chain(t, s); len(got) != 4 || got[3] != "t2||external edit" {
		t.Errorf("at start: %v", got)
	}
	write(t, s, path, "t3\n", "user:y", "preset quiet", false)
	if got := chain(t, s); len(got) != 5 || got[4] != "h2|user:y|preset quiet" {
		t.Errorf("after start: %v", got)
	}
	// a scheduled switch after a hand edit keeps the hand edit
	_ = os.WriteFile(path, []byte("h3\n"), 0o600)
	if id := write(t, s, path, "t4\n", "schedule", "preset cool (schedule)", true); id == 0 {
		t.Error("quiet write after a hand edit stored nothing")
	}
	if got := chain(t, s); len(got) != 7 || got[5] != "t3||external edit" || got[6] != "h3|schedule|preset cool (schedule)" {
		t.Errorf("scheduled after hand edit: %v", got)
	}
	if next, _ := s.Next(1); string(next) != "t1\n" {
		t.Errorf("Next: %q", next)
	}
}

// TestFailureDoesNotBlock: an unwritable directory logs once per cause;
// Before returns 0 and never fails the caller.
func TestFailureDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	block := filepath.Join(dir, "file")
	if err := os.WriteFile(block, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var logged []string
	s := Open(filepath.Join(block, "versions"), func(f string, a ...any) { logged = append(logged, f) })
	path := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(path, []byte("a = 1\n"), 0o600)
	for i := 0; i < 3; i++ {
		if id := s.Before(path, "cli", "editor", false); id != 0 {
			t.Errorf("id %d", id)
		}
		s.Wrote([]byte("a = 1\n"))
	}
	if len(logged) == 0 || len(logged) > 2 {
		t.Errorf("logged %d lines, want once per cause: %v", len(logged), logged)
	}
	if cause(&fs.PathError{Op: "rename", Path: "/x/.index.json.tmp123", Err: fs.ErrPermission}) != cause(&fs.PathError{Op: "rename", Path: "/x/.index.json.tmp987", Err: fs.ErrPermission}) {
		t.Error("cause depends on the temp name")
	}
	if cause(errors.New("write /x/.a.tmp-41: no space")) != cause(errors.New("write /x/.a.tmp-77: no space")) {
		t.Error("cause depends on the temp name (plain error)")
	}
}

// TestIndexSaveFailureKeepsFiles: when the index cannot be saved after the
// ring was trimmed, the oldest file stays (the old index still names it).
func TestIndexSaveFailureKeepsFiles(t *testing.T) {
	s, path, _ := setup(t)
	_ = os.WriteFile(path, []byte("x\n"), 0o600)
	for i := 1; i <= Keep+1; i++ {
		write(t, s, path, "v"+strings.Repeat("y", i)+"\n", "cli", "editor", false)
	}
	l, _ := s.List()
	oldest := l[len(l)-1].ID
	s.writeIndex = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	if id := write(t, s, path, "last\n", "cli", "editor", false); id != 0 {
		t.Errorf("id %d despite a failed index", id)
	}
	if _, err := os.Stat(s.file(oldest)); err != nil {
		t.Errorf("oldest file removed although the index was not saved: %v", err)
	}
	if l2, _ := s.List(); len(l2) != Keep || l2[len(l2)-1].ID != oldest {
		t.Errorf("index changed: %+v", l2)
	}
}

// TestCorruptIndex: an index that does not parse is moved aside, logged,
// and a fresh one continues after the highest version file.
func TestCorruptIndex(t *testing.T) {
	s, path, logged := setup(t)
	_ = os.WriteFile(path, []byte("x\n"), 0o600)
	write(t, s, path, "y\n", "cli", "editor", false)
	write(t, s, path, "z\n", "cli", "editor", false)
	if err := os.WriteFile(filepath.Join(s.dir, "index.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id := write(t, s, path, "w\n", "cli", "editor", false); id != 3 {
		t.Errorf("after a corrupt index: id %d, want 3", id)
	}
	ms, _ := filepath.Glob(filepath.Join(s.dir, "index.json.corrupt-*"))
	if len(ms) != 1 || len(*logged) == 0 || !strings.Contains((*logged)[0], "moved to") {
		t.Errorf("aside %v, log %v", ms, *logged)
	}
	if l, err := s.List(); err != nil || len(l) != 1 {
		t.Errorf("fresh index: %+v %v", l, err)
	}
}

func TestDiff(t *testing.T) {
	d := Diff("a\nb\nc\n", "a\nx\nc\nd\n")
	want := []Op{{" ", "a"}, {"-", "b"}, {"+", "x"}, {" ", "c"}, {"+", "d"}}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("diff %v", d)
	}
	if r, a := Count(d); r != 1 || a != 2 {
		t.Errorf("count %d/%d", r, a)
	}
	if d := Diff("", "a\n"); len(d) != 1 || d[0] != (Op{"+", "a"}) {
		t.Errorf("from empty: %v", d)
	}
	if d := Diff("a\r\nb\r\n", "a\nb\n"); len(d) != 2 || d[0].Op != " " || d[1].Op != " " {
		t.Errorf("CRLF: %v", d)
	}
}

// TestDiffLarge: two 130 000-line files (the 256 KiB PUT limit of blank
// lines and comments) with changes at both ends are diffed within the
// cell bound: the middle counts as replaced, memory stays small.
func TestDiffLarge(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 130_000; i++ {
		a.WriteString("#\n")
		b.WriteString("\n")
	}
	start := time.Now()
	d := Diff("[web]\n"+a.String()+"x = 1\n", "[alert]\n"+b.String()+"x = 2\n")
	r, add := Count(d)
	if r != 130_002 || add != 130_002 {
		t.Errorf("replaced counts %d/%d", r, add)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("large diff took %s", time.Since(start))
	}
	// a large file with one changed line in the middle: head and tail cut
	big := strings.Repeat("# c\n", 100_000)
	d = Diff(big+"a = 1\n"+big, big+"a = 2\n"+big)
	if r, add := Count(d); r != 1 || add != 1 {
		t.Errorf("one line in a large file: %d/%d", r, add)
	}
	if got := Sections("[web]\n"+a.String(), "[alert]\n"+b.String()); len(got) == 0 {
		t.Error("sections of a replaced file")
	}
}

func TestSections(t *testing.T) {
	a := "# top\n[daemon]\ninterval = \"10s\"\n\n[alert]\ntransport = \"auto\"\n\n[[channel]]\nname = \"cpu\"\ncurve = [[45, 85], [80, 255]]\n\n[[channel]]\nname = \"hdd\"\ncurve = [[36, 105], [46, 255]]\n\n[[assist]]\nchannel = \"hdd\"\nenabled = false\n\n[[assist.source]]\nsensor = \"x\"\nboost = 45\n"
	b := strings.Replace(a, "[[36, 105]", "[[38, 105]", 1)
	b = strings.Replace(b, "boost = 45", "boost = 40", 1)
	b = strings.Replace(b, "transport = \"auto\"", "transport = \"log\"", 1)
	b = strings.Replace(b, "# top", "# a comment only", 1)
	got := Sections(a, b)
	want := []string{"[alert]", "[[channel]] hdd", "[[assist]] hdd"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sections %q, want %q", got, want)
	}
	if got := Sections(a, a+"\n[[schedule]]\npreset = \"quiet\"\n"); !reflect.DeepEqual(got, []string{"[[schedule]] quiet"}) {
		t.Errorf("added table: %q", got)
	}
}
