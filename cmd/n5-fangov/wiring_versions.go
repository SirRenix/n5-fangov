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
// goes through saveConfigWhy, which stores the text on disk first, named
// with the reason of the write and the caller of the request that made it.
//
// The caller comes from the web layer: every route that writes the config
// runs inside withActor (web.Deps.ConfigActor), which holds actorMu for the
// request and publishes the caller for saveConfigWhy. Writes outside a
// request (the scheduler, which records no version, and the certificate
// manager at start) see no caller.

// configVersionsDir is <state dir>/config-versions.
const configVersionsDir = "config-versions"

func configVersionsPath(state string) string {
	if state == "" {
		return ""
	}
	return filepath.Join(state, configVersionsDir)
}

var (
	cfgVersions atomic.Pointer[cfgver.Store]
	cfgActor    atomic.Pointer[string]
	actorMu     sync.Mutex
)

// withActor is web.Deps.ConfigActor: one config-writing request at a time
// names its caller. Lock order: actorMu before configFileMu.
func withActor(by string) func() {
	actorMu.Lock()
	cfgActor.Store(&by)
	return func() {
		cfgActor.Store(nil)
		actorMu.Unlock()
	}
}

func actorNow() string {
	if p := cfgActor.Load(); p != nil {
		return *p
	}
	return ""
}

// saveConfigWhy writes raw atomically like saveConfig and, in the daemon,
// stores the text it replaces as a version first. reason "" stores none
// (the scheduler) but the write is still known (no "external edit" later).
// A version that cannot be stored never blocks the write.
func saveConfigWhy(path string, raw []byte, reason string) error {
	v := cfgVersions.Load()
	if v != nil {
		// a write that leaves the text as it is stores nothing
		if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, raw) {
			v.Before(path, actorNow(), reason)
		}
	}
	if err := config.Save(path, raw); err != nil {
		return err
	}
	if v != nil {
		v.Wrote(raw)
	}
	return nil
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

// Restore writes version id as the config the way an import does:
// validated first (a text that does not parse is refused, nothing
// written), the tls keys pinned, written under the file lock (the text
// before becomes a version, "restore <id>"), then reloaded. Accepted
// during full cooling like any change (it applies after it ends).
func (v fileVersions) Restore(id int) (web.RestoreResult, error) {
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
	next, warns, err := config.Parse(raw)
	if err != nil {
		return res, fmt.Errorf("version %d does not parse, nothing written: %v", id, err)
	}
	cur, _ := v.Current()
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
	if l, err := v.store.List(); err == nil && len(l) > 0 {
		res.Version = l[0].ID
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
