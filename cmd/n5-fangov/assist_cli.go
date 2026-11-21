package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/SirRenix/n5-fangov/internal/control"
)

func init() {
	register("assist", command{run: cmdAssist})
}

// cmdAssist switches a channel's [[assist]] table through the daemon
// (PUT /api/assist/{channel}); the daemon writes the enabled line and
// takes the switch on its next cycle. Without an answering socket nothing
// is written: the file is the daemon's to edit while it runs.
func cmdAssist(args []string) int {
	if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
		fmt.Fprintln(os.Stderr, "usage: n5-fangov assist <channel> on|off")
		return exitUsage
	}
	ch := args[0]
	if !channelNameRe.MatchString(ch) {
		fmt.Fprintf(os.Stderr, "assist: channel name %q must match %s\n", ch, channelNameRe)
		return exitUsage
	}
	on := args[1] == "on"
	dir := runDir()
	err := newAPI(dir).do("PUT", "/api/assist/"+url.PathEscape(ch), map[string]bool{"enabled": on}, nil)
	switch {
	case errors.Is(err, errNoDaemon):
		fmt.Fprintf(os.Stderr, "assist: the daemon does not answer on %s; the switch goes through the running daemon (nothing written). Start n5-fangov, or set enabled in the [[assist]] table of %s by hand.\n", socketPath(dir), defaultConfigPath)
		return exitFail
	case err != nil:
		fmt.Fprintln(os.Stderr, "assist:", err)
		return exitFail
	}
	fmt.Printf("%s: assist %s (config written, applied on the next cycle)\n", ch, args[1])
	return exitOK
}

// assistCell is the status column for one channel: "-" without a table,
// "off", "+45 amdgpu" while a boost is added, "0" when on without one.
func assistCell(a *control.AssistState) string {
	switch {
	case a == nil:
		return "-"
	case !a.Enabled:
		return "off"
	case a.Boost > 0:
		return fmt.Sprintf("+%d %s", a.Boost, control.AssistLabel(a.Source))
	}
	return "0"
}
