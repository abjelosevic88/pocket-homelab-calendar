package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// Secret calendar feeds (webcal): one per calendar, and one per person with
// everything they see. The token in the address is the only key.

// feedICS merges calendar objects into one VCALENDAR. mask turns private
// events into "Busy".
func (s *Server) feedICS(ctx context.Context, c *store.Calendar, rows []*store.EventRow, mask bool) string {
	out := ical.NewCalendar()
	out.SetText("X-WR-CALNAME", c.Name)
	if c.Description != "" {
		out.SetText("X-WR-CALDESC", c.Description)
	}
	if c.Color != "" {
		out.SetRaw("X-APPLE-CALENDAR-COLOR", c.Color)
	}
	if c.Timezone != "" {
		out.SetRaw("X-WR-TIMEZONE", c.Timezone)
	}
	out.SetRaw("REFRESH-INTERVAL;VALUE=DURATION", "PT15M")
	out.SetRaw("X-PUBLISHED-TTL", "PT15M")
	s.appendObjects(out, rows, mask)
	return out.String()
}

func (s *Server) appendObjects(out *ical.Component, rows []*store.EventRow, mask bool) {
	tz := map[string]bool{}
	for _, row := range rows {
		parsed, err := ical.Parse(row.ICS)
		if err != nil {
			continue
		}
		for _, ch := range parsed.Children {
			switch ch.Name {
			case "VTIMEZONE":
				id := ch.Value("TZID")
				if tz[id] {
					continue
				}
				tz[id] = true
				out.Children = append([]*ical.Component{ch}, out.Children...)
			case "VEVENT":
				if mask && strings.ToUpper(ch.Value("CLASS")) != "" && strings.ToUpper(ch.Value("CLASS")) != "PUBLIC" {
					busy := &ical.Component{Name: "VEVENT"}
					for _, n := range []string{"UID", "DTSTART", "DTEND", "DURATION", "RRULE", "EXDATE", "RDATE", "RECURRENCE-ID", "DTSTAMP", "SEQUENCE", "TRANSP"} {
						for _, p := range ch.GetAll(n) {
							busy.Props = append(busy.Props, p)
						}
					}
					busy.SetText("SUMMARY", "Busy")
					busy.SetRaw("CLASS", "PRIVATE")
					ch = busy
				}
				out.Children = append(out.Children, ch)
			}
		}
	}
}

func (s *Server) handleCalendarFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tok := strings.TrimSuffix(chi.URLParam(r, "token"), ".ics")
	c, err := s.store.GetCalendarByFeed(ctx, tok)
	if err != nil {
		time.Sleep(200 * time.Millisecond)
		http.NotFound(w, r)
		return
	}
	rows, err := s.store.ListEvents(ctx, c.ID)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	writeICS(w, s.feedICS(ctx, c, rows, false))
}

// handleUserFeed is everything one person sees: their visible calendars
// (private events of read-only shares masked) and accepted invitations.
func (s *Server) handleUserFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tok := strings.TrimSuffix(chi.URLParam(r, "token"), ".ics")
	u, err := s.store.GetUserByFeedToken(ctx, tok)
	if err != nil {
		time.Sleep(200 * time.Millisecond)
		http.NotFound(w, r)
		return
	}
	cals, err := s.store.ListCalendarsFor(ctx, u.ID)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	out := ical.NewCalendar()
	st, _ := s.store.GetSettings(ctx, 0)
	out.SetText("X-WR-CALNAME", firstNonEmpty(u.Name, u.Email)+" · "+st.Title)
	out.SetRaw("REFRESH-INTERVAL;VALUE=DURATION", "PT15M")
	out.SetRaw("X-PUBLISHED-TTL", "PT15M")
	seen := map[int64]bool{}
	for _, c := range cals {
		if !c.Visible || c.Role == store.RoleFreeBusy {
			continue
		}
		rows, err := s.store.ListEvents(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, row := range rows {
			seen[row.ID] = true
		}
		s.appendObjects(out, rows, c.Role == store.RoleRead)
	}
	now := time.Now()
	if inv, err := s.store.InvitedEvents(ctx, u.ID, now.AddDate(-1, 0, 0), now.AddDate(2, 0, 0)); err == nil {
		var keep []*store.EventRow
		for _, row := range inv {
			if seen[row.ID] {
				continue
			}
			if a := s.store.MyAttendance(ctx, row.ID, u.ID); a != nil && a.PartStat != "DECLINED" {
				keep = append(keep, row)
			}
		}
		s.appendObjects(out, keep, false)
	}
	writeICS(w, out.String())
}

func writeICS(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write([]byte(body))
}

func (s *Server) handleFeeds(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	feed := s.absURL(r, "/feed/user/"+u.FeedToken+".ics")
	writeJSON(w, http.StatusOK, map[string]any{
		"all_url":     feed,
		"all_webcal":  strings.Replace(strings.Replace(feed, "https://", "webcal://", 1), "http://", "webcal://", 1),
		"caldav_url":  s.absURL(r, "/dav/"),
		"caldav_user": u.Username,
		"principal":   s.absURL(r, "/dav/principals/"+u.Username+"/"),
	})
}

func (s *Server) handleRotateFeed(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	u.FeedToken = store.RandomToken(16)
	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		s.fail(w, err, "rotate feed")
		return
	}
	s.handleFeeds(w, r)
}
