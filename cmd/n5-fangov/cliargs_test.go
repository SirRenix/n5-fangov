package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseInterspersed(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantPos []string
		wantErr bool
		force   bool
		out     string
	}{
		{"flags before", []string{"--force", "--out", "d", "pwm3"}, []string{"pwm3"}, false, true, "d"},
		{"flags after", []string{"pwm3", "--force", "--out", "d"}, []string{"pwm3"}, false, true, "d"},
		{"flags around", []string{"--out", "d", "pwm3", "--force"}, []string{"pwm3"}, false, true, "d"},
		{"two positionals with flags between", []string{"a", "--force", "b"}, []string{"a", "b"}, false, true, ""},
		{"inline value", []string{"pwm3", "--out=d"}, []string{"pwm3"}, false, false, "d"},
		{"no args", nil, nil, false, false, ""},
		{"dash is positional", []string{"-", "--force"}, []string{"-"}, false, true, ""},
		{"terminator", []string{"--force", "--", "--out", "x"}, []string{"--out", "x"}, false, true, ""},
		{"terminator after positional", []string{"a", "--", "-b"}, []string{"a", "-b"}, false, false, ""},
		{"double dash as flag value", []string{"--out", "--", "a", "--force"}, []string{"a"}, false, true, "--"},
		{"unknown flag before", []string{"--bogus", "pwm3"}, nil, true, false, ""},
		{"unknown flag after", []string{"pwm3", "--bogus"}, nil, true, false, ""},
		{"missing value after", []string{"pwm3", "--out"}, nil, true, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			force := fs.Bool("force", false, "")
			out := fs.String("out", "", "")
			pos, err := parseInterspersed(fs, tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if !reflect.DeepEqual(pos, tc.wantPos) {
				t.Errorf("positionals = %q, want %q", pos, tc.wantPos)
			}
			if *force != tc.force || *out != tc.out {
				t.Errorf("force=%v out=%q, want %v %q", *force, *out, tc.force, tc.out)
			}
		})
	}
}

// "n5-fangov test pwm3 --force" stopped at the channel and failed with a
// usage error (0.4.x, found 2026-10-09).
func TestParseTestArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		want    testOpts
		errText string
	}{
		{"flag after channel", []string{"pwm3", "--force"}, true,
			testOpts{channel: "pwm3", force: true, cfgPath: defaultConfigPath, hold: testStepHold, sample: testSample}, ""},
		{"flag before channel", []string{"--force", "pwm3"}, true,
			testOpts{channel: "pwm3", force: true, cfgPath: defaultConfigPath, hold: testStepHold, sample: testSample}, ""},
		{"no flags", []string{"hdd"}, true,
			testOpts{channel: "hdd", cfgPath: defaultConfigPath, hold: testStepHold, sample: testSample}, ""},
		{"flags on both sides", []string{"--config", "/tmp/c.toml", "pwm1", "--hold", "10s", "--sample", "2s", "--force"}, true,
			testOpts{channel: "pwm1", force: true, cfgPath: "/tmp/c.toml", hold: 10 * time.Second, sample: 2 * time.Second}, ""},
		{"unknown flag after channel", []string{"pwm3", "--bogus"}, false, testOpts{}, "flag provided but not defined"},
		{"unknown flag before channel", []string{"--bogus", "pwm3"}, false, testOpts{}, "flag provided but not defined"},
		{"missing channel", []string{"--force"}, false, testOpts{}, "usage: n5-fangov test"},
		{"no arguments", nil, false, testOpts{}, "usage: n5-fangov test"},
		{"two channels", []string{"pwm1", "--force", "pwm3"}, false, testOpts{}, "usage: n5-fangov test"},
		{"bad sample after channel", []string{"pwm3", "--sample", "0s"}, false, testOpts{}, "must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got testOpts
			var ok bool
			errOut := captureStderr(t, func() { got, ok = parseTestArgs(tc.args) })
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (stderr %q)", ok, tc.ok, errOut)
			}
			if !ok {
				if !strings.Contains(errOut, tc.errText) {
					t.Errorf("stderr %q, want %q", errOut, tc.errText)
				}
				return
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	if rc := cmdTest([]string{"pwm3", "--bogus"}); rc != exitUsage {
		t.Errorf("cmdTest unknown flag: rc=%d, want %d", rc, exitUsage)
	}
}

// The other subcommands with positional arguments and flags take the flags
// on either side as well.
func TestSubcommandsFlagsAfterPositional(t *testing.T) {
	root := t.TempDir()
	t.Setenv("N5FANGOV_RUN_DIR", filepath.Join(root, "run")) // no socket: offline paths
	t.Setenv("N5FANGOV_LOG_ROOT", root)                      // [log].file must lie under the log root
	none := filepath.Join(root, "none.toml")

	// log: positional N with --config after it
	logPath := filepath.Join(root, "n5.log")
	if err := os.WriteFile(logPath, []byte("l1\nl2\nl3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "config.toml")
	if err := os.WriteFile(cfg, []byte("[log]\nfile = \""+filepath.ToSlash(logPath)+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"1", "--config", cfg}, {"--config", cfg, "1"}} {
		if out, errOut, code := captureOutput(t, func() int { return cmdLog(args) }); code != exitOK || out != "l3\n" {
			t.Errorf("log %v: %d %q %q", args, code, out, errOut)
		}
	}

	// usage errors: exit 2 with the parser's or the command's message
	usage := []struct {
		name string
		run  func() int
	}{
		{"log unknown flag after N", func() int { return cmdLog([]string{"1", "--bogus"}) }},
		{"log N and --clear", func() int { return cmdLog([]string{"1", "--clear"}) }},
		{"alerts unknown flag after sub", func() int { return cmdAlerts([]string{"status", "--bogus"}) }},
		{"alerts missing sub", func() int { return cmdAlerts([]string{"--config", none}) }},
		{"export two files", func() int { return cmdExport([]string{"a", "--config", none, "b"}) }},
		{"import unknown flag after file", func() int { return cmdImport([]string{"f", "--bogus"}) }},
		{"import missing file", func() int { return cmdImport([]string{"--config", none}) }},
		{"cert missing sub", func() int { return cmdCert([]string{"--config", none}) }},
		{"cert unknown sub", func() int { return cmdCert([]string{"bogus"}) }},
		{"cert info with --der", func() int { return cmdCert([]string{"info", "--der"}) }},
		{"cert export two files", func() int { return cmdCert([]string{"export", "a", "--der", "b"}) }},
		{"cert regen positional", func() int { return cmdCert([]string{"regen", "x", "--new-key"}) }},
		{"cert upload one file", func() int { return cmdCert([]string{"upload", "c.pem", "--config", none}) }},
		{"token create unknown flag after name", func() int { return cmdToken([]string{"create", "x", "--bogus"}) }},
		{"token create missing name", func() int { return cmdToken([]string{"create", "--scope", "read"}) }},
	}
	for _, u := range usage {
		if out, errOut, code := captureOutput(t, u.run); code != exitUsage || out != "" {
			t.Errorf("%s: %d %q %q", u.name, code, out, errOut)
		}
	}

	// accepted: parsing passes, the command then fails on the missing
	// daemon/files (exit 1), never with a usage error
	accepted := []struct {
		name string
		run  func() int
	}{
		{"cert info --config after", func() int { return cmdCert([]string{"info", "--config", none}) }},
		{"cert export --der after file", func() int {
			return cmdCert([]string{"export", filepath.Join(root, "c.cer"), "--der", "--config", none})
		}},
		{"import --config after file", func() int { return cmdImport([]string{filepath.Join(root, "missing.json"), "--config", none}) }},
	}
	for _, a := range accepted {
		if _, errOut, code := captureOutput(t, a.run); code == exitUsage || strings.Contains(errOut, "usage:") || strings.Contains(errOut, "flag provided but not defined") {
			t.Errorf("%s: %d %q", a.name, code, errOut)
		}
	}
}
