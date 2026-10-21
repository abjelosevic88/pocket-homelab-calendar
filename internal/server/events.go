package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// eventPatch is the body of create and update; absent keys stay as they are.
type eventPatch struct {
	CalendarID  *int64           `json:"calendar_id"`
	Title       *string          `json:"title"`
	Description *string          `json:"description"`
	Location    *string          `json:"location"`
	URL         *string          `json:"url"`
	Conference  *string          `json:"conference"`
	AllDay      *bool            `json:"all_day"`
	Start       *string          `json:"start"`
	End         *string          `json:"end"`
	TZ          *string          `json:"tz"`
	RRule       *string          `json:"rrule"`
	Status      *string          `json:"status"`
	Busy        *bool            `json:"busy"`
	Private     *bool            `json:"private"`
	Color       *string          `json:"color"`
	Categories  *[]string        `json:"categories"`
	Alarms      *[]ical.Alarm    `json:"alarms"`
	Attendees   *[]ical.Attendee `json:"attendees"`
}

func (p *eventPatch) timesChanged() bool {
	return p.Start != nil || p.End != nil || p.AllDay != nil || p.TZ != nil
}

// parseWhen reads an API date: "YYYY-MM-DD", RFC 3339, or a local
// "YYYY-MM-DDTHH:MM[:SS]" in zone.
func parseWhen(v string, allDay bool, zone *time.Location) (time.Time, error) {
	v = strings.TrimSpace(v)
	if allDay {
		if len(v) >= 10 {
			if t, err := time.ParseInLocation("2006-01-02", v[:10], zone); err == nil {
				return t, nil
			}
		}
		return time.Time{}, badRequest("an all-day date must be YYYY-MM-DD")
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.In(zone), nil
	}
	for _, l := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(l, v, zone); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", v, zone); err == nil {
		return t.Add(9 * time.Hour), nil
	}
	return time.Time{}, badRequest("times must be RFC 3339 or YYYY-MM-DDTHH:MM")
}

func midnight(t time.Time, z *time.Location) time.Time {
	t = t.In(z)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, z)
}

// buildInput turns a patch into a VEVENT change. cur is the component's
// current reading; shown is the start of the occurrence the person edited
// (differs from cur.Start when a whole series is moved through one of its
// occurrences).
func (s *Server) buildInput(p *eventPatch, cur *ical.Event, shown time.Time, defZone *time.Location, defDur time.Duration) (ical.EventInput, error) {
	in := ical.EventInput{
		Summary: p.Title, Description: p.Description, Location: p.Location, URL: p.URL, Conference: p.Conference,
		Status: p.Status, Color: p.Color, Categories: p.Categories, Alarms: p.Alarms, RRule: p.RRule,
	}
	if p.Busy != nil {
		v := "opaque"
		if !*p.Busy {
			v = "transparent"
		}
		in.Transp = &v
	}
	if p.Private != nil {
		v := "public"
		if *p.Private {
			v = "private"
		}
		in.Class = &v
	}
	if p.RRule != nil && *p.RRule != "" {
		r := strings.TrimPrefix(strings.TrimSpace(*p.RRule), "RRULE:")
		if ical.DescribeRule(r) == "Repeats" || !strings.Contains(strings.ToUpper(r), "FREQ=") {
			return in, badRequest("rrule is not a valid RRULE")
		}
		in.RRule = &r
	}
	if p.Attendees != nil {
		clean := []ical.Attendee{}
		for _, a := range *p.Attendees {
			a.Email = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(a.Email, "mailto:")))
			if a.Email == "" || !strings.Contains(a.Email, "@") {
				continue
			}
			a.Status = strings.ToUpper(a.Status)
			a.Role = strings.ToUpper(a.Role)
			clean = append(clean, a)
		}
		in.Attendees = &clean
	}
	if !p.timesChanged() && !cur.Start.IsZero() {
		return in, nil
	}
	allDay := cur.AllDay
	if p.AllDay != nil {
		allDay = *p.AllDay
	}
	tzid := cur.TZID
	if p.TZ != nil {
		tzid = strings.TrimSpace(*p.TZ)
	}
	zone := defZone
	if tzid != "" {
		z, err := ical.LoadZone(tzid)
		if err != nil {
			return in, badRequest("unknown time zone " + tzid)
		}
		zone = z
	} else if cur.Start.IsZero() && !allDay {
		tzid = defZone.String()
		if tzid == "Local" {
			tzid = ""
		}
	}
	if allDay {
		tzid = ""
	}
	if shown.IsZero() {
		shown = cur.Start
	}
	curDur := cur.End.Sub(cur.Start)
	// the occurrence as shown, before the change
	var start, end time.Time
	switch {
	case p.Start != nil:
		t, err := parseWhen(*p.Start, allDay, zone)
		if err != nil {
			return in, err
		}
		start = t
	case shown.IsZero():
		return in, badRequest("start is required")
	case allDay && !cur.AllDay:
		start = midnight(shown, zone)
	case !allDay && cur.AllDay:
		start = midnight(shown, zone).Add(9 * time.Hour)
	default:
		start = shown.In(zone)
	}
	switch {
	case p.End != nil && *p.End != "":
		t, err := parseWhen(*p.End, allDay, zone)
		if err != nil {
			return in, err
		}
		end = t
	case allDay && (cur.Start.IsZero() || !cur.AllDay):
		end = start.AddDate(0, 0, 1)
	case allDay:
		days := int((curDur + 12*time.Hour) / (24 * time.Hour))
		if days < 1 {
			days = 1
		}
		end = start.AddDate(0, 0, days)
	case cur.Start.IsZero() || cur.AllDay:
		end = start.Add(defDur)
	default:
		end = start.Add(curDur)
	}
	if allDay && !end.After(start) {
		end = start.AddDate(0, 0, 1)
	}
	if end.Before(start) {
		return in, badRequest("the event ends before it starts")
	}
	// move the component by the same amount the occurrence moved
	if !cur.Start.IsZero() && !shown.Equal(cur.Start) {
		if allDay {
			days := int(midnight(start, zone).Sub(midnight(shown, zone)).Round(24*time.Hour) / (24 * time.Hour))
			dur := end.Sub(start)
			start = midnight(cur.Start, zone).AddDate(0, 0, days)
			end = start.Add(dur).Round(time.Hour)
			if !end.After(start) {
				end = start.AddDate(0, 0, 1)
			}
		} else {
			dur := end.Sub(start)
			start = cur.Start.Add(start.Sub(shown.In(zone)))
			end = start.Add(dur)
		}
	}
	in.AllDay, in.Start, in.End, in.TZID = allDay, &start, &end, &tzid
	return in, nil
}

// ---------- handlers ----------

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, err := s.viewerFor(ctx, userFrom(ctx), r.URL.Query().Get("tz"))
	if err != nil {
		s.fail(w, err, "load calendars")
		return
	}
	from, to, err := parseRange(r.URL.Query().Get("from"), r.URL.Query().Get("to"), v.loc)
	if err != nil {
		s.fail(w, err, "range")
		return
	}
	var ids []int64
	if c := r.URL.Query().Get("calendars"); c != "" {
		ids = []int64{}
		for _, p := range strings.Split(c, ",") {
			id, _ := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
			if _, ok := v.roles[id]; ok {
				ids = append(ids, id)
			}
		}
	}
	occ, err := s.occurrences(ctx, v, ids, from, to, r.URL.Query().Get("invites") != "0")
	if err != nil {
		s.fail(w, err, "list events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": occ, "from": from.In(v.loc).Format(time.RFC3339), "to": to.In(v.loc).Format(time.RFC3339), "tz": v.loc.String()})
}

// loadEvent finds an event the person may see (own/shared calendar or an
// invitation) and their role on it ("" for an invitation).
func (s *Server) loadEvent(ctx context.Context, u *store.User, id int64) (*store.EventRow, string, error) {
	row, err := s.store.GetEvent(ctx, id)
	if err != nil {
		return nil, "", err
	}
	role, _, err := s.store.CalendarRole(ctx, u.ID, row.CalendarID)
	if errors.Is(err, store.ErrNotFound) {
		if s.store.IsAttendee(ctx, row.ID, u.ID) {
			return row, "", nil
		}
		return nil, "", store.ErrNotFound
	}
	return row, role, err
}

func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	id, key, err := parseOccID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if k := r.URL.Query().Get("key"); k != "" {
		key = k
	}
	row, role, err := s.loadEvent(ctx, u, id)
	if err != nil {
		s.fail(w, err, "get event")
		return
	}
	v, err := s.viewerFor(ctx, u, r.URL.Query().Get("tz"))
	if err != nil {
		s.fail(w, err, "load calendars")
		return
	}
	res, err := s.resource(ctx, row)
	if err != nil {
		s.fail(w, err, "read event")
		return
	}
	o, ok := res.Occurrence(key)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such occurrence")
		return
	}
	var inv *InviteView
	if role == "" {
		inv = &InviteView{Status: myStatus(o.Event, u.Email)}
	}
	oc := s.occurrenceView(ctx, v, row, *o, inv)
	out := map[string]any{"event": oc, "role": role}
	if !oc.Masked {
		if cal, ok := v.cals[row.CalendarID]; ok {
			out["calendar"] = map[string]any{"id": cal.ID, "name": cal.Name, "color": firstNonEmpty(cal.MyColor, cal.Color), "owner": cal.Owner}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// targetCalendar picks and checks the calendar a new event goes to.
func (s *Server) targetCalendar(ctx context.Context, u *store.User, id *int64) (*store.Calendar, error) {
	if id == nil || *id == 0 {
		st, _ := s.store.GetSettings(ctx, u.ID)
		if st.DefaultCalendarID > 0 {
			if role, c, err := s.store.CalendarRole(ctx, u.ID, st.DefaultCalendarID); err == nil && store.RoleAtLeast(role, store.RoleWrite) && !c.ReadOnly {
				return c, nil
			}
		}
		c, err := s.store.DefaultCalendar(ctx, u.ID)
		if errors.Is(err, store.ErrNotFound) {
			s.seedUser(ctx, u)
			return s.store.DefaultCalendar(ctx, u.ID)
		}
		return c, err
	}
	role, c, err := s.store.CalendarRole(ctx, u.ID, *id)
	if err != nil {
		return nil, err
	}
	if !store.RoleAtLeast(role, store.RoleWrite) {
		return nil, errForbidden("you can only look at this calendar")
	}
	if c.ReadOnly {
		return nil, errForbidden("this calendar is read-only")
	}
	return c, nil
}

type forbidden struct{ msg string }

func (e forbidden) Error() string { return e.msg }

func errForbidden(msg string) error { return forbidden{msg} }

// createEvent makes a new event from a patch in a calendar.
func (s *Server) createEvent(ctx context.Context, u *store.User, cal *store.Calendar, p *eventPatch, source string) (*store.EventRow, error) {
	if p.Start == nil || *p.Start == "" {
		return nil, badRequest("start is required")
	}
	if p.Title == nil || strings.TrimSpace(*p.Title) == "" {
		t := "New event"
		p.Title = &t
	}
	st, _ := s.store.GetSettings(ctx, u.ID)
	allDay := p.AllDay != nil && *p.AllDay
	if p.Alarms == nil {
		al := []ical.Alarm{}
		def := st.DefaultAlarms
		if allDay {
			def = st.AllDayAlarms
		}
		if len(cal.DefaultAlarms) > 0 {
			def = cal.DefaultAlarms
		}
		for _, t := range def {
			al = append(al, ical.Alarm{Trigger: t})
		}
		p.Alarms = &al
	}
	uid := store.RandomToken(16) + "@pocket-calendar"
	comp := ical.NewEvent(uid)
	cur := &ical.Event{}
	in, err := s.buildInput(p, cur, time.Time{}, s.loc(ctx, u.ID), time.Duration(st.DefaultDurationMin)*time.Minute)
	if err != nil {
		return nil, err
	}
	if in.Attendees != nil && len(*in.Attendees) > 0 {
		in.Organizer = &ical.Attendee{Email: u.Email, Name: u.Name}
	}
	ical.ApplyEvent(comp, in)
	c := ical.NewCalendar()
	c.Children = append(c.Children, comp)
	row, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: cal.ID, ICS: c.String(), Loc: s.calLoc(ctx, cal.ID), Source: source, Actor: u.ID},
		s.eventHook(ctx, "created", cal, uid, *p.Title, source))
	if err != nil {
		return nil, err
	}
	s.afterWrite(ctx, u, nil, row, true)
	return row, nil
}

func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var p eventPatch
	if err := decode(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	cal, err := s.targetCalendar(ctx, u, p.CalendarID)
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	row, err := s.createEvent(ctx, u, cal, &p, s.source(r))
	if err != nil {
		s.fail(w, err, "create event")
		return
	}
	s.respondEvent(w, r, http.StatusCreated, row, "")
}

func (s *Server) respondEvent(w http.ResponseWriter, r *http.Request, status int, row *store.EventRow, key string) {
	ctx := r.Context()
	v, err := s.viewerFor(ctx, userFrom(ctx), r.URL.Query().Get("tz"))
	if err != nil {
		s.fail(w, err, "load calendars")
		return
	}
	res, err := s.resource(ctx, row)
	if err != nil {
		s.fail(w, err, "read event")
		return
	}
	o, ok := res.Occurrence(key)
	if !ok {
		o, _ = res.Occurrence("")
	}
	if o == nil {
		writeJSON(w, status, map[string]any{"id": row.ID})
		return
	}
	writeJSON(w, status, s.occurrenceView(ctx, v, row, *o, nil))
}

// editable checks a person may change an event; link tokens may only touch
// events their app created.
func (s *Server) editable(r *http.Request, row *store.EventRow, role string) error {
	if !store.RoleAtLeast(role, store.RoleWrite) {
		return errForbidden("you can't change events in this calendar")
	}
	if cal, err := s.store.GetCalendar(r.Context(), row.CalendarID); err == nil && cal.ReadOnly {
		return errForbidden("this calendar is read-only")
	}
	if app := s.linkApp(r); app != "" && row.Source != app {
		return errForbidden("Pocket " + app + " can only change events it created")
	}
	return nil
}

func scopeOf(r *http.Request, key string) string {
	sc := r.URL.Query().Get("scope")
	switch sc {
	case "this", "following", "all":
		return sc
	}
	if key == "" {
		return "all"
	}
	return "this"
}

func (s *Server) handleUpdateEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	id, key, err := parseOccID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if k := r.URL.Query().Get("key"); k != "" {
		key = k
	}
	row, role, err := s.loadEvent(ctx, u, id)
	if err != nil {
		s.fail(w, err, "get event")
		return
	}
	if err := s.editable(r, row, role); err != nil {
		s.fail(w, err, "update event")
		return
	}
	var p eventPatch
	if err := decode(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if ifm := r.Header.Get("If-Match"); ifm != "" && ifm != row.ETag {
		writeErr(w, http.StatusPreconditionFailed, "the event changed meanwhile — reload it")
		return
	}
	row, newKey, err := s.updateEvent(ctx, u, row, key, scopeOf(r, key), &p, s.source(r))
	if err != nil {
		s.fail(w, err, "update event")
		return
	}
	s.respondEvent(w, r, http.StatusOK, row, newKey)
}

func cloneComp(c *ical.Component) *ical.Component {
	cp, err := ical.Parse(c.String())
	if err != nil {
		return c
	}
	return cp
}

// updateEvent applies a patch to one occurrence, this and the following
// ones, or the whole series. It returns the row to show and its key.
func (s *Server) updateEvent(ctx context.Context, u *store.User, row *store.EventRow, key, scope string, p *eventPatch, source string) (*store.EventRow, string, error) {
	loc := s.calLoc(ctx, row.CalendarID)
	before, err := ical.ParseResource(row.ICS, loc)
	if err != nil {
		return nil, "", err
	}
	res, _ := ical.ParseResource(row.ICS, loc)
	st, _ := s.store.GetSettings(ctx, u.ID)
	defDur := time.Duration(st.DefaultDurationMin) * time.Minute
	viewLoc := s.loc(ctx, u.ID)
	m := res.MasterEvent()
	recurring := res.Master != nil && (m.RRule != "" || len(m.RDates) > 0)
	if !recurring || key == "" {
		scope = "all"
	}
	var occStart time.Time
	if key != "" {
		t, _, ok := ical.ParseRecurrenceKey(key, m.Zone)
		if !ok {
			return nil, "", badRequest("bad occurrence key")
		}
		occStart = t.In(m.Zone)
		if scope == "following" && occStart.Equal(m.Start) {
			scope = "all"
		}
	}
	if p.CalendarID != nil && *p.CalendarID != row.CalendarID && scope != "all" {
		return nil, "", badRequest("only a whole series can move to another calendar")
	}
	if p.RRule != nil && scope == "this" {
		return nil, "", badRequest("a single occurrence cannot repeat; edit the series instead")
	}
	retKey := key
	switch scope {
	case "all":
		comp := res.Master
		cur := m
		shown := time.Time{}
		if comp == nil { // only overrides: edit the one asked for
			comp = res.Overrides[key]
			if comp == nil {
				return nil, "", store.ErrNotFound
			}
			cur = ical.ReadEvent(comp, loc)
		} else if key != "" {
			if o, ok := res.Occurrence(key); ok {
				shown = o.Start
				if o.Override {
					// moving the series through a moved occurrence: measure from where it would have been
					shown = occStart
				}
			}
		}
		in, err := s.buildInput(p, cur, shown, viewLoc, defDur)
		if err != nil {
			return nil, "", err
		}
		s.organizerFor(u, cur, &in)
		ical.ApplyEvent(comp, in)
		if in.Start != nil && recurring && !in.Start.Equal(cur.Start) {
			// the occurrences moved: drop exceptions keyed on the old times
			delta := in.Start.Sub(cur.Start)
			comp.Remove("EXDATE")
			for k, o := range res.Overrides {
				_ = k
				removeChild(res.Cal, o)
			}
			res.Overrides = nil
			if key != "" {
				retKey = ical.RecurrenceKey(occStart.Add(delta), in.AllDay)
			}
		}
		if in.Start != nil && !recurring {
			retKey = ""
		}
	case "this":
		comp := res.Overrides[key]
		var cur *ical.Event
		if comp == nil {
			o, ok := res.Occurrence(key)
			if !ok {
				return nil, "", store.ErrNotFound
			}
			comp = cloneComp(res.Master)
			for _, n := range []string{"RRULE", "RDATE", "EXDATE"} {
				comp.Remove(n)
			}
			comp.Set(ical.MomentProperty("RECURRENCE-ID", occStart, m.AllDay, m.TZID))
			comp.Set(ical.MomentProperty("DTSTART", o.Start, m.AllDay, m.TZID))
			comp.Remove("DURATION")
			comp.Set(ical.MomentProperty("DTEND", o.End, m.AllDay, m.TZID))
			res.Cal.Children = append(res.Cal.Children, comp)
		}
		cur = ical.ReadEvent(comp, loc)
		in, err := s.buildInput(p, cur, time.Time{}, viewLoc, defDur)
		if err != nil {
			return nil, "", err
		}
		s.organizerFor(u, cur, &in)
		ical.ApplyEvent(comp, in)
	case "following":
		set, err := res.RuleSet(m)
		if err != nil || set == nil {
			return nil, "", badRequest("the series rule cannot be read")
		}
		done := len(set.Between(m.Start, occStart, false))
		newComp := cloneComp(res.Master)
		newUID := store.RandomToken(16) + "@pocket-calendar"
		newComp.SetRaw("UID", newUID)
		dur := m.End.Sub(m.Start)
		newComp.Set(ical.MomentProperty("DTSTART", occStart, m.AllDay, m.TZID))
		newComp.Remove("DURATION")
		newComp.Set(ical.MomentProperty("DTEND", occStart.Add(dur), m.AllDay, m.TZID))
		if m.RRule != "" {
			newComp.SetRaw("RRULE", ical.RemainingRule(m.RRule, done))
		}
		newComp.SetRaw("SEQUENCE", "0")
		cur := ical.ReadEvent(newComp, loc)
		in, err := s.buildInput(p, cur, time.Time{}, viewLoc, defDur)
		if err != nil {
			return nil, "", err
		}
		s.organizerFor(u, cur, &in)
		ical.ApplyEvent(newComp, in)
		// the old series ends before this occurrence
		res.Master.SetRaw("RRULE", ical.TruncateRule(m.RRule, occStart, m.AllDay))
		for k, o := range res.Overrides {
			if t, _, ok := ical.ParseRecurrenceKey(k, m.Zone); ok && !t.Before(occStart) {
				removeChild(res.Cal, o)
			}
		}
		ical.Touch(res.Master)
		cal, err := s.store.GetCalendar(ctx, row.CalendarID)
		if err != nil {
			return nil, "", err
		}
		saved, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: row.CalendarID, Href: row.Href, ICS: res.Cal.String(), Loc: loc, Source: source, Actor: u.ID},
			s.eventHook(ctx, "updated", cal, row.UID, m.Summary, source))
		if err != nil {
			return nil, "", err
		}
		s.afterWrite(ctx, u, before, saved, false)
		nc := ical.NewCalendar()
		nc.Children = append(nc.Children, newComp)
		title := cur.Summary
		if p.Title != nil {
			title = *p.Title
		}
		created, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: row.CalendarID, ICS: nc.String(), Loc: loc, Source: source, Actor: u.ID},
			s.eventHook(ctx, "created", cal, newUID, title, source))
		if err != nil {
			return nil, "", err
		}
		s.afterWrite(ctx, u, nil, created, true)
		nr, _ := ical.ParseResource(created.ICS, loc)
		first := ""
		if nr != nil {
			ne := nr.MasterEvent()
			first = ical.RecurrenceKey(ne.Start, ne.AllDay)
		}
		return created, first, nil
	}
	cal, err := s.store.GetCalendar(ctx, row.CalendarID)
	if err != nil {
		return nil, "", err
	}
	ics := res.Cal.String()
	if p.CalendarID != nil && *p.CalendarID != row.CalendarID {
		target, err := s.targetCalendar(ctx, u, p.CalendarID)
		if err != nil {
			return nil, "", err
		}
		moved, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: target.ID, Href: row.Href, ICS: ics, Loc: s.calLoc(ctx, target.ID), Source: row.Source, Actor: u.ID},
			s.eventHook(ctx, "created", target, row.UID, m.Summary, source))
		if err != nil {
			return nil, "", err
		}
		if err := s.store.DeleteEvent(ctx, row, store.DeleteEventOpts{Actor: u.ID}, s.eventHook(ctx, "deleted", cal, row.UID, m.Summary, source)); err != nil {
			return nil, "", err
		}
		s.afterWrite(ctx, u, before, moved, false)
		return moved, retKey, nil
	}
	saved, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: row.CalendarID, Href: row.Href, ICS: ics, Loc: loc, Source: source, Actor: u.ID},
		s.eventHook(ctx, "updated", cal, row.UID, m.Summary, source))
	if err != nil {
		return nil, "", err
	}
	s.afterWrite(ctx, u, before, saved, false)
	return saved, retKey, nil
}

// organizerFor makes the person the organizer when they add attendees to
// an event that has none, and drops the organizer with the last attendee.
func (s *Server) organizerFor(u *store.User, cur *ical.Event, in *ical.EventInput) {
	if in.Attendees == nil {
		return
	}
	if len(*in.Attendees) == 0 {
		in.Organizer = &ical.Attendee{}
		return
	}
	if cur.Organizer == nil || cur.Organizer.Email == "" {
		in.Organizer = &ical.Attendee{Email: u.Email, Name: u.Name}
	}
}

func removeChild(parent, child *ical.Component) {
	out := parent.Children[:0]
	for _, c := range parent.Children {
		if c != child {
			out = append(out, c)
		}
	}
	parent.Children = out
}

func (s *Server) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	id, key, err := parseOccID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if k := r.URL.Query().Get("key"); k != "" {
		key = k
	}
	row, role, err := s.loadEvent(ctx, u, id)
	if err != nil {
		s.fail(w, err, "get event")
		return
	}
	if err := s.editable(r, row, role); err != nil {
		s.fail(w, err, "delete event")
		return
	}
	deletedID, err := s.deleteEvent(ctx, u, row, key, scopeOf(r, key), s.source(r))
	if err != nil {
		s.fail(w, err, "delete event")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restore_id": deletedID})
}

// deleteEvent removes one occurrence, this and the following ones, or the
// whole series. A whole delete is kept 30 days under Recently deleted.
func (s *Server) deleteEvent(ctx context.Context, u *store.User, row *store.EventRow, key, scope, source string) (int64, error) {
	loc := s.calLoc(ctx, row.CalendarID)
	res, err := ical.ParseResource(row.ICS, loc)
	if err != nil {
		return 0, err
	}
	cal, err := s.store.GetCalendar(ctx, row.CalendarID)
	if err != nil {
		return 0, err
	}
	m := res.MasterEvent()
	recurring := res.Master != nil && (m.RRule != "" || len(m.RDates) > 0)
	if !recurring || key == "" {
		scope = "all"
	}
	var occStart time.Time
	if key != "" {
		t, _, ok := ical.ParseRecurrenceKey(key, m.Zone)
		if !ok {
			return 0, badRequest("bad occurrence key")
		}
		occStart = t.In(m.Zone)
		if scope == "following" && !occStart.After(m.Start) {
			scope = "all"
		}
	}
	switch scope {
	case "this":
		if o, ok := res.Overrides[key]; ok {
			removeChild(res.Cal, o)
		}
		ex := append(m.ExDates, occStart)
		ical.ApplyEvent(res.Master, ical.EventInput{ExDates: &ex})
	case "following":
		res.Master.SetRaw("RRULE", ical.TruncateRule(m.RRule, occStart, m.AllDay))
		for k, o := range res.Overrides {
			if t, _, ok := ical.ParseRecurrenceKey(k, m.Zone); ok && !t.Before(occStart) {
				removeChild(res.Cal, o)
			}
		}
		ical.Touch(res.Master)
	default:
		var restoreID int64
		err := s.store.DeleteEvent(ctx, row, store.DeleteEventOpts{Actor: u.ID, KeepCopy: true}, s.eventHook(ctx, "deleted", cal, row.UID, m.Summary, source))
		if err != nil {
			return 0, err
		}
		_ = s.store.DB.QueryRowContext(ctx, `SELECT MAX(id) FROM deleted_events WHERE user_id = ? AND uid = ?`, u.ID, row.UID).Scan(&restoreID)
		s.afterDelete(ctx, u, row)
		return restoreID, nil
	}
	saved, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: row.CalendarID, Href: row.Href, ICS: res.Cal.String(), Loc: loc, Source: source, Actor: u.ID},
		s.eventHook(ctx, "updated", cal, row.UID, m.Summary, source))
	if err != nil {
		return 0, err
	}
	s.afterWrite(ctx, u, nil, saved, false)
	return 0, nil
}

// handleRSVP answers an invitation: accepted, declined or tentative, for the
// whole series or one occurrence.
func (s *Server) handleRSVP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	id, key, err := parseOccID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	var in struct {
		Status string `json:"status"`
		Key    string `json:"key"`
		Scope  string `json:"scope"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Key != "" {
		key = in.Key
	}
	status := strings.ToUpper(in.Status)
	switch status {
	case "ACCEPTED", "DECLINED", "TENTATIVE", "NEEDS-ACTION":
	default:
		writeErr(w, http.StatusBadRequest, "status must be accepted, declined or tentative")
		return
	}
	row, _, err := s.loadEvent(ctx, u, id)
	if err != nil {
		s.fail(w, err, "get event")
		return
	}
	if !s.store.IsAttendee(ctx, row.ID, u.ID) {
		writeErr(w, http.StatusBadRequest, "you are not invited to this event")
		return
	}
	loc := s.calLoc(ctx, row.CalendarID)
	res, err := ical.ParseResource(row.ICS, loc)
	if err != nil {
		s.fail(w, err, "read event")
		return
	}
	set := func(c *ical.Component) {
		var props []ical.Property
		for _, p := range c.Props {
			if p.Name == "ATTENDEE" {
				email := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(p.Value, "mailto:"), "MAILTO:"))
				if email == u.Email {
					p.SetParam("PARTSTAT", status)
					var params []ical.Param
					for _, pa := range p.Params {
						if pa.Name != "RSVP" {
							params = append(params, pa)
						}
					}
					p.Params = params
				}
			}
			props = append(props, p)
		}
		c.Props = props
		now := time.Now().UTC()
		c.Set(ical.UTCProperty("DTSTAMP", now))
		c.Set(ical.UTCProperty("LAST-MODIFIED", now))
	}
	if in.Scope == "this" && key != "" && res.Master != nil {
		comp, ok := res.Overrides[key]
		if !ok {
			m := res.MasterEvent()
			o, ok := res.Occurrence(key)
			if !ok {
				writeErr(w, http.StatusNotFound, "no such occurrence")
				return
			}
			occStart, _, _ := ical.ParseRecurrenceKey(key, m.Zone)
			comp = cloneComp(res.Master)
			for _, n := range []string{"RRULE", "RDATE", "EXDATE"} {
				comp.Remove(n)
			}
			comp.Set(ical.MomentProperty("RECURRENCE-ID", occStart, m.AllDay, m.TZID))
			comp.Set(ical.MomentProperty("DTSTART", o.Start, m.AllDay, m.TZID))
			comp.Remove("DURATION")
			comp.Set(ical.MomentProperty("DTEND", o.End, m.AllDay, m.TZID))
			res.Cal.Children = append(res.Cal.Children, comp)
		}
		set(comp)
	} else {
		for _, c := range res.Cal.ChildrenNamed("VEVENT") {
			set(c)
		}
	}
	cal, _ := s.store.GetCalendar(ctx, row.CalendarID)
	saved, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: row.CalendarID, Href: row.Href, ICS: res.Cal.String(), Loc: loc, Source: "rsvp", Actor: u.ID},
		s.rsvpHook(ctx, cal, row, u, status))
	if err != nil {
		s.fail(w, err, "save answer")
		return
	}
	s.touch()
	m := res.MasterEvent()
	if m.Organizer != nil {
		if oid := s.store.UserIDByEmail(ctx, m.Organizer.Email); oid > 0 && oid != u.ID {
			verb := map[string]string{"ACCEPTED": "accepted", "DECLINED": "declined", "TENTATIVE": "might come to", "NEEDS-ACTION": "has not answered"}[status]
			s.notifyUser(ctx, oid, "invites", firstNonEmpty(u.Name, u.Email)+" "+verb+" "+m.Summary, whenText(m, s.loc(ctx, oid)), s.eventLink(saved.ID, key))
		}
	}
	s.respondEvent(w, r, http.StatusOK, saved, key)
}

func (s *Server) handleInvitations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	rows, err := s.store.PendingInvites(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "invitations")
		return
	}
	v, err := s.viewerFor(ctx, u, r.URL.Query().Get("tz"))
	if err != nil {
		s.fail(w, err, "load calendars")
		return
	}
	now := time.Now()
	out := []Occurrence{}
	for _, row := range rows {
		res, err := s.resource(ctx, row)
		if err != nil {
			continue
		}
		occ := res.Expand(now, now.AddDate(2, 0, 0))
		if len(occ) == 0 {
			continue
		}
		out = append(out, s.occurrenceView(ctx, v, row, occ[0], &InviteView{Status: myStatus(occ[0].Event, u.Email)}))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleEventICS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _, _ := parseOccID(chi.URLParam(r, "id"))
	row, role, err := s.loadEvent(ctx, userFrom(ctx), id)
	if err != nil {
		s.fail(w, err, "get event")
		return
	}
	if role == store.RoleFreeBusy || (role == store.RoleRead && row.Class != "public") {
		writeErr(w, http.StatusForbidden, "you can only see that this time is taken")
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.ics"`, store.Slug(firstNonEmpty(row.Summary, "event"))))
	_, _ = w.Write([]byte(row.ICS))
}

func (s *Server) handleDuplicateEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	id, _, _ := parseOccID(chi.URLParam(r, "id"))
	row, role, err := s.loadEvent(ctx, u, id)
	if err != nil {
		s.fail(w, err, "get event")
		return
	}
	if role == store.RoleFreeBusy || role == "" {
		writeErr(w, http.StatusForbidden, "you can't copy this event")
		return
	}
	var in struct {
		CalendarID *int64 `json:"calendar_id"`
	}
	_ = decode(r, &in)
	if in.CalendarID == nil {
		in.CalendarID = &row.CalendarID
	}
	cal, err := s.targetCalendar(ctx, u, in.CalendarID)
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	res, err := ical.ParseResource(row.ICS, s.calLoc(ctx, row.CalendarID))
	if err != nil || res.Master == nil {
		writeErr(w, http.StatusBadRequest, "this event cannot be copied")
		return
	}
	uid := store.RandomToken(16) + "@pocket-calendar"
	comp := cloneComp(res.Master)
	comp.SetRaw("UID", uid)
	comp.SetRaw("SEQUENCE", "0")
	comp.Remove("ORGANIZER")
	comp.Remove("ATTENDEE")
	comp.Remove("CREATED")
	ical.Touch(comp)
	c := ical.NewCalendar()
	c.Children = append(c.Children, comp)
	saved, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: cal.ID, ICS: c.String(), Loc: s.calLoc(ctx, cal.ID), Source: s.source(r), Actor: u.ID},
		s.eventHook(ctx, "created", cal, uid, row.Summary, s.source(r)))
	if err != nil {
		s.fail(w, err, "copy event")
		return
	}
	s.afterWrite(ctx, u, nil, saved, true)
	s.respondEvent(w, r, http.StatusCreated, saved, "")
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, []Occurrence{})
		return
	}
	v, err := s.viewerFor(ctx, u, r.URL.Query().Get("tz"))
	if err != nil {
		s.fail(w, err, "load calendars")
		return
	}
	var ids []int64
	for id, role := range v.roles {
		if role != store.RoleFreeBusy {
			ids = append(ids, id)
		}
	}
	rows, err := s.store.Search(ctx, ids, q, 100)
	if err != nil {
		s.fail(w, err, "search")
		return
	}
	now := time.Now()
	out := []Occurrence{}
	for _, row := range rows {
		res, err := s.resource(ctx, row)
		if err != nil {
			continue
		}
		// the next occurrence, else the last one before today
		var pick *ical.Occurrence
		if occ := res.Expand(now.AddDate(0, 0, -1), now.AddDate(3, 0, 0)); len(occ) > 0 {
			pick = &occ[0]
		} else if o, ok := res.Occurrence(""); ok {
			pick = o
		} else if occ := res.Expand(time.Unix(0, 0), now.AddDate(3, 0, 0)); len(occ) > 0 {
			pick = &occ[len(occ)-1]
		}
		if pick == nil {
			continue
		}
		oc := s.occurrenceView(ctx, v, row, *pick, nil)
		if !oc.Masked {
			out = append(out, oc)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- recently deleted ----------

func (s *Server) handleListDeleted(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.ListDeleted(r.Context(), me(r))
	if err != nil {
		s.fail(w, err, "recently deleted")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleRestoreDeleted(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	d, err := s.store.GetDeleted(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "restore")
		return
	}
	cal, err := s.targetCalendar(ctx, u, &d.CalendarID)
	if err != nil {
		s.fail(w, err, "restore")
		return
	}
	row, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: cal.ID, ICS: d.ICS, Loc: s.calLoc(ctx, cal.ID), Source: "restore", Actor: u.ID, IfNoneMatch: true},
		s.eventHook(ctx, "created", cal, d.UID, d.Summary, "restore"))
	if errors.Is(err, store.ErrUIDTaken) || errors.Is(err, store.ErrPrecondition) {
		_ = s.store.ForgetDeleted(ctx, u.ID, d.ID)
		writeErr(w, http.StatusConflict, "this event is already back in the calendar")
		return
	}
	if err != nil {
		s.fail(w, err, "restore")
		return
	}
	_ = s.store.ForgetDeleted(ctx, u.ID, d.ID)
	s.afterWrite(ctx, u, nil, row, true)
	s.respondEvent(w, r, http.StatusOK, row, "")
}

func (s *Server) handleForgetDeleted(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ForgetDeleted(r.Context(), me(r), idParam(r, "id")); err != nil {
		s.fail(w, err, "forget")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- after a write ----------

// afterWrite logs the change, tells people newly invited and bumps the
// change stamp the web app polls.
func (s *Server) afterWrite(ctx context.Context, actor *store.User, before *ical.Resource, row *store.EventRow, created bool) {
	s.touch()
	cal, err := s.store.GetCalendar(ctx, row.CalendarID)
	if err != nil {
		return
	}
	action := "updated"
	if created {
		action = "created"
	}
	cid, eid := cal.ID, row.ID
	s.store.AddActivity(ctx, cal.UserID, &cid, &eid, action, row.Summary, s.actorNote(actor, cal.UserID))
	res, err := ical.ParseResource(row.ICS, s.calLoc(ctx, row.CalendarID))
	if err != nil {
		return
	}
	m := res.MasterEvent()
	old := map[string]bool{}
	if before != nil {
		for _, a := range before.MasterEvent().Attendees {
			old[a.Email] = true
		}
	}
	for _, a := range m.Attendees {
		if old[a.Email] {
			continue
		}
		uid := s.store.UserIDByEmail(ctx, a.Email)
		if uid == 0 || (actor != nil && uid == actor.ID) || uid == cal.UserID {
			continue
		}
		who := "Someone"
		if actor != nil {
			who = firstNonEmpty(actor.Name, actor.Email)
		}
		s.notifyUser(ctx, uid, "invites", who+" invited you: "+m.Summary, whenText(m, s.loc(ctx, uid)), s.eventLink(row.ID, ""))
		if s.core.Configured() {
			_ = core.QueueEvent(ctx, s.store.DB, "calendar.invited", fmt.Sprintf("event:%d", row.ID), map[string]any{
				"id": row.ID, "uid": row.UID, "title": m.Summary, "start": fmtWhen(m.Start, m.AllDay, time.UTC), "user_email": a.Email,
				"organizer": actorEmail(actor)})
		}
	}
}

func actorEmail(u *store.User) string {
	if u == nil {
		return ""
	}
	return u.Email
}

func (s *Server) afterDelete(ctx context.Context, actor *store.User, row *store.EventRow) {
	s.touch()
	if cal, err := s.store.GetCalendar(ctx, row.CalendarID); err == nil {
		cid := cal.ID
		s.store.AddActivity(ctx, cal.UserID, &cid, nil, "deleted", row.Summary, s.actorNote(actor, cal.UserID))
	}
}

func (s *Server) actorNote(actor *store.User, owner int64) string {
	if actor == nil || actor.ID == owner {
		return ""
	}
	return "by " + firstNonEmpty(actor.Name, actor.Email)
}

// eventHook queues the Core event in the same transaction as the change.
func (s *Server) eventHook(ctx context.Context, kind string, cal *store.Calendar, uid, title, source string) store.Hook {
	if !s.core.Configured() || cal == nil {
		return nil
	}
	owner, err := s.store.GetUser(ctx, cal.UserID)
	return func(tx *sql.Tx) error {
		if err != nil {
			return err
		}
		var id int64
		var start, end string
		var allDay bool
		_ = tx.QueryRowContext(ctx, `SELECT id, dtstart, dtend, all_day FROM events WHERE calendar_id = ? AND uid = ?`, cal.ID, uid).Scan(&id, &start, &end, &allDay)
		return core.QueueEvent(ctx, tx, "calendar."+kind, "event:"+uid, map[string]any{
			"id": id, "uid": uid, "calendar_id": cal.ID, "calendar": cal.Name, "title": title, "start": start, "end": end,
			"all_day": allDay, "source": source, "user_email": owner.Email,
		})
	}
}

func (s *Server) rsvpHook(ctx context.Context, cal *store.Calendar, row *store.EventRow, u *store.User, status string) store.Hook {
	if !s.core.Configured() {
		return nil
	}
	return func(tx *sql.Tx) error {
		return core.QueueEvent(ctx, tx, "calendar.rsvp", fmt.Sprintf("event:%d", row.ID), map[string]any{
			"id": row.ID, "uid": row.UID, "title": row.Summary, "status": strings.ToLower(status), "user_email": u.Email,
		})
	}
}

func whenText(e *ical.Event, loc *time.Location) string {
	if e.AllDay {
		return e.Start.Format("Mon, Jan 2")
	}
	return e.Start.In(loc).Format("Mon, Jan 2 · 15:04")
}

func (s *Server) eventLink(id int64, key string) string {
	if s.cfg.BaseURL == "" {
		return ""
	}
	l := fmt.Sprintf("%s/event/%d", s.cfg.BaseURL, id)
	if key != "" {
		l += ":" + key
	}
	return l
}
