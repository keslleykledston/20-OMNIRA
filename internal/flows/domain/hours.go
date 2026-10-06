package domain

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // the runtime images are minimal; do not depend on the host zoneinfo
)

var weekdayKeys = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("time %q must be HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func ValidateBusinessHours(c BusinessHoursConfig) error {
	if _, err := time.LoadLocation(c.Timezone); err != nil || c.Timezone == "" {
		return fmt.Errorf("unknown timezone %q (use an IANA name such as America/Sao_Paulo)", c.Timezone)
	}
	if len(c.Windows) < 1 || len(c.Windows) > 14 {
		return fmt.Errorf("1 to 14 windows are required")
	}
	for _, w := range c.Windows {
		if len(w.Days) < 1 {
			return fmt.Errorf("a window needs at least one day")
		}
		for _, d := range w.Days {
			if _, ok := weekdayKeys[strings.ToLower(d)]; !ok {
				return fmt.Errorf("unknown day %q (mon..sun)", d)
			}
		}
		s, err := parseClock(w.Start)
		if err != nil {
			return err
		}
		e, err := parseClock(w.End)
		if err != nil {
			return err
		}
		if s >= e {
			return fmt.Errorf("window %s-%s: start must be before end", w.Start, w.End)
		}
	}
	return nil
}

// IsOpen reports whether now falls inside any window of the schedule, in the schedule's timezone.
func IsOpen(c BusinessHoursConfig, now time.Time) (bool, error) {
	if err := ValidateBusinessHours(c); err != nil {
		return false, err
	}
	loc, _ := time.LoadLocation(c.Timezone)
	local := now.In(loc)
	minutes := local.Hour()*60 + local.Minute()
	for _, w := range c.Windows {
		s, _ := parseClock(w.Start)
		e, _ := parseClock(w.End)
		for _, d := range w.Days {
			if weekdayKeys[strings.ToLower(d)] == local.Weekday() && minutes >= s && minutes < e {
				return true, nil
			}
		}
	}
	return false, nil
}
