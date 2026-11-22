package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// assistBase is a config with two channels the [[assist]] tables refer to.
const assistBase = `[[channel]]
name = "ssd"
pwm = 2
sensor = "nvme:max"
curve = [[35, 74], [68, 255]]
critical = 72

[[channel]]
name = "hdd"
pwm = 3
sensor = ["drivetemp:max", "disk:sda"]
curve = [[30, 87], [55, 255]]
critical = 60
stop = 140
`

// assistHDD is the table of the design (HDD values of the 16.11. measurement).
const assistHDD = `
[[assist]]
channel = "hdd"                # the channel that is raised
enabled = false                # switch
hysteresis = 2
min_on = "2m"

[[assist.source]]
sensor = "hwmon:amdgpu:temp1"
start = 50
full = 58
boost = 45

[[assist.source]]
sensor = "nvme:max"
start = 46
full = 50
boost = 45
`

func TestAssistParse(t *testing.T) {
	cfg, warns, err := Parse([]byte(assistBase + assistHDD))
	if err != nil || len(warns) != 0 {
		t.Fatalf("parse: %v %v", err, warns)
	}
	want := []Assist{{Channel: "hdd", Enabled: false, Hysteresis: 2, MinOn: 2 * time.Minute, Sources: []AssistSource{
		{Sensor: "hwmon:amdgpu:temp1", Start: 50, Full: 58, Boost: 45},
		{Sensor: "nvme:max", Start: 46, Full: 50, Boost: 45},
	}}}
	if !reflect.DeepEqual(cfg.Assists, want) {
		t.Errorf("assists %+v", cfg.Assists)
	}
	if cfg.Assist("hdd") == nil || cfg.Assist("ssd") != nil {
		t.Error("Assist lookup")
	}
	// defaults: enabled false, hysteresis 2, min_on 2m
	cfg, warns, _ = Parse([]byte(assistBase + "[[assist]]\nchannel = \"hdd\"\n[[assist.source]]\nsensor = \"k10temp\"\nstart = 60\nfull = 70\nboost = 10\n"))
	if len(warns) != 0 || len(cfg.Assists) != 1 || cfg.Assists[0].Enabled || cfg.Assists[0].Hysteresis != 2 || cfg.Assists[0].MinOn != 2*time.Minute {
		t.Errorf("defaults: %+v %v", cfg.Assists, warns)
	}
	// Clone is deep
	c2 := cfg.Clone()
	c2.Assists[0].Sources[0].Boost = 99
	c2.Assists[0].Enabled = true
	if cfg.Assists[0].Sources[0].Boost != 10 || cfg.Assists[0].Enabled {
		t.Error("Clone shares the assist tables")
	}
}

// TestAssistRules: every rule broken once. A table that breaks an identity
// rule is dropped with a warning naming the field; the channels stay and
// parsing never fails (rule 8). hysteresis, min_on and enabled fall back
// to their default and keep the table.
func TestAssistRules(t *testing.T) {
	src := func(lines ...string) string { return "\n[[assist.source]]\n" + strings.Join(lines, "\n") + "\n" }
	good := src(`sensor = "k10temp"`, "start = 60", "full = 70", "boost = 10")
	table := func(head string, sources ...string) string {
		return assistBase + "\n[[assist]]\n" + head + "\n" + strings.Join(sources, "")
	}
	cases := []struct {
		name, raw, field string
		kept             bool
	}{
		{"no channel", table(`enabled = true`, good), "assist[0].channel", false},
		{"bad channel name", table(`channel = "HDD!"`, good), "assist[0].channel", false},
		{"unknown channel", table(`channel = "pcie"`, good), "assist.pcie.channel", false},
		{"no sources", table(`channel = "hdd"`), "assist.hdd.source", false},
		{"source not a table", table("channel = \"hdd\"\nsource = \"k10temp\""), "assist.hdd.source", false},
		{"five sources", table(`channel = "hdd"`,
			src(`sensor = "a"`, "start = 1", "full = 2", "boost = 1"), src(`sensor = "b"`, "start = 1", "full = 2", "boost = 1"),
			src(`sensor = "c"`, "start = 1", "full = 2", "boost = 1"), src(`sensor = "d"`, "start = 1", "full = 2", "boost = 1"),
			src(`sensor = "e"`, "start = 1", "full = 2", "boost = 1")), "assist.hdd.source", false},
		{"sensor missing", table(`channel = "hdd"`, src("start = 60", "full = 70", "boost = 10")), "assist.hdd.source[0].sensor", false},
		{"two ids", table(`channel = "hdd"`, src(`sensor = "k10temp,nvme:max"`, "start = 60", "full = 70", "boost = 10")), "assist.hdd.source[0].sensor", false},
		{"pattern id", table(`channel = "hdd"`, src(`sensor = "hwmon:<name>:temp1"`, "start = 60", "full = 70", "boost = 10")), "assist.hdd.source[0].sensor", false},
		{"own sensor part", table(`channel = "hdd"`, src(`sensor = "drivetemp:max"`, "start = 30", "full = 40", "boost = 10")), "assist.hdd.source[0].sensor", false},
		{"own sensor", table(`channel = "ssd"`, src(`sensor = "nvme:max"`, "start = 30", "full = 40", "boost = 10")), "assist.ssd.source[0].sensor", false},
		{"duplicate source", table(`channel = "hdd"`, good, good), "assist.hdd.source[1].sensor", false},
		{"start missing", table(`channel = "hdd"`, src(`sensor = "k10temp"`, "full = 70", "boost = 10")), "assist.hdd.source[0].start", false},
		{"start not integer", table(`channel = "hdd"`, src(`sensor = "k10temp"`, "start = 60.5", "full = 70", "boost = 10")), "assist.hdd.source[0].start", false},
		{"full out of range", table(`channel = "hdd"`, src(`sensor = "k10temp"`, "start = 60", "full = 130", "boost = 10")), "assist.hdd.source[0].full", false},
		{"start not below full", table(`channel = "hdd"`, src(`sensor = "k10temp"`, "start = 70", "full = 70", "boost = 10")), "assist.hdd.source[0]", false},
		{"boost 0", table(`channel = "hdd"`, src(`sensor = "k10temp"`, "start = 60", "full = 70", "boost = 0")), "assist.hdd.source[0].boost", false},
		{"boost 101", table(`channel = "hdd"`, src(`sensor = "k10temp"`, "start = 60", "full = 70", "boost = 101")), "assist.hdd.source[0].boost", false},
		{"hysteresis 11", table("channel = \"hdd\"\nhysteresis = 11", good), "assist.hdd.hysteresis", true},
		{"min_on 31m", table("channel = \"hdd\"\nmin_on = \"31m\"", good), "assist.hdd.min_on", true},
		{"enabled not bool", table("channel = \"hdd\"\nenabled = \"yes\"", good), "assist.hdd.enabled", true},
		{"unknown key", table("channel = \"hdd\"\nboost = 5", good), "assist.hdd.boost", true},
		{"unknown source key", table(`channel = "hdd"`, src(`sensor = "k10temp"`, "start = 60", "full = 70", "boost = 10", "step = 1")), "assist.hdd.source[0].step", true},
		{"not an array", assistBase + "\n[assist]\nchannel = \"hdd\"\n", "assist", false},
	}
	for _, c := range cases {
		cfg, warns, err := Parse([]byte(c.raw))
		if err != nil {
			t.Errorf("%s: parse error %v (rule 8: never fails)", c.name, err)
			continue
		}
		found := false
		for _, w := range warns {
			if w.Field == c.field {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no warning on %s: %v", c.name, c.field, warns)
		}
		if got := len(cfg.Assists) == 1; got != c.kept {
			t.Errorf("%s: table kept = %v, want %v (%+v)", c.name, got, c.kept, cfg.Assists)
		}
		if len(cfg.Channels) != 2 {
			t.Errorf("%s: channels %d", c.name, len(cfg.Channels))
		}
		if c.kept && (cfg.Assists[0].Hysteresis != AssistHysteresisDefault || cfg.Assists[0].MinOn != AssistMinOnDefault || cfg.Assists[0].Enabled) {
			t.Errorf("%s: defaults not used: %+v", c.name, cfg.Assists[0])
		}
	}
	// at most one table per channel: the second is dropped, the first stays
	cfg, warns, _ := Parse([]byte(table(`channel = "hdd"`, good) + "\n[[assist]]\nchannel = \"hdd\"\nenabled = true\n" + good))
	if len(cfg.Assists) != 1 || cfg.Assists[0].Enabled || len(warns) != 1 || warns[0].Field != "assist.hdd.channel" {
		t.Errorf("second table: %+v %v", cfg.Assists, warns)
	}
}

// TestAssistMarshalRoundTrip: Marshal (setup, the preset fallback) writes
// the tables so that Parse reads them back unchanged.
func TestAssistMarshalRoundTrip(t *testing.T) {
	cfg, _, _ := Parse([]byte(assistBase + assistHDD))
	cfg.Assists[0].Enabled = true
	back, warns, err := Parse(Marshal(cfg))
	if err != nil || len(warns) != 0 {
		t.Fatalf("re-parse: %v %v\n%s", err, warns, Marshal(cfg))
	}
	if !reflect.DeepEqual(back.Assists, cfg.Assists) {
		t.Errorf("round trip: %+v, want %+v", back.Assists, cfg.Assists)
	}
	// no table: no [[assist]] in the output
	cfg.Assists = nil
	if strings.Contains(string(Marshal(cfg)), "assist") {
		t.Error("empty assist list marshalled")
	}
}

// TestReplaceChannelsKeepsAssist: presets, the curve editor and Apply to
// daemon rewrite [[channel]]; the [[assist]] tables keep their bytes,
// wherever they sit in the file.
func TestReplaceChannelsKeepsAssist(t *testing.T) {
	newChans := []Channel{
		{Name: "ssd", PWM: 2, Sensor: "nvme:max", Curve: [][2]int{{30, 90}, {65, 255}}, Critical: 72, Stop: "auto"},
		{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{28, 105}, {50, 255}}, Critical: 60, Stop: "140"},
	}
	for name, raw := range map[string]string{
		"after the channels":   assistBase + assistHDD,
		"before the channels":  strings.TrimPrefix(assistHDD, "\n") + "\n" + assistBase,
		"between the channels": strings.Replace(assistBase, "\n[[channel]]\nname = \"hdd\"", assistHDD+"\n[[channel]]\nname = \"hdd\"", 1),
	} {
		out := string(ReplaceChannels([]byte(raw), newChans))
		block := strings.TrimSpace(assistHDD)
		if !strings.Contains(out, block) {
			t.Errorf("%s: assist block not byte-identical:\n%s", name, out)
		}
		cfg, warns, err := Parse([]byte(out))
		if err != nil || len(warns) != 0 {
			t.Errorf("%s: %v %v\n%s", name, err, warns, out)
			continue
		}
		if len(cfg.Assists) != 1 || len(cfg.Assists[0].Sources) != 2 || cfg.Channel("hdd").Curve[0][0] != 28 {
			t.Errorf("%s: %+v / %+v", name, cfg.Assists, cfg.Channels)
		}
	}
}

// TestSetAssistEnabledCRLF: a CRLF file keeps CRLF on the rewritten and on
// an inserted line.
func TestSetAssistEnabledCRLF(t *testing.T) {
	raw := strings.ReplaceAll(assistBase+assistHDD, "\n", "\r\n")
	out, found := SetAssistEnabled([]byte(raw), "hdd", true)
	if !found || string(out) != strings.Replace(raw, "enabled = false ", "enabled = true ", 1) {
		t.Errorf("rewrite (%v):\n%q", found, out)
	}
	noEnabled := strings.ReplaceAll(assistBase+"[[assist]]\nchannel = \"hdd\"\n[[assist.source]]\nsensor = \"k10temp\"\nstart = 60\nfull = 70\nboost = 10\n", "\n", "\r\n")
	out, _ = SetAssistEnabled([]byte(noEnabled), "hdd", true)
	if strings.Count(string(out), "\n") != strings.Count(string(out), "\r\n") || !strings.Contains(string(out), "channel = \"hdd\"\r\nenabled = true\r\n") {
		t.Errorf("insert:\n%q", out)
	}
	if cfg, warns, err := Parse(out); err != nil || len(warns) != 0 || !cfg.Assist("hdd").Enabled {
		t.Errorf("parse: %v %v", err, warns)
	}
}

// TestSetAssistEnabledDuplicateAndBroken: with two tables for one channel
// the first is edited (the one the parser keeps) and the second keeps its
// bytes; the helper is line-based and edits a file with a syntax error
// elsewhere too — refusing that is the store's job (cmd assistStore).
func TestSetAssistEnabledDuplicateAndBroken(t *testing.T) {
	second := "\n[[assist]]\nchannel = \"hdd\"\nenabled = false # second\n[[assist.source]]\nsensor = \"k10temp\"\nstart = 60\nfull = 70\nboost = 10\n"
	raw := assistBase + assistHDD + second
	out, found := SetAssistEnabled([]byte(raw), "hdd", true)
	if !found || !strings.HasSuffix(string(out), second) || strings.Count(string(out), "enabled = true") != 1 {
		t.Errorf("duplicate:\n%s", out)
	}
	if cfg, _, _ := Parse(out); len(cfg.Assists) != 1 || !cfg.Assists[0].Enabled || cfg.Assists[0].Sources[0].Sensor != "hwmon:amdgpu:temp1" {
		t.Errorf("parsed: %+v", cfg.Assists)
	}
	broken := "[daemon\n" + assistBase + assistHDD
	out, found = SetAssistEnabled([]byte(broken), "hdd", true)
	if !found || !strings.HasPrefix(string(out), "[daemon\n") {
		t.Errorf("broken file: %v", found)
	}
	if _, _, err := Parse(out); err == nil {
		t.Error("the syntax error vanished")
	}
}

// TestExampleAssistBlock: the commented-out [[assist]] example of the
// shipped config parses without a warning once uncommented.
func TestExampleAssistBlock(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	i := strings.Index(text, "# [[assist]]")
	if i < 0 {
		t.Fatal("no [[assist]] example")
	}
	var block []string
	for _, ln := range strings.Split(text[i:], "\n") {
		block = append(block, strings.TrimPrefix(strings.TrimPrefix(ln, "#"), " "))
	}
	cfg, warns, err := Parse([]byte(text[:i] + strings.Join(block, "\n")))
	if err != nil || len(warns) != 0 {
		t.Fatalf("example with assist: %v %v", err, warns)
	}
	a := cfg.Assist("hdd")
	if a == nil || a.Enabled || a.Hysteresis != 2 || a.MinOn != 2*time.Minute || len(a.Sources) != 2 ||
		a.Sources[0] != (AssistSource{Sensor: "hwmon:amdgpu:temp1", Start: 50, Full: 58, Boost: 45}) ||
		a.Sources[1] != (AssistSource{Sensor: "nvme:max", Start: 46, Full: 50, Boost: 45}) {
		t.Errorf("example values: %+v", a)
	}
}

func TestSetAssistEnabled(t *testing.T) {
	raw := `# my config
[daemon]
interval = "10s"

` + assistBase + `
# assist for the ssd fans
[[assist]]
channel = 'ssd'
[[assist.source]]
sensor = "k10temp"
start = 60
full = 70
boost = 10
` + assistHDD + `
[[schedule]]
preset = "n5pro-quiet"
`
	if _, _, err := Parse([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	// existing line: value replaced, alignment and comment kept, nothing else touched
	out, found := SetAssistEnabled([]byte(raw), "hdd", true)
	if !found {
		t.Fatal("hdd not found")
	}
	want := strings.Replace(raw, "enabled = false ", "enabled = true ", 1)
	if string(out) != want {
		t.Errorf("hdd on:\n%s", out)
	}
	cfg, warns, err := Parse(out)
	if err != nil || len(warns) != 0 || !cfg.Assist("hdd").Enabled || cfg.Assist("ssd").Enabled {
		t.Errorf("parse after: %v %v %+v", err, warns, cfg.Assists)
	}
	// and back
	back, _ := SetAssistEnabled(out, "hdd", false)
	if string(back) != raw {
		t.Errorf("hdd off again differs:\n%s", back)
	}
	// no enabled line (single-quoted channel): inserted after the channel line
	out, found = SetAssistEnabled([]byte(raw), "ssd", true)
	if !found || !strings.Contains(string(out), "channel = 'ssd'\nenabled = true\n[[assist.source]]") {
		t.Errorf("ssd insert (%v):\n%s", found, out)
	}
	if cfg, _, _ := Parse(out); !cfg.Assist("ssd").Enabled || cfg.Assist("hdd").Enabled {
		t.Errorf("ssd on: %+v", cfg.Assists)
	}
	// a channel without a table, a [[channel]] of that name does not count
	if out, found := SetAssistEnabled([]byte(raw), "cpu", true); found || string(out) != raw {
		t.Error("cpu: found")
	}
	if _, found := SetAssistEnabled(nil, "hdd", true); found {
		t.Error("empty text: found")
	}
	// a source key named like the switch is not the table's own key
	odd := assistBase + "[[assist]]\nchannel = \"hdd\"\n[[assist.source]]\nsensor = \"k10temp\"\nenabled = false\nstart = 60\nfull = 70\nboost = 10\n"
	out, _ = SetAssistEnabled([]byte(odd), "hdd", true)
	if !strings.Contains(string(out), "channel = \"hdd\"\nenabled = true\n[[assist.source]]") || !strings.Contains(string(out), "sensor = \"k10temp\"\nenabled = false\n") {
		t.Errorf("insert before the source table, the source's line untouched:\n%s", out)
	}
}
