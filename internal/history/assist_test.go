package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAssistRoundTrip: the assist boost per channel is stored, averaged
// over the bucket (a point without it counts 0) and survives Save/Load;
// a point without a boost carries no assist member at all.
func TestAssistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	c := &clock{t: time.Unix(t0, 0)}
	s := New(path, 10*time.Second, c.now, nil)
	// first minute: boosts 45, 45, 30 and three points without (mean 20)
	for i := 0; i < 6; i++ {
		p := point(c.t.Unix(), 30, 100, 1000)
		if i < 3 {
			p.Assist = map[string]int{"hdd": []int{45, 45, 30}[i]}
		}
		s.Push(p)
		c.advance(10 * time.Second)
	}
	s.Push(point(c.t.Unix(), 30, 100, 1000)) // opens the second minute
	raw := s.Range(RawSpan, 0)
	if raw[0].Assist["hdd"] != 45 || raw[3].Assist != nil {
		t.Errorf("raw: %v %v", raw[0].Assist, raw[3].Assist)
	}
	m1 := s.Range(3*time.Hour, 0)
	if m1[0].Assist["hdd"] != 20 || m1[1].Assist != nil {
		t.Errorf("1-min means: %v %v", m1[0].Assist, m1[1].Assist)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"assist":{"hdd":45}`) || strings.Count(string(b), `"assist"`) < 4 {
		t.Errorf("file: assist members missing or written for zero boosts: %s", b)
	}
	back := New(path, 10*time.Second, c.now, nil)
	if got := back.Range(RawSpan, 0); len(got) != 7 || got[1].Assist["hdd"] != 45 || got[2].Assist["hdd"] != 30 || got[4].Assist != nil {
		t.Errorf("loaded raw: %+v", got)
	}
	if got := back.Range(3*time.Hour, 0); got[0].Assist["hdd"] != 20 {
		t.Errorf("loaded 1-min: %+v", got[0])
	}
	// the copies handed out are the caller's
	got := back.Range(RawSpan, 0)
	got[1].Assist["hdd"] = -1
	if back.Range(RawSpan, 0)[1].Assist["hdd"] != 45 {
		t.Error("Range returned the stored assist map")
	}
}

// TestLoadHistoryWithoutAssist: a history file written by 0.6 (no assist
// member, the same format number) loads unchanged; the points carry no
// assist map and new points with one follow.
func TestLoadHistoryWithoutAssist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	old := map[string]any{
		"format": 1, "interval": "10s",
		"raw": []map[string]any{
			{"ts": t0, "temp": map[string]float64{"hdd": 40}, "duty": map[string]int{"hdd": 99}, "rpm": map[string]int{"hdd": 1646}, "extra": map[string]float64{"hwmon:amdgpu:temp1": 35}},
			{"ts": t0 + 10, "temp": map[string]float64{"hdd": 40}, "duty": map[string]int{"hdd": 99}, "rpm": map[string]int{"hdd": 1650}},
		},
		"min1": []map[string]any{{"ts": t0, "temp": map[string]float64{"hdd": 40}, "duty": map[string]int{"hdd": 99}, "rpm": map[string]int{"hdd": 1648}}},
		"min5": []map[string]any{},
	}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Unix(t0+20, 0)}
	l := &logs{}
	s := New(path, 10*time.Second, c.now, l.f)
	got := s.Range(RawSpan, 0)
	if len(got) != 2 || got[0].Duty["hdd"] != 99 || got[0].Extra["hwmon:amdgpu:temp1"] != 35 || got[0].Assist != nil {
		t.Fatalf("0.6 file: %+v (log %v)", got, l.lines)
	}
	p := point(c.t.Unix(), 41, 151, 2400)
	p.Assist = map[string]int{"hdd": 45}
	s.Push(p)
	if got := s.Range(RawSpan, 0); len(got) != 3 || got[2].Assist["hdd"] != 45 {
		t.Errorf("after push: %+v", got)
	}
	// and the other way: what 0.7 writes decodes into the 0.6 point layout
	// (no assist field) without an error
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var v06 struct {
		Format int `json:"format"`
		Raw    []struct {
			TS   int64              `json:"ts"`
			Temp map[string]float64 `json:"temp"`
			Duty map[string]int     `json:"duty"`
		} `json:"raw"`
	}
	if err := json.Unmarshal(data, &v06); err != nil || v06.Format != 1 || len(v06.Raw) != 3 || v06.Raw[2].Duty["cpu"] != 151 {
		t.Errorf("0.6 reader: %v %+v", err, v06)
	}
}
