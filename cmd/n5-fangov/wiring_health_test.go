package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
)

// The header chip turns "reload pending" when the loaded module is not the
// build the installed package expects (the state between apt install and
// the module reload).
func TestHealthViewDriverPending(t *testing.T) {
	root := fakeN5(t, false)
	hw := hwmon.New()
	dev, err := detectDevice(hw, "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	mod := filepath.Join(root, "module", "minisforum_n5_it5571")
	if err := os.WriteFile(filepath.Join(mod, "version"), []byte("0.2.1+sr1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "srcversion"), []byte("22C5CA61EBD0012B034369B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubPackage(t, "0.2.1+sr2-1")
	v := newHealthView("", t.TempDir(), hw, dev, nil, nil)
	v.srcRoot = t.TempDir() // the host's /usr/src stays out of the test
	d := v.Driver()
	if d.Module != "minisforum_n5_it5571" || !d.Loaded || d.Version != "0.2.1+sr1" || d.SrcVersion != "22C5CA61EBD0012B034369B" {
		t.Fatalf("driver = %+v", d)
	}
	if d.Expected != "0.2.1+sr2" || !d.Pending {
		t.Errorf("sr1 loaded, sr2 installed: %+v", d)
	}
	// after the reload; the expected version is cached, the module is read live
	if err := os.WriteFile(filepath.Join(mod, "version"), []byte("0.2.1+sr2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := v.Driver(); d.Pending || d.Version != "0.2.1+sr2" {
		t.Errorf("after the reload: %+v", d)
	}
	// unknown package version: nothing to compare, never pending
	stubPackage(t, "")
	v = newHealthView("", t.TempDir(), hw, dev, nil, nil)
	v.srcRoot = t.TempDir()
	v.now = func() time.Time { return time.Unix(1_789_500_000, 0) }
	if d := v.Driver(); d.Pending {
		t.Errorf("no package, no DKMS source: %+v", d)
	}
}

func TestHealthItemsLevels(t *testing.T) {
	items, sum := healthItems([]checkResult{
		{ok: true, name: "config", detail: "parses"},
		{ok: false, advisory: true, name: "config", detail: "2 warning(s)"},
		{ok: false, advisory: true, name: "  ", detail: "detail line"},
	})
	if len(items) != 3 || items[0].Level != "ok" || items[1].Level != "warn" || items[2].Level != "warn" || items[2].Name != "" {
		t.Errorf("items = %+v", items)
	}
	if sum != "passed with warnings" {
		t.Errorf("summary = %q", sum)
	}
	if _, sum := healthItems([]checkResult{{ok: false, name: "profile", detail: "not detected"}}); sum != "FAILED" {
		t.Errorf("fatal summary = %q", sum)
	}
	if _, sum := healthItems([]checkResult{{ok: true, name: "config"}}); sum != "all good" {
		t.Errorf("ok summary = %q", sum)
	}
}

// Inside the unit's sandbox /lib/modules is hidden: the page shows the gate
// that check --after-update stored, with its time; without one it says so
// instead of failing.
func TestHealthViewStoredGate(t *testing.T) {
	fakeN5(t, false)
	hw := hwmon.New()
	dev, err := detectDevice(hw, "n5pro")
	if err != nil {
		t.Fatal(err)
	}
	v := newHealthView("", t.TempDir(), hw, dev, nil, nil)
	v.modulesRoot = filepath.Join(t.TempDir(), "hidden") // empty, as in the sandbox
	v.gateFile = filepath.Join(t.TempDir(), "kernel-gate.json")
	if got := v.gate(); len(got) != 1 || got[0].Level != "skipped" || !strings.Contains(got[0].Detail, "check --after-update") {
		t.Errorf("no stored result: %+v", got)
	}
	g := gateResult{alert: []string{"7.0.14-30-pve: module missing"}, info: []string{"7.0.12-1-pve: not bootable"}}
	if err := saveGate(v.gateFile, g, time.Unix(1_789_500_000, 0)); err != nil {
		t.Fatal(err)
	}
	got := v.gate()
	if len(got) != 2 || got[0].Level != "fail" || got[1].Level != "warn" || !strings.Contains(got[0].Detail, "module missing (check --after-update, ") {
		t.Errorf("stored gate: %+v", got)
	}
	ok := gateResult{okLine: "n5-fangov: fan driver module present for 2 kernel(s)"}
	if err := saveGate(v.gateFile, ok, time.Unix(1_789_500_000, 0)); err != nil {
		t.Fatal(err)
	}
	if got := v.gate(); len(got) != 1 || got[0].Level != "ok" || strings.HasPrefix(got[0].Detail, "n5-fangov:") {
		t.Errorf("stored ok gate: %+v", got)
	}
}
