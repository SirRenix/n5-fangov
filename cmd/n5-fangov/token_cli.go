package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func init() {
	register("token", command{run: cmdToken})
}

// cmdToken manages the API tokens through the daemon (the daemon owns
// tokens.json; the socket caller may manage them):
//
//	token create NAME [--scope read|control|admin] [--ttl DAYS]   prints the secret once (stdout only)
//	token list                                                    table: id, name, scope, created, expires, last used, last address
//	token revoke ID
//
// Without a running daemon every form exits 1 with the hint: the file
// is not edited from here (the daemon would not see the change until a
// restart, and two writers would race).
func cmdToken(args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov token create NAME [--scope read|control|admin] [--ttl DAYS] | list | revoke ID")
		return exitUsage
	}
	if len(args) == 0 {
		return usage()
	}
	c := tokenClient{dir: runDir()}
	switch args[0] {
	case "create":
		return c.create(args[1:], usage)
	case "list":
		if len(args) != 1 {
			return usage()
		}
		return c.list()
	case "revoke":
		if len(args) != 2 {
			return usage()
		}
		return c.revoke(args[1])
	}
	return usage()
}

type tokenClient struct {
	dir string // run dir (socket)
}

// tokenNoDaemonHint is the exit-1 text when the socket does not answer.
const tokenNoDaemonHint = "daemon not running? tokens are managed by the daemon (systemctl start n5-fangov), or from the dashboard's Account dialog"

// fail prints the error; a missing daemon gets the hint.
func (c tokenClient) fail(what string, err error) int {
	if errors.Is(err, errNoDaemon) {
		fmt.Fprintf(os.Stderr, "token %s: %v — %s\n", what, err, tokenNoDaemonHint)
	} else {
		fmt.Fprintf(os.Stderr, "token %s: %v\n", what, err)
	}
	return exitFail
}

// tokenRow is one entry of GET /api/tokens.
type tokenRow struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Scope    string     `json:"scope"`
	Created  time.Time  `json:"created"`
	Expires  *time.Time `json:"expires"`
	LastUsed *time.Time `json:"last_used"`
	LastIP   string     `json:"last_ip"`
	Expired  bool       `json:"expired"`
}

func (c tokenClient) create(args []string, usage func() int) int {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	scope := fs.String("scope", "read", "token scope: read | control | admin")
	ttl := fs.Int("ttl", 90, "days until expiry (0 = never)")
	// the name may precede or follow the flags
	var name string
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		name, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return usage()
	}
	if name == "" {
		return usage()
	}
	switch *scope {
	case "read", "control", "admin":
	default:
		fmt.Fprintln(os.Stderr, "token create: --scope must be read, control or admin")
		return exitUsage
	}
	if *ttl < 0 {
		fmt.Fprintln(os.Stderr, "token create: --ttl must be 0 or more days")
		return exitUsage
	}
	var resp struct {
		Token   string     `json:"token"`
		ID      string     `json:"id"`
		Name    string     `json:"name"`
		Scope   string     `json:"scope"`
		Expires *time.Time `json:"expires"`
		Warning string     `json:"warning"`
	}
	body := map[string]any{"name": name, "scope": *scope, "ttl_days": *ttl}
	if err := newAPI(c.dir).do("POST", "/api/tokens", body, &resp); err != nil {
		return c.fail("create", err)
	}
	// stdout carries the secret alone (so `n5-fangov token create x` can
	// be captured); everything else goes to stderr.
	fmt.Println(resp.Token)
	fmt.Fprintf(os.Stderr, "token %q created (id %s, scope %s, expires %s); the secret is shown only now\n", resp.Name, resp.ID, resp.Scope, fmtExpiry(resp.Expires))
	if resp.Warning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", resp.Warning)
	}
	return exitOK
}

func (c tokenClient) list() int {
	var resp struct {
		Tokens []tokenRow `json:"tokens"`
	}
	if err := newAPI(c.dir).get("/api/tokens", &resp); err != nil {
		return c.fail("list", err)
	}
	if len(resp.Tokens) == 0 {
		fmt.Println("no API tokens (create one: n5-fangov token create NAME)")
		return exitOK
	}
	fmt.Printf("%-8s  %-32s  %-7s  %-16s  %-16s  %-16s  %s\n", "id", "name", "scope", "created", "expires", "last used", "last address")
	for _, t := range resp.Tokens {
		exp := fmtExpiry(t.Expires)
		if t.Expired {
			exp += " (expired)"
		}
		fmt.Printf("%-8s  %-32s  %-7s  %-16s  %-16s  %-16s  %s\n", t.ID, t.Name, t.Scope, fmtLocal(&t.Created), exp, fmtLocal(t.LastUsed), t.LastIP)
	}
	return exitOK
}

func (c tokenClient) revoke(id string) int {
	var resp struct {
		Revoked string `json:"revoked"`
	}
	if err := newAPI(c.dir).do("DELETE", "/api/tokens/"+id, nil, &resp); err != nil {
		return c.fail("revoke", err)
	}
	fmt.Printf("token %s revoked\n", resp.Revoked)
	return exitOK
}

// fmtExpiry renders an expiry, "never" for none.
func fmtExpiry(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "never"
	}
	return fmtLocal(t)
}

// fmtLocal renders a time in local "2006-01-02 15:04", "-" for none.
func fmtLocal(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}
