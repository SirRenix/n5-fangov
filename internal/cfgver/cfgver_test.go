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

func write(t *testing.T, s *Store, path, text, by, reason string) {
	t.Helper()
	s.Before(path, by, reason)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Wrote([]byte(text))
}

// TestRecordAndRing: every write stores the text before it with author
// and reason; the same text twice is one version; the 11th pushes the
// oldest out (file removed); scheduler writes (reason "") store nothing.
func TestRecordAndRing(t *testing.T) {
	s, path, _ := setup(t)
	if err := os.WriteFile(path, []byte("a = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Start(path) // first start: nothing to compare with, nothing stored
	write(t, s, path, "a = 1\n", "user:admin", "editor")
	write(t, s, path, "a = 2\n", "", "")          // the scheduler: no version
	write(t, s, path, "a = 2\n", "cli", "assist") // same text before as the newest? no: a = 2 is new
	l, _ := s.List()
	if len(l) != 2 || l[0].Reason != "assist" || l[0].By != "cli" || l[1].Reason != "editor" || l[1].By != "user:admin" {
		t.Fatalf("list %+v", l)
	}
	if _, raw, _ := s.Get(l[1].ID); string(raw) != "a = 0\n" {
		t.Errorf("version 1 holds %q", raw)
	}
	if _, raw, _ := s.Get(l[0].ID); string(raw) != "a = 2\n" {
		t.Errorf("version 2 holds %q", raw)
	}
	// identical to the newest: skipped
	s.Before(path, "cli", "again")
	if l, _ := s.List(); len(l) != 2 {
		t.Errorf("identical text stored again: %d", len(l))
	}
	for i := 3; i <= 14; i++ {
		write(t, s, path, "a = "+strings.Repeat("x", i)+"\n", "cli", "editor")
	}
	l, _ = s.List()
	if len(l) != Keep || l[len(l)-1].ID != 4 {
		t.Fatalf("ring: %d versions, oldest %d", len(l), l[len(l)-1].ID)
	}
	if _, err := os.Stat(s.file(3)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dropped version file still there: %v", err)
	}
	if _, _, err := s.Get(3); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Get of a dropped version: %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{s.dir, s.file(4), filepath.Join(s.dir, "index.json")} {
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

// TestExternalEdit: a file changed by hand is stored as "external edit"
// at start and before the next write (the write's own version would hold
// the same text and is skipped).
func TestExternalEdit(t *testing.T) {
	s, path, _ := setup(t)
	write(t, s, path, "a = 1\n", "cli", "editor")
	if err := os.WriteFile(path, []byte("a = 1 # by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Start(path)
	l, _ := s.List()
	if len(l) != 1 || l[0].Reason != External || l[0].By != "" {
		t.Fatalf("at start: %+v", l)
	}
	s.Start(path) // known now
	if l, _ := s.List(); len(l) != 1 {
		t.Errorf("second start stored again: %+v", l)
	}
	if err := os.WriteFile(path, []byte("a = 3 # by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write(t, s, path, "a = 4\n", "user:admin", "preset quiet")
	l, _ = s.List()
	if len(l) != 2 || l[0].Reason != External {
		t.Fatalf("before a write: %+v", l)
	}
	if _, raw, _ := s.Get(l[0].ID); string(raw) != "a = 3 # by hand\n" {
		t.Errorf("stored %q", raw)
	}
	if next, _ := s.Next(l[1].ID); string(next) != "a = 3 # by hand\n" {
		t.Errorf("Next: %q", next)
	}
	if next, _ := s.Next(l[0].ID); next != nil {
		t.Errorf("Next of the newest: %q", next)
	}
}

// TestFailureDoesNotBlock: an unwritable directory logs once per cause;
// Before and Wrote never fail the caller.
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
		s.Before(path, "cli", "editor")
		s.Wrote([]byte("a = 1\n"))
	}
	if len(logged) == 0 || len(logged) > 2 {
		t.Errorf("logged %d lines, want once per cause", len(logged))
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
