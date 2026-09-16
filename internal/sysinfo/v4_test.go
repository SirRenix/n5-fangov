package sysinfo

// Regression tests for the v0.4 audit fixes: CRC-only arrays are not ECC,
// the extended configured speed wins over the nominal speed, NICs are
// never null, a failed lspci is retried before CacheTTL, and Collect does
// not hold the lock while the static part is gathered.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMemoryECCOnlyCorrecting: error correction 0x07 (CRC) is detection,
// not correction; 0x05/0x06 are ECC.
func TestMemoryECCOnlyCorrecting(t *testing.T) {
	for code, want := range map[byte]bool{0x03: false, 0x04: false, 0x05: true, 0x06: true, 0x07: false} {
		var sb smbiosBuilder
		sb.add(16, 0x17, 0x1000, map[int][]byte{0x04: {0x03}, 0x05: {0x03}, 0x06: {code}, 0x0D: u16(1)})
		sb.add(17, 0x1C, 0x1100, map[int][]byte{0x04: u16(0x1000), 0x08: u16(64), 0x0A: u16(64), 0x0C: u16(8192), 0x12: {0x1A}, 0x15: u16(3200)})
		sb.add(127, 4, 0x7F00, nil)
		mods, err := ParseMemoryModules(sb.b)
		if err != nil || len(mods) != 1 {
			t.Fatalf("code %#x: %v %+v", code, err, mods)
		}
		if mods[0].ECC != want {
			t.Errorf("error correction %#x: ECC=%v, want %v", code, mods[0].ECC, want)
		}
	}
}

// TestMemorySpeedExtendedConfigured: a configured speed of 0xFFFF points
// at the extended configured speed (0x58), which wins over a plain
// nominal speed (0x15).
func TestMemorySpeedExtendedConfigured(t *testing.T) {
	var sb smbiosBuilder
	sb.add(17, 0x5C, 0x1100, map[int][]byte{0x0C: u16(8192), 0x12: {0x22}, 0x15: u16(6400), 0x20: u16(0xFFFF), 0x54: u32(0), 0x58: u32(70000)})
	sb.add(127, 4, 0x7F00, nil)
	mods, err := ParseMemoryModules(sb.b)
	if err != nil || len(mods) != 1 {
		t.Fatalf("%v %+v", err, mods)
	}
	if mods[0].SpeedMTs != 70000 {
		t.Errorf("speed = %d, want 70000 (extended configured speed)", mods[0].SpeedMTs)
	}
	// both 0xFFFF and no extended values: unknown
	sb = smbiosBuilder{}
	sb.add(17, 0x5C, 0x1100, map[int][]byte{0x0C: u16(8192), 0x12: {0x22}, 0x15: u16(0xFFFF), 0x20: u16(0xFFFF)})
	sb.add(127, 4, 0x7F00, nil)
	if mods, _ := ParseMemoryModules(sb.b); len(mods) != 1 || mods[0].SpeedMTs != 0 {
		t.Errorf("unknown speed = %+v", mods)
	}
}

// TestNICsNeverNull: a box without physical interfaces serialises
// "nics": [] (the dashboard iterates it).
func TestNICsNeverNull(t *testing.T) {
	o := testOptions(t)
	o.LSPCI = "-"
	if err := os.RemoveAll(filepath.Join(o.Sysfs, "class", "net")); err != nil {
		t.Fatal(err)
	}
	in := Collect(o)
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if in.NICs == nil || !strings.Contains(string(b), `"nics":[]`) {
		t.Errorf("nics = %#v / %s", in.NICs, string(b))
	}
}

// TestLspciFailureRetried: a failed lspci run does not pin nameless PCI
// devices for CacheTTL; after LspciRetry the names are read again.
func TestLspciFailureRetried(t *testing.T) {
	o := testOptions(t)
	now := time.Unix(1789500000, 0)
	o.Now = func() time.Time { return now }
	good := o.LSPCI
	o.LSPCI = filepath.Join(t.TempDir(), "nope")
	c := NewCollector(o)
	first := c.Collect()
	if !hasPrefix(first.Errors, "lspci:") || first.GPUs[0].Name != "PCI device 1002:150e" {
		t.Fatalf("first = %v / %+v", first.Errors, first.GPUs)
	}
	c.o.LSPCI = good
	now = now.Add(LspciRetry - time.Second)
	if in := c.Collect(); !hasPrefix(in.Errors, "lspci:") {
		t.Errorf("retried before LspciRetry: %v", in.Errors)
	}
	now = now.Add(2 * time.Second)
	in := c.Collect()
	if hasPrefix(in.Errors, "lspci:") || in.GPUs[0].Name == "PCI device 1002:150e" || in.StaticAt != now.Unix() {
		t.Errorf("not retried after LspciRetry: %v / %+v", in.Errors, in.GPUs)
	}
	// a good result is then cached for CacheTTL again
	c.o.LSPCI = filepath.Join(t.TempDir(), "nope")
	now = now.Add(LspciRetry + time.Second)
	if in := c.Collect(); hasPrefix(in.Errors, "lspci:") {
		t.Errorf("good static part re-read before CacheTTL: %v", in.Errors)
	}
}

func hasPrefix(list []string, p string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// TestCollectNotBlockedByRefresh: while one Collect gathers the static
// part, a second one is served from the previous static part instead of
// waiting; the very first collection is waited for.
func TestCollectNotBlockedByRefresh(t *testing.T) {
	o := testOptions(t)
	now := time.Unix(1789500000, 0)
	var mu sync.Mutex
	o.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	// an lspci that blocks until the gate file appears
	lspci := filepath.Join(t.TempDir(), "lspci")
	fifo := filepath.Join(t.TempDir(), "gate")
	if err := os.WriteFile(lspci, []byte("#!/bin/sh\necho started >> \""+fifo+".started\"\nwhile [ ! -e \""+fifo+"\" ]; do sleep 0.05; done\necho '00:00.0 \"Host bridge\" \"Example\" \"Bridge\"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := NewCollector(o)
	if in := c.Collect(); in.StaticAt == 0 { // first collection with the default lspci fixture
		t.Fatal("first collection missing")
	}
	// second static collection blocks in lspci; a concurrent Collect must
	// return the cached part meanwhile
	c.o.LSPCI = lspci
	mu.Lock()
	now = now.Add(DefaultCacheTTL + time.Second)
	mu.Unlock()
	done := make(chan Info, 1)
	go func() { done <- c.Collect() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(fifo + ".started"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("blocking lspci never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	fast := make(chan Info, 1)
	go func() { fast <- c.Collect() }()
	select {
	case in := <-fast:
		if in.StaticAt != 1789500000 {
			t.Errorf("concurrent Collect got a static part from %d, want the cached one", in.StaticAt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent Collect blocked behind the refresh")
	}
	if err := os.WriteFile(fifo, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-done:
		if in.StaticAt != now.Unix() {
			t.Errorf("refresh result StaticAt = %d, want %d", in.StaticAt, now.Unix())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never finished")
	}
}
