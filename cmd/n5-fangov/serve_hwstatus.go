package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/SirRenix/n5-fangov/internal/hwstatus"
)

// hwStatusFileName is the record of the hardware status at the last start,
// in the state dir: "<profile>\t<status>\t<key>\n".
const hwStatusFileName = "hwstatus.last"

// logHWStatusChange logs the hardware status of this start (decision 9,
// audit 2026-10-16). A status without a TESTSPEC result (experimental,
// unknown) is a journal warning only when it differs from the one recorded
// at the last start (profile, status or the board/BIOS/EC key changed, or
// no record yet); an unchanged restart logs it at info level, so a restart
// does not add a warning line to the journal (F8b K3). Without a state dir
// nothing can be compared: info level, with that note. The record is
// rewritten whenever it differs, also for a verified status.
func logHWStatusChange(stateDir, profileName string, hs hwstatus.Result) {
	cur := profileName + "\t" + string(hs.Status) + "\t" + hs.Key
	prev, havePrev := "", false
	var path string
	if stateDir != "" {
		path = filepath.Join(stateDir, hwStatusFileName)
		if b, err := os.ReadFile(path); err == nil {
			prev, havePrev = strings.TrimRight(string(b), "\n"), true
		}
	}
	changed := stateDir != "" && (!havePrev || prev != cur)
	if !hs.Status.Verified() {
		what := "hardware status " + string(hs.Status) + " for " + hs.Key + ": no TESTSPEC result for this board/BIOS/EC firmware"
		switch {
		case stateDir == "":
			log.Printf("%s (no state dir: change since the last start not tracked)", what)
		case changed && havePrev:
			log.Printf("%sWARNING: %s (changed since the last start, was: %s)", logWarning, what, strings.ReplaceAll(prev, "\t", " "))
		case changed:
			log.Printf("%sWARNING: %s (first start with this record)", logWarning, what)
		default:
			log.Printf("%s (unchanged since the last start)", what)
		}
	}
	if !changed {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(cur+"\n"), 0o644); err != nil {
		log.Printf("hardware status record %s: %v (the next start warns again)", path, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("hardware status record %s: %v (the next start warns again)", path, err)
	}
}
