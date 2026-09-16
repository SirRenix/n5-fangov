// wiring_account.go holds the state directory (/var/lib/n5-fangov), the
// account store behind /api/account and editConfig, the read-modify-write
// every single-key store (account, alerts, dashboard) goes through.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// ---------------------------------------------------------------------------
// State directory (/var/lib/n5-fangov: sessions.json, alerts.json).

const (
	defaultStateDir  = "/var/lib/n5-fangov"
	stateDirEnv      = "N5FANGOV_STATE_DIR"
	sessionsFileName = "sessions.json"
	alertsFileName   = "alerts.json"
)

// stateDir returns the state directory: N5FANGOV_STATE_DIR, else the
// unit's $STATE_DIRECTORY (first entry), else /var/lib/n5-fangov.
func stateDir() string {
	if d := os.Getenv(stateDirEnv); d != "" {
		return d
	}
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		if i := strings.IndexByte(d, ':'); i >= 0 {
			d = d[:i]
		}
		return d
	}
	return defaultStateDir
}

// ensureStateDir creates dir (0700) and probes that a file can be written
// there. On failure it logs once and returns "": the daemon then runs
// without persistence (sessions and alert history in memory only).
func ensureStateDir(dir string) string {
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("state-dir %s: %v (sessions and alert history kept in memory only)", dir, err)
		return ""
	}
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		log.Printf("state-dir %s not writable: %v (sessions and alert history kept in memory only)", dir, err)
		return ""
	}
	probe.Close()
	_ = os.Remove(probe.Name())
	return dir
}

func sessionsPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, sessionsFileName)
}

func alertsPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, alertsFileName)
}

// readConfigRaw reads the config file; a missing file reads as empty
// text (SetKey then appends the section).
func readConfigRaw(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return raw, nil
}

// editConfig is the read-modify-write behind the single-key stores: read
// the file under configFileMu, let edit rewrite the text with
// SetKey, verify that the result parses and that the parsed values are
// the ones asked for — a layout SetKey cannot edit, above all an inline
// table `section = { … }`, is refused with a clear message instead of
// leaving the file inconsistent — then pin and save. edit sees
// the file text as read, so a store that keeps the untouched value takes
// it from there.
func editConfig(path string, pin func([]byte) []byte, section string, edit func([]byte) []byte, verify func(config.Config) bool) error {
	configFileMu.Lock()
	defer configFileMu.Unlock()
	raw, err := readConfigRaw(path)
	if err != nil {
		return err
	}
	_, _, beforeErr := config.Parse(raw)
	raw = edit(raw)
	cfg, _, err := config.Parse(raw)
	switch {
	case err != nil && beforeErr == nil:
		return fmt.Errorf("config uses an inline [%s] table or a layout the in-place editor cannot handle; edit the file by hand (%w)", section, err)
	case err != nil:
		return fmt.Errorf("config would not parse after the edit: %w", err)
	case !verify(cfg):
		return fmt.Errorf("config uses an inline [%s] table or a layout the in-place editor cannot handle; edit the file by hand", section)
	}
	if pin != nil {
		raw = pin(raw)
	}
	if err := saveConfig(path, raw); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Account store (web.AccountStore): [web] user / password_hash.

type accountStore struct {
	mu      sync.Mutex
	cfgPath string
	pin     func([]byte) []byte
	cur     web.AuthConfig
}

func newAccountStore(cfgPath string, w webSpec, pin func([]byte) []byte) *accountStore {
	return &accountStore{cfgPath: cfgPath, pin: pin,
		cur: web.AuthConfig{Mode: w.Auth, User: w.User, PasswordHash: w.PasswordHash}}
}

func (s *accountStore) Current() web.AuthConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Update rewrites [web] user and/or password_hash ("" keeps the current
// value) with config.SetKey, so comments and the other keys stay, and
// returns the credentials now in effect. The kept value is the one in the
// file at that moment, not s.cur: a PUT /api/config or an import
// may have changed [web] since the last Update; s.cur becomes what was
// written. The mode is not touched: the web layer refuses account changes
// while auth is "none".
func (s *accountStore) Update(user, passwordHash string) (web.AuthConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if user == "" && passwordHash == "" {
		return s.cur, nil
	}
	if passwordHash != "" {
		if _, err := config.ParsePasswordHash(passwordHash); err != nil {
			return s.cur, fmt.Errorf("password hash: %w", err)
		}
	}
	var next web.AuthConfig
	err := editConfig(s.cfgPath, s.pin, "web", func(raw []byte) []byte {
		next = s.cur
		if cur, _, err := config.Parse(raw); err == nil {
			next.User, next.PasswordHash = cur.Web.User, cur.Web.PasswordHash
		}
		if user != "" {
			next.User = user
		}
		if passwordHash != "" {
			next.PasswordHash = passwordHash
		}
		raw = setConfigKey(raw, "web", "user", tomlString(next.User))
		return setConfigKey(raw, "web", "password_hash", tomlString(next.PasswordHash))
	}, func(cfg config.Config) bool {
		return cfg.Web.User == next.User && cfg.Web.PasswordHash == next.PasswordHash
	})
	if err != nil {
		return s.cur, err
	}
	s.cur = next
	return s.cur, nil
}

// orMemory names a state file path or says that the data stays in memory.
func orMemory(path string) string {
	if path == "" {
		return "in memory only"
	}
	return path
}
