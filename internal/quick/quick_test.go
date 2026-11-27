package quick

import (
	"testing"
	"time"
)

// A time with no day is the next time the clock shows it, in the person's
// time zone: "Dentist 3pm" typed at 5pm is tomorrow, not two hours ago. A day
// the line names is kept as written, even when its time has passed.
func TestATimeWithNoDayIsNeverInThePast(t *testing.T) {
	belgrade, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	evening := time.Date(2026, 11, 25, 17, 0, 0, 0, belgrade) // a Wednesday, 5pm
	for _, c := range []struct {
		line  string
		now   time.Time
		loc   *time.Location
		start string
	}{
		{"Dentist 3pm", evening, belgrade, "2026-11-26 15:00"},
		{"Dentist 15:00", evening, belgrade, "2026-11-26 15:00"},
		{"Dentist 6pm", evening, belgrade, "2026-11-25 18:00"},
		{"Dentist today 3pm", evening, belgrade, "2026-11-25 15:00"},
		{"Dentist tomorrow 3pm", evening, belgrade, "2026-11-26 15:00"},
		{"Dentist friday 3pm", evening, belgrade, "2026-11-27 15:00"},
		{"Dentist 2026-11-25 3pm", evening, belgrade, "2026-11-25 15:00"},
		// the person's today, not the server's: 00:30 in Belgrade is still the 25th in UTC
		{"Dentist 9:00", time.Date(2026, 11, 25, 23, 30, 0, 0, time.UTC), belgrade, "2026-11-26 09:00"},
		// 8:30 in New York, when 9am has long passed in UTC
		{"Dentist 9am", time.Date(2026, 11, 25, 13, 30, 0, 0, time.UTC), newYork, "2026-11-25 09:00"},
	} {
		r := Parse(c.line, c.now, c.loc, time.Monday)
		if got := r.Start.In(c.loc).Format("2006-01-02 15:04"); got != c.start || r.AllDay || r.Title != "Dentist" {
			t.Errorf("%q at %s: starts %s (all day %v, title %q), want %s", c.line, c.now.In(c.loc).Format("Mon 15:04"), got, r.AllDay, r.Title, c.start)
		}
	}
}
