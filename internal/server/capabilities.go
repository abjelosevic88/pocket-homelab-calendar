package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// The four routes behind `capabilities` (links.go): /api/v1/pocket/search,
// /day/{date}, /widget and /capture. Each runs as the request's person and
// shows what the calendar itself shows them: their own calendars, the ones
// shared with them (a calendar they only see as busy gives "Busy" blocks,
// and is not searched) and the events they are invited to. The SDK's
// handlers check and cap what goes in and out.

// eventURL opens one occurrence in the app (the address "Copy link" gives).
func eventURL(o Occurrence) string { return "/event/" + o.ID }

// eventTitle is the title as the grid shows it.
func eventTitle(o Occurrence) string {
	if t := strings.TrimSpace(o.Title); t != "" {
		return t
	}
	return "(No title)"
}

// span is an occurrence's start and end on the viewer's clock. An all-day
// event is a plain date, so it runs from midnight to midnight wherever the
// viewer is; its end is the morning after its last day.
func (o Occurrence) span(loc *time.Location) (time.Time, time.Time) {
	if o.AllDay {
		s, _ := time.ParseInLocation("2006-01-02", o.Start, loc)
		e, _ := time.ParseInLocation("2006-01-02", o.End, loc)
		if !e.After(s) {
			e = s.AddDate(0, 0, 1)
		}
		return s, e
	}
	s, _ := time.Parse(time.RFC3339, o.Start)
	e, _ := time.Parse(time.RFC3339, o.End)
	if e.Before(s) {
		e = s
	}
	return s.In(loc), e.In(loc)
}

// agendaEvent is an occurrence with its span on the viewer's clock.
type agendaEvent struct {
	Occurrence
	start, end time.Time
}

// agenda lists what the person's day and agenda views show in [from, to),
// earliest first: every occurrence in their visible calendars and the events
// they are invited to. Cancelled events are left out, declined ones too
// unless the person chose to see them.
func (s *Server) agenda(ctx context.Context, v *viewer, from, to time.Time) ([]agendaEvent, error) {
	// a day wider on both sides: an all-day event is midnight to midnight in
	// its calendar's zone, which may be most of a day away from the viewer's
	occ, err := s.occurrences(ctx, v, nil, from.AddDate(0, 0, -1), to.AddDate(0, 0, 1), true)
	if err != nil {
		return nil, err
	}
	st, _ := s.store.GetSettings(ctx, v.user.ID)
	out := []agendaEvent{}
	for _, o := range occ {
		if o.Status == ical.StatusCancelled || (!st.ShowDeclined && declined(o)) {
			continue
		}
		a, b := o.span(v.loc)
		// an event without a length shows at its moment
		if !a.Before(to) || b.Before(from) || (b.Equal(from) && !a.Equal(b)) {
			continue
		}
		out = append(out, agendaEvent{Occurrence: o, start: a, end: b})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].start.Equal(out[j].start) {
			return out[i].start.Before(out[j].start)
		}
		return out[i].AllDay && !out[j].AllDay
	})
	return out, nil
}

// declined reports whether the viewer said no to the event.
func declined(o Occurrence) bool {
	if o.Invite != nil {
		return o.Invite.Status == "DECLINED"
	}
	for _, a := range o.Attendees {
		if a.Me {
			return a.Status == "DECLINED"
		}
	}
	return false
}

// clock is a time of day the way the person reads it (Settings → time format).
func clock(t time.Time, h12 bool) string {
	if h12 {
		return strings.ToLower(t.Format("3:04 PM"))
	}
	return t.Format("15:04")
}

func (s *Server) h12(ctx context.Context, uid int64) bool {
	st, _ := s.store.GetSettings(ctx, uid)
	return st.TimeFormat == "12h"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// ---------- search ----------

// whenLine says when an occurrence is, as the event's own card does:
// "Fri, Nov 27 · 15:00 – 16:00", "Fri, Nov 27 · All day", "Thu, Dec 10 – Sun,
// Dec 13". The year is said when it is not this one.
func whenLine(a, b time.Time, allDay bool, now time.Time, h12 bool) string {
	day := func(t time.Time) string {
		if t.Year() != now.Year() {
			return t.Format("Mon, Jan 2, 2006")
		}
		return t.Format("Mon, Jan 2")
	}
	sameDay := a.Year() == b.Year() && a.YearDay() == b.YearDay()
	switch {
	case allDay:
		if last := b.AddDate(0, 0, -1); last.After(a) {
			return day(a) + " – " + day(last)
		}
		return day(a) + " · All day"
	case a.Equal(b):
		return day(a) + " · " + clock(a, h12)
	case sameDay || b.Equal(time.Date(a.Year(), a.Month(), a.Day()+1, 0, 0, 0, 0, a.Location())):
		return day(a) + " · " + clock(a, h12) + " – " + clock(b, h12)
	}
	return day(a) + ", " + clock(a, h12) + " – " + day(b) + ", " + clock(b, h12)
}

// pocketSearch is the search behind GET /search, one line per event that
// matches: its next time (the one going on now, else the one to come), else
// its last. In order for a list: what is still to come first, soonest on top,
// then what is over, latest first.
func (s *Server) pocketSearch(r *http.Request, q string, limit int) ([]core.SearchItem, error) {
	ctx := r.Context()
	v, err := s.viewerFor(ctx, userFrom(ctx), "")
	if err != nil {
		return nil, err
	}
	now := time.Now().In(v.loc)
	found, err := s.searchEvents(ctx, v, q, now)
	if err != nil {
		return nil, err
	}
	evs := make([]agendaEvent, 0, len(found))
	for _, o := range found {
		a, b := o.span(v.loc)
		evs = append(evs, agendaEvent{Occurrence: o, start: a, end: b})
	}
	over := func(e agendaEvent) bool { return !e.end.After(now) }
	sort.SliceStable(evs, func(i, j int) bool {
		oi, oj := over(evs[i]), over(evs[j])
		switch {
		case oi != oj:
			return oj
		case oi:
			return evs[i].start.After(evs[j].start)
		}
		return evs[i].start.Before(evs[j].start)
	})
	if len(evs) > limit {
		evs = evs[:limit]
	}
	h12 := s.h12(ctx, v.user.ID)
	out := make([]core.SearchItem, 0, len(evs))
	for _, e := range evs {
		snippet := whenLine(e.start, e.end, e.AllDay, now, h12)
		if place := strings.TrimSpace(e.Location); place != "" {
			snippet += " · " + place
		}
		// Start is the day of an all-day event, else the moment in the person's zone
		out = append(out, core.SearchItem{ID: strconv.FormatInt(e.EventID, 10), Title: eventTitle(e.Occurrence), Snippet: snippet, Kind: "event", Date: e.Start, URL: eventURL(e.Occurrence)})
	}
	return out, nil
}

// ---------- day ----------

// pocketDay lists the events on one day of the person's calendar, as their
// day view would: in the given time zone (else the person's own, else the
// box's), recurring events at that day's occurrence, all-day events on their
// date wherever the person is.
func (s *Server) pocketDay(r *http.Request, date string, tz *time.Location) (*core.Day, error) {
	ctx := r.Context()
	v, err := s.viewerFor(ctx, userFrom(ctx), "")
	if err != nil {
		return nil, err
	}
	if tz != nil {
		v.loc = tz
	}
	from, err := time.ParseInLocation("2006-01-02", date, v.loc)
	if err != nil {
		return nil, nil
	}
	to := from.AddDate(0, 0, 1)
	evs, err := s.agenda(ctx, v, from, to)
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	day := &core.Day{Date: date, Summary: plural(len(evs), "event", "events")}
	for _, e := range evs {
		it := core.DayItem{ID: e.ID, Title: eventTitle(e.Occurrence), Sub: strings.TrimSpace(e.Location), Kind: "event", URL: eventURL(e.Occurrence)}
		if it.Sub == "" {
			if c := v.cals[e.CalendarID]; e.Invite == nil && c != nil {
				it.Sub = c.Name
			} else if e.Invite != nil {
				it.Sub = "Invitation"
			}
		}
		starts := !e.start.Before(from)
		switch {
		case e.AllDay || (!starts && !e.end.Before(to)):
			it.Kind = "all day"
		case !starts: // began on an earlier day: the part of it that is on this one
			it.Time, it.End = "00:00", e.end.Format("15:04")
		case !e.end.After(to):
			it.Time = e.start.Format("15:04")
			if e.end.After(e.start) {
				it.End = e.end.Format("15:04")
			}
		default: // goes on past midnight
			it.Time = e.start.Format("15:04")
		}
		day.Items = append(day.Items, it)
	}
	return day, nil
}

// ---------- widget ----------

const (
	widgetFew  = 5  // rows to fill with what comes next when today has fewer
	widgetDays = 30 // how far ahead "next" looks
)

// nextWord names a later day next to an event: "tomorrow", "Mon 30 Nov".
func nextWord(d, today time.Time) string {
	switch {
	case d.Equal(today.AddDate(0, 0, 1)):
		return "tomorrow"
	case d.Year() == today.Year():
		return d.Format("Mon 2 Jan")
	}
	return d.Format("2 Jan 2006")
}

// pocketWidget is the agenda in a few lines: what is still on today (all-day
// events, what is going on now, what is yet to start) and, when that is
// little, the next events of the coming weeks.
func (s *Server) pocketWidget(r *http.Request) (*core.WidgetData, error) {
	return s.agendaWidget(r.Context(), userFrom(r.Context()), time.Now())
}

func (s *Server) agendaWidget(ctx context.Context, u *store.User, at time.Time) (*core.WidgetData, error) {
	v, err := s.viewerFor(ctx, u, "")
	if err != nil {
		return nil, err
	}
	now := at.In(v.loc)
	today := midnight(now, v.loc)
	tomorrow := today.AddDate(0, 0, 1)
	evs, err := s.agenda(ctx, v, today, tomorrow)
	if err != nil {
		return nil, err
	}
	h12 := s.h12(ctx, v.user.ID)
	out := &core.WidgetData{URL: "/agenda"}
	row := func(e agendaEvent, detail string) {
		out.Items = append(out.Items, core.WidgetItem{ID: e.ID, Title: eventTitle(e.Occurrence), Detail: detail, URL: eventURL(e.Occurrence)})
	}
	var left, allDay, earlier int
	for _, e := range evs {
		switch {
		case e.AllDay || (e.start.Before(today) && !e.end.Before(tomorrow)):
			allDay++
			row(e, "all day")
		case !e.end.After(now):
			earlier++
		case e.start.After(now):
			left++
			row(e, clock(e.start, h12))
		case e.end.After(tomorrow):
			left++
			row(e, "now")
		default:
			left++
			row(e, "until "+clock(e.end, h12))
		}
	}
	switch {
	case left > 0:
		out.Line = strconv.Itoa(left) + " more today"
	case allDay > 0:
		out.Line = plural(allDay, "all-day event", "all-day events") + " today"
	case earlier > 0:
		out.Line = "Nothing more today"
	default:
		out.Line = "Nothing today"
	}
	if len(out.Items) >= widgetFew {
		return out, nil
	}
	next, err := s.agenda(ctx, v, tomorrow, today.AddDate(0, 0, widgetDays))
	if err != nil {
		return nil, err
	}
	for _, e := range next {
		if e.start.Before(tomorrow) { // still running from today: it has its row
			continue
		}
		if len(out.Items) >= widgetFew {
			break
		}
		detail := nextWord(midnight(e.start, v.loc), today)
		if !e.AllDay {
			detail += " " + clock(e.start, h12)
		}
		row(e, detail)
	}
	return out, nil
}

// ---------- capture ----------

// pocketCapture keeps a quick line as a new event, read the way the app's own
// quick add reads it ("Dentist tomorrow 3pm at the clinic for 45m"), in the
// calendar quick add would pick: "#name" one, else the person's default. The
// line is the title when one was given, else the first line of the text; the
// rest becomes the notes. A line that names no day lands on the given date
// (all day when it names no time either). Nobody is invited, whatever the
// line says. The same request_id again answers the same event and adds
// nothing: the receipt is written in the transaction that adds the event.
func (s *Server) pocketCapture(r *http.Request, in core.CaptureInput) (*core.CaptureResult, error) {
	ctx := r.Context()
	u := userFrom(ctx)
	op, err := core.NewOperation(fmt.Sprintf("calendar:user:%d:%s", u.ID, core.CapturePath), in.RequestID, in)
	if err != nil {
		return nil, err
	}
	var res core.CaptureResult
	replay := func() (*core.CaptureResult, error) {
		found, err := op.Replay(ctx, s.store.DB, &res)
		switch {
		case errors.Is(err, core.ErrOperationConflict):
			return nil, fmt.Errorf("%w: this was already kept with other words", core.ErrCapture)
		case err != nil || !found:
			return nil, err
		}
		return &res, nil
	}
	if kept, err := replay(); kept != nil || err != nil {
		return kept, err
	}

	line, notes := in.Title, in.Text
	if line == "" {
		line, notes, _ = strings.Cut(in.Text, "\n")
	}
	line, notes = strings.Join(strings.Fields(line), " "), strings.TrimSpace(notes)
	if line == "" {
		return nil, fmt.Errorf("%w: say what the event is", core.ErrCapture)
	}
	loc := s.loc(ctx, u.ID)
	ev := s.quickRead(ctx, u.ID, line, time.Now(), loc)
	if !ev.HasDate && in.Date != "" {
		// no day in the line: read it as if said first thing on the given one
		if d, err := time.ParseInLocation("2006-01-02", in.Date, loc); err == nil {
			ev = s.quickRead(ctx, u.ID, line, d, loc)
		}
	}
	if t := []rune(ev.Title); len(t) > 300 {
		// a long paragraph: keep all of it as the notes
		ev.Title, notes = strings.TrimSpace(string(t[:299]))+"…", strings.TrimSpace(line+"\n"+notes)
	}
	cal, err := s.quickCalendar(ctx, u, &ev, nil)
	if err != nil {
		return nil, captureRefusal(err)
	}
	p := quickPatch(&ev, loc, cal)
	if notes != "" {
		p.Description = &notes
	}
	_, err = s.createEvent(ctx, u, cal, p, s.source(r), func(tx *sql.Tx, id int64) error {
		res = core.CaptureResult{ID: strconv.FormatInt(id, 10), Title: ev.Title, URL: fmt.Sprintf("/event/%d", id)}
		return op.Save(ctx, tx, res)
	})
	if err != nil {
		// the same line sent twice at once: the other one kept it
		if kept, rerr := replay(); kept != nil || rerr != nil {
			return kept, rerr
		}
		return nil, captureRefusal(err)
	}
	return &res, nil
}

// captureRefusal turns what the person can fix (the app's own 400s and 403s)
// into a capture refusal with the same words.
func captureRefusal(err error) error {
	var ue userError
	var fb forbidden
	switch {
	case errors.As(err, &ue):
		return fmt.Errorf("%w: %s", core.ErrCapture, ue.msg)
	case errors.As(err, &fb):
		return fmt.Errorf("%w: %s", core.ErrCapture, fb.msg)
	}
	return err
}
