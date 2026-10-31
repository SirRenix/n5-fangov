package validate

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sample is one row of samples.csv (TESTSPEC §8). Unknown values are -1
// (integers) or NaN (temperatures, load); both are written as empty cells.
type Sample struct {
	Time time.Time // real read time (t_iso)
	// TS is the due time in seconds since the run started (t_s, TESTSPEC
	// 0.4 §8, #11); for a sample off its due time (OffGrid) and for an
	// unscheduled one, the real time since the start.
	TS      float64
	OffGrid bool // read more than GridTolerance from its due time: a gap, not judged
	Phase   string
	Ch      int // channel under test, 0 = none
	Written int // duty written in this phase, -1 = none (EC automatic)
	DCR     [5]int
	Fan     [3]int
	ECCPU   float64
	ECSys   float64
	ECBoard float64
	ECAmb   float64
	HDDMax  float64
	NVMeMax float64
	Tctl    float64
	Load1   float64
}

// CSVHeader is the column list of samples.csv, in the order of TESTSPEC §8.
var CSVHeader = []string{"t_iso", "t_s", "phase", "ch", "written", "dcr1", "dcr2", "dcr3", "dcr4", "dcr5", "fan1", "fan2", "fan3", "ec_cpu", "ec_sys", "ec_board", "ec_amb", "hdd_max", "nvme_max", "tctl", "load1", "room_t", "room_rh"}

func cellInt(v int) string {
	if v < 0 {
		return ""
	}
	return strconv.Itoa(v)
}

func cellF(v float64) string {
	if math.IsNaN(v) {
		return ""
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func (s Sample) row() []string {
	r := []string{s.Time.UTC().Format(time.RFC3339), strconv.FormatFloat(s.TS, 'f', 1, 64), s.Phase, cellInt(s.Ch), cellInt(s.Written)}
	for _, v := range s.DCR {
		r = append(r, cellInt(v))
	}
	for _, v := range s.Fan {
		r = append(r, cellInt(v))
	}
	for _, v := range []float64{s.ECCPU, s.ECSys, s.ECBoard, s.ECAmb, s.HDDMax, s.NVMeMax, s.Tctl} {
		r = append(r, cellF(v))
	}
	r = append(r, strconv.FormatFloat(s.Load1, 'f', 2, 64))
	if math.IsNaN(s.Load1) {
		r[len(r)-1] = ""
	}
	// room_t, room_rh: reference rig only, not recorded by this tool
	return append(r, "", "")
}

// Bundle is one run's result directory (TESTSPEC §8).
type Bundle struct {
	Meta    any
	Samples []Sample
	Events  []string
	Verdict any
	// Redact, when set, is applied to the text of every file before it is
	// written and hashed (TESTSPEC 0.4 header, #8).
	Redact func(string) string
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// DirName is "<YYYYMMDD-HHMM>-<board>-<bios>-<test>".
func DirName(t time.Time, board, bios, test string) string {
	clean := func(s string) string {
		s = unsafeName.ReplaceAllString(strings.TrimSpace(s), "_")
		if s == "" {
			return "unknown"
		}
		return s
	}
	return fmt.Sprintf("%s-%s-%s-%s", t.Format("20060102-1504"), clean(board), clean(bios), clean(test))
}

// PrepareOut makes sure out can take a bundle before a run starts: it
// creates out with its parents and creates and removes a probe file in it.
// validate calls it before the gates and before anything is written to the
// EC; an error ends the run there (maintenance window 1, D1-b: the bundle
// directory was created only after the run, without its parents, and the
// bundle was lost).
func PrepareOut(out string) error {
	if err := os.MkdirAll(out, 0o750); err != nil {
		return fmt.Errorf("output directory %s: %w", out, err)
	}
	f, err := os.CreateTemp(out, ".n5-fangov-probe-*")
	if err != nil {
		return fmt.Errorf("output directory %s not writable: %w", out, err)
	}
	name := f.Name()
	cerr := f.Close()
	if rerr := os.Remove(name); rerr != nil && cerr == nil {
		cerr = rerr
	}
	if cerr != nil {
		return fmt.Errorf("output directory %s: probe file: %w", out, cerr)
	}
	return nil
}

// Write creates dir (it must not exist) with meta.json, samples.csv,
// events.log, verdict.json and SHA256SUMS over the four. Missing parents of
// dir are created (PrepareOut has normally done that before the run).
func (b Bundle) Write(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o750); err != nil {
		return err
	}
	files := map[string][]byte{}
	var err error
	if files["meta.json"], err = json.MarshalIndent(b.Meta, "", "  "); err != nil {
		return err
	}
	if files["verdict.json"], err = json.MarshalIndent(b.Verdict, "", "  "); err != nil {
		return err
	}
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	_ = w.Write(CSVHeader)
	for _, s := range b.Samples {
		_ = w.Write(s.row())
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	files["samples.csv"] = []byte(sb.String())
	files["events.log"] = []byte(strings.Join(b.Events, "\n") + "\n")
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, n := range names {
		data := files[n]
		if n == "meta.json" || n == "verdict.json" {
			data = append(data, '\n')
		}
		if b.Redact != nil {
			data = []byte(b.Redact(string(data)))
		}
		if err := os.WriteFile(filepath.Join(dir, n), data, 0o640); err != nil {
			return err
		}
		h := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), n)
	}
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o640)
}
