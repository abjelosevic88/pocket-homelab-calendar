package server

import (
	"context"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// seedDemo fills an empty box with a made-up week (DEMO_DATA=true).
func (s *Server) seedDemo(ctx context.Context) error {
	n, _ := s.store.CountUsers(ctx)
	var u *store.User
	var err error
	if n == 0 {
		u, err = s.firstOrSystemUser(ctx, "admin@localhost", "Alex")
		if err != nil {
			return err
		}
	} else {
		users, _ := s.store.ListUsers(ctx)
		u = &users[0]
	}
	s.seedUser(ctx, u)
	if c, _ := s.store.UserCounts(ctx, u.ID)["events"]; c > 0 {
		return nil
	}
	personal, err := s.store.DefaultCalendar(ctx, u.ID)
	if err != nil {
		return err
	}
	work := &store.Calendar{UserID: u.ID, Name: "Work", Color: palette[4]}
	family := &store.Calendar{UserID: u.ID, Name: "Family", Color: palette[1], Description: "Shared with the household"}
	for _, c := range []*store.Calendar{work, family} {
		if err := s.store.CreateCalendar(ctx, c); err != nil {
			return err
		}
	}
	_ = s.store.UpsertShare(ctx, &store.Share{CalendarID: family.ID, Everyone: true, Role: store.RoleWrite}, u.ID)
	loc := s.loc(ctx, u.ID)
	now := time.Now().In(loc)
	monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	for monday.Weekday() != time.Monday {
		monday = monday.AddDate(0, 0, -1)
	}
	at := func(day, h, m int) string {
		return monday.AddDate(0, 0, day).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).Format(time.RFC3339)
	}
	date := func(day int) string { return monday.AddDate(0, 0, day).Format("2006-01-02") }
	str := func(v string) *string { return &v }
	b := func(v bool) *bool { return &v }
	tz := loc.String()
	type ev struct {
		cal *store.Calendar
		p   eventPatch
	}
	evs := []ev{
		{work, eventPatch{Title: str("Standup"), Start: str(at(0, 9, 15)), End: str(at(0, 9, 30)), RRule: str("FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"), Conference: str("https://meet.jit.si/pocket-standup"), TZ: &tz}},
		{work, eventPatch{Title: str("Design review"), Start: str(at(1, 14, 0)), End: str(at(1, 15, 30)), Location: str("Room 2 / Zoom"), Description: str("Walk through the new onboarding flow."), TZ: &tz}},
		{work, eventPatch{Title: str("Focus time"), Start: str(at(2, 10, 0)), End: str(at(2, 12, 0)), Busy: b(true), TZ: &tz}},
		{work, eventPatch{Title: str("1:1 with Maya"), Start: str(at(3, 11, 0)), End: str(at(3, 11, 45)), RRule: str("FREQ=WEEKLY;INTERVAL=2"), TZ: &tz}},
		{personal, eventPatch{Title: str("Gym"), Start: str(at(0, 18, 30)), End: str(at(0, 19, 45)), RRule: str("FREQ=WEEKLY;BYDAY=MO,WE,FR"), TZ: &tz}},
		{personal, eventPatch{Title: str("Dentist"), Start: str(at(2, 16, 0)), End: str(at(2, 16, 45)), Location: str("Dental clinic, Titova 12"), TZ: &tz, Alarms: &[]ical.Alarm{{Trigger: "-PT1H"}}}},
		{family, eventPatch{Title: str("Dinner at grandma's"), Start: str(at(5, 18, 0)), End: str(at(5, 21, 0)), Location: str("Ilidža"), TZ: &tz}},
		{family, eventPatch{Title: str("Weekend in Mostar"), Start: str(date(5)), End: str(date(7)), AllDay: b(true)}},
		{family, eventPatch{Title: str("Bins out"), Start: str(at(3, 20, 0)), End: str(at(3, 20, 15)), RRule: str("FREQ=WEEKLY;BYDAY=TH"), TZ: &tz}},
		{personal, eventPatch{Title: str("Concert: Dubioza kolektiv"), Start: str(at(4, 20, 30)), End: str(at(4, 23, 0)), Location: str("Zetra"), TZ: &tz}},
		{work, eventPatch{Title: str("Quarterly planning"), Start: str(date(9)), End: str(date(10)), AllDay: b(true)}},
	}
	for _, e := range evs {
		p := e.p
		if _, err := s.createEvent(ctx, u, e.cal, &p, "demo"); err != nil {
			return err
		}
	}
	s.log.Info("demo data seeded", "user", u.Email)
	return nil
}
