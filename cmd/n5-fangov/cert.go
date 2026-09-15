package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func init() {
	register("cert", command{run: cmdCert})
}

// tlsDir is where the auto certificate lives: <config dir>/tls (inside
// ReadWritePaths of the unit).
func tlsDir(cfgPath string) string { return filepath.Join(filepath.Dir(cfgPath), setupTLSDir) }

// tlsHosts returns the SANs for the auto certificate: the listen host (or,
// for an unspecified address, every global unicast IP of the machine), the
// host name (short and FQDN), "localhost"/127.0.0.1 and [web].allowed_hosts
// without the "*" wildcard. Sorted, deduplicated, so the same input yields
// the same list (tlscert regenerates when SANs change).
func tlsHosts(w webSpec) []string {
	set := map[string]bool{"localhost": true, "127.0.0.1": true}
	host, _, err := net.SplitHostPort(w.Listen)
	if err == nil && host != "" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsUnspecified() {
			set[host] = true
		} else {
			for _, a := range localIPs() {
				set[a] = true
			}
		}
	} else if err == nil {
		for _, a := range localIPs() {
			set[a] = true
		}
	}
	if hn, err := os.Hostname(); err == nil && hn != "" {
		set[hn] = true
		if i := strings.IndexByte(hn, '.'); i > 0 {
			set[hn[:i]] = true
		}
	}
	for _, h := range w.AllowedHosts {
		h = strings.TrimSpace(h)
		if h != "" && h != "*" {
			if hh, _, err := net.SplitHostPort(h); err == nil {
				h = hh
			}
			set[h] = true
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// localIPs lists the global unicast addresses of every interface that is up.
func localIPs() []string {
	var out []string
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, it := range ifs {
		if it.Flags&net.FlagUp == 0 || it.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := it.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.IsGlobalUnicast() {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}

// cmdCert: `cert export [FILE]` prints the auto certificate (PEM) so it
// can be trusted in a browser or OS store; `cert regen` replaces it (new
// key, same SANs) — restart the daemon afterwards.
func cmdCert(args []string) int {
	fs := flag.NewFlagSet("cert", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath, "config file (the certificate lives next to it in tls/)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	usage := func() int {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov cert export [FILE]   |   n5-fangov cert regen")
		return exitUsage
	}
	if fs.NArg() < 1 {
		return usage()
	}
	dir := tlsDir(*cfgPath)
	switch fs.Arg(0) {
	case "export":
		if fs.NArg() > 2 {
			return usage()
		}
		pem, err := tlsExportPEM(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cert export: %v\n(the certificate is created at the first start with [web].tls = \"auto\" on a non-loopback listen)\n", err)
			return exitFail
		}
		if fs.NArg() == 1 || fs.Arg(1) == "-" {
			os.Stdout.Write(pem)
			return exitOK
		}
		if err := os.WriteFile(fs.Arg(1), pem, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "cert export:", err)
			return exitFail
		}
		fmt.Printf("certificate written to %s\n", fs.Arg(1))
		return exitOK
	case "regen":
		if fs.NArg() != 1 {
			return usage()
		}
		cfg, warns, _ := loadConfig(*cfgPath)
		for _, w := range warns {
			fmt.Fprintf(os.Stderr, "config: %s\n", w)
		}
		hosts := tlsHosts(webOf(cfg))
		if _, err := tlsRegenerate(dir, hosts, setupOrg); err != nil {
			fmt.Fprintln(os.Stderr, "cert regen:", err)
			return exitFail
		}
		fmt.Printf("new certificate in %s for %s\napply with:  systemctl restart n5-fangov\n", dir, strings.Join(hosts, ", "))
		return exitOK
	}
	return usage()
}
