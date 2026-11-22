package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/SirRenix/n5-fangov/internal/cfgver"
	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// Config versions (internal/cfgver, DESIGN "Config versions"): serve opens
// the store in the state dir; every write of the config file by the daemon
// goes through saveConfigWhy (a request) or saveConfigQuiet (the
// scheduler), which store the text on disk first, named with the reason of
// the write and its author.
//
// The author of a request's write comes from the web layer: the handler
// wraps only its store call in web.Deps.ConfigActor (withActor), which
// takes actorMu for that call (not for the body read) and publishes the
// caller; the call reports back the version it stored and the hash of the
// text it wrote (the Undo of the dashboard restores exactly that version,
// and only while the file still holds that text). Every daemon write inside
// a request runs in such a call (the login's legacy-hash upgrade with the
// author "daemon"); writes outside a request (the scheduler, the
// certificate manager at start) name their author themselves and never
// read the actor.

// configVersionsDir is <state dir>/config-versions.
const configVersionsDir = "config-versions"

func configVersionsPath(state string) string {
	if state == "" {
		return ""
	}
	return filepath.Join(state, configVersionsDir)
}

// actorState is one request's config write: who, and what it stored.
type actorState struct {
	by      string
	version int
	sha     string
}

var (
	cfgVersions atomic.Pointer[cfgver.Store]
	cfgActor    atomic.Pointer[actorState]
	actorMu     sync.Mutex
)

// withActor is web.Deps.ConfigActor. Lock order: actorMu before
// configFileMu.
func withActor(by string) func() (int, string) {
	actorMu.Lock()
	st := &actorState{by: by}
	cfgActor.Store(st)
	return func() (int, string) {
		cfgActor.Store(nil)
		actorMu.Unlock()
		return st.version, st.sha
	}
}

// saveConfigWhy writes raw atomically like saveConfig and, in the daemon,
// stores the text it replaces as a version first, named after the caller
// of the request (withActor). A write that leaves the text as it is stores
// nothing. A version that cannot be stored never blocks the write.
func saveConfigWhy(path string, raw []byte, reason string) error {
	st := cfgActor.Load()
	by := ""
	if st != nil {
		by = st.by
	}
	id, err := saveVersioned(path, raw, by, reason, false)
	if st != nil && err == nil {
		st.version, st.sha = id, cfgver.Sum(raw)
	}
	return err
}

// saveConfigQuiet is the scheduler's write: no version (it recurs daily),
// except for a hand edit it would overwrite (kept under its own name).
func saveConfigQuiet(path string, raw []byte, reason string) error {
	_, err := saveVersioned(path, raw, "schedule", reason, true)
	return err
}

func saveVersioned(path string, raw []byte, by, reason string, quiet bool) (int, error) {
	v := cfgVersions.Load()
	id := 0
	if v != nil {
		if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, raw) {
			id = v.Before(path, by, reason, quiet)
		}
	}
	if err := config.Save(path, raw); err != nil {
		return 0, err
	}
	if v != nil {
		v.Wrote(raw)
	}
	return id, nil
}

// versionsOf is the web store over the versions serve opened; nil without
// a state dir.
func versionsOf(cfgPath string, pin func([]byte) []byte, reload func([]byte) error) *fileVersions {
	v := cfgVersions.Load()
	if v == nil {
		return nil
	}
	return &fileVersions{store: v, cfgPath: cfgPath, pin: pin, reload: reload}
}

// fileVersions backs web.ConfigVersions with the store and the config
// file; reload is the daemon's reload hook (hookedService).
type fileVersions struct {
	store   *cfgver.Store
	cfgPath string
	pin     func([]byte) []byte
	reload  func([]byte) error
}

func (v fileVersions) List() ([]cfgver.Version, error)            { return v.store.List() }
func (v fileVersions) Get(id int) (cfgver.Version, []byte, error) { return v.store.Get(id) }
func (v fileVersions) Next(id int) ([]byte, error)                { return v.store.Next(id) }

func (v fileVersions) Current() ([]byte, error) {
	raw, err := os.ReadFile(v.cfgPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return raw, err
}

// restartKeys lists the keys whose change takes effect at the next start
// only (DESIGN section 3 "restart"; the tls keys are the certificate
// manager's and pinned).
func restartKeys(old, next config.Config) []string {
	var out []string
	for _, k := range diffKeys(old.Web, next.Web, "tls", "cert_file", "key_file") {
		out = append(out, "web."+k)
	}
	for _, k := range diffKeys(old.Log, next.Log) {
		out = append(out, "log."+k)
	}
	if old.Daemon.Profile != next.Daemon.Profile {
		out = append(out, "daemon.profile")
	}
	return out
}

// downgrades names what a restore of next would open up after the next
// start: sign-in switched off, the listener leaving loopback, another user.
func downgrades(old, next config.Config) []string {
	var out []string
	if old.Web.Auth == "basic" && next.Web.Auth != "basic" {
		out = append(out, fmt.Sprintf("web.auth %s → %s: the dashboard needs no sign-in after a restart", old.Web.Auth, orDash(next.Web.Auth)))
	}
	if isLoopbackListen(old.Web.Listen) && !isLoopbackListen(next.Web.Listen) && next.Web.Listen != "none" && next.Web.Listen != "" {
		out = append(out, fmt.Sprintf("web.listen %s → %s: reachable from the network after a restart", old.Web.Listen, next.Web.Listen))
	}
	if old.Web.User != next.Web.User {
		out = append(out, fmt.Sprintf("web.user %s → %s after a restart", orDash(old.Web.User), orDash(next.Web.User)))
	}
	return out
}

// Impact compares a version with the current file: the restart-only keys
// a restore writes and the warnings a restore needs (web.ConfigVersions).
func (v fileVersions) Impact(raw []byte) (keys, warnings []string) {
	next, _, err := config.Parse(raw)
	if err != nil {
		return nil, []string{"does not parse: a restore is refused"}
	}
	cur, _ := v.Current()
	old, _, _ := config.Parse(cur)
	return restartKeys(old, next), downgrades(old, next)
}

// Restore writes version id as the config the way an import does:
// validated first (a text that does not parse, or one that carries the
// password placeholder, is refused, nothing written), refused with
// web.ErrChanged when ifCurrent is set and the file no longer has that
// hash (the Undo after a write someone else's write followed), the tls
// keys pinned, written under the file lock (the text before becomes a
// version, "restore <id>"), then reloaded. Accepted during full cooling
// like any change (it applies after it ends).
func (v fileVersions) Restore(id int, ifCurrent string) (web.RestoreResult, error) {
	var res web.RestoreResult
	_, raw, err := v.store.Get(id)
	if err != nil {
		return res, err
	}
	configFileMu.Lock()
	locked := true
	unlock := func() {
		if locked {
			locked = false
			configFileMu.Unlock()
		}
	}
	defer unlock()
	cur, _ := v.Current()
	if ifCurrent != "" && cfgver.Sum(cur) != ifCurrent {
		return res, fmt.Errorf("version %d not restored: %w", id, web.ErrChanged)
	}
	next, warns, err := config.Parse(raw)
	if err != nil {
		return res, fmt.Errorf("version %d does not parse, nothing written: %v", id, err)
	}
	if webOf(next).PasswordHash == redactedHash {
		return res, fmt.Errorf("version %d carries the password placeholder %s, nothing written", id, redactedHash)
	}
	old, _, _ := config.Parse(cur)
	res.RestartKeys = restartKeys(old, next)
	res.Warnings = warningStrings(warns)
	if v.pin != nil {
		raw = v.pin(raw)
	}
	if err := saveConfigWhy(v.cfgPath, raw, fmt.Sprintf("restore %d", id)); err != nil {
		return res, fmt.Errorf("write %s: %w", v.cfgPath, err)
	}
	unlock()
	if st := cfgActor.Load(); st != nil {
		res.Version, res.SHA = st.version, st.sha
	}
	if v.reload == nil {
		return res, nil
	}
	switch err := v.reload(raw); {
	case err == nil:
	case isRestartRequired(err):
		res.RestartRequired = true
	default:
		return res, &web.ReloadError{Err: fmt.Errorf("config written, not applied: %w", err)}
	}
	return res, nil
}
