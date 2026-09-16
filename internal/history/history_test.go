package history

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func point(ts int64, cpuTemp float64, duty, rpm int) Point {
	return Point{TS: ts, Temp: map[string]float64{"cpu": cpuTemp}, Duty: map[string]int{"cpu": duty}, RPM: map[string]int{"cpu": rpm}}
}

// t0 is a bucket boundary of both averaged tiers (multiple of 300).
const t0 int64 = 1_789_500_000

type logs struct{ lines []string }

func (l *logs) f(format string, args ...any) { l.lines = append(l.lines, fmt.Sprintf(format, args...)) }

// fill pushes n points at interval seconds starting at the clock, with
// the temperature rising by 1 per point, advancing the clock after each.
func fill(s *Store, c *clock, n int, interval int64, temp0 float64) {
	for i := 0; i < n; i++ {
		s.Push(point(c.t.Unix(), temp0+float64(i), 100+i, 1000+i))
		c.advance(time.Duration(interval) * time.Second)
	}
}

func TestRawCapacity(t *testing.T) {
	if RawCapacity(10*time.Second) != 720 || RawCapacity(120*time.Second) != 60 || RawCapacity(0) != 1 || RawCapacity(3*time.Hour) != 1 {
		t.Errorf("RawCapacity: %d %d %d %d", RawCapacity(10*time.Second), RawCapacity(120*time.Second), RawCapacity(0), RawCapacity(3*time.Hour))
	}
}

func TestRawTierCapacityAndSince(t *testing.T) {
	c := &clock{t: time.Unix(t0, 0)}
	s := New("", 120*time.Second, c.now, nil)
	if s.RawCap() != 60 {
		t.Fatalf("raw capacity %d", s.RawCap())
	}
	fill(s, c, 65, 120, 30)
	all := s.Range(RawSpan, 0)
	if len(all) != 60 {
		t.Fatalf("raw points %d", len(all))
	}
	if all[0].TS != t0+5*120 || all[59].TS != t0+64*120 {
		t.Errorf("raw window %d..%d", all[0].TS, all[59].TS)
	}
	// span cuts inside the ring; since is strict
	if got := s.Range(10*time.Minute, 0); len(got) != 5 {
		t.Errorf("Range(10m) = %d points", len(got))
	}
	if got := s.Range(RawSpan, t0+63*120); len(got) != 1 || got[0].TS != t0+64*120 {
		t.Errorf("since: %+v", got)
	}
	// copies: the caller cannot poison the store
	all[59].Temp["cpu"] = -1
	if s.Range(RawSpan, 0)[59].Temp["cpu"] == -1 {
		t.Errorf("Range returned the stored map")
	}
	// interval change trims the raw tier
	s.SetInterval(240 * time.Second)
	if got := s.Range(RawSpan, 0); len(got) != 30 || got[0].TS != t0+35*120 {
		t.Errorf("after SetInterval: %d points from %d", len(got), got[0].TS)
	}
}

func TestBucketMeans(t *testing.T) {
	c := &clock{t: time.Unix(t0, 0)}
	s := New("", 10*time.Second, c.now, nil)
	// first minute: six points, temp 30..35 (mean 32.5), duty 100..105
	// (mean 102.5 → 103), rpm 1000..1005 (mean 1002.5 → 1003); the extra
	// sensor and the hdd channel are present in some points only
	for i := 0; i < 6; i++ {
		p := point(c.t.Unix(), 30+float64(i), 100+i, 1000+i)
		if i < 2 {
			p.Extra = map[string]float64{"ec:system": 40 + float64(i)}
			p.Temp["hdd"] = 20 + float64(i)
		}
		s.Push(p)
		c.advance(10 * time.Second)
	}
	// second minute: one point → the open bucket carries its running mean
	s.Push(point(c.t.Unix(), 50, 200, 2000))
	got := s.Range(3*time.Hour, 0)
	if len(got) != 2 {
		t.Fatalf("1-min points: %+v", got)
	}
	b := got[0]
	if b.TS != t0 || b.Temp["cpu"] != 32.5 || b.Duty["cpu"] != 103 || b.RPM["cpu"] != 1003 {
		t.Errorf("closed bucket: %+v", b)
	}
	if b.Temp["hdd"] != 20.5 || b.Extra["ec:system"] != 40.5 {
		t.Errorf("partial keys: %+v", b)
	}
	if _, ok := b.Duty["hdd"]; ok {
		t.Errorf("a key absent in every raw point appeared: %+v", b.Duty)
	}
	o := got[1]
	if o.TS != t0+60 || o.Temp["cpu"] != 50 || o.Duty["cpu"] != 200 || o.Extra != nil {
		t.Errorf("open bucket: %+v", o)
	}
	// 5-min tier: everything so far is one open bucket at t0
	got5 := s.Range(48*time.Hour, 0)
	if len(got5) != 1 || got5[0].TS != t0 || got5[0].Temp["cpu"] != 35 || got5[0].Duty["cpu"] != 116 {
		t.Errorf("5-min open bucket: %+v", got5)
	}
}

// TestBucketMeanSkipsUnknownDuty: the controller records -1 for a duty
// unknown after a failed write; the raw tier keeps the marker, the means
// are formed from the written duties only, and a channel whose duty was
// unknown in every point of the bucket has no duty in the mean.
func TestBucketMeanSkipsUnknownDuty(t *testing.T) {
	c := &clock{t: time.Unix(t0, 0)}
	s := New("", 10*time.Second, c.now, nil)
	for i, d := range []int{100, -1, 120, -1, -1, 140} {
		p := point(c.t.Unix(), 30, d, 1000)
		p.Duty["hdd"] = -1
		s.Push(p)
		if i == 0 {
			if raw := s.Range(RawSpan, 0); raw[0].Duty["cpu"] != 100 {
				t.Fatalf("raw: %+v", raw)
			}
		}
		c.advance(10 * time.Second)
	}
	raw := s.Range(RawSpan, 0)
	if len(raw) != 6 || raw[1].Duty["cpu"] != -1 || raw[5].Duty["hdd"] != -1 {
		t.Errorf("raw tier must keep the marker: %+v", raw)
	}
	got := s.Range(3*time.Hour, 0)
	if len(got) != 1 || got[0].Duty["cpu"] != 120 { // (100+120+140)/3, not (100-3+120+140)/6 = 60
		t.Errorf("1-min mean: %+v", got)
	}
	if _, ok := got[0].Duty["hdd"]; ok {
		t.Errorf("a duty unknown in every point must be absent from the mean: %+v", got[0].Duty)
	}
	if got5 := s.Range(48*time.Hour, 0); len(got5) != 1 || got5[0].Duty["cpu"] != 120 {
		t.Errorf("5-min mean: %+v", got5)
	}
}

func TestTierCutoffsAndCapacity(t *testing.T) {
	c := &clock{t: time.Unix(t0, 0)}
	s := New("", 60*time.Second, c.now, nil)
	// 26 h of one point per minute: raw keeps 2 h, 1-min keeps 24 h, 5-min all
	fill(s, c, 26*60, 60, 20)
	if got := s.Range(RawSpan, 0); len(got) != 120 {
		t.Errorf("raw: %d", len(got))
	}
	m1 := s.Range(Min1Span, 0)
	if len(m1) != 1440 || m1[0].TS != c.t.Unix()-24*3600 {
		t.Errorf("1-min: %d from %d (now %d)", len(m1), m1[0].TS, c.t.Unix())
	}
	// the last 1-min bucket is the open one (its only point)
	if m1[1439].TS != c.t.Unix()-60 {
		t.Errorf("1-min open bucket at %d, want %d", m1[1439].TS, c.t.Unix()-60)
	}
	m5 := s.Range(Min5Span, 0)
	if len(m5) != 26*12 || m5[0].TS != t0 {
		t.Errorf("5-min: %d from %d", len(m5), m5[0].TS)
	}
	// a 3 h span picks the 1-min tier and cuts at now-3h
	h3 := s.Range(3*time.Hour, 0)
	if len(h3) != 180 || h3[0].TS != c.t.Unix()-3*3600 {
		t.Errorf("3 h: %d from %d", len(h3), h3[0].TS)
	}
	// 5-min capacity: 8 days of points keep 2016 buckets
	fill(s, c, (8*24-26)*60, 60, 20)
	if got := s.Range(Min5Span, 0); len(got) != 2016 {
		t.Errorf("5-min capacity: %d", len(got))
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	c := &clock{t: time.Unix(t0, 0)}
	l := &logs{}
	s := New(path, 10*time.Second, c.now, l.f)
	if len(l.lines) != 1 || !strings.Contains(l.lines[0], "starting empty") {
		t.Errorf("missing file log: %v", l.lines)
	}
	// 3 h at 10 s, then 4 points into a fresh minute (open buckets)
	fill(s, c, 3*360, 10, 30)
	fill(s, c, 4, 10, 30)
	before := map[string][]Point{"raw": s.Range(RawSpan, 0), "m1": s.Range(Min1Span, 0), "m5": s.Range(Min5Span, 0)}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("file mode: %v %v", st.Mode(), err)
		}
	}
	var f file
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &f); err != nil || f.Format != 1 || f.Interval != "10s" {
		t.Errorf("file header: %+v %v", f, err)
	}

	// reload with the same clock: identical tiers
	l2 := &logs{}
	s2 := New(path, 10*time.Second, c.now, l2.f)
	if len(l2.lines) != 1 || !strings.Contains(l2.lines[0], "loaded") {
		t.Errorf("want one \"loaded\" line on load, got %v", l2.lines)
	}
	for name, want := range before {
		var got []Point
		switch name {
		case "raw":
			got = s2.Range(RawSpan, 0)
		case "m1":
			got = s2.Range(Min1Span, 0)
		default:
			got = s2.Range(Min5Span, 0)
		}
		if !samePoints(got, want) {
			t.Errorf("%s differs after load: %d vs %d points", name, len(got), len(want))
		}
	}
	// the open bucket continues its mean: 4 points at 30..33 plus two at 40
	// → mean of six = (30+31+32+33+40+40)/6 = 34.333
	s2.Push(point(c.t.Unix(), 40, 100, 1000))
	c.advance(10 * time.Second)
	s2.Push(point(c.t.Unix(), 40, 100, 1000))
	m1 := s2.Range(Min1Span, 0)
	if last := m1[len(m1)-1]; last.Temp["cpu"] != 34.333 {
		t.Errorf("continued open bucket: %+v", last)
	}

	// reload 25 h later: raw and 1-min are gone, 5-min keeps everything
	c.advance(25 * time.Hour)
	s3 := New(path, 10*time.Second, c.now, nil)
	if got := s3.Range(RawSpan, 0); len(got) != 0 {
		t.Errorf("raw after 25 h: %d", len(got))
	}
	if got := s3.Range(Min1Span, 0); len(got) != 0 {
		t.Errorf("1-min after 25 h: %d", len(got))
	}
	if got := s3.Range(Min5Span, 0); len(got) != len(before["m5"]) {
		t.Errorf("5-min after 25 h: %d, want %d", len(got), len(before["m5"]))
	}
}

func samePoints(a, b []Point) bool {
	if len(a) != len(b) {
		return false
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func TestCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	for name, body := range map[string]string{"garbage": "{not json", "format": `{"format":2,"raw":[]}`} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			l := &logs{}
			s := New(path, 10*time.Second, nil, l.f)
			if len(l.lines) != 1 || !strings.Contains(l.lines[0], "starting empty") {
				t.Errorf("log: %v", l.lines)
			}
			if got := s.Range(RawSpan, 0); len(got) != 0 {
				t.Errorf("points after corrupt load: %d", len(got))
			}
			// a Save repairs the file
			s.Push(point(time.Now().Unix(), 1, 1, 1))
			if err := s.Save(); err != nil {
				t.Fatal(err)
			}
			if got := New(path, 10*time.Second, nil, nil).Range(RawSpan, 0); len(got) != 1 {
				t.Errorf("after repair: %d", len(got))
			}
		})
	}
}

func TestUnwritableLoggedOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", "history.json")
	l := &logs{}
	s := New(path, 10*time.Second, nil, l.f)
	l.lines = nil
	s.Push(point(time.Now().Unix(), 1, 1, 1))
	if err := s.Save(); err == nil {
		t.Fatal("Save into a missing directory succeeded")
	}
	_ = s.Save()
	if len(l.lines) != 1 || !strings.Contains(l.lines[0], "memory only") {
		t.Errorf("log lines: %v", l.lines)
	}
	if got := s.Range(RawSpan, 0); len(got) != 1 {
		t.Errorf("memory store lost the point")
	}
	// memory-only store: Save is a no-op
	if err := New("", 10*time.Second, nil, nil).Save(); err != nil {
		t.Errorf("memory Save: %v", err)
	}
}

func TestCSV(t *testing.T) {
	pts := []Point{
		{TS: t0, Temp: map[string]float64{"cpu": 36.5, "hdd": 31}, Duty: map[string]int{"cpu": 85, "hdd": 140}, RPM: map[string]int{"cpu": 2000}, Extra: map[string]float64{"ec:system": 32, "a,b": 40.25}},
		{TS: t0 + 10, Temp: map[string]float64{"cpu": 37}, Duty: map[string]int{"cpu": 90, "hdd": 140}, RPM: map[string]int{"cpu": 2100}},
	}
	var buf bytes.Buffer
	if err := CSV(&buf, pts, []string{"cpu", "hdd"}, []string{"a,b", "ec:system"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: %q", lines)
	}
	if lines[0] != `ts,time,cpu_temp,cpu_duty,cpu_rpm,hdd_temp,hdd_duty,hdd_rpm,"a,b",ec:system` {
		t.Errorf("header: %s", lines[0])
	}
	local := time.Unix(t0, 0).Format(time.RFC3339)
	if lines[1] != fmt.Sprintf("%d,%s,36.5,85,2000,31,140,,40.25,32", t0, local) {
		t.Errorf("row 1: %s", lines[1])
	}
	if !strings.HasSuffix(lines[2], ",37,90,2100,,140,,,") {
		t.Errorf("row 2 (empty cells): %s", lines[2])
	}
	// no points: header only
	buf.Reset()
	if err := CSV(&buf, nil, []string{"cpu"}, nil); err != nil || strings.TrimSpace(buf.String()) != "ts,time,cpu_temp,cpu_duty,cpu_rpm" {
		t.Errorf("empty: %q %v", buf.String(), err)
	}
}
