package logfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTest(t *testing.T) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs", "n5-fangov.log")
	w, err := New(path, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	// the tests need a small threshold; 1 MiB would take long to fill
	w.maxSize = 1000
	t.Cleanup(func() { _ = w.Close() })
	return w, path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestCreateModes(t *testing.T) {
	w, path := newTest(t)
	if w.Path() != path {
		t.Errorf("Path %q", w.Path())
	}
	if !exists(path) {
		t.Fatal("file not created")
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != DirMode {
			t.Errorf("dir mode %o, want %o", st.Mode().Perm(), DirMode)
		}
		if st, _ := os.Stat(path); st.Mode().Perm() != FileMode {
			t.Errorf("file mode %o, want %o", st.Mode().Perm(), FileMode)
		}
	}
	if _, err := New("", 1, 1); err == nil {
		t.Errorf("empty path accepted")
	}
}

// Rotation happens exactly when a write would cross the limit: the file
// that holds 990 bytes takes a 10-byte line (= limit) and rotates on the
// next one; .1..N shift, the oldest disappears.
func TestRotationAtBoundary(t *testing.T) {
	w, path := newTest(t)
	line := func(i int) string { return fmt.Sprintf("line-%04d\n", i) } // 10 bytes
	for i := 0; i < 99; i++ {
		if _, err := w.Write([]byte(line(i))); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := os.Stat(path); st.Size() != 990 {
		t.Fatalf("size %d", st.Size())
	}
	if _, err := w.Write([]byte(line(99))); err != nil { // exactly 1000: fits
		t.Fatal(err)
	}
	if exists(path + ".1") {
		t.Fatal("rotated at the limit, not above it")
	}
	if _, err := w.Write([]byte(line(100))); err != nil { // 1010 > 1000: rotate first
		t.Fatal(err)
	}
	if !exists(path + ".1") {
		t.Fatal("no rotation above the limit")
	}
	b1, _ := os.ReadFile(path + ".1")
	if len(b1) != 1000 || !strings.HasSuffix(string(b1), line(99)) {
		t.Errorf(".1 content: %d bytes, tail %q", len(b1), string(b1[len(b1)-10:]))
	}
	cur, _ := os.ReadFile(path)
	if string(cur) != line(100) {
		t.Errorf("current after rotation: %q", cur)
	}
	// fill and rotate twice more: .1 .2 .3 exist, a fourth rotation drops
	// the oldest (maxFiles = 3)
	fill := func(tag string) {
		for i := 0; i < 100; i++ {
			if _, err := w.Write([]byte(fmt.Sprintf("%s-%04d\n", tag, i))); err != nil {
				t.Fatal(err)
			}
		}
	}
	fill("aaaa") // rotates once at i=99 (current had 10 bytes + 990 = 1000, next crosses)
	fill("bbbb")
	fill("cccc")
	for _, n := range []string{".1", ".2", ".3"} {
		if !exists(path + n) {
			t.Errorf("%s missing", n)
		}
	}
	if exists(path + ".4") {
		t.Errorf(".4 must not exist (maxFiles 3)")
	}
	// the oldest kept file (.3) must be newer than the very first one
	b3, _ := os.ReadFile(path + ".3")
	if strings.HasPrefix(string(b3), "line-0000") {
		t.Errorf(".3 still holds the first generation; oldest not dropped")
	}
	// order: .1 newest of the rotated ones
	b1, _ = os.ReadFile(path + ".1")
	b2, _ := os.ReadFile(path + ".2")
	if !strings.Contains(string(b1), "cccc") && !strings.Contains(string(b1), "bbbb") {
		t.Errorf(".1 is not recent: %q", string(b1[:20]))
	}
	if string(b1) == string(b2) {
		t.Errorf(".1 and .2 identical")
	}
}

func TestLinesTail(t *testing.T) {
	w, path := newTest(t)
	w.maxSize = 1 << 30
	if lines, err := w.Lines(10); err != nil || len(lines) != 0 {
		t.Errorf("empty file: %v %v", lines, err)
	}
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(w, "entry %d\n", i)
	}
	lines, err := w.Lines(5)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, ",") != "entry 21,entry 22,entry 23,entry 24,entry 25" {
		t.Errorf("tail 5: %v", lines)
	}
	lines, _ = w.Lines(100)
	if len(lines) != 25 || lines[0] != "entry 1" || lines[24] != "entry 25" {
		t.Errorf("tail 100: %d lines, first %q last %q", len(lines), lines[0], lines[len(lines)-1])
	}
	if lines, _ := w.Lines(0); lines != nil {
		t.Errorf("n=0: %v", lines)
	}
	// a file without trailing newline: last partial line counts
	_ = os.WriteFile(path, []byte("a\nb\nc"), 0o600)
	if lines, _ := ReadLines(path, 2); strings.Join(lines, ",") != "b,c" {
		t.Errorf("no trailing newline: %v", lines)
	}
	// CRLF tolerated
	_ = os.WriteFile(path, []byte("x\r\ny\r\n"), 0o600)
	if lines, _ := ReadLines(path, 5); strings.Join(lines, ",") != "x,y" {
		t.Errorf("crlf: %q", lines)
	}
	// missing file: nothing, no error
	if lines, err := ReadLines(filepath.Join(t.TempDir(), "nope.log"), 5); err != nil || lines != nil {
		t.Errorf("missing: %v %v", lines, err)
	}
}

// Files beyond TailLimit: only the last MiB is read and the line cut at
// the boundary is dropped, so the first returned line is complete.
func TestLinesTailLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 99) + "\n") // 100 bytes
	total := (TailLimit / 100) + 50                // ~1.005 MiB
	for i := 0; i < total; i++ {
		copy(line, fmt.Sprintf("%08d", i))
		if _, err := f.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	lines, err := ReadLines(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 || len(lines) >= total {
		t.Fatalf("got %d lines of %d", len(lines), total)
	}
	for i, l := range lines {
		if len(l) != 99 {
			t.Fatalf("line %d truncated: %d bytes", i, len(l))
		}
	}
	if lines[len(lines)-1][:8] != fmt.Sprintf("%08d", total-1) {
		t.Errorf("last line %q", lines[len(lines)-1][:8])
	}
	if got := len(lines) * 100; got > TailLimit {
		t.Errorf("read %d bytes, more than TailLimit", got)
	}
}

func TestExportAndClear(t *testing.T) {
	w, path := newTest(t)
	w.maxSize = 1 << 30
	fmt.Fprint(w, "one\ntwo\n")
	var buf bytes.Buffer
	if err := w.Export(&buf); err != nil || buf.String() != "one\ntwo\n" {
		t.Errorf("export: %q %v", buf.String(), err)
	}
	// rotate by hand so a .1 exists, then Clear must leave it alone
	_ = os.WriteFile(path+".1", []byte("old\n"), 0o600)
	if err := w.Clear(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Size() != 0 {
		t.Errorf("not truncated: %d", st.Size())
	}
	if b, _ := os.ReadFile(path + ".1"); string(b) != "old\n" {
		t.Errorf("rotated file touched: %q", b)
	}
	// writes continue at the new start, no rotation triggered
	fmt.Fprint(w, "three\n")
	if lines, _ := w.Lines(10); strings.Join(lines, ",") != "three" {
		t.Errorf("after clear: %v", lines)
	}
	if b, _ := os.ReadFile(path + ".1"); string(b) != "old\n" {
		t.Errorf("spurious rotation after clear")
	}
	// external truncate (CLI while the daemon runs) behaves the same
	if err := Truncate(path); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(w, "four\n")
	if lines, _ := ReadLines(path, 10); strings.Join(lines, ",") != "four" {
		t.Errorf("after external truncate: %v", lines)
	}
	buf.Reset()
	if err := ExportFile(path, &buf); err != nil || buf.String() != "four\n" {
		t.Errorf("ExportFile: %q %v", buf.String(), err)
	}
	if err := ExportFile(filepath.Join(t.TempDir(), "nope"), &buf); err != nil {
		t.Errorf("missing file export must be a no-op: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Errorf("write after close accepted")
	}
	if err := w.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

// Concurrent writers across many rotations: no error, no lost line, every
// line intact (no interleaving) in the union of current and rotated files.
func TestConcurrentWrites(t *testing.T) {
	w, path := newTest(t)
	w.maxSize = 4000
	w.maxFiles = 20
	const workers, per = 8, 200
	var wg sync.WaitGroup
	errs := make(chan error, workers*per)
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if _, err := fmt.Fprintf(w, "w%d-%04d-%s\n", g, i, strings.Repeat("=", 20)); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var all []byte
	for _, p := range append([]string{path}, rotated(path, 20)...) {
		b, err := os.ReadFile(p)
		if err == nil {
			all = append(all, b...)
		}
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimRight(string(all), "\n"), "\n") {
		if len(l) != len("w0-0000-")+20 {
			t.Fatalf("interleaved or cut line %q", l)
		}
		seen[l[:len("w0-0000")]] = true
	}
	// with maxFiles 20 x 4000 bytes = 80 KB the ~46 KB written fit
	if len(seen) != workers*per {
		t.Errorf("%d distinct lines, want %d", len(seen), workers*per)
	}
}

// blockingWriter blocks its first Write until release is closed and
// records what it eventually received.
type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	buf     bytes.Buffer
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *blockingWriter) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// stallLimit is how long a Write/Lines call may take while an Export is
// stuck before the test calls it blocked. A held writer lock would block
// until the export is released (never, here), so the limit only has to
// exceed scheduling noise on a loaded builder (AUDIT 7: 100 ms was too
// tight for the Alpine container on the build host).
const stallLimit = 2 * time.Second

// H1: an Export whose destination stalls (slow HTTP client) must not hold
// the writer lock — the controller logs from inside the regulation cycle
// and a blocked log call would run it into the systemd watchdog. Write and
// Lines complete while the export is stuck; the export delivers the file
// as it was when it started.
func TestExportDoesNotBlockWrites(t *testing.T) {
	w, _ := newTest(t)
	w.maxSize = 1 << 30
	for i := 0; i < 500; i++ { // > bufio buffer so the stalled Write is hit mid-copy
		fmt.Fprintf(w, "line %04d %s\n", i, strings.Repeat("x", 40))
	}
	bw := newBlockingWriter()
	exportDone := make(chan error, 1)
	go func() { exportDone <- w.Export(bw) }()
	select {
	case <-bw.started:
	case <-time.After(5 * time.Second):
		t.Fatal("export never reached the writer")
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := w.Write([]byte("concurrent\n"))
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(stallLimit):
		t.Fatal("Write blocked behind a stalled Export (writer lock held while streaming)")
	}
	linesDone := make(chan []string, 1)
	go func() { l, _ := w.Lines(1); linesDone <- l }()
	select {
	case l := <-linesDone:
		if strings.Join(l, ",") != "concurrent" {
			t.Errorf("Lines during export: %v", l)
		}
	case <-time.After(stallLimit):
		t.Fatal("Lines blocked behind a stalled Export")
	}
	close(bw.release)
	if err := <-exportDone; err != nil {
		t.Fatal(err)
	}
	out := bw.String()
	if strings.Contains(out, "concurrent") {
		t.Errorf("export includes bytes written after it started")
	}
	if !strings.HasSuffix(out, "line 0499 "+strings.Repeat("x", 40)+"\n") || strings.Count(out, "\n") != 500 {
		t.Errorf("export content: %d lines, tail %q", strings.Count(out, "\n"), out[len(out)-60:])
	}
}

// H1: a rotation during a stalled export renames the file under the open
// descriptor; the export still delivers the old content, new lines land in
// the fresh file.
func TestExportSurvivesRotation(t *testing.T) {
	w, path := newTest(t)
	w.maxSize = 1 << 30
	for i := 0; i < 200; i++ {
		fmt.Fprintf(w, "old %04d %s\n", i, strings.Repeat("x", 40))
	}
	bw := newBlockingWriter()
	exportDone := make(chan error, 1)
	go func() { exportDone <- w.Export(bw) }()
	<-bw.started
	w.maxSize = 100 // next write rotates
	if _, err := w.Write([]byte("new line\n")); err != nil {
		t.Fatal(err)
	}
	if !exists(path + ".1") {
		t.Fatal("no rotation")
	}
	close(bw.release)
	if err := <-exportDone; err != nil {
		t.Fatal(err)
	}
	if out := bw.String(); strings.Count(out, "\n") != 200 || strings.Contains(out, "new line") {
		t.Errorf("export after rotation: %d lines", strings.Count(out, "\n"))
	}
	if lines, _ := w.Lines(5); strings.Join(lines, ",") != "new line" {
		t.Errorf("current file after rotation: %v", lines)
	}
}

// H2: an existing target that is not a regular file is refused — a symlink
// (even to a regular file), a directory, a FIFO. A missing file is created.
func TestNewRefusesNonRegularTarget(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(filepath.Join(dir, "sub", "new.log"), 1, 1); err != nil {
		t.Errorf("missing file must be created: %v", err)
	}
	if _, err := New(dir, 1, 1); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("directory accepted: %v", err)
	}
	target := filepath.Join(dir, "target.log")
	if err := os.WriteFile(target, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := New(link, 1, 1); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink accepted: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "x\n" {
		t.Errorf("symlink target touched: %q", b)
	}
	// a device node needs root to create; a FIFO is the same class
	// (exists, not regular) and stands in for /dev/sda here
	fifo := filepath.Join(dir, "fifo.log")
	if err := mkfifo(fifo); err == nil {
		if _, err := New(fifo, 1, 1); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("fifo accepted: %v", err)
		}
	}
}

func rotated(path string, n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("%s.%d", path, i))
	}
	return out
}

func TestTimestamped(t *testing.T) {
	var buf bytes.Buffer
	tw := Timestamped(&buf)
	n, err := tw.Write([]byte("hello\n"))
	if err != nil || n != 6 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	s := buf.String()
	if len(s) != len("2006-01-02 15:04:05 ")+6 || !strings.HasSuffix(s, " hello\n") || s[4] != '-' || s[10] != ' ' {
		t.Errorf("stamped line %q", s)
	}
}
