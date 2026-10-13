package scheduling

import (
	"strings"
	"testing"
	"time"
)

func at(t *testing.T, zone, local string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation("2006-01-02 15:04", local, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustClock(t *testing.T, a, b string) Interval {
	t.Helper()
	i, err := Clock(a, b)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func day(wd time.Weekday, in ...Interval) DayRule { return DayRule{Weekday: wd, Intervals: in} }

func date(t *testing.T, s string) Date {
	t.Helper()
	d, err := ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func open(t *testing.T, s Schedule, when time.Time) bool {
	t.Helper()
	ok, err := s.IsOpen(when)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// "Financeiro fora do expediente": Monday to Friday 08:00-12:00 and 14:00-18:00 in Manaus.
func TestBusinessHoursWithTwoIntervalsAndEdges(t *testing.T) {
	z := "America/Manaus"
	var weekly []DayRule
	for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday} {
		weekly = append(weekly, day(d, mustClock(t, "08:00", "12:00"), mustClock(t, "14:00", "18:00")))
	}
	s := Schedule{Kind: KindCustom, Timezone: z, Weekly: weekly}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		local string
		want  bool
	}{
		{"2026-10-12 07:59", false}, {"2026-10-12 08:00", true}, {"2026-10-12 11:59", true}, {"2026-10-12 12:00", false},
		{"2026-10-12 13:59", false}, {"2026-10-12 14:00", true}, {"2026-10-12 17:59", true}, {"2026-10-12 18:00", false},
		{"2026-10-10 10:00", false}, // Saturday
		{"2026-10-11 10:00", false}, // Sunday
	} {
		if got := open(t, s, at(t, z, c.local)); got != c.want {
			t.Errorf("%s open=%v, want %v", c.local, got, c.want)
		}
	}
	// Friday 18:30 -> next opening is Monday 08:00
	next, ok, err := s.NextOpen(at(t, z, "2026-10-09 18:30"))
	if err != nil || !ok || !next.Equal(at(t, z, "2026-10-12 08:00")) {
		t.Errorf("next open = %v %v %v", next, ok, err)
	}
	// open now: NextOpen is now, NextClose is the end of the current interval
	now := at(t, z, "2026-10-12 09:30")
	if n, ok, _ := s.NextOpen(now); !ok || !n.Equal(now) {
		t.Errorf("open now: next open must be now, got %v", n)
	}
	if c, ok, _ := s.NextClose(now); !ok || !c.Equal(at(t, z, "2026-10-12 12:00")) {
		t.Errorf("next close = %v %v", c, ok)
	}
	if _, ok, _ := s.NextClose(at(t, z, "2026-10-12 13:00")); ok {
		t.Error("closed at 13:00: there is no window to close")
	}
}

// "Suporte 24x7": always open, even with nobody online (that is presence, not the calendar).
func TestAlwaysIsOpenEveryInstantAndHolidaysCanStillCloseADay(t *testing.T) {
	s := Always("America/Manaus")
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"2026-10-12 00:00", "2026-10-12 03:33", "2026-10-11 23:59", "2026-12-25 12:00"} {
		if !open(t, s, at(t, "America/Manaus", l)) {
			t.Errorf("24x7 must be open at %s", l)
		}
	}
	if _, ok, _ := s.NextClose(at(t, "America/Manaus", "2026-10-12 10:00")); ok {
		t.Error("24x7 never closes within the horizon")
	}
	s.Holidays = []Holiday{{Name: "Natal", Date: date(t, "2026-12-25")}}
	if open(t, s, at(t, "America/Manaus", "2026-12-25 12:00")) {
		t.Error("a closed holiday closes even a 24x7 schedule")
	}
	if !open(t, s, at(t, "America/Manaus", "2026-12-26 00:00")) || !open(t, s, at(t, "America/Manaus", "2026-12-24 23:59")) {
		t.Error("the day before and after stay open")
	}
	if (Schedule{Kind: Kind24x7, Timezone: "America/Manaus", Weekly: []DayRule{day(time.Monday, mustClock(t, "08:00", "09:00"))}}).Validate() == nil {
		t.Error("a 24x7 schedule with weekly rules is ambiguous and must be refused")
	}
}

// Turnos atravessando a meia-noite: the interval belongs to the day it starts on.
func TestOvernightShiftBelongsToTheDayItStartsOn(t *testing.T) {
	z := "America/Sao_Paulo"
	// Friday and Saturday nights 22:00-06:00 (a night team); nothing else
	s := Schedule{Kind: KindCustom, Timezone: z, Weekly: []DayRule{
		day(time.Friday, mustClock(t, "22:00", "06:00")), day(time.Saturday, mustClock(t, "22:00", "06:00"))}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		local string
		want  bool
	}{
		{"2026-10-09 21:59", false}, {"2026-10-09 22:00", true}, {"2026-10-09 23:59", true},
		{"2026-10-10 00:00", true}, {"2026-10-10 05:59", true}, {"2026-10-10 06:00", false}, // Saturday morning is Friday's tail
		{"2026-10-10 12:00", false}, {"2026-10-10 22:00", true}, {"2026-10-11 05:59", true}, // Saturday night into Sunday
		{"2026-10-11 06:00", false}, {"2026-10-11 23:00", false}, // Sunday has no rule
		{"2026-10-12 03:00", false}, // Monday early: Sunday has no shift, so closed
	} {
		if got := open(t, s, at(t, z, c.local)); got != c.want {
			t.Errorf("%s open=%v, want %v", c.local, got, c.want)
		}
	}
	// the tail merges with the next day's own interval into one continuous window
	ws, err := Schedule{Kind: KindCustom, Timezone: z, Weekly: []DayRule{
		day(time.Friday, mustClock(t, "22:00", "06:00")), day(time.Saturday, mustClock(t, "06:00", "12:00"))}}.Windows(at(t, z, "2026-10-09 00:00"), at(t, z, "2026-10-11 00:00"))
	if err != nil || len(ws) != 1 || !ws[0].Start.Equal(at(t, z, "2026-10-09 22:00")) || !ws[0].End.Equal(at(t, z, "2026-10-10 12:00")) {
		t.Errorf("touching intervals must merge into one window: %v %v", ws, err)
	}
	if c, ok, _ := s.NextClose(at(t, z, "2026-10-09 23:00")); !ok || !c.Equal(at(t, z, "2026-10-10 06:00")) {
		t.Errorf("close of the overnight shift = %v %v", c, ok)
	}
}

// Feriados e exceções.
func TestHolidaysAndExceptionsPrecedence(t *testing.T) {
	z := "America/Manaus"
	s := Business(z)
	s.Holidays = []Holiday{
		{Name: "Independência", Date: date(t, "2026-09-07")},                                                  // Monday: closed
		{Name: "Véspera", Date: date(t, "2026-12-24"), Intervals: []Interval{mustClock(t, "09:00", "12:00")}}, // Thursday: half day
	}
	s.Exceptions = []Exception{
		{Name: "Mutirão", From: date(t, "2026-10-10"), To: date(t, "2026-10-10"), Intervals: []Interval{mustClock(t, "09:00", "13:00")}}, // opens a Saturday
		{Name: "Recesso", From: date(t, "2026-12-28"), To: date(t, "2026-12-31"), Closed: true},
		{Name: "Feriado com exceção", From: date(t, "2026-11-02"), To: date(t, "2026-11-02"), Intervals: []Interval{mustClock(t, "10:00", "11:00")}},
	}
	s.Holidays = append(s.Holidays, Holiday{Name: "Finados", Date: date(t, "2026-11-02")}) // exception wins over holiday
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		local string
		want  bool
	}{
		{"2026-09-07 10:00", false}, {"2026-09-08 10:00", true},
		{"2026-12-24 10:00", true}, {"2026-12-24 13:00", false},
		{"2026-10-10 10:00", true}, {"2026-10-10 14:00", false}, {"2026-10-17 10:00", false},
		{"2026-12-29 10:00", false}, {"2027-01-04 10:00", true},
		{"2026-11-02 10:30", true}, {"2026-11-02 14:00", false},
	} {
		if got := open(t, s, at(t, z, c.local)); got != c.want {
			t.Errorf("%s open=%v, want %v", c.local, got, c.want)
		}
	}
	// a holiday does not cut the tail of the shift that started the evening before
	n := Schedule{Kind: KindCustom, Timezone: z, Weekly: []DayRule{day(time.Sunday, mustClock(t, "22:00", "06:00"))},
		Holidays: []Holiday{{Date: date(t, "2026-09-07")}}} // Monday holiday
	if !open(t, n, at(t, z, "2026-09-07 03:00")) {
		t.Error("Sunday night shift runs until Monday 06:00 even though Monday is a holiday")
	}
}

// Horário de verão: spring forward / fall back in New York, and a zone without DST for contrast.
func TestDaylightSavingTime(t *testing.T) {
	ny := "America/New_York"
	night := Schedule{Kind: KindCustom, Timezone: ny, Weekly: allDays(day(time.Sunday, mustClock(t, "01:00", "04:00")))}
	// 2026-03-08 (Sunday): clocks jump 02:00 -> 03:00, so 01:00-04:00 local is only 2 real hours
	el, err := night.BusinessElapsed(at(t, ny, "2026-03-08 00:00"), at(t, ny, "2026-03-09 00:00"))
	if err != nil || el != 2*time.Hour {
		t.Errorf("spring-forward day: %v %v, want 2h", el, err)
	}
	// 2026-11-01 (Sunday): 01:00-02:00 happens twice, so 01:00-04:00 local is 4 real hours
	el, err = night.BusinessElapsed(at(t, ny, "2026-11-01 00:00").UTC().Add(-time.Hour), at(t, ny, "2026-11-02 00:00"))
	if err != nil || el != 4*time.Hour {
		t.Errorf("fall-back day: %v %v, want 4h", el, err)
	}
	// an ordinary day for the same rule: 3h
	el, _ = night.BusinessElapsed(at(t, ny, "2026-10-04 00:00"), at(t, ny, "2026-10-05 00:00"))
	if el != 3*time.Hour {
		t.Errorf("ordinary Sunday: %v, want 3h", el)
	}
	// office hours keep their wall-clock meaning across the change: 09:00 local is always open, 08:59 never
	office := Schedule{Kind: KindCustom, Timezone: ny, Weekly: allDays(day(time.Sunday, mustClock(t, "09:00", "17:00")))}
	for _, d := range []string{"2026-03-01", "2026-03-08", "2026-03-15", "2026-11-01", "2026-11-08"} {
		if !open(t, office, at(t, ny, d+" 09:00")) || open(t, office, at(t, ny, d+" 08:59")) || open(t, office, at(t, ny, d+" 17:00")) {
			t.Errorf("%s: wall-clock edges moved across the DST change", d)
		}
	}
	// a start inside the gap (02:30 does not exist) is pushed forward to 03:30; a start in the repeated hour means the first 01:30
	gap := Schedule{Kind: KindCustom, Timezone: ny, Weekly: allDays(day(time.Sunday, mustClock(t, "02:30", "05:00")))}
	ws, _ := gap.Windows(at(t, ny, "2026-03-08 00:00"), at(t, ny, "2026-03-09 00:00"))
	if len(ws) != 1 || !ws[0].Start.Equal(time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC)) { // 03:30 EDT = 07:30Z
		t.Errorf("gap start: %v", ws)
	}
	rep := Schedule{Kind: KindCustom, Timezone: ny, Weekly: allDays(day(time.Sunday, mustClock(t, "01:30", "05:00")))}
	ws, _ = rep.Windows(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	if len(ws) != 1 || !ws[0].Start.Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) { // first 01:30 = EDT = 05:30Z
		t.Errorf("repeated-hour start must be the first occurrence: %v", ws)
	}
	// Manaus has no DST: a 9h day is 9h all year
	mn := Business("America/Manaus")
	for _, d := range []string{"2026-03-09", "2026-11-02"} {
		e, _ := mn.BusinessElapsed(at(t, "America/Manaus", d+" 00:00"), at(t, "America/Manaus", d+" 23:59"))
		if e != 9*time.Hour {
			t.Errorf("Manaus %s: %v", d, e)
		}
	}
}

func allDays(r DayRule) []DayRule { return []DayRule{r} }

// SLA em calendário útil.
func TestBusinessDeadlineSkipsClosedTimeWeekendsAndHolidays(t *testing.T) {
	z := "America/Manaus"
	s := Business(z)
	s.Holidays = []Holiday{{Date: date(t, "2026-10-12")}} // Monday holiday
	// Friday 16:00 + 4 business hours: 2h left Friday, Monday is a holiday, Tuesday 09:00 + 2h = 11:00
	due, ok, err := s.AddBusiness(at(t, z, "2026-10-09 16:00"), 4*time.Hour)
	if err != nil || !ok || !due.Equal(at(t, z, "2026-10-13 11:00")) {
		t.Errorf("due = %v %v %v", due, ok, err)
	}
	// started outside the hours: the clock starts at the next opening
	due, ok, _ = s.AddBusiness(at(t, z, "2026-10-09 20:00"), 30*time.Minute)
	if !ok || !due.Equal(at(t, z, "2026-10-13 09:30")) {
		t.Errorf("due from closed time = %v %v", due, ok)
	}
	// elapsed counts only open time, and the same span is shorter than the wall clock
	el, _ := s.BusinessElapsed(at(t, z, "2026-10-09 17:00"), at(t, z, "2026-10-13 10:00"))
	if el != 2*time.Hour {
		t.Errorf("elapsed = %v, want 2h", el)
	}
	if d, ok, _ := s.AddBusiness(at(t, z, "2026-10-09 10:00"), 0); !ok || !d.Equal(at(t, z, "2026-10-09 10:00")) {
		t.Error("zero duration returns the start")
	}
	// a schedule that never gives that much open time within the horizon answers "no"
	if _, ok, _ := s.AddBusiness(at(t, z, "2026-10-09 10:00"), 10000*time.Hour); ok {
		t.Error("beyond the horizon must report ok=false")
	}
}

func TestValidation(t *testing.T) {
	z := "America/Manaus"
	good := Business(z)
	bad := map[string]Schedule{
		"no timezone":      {Kind: KindBusiness, Weekly: good.Weekly},
		"unknown timezone": {Kind: KindBusiness, Timezone: "Mars/Olympus", Weekly: good.Weekly},
		"unknown kind":     {Kind: "weird", Timezone: z, Weekly: good.Weekly},
		"no weekly":        {Kind: KindCustom, Timezone: z},
		"duplicate day":    {Kind: KindCustom, Timezone: z, Weekly: []DayRule{day(time.Monday, Interval{540, 600}), day(time.Monday, Interval{700, 800})}},
		"empty interval":   {Kind: KindCustom, Timezone: z, Weekly: []DayRule{day(time.Monday, Interval{540, 540})}},
		"overlap":          {Kind: KindCustom, Timezone: z, Weekly: []DayRule{day(time.Monday, Interval{540, 700}, Interval{600, 800})}},
		"overnight overlap": {Kind: KindCustom, Timezone: z, Weekly: []DayRule{
			day(time.Monday, Interval{1320, 360}, Interval{1380, 1430})}},
		"out of day": {Kind: KindCustom, Timezone: z, Weekly: []DayRule{day(time.Monday, Interval{-1, 100})}},
		"exception overlap": {Kind: KindBusiness, Timezone: z, Weekly: good.Weekly, Exceptions: []Exception{
			{From: Date{2026, 10, 1}, To: Date{2026, 10, 5}, Closed: true}, {From: Date{2026, 10, 5}, To: Date{2026, 10, 6}, Closed: true}}},
		"exception reversed": {Kind: KindBusiness, Timezone: z, Weekly: good.Weekly, Exceptions: []Exception{{From: Date{2026, 10, 5}, To: Date{2026, 10, 1}, Closed: true}}},
		"exception empty":    {Kind: KindBusiness, Timezone: z, Weekly: good.Weekly, Exceptions: []Exception{{From: Date{2026, 10, 5}, To: Date{2026, 10, 5}}}},
		"holiday twice":      {Kind: KindBusiness, Timezone: z, Weekly: good.Weekly, Holidays: []Holiday{{Date: Date{2026, 9, 7}}, {Date: Date{2026, 9, 7}}}},
		"impossible date":    {Kind: KindBusiness, Timezone: z, Weekly: good.Weekly, Holidays: []Holiday{{Date: Date{2026, 2, 30}}}},
	}
	for name, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: must be refused", name)
		}
		if _, err := s.IsOpen(time.Now()); err == nil {
			t.Errorf("%s: the resolver must not run on an invalid schedule", name)
		}
	}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := Clock("25:00", "26:00"); err == nil {
		t.Error("bad clock accepted")
	}
	if _, err := ParseDate("07/09/2026"); err == nil {
		t.Error("bad date accepted")
	}
	if _, err := good.Windows(time.Now(), time.Now().Add(3*Horizon)); err == nil || !strings.Contains(err.Error(), "range") {
		t.Errorf("an absurd range must be refused: %v", err)
	}
	// a whole-day and an overnight interval are valid shapes
	ok := Schedule{Kind: KindCustom, Timezone: z, Weekly: []DayRule{day(time.Monday, Interval{0, 1440}), day(time.Tuesday, Interval{1320, 360})}}
	if err := ok.Validate(); err != nil {
		t.Errorf("whole day / overnight shapes: %v", err)
	}
}

// The resolver is deterministic and never mutates the schedule it was given.
func TestResolverIsPureAndRepeatable(t *testing.T) {
	z := "America/Sao_Paulo"
	s := Business(z)
	s.Holidays = []Holiday{{Date: date(t, "2026-11-20")}}
	before := s.Describe()
	a, _ := s.Windows(at(t, z, "2026-11-01 00:00"), at(t, z, "2026-12-01 00:00"))
	b, _ := s.Windows(at(t, z, "2026-11-01 00:00"), at(t, z, "2026-12-01 00:00"))
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("windows differ between runs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !a[i].Start.Equal(b[i].Start) || !a[i].End.Equal(b[i].End) {
			t.Fatal("windows differ between runs")
		}
	}
	if s.Describe() != before || len(s.Holidays) != 1 {
		t.Error("the schedule changed")
	}
	// 30 days of Mon-Fri 09-18 minus one holiday: 21 weekdays in Nov 2026 -> 20 open days of 9h
	total, _ := s.BusinessElapsed(at(t, z, "2026-11-01 00:00"), at(t, z, "2026-12-01 00:00"))
	if total != 20*9*time.Hour {
		t.Errorf("November total = %v, want %v", total, 20*9*time.Hour)
	}
}
