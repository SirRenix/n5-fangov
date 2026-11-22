// versions_api.go holds the config versions (DESIGN "Config versions"):
// GET /api/config/versions, GET /api/config/versions/{id} and POST
// /api/config/versions/{id}/restore, scope admin like GET/PUT /api/config.
// password_hash never leaves: every text is redacted ("<redacted>", a
// version whose hash differs from the current one "<redacted, changed>"),
// error texts too.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"regexp"
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
	// Impact names the restart-only keys a restore of raw writes and the
	// warnings it needs (sign-in off, listener off loopback, another user).
	Impact(raw []byte) (restartKeys, warnings []string)
	// Restore writes version id as the config (validated first; the text
	// before becomes a version) and reloads the daemon. ifCurrent, when set,
	// is the hash the file must still have (ErrChanged otherwise).
	Restore(id int, ifCurrent string) (RestoreResult, error)
}

// ErrChanged refuses a restore whose expected current text changed
// meanwhile (409): an Undo must not revert someone else's later write.
var ErrChanged = errors.New("the config changed meanwhile")

// RestoreResult is the answer of a restore: RestartKeys are written but
// take effect at the next start (listen, auth, the log file, the profile,
// the web user and password); Version is the version the restore stored
// (the text before it; 0: none) and SHA the hash of the text written.
type RestoreResult struct {
	RestartRequired bool     `json:"restart_required"`
	RestartKeys     []string `json:"restart_keys"`
	Warnings        []string `json:"warnings"`
	Version         int      `json:"version"`
	SHA             string   `json:"config_sha256"`
}

// WriteResult is what a config write of a request stored: the version (0:
// none) and the hash of the written text (the Undo's if_current).
type WriteResult struct {
	Version int
	SHA     string
}

// asCaller runs fn — a store call that may write the config — as r's
// caller (Deps.ConfigActor); only the call, never the body read, holds the
// daemon's write turn.
func (s *Server) asCaller(r *http.Request, fn func() error) (WriteResult, error) {
	return s.asBy(ackSource(CallerFrom(r.Context())), fn)
}

// asBy is asCaller with an explicit author ("" = the daemon itself). done
// runs deferred: a store call that panics (net/http recovers the handler)
// must not leave the write turn taken.
func (s *Server) asBy(by string, fn func() error) (res WriteResult, err error) {
	if s.deps.ConfigActor == nil {
		return WriteResult{}, fn()
	}
	done := s.deps.ConfigActor(by)
	defer func() { res.Version, res.SHA = done() }()
	return WriteResult{}, fn()
}

// Redacted is the placeholder of password_hash in a version text;
// RedactedChanged marks a version whose hash differs from the current one.
const (
	Redacted        = "<redacted>"
	RedactedChanged = "<redacted, changed>"
)

// hashLike matches hash values wherever they stand (a version that does
// not parse: an inline table, a multi-line string): pbkdf2$… and the legacy
// 64-hex sha256.
var hashLike = regexp.MustCompile(`pbkdf2\$[0-9A-Za-z$]+|\b[0-9a-fA-F]{64}\b`)

// redactAs replaces password_hash in raw with ph: the assignment lines
// (RedactRaw), every known hash value as a substring, anything that looks
// like a hash, and a line naming password_hash whose value is not a plain
// assignment is blanked to "password_hash = <ph>".
func redactAs(raw, ph string, known ...string) string {
	return redactText(raw, ph, true, known...)
}

// redactText is redactAs; blank false keeps lines naming password_hash
// (an error message: its hash values are still removed).
func redactText(raw, ph string, blank bool, known ...string) string {
	out := mapHashLiterals(raw, func(v string) (string, bool) { return ph, v != "" })
	if h := currentHash(raw); len(h) >= minLiteralHash {
		out = strings.ReplaceAll(out, h, ph)
	}
	for _, h := range known {
		if len(h) >= 8 {
			out = strings.ReplaceAll(out, h, ph)
		}
	}
	out = hashLike.ReplaceAllString(out, ph)
	if !blank || !strings.Contains(out, "password_hash") {
		return out
	}
	ls := strings.Split(out, "\n")
	for i, l := range ls {
		if strings.Contains(l, "password_hash") && !hashLine.MatchString(l) {
			ls[i] = l[:len(l)-len(strings.TrimLeft(l, " \t"))] + "password_hash = <" + strings.Trim(ph, "<>") + ">"
		}
	}
	return strings.Join(ls, "\n")
}

// redactErr removes the hashes of the given texts from an error message.
func redactErr(msg string, known []string, texts ...[]byte) string {
	for _, t := range texts {
		if h := currentHash(string(t)); h != "" {
			known = append(known, h)
		}
	}
	return redactText(msg, Redacted, false, known...)
}

// knownHashes are the password hashes of the current file and of every
// stored version (each is replaced as a substring wherever it appears).
func (s *Server) knownHashes() []string {
	var out []string
	if cur, err := s.deps.Versions.Current(); err == nil {
		if h := currentHash(string(cur)); h != "" {
			out = append(out, h)
		}
	}
	if l, err := s.deps.Versions.List(); err == nil {
		for _, v := range l {
			if _, raw, err := s.deps.Versions.Get(v.ID); err == nil {
				if h := currentHash(string(raw)); h != "" {
					out = append(out, h)
				}
			}
		}
	}
	return out
}

// VersionDiff returns the version text redacted and the line diff current
// → version (what a restore changes) on redacted texts; changed reports a
// password_hash that differs from the current one (shown as
// RedactedChanged). known are further hash values to remove (the other
// versions'). The API and `n5-fangov config diff` use it.
func VersionDiff(version, current []byte, known ...string) (redacted string, diff []cfgver.Op, passwordChanged bool) {
	passwordChanged = currentHash(string(version)) != currentHash(string(current))
	ph := Redacted
	if passwordChanged {
		ph = RedactedChanged
	}
	known = append(known, currentHash(string(version)), currentHash(string(current)))
	redacted = redactAs(string(version), ph, known...)
	return redacted, cfgver.Diff(redactAs(string(current), Redacted, known...), redacted), passwordChanged
}

// versionView is one version in the API: the list carries the sections
// the next state changed; the detail adds the redacted text, the diff
// against the current file (current → version: what a restore would
// change, "-" lines go, "+" lines come) and what a restore needs to say.
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
	RestartKeys     []string    `json:"restart_keys,omitempty"`
	CurrentSHA      string      `json:"config_sha256,omitempty"`
	Warnings        []string    `json:"warnings,omitempty"`
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
		writeError(w, http.StatusInternalServerError, "config versions: "+redactErr(err.Error(), s.knownHashes()))
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
		writeError(w, http.StatusInternalServerError, "config versions: "+redactErr(err.Error(), s.knownHashes(), raw))
		return
	}
	cur, err := s.deps.Versions.Current()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	known := s.knownHashes()
	vr, d, changed := VersionDiff(raw, cur, known...)
	rm, add := cfgver.Count(d)
	keys, warns := s.deps.Versions.Impact(raw)
	for i := range warns {
		warns[i] = redactErr(warns[i], known, raw, cur)
	}
	vv := versionView{ID: v.ID, TS: v.TS, By: v.By, Reason: v.Reason, Sections: s.sectionsAfter(v, raw, cur),
		Raw: vr, Diff: d, Changes: cfgver.Sections(string(cur), string(raw)), Removed: &rm, Added: &add, PasswordChanged: &changed,
		RestartKeys: keys, Warnings: warns, CurrentSHA: cfgver.Sum(cur)}
	if vv.Changes == nil {
		vv.Changes = []string{}
	}
	writeJSON(w, http.StatusOK, vv)
}

// restoreBody is the optional POST body: if_current = the hash the file
// must still have (the Undo after a write).
type restoreBody struct {
	IfCurrent string `json:"if_current"`
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
	var b restoreBody
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxOverrideBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	if len(strings.TrimSpace(string(body))) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&b); err != nil {
			writeError(w, http.StatusBadRequest, `body must be {"if_current": "<sha256>"} or empty`)
			return
		}
	}
	known := s.knownHashes()
	_, raw, _ := s.deps.Versions.Get(id)
	var res RestoreResult
	_, err = s.asCaller(r, func() (err error) { res, err = s.deps.Versions.Restore(id, b.IfCurrent); return err })
	if err != nil {
		msg := redactErr(err.Error(), known, raw)
		switch {
		case errors.Is(err, ErrChanged):
			writeError(w, http.StatusConflict, "restore: "+msg)
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
		res.Warnings[i] = redactErr(x, known, raw)
	}
	s.event(r, events.Restore, "", fmt.Sprintf("config restored to version %d", id))
	status := http.StatusOK
	if res.RestartRequired {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"ok": true, "restored": id, "version": res.Version, "config_sha256": res.SHA, "restart_required": res.RestartRequired,
		"restart_keys": res.RestartKeys, "warnings": res.Warnings})
}
