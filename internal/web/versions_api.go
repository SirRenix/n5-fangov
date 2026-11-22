// versions_api.go holds the config versions (DESIGN "Config versions"):
// GET /api/config/versions, GET /api/config/versions/{id} and POST
// /api/config/versions/{id}/restore, scope admin like GET/PUT /api/config.
// password_hash never leaves: every text is redacted ("<redacted>", a
// version whose hash differs from the current one "<redacted, changed>"),
// error texts too.
package web

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/SirRenix/n5-fangov/internal/cfgver"
	"github.com/SirRenix/n5-fangov/internal/events"
)

// ConfigVersions is the version store behind the endpoints (cmd: the
// config-versions directory in the state dir).
type ConfigVersions interface {
	// List returns the versions, newest first.
	List() ([]cfgver.Version, error)
	// Get returns one version and its text (fs.ErrNotExist: unknown id).
	Get(id int) (cfgver.Version, []byte, error)
	// Next returns the text that followed version id; nil for the newest.
	Next(id int) ([]byte, error)
	// Current returns the config file as it is now.
	Current() ([]byte, error)
	// Restore writes version id as the config (validated first; the text
	// before becomes a version) and reloads the daemon.
	Restore(id int) (RestoreResult, error)
}

// RestoreResult is the answer of a restore: RestartKeys are written but
// take effect at the next start (listen, auth, the log file, the profile,
// the web user and password); Version is the newest version afterwards
// (the text before the restore).
type RestoreResult struct {
	RestartRequired bool     `json:"restart_required"`
	RestartKeys     []string `json:"restart_keys"`
	Warnings        []string `json:"warnings"`
	Version         int      `json:"version"`
}

// Redacted is the placeholder of password_hash in a version text; Changed
// marks a version whose hash differs from the current one.
const (
	Redacted        = "<redacted>"
	RedactedChanged = "<redacted, changed>"
)

// redactAs replaces password_hash in raw like RedactRaw, with ph.
func redactAs(raw, ph string) string {
	out := mapHashLiterals(raw, func(v string) (string, bool) { return ph, v != "" })
	if h := currentHash(raw); len(h) >= minLiteralHash {
		out = strings.ReplaceAll(out, h, ph)
	}
	return out
}

// redactErr removes the hashes of the given texts from an error message.
func redactErr(msg string, texts ...[]byte) string {
	msg = redactAs(msg, Redacted)
	for _, t := range texts {
		if h := currentHash(string(t)); h != "" {
			msg = strings.ReplaceAll(msg, h, Redacted)
		}
	}
	return msg
}

// VersionDiff returns the version text redacted and the line diff current
// → version (what a restore changes) on redacted texts; changed reports a
// password_hash that differs from the current one (shown as
// RedactedChanged). The API and `n5-fangov config diff` use it.
func VersionDiff(version, current []byte) (redacted string, diff []cfgver.Op, passwordChanged bool) {
	passwordChanged = currentHash(string(version)) != currentHash(string(current))
	ph := Redacted
	if passwordChanged {
		ph = RedactedChanged
	}
	redacted = redactAs(string(version), ph)
	return redacted, cfgver.Diff(redactAs(string(current), Redacted), redacted), passwordChanged
}

// versionView is one version in the API: the list carries the sections
// the next state changed; the detail adds the redacted text and the diff
// against the current file (current → version: what a restore would
// change, "-" lines go, "+" lines come).
type versionView struct {
	ID              int         `json:"id"`
	TS              int64       `json:"ts"`
	By              string      `json:"by"`
	Reason          string      `json:"reason"`
	Sections        []string    `json:"sections"`
	Raw             string      `json:"raw,omitempty"`
	Diff            []cfgver.Op `json:"diff,omitempty"`
	Changes         []string    `json:"changes,omitempty"`
	Removed         *int        `json:"removed,omitempty"`
	Added           *int        `json:"added,omitempty"`
	PasswordChanged *bool       `json:"password_changed,omitempty"`
}

func (s *Server) versionID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid version id")
		return 0, false
	}
	return id, true
}

// sectionsAfter names the sections the state after version v changed.
func (s *Server) sectionsAfter(v cfgver.Version, raw, cur []byte) []string {
	next, err := s.deps.Versions.Next(v.ID)
	if err != nil {
		return []string{}
	}
	if next == nil {
		next = cur
	}
	out := cfgver.Sections(string(raw), string(next))
	if out == nil {
		out = []string{}
	}
	return out
}

func (s *Server) getConfigVersions(w http.ResponseWriter, r *http.Request) {
	if s.deps.Versions == nil {
		writeError(w, http.StatusNotImplemented, "no config versions")
		return
	}
	list, err := s.deps.Versions.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config versions: "+err.Error())
		return
	}
	cur, _ := s.deps.Versions.Current()
	out := make([]versionView, 0, len(list))
	for _, v := range list {
		vv := versionView{ID: v.ID, TS: v.TS, By: v.By, Reason: v.Reason, Sections: []string{}}
		if _, raw, err := s.deps.Versions.Get(v.ID); err == nil {
			vv.Sections = s.sectionsAfter(v, raw, cur)
		}
		out = append(out, vv)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getConfigVersion(w http.ResponseWriter, r *http.Request) {
	if s.deps.Versions == nil {
		writeError(w, http.StatusNotImplemented, "no config versions")
		return
	}
	id, ok := s.versionID(w, r)
	if !ok {
		return
	}
	v, raw, err := s.deps.Versions.Get(id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("no version %d", id))
			return
		}
		writeError(w, http.StatusInternalServerError, "config versions: "+redactErr(err.Error(), raw))
		return
	}
	cur, err := s.deps.Versions.Current()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	vr, d, changed := VersionDiff(raw, cur)
	rm, add := cfgver.Count(d)
	vv := versionView{ID: v.ID, TS: v.TS, By: v.By, Reason: v.Reason, Sections: s.sectionsAfter(v, raw, cur),
		Raw: vr, Diff: d, Changes: cfgver.Sections(string(cur), string(raw)), Removed: &rm, Added: &add, PasswordChanged: &changed}
	if vv.Changes == nil {
		vv.Changes = []string{}
	}
	writeJSON(w, http.StatusOK, vv)
}

func (s *Server) restoreConfigVersion(w http.ResponseWriter, r *http.Request) {
	if s.deps.Versions == nil {
		writeError(w, http.StatusNotImplemented, "no config versions")
		return
	}
	id, ok := s.versionID(w, r)
	if !ok {
		return
	}
	_, raw, _ := s.deps.Versions.Get(id)
	cur, _ := s.deps.Versions.Current()
	res, err := s.deps.Versions.Restore(id)
	if err != nil {
		msg := redactErr(err.Error(), raw, cur)
		switch {
		case errors.Is(err, fs.ErrNotExist) && !isStoreError(err):
			writeError(w, http.StatusNotFound, fmt.Sprintf("no version %d", id))
		case isReloadError(err):
			writeError(w, http.StatusInternalServerError, "restore: "+msg)
		default:
			writeError(w, storeStatus(err), "restore: "+msg)
		}
		return
	}
	if res.RestartKeys == nil {
		res.RestartKeys = []string{}
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	for i, x := range res.Warnings {
		res.Warnings[i] = redactErr(x, raw, cur)
	}
	s.event(r, events.Restore, "", fmt.Sprintf("config restored to version %d", id))
	status := http.StatusOK
	if res.RestartRequired {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"ok": true, "restored": id, "version": res.Version, "restart_required": res.RestartRequired,
		"restart_keys": res.RestartKeys, "warnings": res.Warnings})
}
