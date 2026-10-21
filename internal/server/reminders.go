package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// Reminders fire from the server (every 30 s): each VALARM of each
// occurrence once per person, to the calendar owner, to people it is shared
// with who turned reminders on, and to invited people who said yes or maybe.
// The morning agenda goes out at each person's chosen time.

func (s *Server) reminderLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		s.fireAlarms(ctx, time.Now())
		s.sendDigests(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// fireAlarms sends what fell due in the last 10 minutes (a restart or a
// sleep does not lose them, and nothing older is sent late).
func (s *Server) fireAlarms(ctx context.Context, now time.Time) {
	rows, err := s.store.EventsWithAlarms(ctx, now.Add(-time.Hour), now.AddDate(0, 0, 15))
	if err != nil {
		s.log.Warn("reminders: query", "err", err)
		return
	}
	for _, row := range rows {
		res, err := s.resource(ctx, row)
		if err != nil {
			continue
		}
		for _, o := range res.Expand(now.Add(-24*time.Hour), now.AddDate(0, 0, 15)) {
			e := o.Event
			if e.Status == ical.StatusCancelled {
				continue
			}
			for _, a := range e.Alarms {
				var at time.Time
				if t, err := time.Parse(time.RFC3339, a.Trigger); err == nil {
					at = t
				} else if d, err := ical.Duration(a.Trigger); err == nil {
					at = o.Start.Add(d)
				} else {
					continue
				}
				if at.After(now) || at.Before(now.Add(-10*time.Minute)) {
					continue
				}
				s.sendAlarm(ctx, row, o, a, now)
			}
		}
	}
}

func (s *Server) sendAlarm(ctx context.Context, row *store.EventRow, o ical.Occurrence, a ical.Alarm, now time.Time) {
	occKey := o.Key
	if occKey == "" {
		occKey = ical.RecurrenceKey(o.Start, o.AllDay)
	}
	recipients := map[int64]bool{}
	for _, uid := range s.store.AlarmRecipients(ctx, row.CalendarID) {
		if _, _, err := s.store.CalendarRole(ctx, uid, row.CalendarID); err == nil {
			recipients[uid] = true
		}
	}
	for _, at := range o.Event.Attendees {
		if at.Status != "ACCEPTED" && at.Status != "TENTATIVE" {
			continue
		}
		if uid := s.store.UserIDByEmail(ctx, at.Email); uid > 0 {
			recipients[uid] = true
		}
	}
	for uid := range recipients {
		if !s.store.AlarmFired(ctx, uid, row.ID, occKey, a.Trigger) {
			continue
		}
		loc := s.loc(ctx, uid)
		title := o.Event.Summary
		if title == "" {
			title = "Event"
		}
		var body string
		switch {
		case o.AllDay:
			body = o.Start.Format("Monday, Jan 2") + " · all day"
		default:
			until := o.Start.Sub(now).Round(time.Minute)
			when := o.Start.In(loc).Format("15:04")
			if until > 0 {
				body = "In " + humanDur(until) + " · " + when
			} else {
				body = "Now · " + when
			}
			if !o.End.Equal(o.Start) {
				body += "–" + o.End.In(loc).Format("15:04")
			}
		}
		if o.Event.Location != "" {
			body += "\n📍 " + o.Event.Location
		}
		if o.Event.Conference != "" {
			body += "\n🎥 " + o.Event.Conference
		}
		s.notifyUser(ctx, uid, "alarms", "🔔 "+title, body, s.eventLink(row.ID, o.Key))
		if s.core.Configured() {
			if u, err := s.store.GetUser(ctx, uid); err == nil {
				_ = core.QueueEvent(ctx, s.store.DB, "calendar.reminder", fmt.Sprintf("event:%d:%s", row.ID, occKey), map[string]any{
					"id": row.ID, "uid": row.UID, "title": title, "start": fmtWhen(o.Start, o.AllDay, time.UTC), "all_day": o.AllDay,
					"location": o.Event.Location, "user_email": u.Email,
				})
			}
		}
	}
}

func humanDur(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	default:
		days := int(d.Hours()+12) / 24
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
}

// sendDigests sends each person who asked for it the day's agenda once,
// at their chosen time in their zone.
func (s *Server) sendDigests(ctx context.Context, now time.Time) {
	for _, uid := range s.store.UserIDs(ctx) {
		st, err := s.store.GetSettings(ctx, uid)
		if err != nil || st.DigestTime == "" {
			continue
		}
		loc := s.loc(ctx, uid)
		local := now.In(loc)
		day := local.Format("2006-01-02")
		if local.Format("15:04") < st.DigestTime || s.store.UserSetting(ctx, uid, "digest.last") == day {
			continue
		}
		_ = s.store.SetUserSetting(ctx, uid, "digest.last", day)
		u, err := s.store.GetUser(ctx, uid)
		if err != nil {
			continue
		}
		v, err := s.viewerFor(ctx, u, "")
		if err != nil {
			continue
		}
		from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		occ, err := s.occurrences(ctx, v, nil, from, from.AddDate(0, 0, 1), true)
		if err != nil {
			continue
		}
		var lines []string
		for _, o := range occ {
			if o.Invite != nil && o.Invite.Status == "DECLINED" {
				continue
			}
			when := "all day"
			if !o.AllDay {
				if t, err := time.Parse(time.RFC3339, o.Start); err == nil {
					when = t.In(loc).Format("15:04")
				}
			}
			line := when + "  " + o.Title
			if o.Location != "" && !o.Masked {
				line += " · " + o.Location
			}
			lines = append(lines, line)
		}
		title := "📅 " + local.Format("Monday, Jan 2")
		body := "Nothing on the calendar today."
		if len(lines) > 0 {
			body = strings.Join(lines, "\n")
		}
		if n := len(s.pendingInviteCount(ctx, uid)); n > 0 {
			body += fmt.Sprintf("\n\n%d invitation(s) waiting for an answer.", n)
		}
		s.notifyUser(ctx, uid, "digest", title, body, s.cfg.BaseURL)
	}
}

func (s *Server) pendingInviteCount(ctx context.Context, uid int64) []*store.EventRow {
	rows, _ := s.store.PendingInvites(ctx, uid)
	return rows
}
