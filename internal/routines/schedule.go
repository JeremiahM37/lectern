// Package routines turns a job you keep asking for into one you can press a
// button for, or schedule.
package routines

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseSchedule reads the small set of schedule forms lectern understands and
// returns the next time a routine should run after `from`.
//
// Deliberately not cron. Cron's five fields are famously easy to get subtly
// wrong, and a schedule that silently means something other than you intended is
// worse than one that refuses to parse — especially for a routine that opens
// pull requests. These forms cover what a homelab actually needs and each one
// reads as what it does:
//
//	every 30m / every 6h / every 2d   — a fixed interval
//	hourly                            — the top of each hour
//	daily at 09:00                    — a wall-clock time each day
//	daily                             — same, at midnight
//	weekly on mon at 08:30            — a wall-clock time on one weekday
//
// Times are local to the control plane, which is the clock you think in when you
// say "every morning".
func ParseSchedule(spec string, from time.Time) (time.Time, error) {
	s := strings.ToLower(strings.TrimSpace(spec))
	if s == "" {
		return time.Time{}, fmt.Errorf("no schedule")
	}

	if rest, ok := strings.CutPrefix(s, "every "); ok {
		d, err := parseInterval(strings.TrimSpace(rest))
		if err != nil {
			return time.Time{}, err
		}
		return from.Add(d), nil
	}

	if s == "hourly" {
		return from.Truncate(time.Hour).Add(time.Hour), nil
	}

	if rest, ok := strings.CutPrefix(s, "daily"); ok {
		h, m, err := parseAt(rest)
		if err != nil {
			return time.Time{}, err
		}
		next := time.Date(from.Year(), from.Month(), from.Day(), h, m, 0, 0, from.Location())
		if !next.After(from) {
			next = next.AddDate(0, 0, 1)
		}
		return next, nil
	}

	if rest, ok := strings.CutPrefix(s, "weekly on "); ok {
		fields := strings.SplitN(strings.TrimSpace(rest), " ", 2)
		day, ok := weekdays[fields[0]]
		if !ok {
			return time.Time{}, fmt.Errorf("unknown day %q", fields[0])
		}
		var atPart string
		if len(fields) > 1 {
			atPart = fields[1]
		}
		h, m, err := parseAt(atPart)
		if err != nil {
			return time.Time{}, err
		}
		next := time.Date(from.Year(), from.Month(), from.Day(), h, m, 0, 0, from.Location())
		for next.Weekday() != day || !next.After(from) {
			next = next.AddDate(0, 0, 1)
		}
		return next, nil
	}

	return time.Time{}, fmt.Errorf("cannot read schedule %q — try "+
		`"every 6h", "hourly", "daily at 09:00", or "weekly on mon at 08:30"`, spec)
}

// Valid reports whether a schedule string is one lectern can act on. An empty
// schedule is valid and means the routine only runs when you press the button.
func Valid(spec string) error {
	if strings.TrimSpace(spec) == "" {
		return nil
	}
	_, err := ParseSchedule(spec, time.Now())
	return err
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

// parseInterval reads "30m", "6h", "2d".
func parseInterval(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf(`"every" needs an interval, e.g. "every 6h"`)
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(strings.TrimSpace(s[:len(s)-1]))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not an interval like 30m, 6h or 2d", s)
	}
	switch unit {
	case 'm':
		// a routine dispatches real agents; running one every few seconds is
		// never what someone meant to ask for
		if n < 5 {
			return 0, fmt.Errorf("the shortest interval is 5m")
		}
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unknown unit %q — use m, h or d", string(unit))
}

// parseAt reads the optional " at HH:MM" tail, defaulting to midnight.
func parseAt(rest string) (int, int, error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return 0, 0, nil
	}
	clock, ok := strings.CutPrefix(rest, "at ")
	if !ok {
		return 0, 0, fmt.Errorf("expected \"at HH:MM\", got %q", rest)
	}
	h, m, ok := strings.Cut(strings.TrimSpace(clock), ":")
	if !ok {
		return 0, 0, fmt.Errorf("expected HH:MM, got %q", clock)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("%q is not a time of day", clock)
	}
	return hh, mm, nil
}
