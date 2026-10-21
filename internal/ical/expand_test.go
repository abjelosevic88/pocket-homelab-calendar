package ical

import (
	"testing"
	"time"
)

const weekly = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:w1\r\nSUMMARY:Standup\r\nDTSTART;TZID=Europe/Sarajevo:20261019T090000\r\nDTEND;TZID=Europe/Sarajevo:20261019T091500\r\nRRULE:FREQ=WEEKLY;BYDAY=MO,WE\r\nEXDATE;TZID=Europe/Sarajevo:20261021T090000\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nUID:w1\r\nRECURRENCE-ID;TZID=Europe/Sarajevo:20261026T090000\r\nSUMMARY:Standup (moved)\r\nDTSTART;TZID=Europe/Sarajevo:20261026T110000\r\nDTEND;TZID=Europe/Sarajevo:20261026T113000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func TestExpandWeeklyAcrossDST(t *testing.T) {
	r, err := ParseResource(weekly, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	occ := r.Expand(from, to)
	// Oct 19, (21 excluded), 26 (override), 28, Nov 2
	if len(occ) != 4 {
		for _, o := range occ {
			t.Log(o.Key, o.Start, o.Event.Summary)
		}
		t.Fatalf("want 4 occurrences, got %d", len(occ))
	}
	sar, _ := time.LoadLocation("Europe/Sarajevo")
	if h := occ[0].Start.In(sar).Hour(); h != 9 {
		t.Fatalf("first at %d", h)
	}
	if occ[1].Event.Summary != "Standup (moved)" || occ[1].Start.In(sar).Hour() != 11 {
		t.Fatalf("override not applied: %+v", occ[1])
	}
	// after the DST change (Oct 25) still 09:00 local, i.e. 08:00 UTC
	if occ[2].Start.In(sar).Hour() != 9 || occ[2].Start.UTC().Hour() != 8 {
		t.Fatalf("DST: %v", occ[2].Start)
	}
	s, e := r.Span()
	if e.Year() < 9999 || s.After(from) {
		t.Fatalf("span %v %v", s, e)
	}
	if got := DescribeRule("FREQ=WEEKLY;BYDAY=MO,WE"); got != "Weekly on Mon, Wed" {
		t.Fatal(got)
	}
}

func TestAllDayCount(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:a\r\nSUMMARY:Trip\r\nDTSTART;VALUE=DATE:20261101\r\nDTEND;VALUE=DATE:20261103\r\nRRULE:FREQ=MONTHLY;COUNT=3\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	r, err := ParseResource(ics, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	occ := r.Expand(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC))
	if len(occ) != 3 || !occ[0].AllDay || occ[0].End.Sub(occ[0].Start) != 48*time.Hour {
		t.Fatalf("got %d %+v", len(occ), occ)
	}
	_, e := r.Span()
	if e.Year() != 2027 {
		t.Fatalf("span end %v", e)
	}
	o, ok := r.Occurrence("20261201")
	if !ok || o.Start.Day() != 1 || o.Start.Month() != 12 {
		t.Fatalf("occurrence lookup %v %v", o, ok)
	}
}

func TestApplyRoundTrip(t *testing.T) {
	c := NewEvent("x1")
	sar, _ := time.LoadLocation("Europe/Sarajevo")
	st := time.Date(2026, 10, 21, 13, 0, 0, 0, sar)
	en := st.Add(90 * time.Minute)
	title, tz := "Lunch", "Europe/Sarajevo"
	al := []Alarm{{Trigger: "-PT15M"}}
	att := []Attendee{{Email: "sani@example.com", Name: "Sani"}}
	ApplyEvent(c, EventInput{Summary: &title, Start: &st, End: &en, TZID: &tz, Alarms: &al, Attendees: &att, Organizer: &Attendee{Email: "me@example.com"}})
	cal := NewCalendar()
	cal.Children = append(cal.Children, c)
	r, err := ParseResource(cal.String(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	e := r.MasterEvent()
	if e.Summary != "Lunch" || !e.Start.Equal(st) || e.End.Sub(e.Start) != 90*time.Minute || e.TZID != "Europe/Sarajevo" {
		t.Fatalf("%+v", e)
	}
	if len(e.Alarms) != 1 || e.Alarms[0].Trigger != "-PT15M" || len(e.Attendees) != 1 || e.Attendees[0].Status != "NEEDS-ACTION" {
		t.Fatalf("alarms/attendees %+v %+v", e.Alarms, e.Attendees)
	}
	if FindMeetingLink("join https://meet.google.com/abc-defg-hij now") != "https://meet.google.com/abc-defg-hij" {
		t.Fatal("meeting link")
	}
}
