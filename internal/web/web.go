// Package web serves the HTTP API (DESIGN.md "HTTP API") and the embedded
// static UI. The same mux is served on TCP (with Host check, CSRF header and
// optional basic auth) and on the unix socket (no checks, see SocketHandler).
package web

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/ipc"
	"github.com/SirRenix/n5-fangov/internal/tlscert"
)

//go:embed static/index.html static/app.js static/app.css
var staticFS embed.FS

// CSRFHeader must be present (value "1") on every state-changing request over TCP.
const CSRFHeader = "X-N5-Fangov-Csrf"

// RedactedHash replaces password_hash in GET /api/config; a PUT carrying it
// keeps the hash from the current file.
const RedactedHash = "<unchanged>"

// Body limits: config TOML, override JSON, settings bundle JSON.
const (
	maxBody         = 256 << 10
	maxOverrideBody = 4096
	maxImportBody   = 1 << 20
)

// HSTS is sent on every response that arrived over TLS (ServeTLS).
const hstsValue = "max-age=31536000"

// exportLines is how many lines a journal-only log source exports.
const exportLines = 5000

// ConfigStore is the daemon's config file access.
type ConfigStore interface {
	// Raw returns the current config file text.
	Raw() ([]byte, error)
	// Save writes a new config file text. Validation happens before Save
	// (Deps.Validate); Save may still refuse.
	Save(raw []byte) error
}

// ParsedConfigStore is optionally implemented by a ConfigStore; when present,
// GET /api/config also carries the parsed config under "config".
type ParsedConfigStore interface {
	Parsed() (any, error)
}

// Preset is one entry of /etc/n5-fangov/presets.
type Preset struct {
	Name        string   `json:"name"`
	Channels    []string `json:"channels"` // channel names contained in the preset
	Builtin     bool     `json:"builtin"`
	Description string   `json:"description,omitempty"`
}

// ErrPresetBuiltin is returned by PresetStore.Save/Delete for a built-in preset (409).
var ErrPresetBuiltin = errors.New("built-in preset")

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
//
// Deprecated: pass a LogStore in Deps.Log. A LogSource is still accepted for
// one release (v0.2) and adapted: /api/log answers with source "journal",
// /api/log/export streams the newest exportLines lines, DELETE /api/log
// answers 501.
type LogSource func(lines int) ([]string, error)

// LogStore is the daemon's log file (implemented by internal/logfile).
type LogStore interface {
	// Lines returns the newest n lines (from the journal when the file is disabled).
	Lines(n int) ([]string, error)
	// Export streams the whole current file to w.
	Export(w io.Writer) error
	// Clear truncates the current file; rotated files and the journal stay.
	// errors.ErrUnsupported → 501.
	Clear() error
	// Path is the log file path, "" when the file is disabled (journal only).
	Path() string
}

// Bundle exports and imports the settings bundle (config + presets) as JSON:
// {"format":1,"version":..,"exported":ts,"config":rawTOML,"presets":{name:rawTOML}}.
type Bundle interface {
	Export() ([]byte, error)
	// Import validates every part before writing anything; password_hash
	// "<unchanged>" keeps the current hash. restartRequired → 202.
	Import(b []byte) (restartRequired bool, err error)
}

// funcLogStore adapts a LogSource to LogStore (journal only).
type funcLogStore struct{ f LogSource }

func (s funcLogStore) Lines(n int) ([]string, error) { return s.f(n) }
func (s funcLogStore) Export(w io.Writer) error {
	lines, err := s.f(exportLines)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := io.WriteString(w, l+"\n"); err != nil {
			return err
		}
	}
	return nil
}
func (s funcLogStore) Clear() error {
	return fmt.Errorf("journal-only log source: %w", errors.ErrUnsupported)
}
func (s funcLogStore) Path() string { return "" }

// logStoreOf accepts the two supported Deps.Log forms; nil for anything else.
func logStoreOf(v any) LogStore {
	switch l := v.(type) {
	case nil:
		return nil
	case LogStore:
		return l
	case LogSource:
		return funcLogStore{l}
	case func(int) ([]string, error):
		return funcLogStore{l}
	}
	return nil
}

// ProfileInfo describes one hardware profile for /api/profiles.
type ProfileInfo struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Verified bool   `json:"verified"`
	Notes    string `json:"notes"`
	Active   bool   `json:"active"`
}

// SensorInfo is one selectable sensor source for the curve editor. Temp is
// the current reading in degrees C when the source is readable right now.
type SensorInfo struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Temp        *float64 `json:"temp,omitempty"`
}

// AuthConfig mirrors the [web] auth settings.
type AuthConfig struct {
	Mode         string // "none" | "basic"
	User         string
	PasswordHash string // pbkdf2$... (PasswordHash) or legacy sha256 hex (LegacyPasswordHash)
}

// Deps wires the handler to the rest of the daemon. Nil optional members
// (Config, Presets, Log, Bundle, Profiles, Sensors, TLSMgr, Account,
// Alerts, Dashboard) make the corresponding endpoints answer 501.
type Deps struct {
	Service control.Service
	Config  ConfigStore
	// Validate parses raw config text before it is saved. err refuses the
	// PUT (400); warnings are returned to the client but do not block.
	Validate func(raw []byte) (warnings []string, err error)
	Presets  PresetStore
	// Log is the log backend: a LogStore (file: lines, export, clear) or —
	// accepted for one more release — a LogSource / func(int) ([]string,
	// error) (journal only, see LogSource). Any other value counts as nil
	// and is logged once at New.
	Log any
	// Bundle backs GET /api/config/export and POST /api/config/import.
	Bundle   Bundle
	Profiles func() []ProfileInfo
	Version  string
	Auth     AuthConfig
	Sensors  func() []SensorInfo
	// AllowedHosts are Host header values (host or host:port) accepted on
	// TCP besides IP literals and "localhost"; "*" disables the check.
	AllowedHosts []string
	// TLS is informational: true when the TCP listener is TLS-terminated,
	// exposed as "tls" in GET /api/version for the UI indicator. ServeTLS
	// sets it itself; a TLS reverse proxy in front of plain Serve may set it.
	// It also marks the session cookie Secure.
	TLS bool
	// BehindTLSProxy ([web] behind_tls_proxy): the plain-HTTP listener is
	// only reached through a TLS-terminating reverse proxy, so the session
	// cookie is marked Secure although r.TLS is nil.
	BehindTLSProxy bool
	// TLSMgr backs the /api/tls endpoints (certificate panel). nil → 501.
	TLSMgr TLSMgr
	// TLSHosts are the addresses the certificate should cover (listen host,
	// host name, allowed_hosts); reported by GET /api/tls.
	TLSHosts []string
	// Logf receives auth failures and startup warnings; nil → log.Printf.
	Logf func(format string, args ...any)

	// v0.3.0-beta members (DESIGN.md "v0.3.0-beta contract").
	// SessionFile mirrors the cookie sessions; "" = memory only.
	SessionFile string
	// Account backs /api/account (nil → 501; credentials then stay Deps.Auth).
	Account AccountStore
	// Alerts backs /api/alerts (nil → 501).
	Alerts AlertMgr
	// Dashboard backs /api/dashboard (nil → 501).
	Dashboard DashboardStore
	// About is served verbatim by GET /api/about.
	About About
	// System returns the hardware inventory for GET /api/system (the
	// sysinfo.Info of the cmd collector; typed any to keep the package
	// free of that import). nil → 501.
	System func() any
}

// Server holds the mux and serves it on TCP and on the unix socket.
type Server struct {
	deps    Deps
	logs    LogStore
	mux     *http.ServeMux
	tcp     http.Handler
	socket  http.Handler
	allowed map[string]bool
	anyHost bool
	limiter *authLimiter
	logf    func(string, ...any)
	// auth is the credential set in effect: Deps.Auth at start, replaced by
	// every successful AccountStore.Update. Read per request without a lock.
	auth     atomic.Pointer[AuthConfig]
	sessions SessionStore
	// tlsNoise summarises rejected TLS handshakes (browsers without the CA).
	tlsNoise *handshakeFilter
}

// New builds a Server from deps.
func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux(), allowed: map[string]bool{}, limiter: newAuthLimiter()}
	s.logf = deps.Logf
	if s.logf == nil {
		s.logf = log.Printf
	}
	s.limiter.logf = s.logf
	auth := deps.Auth
	s.auth.Store(&auth)
	// R-M1: the mirror file is only loaded when it was written under the
	// credentials in effect now; a rotation outside the daemon drops it.
	s.sessions = NewSessionStoreEpoch(deps.SessionFile, CredentialEpoch(auth.User, auth.PasswordHash), s.logf)
	s.tlsNoise = newHandshakeFilter(s.logf, time.Now)
	s.logs = logStoreOf(deps.Log)
	if s.logs == nil && deps.Log != nil {
		s.logf("web: Deps.Log has unsupported type %T; log endpoints answer 501", deps.Log)
	}
	for _, h := range deps.AllowedHosts {
		h = normalizeHost(h)
		switch h {
		case "":
		case "*":
			s.anyHost = true
		default:
			s.allowed[h] = true
		}
	}
	s.routes()
	// R-L7: net/http 1.17–1.24 logged one ErrorLog line per request whose
	// query carried a ';' unless the handler opted in — an unauthenticated
	// log-flood vector. Go 1.25 dropped that line; the wrapper stays so the
	// behaviour is explicit whatever toolchain builds this (';' → '&').
	s.socket = http.AllowQuerySemicolons(withCaller(s.mux, Caller{Authenticated: true, Via: "socket"}))
	s.tcp = http.AllowQuerySemicolons(s.guard(s.mux))
	return s
}

// NewHandler returns the TCP handler (Host check, CSRF, auth enforced).
func NewHandler(deps Deps) http.Handler { return New(deps).Handler() }

// Handler is the TCP handler: Host header validated on every request, CSRF
// header on state-changing methods, and (when Auth.Mode == "basic") a
// signed-in caller (session cookie or basic auth) on everything that is not
// public (see publicPath).
func (s *Server) Handler() http.Handler { return s.tcp }

// SocketHandler is the same mux without Host/CSRF/auth checks, for the unix
// socket; every request there counts as signed in (via "socket").
func (s *Server) SocketHandler() http.Handler { return s.socket }

// authCfg is the credential set currently in effect.
func (s *Server) authCfg() AuthConfig { return *s.auth.Load() }

// basicMode reports whether [web] auth = "basic" (case-insensitive).
func (s *Server) basicMode() bool { return strings.EqualFold(s.authCfg().Mode, "basic") }

// authMode is the value reported as "auth"/"mode": "basic" or "none".
func (s *Server) authMode() string {
	if s.basicMode() {
		return "basic"
	}
	return "none"
}

// ListenAndServe serves the TCP handler on tcpAddr until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context, tcpAddr string) error {
	ln, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("web: listen %s: %w", tcpAddr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve serves the TCP handler on ln until ctx is done. A listener that is
// reachable from the network without basic auth is logged loudly (H3): the
// API can then change fan duties for anyone on the LAN.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if !s.basicMode() && !listenerIsLoopback(ln) {
		s.logf("WARNING: web listening on non-loopback %s without auth; anyone reaching this port can change fan duties. Set [web].auth = \"basic\" or bind to 127.0.0.1 behind a TLS reverse proxy.", ln.Addr())
	}
	return s.serve(ctx, ln)
}

// ServeTLS serves the TCP handler on ln with TLS terminated by cert (the
// automatic pair from internal/tlscert or a configured file pair). It is
// ServeTLSStore with a store that is never swapped.
func (s *Server) ServeTLS(ctx context.Context, ln net.Listener, cert tls.Certificate) error {
	return s.ServeTLSStore(ctx, ln, tlscert.NewStore(cert))
}

// ServeTLSStore serves the TCP handler on ln with TLS terminated by the
// certificate currently in store, with tlscert.ServerConfig (TLS 1.2
// minimum, AEAD suites, HTTP/2, no session tickets) — the same config
// tlscert.ValidatePair handshakes against, so an accepted pair is one this
// listener can serve (M1). Every response carries
// Strict-Transport-Security. Sets Deps.TLS for /api/version. The store is
// consulted per handshake (tls.Config.GetCertificate), so a Store.Set from
// the certificate manager takes effect for the next connection without
// touching established ones or the listener.
func (s *Server) ServeTLSStore(ctx context.Context, ln net.Listener, store *tlscert.Store) error {
	cfg := tlscert.ServerConfig(store.Get)
	s.deps.TLS = true // before serving: handlers read it without a lock
	if !s.basicMode() && !listenerIsLoopback(ln) {
		s.logf("WARNING: web listening on non-loopback %s with TLS but without auth; anyone reaching this port can change fan duties. Set [web].auth = \"basic\".", ln.Addr())
	}
	return s.serve(ctx, tls.NewListener(ln, cfg))
}

// serve runs the http.Server on ln until ctx is done. The server's own
// error log goes through handshakeFilter: rejected TLS handshakes (a
// browser that has not imported the certificate yet) are counted and
// summarised instead of logged one by one.
func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.tcp,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(s.tlsNoise, "", 0),
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

// listenerIsLoopback reports whether ln is bound to a loopback address.
// Unknown address types count as non-loopback.
func listenerIsLoopback(ln net.Listener) bool {
	ta, ok := ln.Addr().(*net.TCPAddr)
	if !ok || ta.IP == nil {
		return false
	}
	return ta.IP.IsLoopback()
}

// ServeSocket serves the unauthenticated SocketHandler on the unix socket at
// socketPath (see internal/ipc) until ctx is done.
func (s *Server) ServeSocket(ctx context.Context, socketPath string) error {
	return ipc.Serve(ctx, socketPath, s.socket)
}

// PasswordHash computes the stored form of a password (M3): salted
// PBKDF2-HMAC-SHA256, config.PBKDF2Iter iterations, rendered as
// pbkdf2$<iter>$<salt hex>$<key hex>. setup and passwd write this form.
func PasswordHash(user, password string) string {
	salt := make([]byte, config.PBKDF2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic("web: crypto/rand: " + err.Error()) // no entropy: nothing sensible to store
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, config.PBKDF2Iter, config.PBKDF2KeyLen)
	if err != nil {
		panic("web: pbkdf2: " + err.Error()) // only for out-of-range parameters
	}
	return fmt.Sprintf("%s$%d$%s$%s", config.PBKDF2Prefix, config.PBKDF2Iter, hex.EncodeToString(salt), hex.EncodeToString(key))
}

// LegacyPasswordHash is the pre-M3 stored form: sha256 hex of
// "user:password". Still accepted by VerifyPassword; no longer written.
func LegacyPasswordHash(user, password string) string {
	sum := sha256.Sum256([]byte(user + ":" + password))
	return hex.EncodeToString(sum[:])
}

// VerifyPassword checks user/password against a stored hash in either
// form with constant-time comparisons. A stored value that does not parse
// never verifies (fail closed).
func VerifyPassword(user, password, stored string) bool {
	ph, err := config.ParsePasswordHash(stored)
	if err != nil {
		return false
	}
	if ph.Legacy != nil {
		got := sha256.Sum256([]byte(user + ":" + password))
		return subtle.ConstantTimeCompare(got[:], ph.Legacy) == 1
	}
	key, err := pbkdf2.Key(sha256.New, password, ph.Salt, ph.Iter, len(ph.Key))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(key, ph.Key) == 1
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
	m.HandleFunc("DELETE /api/presets/{name}", s.deletePreset)
	m.HandleFunc("GET /api/presets/{name}", s.getPreset)
	m.HandleFunc("POST /api/presets/{name}/rename", s.renamePreset)
	m.HandleFunc("GET /api/log", s.getLog)
	m.HandleFunc("GET /api/log/export", s.exportLog)
	m.HandleFunc("DELETE /api/log", s.clearLog)
	m.HandleFunc("GET /api/config/export", s.exportConfig)
	m.HandleFunc("POST /api/config/import", s.importConfig)
	m.HandleFunc("GET /api/profiles", s.getProfiles)
	m.HandleFunc("GET /api/version", s.getVersion)
	m.HandleFunc("GET /api/sensors", s.getSensors)
	m.HandleFunc("GET /api/tls", s.getTLS)
	m.HandleFunc("GET /api/tls/cert.crt", s.tlsCertPEM)
	m.HandleFunc("GET /api/tls/cert.cer", s.tlsCertDER)
	m.HandleFunc("POST /api/tls/regenerate", s.tlsRegenerate)
	m.HandleFunc("POST /api/tls/upload", s.tlsUpload)
	m.HandleFunc("POST /api/tls/reset", s.tlsReset)
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/logout", s.logout)
	m.HandleFunc("GET /api/session", s.getSession)
	m.HandleFunc("GET /api/account", s.getAccount)
	m.HandleFunc("POST /api/account/password", s.accountPassword)
	m.HandleFunc("POST /api/account/user", s.accountUser)
	m.HandleFunc("POST /api/account/sessions/revoke", s.accountRevoke)
	m.HandleFunc("GET /api/alerts", s.getAlerts)
	m.HandleFunc("PUT /api/alerts", s.putAlerts)
	m.HandleFunc("POST /api/alerts/test", s.alertsTest)
	m.HandleFunc("POST /api/alerts/template", s.alertsTemplate)
	m.HandleFunc("GET /api/dashboard", s.getDashboard)
	m.HandleFunc("PUT /api/dashboard", s.putDashboard)
	m.HandleFunc("GET /api/about", s.getAbout)
	m.HandleFunc("GET /api/system", s.getSystem)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "unknown endpoint")
	})
	m.HandleFunc("/", s.static)
}

// Caller is the resolved identity of a request (CallerFrom). Via is
// "cookie" (session), "basic" (Authorization header), "socket" (unix
// socket) or "none" (anonymous, or auth = none).
type Caller struct {
	Authenticated bool
	User          string
	Via           string
	// token is the cookie token of a session caller (kept out of the
	// JSON world; used as the keep argument of RevokeAll).
	token   string
	session Session
}

type callerKey struct{}

// CallerFrom returns the caller resolved by guard (or the socket handler);
// the zero Caller for a request that went through neither.
func CallerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

// withCaller stores a fixed caller in every request's context.
func withCaller(next http.Handler, c Caller) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
	})
}

// publicPath lists what an anonymous caller may reach with auth = basic
// (DESIGN.md "Visibility model"): the static UI, the version/about/session
// cards, login/logout and the two filtered reads (state, history — the
// handlers reduce them for anonymous callers). Everything else under /api/
// is protected.
func publicPath(path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return true
	}
	switch path {
	case "/api/version", "/api/about", "/api/session", "/api/login", "/api/logout", "/api/state", "/api/history":
		return true
	}
	return false
}

// guard enforces, in this order: Host header (DNS rebinding, M1), CSRF
// header on state-changing methods, then resolves the caller once (cookie
// session, else basic auth) and refuses anonymous access to protected
// paths. Over TLS every answer carries HSTS.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", hstsValue)
		}
		if !s.hostAllowed(r.Host) {
			writeError(w, http.StatusMisdirectedRequest, "host header not allowed; use the IP address, localhost or a configured allowed_hosts entry")
			return
		}
		write := true
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			write = false
		}
		if write && r.Header.Get(CSRFHeader) != "1" {
			writeError(w, http.StatusForbidden, "missing "+CSRFHeader+" header")
			return
		}
		c, ok := s.resolveCaller(w, r)
		if !ok {
			return
		}
		if !c.Authenticated && !publicPath(r.URL.Path) {
			// Anonymous: a silent 401 (no log line, no rate-limit count)
			// that makes the UI show its login form. No WWW-Authenticate
			// challenge on purpose: the UI has its own form.
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
	})
}

// resolveCaller identifies the request: with auth = none everyone is signed
// in; else the session cookie wins, then a presented basic credential. A
// presented credential that does not verify is a failure (counted, logged,
// answered 401 whatever the path); ok=false means the answer was written.
func (s *Server) resolveCaller(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	if !s.basicMode() {
		return Caller{Authenticated: true, Via: "none"}, true
	}
	if ck, err := r.Cookie(sessionCookie); err == nil && ck.Value != "" {
		if sess, ok := s.sessions.Lookup(ck.Value); ok {
			return Caller{Authenticated: true, User: sess.User, Via: "cookie", token: ck.Value, session: sess}, true
		}
	}
	if r.Header.Get("Authorization") == "" {
		return Caller{Via: "none"}, true
	}
	ip := remoteIP(r)
	// A bucket that already has limitConcurrent failed attempts sleeping
	// gets an immediate 429 before any hash is computed (M3c): the delay
	// cannot be side-stepped with parallel requests, and the PBKDF2 cost
	// is not paid for them. The same answer when the process-wide
	// verification slots are all taken.
	if s.limiter.busy(ip) || !s.limiter.acquire() {
		writeError(w, http.StatusTooManyRequests, "too many concurrent authentication attempts")
		return Caller{}, false
	}
	ok := s.authorized(r)
	s.limiter.release()
	user, pass, _ := r.BasicAuth()
	if !ok {
		n, delay := s.limiter.fail(ip)
		s.logf("web: auth failure from %s (user %.64q, %s %s, %d recent failures, delay %s)", ip, user, r.Method, r.URL.Path, n, delay)
		writeError(w, http.StatusUnauthorized, "authentication required")
		return Caller{}, false
	}
	s.limiter.reset(ip)
	s.upgradeLegacyHash(user, pass)
	return Caller{Authenticated: true, User: user, Via: "basic"}, true
}

// upgradeLegacyHash rewrites a stored legacy sha256("user:password") hash
// as PBKDF2 after a successful verification — the only moment the
// password is at hand. Needs an AccountStore; without one the legacy form
// simply stays. The sessions are kept: the epoch follows the new hash so
// the mirror file is still loaded after a restart.
func (s *Server) upgradeLegacyHash(user, password string) {
	if s.deps.Account == nil {
		return
	}
	cfg := s.authCfg()
	ph, err := config.ParsePasswordHash(cfg.PasswordHash)
	if err != nil || ph.Legacy == nil {
		return
	}
	next, err := s.deps.Account.Update("", PasswordHash(user, password))
	if err != nil {
		s.logf("web: legacy password hash not upgraded: %v", err)
		return
	}
	s.auth.Store(&next)
	s.sessions.SetEpoch(CredentialEpoch(next.User, next.PasswordHash))
	s.logf("web: legacy password hash upgraded to pbkdf2 for user %.64q", user)
}

// hostAllowed accepts IP literals, localhost and the configured hosts (M1).
func (s *Server) hostAllowed(host string) bool {
	if s.anyHost {
		return true
	}
	h := normalizeHost(host)
	if h == "" {
		return false
	}
	if h == "localhost" || net.ParseIP(h) != nil {
		return true
	}
	return s.allowed[h]
}

// normalizeHost strips a port and IPv6 brackets and lowercases.
func normalizeHost(host string) string {
	h := strings.TrimSpace(host)
	if hp, _, err := net.SplitHostPort(h); err == nil {
		h = hp
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

func remoteIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// authorized checks basic auth against the configured user and hash
// (either stored form, see VerifyPassword). Both comparisons run
// regardless of the other's outcome.
func (s *Server) authorized(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	return s.credentialsOK(user, pass)
}

// credentialsOK checks user/password against the credential set in effect.
func (s *Server) credentialsOK(user, pass string) bool {
	cfg := s.authCfg()
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(cfg.User))
	hashOK := 0
	if VerifyPassword(user, pass, cfg.PasswordHash) {
		hashOK = 1
	}
	return userOK&hashOK == 1
}

// ---- handlers ------------------------------------------------------------

// publicState is the reduced GET /api/state for anonymous callers: no
// hwmon path, extra temperatures, alert times or watched sensors.
type publicState struct {
	TS       int64           `json:"ts"`
	Status   string          `json:"status"`
	Profile  string          `json:"profile"`
	Verified bool            `json:"verified"`
	DryRun   bool            `json:"dry_run"`
	Channels []publicChannel `json:"channels"`
	Uptime   int64           `json:"uptime_s"`
}

type publicChannel struct {
	Name   string       `json:"name"`
	PWM    int          `json:"pwm"`
	Sensor string       `json:"sensor"`
	Temp   float64      `json:"temp"`
	Duty   int          `json:"duty"`
	Target int          `json:"target"`
	RPM    int          `json:"rpm"`
	Mode   control.Mode `json:"mode"`
}

// publicPoint is a history point without the extra sensors.
type publicPoint struct {
	TS   int64              `json:"ts"`
	Temp map[string]float64 `json:"temp"`
	Duty map[string]int     `json:"duty"`
	RPM  map[string]int     `json:"rpm"`
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	if s.deps.Service == nil {
		writeError(w, http.StatusNotImplemented, "no service")
		return
	}
	snap := s.deps.Service.Snapshot()
	if !CallerFrom(r.Context()).Authenticated {
		out := publicState{TS: snap.TS, Status: snap.Status, Profile: snap.Profile, Verified: snap.Verified, DryRun: snap.DryRun, Uptime: snap.Uptime, Channels: make([]publicChannel, 0, len(snap.Channels))}
		for _, c := range snap.Channels {
			out.Channels = append(out.Channels, publicChannel{Name: c.Name, PWM: c.PWM, Sensor: c.Sensor, Temp: c.Temp, Duty: c.Duty, Target: c.Target, RPM: c.RPM, Mode: c.Mode})
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
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
	var since int64
	if q := r.URL.Query().Get("since"); q != "" {
		n, err := strconv.ParseInt(q, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "since must be a unix timestamp")
			return
		}
		since = n
	}
	pts := s.deps.Service.History(time.Duration(minutes) * time.Minute)
	if !CallerFrom(r.Context()).Authenticated {
		// Anonymous: the channel series only; the extra sensors stay
		// behind the sign-in (they name the operator's hardware).
		out := make([]publicPoint, 0, len(pts))
		for _, p := range pts {
			if p.TS > since {
				out = append(out, publicPoint{TS: p.TS, Temp: p.Temp, Duty: p.Duty, RPM: p.RPM})
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	out := make([]control.HistoryPoint, 0, len(pts))
	for _, p := range pts {
		if p.TS > since {
			out = append(out, p)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// hashLine matches a password_hash assignment at line start in either TOML
// quote style ("basic" or 'literal'), with any spacing, also in the dotted
// form web.password_hash. Group 2 is the quoted literal including quotes.
var hashLine = regexp.MustCompile(`(?m)^([ \t]*(?:web\.)?password_hash[ \t]*=[ \t]*)("[^"\n]*"|'[^'\n]*')`)

// minLiteralHash is the shortest hash value that is also replaced as a
// bare substring (see RedactRaw); shorter strings would hit unrelated text.
const minLiteralHash = 32

// mapHashLiterals rewrites the value of every hashLine match for which f
// returns true, keeping the quote style.
func mapHashLiterals(raw string, f func(val string) (string, bool)) string {
	return hashLine.ReplaceAllStringFunc(raw, func(m string) string {
		sub := hashLine.FindStringSubmatch(m)
		lit := sub[2]
		nv, ok := f(lit[1 : len(lit)-1])
		if !ok {
			return m
		}
		return sub[1] + lit[:1] + nv + lit[:1]
	})
}

// RedactRaw replaces a non-empty password_hash value in TOML text (H1).
// The line forms are rewritten by regex; in addition the hash the parser
// actually sees is replaced wherever it appears (inline table, unusual
// layout), so the value never leaves the daemon however the file is laid
// out.
func RedactRaw(raw string) string {
	out := mapHashLiterals(raw, func(v string) (string, bool) {
		return RedactedHash, v != "" && v != RedactedHash
	})
	if h := parsedHash(raw); len(h) >= minLiteralHash {
		out = strings.ReplaceAll(out, h, RedactedHash)
	}
	return out
}

// parsedHash returns web.password_hash as the config parser reads it, or
// "" when the text does not parse or has none.
func parsedHash(raw string) string {
	cfg, _, err := config.Parse([]byte(raw))
	if err != nil {
		return ""
	}
	return cfg.Web.PasswordHash
}

// currentHash extracts password_hash from TOML text ("" when absent): the
// parsed value when the text parses, else the first line-form literal.
func currentHash(raw string) string {
	if h := parsedHash(raw); h != "" {
		return h
	}
	if sub := hashLine.FindStringSubmatch(raw); sub != nil {
		return sub[2][1 : len(sub[2])-1]
	}
	return ""
}

// redactParsed blanks web.password_hash in the parsed object when it is the
// map layout produced by the daemon's config store.
func redactParsed(cfg any) {
	m, ok := cfg.(map[string]any)
	if !ok {
		return
	}
	web, ok := m["web"].(map[string]any)
	if !ok {
		return
	}
	if h, ok := web["password_hash"].(string); ok && h != "" {
		web["password_hash"] = RedactedHash
	}
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
	out := map[string]any{"raw": RedactRaw(string(raw))}
	if p, ok := s.deps.Config.(ParsedConfigStore); ok {
		if cfg, err := p.Parsed(); err == nil {
			redactParsed(cfg)
			out["config"] = cfg
		} else {
			out["parse_error"] = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// putConfig: read (413 on overflow) → restore a redacted hash → validate
// (400, nothing written) → save → reload. Order matters (M2): a syntax
// error never reaches the file.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil || s.deps.Service == nil {
		writeError(w, http.StatusNotImplemented, "no config store")
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
	if len(strings.TrimSpace(string(body))) == 0 {
		writeError(w, http.StatusBadRequest, "empty config")
		return
	}
	if strings.Contains(string(body), RedactedHash) {
		cur, err := s.deps.Config.Raw()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read current config: "+err.Error())
			return
		}
		hash := currentHash(string(cur))
		body = []byte(mapHashLiterals(string(body), func(v string) (string, bool) {
			return hash, v == RedactedHash
		}))
	}
	var warnings []string
	if s.deps.Validate != nil {
		warns, err := s.deps.Validate(body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "config rejected: " + err.Error(), "errors": nonNil(warns)})
			return
		}
		warnings = warns
	}
	if err := s.deps.Config.Save(body); err != nil {
		writeError(w, http.StatusBadRequest, "config rejected: "+err.Error())
		return
	}
	err = s.deps.Service.Reload(body)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_required": false, "warnings": nonNil(warnings)})
	case errors.Is(err, control.ErrRestartRequired):
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "restart_required": true, "message": err.Error(), "warnings": nonNil(warnings)})
	default:
		writeError(w, http.StatusInternalServerError, "config saved, reload failed: "+err.Error())
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// isTooLarge reports whether err comes from http.MaxBytesReader (L3).
func isTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// overrideBody accepts {"duty":191} or {"percent":75}.
type overrideBody struct {
	Duty    *float64 `json:"duty"`
	Percent *float64 `json:"percent"`
}

var channelName = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// channel returns the channel state from a fresh snapshot, or nil.
func (s *Server) channel(name string) *control.ChannelState {
	for _, c := range s.deps.Service.Snapshot().Channels {
		if c.Name == name {
			return &c
		}
	}
	return nil
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
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOverrideBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		if isTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", maxOverrideBody))
			return
		}
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
	if s.channel(name) == nil {
		writeError(w, http.StatusNotFound, "unknown channel "+name)
		return
	}
	if err := s.deps.Service.SetOverride(name, duty); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The override is applied by the loop on its next cycle; mode reports
	// what the channel is doing now (L4: critical/stall are not "manual").
	mode := control.ModeManual
	if c := s.channel(name); c != nil {
		mode = c.Mode
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": name, "duty": duty, "mode": mode})
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
	if s.channel(name) == nil {
		writeError(w, http.StatusNotFound, "unknown channel "+name)
		return
	}
	if err := s.deps.Service.ClearOverride(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := control.ModeAuto
	if c := s.channel(name); c != nil {
		mode = c.Mode
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": name, "mode": mode})
}

// presetName mirrors config's preset rule ([a-z0-9_-], at most 64) (L1).
var presetName = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

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
		switch {
		case errors.Is(err, control.ErrRestartRequired):
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "restart_required": true, "message": err.Error()})
		case errors.Is(err, fs.ErrNotExist):
			// R-L8: no such preset for this profile (a built-in of another
			// profile counts as missing).
			writeError(w, http.StatusNotFound, "unknown preset "+name)
		default:
			writeError(w, http.StatusBadRequest, "apply preset: "+err.Error())
		}
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
		if errors.Is(err, ErrPresetBuiltin) {
			// R-U8: the contract says 409 for a built-in name, like delete.
			writeError(w, http.StatusConflict, "save preset: "+err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, "save preset: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": name})
}

// logSource names where the lines come from: "file" or "journal".
func (s *Server) logSource() string {
	if s.logs.Path() == "" {
		return "journal"
	}
	return "file"
}

func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
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
	out, err := s.logs.Lines(lines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read log: "+err.Error())
		return
	}
	if out == nil {
		out = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": out, "source": s.logSource()})
}

// exportLog streams the whole log file as a text attachment
// n5-fangov-<host>-<ts>.log (a journal-only source: newest exportLines lines).
func (s *Server) exportLog(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeError(w, http.StatusNotImplemented, "no log source")
		return
	}
	name := fmt.Sprintf("n5-fangov-%s-%s.log", hostLabel(), time.Now().UTC().Format("20060102-150405"))
	attachment(w, "text/plain; charset=utf-8", name)
	w.WriteHeader(http.StatusOK)
	if err := s.logs.Export(w); err != nil {
		// Headers are out; the client sees a truncated file. Say so in the log.
		s.logf("web: log export: %v", err)
	}
}

// clearLog truncates the current log file. Rotated files and the journal
// stay; a journal-only source answers 501.
func (s *Server) clearLog(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeError(w, http.StatusNotImplemented, "no log store")
		return
	}
	if err := s.logs.Clear(); err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			writeError(w, http.StatusNotImplemented, "log clear not supported: "+err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "clear log: "+err.Error())
		return
	}
	// The truncation itself erased the trail; this line is the first entry
	// of the new file and lands in the journal as well (L2).
	s.logf("web: log cleared by %s", remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true, "note": "journal untouched"})
}

// exportConfig answers the settings bundle as a JSON attachment
// n5-fangov-settings-<ts>.json with password_hash redacted inside "config".
func (s *Server) exportConfig(w http.ResponseWriter, r *http.Request) {
	if s.deps.Bundle == nil {
		writeError(w, http.StatusNotImplemented, "no settings bundle")
		return
	}
	raw, err := s.deps.Bundle.Export()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export settings: "+err.Error())
		return
	}
	out, err := redactBundle(raw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export settings: "+err.Error())
		return
	}
	name := "n5-fangov-settings-" + time.Now().UTC().Format("20060102-150405") + ".json"
	attachment(w, "application/json; charset=utf-8", name)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// redactBundle rewrites the "config" string of a bundle document with
// RedactRaw; every other member passes through untouched.
func redactBundle(raw []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("bundle is not a JSON object: %w", err)
	}
	if c, ok := doc["config"]; ok {
		var cfg string
		if err := json.Unmarshal(c, &cfg); err != nil {
			return nil, fmt.Errorf("bundle config is not a string: %w", err)
		}
		b, err := json.Marshal(RedactRaw(cfg))
		if err != nil {
			return nil, err
		}
		doc["config"] = b
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// importConfig: read (413 over maxImportBody) → must be a JSON object (400)
// → Bundle.Import (400 with the error lines under "errors") → 200 or 202.
func (s *Server) importConfig(w http.ResponseWriter, r *http.Request) {
	if s.deps.Bundle == nil {
		writeError(w, http.StatusNotImplemented, "no settings bundle")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBody))
	if err != nil {
		if isTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", maxImportBody))
			return
		}
		writeError(w, http.StatusBadRequest, "unreadable body: "+err.Error())
		return
	}
	trimmed := strings.TrimSpace(string(body))
	if !strings.HasPrefix(trimmed, "{") || !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "body must be a JSON settings bundle")
		return
	}
	restart, err := s.deps.Bundle.Import(body)
	if err != nil {
		lines := importErrorLines(err)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "import rejected: " + lines[0], "errors": lines})
		return
	}
	if restart {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "restart_required": true, "message": "restart required: systemctl restart n5-fangov"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_required": false})
}

// importErrorLines turns a Bundle.Import error into the "errors" list: an
// error carrying Errors() []string (the cmd bundle's validation list)
// contributes its items, anything else is split at newlines. Never empty.
func importErrorLines(err error) []string {
	var multi interface{ Errors() []string }
	if errors.As(err, &multi) {
		if lines := multi.Errors(); len(lines) > 0 {
			return lines
		}
	}
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return []string{"import failed"}
	}
	return lines
}

// attachment sets the download headers for an export.
func attachment(w http.ResponseWriter, ctype, filename string) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
}

// hostLabel is the short host name reduced to [A-Za-z0-9.-], "host" when unknown.
func hostLabel() string {
	hn, err := os.Hostname()
	if err != nil || hn == "" {
		return "host"
	}
	var b strings.Builder
	for _, c := range hn {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.':
			b.WriteRune(c)
		}
	}
	if b.Len() == 0 {
		return "host"
	}
	return b.String()
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
	writeJSON(w, http.StatusOK, map[string]any{"name": "n5-fangov", "version": s.deps.Version, "tls": s.deps.TLS, "prerelease": s.deps.About.Prerelease, "auth": s.authMode()})
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
