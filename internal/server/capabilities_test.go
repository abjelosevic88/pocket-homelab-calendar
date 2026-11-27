package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// getInto calls a route and reads its JSON answer into out (a pointer,
// emptied first so nothing of an earlier answer stays).
func getInto(t *testing.T, c *apiClient, method, path string, body, out any) int {
	t.Helper()
	code, b := c.call(method, path, body)
	if out != nil {
		reflect.ValueOf(out).Elem().SetZero()
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %d %s", method, path, code, b)
		}
	}
	return code
}

// addEvent creates an event and returns its first occurrence.
func addEvent(t *testing.T, c *apiClient, body map[string]any) occJSON {
	t.Helper()
	var ev occJSON
	c.json("POST", "/api/v1/events", body, 201, &ev)
	return ev
}

// addCalendar creates a calendar and returns it.
func addCalendar(t *testing.T, c *apiClient, name string) calJSON {
	t.Helper()
	var cal calJSON
	c.json("POST", "/api/v1/calendars", map[string]any{"name": name}, 201, &cal)
	if cal.ID == 0 {
		t.Fatalf("calendar %q has no id", name)
	}
	return cal
}

// boxZone is the zone the test box runs in (apiServer sets TZ).
func boxZone(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Sarajevo")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// dayLine is a day as whenLine writes it, seen from now.
func dayLine(d, now time.Time) string {
	if d.Year() != now.Year() {
		return d.Format("Mon, Jan 2, 2006")
	}
	return d.Format("Mon, Jan 2")
}

func TestPocketSearch(t *testing.T) {
	s, h := apiServer(t)
	alice := apiPerson(t, s, h, "alice@example.com", "Alice")
	bob := apiPerson(t, s, h, "bob@example.com", "Bob")
	now := time.Now().In(boxZone(t))
	day := func(n int) string { return now.AddDate(0, 0, n).Format("2006-01-02") }

	long := addEvent(t, alice, map[string]any{"title": "Dentist check-up", "start": "2020-03-05T10:00", "end": "2020-03-05T10:45", "location": "Old clinic"})
	soon := addEvent(t, alice, map[string]any{"title": "Dentist", "start": day(3) + "T15:00", "end": day(3) + "T16:00", "location": "Clinic on Main Street"})
	later := addEvent(t, alice, map[string]any{"title": "Dentist for Mia", "start": day(10), "all_day": true})
	lastWeek := addEvent(t, alice, map[string]any{"title": "Dentist bill", "start": day(-7) + "T09:00", "end": day(-7) + "T09:30"})
	// every day since January: the one found is the next one, not the first
	daily := addEvent(t, alice, map[string]any{"title": "Yoga", "description": "ask about the dentist's note", "start": "2026-01-05T06:00", "end": "2026-01-05T06:30", "rrule": "FREQ=DAILY"})
	addEvent(t, alice, map[string]any{"title": "Water the plants", "start": day(1) + "T08:00"})
	addEvent(t, bob, map[string]any{"title": "Dentist (Bob)", "start": day(2) + "T10:00"})

	var got core.SearchResults
	if code := getInto(t, alice, "GET", "/api/v1/pocket/search?q=dentist", nil, &got); code != 200 || len(got.Items) != 5 {
		t.Fatalf("search: %d %+v", code, got)
	}
	// what is still to come first, soonest on top; then what is over, latest first
	order := []string{}
	for _, it := range got.Items {
		order = append(order, it.Title)
		if it.Kind != "event" {
			t.Errorf("%q: kind %q", it.Title, it.Kind)
		}
	}
	if want := "Yoga | Dentist | Dentist for Mia | Dentist bill | Dentist check-up"; strings.Join(order, " | ") != want {
		t.Errorf("order: %s\n want: %s", strings.Join(order, " | "), want)
	}
	by := map[string]core.SearchItem{}
	for _, it := range got.Items {
		by[it.Title] = it
	}
	d3 := now.AddDate(0, 0, 3)
	if it := by["Dentist"]; it.ID != itoa64(soon.EventID) || it.URL != "/event/"+itoa64(soon.EventID) || it.Snippet != dayLine(d3, now)+" · 15:00 – 16:00 · Clinic on Main Street" || !strings.HasPrefix(it.Date, day(3)+"T15:00:00") {
		t.Errorf("a timed event with a place: %+v", it)
	}
	if it := by["Dentist for Mia"]; it.ID != itoa64(later.EventID) || it.Snippet != dayLine(now.AddDate(0, 0, 10), now)+" · All day" || it.Date != day(10) {
		t.Errorf("an all-day event: %+v", it)
	}
	if it := by["Dentist check-up"]; it.ID != itoa64(long.EventID) || it.Snippet != "Thu, Mar 5, 2020 · 10:00 – 10:45 · Old clinic" || it.Date != "2020-03-05T10:00:00+01:00" {
		t.Errorf("a long-past event: %+v", it)
	}
	if it := by["Dentist bill"]; it.ID != itoa64(lastWeek.EventID) {
		t.Errorf("last week's event: %+v", it)
	}
	// a repeating event is found once, at its next time, and opens there
	y := by["Yoga"]
	at, err := time.Parse(time.RFC3339, y.Date)
	if err != nil || at.Add(30*time.Minute).Before(now) || at.After(now.Add(24*time.Hour)) || at.In(boxZone(t)).Format("15:04") != "06:00" {
		t.Errorf("yoga's next time: %q (now %s)", y.Date, now.Format(time.RFC3339))
	}
	if y.ID != itoa64(daily.EventID) || !strings.HasPrefix(y.URL, "/event/"+itoa64(daily.EventID)+":") {
		t.Errorf("a repeating event: %+v", y)
	}
	if code, _ := alice.call("GET", "/api/v1/events/"+strings.TrimPrefix(y.URL, "/event/"), nil); code != 200 {
		t.Errorf("the address of yoga's next time does not open: %d", code)
	}

	if getInto(t, alice, "GET", "/api/v1/pocket/search?q=dentist&limit=2", nil, &got); len(got.Items) != 2 || got.Items[1].Title != "Dentist" {
		t.Errorf("limit=2: %+v", got.Items)
	}
	if code := getInto(t, alice, "GET", "/api/v1/pocket/search?q=", nil, &got); code != 200 || got.Items == nil || len(got.Items) != 0 {
		t.Errorf("no words: %d %+v", code, got)
	}
	// the app's own search still answers as before
	var own []occJSON
	if alice.json("GET", "/api/v1/search?q=dentist", nil, 200, &own); len(own) != 5 {
		t.Errorf("GET /search: %d events", len(own))
	}

	// strictly per person
	if getInto(t, bob, "GET", "/api/v1/pocket/search?q=dentist", nil, &got); len(got.Items) != 1 || got.Items[0].Title != "Dentist (Bob)" {
		t.Errorf("bob finds: %+v", got.Items)
	}
	if code, _ := (&apiClient{t: t, h: h}).call("GET", "/api/v1/pocket/search?q=dentist", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}

	// a calendar shared to read is searched, apart from its private events;
	// one shared only as busy or free is not
	shared := firstCal(bob)
	addEvent(t, bob, map[string]any{"title": "Dentist in secret", "start": day(4) + "T10:00", "private": true})
	bob.json("POST", "/api/v1/calendars/"+itoa64(shared.ID)+"/shares", map[string]any{"user_id": alice.user.ID, "role": "read"}, 200, nil)
	getInto(t, alice, "GET", "/api/v1/pocket/search?q=dentist", nil, &got)
	titles := []string{}
	for _, it := range got.Items {
		titles = append(titles, it.Title)
	}
	if all := strings.Join(titles, " | "); len(got.Items) != 6 || !strings.Contains(all, "Dentist (Bob)") || strings.Contains(all, "secret") || strings.Contains(all, "Busy") {
		t.Errorf("with bob's calendar to read: %s", all)
	}
	bob.json("POST", "/api/v1/calendars/"+itoa64(shared.ID)+"/shares", map[string]any{"user_id": alice.user.ID, "role": "freebusy"}, 200, nil)
	if getInto(t, alice, "GET", "/api/v1/pocket/search?q=dentist", nil, &got); len(got.Items) != 5 {
		t.Errorf("with bob's calendar as busy only: %+v", got.Items)
	}
}

func TestWhenLine(t *testing.T) {
	loc := boxZone(t)
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, loc)
	at := func(d, h, m int) time.Time { return time.Date(2026, 12, d, h, m, 0, 0, loc) }
	for _, c := range []struct {
		a, b   time.Time
		allDay bool
		h12    bool
		want   string
	}{
		{at(4, 15, 0), at(4, 16, 30), false, false, "Fri, Dec 4 · 15:00 – 16:30"},
		{at(4, 15, 0), at(4, 16, 30), false, true, "Fri, Dec 4 · 3:00 pm – 4:30 pm"},
		{at(4, 15, 0), at(4, 15, 0), false, false, "Fri, Dec 4 · 15:00"},
		{at(4, 22, 0), at(5, 0, 0), false, false, "Fri, Dec 4 · 22:00 – 00:00"},
		{at(4, 22, 0), at(5, 6, 0), false, false, "Fri, Dec 4, 22:00 – Sat, Dec 5, 06:00"},
		{at(4, 0, 0), at(5, 0, 0), true, false, "Fri, Dec 4 · All day"},
		{at(10, 0, 0), at(14, 0, 0), true, false, "Thu, Dec 10 – Sun, Dec 13"},
		{at(31, 0, 0), time.Date(2027, 1, 3, 0, 0, 0, 0, loc), true, false, "Thu, Dec 31 – Sat, Jan 2, 2027"},
		{time.Date(2025, 5, 1, 9, 0, 0, 0, loc), time.Date(2025, 5, 1, 10, 0, 0, 0, loc), false, false, "Thu, May 1, 2025 · 09:00 – 10:00"},
	} {
		if got := whenLine(c.a, c.b, c.allDay, now, c.h12); got != c.want {
			t.Errorf("whenLine: %q, want %q", got, c.want)
		}
	}
}

func dayItems(d core.Day) map[string]core.DayItem {
	out := map[string]core.DayItem{}
	for _, it := range d.Items {
		out[it.Title] = it
	}
	return out
}

// dayRows is a day in one line: "Title (time–end, kind, sub)" in the order given.
func dayRows(d core.Day) string {
	rows := []string{}
	for _, it := range d.Items {
		when := it.Time
		if it.End != "" {
			when += "–" + it.End
		}
		rows = append(rows, it.Title+" ("+strings.Join([]string{when, it.Kind, it.Sub}, ", ")+")")
	}
	return strings.Join(rows, " | ")
}

// The week of 16 November 2026 in alice's calendar. The box is in Sarajevo
// (UTC+1 then), so Honolulu is eleven hours behind it and Kiritimati thirteen
// ahead: a day there is 11:00 to 11:00 in Sarajevo.
func TestPocketDay(t *testing.T) {
	s, h := apiServer(t)
	alice := apiPerson(t, s, h, "alice@example.com", "Alice")
	bob := apiPerson(t, s, h, "bob@example.com", "Bob")
	own := firstCal(alice)

	standup := addEvent(t, alice, map[string]any{"title": "Standup", "start": "2026-11-02T09:15", "end": "2026-11-02T09:30", "location": "Office",
		"rrule": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"})
	var week struct {
		Events []occJSON `json:"events"`
	}
	alice.json("GET", "/api/v1/events?from=2026-11-16&to=2026-11-21", nil, 200, &week)
	if len(week.Events) != 5 {
		t.Fatalf("the week's standups: %+v", week.Events)
	}
	// Wednesday's is taken out, Thursday's moved to 11:00 under another name
	alice.json("DELETE", "/api/v1/events/"+week.Events[2].ID+"?scope=this", nil, 200, nil)
	alice.json("PUT", "/api/v1/events/"+week.Events[3].ID+"?scope=this", map[string]any{"title": "Late standup", "start": "2026-11-19T11:00", "end": "2026-11-19T11:15"}, 200, nil)
	friday := week.Events[4]

	bins := addEvent(t, alice, map[string]any{"title": "Bins out", "start": "2026-11-06", "all_day": true, "rrule": "FREQ=WEEKLY"})
	flight := addEvent(t, alice, map[string]any{"title": "Early flight", "start": "2026-11-21T05:00", "end": "2026-11-21T07:00", "location": "Airport"})
	train := addEvent(t, alice, map[string]any{"title": "Night train", "start": "2026-11-21T22:00", "end": "2026-11-22T06:00"})
	addEvent(t, alice, map[string]any{"title": "Conference", "start": "2026-11-24T10:00", "end": "2026-11-26T16:00"})
	addEvent(t, alice, map[string]any{"title": "Lunch that fell through", "start": "2026-11-20T12:00", "end": "2026-11-20T13:00", "status": "cancelled"})
	// bob: a calendar alice sees only as busy, and a dinner he invites her to from another one
	addEvent(t, bob, map[string]any{"title": "Bob's secret", "start": "2026-11-20T14:00", "end": "2026-11-20T15:00", "location": "Nowhere"})
	bobs := firstCal(bob)
	bob.json("POST", "/api/v1/calendars/"+itoa64(bobs.ID)+"/shares", map[string]any{"user_id": alice.user.ID, "role": "freebusy"}, 200, nil)
	friends := addCalendar(t, bob, "Friends")
	dinner := addEvent(t, bob, map[string]any{"title": "Dinner", "calendar_id": friends.ID, "start": "2026-11-20T19:00", "end": "2026-11-20T21:00",
		"attendees": []map[string]string{{"email": "alice@example.com"}}})
	addEvent(t, bob, map[string]any{"title": "Bob alone", "calendar_id": friends.ID, "start": "2026-11-20T08:00", "end": "2026-11-20T08:30"})

	var day core.Day
	// Friday as the person's own zone has it
	if code := getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-20", nil, &day); code != 200 || day.Date != "2026-11-20" || day.Summary != "4 events" {
		t.Fatalf("friday: %d %+v", code, day)
	}
	if got, want := dayRows(day), "Bins out (, all day, "+own.Name+") | Standup (09:15–09:30, event, Office) | Busy (14:00–15:00, event, "+bobs.Name+") | Dinner (19:00–21:00, event, Invitation)"; got != want {
		t.Errorf("friday:\n got %s\nwant %s", got, want)
	}
	got := dayItems(day)
	if it := got["Standup"]; it.ID != friday.ID || !strings.Contains(it.ID, ":") || it.URL != "/event/"+friday.ID {
		t.Errorf("friday's standup is that day's occurrence: %+v, want id %s", it, friday.ID)
	}
	if it := got["Bins out"]; !strings.HasPrefix(it.ID, itoa64(bins.EventID)+":") || it.Time != "" || it.End != "" {
		t.Errorf("an all-day event: %+v", it)
	}
	if it := got["Dinner"]; it.ID != itoa64(dinner.EventID) || it.URL != "/event/"+itoa64(dinner.EventID) {
		t.Errorf("an invitation: %+v", it)
	}
	for _, it := range day.Items {
		if code, _ := alice.call("GET", "/api/v1/events/"+strings.TrimPrefix(it.URL, "/event/"), nil); code != 200 {
			t.Errorf("%q: its address %s does not open: %d", it.Title, it.URL, code)
		}
	}
	if b, _ := json.Marshal(day); strings.Contains(string(b), "secret") || strings.Contains(string(b), "Nowhere") || strings.Contains(string(b), "Bob alone") || strings.Contains(string(b), "fell through") {
		t.Errorf("friday shows what alice may not see, or what was called off: %s", b)
	}

	// the occurrence taken out is gone, the moved one is where it went
	if code := getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-18", nil, &day); code != 200 || day.Summary != "" || day.Items == nil || len(day.Items) != 0 {
		t.Errorf("wednesday: %d %+v", code, day)
	}
	if getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-19", nil, &day); day.Summary != "1 event" || dayRows(day) != "Late standup (11:00–11:15, event, Office)" {
		t.Errorf("thursday: %s", dayRows(day))
	}

	// Friday in Honolulu is 11:00 Friday to 11:00 Saturday in Sarajevo: the
	// standup at 09:15 was Thursday evening there, Saturday's flight is Friday
	// evening. The bins are a plain date: on the 20th wherever the person is.
	getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-20?tz=Pacific/Honolulu", nil, &day)
	if want := "Bins out (, all day, " + own.Name + ") | Busy (03:00–04:00, event, " + bobs.Name + ") | Dinner (08:00–10:00, event, Invitation) | Early flight (18:00–20:00, event, Airport)"; day.Summary != "4 events" || dayRows(day) != want {
		t.Errorf("friday in Honolulu: %s\n got %s\nwant %s", day.Summary, dayRows(day), want)
	}
	// Thursday there: the moved standup starts on the stroke of midnight, and
	// Friday's is that evening. No bins: they are Friday's.
	getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-19?tz=Pacific/Honolulu", nil, &day)
	if want := "Late standup (00:00–00:15, event, Office) | Standup (22:15–22:30, event, Office)"; day.Summary != "2 events" || dayRows(day) != want {
		t.Errorf("thursday in Honolulu:\n got %s\nwant %s", dayRows(day), want)
	}
	if it := dayItems(day)["Standup"]; it.ID != friday.ID {
		t.Errorf("thursday evening's standup in Honolulu is friday's occurrence: %+v", it)
	}
	// a weekday event on a Sunday: Monday's standup, the evening before
	getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-22?tz=Pacific/Honolulu", nil, &day)
	if it := dayItems(day)["Standup"]; dayRows(day) != "Standup (22:15–22:30, event, Office)" || !strings.HasPrefix(it.ID, itoa64(standup.EventID)+":20261123T") {
		t.Errorf("sunday in Honolulu: %s %+v", dayRows(day), it)
	}

	// Kiritimati, far east: its Saturday is the same 24 hours as Honolulu's
	// Friday, so the same timed events at other hours, and no bins
	getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-21?tz=Pacific/Kiritimati", nil, &day)
	if want := "Busy (03:00–04:00, event, " + bobs.Name + ") | Dinner (08:00–10:00, event, Invitation) | Early flight (18:00–20:00, event, Airport)"; day.Summary != "3 events" || dayRows(day) != want {
		t.Errorf("saturday in Kiritimati:\n got %s\nwant %s", dayRows(day), want)
	}
	getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-20?tz=Pacific/Kiritimati", nil, &day)
	if want := "Bins out (, all day, " + own.Name + ") | Late standup (00:00–00:15, event, Office) | Standup (22:15–22:30, event, Office)"; dayRows(day) != want {
		t.Errorf("friday in Kiritimati:\n got %s\nwant %s", dayRows(day), want)
	}
	// a zone nobody knows is left aside: the person's own decides
	if getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-19?tz=Mars/Olympus", nil, &day); dayRows(day) != "Late standup (11:00–11:15, event, Office)" {
		t.Errorf("an unknown zone: %s", dayRows(day))
	}

	// an event over midnight is on both days: from its start, then until its end
	getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-21", nil, &day)
	if want := "Early flight (05:00–07:00, event, Airport) | Night train (22:00, event, " + own.Name + ")"; dayRows(day) != want {
		t.Errorf("saturday:\n got %s\nwant %s", dayRows(day), want)
	}
	getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-22", nil, &day)
	if it := dayItems(day)["Night train"]; day.Summary != "1 event" || dayRows(day) != "Night train (00:00–06:00, event, "+own.Name+")" || it.ID != itoa64(train.EventID) {
		t.Errorf("sunday: %s", dayRows(day))
	}
	// one that covers the whole day reads as all day
	getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-25", nil, &day)
	if want := "Conference (, all day, " + own.Name + ") | Standup (09:15–09:30, event, Office)"; dayRows(day) != want {
		t.Errorf("wednesday the 25th:\n got %s\nwant %s", dayRows(day), want)
	}
	_ = flight

	// strictly per person: bob has his own Friday
	getInto(t, bob, "GET", "/api/v1/pocket/day/2026-11-20", nil, &day)
	if want := "Bob alone (08:00–08:30, event, Friends) | Bob's secret (14:00–15:00, event, Nowhere) | Dinner (19:00–21:00, event, Friends)"; day.Summary != "3 events" || dayRows(day) != want {
		t.Errorf("bob's friday:\n got %s\nwant %s", dayRows(day), want)
	}

	// what the person hid or declined in the app is not on their day either
	alice.json("PUT", "/api/v1/calendars/"+itoa64(bobs.ID), map[string]any{"visible": false}, 200, nil)
	alice.json("POST", "/api/v1/events/"+itoa64(dinner.EventID)+"/rsvp", map[string]any{"status": "declined"}, 200, nil)
	getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-20", nil, &day)
	if want := "Bins out (, all day, " + own.Name + ") | Standup (09:15–09:30, event, Office)"; day.Summary != "2 events" || dayRows(day) != want {
		t.Errorf("friday without the hidden calendar and the declined dinner:\n got %s\nwant %s", dayRows(day), want)
	}
	alice.json("PUT", "/api/v1/settings", map[string]any{"show_declined": true}, 200, nil)
	if getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-20", nil, &day); day.Summary != "3 events" || dayItems(day)["Dinner"].Time != "19:00" {
		t.Errorf("friday with declined events shown: %s", dayRows(day))
	}
	// the person's own zone, once they chose one
	alice.json("PUT", "/api/v1/settings", map[string]any{"timezone": "Pacific/Honolulu"}, 200, nil)
	if getInto(t, alice, "GET", "/api/v1/pocket/day/2026-11-19", nil, &day); dayRows(day) != "Late standup (00:00–00:15, event, Office) | Standup (22:15–22:30, event, Office)" {
		t.Errorf("thursday in the person's own zone: %s", dayRows(day))
	}

	if code, _ := alice.call("GET", "/api/v1/pocket/day/yesterday", nil); code != 400 {
		t.Errorf("not a day: %d", code)
	}
	if code, _ := (&apiClient{t: t, h: h}).call("GET", "/api/v1/pocket/day/2026-11-20", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
}

// widgetRows is a widget's rows in one line: "Title (detail)".
func widgetRows(w *core.WidgetData) string {
	rows := []string{}
	for _, it := range w.Items {
		rows = append(rows, it.Title+" ("+it.Detail+")")
	}
	return strings.Join(rows, " | ")
}

func TestPocketWidget(t *testing.T) {
	s, h := apiServer(t)
	alice := apiPerson(t, s, h, "alice@example.com", "Alice")
	bob := apiPerson(t, s, h, "bob@example.com", "Bob")
	loc := boxZone(t)
	ctx := context.Background()
	at := func(d, hh, mm int) time.Time { return time.Date(2026, 11, d, hh, mm, 0, 0, loc) }

	addEvent(t, alice, map[string]any{"title": "Bins out", "start": "2026-11-06", "all_day": true, "rrule": "FREQ=WEEKLY"})
	addEvent(t, alice, map[string]any{"title": "Standup", "start": "2026-11-20T09:15", "end": "2026-11-20T09:30"})
	addEvent(t, alice, map[string]any{"title": "Lunch", "start": "2026-11-20T12:30", "end": "2026-11-20T13:30"})
	dentist := addEvent(t, alice, map[string]any{"title": "Dentist", "start": "2026-11-20T15:00", "end": "2026-11-20T16:00"})
	addEvent(t, alice, map[string]any{"title": "Night train", "start": "2026-11-20T22:00", "end": "2026-11-21T06:00"})
	addEvent(t, alice, map[string]any{"title": "Called off", "start": "2026-11-20T17:00", "end": "2026-11-20T18:00", "status": "cancelled"})
	addEvent(t, alice, map[string]any{"title": "Early flight", "start": "2026-11-21T05:00", "end": "2026-11-21T07:00"})
	addEvent(t, alice, map[string]any{"title": "Mia's birthday", "start": "2026-11-23", "all_day": true})
	addEvent(t, alice, map[string]any{"title": "Trip", "start": "2026-11-26T10:00", "end": "2026-11-26T12:00"})
	addEvent(t, alice, map[string]any{"title": "Too far ahead", "start": "2027-03-01T10:00"})
	addEvent(t, bob, map[string]any{"title": "Bob's", "start": "2026-11-20T14:00", "end": "2026-11-20T15:00"})

	for _, c := range []struct {
		name string
		at   time.Time
		line string
		rows string
	}{
		{"friday after lunch started", at(20, 13, 0), "3 more today",
			"Bins out (all day) | Lunch (until 13:30) | Dentist (15:00) | Night train (22:00) | Early flight (tomorrow 05:00)"},
		{"friday late: the train runs past midnight", at(20, 23, 30), "1 more today",
			"Bins out (all day) | Night train (now) | Early flight (tomorrow 05:00) | Mia's birthday (Mon 23 Nov) | Trip (Thu 26 Nov 10:00)"},
		{"saturday morning on the train", at(21, 4, 0), "2 more today",
			"Night train (until 06:00) | Early flight (05:00) | Mia's birthday (Mon 23 Nov) | Trip (Thu 26 Nov 10:00) | Bins out (Fri 27 Nov)"},
		{"saturday noon, all over", at(21, 12, 0), "Nothing more today",
			"Mia's birthday (Mon 23 Nov) | Trip (Thu 26 Nov 10:00) | Bins out (Fri 27 Nov) | Bins out (Fri 4 Dec) | Bins out (Fri 11 Dec)"},
		{"sunday, an empty day", at(22, 9, 0), "Nothing today",
			"Mia's birthday (tomorrow) | Trip (Thu 26 Nov 10:00) | Bins out (Fri 27 Nov) | Bins out (Fri 4 Dec) | Bins out (Fri 11 Dec)"},
		{"monday, only an all-day event", at(23, 9, 0), "1 all-day event today",
			"Mia's birthday (all day) | Trip (Thu 26 Nov 10:00) | Bins out (Fri 27 Nov) | Bins out (Fri 4 Dec) | Bins out (Fri 11 Dec)"},
	} {
		w, err := s.agendaWidget(ctx, alice.user, c.at)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if w.Line != c.line || widgetRows(w) != c.rows || w.URL != "/agenda" {
			t.Errorf("%s: %q\n got %s\nwant %s", c.name, w.Line, widgetRows(w), c.rows)
		}
	}
	// a row opens its event
	w, _ := s.agendaWidget(ctx, alice.user, at(20, 13, 0))
	if it := w.Items[2]; it.ID != itoa64(dentist.EventID) || it.URL != "/event/"+itoa64(dentist.EventID) {
		t.Errorf("the dentist's row: %+v", it)
	}
	// a full day is not padded with later ones
	for i := 0; i < 5; i++ {
		addEvent(t, alice, map[string]any{"title": "More", "start": "2026-11-20T18:0" + itoa64(int64(i)), "end": "2026-11-20T18:30"})
	}
	if w, _ = s.agendaWidget(ctx, alice.user, at(20, 13, 0)); w.Line != "8 more today" || len(w.Items) != 9 || strings.Contains(widgetRows(w), "tomorrow") {
		t.Errorf("a full day: %q %s", w.Line, widgetRows(w))
	}
	// the clock the person chose
	alice.json("PUT", "/api/v1/settings", map[string]any{"time_format": "12h"}, 200, nil)
	if w, _ = s.agendaWidget(ctx, alice.user, at(21, 4, 0)); !strings.HasPrefix(widgetRows(w), "Night train (until 6:00 am) | Early flight (5:00 am)") {
		t.Errorf("12-hour clock: %s", widgetRows(w))
	}
	// strictly per person
	if w, _ = s.agendaWidget(ctx, bob.user, at(20, 13, 0)); w.Line != "1 more today" || widgetRows(w) != "Bob's (14:00)" {
		t.Errorf("bob's widget: %q %s", w.Line, widgetRows(w))
	}

	// the route, at the real time of day: carol has one event, tomorrow at noon
	carol := apiPerson(t, s, h, "carol@example.com", "Carol")
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	noon := addEvent(t, carol, map[string]any{"title": "Lunch with Mia", "start": tomorrow + "T12:00", "end": tomorrow + "T13:00"})
	var got core.WidgetData
	if code := getInto(t, carol, "GET", "/api/v1/pocket/widget", nil, &got); code != 200 || got.Line != "Nothing today" || got.URL != "/agenda" || widgetRows(&got) != "Lunch with Mia (tomorrow 12:00)" || got.Items[0].URL != "/event/"+itoa64(noon.EventID) {
		t.Errorf("carol's widget: %d %+v", code, got)
	}
	dave := apiPerson(t, s, h, "dave@example.com", "Dave")
	if code := getInto(t, dave, "GET", "/api/v1/pocket/widget", nil, &got); code != 200 || got.Line != "Nothing today" || got.Items == nil || len(got.Items) != 0 {
		t.Errorf("an empty calendar: %d %+v", code, got)
	}
	if code, _ := (&apiClient{t: t, h: h}).call("GET", "/api/v1/pocket/widget", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
}

func capture(t *testing.T, c *apiClient, body map[string]any) (int, core.CaptureResult, string) {
	t.Helper()
	code, b := c.call("POST", "/api/v1/pocket/capture", body)
	var res core.CaptureResult
	_ = json.Unmarshal(b, &res)
	return code, res, string(b)
}

// kept is a captured event as GET /events/{id} shows it.
type kept struct {
	Event struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Location    string `json:"location"`
		Start       string `json:"start"`
		End         string `json:"end"`
		AllDay      bool   `json:"all_day"`
		CalendarID  int64  `json:"calendar_id"`
		Source      string `json:"source"`
		Attendees   []any  `json:"attendees"`
		Organizer   any    `json:"organizer"`
	} `json:"event"`
}

func keptEvent(t *testing.T, c *apiClient, id string) kept {
	t.Helper()
	var k kept
	c.json("GET", "/api/v1/events/"+id, nil, 200, &k)
	return k
}

// eventCount is how many events a person has in their own calendars.
func eventCount(t *testing.T, s *Server, email string) int {
	t.Helper()
	return len(eventsOf(t, s, email))
}

func TestPocketCapture(t *testing.T) {
	s, h := apiServer(t)
	alice := apiPerson(t, s, h, "alice@example.com", "Alice")
	bob := apiPerson(t, s, h, "bob@example.com", "Bob")
	own := firstCal(alice)
	family := addCalendar(t, alice, "Family")
	loc := boxZone(t)
	now := time.Now().In(loc)
	today, tomorrow := now.Format("2006-01-02"), now.AddDate(0, 0, 1).Format("2006-01-02")

	// one line, read the way quick add reads it
	line := map[string]any{"request_id": "hub-0000000000000001", "kind": "event", "text": "  Dentist tomorrow 3pm at the clinic  "}
	code, first, body := capture(t, alice, line)
	if code != 201 || first.ID == "" || first.Title != "Dentist" || first.URL != "/event/"+first.ID {
		t.Fatalf("capture: %d %s", code, body)
	}
	k := keptEvent(t, alice, first.ID).Event
	if k.Title != "Dentist" || k.Location != "the clinic" || k.AllDay || !strings.HasPrefix(k.Start, tomorrow+"T15:00:00") || !strings.HasPrefix(k.End, tomorrow+"T16:00:00") ||
		k.CalendarID != own.ID || k.Description != "" || len(k.Attendees) != 0 || k.Source != "api" {
		t.Errorf("the event it made: %+v", k)
	}

	// the same request again answers the same event and adds nothing
	code, again, body := capture(t, alice, line)
	if code != 201 || again != first {
		t.Errorf("same request_id: %d %s, want %+v", code, body, first)
	}
	if n := eventCount(t, s, "alice@example.com"); n != 1 {
		t.Errorf("same request_id twice made %d events", n)
	}
	// …and it is not a way to keep something else
	if code, _, body := capture(t, alice, map[string]any{"request_id": "hub-0000000000000001", "text": "Something else"}); code != 400 {
		t.Errorf("same request_id, other words: %d %s", code, body)
	}
	// another person's same request_id is their own event
	code, his, _ := capture(t, bob, line)
	if code != 201 || his.ID == first.ID {
		t.Errorf("bob's capture: %d %+v", code, his)
	}
	if code, _ := bob.call("GET", "/api/v1/events/"+first.ID, nil); code != 404 {
		t.Errorf("bob reads alice's captured event: %d", code)
	}

	// a title with a time, details and a day: the line names no day, so it is on the given one
	code, full, body := capture(t, alice, map[string]any{"request_id": "hub-0000000000000002", "title": "Parents' evening 18:30", "text": "Room 12\nBring the form", "date": "2026-12-05"})
	if code != 201 || full.Title != "Parents' evening" {
		t.Fatalf("capture with a title: %d %s", code, body)
	}
	if k = keptEvent(t, alice, full.ID).Event; k.Description != "Room 12\nBring the form" || !strings.HasPrefix(k.Start, "2026-12-05T18:30:00") || !strings.HasPrefix(k.End, "2026-12-05T19:30:00") || k.AllDay {
		t.Errorf("the event with a title: %+v", k)
	}
	// no time either: all day on the given day
	_, recital, _ := capture(t, alice, map[string]any{"request_id": "hub-0000000000000003", "text": "Mia's recital", "date": "2026-12-12"})
	if k = keptEvent(t, alice, recital.ID).Event; recital.Title != "Mia's recital" || !k.AllDay || k.Start != "2026-12-12" || k.End != "2026-12-13" {
		t.Errorf("a line with no time: %+v %+v", recital, k)
	}
	// a day in the line wins over the given one
	_, car, _ := capture(t, alice, map[string]any{"request_id": "hub-0000000000000004", "text": "Car service 2026-12-20 9:00 for 2h", "date": "2026-12-01"})
	if k = keptEvent(t, alice, car.ID).Event; car.Title != "Car service" || !strings.HasPrefix(k.Start, "2026-12-20T09:00:00") || !strings.HasPrefix(k.End, "2026-12-20T11:00:00") {
		t.Errorf("a line with its own day: %+v %+v", car, k)
	}
	// neither a day nor a date: today, all day (as quick add does)
	_, call, _ := capture(t, alice, map[string]any{"request_id": "hub-0000000000000005", "text": "Call grandma"})
	if k = keptEvent(t, alice, call.ID).Event; !k.AllDay || k.Start != today {
		t.Errorf("a bare line: %+v", k)
	}
	// several lines and no title: the first is the event, the rest the notes
	_, lines, _ := capture(t, alice, map[string]any{"request_id": "hub-0000000000000006", "text": "Pack for the trip tomorrow\npassport\ncharger"})
	if k = keptEvent(t, alice, lines.ID).Event; lines.Title != "Pack for the trip" || k.Description != "passport\ncharger" || k.Start != tomorrow {
		t.Errorf("several lines: %+v %+v", lines, k)
	}
	// "#family" picks the calendar, as in quick add
	_, picnic, _ := capture(t, alice, map[string]any{"request_id": "hub-0000000000000007", "text": "Picnic 12:00 #family", "date": "2026-12-06"})
	if k = keptEvent(t, alice, picnic.ID).Event; picnic.Title != "Picnic" || k.CalendarID != family.ID {
		t.Errorf("a calendar by name: %+v %+v", picnic, k)
	}
	// a repeating line starts on the first day that fits, counted from the given one
	_, gym, _ := capture(t, alice, map[string]any{"request_id": "hub-0000000000000008", "text": "Gym every monday 7:00", "date": "2026-12-02"})
	if k = keptEvent(t, alice, gym.ID).Event; gym.Title != "Gym" || !strings.HasPrefix(k.Start, "2026-12-07T07:00:00") {
		t.Errorf("a repeating line: %+v %+v", gym, k)
	}
	var mondays struct {
		Events []occJSON `json:"events"`
	}
	alice.json("GET", "/api/v1/events?from=2026-12-21&to=2027-01-05", nil, 200, &mondays)
	if len(mondays.Events) != 3 || mondays.Events[0].Title != "Gym" || !strings.HasPrefix(mondays.Events[2].Start, "2027-01-04T07:00:00") {
		t.Errorf("the gym's next mondays: %+v", mondays.Events)
	}

	// quick add would invite Bob; a captured line never invites anyone
	var quickOut struct {
		Parsed struct {
			Attendees []any `json:"attendees"`
		} `json:"parsed"`
	}
	alice.json("POST", "/api/v1/events/quick", map[string]any{"text": "Lunch with Bob 2026-12-08 13:00", "preview": true}, 200, &quickOut)
	if len(quickOut.Parsed.Attendees) != 1 {
		t.Fatalf("quick add no longer reads \"with Bob\": %+v", quickOut)
	}
	_, lunch, _ := capture(t, alice, map[string]any{"request_id": "hub-0000000000000009", "text": "Lunch with Bob 2026-12-08 13:00"})
	if k = keptEvent(t, alice, lunch.ID).Event; lunch.Title != "Lunch with Bob" || len(k.Attendees) != 0 || k.Organizer != nil {
		t.Errorf("a line with a name in it: %+v %+v", lunch, k)
	}
	var day core.Day
	if getInto(t, bob, "GET", "/api/v1/pocket/day/2026-12-08", nil, &day); len(day.Items) != 0 {
		t.Errorf("bob was invited: %+v", day.Items)
	}
	var invites []occJSON
	if bob.json("GET", "/api/v1/invitations", nil, 200, &invites); len(invites) != 0 {
		t.Errorf("bob has invitations: %+v", invites)
	}

	before := eventCount(t, s, "alice@example.com")
	for name, bad := range map[string]map[string]any{
		"nothing to keep": {"request_id": "hub-000000000000000b", "kind": "event"},
		"only spaces":     {"request_id": "hub-000000000000000b", "text": "   "},
		"no request_id":   {"text": "Dentist tomorrow 3pm"},
		"unknown kind":    {"request_id": "hub-000000000000000b", "kind": "task", "text": "Dentist tomorrow 3pm"},
		"not a day":       {"request_id": "hub-000000000000000b", "text": "Dentist 3pm", "date": "tomorrow"},
	} {
		if code, _, body := capture(t, alice, bad); code != 400 {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if n := eventCount(t, s, "alice@example.com"); n != before || n != 9 {
		t.Errorf("alice has %d events, want 9 (and %d before the refused ones)", n, before)
	}
	if code, _ := (&apiClient{t: t, h: h}).call("POST", "/api/v1/pocket/capture", line); code != 401 {
		t.Errorf("signed out: %d", code)
	}
}

// The same line sent several times at once (a retry racing the first try)
// still makes one event, and every answer names it.
func TestPocketCaptureAtOnce(t *testing.T) {
	s, h := apiServer(t)
	alice := apiPerson(t, s, h, "alice@example.com", "Alice")
	firstCal(alice)
	line := map[string]any{"request_id": "hub-000000000000000a", "text": "Only once 2026-12-01 10:00"}
	const n = 8
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, b := alice.call("POST", "/api/v1/pocket/capture", line)
			var res core.CaptureResult
			_ = json.Unmarshal(b, &res)
			if code != 201 {
				res.ID = "HTTP " + itoa64(int64(code)) + " " + string(b)
			}
			ids <- res.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]int{}
	for id := range ids {
		seen[id]++
	}
	if len(seen) != 1 {
		t.Errorf("answers: %v", seen)
	}
	if got := eventsOf(t, s, "alice@example.com"); len(got) != 1 {
		t.Errorf("events: %v, want 1", got)
	}
}

func TestPocketLinkTokens(t *testing.T) {
	s, h := linkServer(t, "hub", map[string]string{"pcx_search": core.ScopeSearch, "pcx_day": core.ScopeDay, "pcx_widget": core.ScopeWidget,
		"pcx_capture": core.ScopeCapture, "pcx_read": "calendar:read", "pcx_rw": "calendar:read calendar:write", "pcx_export": core.ScopeMoveExport})
	alice := apiPerson(t, s, h, "alice@example.com", "Alice")
	bob := apiPerson(t, s, h, "bob@example.com", "Bob")
	loc := boxZone(t)
	now := time.Now().In(loc)
	today, tomorrow := now.Format("2006-01-02"), now.AddDate(0, 0, 1).Format("2006-01-02")
	addEvent(t, alice, map[string]any{"title": "Alice's dentist", "start": tomorrow + "T10:00", "end": tomorrow + "T11:00"})
	addEvent(t, bob, map[string]any{"title": "Bob's dentist", "start": tomorrow + "T10:00", "end": tomorrow + "T11:00"})

	line := map[string]any{"request_id": "hub-0000000000000001", "text": "Kept through a link", "date": tomorrow}
	routes := []struct {
		scope, method, path string
		body                any
		ok                  int
	}{
		{"search", "GET", "/api/v1/pocket/search?q=dentist", nil, 200},
		{"day", "GET", "/api/v1/pocket/day/" + tomorrow + "?tz=Europe/Sarajevo", nil, 200},
		{"widget", "GET", "/api/v1/pocket/widget", nil, 200},
		{"capture", "POST", "/api/v1/pocket/capture", line, 201},
	}
	for _, tok := range []string{"search", "day", "widget", "capture", "read", "rw", "export"} {
		c := &apiClient{t: t, h: h, token: "pcx_" + tok}
		for _, rt := range routes {
			want := 403
			if rt.scope == tok {
				want = rt.ok
			}
			code, b := c.call(rt.method, rt.path, rt.body)
			if code != want {
				t.Errorf("pcx_%s %s %s: %d, want %d", tok, rt.method, rt.path, code, want)
			}
			if strings.Contains(string(b), "Bob") {
				t.Errorf("pcx_%s %s: another person's event: %s", tok, rt.path, b)
			}
		}
		if code, _ := c.call("GET", "/api/v1/auth/me", nil); code != 200 {
			t.Errorf("pcx_%s /auth/me: %d", tok, code)
		}
		if tok == "read" || tok == "rw" || tok == "export" {
			continue
		}
		// a capability opens its one route with its one method, nothing else of the app
		for _, rt := range []struct{ method, path string }{
			{"GET", "/api/v1/events?from=" + today + "&to=" + tomorrow}, {"GET", "/api/v1/search?q=dentist"}, {"GET", "/api/v1/calendars"},
			{"POST", "/api/v1/events"}, {"POST", "/api/v1/events/quick"}, {"GET", "/api/v1/settings"}, {"GET", "/api/v1/export.ics"}, {"GET", core.PortablePath},
		} {
			if code, _ := c.call(rt.method, rt.path, map[string]any{"title": "nope", "text": "nope", "start": tomorrow + "T10:00"}); code != 403 {
				t.Errorf("pcx_%s %s %s: %d, want 403", tok, rt.method, rt.path, code)
			}
		}
	}
	// the old scopes still open what they opened
	if code, _ := (&apiClient{t: t, h: h, token: "pcx_read"}).call("GET", "/api/v1/search?q=dentist", nil); code != 200 {
		t.Errorf("calendar:read GET /search: %d", code)
	}

	// the other method on a capability's own route is no way in either
	for _, rt := range []struct{ tok, method, path string }{{"search", "POST", "/api/v1/pocket/search?q=dentist"}, {"capture", "GET", "/api/v1/pocket/capture"}, {"widget", "DELETE", "/api/v1/pocket/widget"}} {
		if code, _ := (&apiClient{t: t, h: h, token: "pcx_" + rt.tok}).call(rt.method, rt.path, nil); code != 403 && code != http.StatusMethodNotAllowed {
			t.Errorf("pcx_%s %s %s: %d", rt.tok, rt.method, rt.path, code)
		}
	}

	// what came back is alice's, and only hers
	var found core.SearchResults
	getInto(t, &apiClient{t: t, h: h, token: "pcx_search"}, "GET", "/api/v1/pocket/search?q=dentist", nil, &found)
	if len(found.Items) != 1 || found.Items[0].Title != "Alice's dentist" {
		t.Errorf("link search: %+v", found.Items)
	}
	var day core.Day
	getInto(t, &apiClient{t: t, h: h, token: "pcx_day"}, "GET", "/api/v1/pocket/day/"+tomorrow, nil, &day)
	if day.Summary != "2 events" || dayRows(day) != "Kept through a link (, all day, Personal) | Alice's dentist (10:00–11:00, event, Personal)" {
		t.Errorf("link day: %s %s", day.Summary, dayRows(day))
	}
	var wd core.WidgetData
	getInto(t, &apiClient{t: t, h: h, token: "pcx_widget"}, "GET", "/api/v1/pocket/widget", nil, &wd)
	if wd.Line != "Nothing today" || widgetRows(&wd) != "Kept through a link (tomorrow) | Alice's dentist (tomorrow 10:00)" {
		t.Errorf("link widget: %q %s", wd.Line, widgetRows(&wd))
	}

	// the same capture again through the link: still one event, it is alice's,
	// and it says which app made it
	hub := &apiClient{t: t, h: h, token: "pcx_capture"}
	code, first, body := capture(t, hub, line)
	code2, again, _ := capture(t, hub, line)
	if code != 201 || code2 != 201 || first.ID == "" || first != again {
		t.Fatalf("link capture twice: %d %d %s", code, code2, body)
	}
	if got := strings.Join(eventsOf(t, s, "alice@example.com"), ","); got != "Personal/Alice's dentist,Personal/Kept through a link" {
		t.Errorf("alice's events: %s", got)
	}
	if got := eventsOf(t, s, "bob@example.com"); len(got) != 1 {
		t.Errorf("bob's events: %v", got)
	}
	if k := keptEvent(t, alice, first.ID).Event; k.Source != "hub" || k.Start != tomorrow || !k.AllDay {
		t.Errorf("the event kept through the link: %+v", k)
	}
	if code, _, body := capture(t, hub, map[string]any{"request_id": "hub-0000000000000009"}); code != 400 {
		t.Errorf("link capture with nothing: %d %s", code, body)
	}
}

// The manifest Core gets says what Calendar can do, so nobody has to list it.
func TestManifestOffersCapabilities(t *testing.T) {
	s, _ := apiServer(t)
	b, err := json.Marshal(s.manifest())
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal(b, &sent)
	if sent["slug"] != "calendar" || sent["category"] != "Everyday" {
		t.Errorf("manifest: slug %v, category %v", sent["slug"], sent["category"])
	}
	for _, k := range []string{"search", "day", "widget", "capture"} {
		if sent[k] == nil {
			t.Errorf("manifest has no %q", k)
		}
	}
	bangs, _ := sent["search"].(map[string]any)["bangs"].([]any)
	if len(bangs) != 2 || bangs[0] != "cal" || bangs[1] != "calendar" {
		t.Errorf("search bangs: %v", bangs)
	}
	if feed, _ := sent["day"].(map[string]any); feed["label"] != "Calendar" || feed["one"] != "event" || feed["many"] != "events" || feed["unit"] != "count" {
		t.Errorf("day: %v", sent["day"])
	}
	if w, _ := sent["widget"].(map[string]any); w["title"] != "Agenda" {
		t.Errorf("widget: %v", sent["widget"])
	}
	kinds, _ := sent["capture"].(map[string]any)["kinds"].([]any)
	if len(kinds) != 1 || kinds[0].(map[string]any)["kind"] != "event" {
		t.Errorf("capture kinds: %v", sent["capture"])
	}
	// Register adds one offer per capability, next to the ones Calendar already had
	offered := map[string]bool{}
	for _, o := range capabilities.Offers() {
		offered[o.Scope] = true
	}
	for _, sc := range []string{core.ScopeSearch, core.ScopeDay, core.ScopeWidget, core.ScopeCapture} {
		if !offered[sc] {
			t.Errorf("no offer for %s: %v", sc, offered)
		}
	}
	old := map[string]bool{}
	for _, o := range s.manifest().Offers {
		old[o.Scope] = true
	}
	if !old["calendar:read"] || !old["calendar:write"] {
		t.Errorf("the calendar's own offers: %v", old)
	}
}
