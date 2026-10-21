package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/quick"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// handleQuickAdd creates an event from plain words ("Lunch with Sani fri
// 13:00 at Kod Keke #family remind 30m"); preview=true only reads it.
func (s *Server) handleQuickAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Text       string `json:"text"`
		CalendarID *int64 `json:"calendar_id"`
		Preview    bool   `json:"preview"`
		TZ         string `json:"tz"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Text) == "" {
		writeErr(w, http.StatusBadRequest, "type what and when")
		return
	}
	loc := s.loc(ctx, u.ID)
	if in.TZ != "" {
		if l, err := ical.LoadZone(in.TZ); err == nil {
			loc = l
		}
	}
	st, _ := s.store.GetSettings(ctx, u.ID)
	res := quick.Parse(in.Text, time.Now(), loc, time.Weekday(st.WeekStart))
	if res.End.IsZero() {
		res.End = res.Start.Add(time.Duration(st.DefaultDurationMin) * time.Minute)
	}

	// "#work" picks a calendar by name
	calID := in.CalendarID
	calName := ""
	cals, _ := s.store.ListCalendarsFor(ctx, u.ID)
	if res.Calendar != "" {
		want := strings.ToLower(res.Calendar)
		for _, c := range cals {
			if store.RoleAtLeast(c.Role, store.RoleWrite) && !c.ReadOnly && strings.HasPrefix(strings.ToLower(strings.ReplaceAll(c.Name, " ", "")), want) {
				id := c.ID
				calID = &id
				break
			}
		}
		if calID == nil || *calID == 0 {
			res.Title = strings.TrimSpace(res.Title + " #" + res.Calendar)
		}
	}
	cal, err := s.targetCalendar(ctx, u, calID)
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	calName = cal.Name

	// "with Sani" invites people on the box (or an email)
	attendees := []ical.Attendee{}
	if len(res.With) > 0 {
		people, _ := s.store.People(ctx)
		for _, name := range res.With {
			ln := strings.ToLower(name)
			if strings.Contains(ln, "@") {
				attendees = append(attendees, ical.Attendee{Email: ln})
				continue
			}
			for _, p := range people {
				if p.ID == u.ID {
					continue
				}
				first := strings.ToLower(strings.Fields(p.Name + " x")[0])
				if first == ln || strings.EqualFold(p.Name, name) || strings.HasPrefix(strings.ToLower(p.Email), ln+"@") {
					attendees = append(attendees, ical.Attendee{Email: p.Email, Name: p.Name})
					break
				}
			}
		}
	}

	start, end := fmtWhen(res.Start, res.AllDay, loc), fmtWhen(res.End, res.AllDay, loc)
	parsed := map[string]any{
		"title": res.Title, "start": start, "end": end, "all_day": res.AllDay, "rrule": res.RRule,
		"repeat_text": ical.DescribeRule(res.RRule), "location": res.Location, "calendar_id": cal.ID, "calendar": calName,
		"alarms": res.Alarms, "attendees": attendees, "recognized": res.Recognized, "has_date": res.HasDate, "has_time": res.HasTime,
		"private": res.Private,
	}
	if in.Preview {
		writeJSON(w, http.StatusOK, map[string]any{"parsed": parsed})
		return
	}
	tz := loc.String()
	p := &eventPatch{Title: &res.Title, Start: &start, End: &end, AllDay: &res.AllDay, CalendarID: &cal.ID, TZ: &tz}
	if res.AllDay {
		p.TZ = nil
	}
	if res.RRule != "" {
		p.RRule = &res.RRule
	}
	if res.Location != "" {
		p.Location = &res.Location
	}
	if len(res.Alarms) > 0 {
		al := []ical.Alarm{}
		for _, a := range res.Alarms {
			al = append(al, ical.Alarm{Trigger: a})
		}
		p.Alarms = &al
	}
	if len(attendees) > 0 {
		p.Attendees = &attendees
	}
	if res.Busy != nil {
		p.Busy = res.Busy
	}
	if res.Private {
		t := true
		p.Private = &t
	}
	row, err := s.createEvent(ctx, u, cal, p, s.source(r))
	if err != nil {
		s.fail(w, err, "create event")
		return
	}
	v, err := s.viewerFor(ctx, u, in.TZ)
	if err != nil {
		s.fail(w, err, "load calendars")
		return
	}
	rs, err := s.resource(ctx, row)
	if err != nil {
		s.fail(w, err, "read event")
		return
	}
	o, _ := rs.Occurrence("")
	out := map[string]any{"parsed": parsed}
	if o != nil {
		out["event"] = s.occurrenceView(ctx, v, row, *o, nil)
	}
	writeJSON(w, http.StatusCreated, out)
}
