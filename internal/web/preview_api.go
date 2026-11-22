// preview_api.go holds POST /api/preview: the curve preview over the
// recorded history (DESIGN "Preview"). It writes nothing.
package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
)

// previewBody is the request: everything optional. No body = the running
// config over 24 h.
type previewBody struct {
	Channels []presetChannelIn `json:"channels"`
	Preset   string            `json:"preset"`
	Assist   map[string]bool   `json:"assist"`
	Span     string            `json:"span"`
}

func (s *Server) postPreview(w http.ResponseWriter, r *http.Request) {
	if s.deps.Preview == nil {
		writeError(w, http.StatusNotImplemented, "no preview")
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
	var b previewBody
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if derr := dec.Decode(&b); derr != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+derr.Error())
			return
		}
		if derr := dec.Decode(new(json.RawMessage)); derr != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid JSON body: trailing data after the object")
			return
		}
	}
	req := PreviewRequest{Preset: b.Preset, Assist: b.Assist}
	switch b.Span {
	case "", "24h":
		req.Span = control.PreviewSpanDay
	case "7d":
		req.Span = control.PreviewSpanWeek
	default:
		writeError(w, http.StatusBadRequest, `span must be "24h" or "7d"`)
		return
	}
	if len(b.Channels) > 0 && b.Preset != "" {
		writeError(w, http.StatusBadRequest, "give either channels or preset, not both")
		return
	}
	if b.Preset != "" && !presetName.MatchString(b.Preset) {
		writeError(w, http.StatusBadRequest, "invalid preset name")
		return
	}
	for name := range b.Assist {
		if !channelName.MatchString(name) {
			writeError(w, http.StatusBadRequest, "assist: invalid channel name")
			return
		}
	}
	if len(b.Channels) > 0 {
		chans, errs := previewDrafts(b.Channels, s.deps.Preview.RunningChannels())
		if len(errs) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "preview rejected", "errors": errs})
			return
		}
		req.Channels = chans
	}
	res, err := s.deps.Preview.Preview(req)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, res)
	case errors.Is(err, control.ErrPreviewBusy):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrBadInput):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "preview: "+err.Error())
	}
}

// previewDrafts matches draft channels to the running ones by name (a part
// of the channels is fine, the rest stays as running), takes pwm and stop
// from the running channel when the draft has none, and validates the
// drafts with the preset rules (parseChannelDrafts).
func previewDrafts(in []presetChannelIn, running []config.Channel) ([]config.Channel, []string) {
	byName := map[string]config.Channel{}
	for _, c := range running {
		byName[c.Name] = c
	}
	var errs []string
	seen := map[string]bool{}
	for i := range in {
		rc, ok := byName[in[i].Name]
		switch {
		case !ok:
			errs = append(errs, fmt.Sprintf("channel %q: not a running channel", in[i].Name))
			continue
		case seen[in[i].Name]:
			errs = append(errs, fmt.Sprintf("channel %q: given twice", in[i].Name))
			continue
		case in[i].PWM != 0 && in[i].PWM != rc.PWM:
			errs = append(errs, fmt.Sprintf("channel %q: pwm%d, the running channel is pwm%d", in[i].Name, in[i].PWM, rc.PWM))
			continue
		}
		seen[in[i].Name] = true
		in[i].PWM = rc.PWM
		if in[i].Stop == "" {
			in[i].Stop = rc.Stop
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return parseChannelDrafts(in)
}
