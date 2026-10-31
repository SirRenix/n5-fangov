package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/n5driver"
)

// readBundle returns meta.json, verdict.json and events.log of the only
// bundle below out.
func readBundle(t *testing.T, out string) (meta map[string]any, verdict string, events string) {
	t.Helper()
	dirs, _ := os.ReadDir(out)
	if len(dirs) != 1 {
		t.Fatalf("bundles %v", dirs)
	}
	d := filepath.Join(out, dirs[0].Name())
	raw, _ := os.ReadFile(filepath.Join(d, "meta.json"))
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	v, _ := os.ReadFile(filepath.Join(d, "verdict.json"))
	ev, _ := os.ReadFile(filepath.Join(d, "events.log"))
	return meta, string(v), string(ev)
}

func noECTestState(t *testing.T) {
	t.Helper()
	old := ectestStatePath
	ectestStatePath = filepath.Join(t.TempDir(), "n5-ectest", "restore.json")
	t.Cleanup(func() { ectestStatePath = old })
}

// TESTSPEC 0.4: bundle carries spec 0.4, no deviations, the redaction rule
// set, the settle times and the role; the state file of the run is gone
// after a clean restore.
func TestValidateSpec04Bundle(t *testing.T) {
	referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	noECTestState(t)
	(&fakeClock{now: time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)}).install(t, nil)
	validateStdin = strings.NewReader(validateConfirm + "\n")
	out := t.TempDir()
	stdout, errOut, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T0,T1", "--boot", "cold", "--out", out, "pwm3"})
	})
	if rc != exitOK {
		t.Fatalf("rc %d\n%s\n%s", rc, stdout, errOut)
	}
	meta, verdict, events := readBundle(t, out)
	if meta["spec_version"] != "0.4" || meta["result_role"] == nil || meta["redaction"] == nil {
		t.Fatalf("meta %v", meta)
	}
	if devs, ok := meta["spec_deviations"].([]any); !ok || len(devs) != 0 {
		t.Fatalf("spec_deviations %v", meta["spec_deviations"])
	}
	if st, _ := meta["settle_time_s"].(map[string]any); st["pwm3"] != float64(10) || st["pwm4"] != float64(1) {
		t.Fatalf("settle_time_s %v", meta["settle_time_s"])
	}
	gates := map[string]string{}
	for _, g := range meta["gates"].([]any) {
		m := g.(map[string]any)
		gates[m["id"].(string)] = m["result"].(string)
	}
	if gates["G7"] != "PASS" || gates["G6"] != "PARTIAL" || len(gates) != 7 {
		t.Fatalf("gates %v", gates)
	}
	if !strings.Contains(verdict, `"not_judged"`) || !strings.Contains(verdict, "settle time 10 s") {
		t.Fatalf("verdict without the unjudged samples: %.400s", verdict)
	}
	if _, err := os.Stat(validateMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("state file left after a clean restore: %v", err)
	}
	if !strings.Contains(events, "restore pwm3 after T1 ok") {
		t.Fatalf("events %s", events)
	}
}

// G6 with the driver's ecram file (0.2.1+sr2): PASS, tables in meta.json;
// C1 stays INCONCLUSIVE (TESTSPEC 0.4 #9-C1 a).
func TestValidateG6ECRAM(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	noECTestState(t)
	var b strings.Builder
	for row := 0; row < 16; row++ {
		fmt.Fprintf(&b, "%02x:", row*16)
		for i := 0; i < 16; i++ {
			fmt.Fprintf(&b, " %02x", row*16+i)
		}
		b.WriteString("\n")
	}
	if err := os.WriteFile(n5driver.ECRAMPath(root), []byte(b.String()), 0o400); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, errOut, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T0,C1", "--boot", "cold", "--out", out, "pwm3"})
	}); rc != exitOK {
		t.Fatalf("rc %d %s", rc, errOut)
	}
	meta, verdict, _ := readBundle(t, out)
	tab, _ := meta["ec_ram_tables"].(map[string]any)
	if tab["0x70-0x81"] == nil || !strings.HasPrefix(tab["0x10-0x27"].(string), "10 11 12") {
		t.Fatalf("tables %v", meta["ec_ram_tables"])
	}
	if !strings.Contains(verdict, "no C1 path before the byte-compare") {
		t.Fatalf("C1: %s", verdict)
	}
}

// G7 (TESTSPEC 0.4 §3, #7, #12): a force-loaded driver fails G7, the
// writing tests are INCONCLUSIVE "not run: gate G7", G3/G4 SKIPPED, nothing
// written, exit 1.
func TestValidateG7Blocks(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	noECTestState(t)
	if err := os.WriteFile(filepath.Join(root, "module/minisforum_n5_it5571/parameters/force"), []byte("Y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	(&fakeClock{now: time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)}).install(t, nil)
	validateStdin = strings.NewReader(validateConfirm + "\n")
	before := snapshotTree(t, root)
	out := t.TempDir()
	stdout, _, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T1", "--boot", "cold", "--out", out, "pwm3"})
	})
	if rc != exitFail || !strings.Contains(stdout, "Writing tests not run: G7 failed") || strings.Contains(stdout, "sampling") {
		t.Fatalf("rc %d\n%s", rc, stdout)
	}
	for k, v := range before {
		if snapshotTree(t, root)[k] != v {
			t.Fatalf("%s written after a failed G7", k)
		}
	}
	meta, verdict, _ := readBundle(t, out)
	if !strings.Contains(verdict, "not run: gate G7 failed: module loaded with force=Y") {
		t.Fatalf("verdict %s", verdict)
	}
	for _, g := range meta["gates"].([]any) {
		m := g.(map[string]any)
		if (m["id"] == "G3" || m["id"] == "G4") && (m["result"] != "SKIPPED" || !strings.Contains(m["detail"].(string), "G7 failed")) {
			t.Fatalf("gate %v", m)
		}
	}
}

// Third refusal (TESTSPEC 0.4 §3, #10): an own state file of an earlier
// run, an open n5-ectest state file, or an unreadable one refuses the run
// without a bundle.
func TestValidateRefusesOpenRestoreState(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	noECTestState(t)
	before := snapshotTree(t, root)
	run := func() (string, int, string) {
		out := t.TempDir()
		validateStdin = strings.NewReader(validateConfirm + "\n")
		_, errOut, rc := captureOutput(t, func() int {
			return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T1", "--boot", "cold", "--out", out, "pwm3"})
		})
		return errOut, rc, out
	}
	if err := writeValidateMarker(3, map[int]string{3: "140"}, "/tmp/x"); err != nil {
		t.Fatal(err)
	}
	errOut, rc, out := run()
	if rc != exitFail || !strings.Contains(errOut, "did not confirm the restore") {
		t.Fatalf("own marker: rc %d %s", rc, errOut)
	}
	if d, _ := os.ReadDir(out); len(d) != 0 {
		t.Fatalf("bundle written on a refusal: %v", d)
	}
	if err := os.WriteFile(validateMarkerPath(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if errOut, rc, _ := run(); rc != exitFail || !strings.Contains(errOut, "not readable") {
		t.Fatalf("broken marker: rc %d %s", rc, errOut)
	}
	if err := os.Remove(validateMarkerPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ectestStatePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ectestStatePath, []byte(`{"state":"active","pid":77,"channels":{"1":"auto"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if errOut, rc, _ := run(); rc != exitFail || !strings.Contains(errOut, "n5-ectest run (pid 77)") {
		t.Fatalf("n5-ectest state: rc %d %s", rc, errOut)
	}
	if err := os.WriteFile(ectestStatePath, []byte(`{"state":"restored"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if why := openRestoreState(); why != "" {
		t.Fatalf("restored n5-ectest state refuses: %s", why)
	}
	for k, v := range before {
		if snapshotTree(t, root)[k] != v {
			t.Fatalf("%s written by a refused run", k)
		}
	}
}

// G1 at every sample (TESTSPEC 0.4 §3, #5): a pwm value this run did not
// write aborts the run; the channel is restored.
func TestValidateG1ForeignPWMWrite(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	noECTestState(t)
	pwm1 := filepath.Join(root, "class", "hwmon", "hwmon14", "pwm1")
	(&fakeClock{now: time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)}).install(t, func(n int, _ context.Context) bool {
		if n == 80 { // in the T1 steps: after the gate window (60) and the rest reference (12)
			_ = os.WriteFile(pwm1, []byte("201\n"), 0o644)
		}
		return true
	})
	validateStdin = strings.NewReader(validateConfirm + "\n")
	out := t.TempDir()
	stdout, _, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T1", "--boot", "cold", "--out", out, "pwm3"})
	})
	if rc != exitFail || !strings.Contains(stdout, "ABORTED: another pwm writer became active: pwm1 changed from") {
		t.Fatalf("rc %d\n%s", rc, stdout)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "class", "hwmon", "hwmon14", "pwm3")); strings.TrimSpace(string(b)) != "140" {
		t.Fatalf("pwm3 not restored: %q", b)
	}
}

// G1 by process and open file (TESTSPEC 0.4 §3 G1, #5).
func TestG1ProbeProcessesAndFDs(t *testing.T) {
	root := referenceN5(t)
	proc := fakeProc(t)
	hwmonPath := filepath.Join(root, "class", "hwmon", "hwmon14")
	mk := func(pid, comm, cmdline string) {
		d := filepath.Join(proc, pid)
		if err := os.MkdirAll(filepath.Join(d, "fd"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(d, "cmdline"), []byte(cmdline), 0o644)
	}
	mk("4242", "fancontrol", "/bin/bash\x00/usr/sbin/fancontrol\x00")
	mk("4243", "n5-fangov", "/usr/bin/n5-fangov\x00--config\x00/etc/n5-fangov/config.toml\x00serve\x00")
	mk("4244", "n5-fangov", "/usr/bin/n5-fangov\x00status\x00")
	mk("4245", "python3", "python3\x00fan.py\x00")
	if err := os.Symlink(filepath.Join(hwmonPath, "pwm2"), filepath.Join(proc, "4245", "fd", "3")); err != nil {
		t.Fatal(err)
	}
	p := newG1Probe(hwmon.New(), hwmonPath, nil)
	got := strings.Join(p.check(), "; ")
	for _, want := range []string{"process fancontrol (pid 4242)", "process n5-fangov serve (pid 4243)", "pid 4245 (python3) holds pwm2 open"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "4244") {
		t.Errorf("n5-fangov status counted as a writer: %s", got)
	}
}

// --restore overrides the stop value (TESTSPEC 0.4 §4); a duty below 100 is
// refused.
func TestValidateRestoreFlag(t *testing.T) {
	m, err := parseRestoreFlag("pwm3=200, pwm4=auto")
	if err != nil || m[3] != "200" || m[4] != "auto" {
		t.Fatalf("%v %v", m, err)
	}
	for _, bad := range []string{"pwm3=99", "pwm5=200", "pwm3", "pwm1=fast"} {
		if _, err := parseRestoreFlag(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	root := referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	noECTestState(t)
	(&fakeClock{now: time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)}).install(t, nil)
	validateStdin = strings.NewReader(validateConfirm + "\n")
	out := t.TempDir()
	if _, errOut, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--restore", "pwm3=200", "--tests", "T1", "--boot", "cold", "--out", out, "pwm3"})
	}); rc != exitOK {
		t.Fatalf("rc %d %s", rc, errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "class", "hwmon", "hwmon14", "pwm3")); strings.TrimSpace(string(b)) != "200" {
		t.Fatalf("pwm3 ends at %q, want 200", b)
	}
	meta, _, _ := readBundle(t, out)
	if rv, _ := meta["restore_values"].(map[string]any); rv["3"] != "200" {
		t.Fatalf("restore_values %v", meta["restore_values"])
	}
	if _, _, rc := captureOutput(t, func() int { return cmdValidate([]string{"--restore", "pwm3=50", "pwm3"}) }); rc != exitUsage {
		t.Fatalf("--restore pwm3=50: rc %d", rc)
	}
}

// The redactor of a bundle knows this machine's serials, users and SSH
// values (TESTSPEC 0.4 #8).
func TestBundleRedactor(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	if err := os.WriteFile(filepath.Join(root, "class", "dmi", "id", "product_serial"), []byte("N5P0012345\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"USER": "alice", "SUDO_USER": "root", "SSH_CONNECTION": "198.51.100.7 51000 198.51.100.8 22"}
	r := bundleRedactor(root, collectIdentity(hwmon.New()), func(k string) string { return env[k] })
	out := r.String("serial N5P0012345 by alice (root) from 198.51.100.7 51000 198.51.100.8 22 kernel 7.0.14-20-pve")
	if strings.Contains(out, "N5P0012345") || strings.Contains(out, "alice") || strings.Contains(out, "198.51") || !strings.Contains(out, "(root)") || !strings.Contains(out, "7.0.14-20-pve") {
		t.Fatalf("redacted: %s", out)
	}
}
