package server

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// Occurrence is one instance of an event as the API shows it.
type Occurrence struct {
	ID          string         `json:"id"` // "<event_id>" or "<event_id>:<key>"
	EventID     int64          `json:"event_id"`
	Key         string         `json:"key"`
	UID         string         `json:"uid"`
	CalendarID  int64          `json:"calendar_id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Location    string         `json:"location,omitempty"`
	URL         string         `json:"url,omitempty"`
	Conference  string         `json:"conference,omitempty"`
	Start       string         `json:"start"`
	End         string         `json:"end"`
	AllDay      bool           `json:"all_day"`
	TZ          string         `json:"tz,omitempty"`
	Status      string         `json:"status"`
	Busy        bool           `json:"busy"`
	Private     bool           `json:"private"`
	Color       string         `json:"color,omitempty"`
	Categories  []string       `json:"categories"`
	Recurring   bool           `json:"recurring"`
	RRule       string         `json:"rrule,omitempty"`
	RepeatText  string         `json:"repeat_text,omitempty"`
	Override    bool           `json:"override"`
	Alarms      []ical.Alarm   `json:"alarms"`
	Organizer   *ical.Attendee `json:"organizer,omitempty"`
	Attendees   []AttendeeView `json:"attendees"`
	Invite      *InviteView    `json:"invite,omitempty"`
	CanEdit     bool           `json:"can_edit"`
	Masked      bool           `json:"masked"`
	Source      string         `json:"source,omitempty"`
	CreatedBy   *store.Person  `json:"created_by,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
	ETag        string         `json:"etag,omitempty"`
	sortKey     time.Time
}

// AttendeeView is an attendee with the account they have on the box.
type AttendeeView struct {
	ical.Attendee
	UserID *int64 `json:"user_id,omitempty"`
	Me     bool   `json:"me,omitempty"`
}

// InviteView says the occurrence is shown because the person was invited.
type InviteView struct {
	Status string `json:"status"`
}

// viewer is who is looking: their zone, their calendars and roles.
type viewer struct {
	user  *store.User
	loc   *time.Location
	roles map[int64]string
	cals  map[int64]*store.Calendar
}

func (s *Server) viewerFor(ctx context.Context, u *store.User, tz string) (*viewer, error) {
	v := &viewer{user: u, loc: s.loc(ctx, u.ID), roles: map[int64]string{}, cals: map[int64]*store.Calendar{}}
	if tz != "" {
		if l, err := ical.LoadZone(tz); err == nil {
			v.loc = l
		}
	}
	cals, err := s.store.ListCalendarsFor(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range cals {
		v.roles[c.ID] = c.Role
		v.cals[c.ID] = c
	}
	return v, nil
}

// parsed resources are cached by etag; parsing is the expensive part of a range read.
var resCache = struct {
	sync.Mutex
	m map[string]*ical.Resource
}{m: map[string]*ical.Resource{}}

func (s *Server) resource(ctx context.Context, e *store.EventRow) (*ical.Resource, error) {
	loc := s.calLoc(ctx, e.CalendarID)
	key := e.ETag + "|" + loc.String()
	resCache.Lock()
	r, ok := resCache.m[key]
	resCache.Unlock()
	if ok {
		return r, nil
	}
	r, err := ical.ParseResource(e.ICS, loc)
	if err != nil {
		return nil, err
	}
	resCache.Lock()
	if len(resCache.m) > 5000 {
		resCache.m = map[string]*ical.Resource{}
	}
	resCache.m[key] = r
	resCache.Unlock()
	return r, nil
}

// calLoc is the zone floating times of a calendar are read in: the
// calendar's own zone, else its owner's.
func (s *Server) calLoc(ctx context.Context, calID int64) *time.Location {
	c, err := s.store.GetCalendar(ctx, calID)
	if err != nil {
		return time.UTC
	}
	if c.Timezone != "" {
		if l, err := ical.LoadZone(c.Timezone); err == nil {
			return l
		}
	}
	return s.loc(ctx, c.UserID)
}

func fmtWhen(t time.Time, allDay bool, loc *time.Location) string {
	if allDay {
		return t.Format("2006-01-02")
	}
	return t.In(loc).Format(time.RFC3339)
}

// occurrenceView turns one instance into the API shape, masking details
// for people who may only see that the time is taken.
func (s *Server) occurrenceView(ctx context.Context, v *viewer, row *store.EventRow, o ical.Occurrence, invite *InviteView) Occurrence {
	e := o.Event
	role := v.roles[row.CalendarID]
	cal := v.cals[row.CalendarID]
	id := strconv.FormatInt(row.ID, 10)
	if o.Key != "" {
		id += ":" + o.Key
	}
	out := Occurrence{
		ID: id, EventID: row.ID, Key: o.Key, UID: row.UID, CalendarID: row.CalendarID,
		Title: e.Summary, Description: e.Description, Location: e.Location, URL: e.URL, Conference: e.Conference,
		Start: fmtWhen(o.Start, o.AllDay, v.loc), End: fmtWhen(o.End, o.AllDay, v.loc), AllDay: o.AllDay, TZ: e.TZID,
		Status: e.Status, Busy: e.Transp != "transparent", Private: e.Class != "public", Color: e.Color,
		Categories: e.Categories, Recurring: o.Recurring, Override: o.Override, Alarms: e.Alarms, Organizer: e.Organizer,
		Invite: invite, Source: row.Source, UpdatedAt: row.UpdatedAt, ETag: row.ETag, sortKey: o.Start,
	}
	if out.Categories == nil {
		out.Categories = []string{}
	}
	if out.Alarms == nil {
		out.Alarms = []ical.Alarm{}
	}
	if o.Recurring {
		out.RRule = row.RRule
		out.RepeatText = ical.DescribeRule(row.RRule)
	}
	out.Attendees = []AttendeeView{}
	for _, a := range e.Attendees {
		av := AttendeeView{Attendee: a}
		if id := s.store.UserIDByEmail(ctx, a.Email); id > 0 {
			av.UserID = &id
			av.Me = id == v.user.ID
		}
		out.Attendees = append(out.Attendees, av)
	}
	out.CanEdit = invite == nil && store.RoleAtLeast(role, store.RoleWrite) && cal != nil && !cal.ReadOnly
	if role == store.RoleFreeBusy || (invite == nil && out.Private && !store.RoleAtLeast(role, store.RoleWrite)) {
		out = Occurrence{ID: out.ID, EventID: out.EventID, Key: out.Key, CalendarID: out.CalendarID, Title: "Busy",
			Start: out.Start, End: out.End, AllDay: out.AllDay, Status: out.Status, Busy: out.Busy, Private: true,
			Categories: []string{}, Alarms: []ical.Alarm{}, Attendees: []AttendeeView{}, Recurring: out.Recurring,
			Masked: true, sortKey: out.sortKey}
	}
	if row.CreatedBy > 0 && !out.Masked {
		if u, err := s.store.GetUser(ctx, row.CreatedBy); err == nil {
			out.CreatedBy = &store.Person{ID: u.ID, Name: u.Name, Email: u.Email}
		}
	}
	return out
}

// occurrences lists what a person sees in [from, to): the visible
// calendars (or the given ones) and the events they are invited to.
func (s *Server) occurrences(ctx context.Context, v *viewer, calIDs []int64, from, to time.Time, withInvites bool) ([]Occurrence, error) {
	if calIDs == nil {
		for id, c := range v.cals {
			if c.Visible {
				calIDs = append(calIDs, id)
			}
		}
	}
	rows, err := s.store.EventsInRange(ctx, calIDs, from, to)
	if err != nil {
		return nil, err
	}
	out := []Occurrence{}
	seen := map[int64]bool{}
	for _, row := range rows {
		seen[row.ID] = true
		r, err := s.resource(ctx, row)
		if err != nil {
			s.log.Debug("unreadable event", "id", row.ID, "err", err)
			continue
		}
		for _, o := range r.Expand(from, to) {
			out = append(out, s.occurrenceView(ctx, v, row, o, nil))
		}
	}
	if withInvites {
		st, _ := s.store.GetSettings(ctx, v.user.ID)
		inv, err := s.store.InvitedEvents(ctx, v.user.ID, from, to)
		if err == nil {
			for _, row := range inv {
				if seen[row.ID] {
					continue
				}
				seen[row.ID] = true
				r, err := s.resource(ctx, row)
				if err != nil {
					continue
				}
				for _, o := range r.Expand(from, to) {
					status := myStatus(o.Event, v.user.Email)
					if status == "DECLINED" && !st.ShowDeclined {
						continue
					}
					oc := s.occurrenceView(ctx, v, row, o, &InviteView{Status: status})
					oc.Color = st.InvitesColor
					out = append(out, oc)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AllDay != out[j].AllDay {
			return out[i].AllDay
		}
		return out[i].sortKey.Before(out[j].sortKey)
	})
	return out, nil
}

func myStatus(e *ical.Event, email string) string {
	for _, a := range e.Attendees {
		if strings.EqualFold(a.Email, email) {
			if a.Status == "" {
				return "NEEDS-ACTION"
			}
			return a.Status
		}
	}
	return "NEEDS-ACTION"
}

// parseRange reads from/to (dates or RFC 3339) in the viewer's zone.
func parseRange(fromS, toS string, loc *time.Location) (time.Time, time.Time, error) {
	parse := func(v string) (time.Time, error) {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, nil
		}
		return time.ParseInLocation("2006-01-02", v, loc)
	}
	from, err := parse(fromS)
	if err != nil {
		return from, from, badRequest("from must be YYYY-MM-DD or RFC 3339")
	}
	to, err := parse(toS)
	if err != nil {
		return from, to, badRequest("to must be YYYY-MM-DD or RFC 3339")
	}
	if !to.After(from) {
		return from, to, badRequest("to must be after from")
	}
	if to.Sub(from) > 400*24*time.Hour {
		return from, to, badRequest("at most 400 days at a time")
	}
	return from, to, nil
}

// parseOccID reads "12" or "12:20261021T090000Z".
func parseOccID(s string) (int64, string, error) {
	id, key, _ := strings.Cut(s, ":")
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("bad occurrence id %q", s)
	}
	return n, key, nil
}
