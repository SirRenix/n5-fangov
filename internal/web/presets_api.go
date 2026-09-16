// presets_api.go holds the per-preset endpoints (/api/presets/{name}: detail,
// delete, rename); list, apply and save are in web.go.
package web

import (
	"errors"
	"io/fs"
	"net/http"
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
