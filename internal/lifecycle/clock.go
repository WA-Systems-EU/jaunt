package lifecycle

import (
	"strings"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/config"
)

// parseHHMM parses a strict "HH:MM" into minutes since midnight.
func parseHHMM(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// Deadline returns the earlier of upAt+MaxUptime and the evening cutoff on
// upAt's local date in loc, with the reason "max-uptime" or "evening-cutoff".
// An unparsable cutoff is treated as no cutoff.
func Deadline(upAt time.Time, l config.Limits, loc *time.Location) (time.Time, string) {
	maxUp := upAt.Add(time.Duration(l.MaxUptime))
	mins, ok := parseHHMM(l.EveningCutoff)
	if !ok {
		return maxUp, "max-uptime"
	}
	y, m, d := upAt.In(loc).Date()
	cutoff := time.Date(y, m, d, mins/60, mins%60, 0, 0, loc)
	if cutoff.Before(maxUp) {
		return cutoff, "evening-cutoff"
	}
	return maxUp, "max-uptime"
}

// InNightHours reports whether t, in loc, falls in nightHours ("22:00-07:00",
// may wrap midnight). Unparsable input is treated as not night.
func InNightHours(t time.Time, nightHours string, loc *time.Location) bool {
	from, to, found := strings.Cut(nightHours, "-")
	if !found {
		return false
	}
	start, ok1 := parseHHMM(from)
	end, ok2 := parseHHMM(to)
	if !ok1 || !ok2 {
		return false
	}
	lt := t.In(loc)
	cur := lt.Hour()*60 + lt.Minute()
	if start <= end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end
}
