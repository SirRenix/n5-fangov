package validate

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/n5driver"
)

// Replay evaluates T2/T3 from raw logs of the pre-TESTSPEC measurement of
// 2026-09-14 (n5pro-ec befunde/raw/06-pwmtest-ch*.txt and
// 06-i2ec-dcr-*.txt): the last duty the pwm test wrote, then the DCR
// snapshots taken after the hand-back. The logs carry no timestamp of the
// auto command and only one direction (no H/M pair), so the result can at
// best show that the channel left the written value; it never reaches PASS.

// PWMTestLog is what Replay needs from a 06-pwm-test log.
type PWMTestLog struct {
	Channel  int
	Started  time.Time // from the header line
	LastDuty int       // the last "--- pwmN = V" step
	HandBack bool      // the log contains the restore to EC automatic
}

var (
	pwmHeaderRe = regexp.MustCompile(`^=== 06-pwm-test\s+Kanal (\d+)\s+(\S+) ===`)
	pwmStepRe   = regexp.MustCompile(`^--- pwm(\d+) = (\d+)\b`)
	sectionRe   = regexp.MustCompile(`^(\d{1,2}):(\d{2})(?:-\d{1,2}:\d{2})?\s+(.*)$`)
)

// ParsePWMTest reads a 06-pwm-test log.
func ParsePWMTest(s string) (PWMTestLog, error) {
	var l PWMTestLog
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := pwmHeaderRe.FindStringSubmatch(line); m != nil {
			l.Channel, _ = strconv.Atoi(m[1])
			if t, err := time.Parse(time.RFC3339, m[2]); err == nil {
				l.Started = t
			}
			continue
		}
		if m := pwmStepRe.FindStringSubmatch(line); m != nil {
			ch, _ := strconv.Atoi(m[1])
			v, _ := strconv.Atoi(m[2])
			if l.Channel == 0 || ch == l.Channel {
				l.Channel, l.LastDuty = ch, v
			}
			continue
		}
		if strings.Contains(line, "Wiederherstellung") || strings.Contains(line, "_enable=2") {
			l.HandBack = true
		}
	}
	if l.Channel == 0 || l.LastDuty == 0 {
		return l, fmt.Errorf("replay: pwm test log without channel or duty step")
	}
	if !l.HandBack {
		return l, fmt.Errorf("replay: pwm test log without the hand-back to EC automatic")
	}
	return l, nil
}

// I2ECSnapshot is one register dump of a section of the I2EC log.
type I2ECSnapshot struct {
	Clock   string // "17:20"
	Minutes int    // minutes since midnight
	DCR     n5driver.DCR
}

// ParseI2EC reads the DCR snapshots of a hand log: sections start with
// "HH:MM <text>", dump lines are "18xx: hex bytes". Sections from the first
// one that records a new write (text contains "manuell", "manual" or
// "aktiv") on are ignored: from there the channel no longer shows the
// hand-back.
func ParseI2EC(s string) ([]I2ECSnapshot, error) {
	var out []I2ECSnapshot
	clock, minutes, stopped := "", -1, false
	var cur []string
	flush := func() error {
		if len(cur) == 0 || minutes < 0 {
			cur = nil
			return nil
		}
		for _, l := range cur {
			d, err := n5driver.ParseDCR(l)
			if err != nil {
				continue // a dump line outside DCR1..5 (e.g. 0x1810)
			}
			out = append(out, I2ECSnapshot{Clock: clock, Minutes: minutes, DCR: d})
		}
		cur = nil
		return nil
	}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			if err := flush(); err != nil {
				return nil, err
			}
			low := strings.ToLower(m[3])
			if strings.Contains(low, "manuell") || strings.Contains(low, "manual") || strings.Contains(low, "aktiv") {
				stopped = true
			}
			h, _ := strconv.Atoi(m[1])
			mi, _ := strconv.Atoi(m[2])
			clock, minutes = m[1]+":"+m[2], h*60+mi
			continue
		}
		if stopped {
			continue
		}
		if a, _, ok := strings.Cut(line, ":"); ok && len(a) == 4 && strings.HasPrefix(a, "18") {
			if i := strings.IndexByte(line, '('); i >= 0 {
				line = line[:i] // "1802: 00 55 4a 57 57 8c   (unveraendert, ...)"
			}
			cur = append(cur, line)
		}
	}
	_ = flush()
	if len(out) == 0 {
		return nil, fmt.Errorf("replay: no DCR snapshot in the I2EC log")
	}
	return out, nil
}

// Replay builds T2/T3 for the channel of the pwm test log from the DCR
// snapshots. The channel's DCR (pwm3 -> DCR3) is compared with the last
// written duty; sample times are minutes since the first snapshot, because
// the log has no timestamp of the auto command (TimingKnown false).
func Replay(pwmLog, i2ecLog string) ([]Result, error) {
	p, err := ParsePWMTest(pwmLog)
	if err != nil {
		return nil, err
	}
	snaps, err := ParseI2EC(i2ecLog)
	if err != nil {
		return nil, err
	}
	run := HandbackRun{Label: "replay", Written: p.LastDuty}
	t0 := snaps[0].Minutes
	for _, s := range snaps {
		v, ok := s.DCR.Channel(p.Channel)
		if !ok {
			return nil, fmt.Errorf("replay: channel pwm%d has no DCR", p.Channel)
		}
		run.Samples = append(run.Samples, DCRSample{T: float64(s.Minutes-t0) * 60, DCR: v})
	}
	sub := EvalHandback(run)
	t2, t3 := AggregateHandback(p.Channel, [][]SubResult{{sub}}, []string{"H", "M"})
	note := fmt.Sprintf("replay of raw logs (pwm test started %s, last written duty %d, %d DCR snapshot(s) %s..%s, time of the auto command not logged)",
		p.Started.Format(time.RFC3339), p.LastDuty, len(snaps), snaps[0].Clock, snaps[len(snaps)-1].Clock)
	t2.Reasons = append([]string{note}, t2.Reasons...)
	t3.Reasons = append([]string{note}, t3.Reasons...)
	return []Result{t2, t3}, nil
}
