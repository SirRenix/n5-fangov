package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/profile"
)

func init() {
	register("setup", command{run: cmdSetup})
	register("passwd", command{run: cmdPasswd})
}

// Defaults of the setup dialogue.
const (
	setupPort    = "8010"
	setupOrg     = "n5-fangov"
	setupAdmin   = "admin"
	setupTLSDir  = "tls" // under the config directory
	setupBakStem = ".bak-"
)

// Generic profile defaults: a conservative curve (fans never below ~40 %,
// full at 70 C) for chips that are documented but not verified.
var (
	genericCurve    = [][2]int{{40, 100}, {70, 255}}
	genericCritical = 85
)

// cmdSetup writes /etc/n5-fangov/config.toml for this machine: detected
// profile → channel set, scope local/lan → listener, auth and TLS. Every
// answer can be given as a flag; with --yes nothing is asked and every
// needed flag must be present. An existing file is backed up first.
func cmdSetup(args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file to write")
	listen := fs.String("listen", "", "local | lan | host:port  (local: 127.0.0.1 without auth/TLS; lan: primary LAN IP with basic auth and HTTPS)")
	user := fs.String("user", "", "web user (lan)")
	password := fs.String("password", "", passwordFlagHelp)
	passwordFile := fs.String("password-file", "", passwordFileFlagHelp)
	yes := fs.Bool("yes", false, "non-interactive: no questions, all needed flags required, existing config overwritten (backup kept)")
	prof := fs.String("profile", "auto", "profile to detect (auto | n5pro | nct67xx | it87xx | monitor)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov setup [--listen local|lan|HOST:PORT] [--user U] [--password-file F | --password -] [--yes]")
		return exitUsage
	}
	pwArg, err := passwordFromArgs(*password, *passwordFile, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		return exitUsage
	}

	// 1. hardware
	hw := hwmon.New()
	dev, err := detectDevice(hw, *prof)
	if err != nil {
		fmt.Fprintf(os.Stderr, "setup: %v\nNo profile detected; nothing written. `n5-fangov detect` shows what is visible.\n", err)
		return exitFail
	}
	p := dev.Profile()
	fmt.Printf("profile: %s (%s) at %s", p.Name(), p.Title(), dev.HwmonPath())
	if p.Verified() {
		fmt.Println("  [verified on hardware]")
	} else {
		fmt.Println("  [from documentation, untested]")
	}
	chans := setupChannels(dev, hw, newSensorFactory(hw, dev))
	for _, c := range chans {
		fmt.Printf("  channel %-6s pwm%d  sensor %-16s curve %v  critical %d  stop %s\n", c.Name, c.PWM, c.Sensor, c.Curve, c.Critical, c.Stop)
	}
	if len(chans) == 0 {
		fmt.Println("  no pwm channels: monitoring only")
	}

	// 2. scope
	var pr *prompter
	if !*yes {
		pr = openPrompter()
		defer pr.close()
	}
	scope := strings.ToLower(strings.TrimSpace(*listen))
	if scope == "" {
		if *yes {
			fmt.Fprintln(os.Stderr, "setup: --yes needs --listen local|lan|HOST:PORT")
			return exitUsage
		}
		fmt.Println()
		fmt.Println("Web UI scope:")
		fmt.Println("  local  127.0.0.1:" + setupPort + ", no auth, plain HTTP (use ssh -L or a reverse proxy)")
		fmt.Println("  lan    this machine's LAN address, basic auth, HTTPS with a self-signed certificate")
		scope, err = pr.askChoice("scope", []string{"local", "lan"}, "local")
		if err != nil {
			return exitFail
		}
	}
	w := webSpec{Listen: "127.0.0.1:" + setupPort, Auth: "none", TLS: "off"}
	switch scope {
	case "local":
	case "lan":
		ip := primaryLANIP()
		if ip == "" {
			fmt.Fprintln(os.Stderr, "setup: no LAN address found; use --listen HOST:PORT")
			return exitFail
		}
		w.Listen = net.JoinHostPort(ip, setupPort)
	default:
		if _, _, err := net.SplitHostPort(scope); err != nil {
			fmt.Fprintf(os.Stderr, "setup: --listen %q is neither local, lan nor host:port\n", scope)
			return exitUsage
		}
		w.Listen = scope
	}
	if !isLoopbackListen(w.Listen) {
		w.Auth, w.TLS = "basic", "auto"
		w.User = strings.TrimSpace(*user)
		if w.User == "" {
			if *yes {
				fmt.Fprintln(os.Stderr, "setup: --yes with a LAN listener needs --user and --password-file (or --password -)")
				return exitUsage
			}
			if w.User, err = pr.ask("web user", setupAdmin); err != nil {
				return exitFail
			}
		}
		if err := validUserName(w.User); err != nil {
			fmt.Fprintln(os.Stderr, "setup:", err)
			return exitUsage
		}
		pw := pwArg
		if pw == "" {
			if *yes {
				fmt.Fprintln(os.Stderr, "setup: --yes with a LAN listener needs --user and --password-file (or --password -)")
				return exitUsage
			}
			if pw, err = pr.askPasswordTwice(); err != nil {
				fmt.Fprintln(os.Stderr, "setup:", err)
				return exitFail
			}
		}
		if err := validPassword(pw); err != nil {
			fmt.Fprintln(os.Stderr, "setup:", err)
			return exitUsage
		}
		w.PasswordHash = passwordHash(w.User, pw)
	}

	// 3. existing file
	if _, err := os.Stat(*cfgPath); err == nil {
		if !*yes {
			ok, err := pr.askYesNo(*cfgPath+" exists; replace it (a backup is kept)", false)
			if err != nil || !ok {
				fmt.Println("nothing written")
				return exitFail
			}
		}
		bak := *cfgPath + setupBakStem + time.Now().Format("20060102-150405")
		if err := copyFile(*cfgPath, bak); err != nil {
			fmt.Fprintf(os.Stderr, "setup: backup %s: %v\n", bak, err)
			return exitFail
		}
		fmt.Printf("backup: %s\n", bak)
	}

	// 4. write
	raw := setupConfigText(p.Name(), chans, w)
	if _, warns, err := parseConfigErr(raw); err != nil || len(warns) != 0 {
		// The text may carry the password hash: only the redacted form leaves the process.
		fmt.Fprintf(os.Stderr, "setup: generated config does not validate (%v %v):\n%s", err, warns, redactConfigText(string(raw)))
		return exitFail
	}
	if err := saveConfig(*cfgPath, raw); err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		return exitFail
	}
	fmt.Printf("written: %s\n\n", *cfgPath)
	printSetupNext(w, p.Name())
	return exitOK
}

// setupChannels returns the channel set for a detected device: the
// verified N5 Pro set, nothing for monitor, one conservative channel per
// pwm for the generic chips (sensor: k10temp, else coretemp, else the
// first hwmon temperature).
func setupChannels(dev profile.Device, hw *hwmon.FS, f sensorFactory) []chanSpec {
	switch dev.Profile().Name() {
	case "n5pro":
		return n5proChannels()
	case "monitor":
		return nil
	}
	sensor := pickSetupSensor(hw, f)
	var out []chanSpec
	for _, ch := range dev.Channels() {
		out = append(out, chanSpec{
			Name:     "fan" + strconv.Itoa(ch.Index),
			PWM:      ch.Index,
			Sensor:   sensor,
			Curve:    append([][2]int(nil), genericCurve...),
			Critical: genericCritical,
			Stop:     "auto",
		})
	}
	return out
}

// pickSetupSensor prefers the CPU die sensors; otherwise the first hwmon
// device with a temp1_input, in hwmon order.
func pickSetupSensor(hw *hwmon.FS, f sensorFactory) string {
	for _, id := range []string{"k10temp", "coretemp"} {
		if _, err := f(id); err == nil {
			return id
		}
	}
	devs, err := listHwmon(hw)
	if err != nil {
		return "k10temp"
	}
	for _, d := range devs {
		if hw.Exists(d.Path + "/temp1_input") {
			return "hwmon:" + d.Name + ":temp1"
		}
	}
	return "k10temp"
}

// setupConfigText renders the config with a short header. The layout is
// config.Marshal's (no per-key comments; the example file documents them).
func setupConfigText(profileName string, chans []chanSpec, w webSpec) []byte {
	head := "# n5-fangov configuration, written by `n5-fangov setup` on " + time.Now().Format("2006-01-02 15:04") + "\n" +
		"# Reference with every key explained: /usr/share/doc/n5-fangov/config.example.toml\n" +
		"# (or deploy/config.example.toml in the checkout). Invalid values fall back to\n" +
		"# built-in defaults with a warning; the daemon always starts.\n\n"
	return append([]byte(head), renderConfig(profileName, chans, w)...)
}

func printSetupNext(w webSpec, profileName string) {
	fmt.Println("next:")
	fmt.Println("  n5-fangov check                      # what serve will do with this config")
	fmt.Println("  systemctl enable --now n5-fangov")
	fmt.Println("  n5-fangov status")
	if isLoopbackListen(w.Listen) {
		fmt.Printf("  web UI: http://%s (this machine only; from elsewhere: ssh -L %s:%s <host>)\n", w.Listen, setupPort, w.Listen)
	} else {
		fmt.Printf("  web UI: https://%s  user %s\n", w.Listen, w.User)
		fmt.Println("  the certificate is self-signed; trust it once:  n5-fangov cert export > n5-fangov.pem")
	}
	if profileName == "n5pro" {
		fmt.Println("  kernel updates: `n5-fangov check --after-update` runs from the apt hook and alerts when the DKMS module is missing")
	}
}

// primaryLANIP returns the first global-unicast IPv4 address of an
// interface that is up and not loopback (on Proxmox that is vmbr0).
func primaryLANIP() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	sort.Slice(ifs, func(i, j int) bool { return ifs[i].Index < ifs[j].Index })
	for _, it := range ifs {
		if it.Flags&net.FlagUp == 0 || it.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := it.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip != nil && ip.IsGlobalUnicast() {
				return ip.String()
			}
		}
	}
	return ""
}

// copyFile copies src to dst (mode 0600; the config may carry a hash).
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

// ---------------------------------------------------------------------------
// passwd

// cmdPasswd sets or replaces the web user and password in the config file
// and switches auth to basic. Comments and other keys of the file are
// kept. A restart applies it ([web] is read once at start).
func cmdPasswd(args []string) int {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	user := fs.String("user", "", "web user (default: the configured one, else "+setupAdmin+")")
	password := fs.String("password", "", passwordFlagHelp)
	passwordFile := fs.String("password-file", "", passwordFileFlagHelp)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov passwd [--user U] [--password-file F | --password -]")
		return exitUsage
	}
	pw, err := passwordFromArgs(*password, *passwordFile, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "passwd:", err)
		return exitUsage
	}
	raw, err := os.ReadFile(*cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = nil, nil
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "passwd:", err)
		return exitFail
	}
	cur, _ := parseConfig(raw)
	if _, _, perr := parseConfigErr(raw); perr != nil {
		fmt.Fprintf(os.Stderr, "passwd: %s has a syntax error, fix it first: %v\n", *cfgPath, perr)
		return exitFail
	}
	u := strings.TrimSpace(*user)
	if u != "" {
		if err := validUserName(u); err != nil {
			fmt.Fprintln(os.Stderr, "passwd:", err)
			return exitUsage
		}
	}
	if pw != "" {
		if err := validPassword(pw); err != nil {
			fmt.Fprintln(os.Stderr, "passwd:", err)
			return exitUsage
		}
	}
	if u == "" || pw == "" {
		pr := openPrompter()
		defer pr.close()
		if u == "" {
			def := webOf(cur).User
			if def == "" {
				def = setupAdmin
			}
			if u, err = pr.ask("web user", def); err != nil {
				return exitFail
			}
			if err := validUserName(u); err != nil {
				fmt.Fprintln(os.Stderr, "passwd:", err)
				return exitFail
			}
		}
		if pw == "" {
			if pw, err = pr.askPasswordTwice(); err != nil {
				fmt.Fprintln(os.Stderr, "passwd:", err)
				return exitFail
			}
			if err := validPassword(pw); err != nil {
				fmt.Fprintln(os.Stderr, "passwd:", err)
				return exitFail
			}
		}
	}
	raw = setWebAuth(raw, u, pw)
	if _, warns, err := parseConfigErr(raw); err != nil {
		fmt.Fprintln(os.Stderr, "passwd: result does not parse:", err)
		return exitFail
	} else {
		for _, w := range warns {
			fmt.Fprintln(os.Stderr, "passwd: warning:", w)
		}
	}
	if err := saveConfig(*cfgPath, raw); err != nil {
		fmt.Fprintln(os.Stderr, "passwd:", err)
		return exitFail
	}
	fmt.Printf("%s: auth = basic, user %q, password hash updated\n", *cfgPath, u)
	fmt.Println("apply with:  systemctl restart n5-fangov   ([web] is read at start)")
	return exitOK
}

// Password flag help: a password on the command line is visible in `ps`,
// the shell history and the journal, so --password only accepts "-"
// (stdin); a literal value is refused. --password-file reads a file.
const (
	passwordFlagHelp     = "\"-\" reads the password from stdin (one line). No literal value: it would be visible in ps/history; use --password-file or \"-\""
	passwordFileFlagHelp = "file whose first line is the password (mode 0600 recommended)"
)

// validPassword applies the account API's password length rule (runes):
// the CLI must not write what the dashboard would refuse.
func validPassword(pw string) error {
	if n := utf8.RuneCountInString(pw); n < passwordMinLen || n > passwordMaxLen {
		return fmt.Errorf("password must be %d..%d characters", passwordMinLen, passwordMaxLen)
	}
	return nil
}

// validUserName applies the account API's user name rule.
func validUserName(u string) error {
	if !userNameRe.MatchString(u) {
		return fmt.Errorf("user %q must match %s", u, userNameRe)
	}
	return nil
}

// passwordFromArgs resolves the password flags: --password-file wins, then
// --password "-" (one line from stdin). "" means "ask"; any other
// --password value is refused (it would sit in ps and the shell history).
func passwordFromArgs(literal, file string, stdin io.Reader) (string, error) {
	switch {
	case literal != "" && literal != "-":
		return "", errors.New("--password takes only \"-\" (read from stdin); a literal password is not accepted, use --password-file")
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("--password-file: %w", err)
		}
		pw := strings.TrimRight(firstLine(string(b)), "\r")
		if pw == "" {
			return "", fmt.Errorf("--password-file %s: first line is empty", file)
		}
		return pw, nil
	case literal == "-":
		r := bufio.NewReader(stdin)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("--password -: no line on stdin")
		}
		pw := strings.TrimRight(line, "\r\n")
		if pw == "" {
			return "", errors.New("--password -: empty line on stdin")
		}
		return pw, nil
	}
	return "", nil
}

// setWebAuth edits the three auth keys of [web] in place.
func setWebAuth(raw []byte, user, password string) []byte {
	raw = setConfigKey(raw, "web", "auth", tomlString("basic"))
	raw = setConfigKey(raw, "web", "user", tomlString(user))
	return setConfigKey(raw, "web", "password_hash", tomlString(passwordHash(user, password)))
}
