package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/tlscert"
)

// cmdCert manages the dashboard certificate from the shell, mirroring the
// dashboard panel and the /api/tls endpoints:
//
//	cert info                     mode, subject, SANs, validity, fingerprint
//	cert export [--der] [FILE]    certificate only (PEM, or DER with --der)
//	cert regen [--new-key]        reissue the automatic certificate (key kept unless --new-key)
//	cert upload CERT KEY          install an own PEM pair (tls = "file")
//	cert reset                    back to the automatic certificate
//
// With the daemon running the calls go through the unix socket and take
// effect at once (hot swap); without it the same manager code works on
// the files directly and a restart applies the change.
func cmdCert(args []string) int {
	fs := flag.NewFlagSet("cert", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file (the certificate lives next to it in tls/)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	usage := func() int {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov cert info | export [--der] [FILE] | regen [--new-key] | upload CERT KEY | reset")
		return exitUsage
	}
	if fs.NArg() < 1 {
		return usage()
	}
	sub, rest := fs.Arg(0), fs.Args()[1:]
	c := certClient{cfgPath: *cfgPath, dir: runDir()}
	switch sub {
	case "info":
		if len(rest) != 0 {
			return usage()
		}
		return c.info()
	case "export":
		efs := flag.NewFlagSet("cert export", flag.ContinueOnError)
		der := efs.Bool("der", false, "DER (.cer) instead of PEM")
		if err := efs.Parse(rest); err != nil || efs.NArg() > 1 {
			return usage()
		}
		return c.export(*der, efs.Arg(0))
	case "regen":
		rfs := flag.NewFlagSet("cert regen", flag.ContinueOnError)
		newKey := rfs.Bool("new-key", false, "generate a new private key (imported trust breaks)")
		if err := rfs.Parse(rest); err != nil || rfs.NArg() != 0 {
			return usage()
		}
		return c.regen(!*newKey)
	case "upload":
		if len(rest) != 2 {
			return usage()
		}
		return c.upload(rest[0], rest[1])
	case "reset":
		if len(rest) != 0 {
			return usage()
		}
		return c.reset()
	}
	return usage()
}

// certClient runs one cert subcommand against the daemon (socket) or,
// when it does not answer, against the files through tlsManager.
type certClient struct {
	cfgPath string
	dir     string // run dir (socket)
}

// errNoCertYet: tls = "auto" but the pair has not been created (first
// start pending). regen and upload proceed, info and export cannot.
var errNoCertYet = errors.New("no certificate yet (it is created at the first start with tls = \"auto\")")

// offline builds the manager from the config file for the no-daemon path.
// The manager comes back even when the current pair does not load (the
// error says why): info and export need a loaded pair, reset, upload and
// regen do not — they are the repair. offlineRepair is their variant.
func (c certClient) offline() (*tlsManager, error) {
	cfg, warns, _ := loadConfig(c.cfgPath)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "config: %s\n", w)
	}
	w := webOf(cfg)
	m := newTLSManager(c.cfgPath, w, tlsHosts(w))
	if w.TLS == "off" {
		return m, nil
	}
	if _, err := m.load(false); err != nil {
		if w.TLS == "auto" && errors.Is(err, os.ErrNotExist) {
			return m, fmt.Errorf("%w in %s", errNoCertYet, m.dir)
		}
		return m, err
	}
	return m, nil
}

// tlsResp is GET /api/tls and the POST answers.
type tlsResp struct {
	Mode     string            `json:"mode"`
	Info     *tlscert.InfoData `json:"info"`
	Hosts    []string          `json:"hosts"`
	Warnings []string          `json:"warnings"`
	Warning  string            `json:"warning"`
	Fallback bool              `json:"fallback"`
}

func (c certClient) info() int {
	var resp tlsResp
	err := newAPI(c.dir).get("/api/tls", &resp)
	switch {
	case err == nil:
		if resp.Info == nil {
			fmt.Printf("mode: %s (source: daemon)\n", resp.Mode)
			return exitOK
		}
		printCertInfo(resp.Mode, "daemon", *resp.Info, resp.Hosts)
		printWarnings(resp.Warnings)
		if resp.Fallback {
			fmt.Println("note: the configured file pair could not be loaded; the automatic certificate is served (see the daemon log). Repair: cert upload CERT KEY or cert reset")
		}
		return exitOK
	case !errors.Is(err, errNoDaemon):
		fmt.Fprintln(os.Stderr, "cert info:", err)
		return exitFail
	}
	m, err := c.offline()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cert info:", err)
		return exitFail
	}
	info, mode, err := m.Info()
	if mode == "off" {
		fmt.Println("mode: off (source: config file)")
		return exitOK
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cert info:", err)
		return exitFail
	}
	printCertInfo(mode, "files", info, m.hosts)
	return exitOK
}

func (c certClient) export(der bool, file string) int {
	path := "/api/tls/cert.crt"
	if der {
		path = "/api/tls/cert.cer"
	}
	data, err := newAPI(c.dir).raw(path)
	if err != nil && errors.Is(err, errNoDaemon) {
		var m *tlsManager
		if m, err = c.offline(); err == nil {
			switch {
			case m.Mode() == "off":
				err = errors.New("tls is off")
			case der:
				data, err = m.ExportDER()
			default:
				data, err = m.ExportPEM()
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cert export:", err)
		return exitFail
	}
	if file == "" || file == "-" {
		os.Stdout.Write(data)
		return exitOK
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "cert export:", err)
		return exitFail
	}
	fmt.Printf("certificate written to %s\n", file)
	return exitOK
}

// offlineRepair is offline for the commands that replace the pair: a pair
// that cannot be loaded is reported as a note, not an error, and the
// manager is returned for the repair.
func (c certClient) offlineRepair(cmd string) *tlsManager {
	m, err := c.offline()
	if err != nil && !errors.Is(err, errNoCertYet) {
		fmt.Fprintf(os.Stderr, "%s: note: current certificate not loadable (%v); continuing\n", cmd, err)
	}
	return m
}

func (c certClient) regen(keepKey bool) int {
	var resp tlsResp
	_, err := newAPI(c.dir).doRaw("POST", "/api/tls/regenerate", "application/json", []byte(fmt.Sprintf(`{"keep_key":%v}`, keepKey)), &resp)
	if err == nil {
		fmt.Println("certificate regenerated, served from the next connection on (daemon)")
		if resp.Warning != "" {
			fmt.Println("note:", resp.Warning)
		}
		if resp.Info != nil {
			printCertInfo("auto", "daemon", *resp.Info, nil)
		}
		return exitOK
	}
	if !errors.Is(err, errNoDaemon) {
		fmt.Fprintln(os.Stderr, "cert regen:", err)
		return exitFail
	}
	m := c.offlineRepair("cert regen")
	info, kept, err := m.Regenerate(keepKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cert regen:", err)
		return exitFail
	}
	fmt.Printf("new certificate in %s\napply with:  systemctl restart n5-fangov\n", m.dir)
	switch {
	case !keepKey:
		fmt.Println("note: new private key — download and trust the certificate again where it was imported")
	case !kept:
		fmt.Println("note: the stored private key could not be reused, a new pair was generated — download and trust the certificate again where it was imported")
	}
	printCertInfo("auto", "files", info, m.hosts)
	return exitOK
}

func (c certClient) upload(certFile, keyFile string) int {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cert upload:", err)
		return exitFail
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cert upload:", err)
		return exitFail
	}
	body, _ := json.Marshal(map[string]string{"cert": string(certPEM), "key": string(keyPEM)})
	var resp tlsResp
	_, err = newAPI(c.dir).doRaw("POST", "/api/tls/upload", "application/json", body, &resp)
	if err == nil {
		fmt.Println("certificate installed (tls = \"file\"), served from the next connection on (daemon)")
		printWarnings(resp.Warnings)
		if resp.Info != nil {
			printCertInfo("file", "daemon", *resp.Info, nil)
		}
		return exitOK
	}
	if !errors.Is(err, errNoDaemon) {
		fmt.Fprintln(os.Stderr, "cert upload:", err)
		return exitFail
	}
	m := c.offlineRepair("cert upload")
	if m.Mode() == "off" {
		fmt.Fprintln(os.Stderr, "cert upload: tls is off in", c.cfgPath)
		return exitFail
	}
	info, warns, err := m.Upload(certPEM, keyPEM)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cert upload:", err)
		return exitFail
	}
	fmt.Printf("certificate installed in %s, config set to tls = \"file\"\napply with:  systemctl restart n5-fangov\n", m.dir)
	printWarnings(warns)
	printCertInfo("file", "files", info, m.hosts)
	return exitOK
}

func (c certClient) reset() int {
	var resp tlsResp
	_, err := newAPI(c.dir).doRaw("POST", "/api/tls/reset", "", nil, &resp)
	if err == nil {
		fmt.Println("back to the automatic certificate, served from the next connection on (daemon)")
		if resp.Info != nil {
			printCertInfo("auto", "daemon", *resp.Info, nil)
		}
		return exitOK
	}
	if !errors.Is(err, errNoDaemon) {
		fmt.Fprintln(os.Stderr, "cert reset:", err)
		return exitFail
	}
	m := c.offlineRepair("cert reset")
	if m.Mode() == "off" {
		fmt.Fprintln(os.Stderr, "cert reset: tls is off in", c.cfgPath)
		return exitFail
	}
	info, err := m.ResetAuto()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cert reset:", err)
		return exitFail
	}
	fmt.Printf("config set to tls = \"auto\", uploaded pair removed\napply with:  systemctl restart n5-fangov\n")
	printCertInfo("auto", "files", info, m.hosts)
	return exitOK
}

func printWarnings(w []string) {
	for _, s := range w {
		fmt.Println("warning:", s)
	}
}

// printCertInfo renders InfoData like the dashboard panel.
func printCertInfo(mode, source string, i tlscert.InfoData, hosts []string) {
	fmt.Printf("mode:         %s (source: %s)\n", mode, source)
	fmt.Printf("subject:      %s\n", i.Subject)
	fmt.Printf("issuer:       %s\n", i.Issuer)
	fmt.Printf("SANs:         %s\n", strings.Join(append(append([]string{}, i.DNSNames...), i.IPs...), ", "))
	left := time.Until(i.NotAfter)
	expiry := i.NotAfter.Format("2006-01-02")
	switch {
	case left < 0:
		expiry += "  EXPIRED"
	case left < tlscert.ExpiresSoon:
		expiry += fmt.Sprintf("  (expires in %d days)", tlscert.DaysLeft(i.NotAfter, time.Now()))
	}
	fmt.Printf("valid:        %s .. %s\n", i.NotBefore.Format("2006-01-02"), expiry)
	ca := ""
	if i.IsCA {
		ca = "  (CA flag: usable as trust anchor)"
	}
	fmt.Printf("key:          %s%s\n", i.KeyAlgo, ca)
	fmt.Printf("serial:       %s\n", i.SerialHex)
	fmt.Printf("SHA-256:      %s\n", i.FingerprintSHA256)
	if len(hosts) > 0 {
		fmt.Printf("listen hosts: %s\n", strings.Join(hosts, ", "))
	}
}
