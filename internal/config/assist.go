package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// maxSensorIDLen bounds one assist source id (the longest built-in forms,
// hwmon:<name>:tempN and disk:<dev>, are far shorter).
const maxSensorIDLen = 128

// ValidSensorID checks the shape of a single sensor id as [dashboard]
// sensors and [[assist.source]] take it: trimmed, non-empty, one id (no
// ","), no pattern placeholder ("<…>"), no quotes, backslashes or control
// characters. Whether the id resolves on this machine is the controller's
// and check's business. The error text is meant to be appended to the id.
func ValidSensorID(id string) error {
	switch {
	case id == "":
		return errors.New("is empty")
	case len(id) > maxSensorIDLen:
		return fmt.Errorf("is longer than %d bytes", maxSensorIDLen)
	case id != strings.TrimSpace(id):
		return errors.New("has surrounding blanks")
	case strings.Contains(id, ","):
		return errors.New("is more than one id (\",\"); an assist source is one sensor")
	case strings.ContainsAny(id, "<>"):
		return errors.New("is a pattern, not a concrete id")
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\'' || r == '\\' {
			return errors.New("contains a quote, backslash or control character")
		}
	}
	return nil
}

// assists validates the [[assist]] tables against the parsed channels
// (DESIGN "Assist"). Identity problems drop the table with one warning —
// no or an unknown channel, a second table for the same channel, a missing
// or invalid source list — and regulation runs on without assist for that
// channel (rule 8). hysteresis, min_on and enabled fall back to their
// defaults with a warning, as on [[channel]].
func (p *parser) assists(secs []map[string]toml.Primitive, chans []Channel) []Assist {
	var out []Assist
	seen := map[string]bool{}
	for i, sec := range secs {
		pre := fmt.Sprintf("assist[%d]", i)
		name, ok := p.strField(pre, sec, "channel", "")
		name = strings.TrimSpace(name)
		if ok && nameRe.MatchString(name) {
			pre = "assist." + name
		}
		p.unknown(pre, sec, "channel", "enabled", "hysteresis", "min_on", "source")
		if !ok || !nameRe.MatchString(name) {
			p.warn(pre+".channel", "missing or not [a-z0-9_]{1,32}, table dropped")
			continue
		}
		var ch *Channel
		for j := range chans {
			if chans[j].Name == name {
				ch = &chans[j]
				break
			}
		}
		if ch == nil {
			p.warn(pre+".channel", "no [[channel]] named %q, table dropped", name)
			continue
		}
		if seen[name] {
			p.warn(pre+".channel", "a second [[assist]] table for channel %q, table dropped", name)
			continue
		}
		seen[name] = true
		a := Assist{Channel: name}
		a.Enabled = p.boolField(pre, sec, "enabled", false)
		a.Hysteresis = p.intField(pre, sec, "hysteresis", AssistHysteresisDefault, 0, HysteresisMax)
		a.MinOn = p.durField(pre, sec, "min_on", AssistMinOnDefault, 0, AssistMinOnMax)
		srcs, ok := p.assistSources(pre, sec, *ch)
		if !ok {
			continue
		}
		a.Sources = srcs
		out = append(out, a)
	}
	return out
}

// assistSources validates the [[assist.source]] tables of one assist
// table: 1..AssistMaxSources entries, each with one sensor id that is not
// the channel's own sensor (nor a part of it) and listed once, start <
// full within the curve temperature range and boost within
// AssistBoostMin..AssistBoostMax. Any problem drops the whole table (false).
func (p *parser) assistSources(pre string, sec map[string]toml.Primitive, ch Channel) ([]AssistSource, bool) {
	prim, ok := sec["source"]
	if !ok {
		p.warn(pre+".source", "missing (1..%d [[assist.source]] tables), table dropped", AssistMaxSources)
		return nil, false
	}
	var secs []map[string]toml.Primitive
	if err := p.md.PrimitiveDecode(prim, &secs); err != nil {
		p.warn(pre+".source", "not an array of tables ([[assist.source]]), table dropped")
		return nil, false
	}
	if len(secs) == 0 || len(secs) > AssistMaxSources {
		p.warn(pre+".source", "%d sources, need 1..%d, table dropped", len(secs), AssistMaxSources)
		return nil, false
	}
	own := ch.Sensors()
	ids := map[string]bool{}
	out := make([]AssistSource, 0, len(secs))
	for j, s := range secs {
		spre := fmt.Sprintf("%s.source[%d]", pre, j)
		p.unknown(spre, s, "sensor", "start", "full", "boost")
		id, ok := p.strField(spre, s, "sensor", "")
		if !ok {
			p.warn(spre+".sensor", "missing, table dropped")
			return nil, false
		}
		if err := ValidSensorID(id); err != nil {
			p.warn(spre+".sensor", "%q %v, table dropped", id, err)
			return nil, false
		}
		if id == ch.Sensor || slices.Contains(own, id) {
			p.warn(spre+".sensor", "%q is the sensor of channel %q itself, table dropped", id, ch.Name)
			return nil, false
		}
		if ids[id] {
			p.warn(spre+".sensor", "%q listed twice, table dropped", id)
			return nil, false
		}
		ids[id] = true
		src := AssistSource{Sensor: id}
		if src.Start, ok = p.reqInt(spre, s, "start", MinCurveTemp, MaxCurveTemp); !ok {
			return nil, false
		}
		if src.Full, ok = p.reqInt(spre, s, "full", MinCurveTemp, MaxCurveTemp); !ok {
			return nil, false
		}
		if src.Boost, ok = p.reqInt(spre, s, "boost", AssistBoostMin, AssistBoostMax); !ok {
			return nil, false
		}
		if src.Start >= src.Full {
			p.warn(spre, "start %d not below full %d, table dropped", src.Start, src.Full)
			return nil, false
		}
		out = append(out, src)
	}
	return out, true
}

// reqInt decodes a mandatory integer within [lo, hi]; false with one
// warning ("table dropped") when it is missing, not an integer or outside.
func (p *parser) reqInt(prefix string, sec map[string]toml.Primitive, key string, lo, hi int) (int, bool) {
	prim, ok := sec[key]
	if !ok {
		p.warn(prefix+"."+key, "missing, table dropped")
		return 0, false
	}
	var v int64
	if err := p.md.PrimitiveDecode(prim, &v); err != nil {
		p.warn(prefix+"."+key, "not an integer, table dropped")
		return 0, false
	}
	if v < int64(lo) || v > int64(hi) {
		p.warn(prefix+"."+key, "%d outside %d..%d, table dropped", v, lo, hi)
		return 0, false
	}
	return int(v), true
}
