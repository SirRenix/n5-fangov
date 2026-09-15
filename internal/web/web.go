// Package web serves the HTTP API (DESIGN.md "HTTP API") and the embedded
// static UI. The same mux is served on TCP (with CSRF header + optional basic
// auth on state-changing methods) and on the unix socket (no auth, see
// SocketHandler).
package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SirRenix/pvefand/internal/control"
	"github.com/SirRenix/pvefand/internal/ipc"
)

//go:embed static/index.html static/app.js static/app.css
var staticFS embed.FS

// CSRFHeader must be present (value "1") on every state-changing request over TCP.
const CSRFHeader = "X-Pvefand-Csrf"

// maxBody bounds request bodies (config TOML, override JSON).
const maxBody = 256 << 10

// ConfigStore is the daemon's config file access.
type ConfigStore interface {
	// Raw returns the current config file text.
	Raw() ([]byte, error)
	// Save validates and writes a new config file text.
	Save(raw []byte) error
}

// ParsedConfigStore is optionally implemented by a ConfigStore; when present,
// GET /api/config also carries the parsed config under "config".
type ParsedConfigStore interface {
	Parsed() (any, error)
}

// Preset is one entry of /etc/pvefand/presets.
type Preset struct {
	Name     string   `json:"name"`
	Channels []string `json:"channels"` // channel names contained in the preset
}

// PresetStore lists, applies and saves curve presets.
type PresetStore interface {
	List() ([]Preset, error)
	// Apply replaces the [[channel]] tables of the running config with the
	// preset and reloads the daemon. May return control.ErrRestartRequired.
	Apply(name string) error
	// Save writes the current [[channel]] tables as preset name.
	Save(name string) error
}

// LogSource returns the last n log lines.
type LogSource func(lines int) ([]string, error)

// ProfileInfo describes one hardware profile for /api/profiles.
type ProfileInfo struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Verified bool   `json:"verified"`
	Notes    string `json:"notes"`
	Active   bool   `json:"active"`
}

// SensorInfo is one selectable sensor source for the curve editor.
type SensorInfo struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// AuthConfig mirrors the [web] auth settings.
type AuthConfig struct {
	Mode         string // "none" | "basic"
	User         string
	PasswordHash string // sha256 hex of "user:password", see PasswordHash
}

// Deps wires the handler to the rest of the daemon. Nil optional members
// (Config, Presets, Log, Profiles, Sensors) make the corresponding endpoints
// answer 501.
type Deps struct {
	Service  control.Service
	Config   ConfigStore
	Presets  PresetStore
	Log      LogSource
	Profiles func() []ProfileInfo
	Version  string
	Auth     AuthConfig
	Sensors  func() []SensorInfo
}

// Server holds the mux and serves it on TCP and on the unix socket.
type Server struct {
	deps   Deps
	mux    *http.ServeMux
	tcp    http.Handler
	socket http.Handler
}

// New builds a Server from deps.
func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.routes()
	s.socket = s.mux
	s.tcp = s.guard(s.mux)
	return s
}

// NewHandler returns the TCP handler (CSRF + auth enforced).
func NewHandler(deps Deps) http.Handler { return New(deps).Handler() }

// Handler is the TCP handler: CSRF header and (when Auth.Mode == "basic")
// basic auth are required on state-changing methods.
func (s *Server) Handler() http.Handler { return s.tcp }

// SocketHandler is the same mux without CSRF/auth, for the unix socket.
func (s *Server) SocketHandler() http.Handler { return s.socket }

// ListenAndServe serves the TCP handler on tcpAddr until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context, tcpAddr string) error {
	ln, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("web: listen %s: %w", tcpAddr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve serves the TCP handler on ln until ctx is done.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.tcp,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(c)
		case <-done:
		}
	}()
	err := srv.Serve(ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ServeSocket serves the unauthenticated SocketHandler on the unix socket at
// socketPath (see internal/ipc) until ctx is done.
func (s *Server) ServeSocket(ctx context.Context, socketPath string) error {
	return ipc.Serve(ctx, socketPath, s.socket)
}

// PasswordHash computes the configured hash form: sha256 hex of "user:password".
func PasswordHash(user, password string) string {
	sum := sha256.Sum256([]byte(user + ":" + password))
	return hex.EncodeToString(sum[:])
}

// ---- routing -------------------------------------------------------------

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/state", s.getState)
	m.HandleFunc("GET /api/history", s.getHistory)
	m.HandleFunc("GET /api/config", s.getConfig)
	m.HandleFunc("PUT /api/config", s.putConfig)
	m.HandleFunc("PUT /api/override/{name}", s.putOverride)
	m.HandleFunc("DELETE /api/override/{name}", s.deleteOverride)
	m.HandleFunc("GET /api/presets", s.getPresets)
	m.HandleFunc("POST /api/presets/{name}/apply", s.applyPreset)
	m.HandleFunc("PUT /api/presets/{name}", s.savePreset)
	m.HandleFunc("GET /api/log", s.getLog)
	m.HandleFunc("GET /api/profiles", s.getProfiles)
	m.HandleFunc("GET /api/version", s.getVersion)
	m.HandleFunc("GET /api/sensors", s.getSensors)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "unknown endpoint")
	})
	m.HandleFunc("/", s.static)
}

// guard enforces the CSRF header and basic auth on state-changing methods.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(CSRFHeader) != "1" {
			writeError(w, http.StatusForbidden, "missing "+CSRFHeader+" header")
			return
		}
		if strings.EqualFold(s.deps.Auth.Mode, "basic") {
			if !s.authorized(r) {
				// No WWW-Authenticate challenge on purpose: the UI shows its own
				// login form and sends the Authorization header itself.
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authorized checks basic auth in constant time against the configured hash.
func (s *Server) authorized(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	want, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(s.deps.Auth.PasswordHash)))
	if err != nil || len(want) != sha256.Size {
		// Misconfigured hash: nobody gets in (fail closed).
		return false
	}
	got := sha256.Sum256([]byte(user + ":" + pass))
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.deps.Auth.User))
	hashOK := subtle.ConstantTimeCompare(got[:], want)
	return userOK&hashOK == 1
}

// ---- handlers ------------------------------------------------------------

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	if s.deps.Service == nil {
		writeError(w, http.StatusNotImplemented, "no service")
		return
	}
	snap := s.deps.Service.Snapshot()
	if snap.Channels == nil {
		snap.Channels = []control.ChannelState{}
	}
	if snap.ExtraTemps == nil {
		snap.ExtraTemps = map[string]float64{}
	}
	if snap.Alerts == nil {
		snap.Alerts = map[string]int64{}
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	if s.deps.Service == nil {
		writeError(w, http.StatusNotImplemented, "no service")
		return
	}
	minutes := 120
	if q := r.URL.Query().Get("minutes"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 24*60 {
			writeError(w, http.StatusBadRequest, "minutes must be 1..1440")
			return
		}
		minutes = n
	}
	pts := s.deps.Service.History(time.Duration(minutes) * time.Minute)
	if pts == nil {
		pts = []control.HistoryPoint{}
	}
	writeJSON(w, http.StatusOK, pts)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
		writeError(w, http.StatusNotImplemented, "no config store")
		return
	}
	raw, err := s.deps.Config.Raw()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	out := map[string]any{"raw": string(raw)}
	if p, ok := s.deps.Config.(ParsedConfigStore); ok {
		if cfg, err := p.Parsed(); err == nil {
			out["config"] = cfg
		} else {
			out["parse_error"] = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil || s.deps.Service == nil {
		writeError(w, http.StatusNotImplemented, "no config store")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "body too large or unreadable")
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		writeError(w, http.StatusBadRequest, "empty config")
		return
	}
	if err := s.deps.Config.Save(body); err != nil {
		writeError(w, http.StatusBadRequest, "config rejected: "+err.Error())
		return
	}
	err = s.deps.Service.Reload(body)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_required": false})
	case errors.Is(err, control.ErrRestartRequired):
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "restart_required": true, "message": err.Error()})
	default:
		writeError(w, http.StatusInternalServerError, "config saved, reload failed: "+err.Error())
	}
}

// overrideBody accepts {"duty":191} or {"percent":75}.
type overrideBody struct {
	Duty    *float64 `json:"duty"`
	Percent *float64 `json:"percent"`
}

var channelName = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

func (s *Server) channelExists(name string) bool {
	for _, c := range s.deps.Service.Snapshot().Channels {
		if c.Name == name {
			return true
		}
	}
	return false
}

func (s *Server) putOverride(w http.ResponseWriter, r *http.Request) {
	if s.deps.Service == nil {
		writeError(w, http.StatusNotImplemented, "no service")
		return
	}
	name := r.PathValue("name")
	if !channelName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid channel name")
		return
	}
	var b overrideBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	var duty int
	switch {
	case b.Duty != nil && b.Percent != nil:
		writeError(w, http.StatusBadRequest, "give either duty or percent, not both")
		return
	case b.Duty != nil:
		if *b.Duty != float64(int(*b.Duty)) || *b.Duty < 0 || *b.Duty > 255 {
			writeError(w, http.StatusBadRequest, "duty must be an integer 0..255")
			return
		}
		duty = int(*b.Duty)
	case b.Percent != nil:
		if *b.Percent < 0 || *b.Percent > 100 {
			writeError(w, http.StatusBadRequest, "percent must be 0..100")
			return
		}
		duty = int(*b.Percent*255/100 + 0.5)
	default:
		writeError(w, http.StatusBadRequest, "duty or percent required")
		return
	}
	if !s.channelExists(name) {
		writeError(w, http.StatusNotFound, "unknown channel "+name)
		return
	}
	if err := s.deps.Service.SetOverride(name, duty); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": name, "duty": duty, "mode": control.ModeManual})
}

func (s *Server) deleteOverride(w http.ResponseWriter, r *http.Request) {
	if s.deps.Service == nil {
		writeError(w, http.StatusNotImplemented, "no service")
		return
	}
	name := r.PathValue("name")
	if !channelName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid channel name")
		return
	}
	if !s.channelExists(name) {
		writeError(w, http.StatusNotFound, "unknown channel "+name)
		return
	}
	if err := s.deps.Service.ClearOverride(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": name, "mode": control.ModeAuto})
}

var presetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func (s *Server) getPresets(w http.ResponseWriter, r *http.Request) {
	if s.deps.Presets == nil {
		writeError(w, http.StatusNotImplemented, "no preset store")
		return
	}
	list, err := s.deps.Presets.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list presets: "+err.Error())
		return
	}
	if list == nil {
		list = []Preset{}
	}
	for i := range list {
		if list[i].Channels == nil {
			list[i].Channels = []string{}
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) applyPreset(w http.ResponseWriter, r *http.Request) {
	if s.deps.Presets == nil {
		writeError(w, http.StatusNotImplemented, "no preset store")
		return
	}
	name := r.PathValue("name")
	if !presetName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid preset name")
		return
	}
	if err := s.deps.Presets.Apply(name); err != nil {
		if errors.Is(err, control.ErrRestartRequired) {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "restart_required": true, "message": err.Error()})
			return
		}
		writeError(w, http.StatusBadRequest, "apply preset: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": name})
}

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
	if err := s.deps.Presets.Save(name); err != nil {
		writeError(w, http.StatusBadRequest, "save preset: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": name})
}

func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	if s.deps.Log == nil {
		writeError(w, http.StatusNotImplemented, "no log source")
		return
	}
	lines := 100
	if q := r.URL.Query().Get("lines"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 5000 {
			writeError(w, http.StatusBadRequest, "lines must be 1..5000")
			return
		}
		lines = n
	}
	out, err := s.deps.Log(lines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read log: "+err.Error())
		return
	}
	if out == nil {
		out = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": out})
}

func (s *Server) getProfiles(w http.ResponseWriter, r *http.Request) {
	if s.deps.Profiles == nil {
		writeError(w, http.StatusNotImplemented, "no profile list")
		return
	}
	p := s.deps.Profiles()
	if p == nil {
		p = []ProfileInfo{}
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"name": "pvefand", "version": s.deps.Version})
}

func (s *Server) getSensors(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sensors == nil {
		writeError(w, http.StatusNotImplemented, "no sensor list")
		return
	}
	list := s.deps.Sensors()
	if list == nil {
		list = []SensorInfo{}
	}
	writeJSON(w, http.StatusOK, list)
}

// static serves the embedded UI. Only the three known files exist; everything
// else is 404 (API paths get a JSON 404).
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "unknown endpoint")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	var ctype string
	switch name {
	case "index.html":
		ctype = "text/html; charset=utf-8"
	case "app.js":
		ctype = "text/javascript; charset=utf-8"
	case "app.css":
		ctype = "text/css; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		http.Error(w, "missing embedded asset", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	if name == "index.html" {
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

// ---- helpers -------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
