package scheduling

import (
	"fmt"
	"strings"
	"time"
)

// Spec is the API/storage shape of a Schedule: dates are "YYYY-MM-DD", times are "HH:MM", weekdays are "mon".."sun". Suspensions are
// separate rows (they are instants, created in the middle of the day), so they are not part of the spec.
type Spec struct {
	Kind       Kind            `json:"kind"`
	Timezone   string          `json:"timezone"`
	Weekly     []SpecDay       `json:"weekly,omitempty"`
	Exceptions []SpecException `json:"exceptions,omitempty"`
	Holidays   []SpecHoliday   `json:"holidays,omitempty"`
}

type SpecInterval struct {
	Start string `json:"start"`
	End   string `json:"end"` // "24:00" allowed
}

type SpecDay struct {
	Day       string         `json:"day"`
	Intervals []SpecInterval `json:"intervals"`
}

type SpecException struct {
	Name      string         `json:"name,omitempty"`
	From      string         `json:"from"`
	To        string         `json:"to"`
	Closed    bool           `json:"closed"`
	Intervals []SpecInterval `json:"intervals,omitempty"`
}

type SpecHoliday struct {
	Name      string         `json:"name,omitempty"`
	Date      string         `json:"date"`
	Intervals []SpecInterval `json:"intervals,omitempty"`
}

var dayKeys = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}
var dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func intervalsFrom(in []SpecInterval) ([]Interval, error) {
	out := make([]Interval, 0, len(in))
	for _, i := range in {
		v, err := Clock(i.Start, i.End)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func intervalsTo(in []Interval) []SpecInterval {
	out := make([]SpecInterval, 0, len(in))
	for _, i := range in {
		out = append(out, SpecInterval{Start: fmtClock(i.Start), End: fmtClock(i.End)})
	}
	return out
}

// Schedule converts the spec and validates the result: a spec that converts is a schedule the resolver accepts.
func (sp Spec) Schedule() (Schedule, error) {
	s := Schedule{Kind: sp.Kind, Timezone: sp.Timezone}
	for _, d := range sp.Weekly {
		wd, ok := dayKeys[strings.ToLower(d.Day)]
		if !ok {
			return Schedule{}, fmt.Errorf("unknown day %q (mon..sun)", d.Day)
		}
		iv, err := intervalsFrom(d.Intervals)
		if err != nil {
			return Schedule{}, err
		}
		s.Weekly = append(s.Weekly, DayRule{Weekday: wd, Intervals: iv})
	}
	for _, e := range sp.Exceptions {
		from, err := ParseDate(e.From)
		if err != nil {
			return Schedule{}, err
		}
		to, err := ParseDate(e.To)
		if err != nil {
			return Schedule{}, err
		}
		iv, err := intervalsFrom(e.Intervals)
		if err != nil {
			return Schedule{}, err
		}
		s.Exceptions = append(s.Exceptions, Exception{Name: e.Name, From: from, To: to, Closed: e.Closed, Intervals: iv})
	}
	for _, h := range sp.Holidays {
		d, err := ParseDate(h.Date)
		if err != nil {
			return Schedule{}, err
		}
		iv, err := intervalsFrom(h.Intervals)
		if err != nil {
			return Schedule{}, err
		}
		s.Holidays = append(s.Holidays, Holiday{Name: h.Name, Date: d, Intervals: iv})
	}
	return s, s.Validate()
}

// SpecOf is the inverse of Spec.Schedule (suspensions are not part of the spec).
func SpecOf(s Schedule) Spec {
	sp := Spec{Kind: s.Kind, Timezone: s.Timezone}
	for _, d := range s.Weekly {
		sp.Weekly = append(sp.Weekly, SpecDay{Day: dayNames[d.Weekday], Intervals: intervalsTo(d.Intervals)})
	}
	for _, e := range s.Exceptions {
		sp.Exceptions = append(sp.Exceptions, SpecException{Name: e.Name, From: e.From.String(), To: e.To.String(), Closed: e.Closed, Intervals: intervalsTo(e.Intervals)})
	}
	for _, h := range s.Holidays {
		sp.Holidays = append(sp.Holidays, SpecHoliday{Name: h.Name, Date: h.Date.String(), Intervals: intervalsTo(h.Intervals)})
	}
	return sp
}
