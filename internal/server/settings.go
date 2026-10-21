package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, err := s.store.GetSettings(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, s.publicSettings(ctx, u, st))
}

func (s *Server) publicSettings(ctx context.Context, u *store.User, st store.Settings) map[string]any {
	return map[string]any{
		"settings":          st,
		"effective_tz":      s.loc(ctx, u.ID).String(),
		"box_tz":            s.cfg.Timezone,
		"box_holidays":      s.cfg.HolidayCountry,
		"notify_has_token":  st.NotifyToken != "",
		"notify_box_route":  s.cfg.NotifyURL != "" && s.isOwner(ctx, u.ID),
		"google_configured": s.cfg.GoogleClientID != "",
		"core":              s.coreStatus(),
	}
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, err := s.store.GetSettings(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	var in struct {
		Title              *string   `json:"title"`
		Timezone           *string   `json:"timezone"`
		WeekStart          *int      `json:"week_start"`
		TimeFormat         *string   `json:"time_format"`
		DefaultView        *string   `json:"default_view"`
		DefaultCalendarID  *int64    `json:"default_calendar_id"`
		DefaultDurationMin *int      `json:"default_duration_min"`
		DefaultAlarms      *[]string `json:"default_alarms"`
		AllDayAlarms       *[]string `json:"all_day_alarms"`
		WorkStart          *int      `json:"work_start"`
		WorkEnd            *int      `json:"work_end"`
		WorkDays           *[]int    `json:"work_days"`
		ShowWeekNumbers    *bool     `json:"show_week_numbers"`
		ShowDeclined       *bool     `json:"show_declined"`
		ShowWeekends       *bool     `json:"show_weekends"`
		Density            *string   `json:"density"`
		HolidayCountry     *string   `json:"holiday_country"`
		FreeBusyShared     *bool     `json:"freebusy_shared"`
		InvitesColor       *string   `json:"invites_color"`
		NotifyKind         *string   `json:"notify_kind"`
		NotifyURL          *string   `json:"notify_url"`
		NotifyToken        *string   `json:"notify_token"`
		NotifyAlarms       *bool     `json:"notify_alarms"`
		NotifyInvites      *bool     `json:"notify_invites"`
		DigestTime         *string   `json:"digest_time"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Title != nil && u.Role == "admin" && strings.TrimSpace(*in.Title) != "" {
		box, _ := s.store.GetSettings(ctx, 0)
		box.Title = strings.TrimSpace(*in.Title)
		_ = s.store.SaveBoxSettings(ctx, box)
		st.Title = box.Title
	}
	if in.Timezone != nil {
		if *in.Timezone != "" {
			if _, err := ical.LoadZone(*in.Timezone); err != nil {
				writeErr(w, http.StatusBadRequest, "unknown time zone")
				return
			}
		}
		st.Timezone = *in.Timezone
	}
	if in.WeekStart != nil && (*in.WeekStart == 0 || *in.WeekStart == 1 || *in.WeekStart == 6) {
		st.WeekStart = *in.WeekStart
	}
	if in.TimeFormat != nil && (*in.TimeFormat == "24h" || *in.TimeFormat == "12h") {
		st.TimeFormat = *in.TimeFormat
	}
	if in.DefaultView != nil {
		switch *in.DefaultView {
		case "day", "3day", "week", "month", "year", "agenda", "people":
			st.DefaultView = *in.DefaultView
		}
	}
	if in.DefaultCalendarID != nil {
		st.DefaultCalendarID = *in.DefaultCalendarID
	}
	if in.DefaultDurationMin != nil && *in.DefaultDurationMin >= 5 && *in.DefaultDurationMin <= 24*60 {
		st.DefaultDurationMin = *in.DefaultDurationMin
	}
	for _, p := range []struct {
		in  *[]string
		out *[]string
	}{{in.DefaultAlarms, &st.DefaultAlarms}, {in.AllDayAlarms, &st.AllDayAlarms}} {
		if p.in == nil {
			continue
		}
		for _, a := range *p.in {
			if _, err := ical.Duration(a); err != nil {
				writeErr(w, http.StatusBadRequest, "alarms look like -PT10M")
				return
			}
		}
		*p.out = *p.in
	}
	if in.WorkStart != nil && *in.WorkStart >= 0 && *in.WorkStart <= 23 {
		st.WorkStart = *in.WorkStart
	}
	if in.WorkEnd != nil && *in.WorkEnd >= 1 && *in.WorkEnd <= 24 {
		st.WorkEnd = *in.WorkEnd
	}
	if in.WorkDays != nil {
		st.WorkDays = *in.WorkDays
	}
	setB := func(p *bool, dst *bool) {
		if p != nil {
			*dst = *p
		}
	}
	setB(in.ShowWeekNumbers, &st.ShowWeekNumbers)
	setB(in.ShowDeclined, &st.ShowDeclined)
	setB(in.ShowWeekends, &st.ShowWeekends)
	setB(in.FreeBusyShared, &st.FreeBusyShared)
	setB(in.NotifyAlarms, &st.NotifyAlarms)
	setB(in.NotifyInvites, &st.NotifyInvite)
	if in.Density != nil && (*in.Density == "comfortable" || *in.Density == "compact") {
		st.Density = *in.Density
	}
	if in.HolidayCountry != nil {
		st.HolidayCountry = strings.ToUpper(strings.TrimSpace(*in.HolidayCountry))
		s.dropOverlayCache(u.ID, "holidays")
	}
	if in.InvitesColor != nil && validColor(*in.InvitesColor) {
		st.InvitesColor = *in.InvitesColor
	}
	if in.NotifyKind != nil {
		switch *in.NotifyKind {
		case "apprise", "ntfy", "webhook":
			st.NotifyKind = *in.NotifyKind
		}
	}
	if in.NotifyURL != nil {
		st.NotifyURL = strings.TrimSpace(*in.NotifyURL)
	}
	if in.DigestTime != nil {
		v := strings.TrimSpace(*in.DigestTime)
		if v != "" {
			if _, err := time.Parse("15:04", v); err != nil {
				writeErr(w, http.StatusBadRequest, "the agenda time looks like 07:30")
				return
			}
		}
		st.DigestTime = v
	}
	if err := s.store.SaveSettings(ctx, u.ID, st); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	if in.NotifyToken != nil {
		_ = s.store.SetUserSetting(ctx, u.ID, "notify_token", strings.TrimSpace(*in.NotifyToken))
	}
	s.touch()
	st, _ = s.store.GetSettings(ctx, u.ID)
	writeJSON(w, http.StatusOK, s.publicSettings(ctx, u, st))
}

// notifyRoute is where a person's notifications go: their own route, else
// (for the owner) the box route from the environment.
func (s *Server) notifyRoute(ctx context.Context, uid int64, st store.Settings) (kind, url, token string) {
	if st.NotifyURL != "" {
		return st.NotifyKind, st.NotifyURL, st.NotifyToken
	}
	if s.cfg.NotifyURL != "" && s.isOwner(ctx, uid) {
		return s.cfg.NotifyKind, s.cfg.NotifyURL, ""
	}
	return "", "", ""
}

// notifyUser sends one message to a person when they want that kind
// ("alarms", "invites", "digest").
func (s *Server) notifyUser(ctx context.Context, uid int64, what, title, body, link string) {
	st, err := s.store.GetSettings(ctx, uid)
	if err != nil {
		return
	}
	switch what {
	case "alarms":
		if !st.NotifyAlarms {
			return
		}
	case "invites":
		if !st.NotifyInvite {
			return
		}
	}
	kind, url, token := s.notifyRoute(ctx, uid, st)
	if url == "" {
		return
	}
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := notify.Send(c, kind, url, token, title, body, link); err != nil {
			s.log.Warn("notification failed", "user", uid, "what", what, "err", err)
		}
	}()
}

func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	kind, url, token := s.notifyRoute(ctx, u.ID, st)
	if url == "" {
		writeErr(w, http.StatusBadRequest, "set a notification address first")
		return
	}
	if err := notify.Send(ctx, kind, url, token, "Pocket Calendar", "Notifications work. Reminders will look like this.", s.cfg.BaseURL); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	info := map[string]any{
		"version": Version, "go": runtime.Version(), "uptime": time.Since(s.started).Round(time.Second).String(),
		"counts": s.store.UserCounts(ctx, u.ID), "core": s.coreStatus(), "auth_mode": s.cfg.AuthMode,
		"base_url": s.cfg.BaseURL, "timezone": s.cfg.Timezone,
	}
	if u.Role == "admin" {
		if fi, err := os.Stat(s.cfg.DBPath); err == nil {
			info["db_size"] = fi.Size()
		}
		info["users"], _ = s.store.CountUsers(ctx)
	}
	writeJSON(w, http.StatusOK, info)
}

// handleBackup streams a consistent copy of the whole database; only an
// admin, and only while they are the only person on the box.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "only an admin can download the database")
		return
	}
	if n, _ := s.store.CountUsers(ctx); n > 1 {
		writeErr(w, http.StatusForbidden, "the database holds other people's calendars; back it up on the server (pocket-calendar backup)")
		return
	}
	tmp := fmt.Sprintf("%s/backups/download-%d.db", s.cfg.DataDir, time.Now().UnixNano())
	if err := s.store.Backup(ctx, tmp); err != nil {
		s.fail(w, err, "backup")
		return
	}
	defer os.Remove(tmp)
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-calendar-`+time.Now().Format("20060102")+`.db"`)
	http.ServeFile(w, r, tmp)
}

// handleExportAll is every calendar the person owns as one .ics.
func (s *Server) handleExportAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	own, err := s.store.OwnCalendars(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	out := ical.NewCalendar()
	out.SetText("X-WR-CALNAME", firstNonEmpty(u.Name, u.Email))
	for _, c := range own {
		rows, err := s.store.ListEvents(ctx, c.ID)
		if err != nil {
			continue
		}
		s.appendObjects(out, rows, false)
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-calendar.ics"`)
	_, _ = w.Write([]byte(out.String()))
}
