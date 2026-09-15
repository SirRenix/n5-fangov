package main

import (
	"flag"
	"fmt"
	"log"
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
// for an unspecified address, the primary IPv4 and IPv6 address of the
// machine), the host name (short and FQDN), "localhost"/127.0.0.1 and
// [web].allowed_hosts without the "*" wildcard. Sorted, deduplicated, so
// the same input yields the same list (tlscert regenerates when SANs
// change — which is why a wildcard listen does not pull in every address
// of every interface (M5): a VM bridge or a container network coming and
// going would churn the certificate).
//
// A wildcard listen that yields no address at all is logged (H3): the
// certificate then covers only the host name and loopback, and a browser
// reaching the box by IP sees a name mismatch.
func tlsHosts(w webSpec) []string {
	set := map[string]bool{"localhost": true, "127.0.0.1": true}
	host, _, err := net.SplitHostPort(w.Listen)
	if err == nil {
		ip := net.ParseIP(host)
		switch {
		case host != "" && (ip == nil || !ip.IsUnspecified()):
			set[host] = true
		default:
			addrs := primaryIPsFn()
			if len(addrs) == 0 {
				log.Printf("web: listen %s is unspecified and no primary IPv4/IPv6 address could be determined; the TLS certificate covers the host name and loopback only", w.Listen)
			}
			for _, a := range addrs {
				set[a] = true
			}
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

// primaryIPsFn is primaryIPs, replaceable in tests.
var primaryIPsFn = primaryIPs

// primaryIPs returns the source addresses the kernel would use towards a
// public IPv4 and IPv6 destination: a UDP "connect" (no packet is sent)
// and the local address of the resulting socket. That is the address the
// default route leaves through — the one a LAN client reaches the box by.
// Interface enumeration is the fallback (and its failure the H3 case: it
// needs AF_NETLINK, which the unit now allows).
func primaryIPs() []string {
	var out []string
	for _, dst := range []string{"1.1.1.1:53", "[2606:4700::1111]:53"} {
		if a := routeSource(dst); a != "" {
			out = append(out, a)
		}
	}
	if len(out) > 0 {
		return out
	}
	addrs, err := localIPs()
	if err != nil {
		log.Printf("web: interface enumeration failed: %v (AF_NETLINK blocked in the unit?)", err)
		return nil
	}
	return addrs
}

// routeSource is the local address a UDP socket to dst binds to, "" when
// there is no route (offline, no IPv6).
func routeSource(dst string) string {
	c, err := net.Dial("udp", dst)
	if err != nil {
		return ""
	}
	defer c.Close()
	ua, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || ua.IP == nil || !ua.IP.IsGlobalUnicast() {
		return ""
	}
	return ua.IP.String()
}

// localIPs lists the global unicast addresses of every interface that is
// up; the first IPv4 and the first IPv6 only, to keep the SAN set stable.
func localIPs() ([]string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	sort.Slice(ifs, func(i, j int) bool { return ifs[i].Index < ifs[j].Index })
	var v4, v6 string
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
			if !ok || !ipn.IP.IsGlobalUnicast() {
				continue
			}
			if ipn.IP.To4() != nil {
				if v4 == "" {
					v4 = ipn.IP.String()
				}
			} else if v6 == "" {
				v6 = ipn.IP.String()
			}
		}
	}
	var out []string
	for _, a := range []string{v4, v6} {
		if a != "" {
			out = append(out, a)
		}
	}
	return out, nil
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
