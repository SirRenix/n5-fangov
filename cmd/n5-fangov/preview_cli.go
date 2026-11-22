package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/SirRenix/n5-fangov/internal/control"
)

func init() {
	register("preview", command{run: cmdPreview})
}

// assistFlags collects repeated --assist CH=on|off.
type assistFlags map[string]bool

func (a assistFlags) String() string { return "" }
func (a assistFlags) Set(v string) error {
	ch, state, ok := strings.Cut(v, "=")
	if !ok || !channelNameRe.MatchString(ch) || (state != "on" && state != "off") {
		return fmt.Errorf("want CHANNEL=on|off, got %q", v)
	}
	a[ch] = state == "on"
	return nil
}

// cmdPreview asks the daemon for the curve preview (POST /api/preview)
// and prints one line per channel; --json prints the API answer. The
// preview needs the daemon's history: without an answering socket it fails
// and reads no files itself.
func cmdPreview(args []string) int {
	fs := flag.NewFlagSet("preview", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	preset := fs.String("preset", "", "compute the channels of this preset")
	span := fs.String("span", "24h", "24h or 7d")
	asJSON := fs.Bool("json", false, "print the API answer")
	assist := assistFlags{}
	fs.Var(assist, "assist", "CHANNEL=on|off, the assist switch for the computation (repeatable)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || (*span != "24h" && *span != "7d") || (*preset != "" && !validPresetName(*preset)) {
		if err != nil {
			fmt.Fprintln(os.Stderr, "preview:", err)
		}
		fmt.Fprintln(os.Stderr, "usage: n5-fangov preview [--preset NAME] [--assist CH=on|off]... [--span 24h|7d] [--json]")
		return exitUsage
	}
	body := map[string]any{"span": *span}
	if *preset != "" {
		body["preset"] = *preset
	}
	if len(assist) > 0 {
		body["assist"] = map[string]bool(assist)
	}
	raw, _ := json.Marshal(body)
	dir := runDir()
	var out json.RawMessage
	_, err := newAPI(dir).doRaw("POST", "/api/preview", "application/json", raw, &out)
	switch {
	case errors.Is(err, errNoDaemon):
		fmt.Fprintf(os.Stderr, "preview: the daemon does not answer on %s; the preview is computed by the running daemon from its history (no files are read here)\n", socketPath(dir))
		return exitFail
	case err != nil:
		fmt.Fprintln(os.Stderr, "preview:", err)
		return exitFail
	}
	if *asJSON {
		fmt.Println(string(out))
		return exitOK
	}
	var res control.PreviewResult
	if err := json.Unmarshal(out, &res); err != nil {
		fmt.Fprintln(os.Stderr, "preview: unexpected answer:", err)
		return exitFail
	}
	printPreview(os.Stdout, res)
	return exitOK
}

// printPreview renders the table of the spec: actual → preview per channel.
func printPreview(w io.Writer, res control.PreviewResult) {
	fmt.Fprintf(w, "preview over %s (one point per %d s)\n\n", res.Span, res.Step)
	fmt.Fprintln(w, "channel  actual Ø/P95/max   preview Ø/P95/max   min>=200   assist min")
	var measured []string
	for _, name := range res.Order {
		c := res.Channels[name]
		if c.Error != "" || c.Summary == nil {
			fmt.Fprintf(w, "%-8s %s\n", name, "no preview: "+c.Error)
		} else {
			a, p := c.Summary.Actual, c.Summary.Preview
			fmt.Fprintf(w, "%-8s %4.0f / %3d / %3d   %4.0f / %3d / %3d   %4d → %-4d %4d → %d\n",
				name, a.Mean, a.P95, a.Max, p.Mean, p.P95, p.Max, a.Min200, p.Min200, a.AssistMin, p.AssistMin)
		}
		for _, n := range c.Notes {
			fmt.Fprintf(w, "         note: %s\n", n)
		}
		if c.Error != "" || c.Summary == nil {
			continue
		}
		if c.Measured != "" {
			measured = append(measured, name+": "+c.Measured)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, res.Note)
	for _, m := range measured {
		fmt.Fprintln(w, m)
	}
}
