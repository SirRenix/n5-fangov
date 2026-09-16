package history

import (
	"encoding/csv"
	"io"
	"strconv"
	"time"
)

// CSV writes pts as comma-separated text: header
// ts,time,<ch>_temp,<ch>_duty,<ch>_rpm,…,<extra id>,… with the channels in
// the given order and the extra ids as given; `time` is RFC 3339 in the
// local zone, an absent value is an empty cell. Ids that contain commas
// (composite sensors) are quoted by the encoder.
func CSV(w io.Writer, pts []Point, channels []string, extras []string) error {
	cw := csv.NewWriter(w)
	header := []string{"ts", "time"}
	for _, ch := range channels {
		header = append(header, ch+"_temp", ch+"_duty", ch+"_rpm")
	}
	header = append(header, extras...)
	if err := cw.Write(header); err != nil {
		return err
	}
	row := make([]string, 0, len(header))
	for _, p := range pts {
		row = row[:0]
		row = append(row, strconv.FormatInt(p.TS, 10), time.Unix(p.TS, 0).Format(time.RFC3339))
		for _, ch := range channels {
			row = append(row, floatCell(p.Temp, ch), intCell(p.Duty, ch), intCell(p.RPM, ch))
		}
		for _, id := range extras {
			row = append(row, floatCell(p.Extra, id))
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func floatCell(m map[string]float64, k string) string {
	v, ok := m[k]
	if !ok {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func intCell(m map[string]int, k string) string {
	v, ok := m[k]
	if !ok {
		return ""
	}
	return strconv.Itoa(v)
}
