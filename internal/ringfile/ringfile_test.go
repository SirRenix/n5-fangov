package ringfile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type recLogger struct{ lines []string }

func (l *recLogger) Printf(f string, a ...any) { l.lines = append(l.lines, fmt.Sprintf(f, a...)) }

func (l *recLogger) count(sub string) int {
	n := 0
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

type rec struct {
	ID int    `json:"id"`
	S  string `json:"s"`
}

func TestFileReadWriteChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ring.json")
	lg := &recLogger{}
	a := New[rec](path, "test ring", "records", lg)
	if recs, ok := a.Read(); ok || recs != nil || a.Changed() {
		t.Fatalf("missing file: %v %v %v", recs, ok, a.Changed())
	}
	a.Write([]rec{{1, "x"}})
	if a.Changed() {
		t.Error("own write counts as a change")
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("mode: %v %v", err, st)
		}
	}
	b := New[rec](path, "test ring", "records", lg)
	if recs, ok := b.Read(); !ok || len(recs) != 1 || recs[0].S != "x" {
		t.Fatalf("second file view: %v %v", recs, ok)
	}
	time.Sleep(10 * time.Millisecond) // mtime granularity; the size changes anyway
	b.Write([]rec{{1, "x"}, {2, "yy"}})
	if !a.Changed() {
		t.Fatal("another writer's change not seen")
	}
	if recs, ok := a.Read(); !ok || len(recs) != 2 || a.Changed() {
		t.Fatalf("after re-read: %v %v", recs, ok)
	}
	// unparsable: logged, ignored
	if err := os.WriteFile(path, []byte("[{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Read(); ok || lg.count("test ring "+path) != 1 || lg.count("(ignored)") != 1 {
		t.Errorf("broken file: %v", lg.lines)
	}
}

func TestFileMemoryOnlyAndWriteFailure(t *testing.T) {
	m := New[rec]("", "test ring", "records", nil)
	m.Lock()()
	m.Write([]rec{{1, "x"}})
	if _, ok := m.Read(); ok || m.Changed() || m.Path() != "" {
		t.Error("memory-only file touched something")
	}
	if runtime.GOOS == "windows" {
		return
	}
	lg := &recLogger{}
	f := New[rec](filepath.Join(t.TempDir(), "missing", "ring.json"), "test ring", "records", lg)
	f.Write([]rec{{1, "x"}})
	f.Write([]rec{{1, "x"}})
	if lg.count("records kept in memory only") != 1 {
		t.Errorf("write failure logged %v", lg.lines)
	}
	// the lock file cannot be created there either: logged once, no-op
	f.Lock()()
	f.Lock()()
	if lg.count("lock:") != 1 {
		t.Errorf("lock failure logged %v", lg.lines)
	}
}
