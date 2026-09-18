// presets_api.go holds the per-preset endpoints (/api/presets/{name}: detail,
// delete, rename, save); list and apply are in web.go.
package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// deletePreset removes a user preset: ErrPresetBuiltin → 409, missing →
// 404; a store without Delete answers 501.
func (s *Server) deletePreset(w http.ResponseWriter, r *http.Request) {
	if s.deps.Presets == nil {
		writeError(w, http.StatusNotImplemented, "no preset store")
		return
	}
	d, ok := s.deps.Presets.(PresetDeleter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "preset store cannot delete")
		return
	}
	name := r.PathValue("name")
	if !presetName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid preset name")
		return
	}
	if err := d.Delete(name); err != nil {
		switch {
		case errors.Is(err, ErrPresetBuiltin):
			writeError(w, http.StatusConflict, "delete preset: "+err.Error())
		case errors.Is(err, fs.ErrNotExist):
			writeError(w, http.StatusNotFound, "unknown preset "+name)
		default:
			writeError(w, http.StatusInternalServerError, "delete preset: "+err.Error())
		}
		return
	}
	s.logf("web: preset %q deleted by %s", name, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": name})
}

// getPreset shows a preset's channel tables (the operator asked to see what a
// saved preset contains, not only its channel names).
func (s *Server) getPreset(w http.ResponseWriter, r *http.Request) {
	if s.deps.Presets == nil {
		writeError(w, http.StatusNotImplemented, "no preset store")
		return
	}
	d, ok := s.deps.Presets.(PresetDetailer)
	if !ok {
		writeError(w, http.StatusNotImplemented, "preset store has no detail view")
		return
	}
	name := r.PathValue("name")
	if !presetName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid preset name")
		return
	}
	det, err := d.Detail(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "unknown preset "+name)
			return
		}
		writeError(w, http.StatusInternalServerError, "read preset: "+err.Error())
		return
	}
	if det.Channels == nil {
		det.Channels = []PresetChannel{}
	}
	writeJSON(w, http.StatusOK, det)
}

// renamePreset: {"name": new}. Built-in names on either side → 409, an
// existing target → 409, a missing source → 404.
func (s *Server) renamePreset(w http.ResponseWriter, r *http.Request) {
	if s.deps.Presets == nil {
		writeError(w, http.StatusNotImplemented, "no preset store")
		return
	}
	rn, ok := s.deps.Presets.(PresetRenamer)
	if !ok {
		writeError(w, http.StatusNotImplemented, "preset store cannot rename")
		return
	}
	old := r.PathValue("name")
	var b struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if !presetName.MatchString(old) || !presetName.MatchString(b.Name) {
		writeError(w, http.StatusBadRequest, "invalid preset name (a-z, 0-9, _ and -, at most 64 characters)")
		return
	}
	if old == b.Name {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": b.Name})
		return
	}
	if err := rn.Rename(old, b.Name); err != nil {
		switch {
		case errors.Is(err, ErrPresetBuiltin), errors.Is(err, fs.ErrExist):
			writeError(w, http.StatusConflict, "rename preset: "+err.Error())
		case errors.Is(err, fs.ErrNotExist):
			writeError(w, http.StatusNotFound, "unknown preset "+old)
		default:
			writeError(w, http.StatusInternalServerError, "rename preset: "+err.Error())
		}
		return
	}
	s.logf("web: preset %q renamed to %q by %s", old, b.Name, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": b.Name})
}

// savePreset: PUT /api/presets/{name}. An empty body saves the running
// [[channel]] tables (PresetStore.Save). A JSON body {channels:[…]} saves
// the composed channels instead (the preset editor's values — nothing is
// applied): the body is rendered as [[channel]] TOML and parsed with the
// config's own channel rules; any warning the parser would substitute a
// default for is a 400 {error, errors[]} here, like PUT /api/config?strict=1.
// The channel names must be the running config's (a preset is applied
// to this host). A built-in name is 409.
func (s *Server) savePreset(w http.ResponseWriter, r *http.Request) {
	if s.deps.Presets == nil {
		writeError(w, http.StatusNotImplemented, "no preset store")
		return
	}
	name := r.PathValue("name")
	if !presetName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid preset name")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if isTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", maxBody))
			return
		}
		writeError(w, http.StatusBadRequest, "unreadable body: "+err.Error())
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		err = s.deps.Presets.Save(name)
	} else {
		cs, ok := s.deps.Presets.(PresetChannelSaver)
		if !ok {
			writeError(w, http.StatusNotImplemented, "preset store cannot save composed channels")
			return
		}
		var b struct {
			Channels []presetChannelIn `json:"channels"`
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if derr := dec.Decode(&b); derr != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+derr.Error())
			return
		}
		// one JSON value and nothing after it: a second document, a stray
		// bracket or text behind the object is not silently ignored
		if derr := dec.Decode(new(json.RawMessage)); derr != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid JSON body: trailing data after the object")
			return
		}
		chans, errs := s.presetChannels(b.Channels)
		if len(errs) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "preset rejected", "errors": errs})
			return
		}
		err = cs.SaveChannels(name, chans)
	}
	if err != nil {
		if errors.Is(err, ErrPresetBuiltin) {
			// The contract says 409 for a built-in name, like delete.
			writeError(w, http.StatusConflict, "save preset: "+err.Error())
			return
		}
		writeError(w, storeStatus(err), "save preset: "+err.Error())
		return
	}
	s.logf("web: preset %q saved by %s", name, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": name})
}

// presetChannels validates a composed channel list with the config
// parser (config.MarshalChannels → config.ParseChannels: every warning is
// an error here, a channel the parser drops is one too) and checks the
// channel set against the running config. It returns the parsed channels
// or the list of errors.
func (s *Server) presetChannels(in []presetChannelIn) ([]config.Channel, []string) {
	var errs []string
	if len(in) == 0 {
		return nil, []string{"channels: none given"}
	}
	chans := make([]config.Channel, 0, len(in))
	for i, c := range in {
		pre := fmt.Sprintf("channel[%d]", i)
		if channelName.MatchString(c.Name) {
			pre = "channel." + c.Name
		}
		// every point is exactly [temp, duty]: a [][2]int field would read
		// [40] as [40, 0] and drop the third value of [40, 80, 1]
		curve := make([][2]int, 0, len(c.Curve))
		for j, pt := range c.Curve {
			if len(pt) != 2 {
				errs = append(errs, fmt.Sprintf("%s.curve: point %d has %d values, need [temp, duty]", pre, j, len(pt)))
				continue
			}
			curve = append(curve, [2]int{pt[0], pt[1]})
		}
		ch := config.Channel{Name: c.Name, PWM: c.PWM, Sensor: c.Sensor, Curve: curve, Critical: c.Critical, Stop: c.Stop, Hysteresis: c.Hysteresis}
		if ch.Stop == "" {
			ch.Stop = "auto"
		}
		if strings.TrimSpace(c.MinOn) != "" {
			d, err := time.ParseDuration(strings.TrimSpace(c.MinOn))
			if err != nil || d < 0 {
				errs = append(errs, pre+".min_on: "+fmt.Sprintf("%q is not a duration", c.MinOn))
				continue
			}
			ch.MinOn = d
		}
		chans = append(chans, ch)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	parsed, warns, err := config.ParseChannels(config.MarshalChannels(chans))
	if err != nil {
		return nil, []string{"channel: " + err.Error()}
	}
	for _, w := range warns {
		errs = append(errs, w.String())
	}
	if len(parsed) != len(chans) && len(errs) == 0 {
		errs = append(errs, fmt.Sprintf("channel: %d of %d channels usable", len(parsed), len(chans)))
	}
	if len(errs) > 0 {
		return nil, errs
	}
	if s.deps.Config != nil {
		raw, rerr := s.deps.Config.Raw()
		if rerr == nil {
			if cfg, _, perr := config.Parse(raw); perr == nil && len(cfg.Channels) > 0 {
				want, have := channelKeySet(cfg.Channels), channelKeySet(parsed)
				if !slices.Equal(want, have) {
					return nil, []string{fmt.Sprintf("channel: names/pwm %v do not match the running config %v", have, want)}
				}
			}
		}
	}
	return parsed, nil
}

// presetChannelIn is one channel of the PUT /api/presets/{name} body: like
// PresetChannel, but the curve is decoded as free-length points so that a
// malformed point is an error instead of a silently padded or truncated
// one (presetChannels checks the shape).
type presetChannelIn struct {
	Name       string  `json:"name"`
	PWM        int     `json:"pwm"`
	Sensor     string  `json:"sensor"`
	Curve      [][]int `json:"curve"`
	Critical   int     `json:"critical"`
	Stop       string  `json:"stop"`
	Hysteresis int     `json:"hysteresis"`
	MinOn      string  `json:"min_on"`
}

// channelKeySet lists the channels as "name@pwmN" sorted (set comparison):
// Apply merges a preset by pwm and keeps the config channel's name, so a
// preset whose names sit on other pwms than the running config's would
// swap curves between channels on apply.
func channelKeySet(chans []config.Channel) []string {
	out := make([]string, 0, len(chans))
	for _, c := range chans {
		out = append(out, fmt.Sprintf("%s@pwm%d", c.Name, c.PWM))
	}
	slices.Sort(out)
	return out
}
