// Package scheduling is the shared calendar of the sectors (ADR-0044, phase A): pure domain, no I/O. One place answers "is this sector
// open now?", "when does it open next?" and "when is a business-time deadline due?" for the routing, overflow, SLA and Flow Builder code,
// so none of them re-implements time rules.
//
// Time model. Instants are time.Time (stored as timestamptz). A rule is LOCAL: a weekday plus minutes-of-day in an IANA zone, never a
// fixed offset. A rule becomes instants only here, date by date, so daylight-saving changes are handled in one place:
//   - a local time that does not exist (spring forward) is pushed forward by the size of the gap (02:30 becomes 03:30);
//   - a local time that happens twice (fall back) means its FIRST occurrence;
//   - durations are always measured between instants, never by adding 24 h to a date.
//
// An interval belongs to the DAY IT STARTS ON. 22:00-06:00 on Friday is Friday's interval and ends on Saturday morning, whatever
// Saturday's own rule says (even if Saturday is a holiday): a shift is not cut in half by the calendar.
// Precedence for the intervals that START on a local date: exception > holiday > weekly rule.
package scheduling

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // the runtime images are minimal; do not depend on the host zoneinfo
)

type Kind string

const (
	// Kind24x7 is open all day every day (exceptions and holidays can still close days).
	Kind24x7 Kind = "24x7"
	// KindBusiness and KindCustom both use Weekly; Business is the label of the usual office-hours preset.
	KindBusiness Kind = "business"
	KindCustom   Kind = "custom"
)

// Horizon bounds every search ("next opening", "add business time"): a schedule that never opens in this span answers "never".
// The span is [t, t+Horizon): an opening exactly at t+Horizon is outside it, while a deadline computed by AddBusiness may land
// exactly on t+Horizon (the end of the last window it needed).
const Horizon = 366 * 24 * time.Hour

// DefaultTimezone is what the ADAPTERS (tenant defaults, the API) fill in when a tenant has not chosen a zone. The domain never
// guesses: Validate refuses an empty zone, so a missing zone is a decision somebody made, not an accident of this package.
const DefaultTimezone = "America/Manaus"

// lookbackDays: an interval belongs to the day it starts on and is shorter than 24 h, so it started at most one civil day before
// the instant asked about. Two covers zones that skipped a whole civil day (Pacific/Apia, 2011-12-30), where "the next day"
// of a shift started on the 29th is already the 31st.
const lookbackDays = 2

const minutesPerDay = 24 * 60

// Interval is [Start, End) in minutes from local midnight. End <= Start means the interval crosses midnight and ends on the next
// local day (22:00-06:00 is {1320, 360}); {0, 1440} is the whole day. Start must be 0..1439 and End 1..1440.
type Interval struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

func (i Interval) crossesMidnight() bool { return i.End <= i.Start }

// Clock builds an Interval from "HH:MM" strings ("24:00" is allowed as an end).
func Clock(start, end string) (Interval, error) {
	s, err := parseClock(start, false)
	if err != nil {
		return Interval{}, err
	}
	e, err := parseClock(end, true)
	if err != nil {
		return Interval{}, err
	}
	return Interval{Start: s, End: e}, nil
}

func parseClock(s string, allow24 bool) (int, error) {
	if allow24 && s == "24:00" {
		return minutesPerDay, nil
	}
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("time %q must be HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Date is a civil (zone-less) calendar date.
type Date struct {
	Year  int        `json:"year"`
	Month time.Month `json:"month"`
	Day   int        `json:"day"`
}

func ParseDate(s string) (Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return Date{}, fmt.Errorf("date %q must be YYYY-MM-DD", s)
	}
	return Date{t.Year(), t.Month(), t.Day()}, nil
}

func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day) }

// ordinal makes dates comparable without a zone.
func (d Date) ordinal() int { return d.Year*10000 + int(d.Month)*100 + d.Day }

func (d Date) valid() bool {
	t := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
	return t.Year() == d.Year && t.Month() == d.Month && t.Day() == d.Day
}

func (d Date) addDays(n int) Date {
	t := time.Date(d.Year, d.Month, d.Day+n, 0, 0, 0, 0, time.UTC)
	return Date{t.Year(), t.Month(), t.Day()}
}

// DayRule is the weekly rule of one weekday: the intervals that start on that weekday. No intervals = closed that weekday.
type DayRule struct {
	Weekday   time.Weekday `json:"weekday"`
	Intervals []Interval   `json:"intervals"`
}

// Exception overrides the intervals that start on each date From..To (inclusive). Closed (or no intervals) closes those days.
type Exception struct {
	Name      string     `json:"name,omitempty"`
	From      Date       `json:"from"`
	To        Date       `json:"to"`
	Closed    bool       `json:"closed"`
	Intervals []Interval `json:"intervals,omitempty"`
}

// Holiday is a one-day entry of the tenant's holiday calendar. Closed unless it names intervals (e.g. a half-day).
type Holiday struct {
	Name      string     `json:"name,omitempty"`
	Date      Date       `json:"date"`
	Intervals []Interval `json:"intervals,omitempty"`
}

// Suspension closes the sector between two INSTANTS regardless of every other rule (an emergency: outage, evacuation, strike). It is
// the strongest rule of the calendar and, unlike a holiday, it can start and end at any minute and may cut a running shift.
type Suspension struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Reason string    `json:"reason,omitempty"`
}

// Schedule is the agenda of one sector.
type Schedule struct {
	Kind       Kind        `json:"kind"`
	Timezone   string      `json:"timezone"` // IANA name, e.g. America/Manaus
	Weekly     []DayRule   `json:"weekly,omitempty"`
	Exceptions []Exception `json:"exceptions,omitempty"`
	Holidays   []Holiday   `json:"holidays,omitempty"`
	// Suspensions are applied last: what they cover is closed, whatever the weekly rule, holiday or exception says.
	Suspensions []Suspension `json:"suspensions,omitempty"`
}

// Business returns the usual preset: Monday to Friday, 09:00-18:00.
func Business(tz string) Schedule {
	var weekly []DayRule
	for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday} {
		weekly = append(weekly, DayRule{Weekday: d, Intervals: []Interval{{Start: 9 * 60, End: 18 * 60}}})
	}
	return Schedule{Kind: KindBusiness, Timezone: tz, Weekly: weekly}
}

// Always returns the 24x7 schedule.
func Always(tz string) Schedule { return Schedule{Kind: Kind24x7, Timezone: tz} }

const (
	maxIntervalsPerDay = 8
	maxExceptions      = 366
	maxHolidays        = 400
	maxSuspensions     = 200
	nameMax            = 80 // exception and holiday names (the columns hold 80)
)

// Validate checks everything the resolver relies on. A schedule that validates never makes the resolver fail.
func (s Schedule) Validate() error {
	if s.Timezone == "" {
		return errors.New("timezone is required (an IANA name such as America/Manaus)")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("unknown timezone %q (use an IANA name such as America/Manaus)", s.Timezone)
	}
	switch s.Kind {
	case Kind24x7:
		if len(s.Weekly) > 0 {
			return errors.New("a 24x7 schedule has no weekly rules (use exceptions or holidays to close days)")
		}
	case KindBusiness, KindCustom:
		if len(s.Weekly) == 0 {
			return errors.New("at least one weekday rule is required")
		}
	default:
		return fmt.Errorf("unknown schedule kind %q (24x7, business or custom)", s.Kind)
	}
	seenDay := map[time.Weekday]bool{}
	for _, d := range s.Weekly {
		if d.Weekday < time.Sunday || d.Weekday > time.Saturday {
			return fmt.Errorf("invalid weekday %d", d.Weekday)
		}
		if seenDay[d.Weekday] {
			return fmt.Errorf("weekday %s appears twice", d.Weekday)
		}
		seenDay[d.Weekday] = true
		if err := validateIntervals(d.Intervals); err != nil {
			return fmt.Errorf("%s: %w", d.Weekday, err)
		}
	}
	if len(s.Exceptions) > maxExceptions || len(s.Holidays) > maxHolidays {
		return errors.New("too many exceptions or holidays")
	}
	for i, e := range s.Exceptions {
		if len(e.Name) > nameMax {
			return fmt.Errorf("exception %d: name longer than %d characters", i+1, nameMax)
		}
		if !e.From.valid() || !e.To.valid() {
			return fmt.Errorf("exception %d: invalid date", i+1)
		}
		if e.To.ordinal() < e.From.ordinal() {
			return fmt.Errorf("exception %d: end date before start date", i+1)
		}
		if !e.Closed && len(e.Intervals) == 0 {
			return fmt.Errorf("exception %d: give intervals or mark it closed", i+1)
		}
		if e.Closed && len(e.Intervals) > 0 {
			return fmt.Errorf("exception %d: a closed exception has no intervals", i+1)
		}
		if err := validateIntervals(e.Intervals); err != nil {
			return fmt.Errorf("exception %d: %w", i+1, err)
		}
		for j := 0; j < i; j++ {
			o := s.Exceptions[j]
			if e.From.ordinal() <= o.To.ordinal() && o.From.ordinal() <= e.To.ordinal() {
				return fmt.Errorf("exceptions %d and %d overlap", j+1, i+1)
			}
		}
	}
	if len(s.Suspensions) > maxSuspensions {
		return errors.New("too many suspensions")
	}
	for i, sp := range s.Suspensions {
		if sp.From.IsZero() || sp.To.IsZero() || !sp.To.After(sp.From) {
			return fmt.Errorf("suspension %d: the end must be after the start", i+1)
		}
		if len(sp.Reason) > 200 {
			return fmt.Errorf("suspension %d: reason longer than 200 characters", i+1)
		}
	}
	seenHoliday := map[int]bool{}
	for i, h := range s.Holidays {
		if len(h.Name) > nameMax {
			return fmt.Errorf("holiday %d: name longer than %d characters", i+1, nameMax)
		}
		if !h.Date.valid() {
			return fmt.Errorf("holiday %d: invalid date", i+1)
		}
		if seenHoliday[h.Date.ordinal()] {
			return fmt.Errorf("holiday %s appears twice", h.Date)
		}
		seenHoliday[h.Date.ordinal()] = true
		if err := validateIntervals(h.Intervals); err != nil {
			return fmt.Errorf("holiday %s: %w", h.Date, err)
		}
	}
	return nil
}

func validateIntervals(in []Interval) error {
	if len(in) > maxIntervalsPerDay {
		return fmt.Errorf("at most %d intervals per day", maxIntervalsPerDay)
	}
	for _, i := range in {
		if i.Start < 0 || i.Start >= minutesPerDay || i.End < 1 || i.End > minutesPerDay {
			return fmt.Errorf("interval %s-%s is outside the day", fmtClock(i.Start), fmtClock(i.End))
		}
		if i.End == i.Start {
			return fmt.Errorf("interval %s-%s is empty (use 00:00-24:00 for a whole day)", fmtClock(i.Start), fmtClock(i.End))
		}
	}
	// intervals that START on one day may not overlap each other (an overnight one counts through its real end on the next day)
	type span struct{ s, e int }
	var spans []span
	for _, i := range in {
		e := i.End
		if i.crossesMidnight() {
			e += minutesPerDay
		}
		spans = append(spans, span{i.Start, e})
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a].s < spans[b].s })
	for k := 1; k < len(spans); k++ {
		if spans[k].s < spans[k-1].e {
			return errors.New("intervals of one day overlap")
		}
	}
	return nil
}

func fmtClock(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

// Window is an open span of instants, [Start, End).
type Window struct{ Start, End time.Time }

// Duration of the window in real time.
func (w Window) Duration() time.Duration { return w.End.Sub(w.Start) }

// intervalsStartingOn applies the precedence exception > holiday > weekly (> 24x7 whole day).
func (s Schedule) intervalsStartingOn(d Date) []Interval {
	for _, e := range s.Exceptions {
		if d.ordinal() >= e.From.ordinal() && d.ordinal() <= e.To.ordinal() {
			if e.Closed {
				return nil
			}
			return e.Intervals
		}
	}
	for _, h := range s.Holidays {
		if h.Date.ordinal() == d.ordinal() {
			return h.Intervals // none = closed
		}
	}
	if s.Kind == Kind24x7 {
		return []Interval{{Start: 0, End: minutesPerDay}}
	}
	wd := time.Date(d.Year, d.Month, d.Day, 12, 0, 0, 0, time.UTC).Weekday()
	for _, r := range s.Weekly {
		if r.Weekday == wd {
			return r.Intervals
		}
	}
	return nil
}

// localToInstant turns a civil date and minute-of-day into an instant with the daylight-saving rules of the package comment.
func localToInstant(loc *time.Location, d Date, minute int) time.Time {
	day := d
	if minute >= minutesPerDay {
		day, minute = d.addDays(1), minute-minutesPerDay
	}
	wall := time.Date(day.Year, day.Month, day.Day, minute/60, minute%60, 0, 0, time.UTC) // the wall clock, read as if it were UTC
	offBefore := offsetAt(loc, wall.Add(-48*time.Hour))
	offAfter := offsetAt(loc, wall.Add(48*time.Hour))
	c1 := wall.Add(-time.Duration(offBefore) * time.Second)
	c2 := wall.Add(-time.Duration(offAfter) * time.Second)
	ok1 := offsetAt(loc, c1) == offBefore
	ok2 := offsetAt(loc, c2) == offAfter
	switch {
	case ok1 && ok2: // the wall time happens twice: the first occurrence is the earlier instant
		if c1.Before(c2) {
			return c1
		}
		return c2
	case ok1:
		return c1
	case ok2:
		return c2
	default: // the wall time does not exist: push it forward by the size of the gap
		if c1.After(c2) {
			return c1
		}
		return c2
	}
}

func offsetAt(loc *time.Location, t time.Time) int {
	_, off := t.In(loc).Zone()
	return off
}

// Windows returns the open spans that intersect [from, to), merged and sorted. The range is limited to Horizon.
func (s Schedule) Windows(from, to time.Time) ([]Window, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if !to.After(from) {
		return nil, nil
	}
	if to.Sub(from) > Horizon+48*time.Hour {
		return nil, fmt.Errorf("range longer than %d days", int(Horizon.Hours()/24))
	}
	loc, _ := time.LoadLocation(s.Timezone)
	first := dateOf(from.In(loc)).addDays(-lookbackDays) // an interval that started on an earlier day may still be running
	last := dateOf(to.In(loc))
	var raw []Window
	for d := first; d.ordinal() <= last.ordinal(); d = d.addDays(1) {
		for _, i := range s.intervalsStartingOn(d) {
			start := localToInstant(loc, d, i.Start)
			var end time.Time
			if i.crossesMidnight() {
				end = localToInstant(loc, d.addDays(1), i.End)
			} else {
				end = localToInstant(loc, d, i.End)
			}
			if !end.After(start) {
				continue
			}
			if end.After(from) && start.Before(to) {
				raw = append(raw, Window{start, end})
			}
		}
	}
	return subtract(merge(clip(raw, from, to)), s.Suspensions), nil
}

// subtract removes the suspended spans from the open windows (windows are sorted and disjoint).
func subtract(ws []Window, cuts []Suspension) []Window {
	if len(cuts) == 0 {
		return ws
	}
	out := ws
	for _, c := range cuts {
		next := make([]Window, 0, len(out)+1)
		for _, w := range out {
			if !c.To.After(w.Start) || !c.From.Before(w.End) { // no overlap
				next = append(next, w)
				continue
			}
			if c.From.After(w.Start) {
				next = append(next, Window{w.Start, c.From})
			}
			if c.To.Before(w.End) {
				next = append(next, Window{c.To, w.End})
			}
		}
		out = next
	}
	return out
}

func dateOf(t time.Time) Date { return Date{t.Year(), t.Month(), t.Day()} }

func clip(ws []Window, from, to time.Time) []Window {
	out := make([]Window, 0, len(ws))
	for _, w := range ws {
		if w.Start.Before(from) {
			w.Start = from
		}
		if w.End.After(to) {
			w.End = to
		}
		if w.End.After(w.Start) {
			out = append(out, w)
		}
	}
	return out
}

func merge(ws []Window) []Window {
	if len(ws) == 0 {
		return nil
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Start.Before(ws[j].Start) })
	out := []Window{ws[0]}
	for _, w := range ws[1:] {
		last := &out[len(out)-1]
		if !w.Start.After(last.End) { // overlapping or touching (an overnight tail meeting the next day's first interval)
			if w.End.After(last.End) {
				last.End = w.End
			}
			continue
		}
		out = append(out, w)
	}
	return out
}

// IsOpen says whether the sector attends at instant t. The end of a window is exclusive.
func (s Schedule) IsOpen(t time.Time) (bool, error) {
	ws, err := s.Windows(t, t.Add(time.Nanosecond))
	if err != nil {
		return false, err
	}
	return len(ws) > 0, nil
}

// NextOpen returns the first instant >= t at which the sector is open (t itself when it is open now); ok=false when it does not
// open within the Horizon.
func (s Schedule) NextOpen(t time.Time) (time.Time, bool, error) {
	ws, err := s.Windows(t, t.Add(Horizon))
	if err != nil || len(ws) == 0 {
		return time.Time{}, false, err
	}
	return ws[0].Start, true, nil
}

// NextClose returns the end of the window that contains t; ok=false when the sector is closed at t. A window that runs through the
// whole Horizon (24x7) reports ok=false too: it does not close within the span that can be computed.
func (s Schedule) NextClose(t time.Time) (time.Time, bool, error) {
	ws, err := s.Windows(t, t.Add(Horizon))
	if err != nil || len(ws) == 0 || ws[0].Start.After(t) {
		return time.Time{}, false, err
	}
	if ws[0].End.Equal(t.Add(Horizon)) {
		return time.Time{}, false, nil
	}
	return ws[0].End, true, nil
}

// BusinessElapsed is the open time between a and b: what a business-time SLA counts. Zero when b <= a.
func (s Schedule) BusinessElapsed(a, b time.Time) (time.Duration, error) {
	if err := s.Validate(); err != nil { // an invalid schedule is an error even when the answer would be zero
		return 0, err
	}
	if !b.After(a) {
		return 0, nil
	}
	ws, err := s.Windows(a, b)
	if err != nil {
		return 0, err
	}
	var total time.Duration
	for _, w := range ws {
		total += w.Duration()
	}
	return total, nil
}

// AddBusiness returns the instant at which d of open time has elapsed since t (the deadline of a business-time SLA). ok=false when
// the schedule does not provide that much open time within the Horizon. d <= 0 returns t.
func (s Schedule) AddBusiness(t time.Time, d time.Duration) (time.Time, bool, error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, false, err
	}
	if d <= 0 {
		return t, true, nil
	}
	ws, err := s.Windows(t, t.Add(Horizon))
	if err != nil {
		return time.Time{}, false, err
	}
	left := d
	for _, w := range ws {
		if w.Duration() >= left {
			return w.Start.Add(left), true, nil
		}
		left -= w.Duration()
	}
	return time.Time{}, false, nil
}

// Describe is a one-line summary for logs and the audit trail (never a rule dump).
func (s Schedule) Describe() string {
	var b strings.Builder
	b.WriteString(string(s.Kind))
	b.WriteString(" @ ")
	b.WriteString(s.Timezone)
	return b.String()
}
