// wiring_health.go backs GET /api/driver (the loaded fan driver for the
// header chip) and GET /api/health (the health page): the same checks as
// `n5-fangov check`, run in the daemon and cached briefly, because they
// read sysfs and run dpkg-query, dkms and systemctl. The kernel gate needs
// /lib/modules, which the unit hides (ProtectKernelModules=yes): the page
// shows the result `check --after-update` (apt hook, outside the sandbox)
// stored in the state directory.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/fsutil"
	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/n5driver"
	"github.com/SirRenix/n5-fangov/internal/profile"
	"github.com/SirRenix/n5-fangov/internal/web"
)

const (
	// driverCacheTTL bounds how often the expected module version is
	// looked up (dpkg-query); sysfs is read on every call.
	driverCacheTTL = time.Minute
	// healthCacheTTL bounds how often the full check runs.
	healthCacheTTL = 15 * time.Second
	// healthGraceCycles: GET /healthz counts "starting" as healthy for
	// this many cycle intervals after the daemon started.
	healthGraceCycles = 3
)

// healthView computes both documents; one per daemon.
type healthView struct {
	cfgPath string
	dir     string
	hw      *hwmon.FS
	dev     profile.Device
	svc     control.Service
	alerts  *alertManager
	now     func() time.Time
	// srcRoot (/usr/src), modulesRoot (/lib/modules) and gateFile (the
	// stored kernel gate) are fields for tests.
	srcRoot, modulesRoot, gateFile string

	build1     sync.Mutex // one build of the health document at a time
	mu         sync.Mutex
	expected   string
	expectedAt time.Time
	health     map[string]any
	healthAt   time.Time
}

func newHealthView(cfgPath, dir string, hw *hwmon.FS, dev profile.Device, svc control.Service, alerts *alertManager) *healthView {
	return &healthView{cfgPath: cfgPath, dir: dir, hw: hw, dev: dev, svc: svc, alerts: alerts, now: time.Now,
		srcRoot: defaultDKMSSrcRoot, modulesRoot: defaultModulesRoot, gateFile: kernelGatePath(stateDir())}
}

// isN5Pro reports whether the daemon drives the N5 Pro kernel module.
func (v *healthView) isN5Pro() bool {
	return v.dev != nil && v.dev.Profile().Name() == "n5pro"
}

// expectedVersion is the MODULE_VERSION the installed driver package
// builds, cached for driverCacheTTL.
func (v *healthView) expectedVersion() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.expectedAt.IsZero() || v.now().Sub(v.expectedAt) > driverCacheTTL {
		v.expected = expectedModuleVersion(installedPackageVersion(), dkmsSourceVersion(v.srcRoot))
		v.expectedAt = v.now()
	}
	return v.expected
}

// Driver is GET /api/driver. N5 Pro: the minisforum module from sysfs and
// whether it still has to be reloaded. Other profiles: the hwmon chip
// the daemon writes through, without versions.
func (v *healthView) Driver() web.DriverInfo {
	if !v.isN5Pro() {
		name := ""
		if v.dev != nil {
			if b, err := os.ReadFile(filepath.Join(v.dev.HwmonPath(), "name")); err == nil {
				name = strings.TrimSpace(string(b))
			}
		}
		return web.DriverInfo{Module: name, Loaded: name != "", Mode: "write-enabled"}
	}
	st := n5driver.Read(v.hw.Root)
	d := web.DriverInfo{Module: n5driver.ModuleName, Loaded: st.Loaded, Version: st.Version, SrcVersion: st.SrcVersion, Mode: st.Mode()}
	d.Expected = v.expectedVersion()
	d.Pending = st.Loaded && d.Expected != "" && st.Version != d.Expected
	return d
}

// Health is GET /api/health, cached for healthCacheTTL. Concurrent
// callers after the expiry wait for one build instead of each starting
// the subprocesses again.
func (v *healthView) Health() map[string]any {
	if h := v.cached(); h != nil {
		return h
	}
	v.build1.Lock()
	defer v.build1.Unlock()
	if h := v.cached(); h != nil {
		return h
	}
	h := v.build()
	v.mu.Lock()
	v.health, v.healthAt = h, v.now()
	v.mu.Unlock()
	return h
}

// cached returns the health document while it is fresh, else nil.
func (v *healthView) cached() map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.health != nil && v.now().Sub(v.healthAt) < healthCacheTTL {
		return v.health
	}
	return nil
}

func (v *healthView) build() map[string]any {
	checks, summary := healthItems(runChecks(v.cfgPath, v.dir))
	if !v.modulesVisible() {
		// dkms status cannot see the built modules either: not a finding
		for i := range checks {
			if checks[i].Name == "dkms" {
				checks[i] = web.HealthItem{Level: "skipped", Name: "dkms", Detail: "not visible from inside the service sandbox (ProtectKernelModules); see the kernel gate"}
			}
		}
	}
	h := map[string]any{
		"checked": v.now().Unix(),
		"checks":  checks,
		"summary": summary,
		"driver":  v.Driver(),
		"gate":    v.gate(),
	}
	if v.svc != nil {
		snap := v.svc.Snapshot()
		h["started"] = v.now().Unix() - snap.Uptime
		h["status"] = snap.Status
	}
	if v.alerts != nil {
		last := v.alerts.Last()
		ev := map[string]int64{}
		for _, k := range []string{"restart", "failed", "emergency", "kernel", "driver"} {
			if ts, ok := last[k]; ok {
				ev[k] = ts
			}
		}
		h["last_events"] = ev
	}
	return h
}

// healthItems turns check results into page lines and the summary line
// `n5-fangov check` prints. Indented detail lines of one finding keep
// the finding's level.
func healthItems(res []checkResult) ([]web.HealthItem, string) {
	out := make([]web.HealthItem, 0, len(res))
	warnings, failed := 0, false
	for _, r := range res {
		level := "ok"
		switch {
		case r.ok:
		case r.advisory:
			level = "warn"
			if strings.TrimSpace(r.name) != "" {
				warnings++
			}
		default:
			level = "fail"
			failed = true
		}
		out = append(out, web.HealthItem{Level: level, Name: strings.TrimSpace(r.name), Detail: r.detail})
	}
	switch {
	case failed:
		return out, "FAILED"
	case warnings > 0:
		return out, "passed with warnings"
	}
	return out, "all good"
}

// modulesVisible reports whether /lib/modules can be listed (it cannot
// inside the unit's sandbox).
func (v *healthView) modulesVisible() bool {
	ents, err := os.ReadDir(v.modulesRoot)
	return err == nil && len(ents) > 0
}

// gate is the kernel gate as page lines: computed here when /lib/modules
// is visible (CLI, tests), else the result `check --after-update` stored.
func (v *healthView) gate() []web.HealthItem {
	if !v.isN5Pro() {
		return []web.HealthItem{{Level: "skipped", Name: "kernel gate", Detail: "not applicable (profile is not n5pro)"}}
	}
	if !v.modulesVisible() {
		return storedGate(v.gateFile)
	}
	toolOut, toolErr := bootToolKernelList()
	g, err := kernelGate(v.modulesRoot, v.expectedVersion(), runningKernel(), toolOut, toolErr)
	if err != nil {
		return []web.HealthItem{{Level: "fail", Name: "kernel gate", Detail: err.Error()}}
	}
	return gateItems(g)
}

// gateItems turns a gate result into page lines: alert lines are fail,
// info lines warn, else the ok line.
func gateItems(g gateResult) []web.HealthItem {
	var out []web.HealthItem
	for _, l := range g.alert {
		out = append(out, web.HealthItem{Level: "fail", Name: "kernel gate", Detail: l})
	}
	for _, l := range g.info {
		out = append(out, web.HealthItem{Level: "warn", Name: "kernel gate", Detail: l})
	}
	if len(g.alert) == 0 {
		out = append(out, web.HealthItem{Level: "ok", Name: "kernel gate", Detail: strings.TrimPrefix(g.okLine, "n5-fangov: ")})
	}
	return out
}

// storedGateDoc is <state dir>/kernel-gate.json, written by
// `check --after-update`.
type storedGateDoc struct {
	TS    int64            `json:"ts"`
	Lines []web.HealthItem `json:"lines"`
}

// kernelGatePath is where `check --after-update` stores its result.
func kernelGatePath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "kernel-gate.json")
}

// saveGate stores the gate result for the health page; best effort (the
// apt hook must not fail on it).
func saveGate(path string, g gateResult, now time.Time) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(storedGateDoc{TS: now.Unix(), Lines: gateItems(g)})
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(path, append(b, '\n'), 0o600)
}

// storedGate reads the stored gate result; its lines carry the time it
// was computed.
func storedGate(path string) []web.HealthItem {
	none := []web.HealthItem{{Level: "skipped", Name: "kernel gate", Detail: "no stored result: the daemon cannot read /lib/modules (sandbox); the apt hook stores it after every package run, or run n5-fangov check --after-update"}}
	b, err := os.ReadFile(path)
	if err != nil {
		return none
	}
	var d storedGateDoc
	if err := json.Unmarshal(b, &d); err != nil || len(d.Lines) == 0 {
		return none
	}
	at := time.Unix(d.TS, 0).Format("2006-01-02 15:04")
	for i := range d.Lines {
		d.Lines[i].Detail += " (check --after-update, " + at + ")"
	}
	return d.Lines
}

// applyHealthDeps wires /api/driver, /api/health and the /healthz grace.
func applyHealthDeps(deps *web.Deps, v *healthView, interval time.Duration) {
	if v != nil {
		deps.Driver = func() any { return v.Driver() }
		deps.Health = func() any { return v.Health() }
	}
	deps.HealthGrace = healthGraceCycles * interval
}
