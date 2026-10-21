package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// palette the app picks colours from for new calendars.
var palette = []string{"#3b82f6", "#10b981", "#f59e0b", "#ef4444", "#8b5cf6", "#ec4899", "#14b8a6", "#f97316", "#6366f1", "#84cc16"}

// seedUser gives a new person their first calendar and binds what was
// shared with or sent to their email before they had an account.
func (s *Server) seedUser(ctx context.Context, u *store.User) {
	s.store.BindPendingShares(ctx, u.ID, u.Email)
	if own, _ := s.store.OwnCalendars(ctx, u.ID); len(own) > 0 {
		return
	}
	c := &store.Calendar{UserID: u.ID, UID: "personal", Name: "Personal", Color: palette[0], IsDefault: true}
	if err := s.store.CreateCalendar(ctx, c); err != nil {
		s.log.Warn("seed calendar", "user", u.Email, "err", err)
	}
}

func (s *Server) calendarView(c *store.Calendar, r *http.Request) map[string]any {
	out := map[string]any{
		"id": c.ID, "uid": c.UID, "name": c.Name, "color": firstNonEmpty(c.MyColor, c.Color), "calendar_color": c.Color,
		"description": c.Description, "timezone": c.Timezone, "kind": c.Kind, "account_id": c.AccountID,
		"read_only": c.ReadOnly, "is_default": c.IsDefault, "default_alarms": c.DefaultAlarms, "public": c.Public,
		"sort_order": c.SortOrder, "role": c.Role, "visible": c.Visible, "notify": c.Notify, "owner": c.Owner,
		"events": c.Events, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
		"writable": store.RoleAtLeast(c.Role, store.RoleWrite) && !c.ReadOnly,
	}
	if c.Role == store.RoleOwner || c.Role == store.RoleManage {
		out["feed_url"] = s.absURL(r, "/feed/cal/"+c.FeedToken+".ics")
		out["caldav_url"] = s.absURL(r, "/dav/calendars/"+s.principalOf(r.Context(), c.UserID)+"/"+c.UID+"/")
	}
	return out
}

func (s *Server) principalOf(ctx context.Context, uid int64) string {
	if u, err := s.store.GetUser(ctx, uid); err == nil {
		return u.Username
	}
	return ""
}

func (s *Server) absURL(r *http.Request, path string) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL + path
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}

func (s *Server) handleListCalendars(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	cals, err := s.store.ListCalendarsFor(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "list calendars")
		return
	}
	if len(cals) == 0 {
		s.seedUser(ctx, u)
		cals, _ = s.store.ListCalendarsFor(ctx, u.ID)
	}
	out := []map[string]any{}
	for _, c := range cals {
		if c.Role != store.RoleOwner && !c.Visible && r.URL.Query().Get("all") != "1" {
			// a calendar someone left stays hidden from their list
			if left := s.store.UserSetting(ctx, u.ID, "left."+itoa64(c.ID)); left == "1" {
				continue
			}
		}
		v := s.calendarView(c, r)
		if c.Role == store.RoleOwner || c.Role == store.RoleManage {
			if sh, err := s.store.ListShares(ctx, c.ID); err == nil {
				v["shares"] = sh
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

type calendarIn struct {
	Name          *string   `json:"name"`
	Color         *string   `json:"color"`
	Description   *string   `json:"description"`
	Timezone      *string   `json:"timezone"`
	DefaultAlarms *[]string `json:"default_alarms"`
	IsDefault     *bool     `json:"is_default"`
	Public        *bool     `json:"public"`
	// the viewer's own
	Visible *bool    `json:"visible"`
	Notify  *bool    `json:"notify"`
	MyColor *string  `json:"my_color"`
	Sort    *float64 `json:"sort_order"`
}

func validColor(c string) bool {
	if c == "" {
		return true
	}
	if len(c) != 7 || c[0] != '#' {
		return false
	}
	for _, r := range strings.ToLower(c[1:]) {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (s *Server) handleCreateCalendar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in calendarIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		writeErr(w, http.StatusBadRequest, "a calendar needs a name")
		return
	}
	own, _ := s.store.OwnCalendars(ctx, u.ID)
	c := &store.Calendar{UserID: u.ID, Name: strings.TrimSpace(*in.Name), Color: palette[len(own)%len(palette)]}
	if err := applyCalendarIn(c, &in); err != nil {
		s.fail(w, err, "calendar")
		return
	}
	if err := s.store.CreateCalendar(ctx, c); err != nil {
		s.fail(w, err, "create calendar")
		return
	}
	if c.IsDefault {
		_ = s.store.SetDefaultCalendar(ctx, u.ID, c.ID)
	}
	s.touch()
	cid := c.ID
	s.store.AddActivity(ctx, u.ID, &cid, nil, "calendar_created", c.Name, "")
	s.respondCalendar(w, r, http.StatusCreated, c.ID)
}

func applyCalendarIn(c *store.Calendar, in *calendarIn) error {
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if in.Color != nil {
		if !validColor(*in.Color) {
			return badRequest("colors look like #3b82f6")
		}
		c.Color = *in.Color
	}
	if in.Description != nil {
		c.Description = *in.Description
	}
	if in.Timezone != nil {
		if *in.Timezone != "" {
			if _, err := ical.LoadZone(*in.Timezone); err != nil {
				return badRequest("unknown time zone")
			}
		}
		c.Timezone = *in.Timezone
	}
	if in.DefaultAlarms != nil {
		for _, a := range *in.DefaultAlarms {
			if _, err := ical.Duration(a); err != nil {
				return badRequest("default alarms look like -PT10M")
			}
		}
		c.DefaultAlarms = *in.DefaultAlarms
	}
	if in.IsDefault != nil {
		c.IsDefault = *in.IsDefault
	}
	if in.Public != nil {
		c.Public = *in.Public
	}
	return nil
}

func (s *Server) respondCalendar(w http.ResponseWriter, r *http.Request, status int, id int64) {
	cals, err := s.store.ListCalendarsFor(r.Context(), me(r))
	if err != nil {
		s.fail(w, err, "list calendars")
		return
	}
	for _, c := range cals {
		if c.ID == id {
			v := s.calendarView(c, r)
			if sh, err := s.store.ListShares(r.Context(), c.ID); err == nil && (c.Role == store.RoleOwner || c.Role == store.RoleManage) {
				v["shares"] = sh
			}
			writeJSON(w, status, v)
			return
		}
	}
	writeErr(w, http.StatusNotFound, "not found")
}

func (s *Server) handleUpdateCalendar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	role, c, err := s.store.CalendarRole(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	var in calendarIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Visible != nil || in.Notify != nil || in.MyColor != nil || in.Sort != nil {
		if in.MyColor != nil && !validColor(*in.MyColor) {
			writeErr(w, http.StatusBadRequest, "colors look like #3b82f6")
			return
		}
		if err := s.store.SavePrefs(ctx, u.ID, c.ID, in.Visible, in.Notify, in.MyColor, in.Sort); err != nil {
			s.fail(w, err, "save")
			return
		}
		if in.Visible != nil && *in.Visible {
			_ = s.store.SetUserSetting(ctx, u.ID, "left."+itoa64(c.ID), "")
		}
	}
	changesCal := in.Name != nil || in.Color != nil || in.Description != nil || in.Timezone != nil || in.DefaultAlarms != nil || in.Public != nil
	if changesCal {
		if !store.RoleAtLeast(role, store.RoleManage) {
			writeErr(w, http.StatusForbidden, "only the owner can change this calendar")
			return
		}
		if err := applyCalendarIn(c, &in); err != nil {
			s.fail(w, err, "calendar")
			return
		}
		if err := s.store.UpdateCalendar(ctx, c); err != nil {
			s.fail(w, err, "update calendar")
			return
		}
	}
	if in.IsDefault != nil && *in.IsDefault {
		if role != store.RoleOwner {
			// a shared calendar can be one's default too
			_ = s.store.SetUserSetting(ctx, u.ID, "default_calendar_id", itoa64(c.ID))
		} else {
			_ = s.store.SetDefaultCalendar(ctx, u.ID, c.ID)
			_ = s.store.SetUserSetting(ctx, u.ID, "default_calendar_id", itoa64(c.ID))
		}
	}
	s.touch()
	s.respondCalendar(w, r, http.StatusOK, c.ID)
}

func (s *Server) handleDeleteCalendar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	role, c, err := s.store.CalendarRole(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	if role != store.RoleOwner {
		writeErr(w, http.StatusForbidden, "only the owner can delete a calendar — leave it instead")
		return
	}
	if own, _ := s.store.OwnCalendars(ctx, u.ID); len(own) <= 1 {
		writeErr(w, http.StatusBadRequest, "you need at least one calendar")
		return
	}
	if err := s.store.DeleteCalendar(ctx, c.ID); err != nil {
		s.fail(w, err, "delete calendar")
		return
	}
	s.store.AddActivity(ctx, u.ID, nil, nil, "calendar_deleted", c.Name, "")
	s.touch()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReorderCalendars(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ctx := r.Context()
	for i, id := range in.IDs {
		if _, _, err := s.store.CalendarRole(ctx, me(r), id); err != nil {
			continue
		}
		sort := float64(i + 1)
		_ = s.store.SavePrefs(ctx, me(r), id, nil, nil, nil, &sort)
	}
	s.touch()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLeaveCalendar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	role, c, err := s.store.CalendarRole(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	if role == store.RoleOwner {
		writeErr(w, http.StatusBadRequest, "you own this calendar")
		return
	}
	if err := s.store.LeaveCalendar(ctx, u.ID, c.ID); err != nil {
		s.fail(w, err, "leave")
		return
	}
	_ = s.store.SetUserSetting(ctx, u.ID, "left."+itoa64(c.ID), "1")
	s.touch()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRotateCalendarFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role, c, err := s.store.CalendarRole(ctx, me(r), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	if !store.RoleAtLeast(role, store.RoleManage) {
		writeErr(w, http.StatusForbidden, "only the owner can change the feed")
		return
	}
	c.FeedToken = store.RandomToken(18)
	if err := s.store.UpdateCalendar(ctx, c); err != nil {
		s.fail(w, err, "rotate")
		return
	}
	s.respondCalendar(w, r, http.StatusOK, c.ID)
}

// ---------- sharing ----------

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role, c, err := s.store.CalendarRole(ctx, me(r), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	if !store.RoleAtLeast(role, store.RoleManage) {
		writeErr(w, http.StatusForbidden, "only the owner sees who a calendar is shared with")
		return
	}
	sh, err := s.store.ListShares(ctx, c.ID)
	if err != nil {
		s.fail(w, err, "shares")
		return
	}
	writeJSON(w, http.StatusOK, sh)
}

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	role, c, err := s.store.CalendarRole(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	if !store.RoleAtLeast(role, store.RoleManage) {
		writeErr(w, http.StatusForbidden, "only the owner can share this calendar")
		return
	}
	var in struct {
		UserID   *int64 `json:"user_id"`
		Email    string `json:"email"`
		Everyone bool   `json:"everyone"`
		Role     string `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Role == "" {
		in.Role = store.RoleRead
	}
	if !store.ValidRole(in.Role) {
		writeErr(w, http.StatusBadRequest, "role must be freebusy, read, write or manage")
		return
	}
	if role == store.RoleManage && in.Role == store.RoleManage {
		writeErr(w, http.StatusForbidden, "only the owner can hand out manage rights")
		return
	}
	sh := &store.Share{CalendarID: c.ID, Role: in.Role, Everyone: in.Everyone}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	switch {
	case in.Everyone:
	case in.UserID != nil:
		p, err := s.store.GetUser(ctx, *in.UserID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "no such person on this box")
			return
		}
		sh.UserID = &p.ID
		email = p.Email
	case email != "" && strings.Contains(email, "@"):
		if id := s.store.UserIDByEmail(ctx, email); id > 0 {
			sh.UserID = &id
		} else {
			sh.Email = email
		}
	default:
		writeErr(w, http.StatusBadRequest, "share with a person, an email or everyone")
		return
	}
	if sh.UserID != nil && *sh.UserID == c.UserID {
		writeErr(w, http.StatusBadRequest, "that's the owner")
		return
	}
	if err := s.store.UpsertShare(ctx, sh, u.ID); err != nil {
		s.fail(w, err, "share")
		return
	}
	if sh.UserID != nil {
		_ = s.store.SetUserSetting(ctx, *sh.UserID, "left."+itoa64(c.ID), "")
		s.notifyUser(ctx, *sh.UserID, "invites", firstNonEmpty(u.Name, u.Email)+" shared a calendar with you", c.Name+" — "+roleText(in.Role), s.cfg.BaseURL)
	} else if in.Everyone {
		for _, id := range s.store.UserIDs(ctx) {
			if id != c.UserID && id != u.ID {
				s.notifyUser(ctx, id, "invites", firstNonEmpty(u.Name, u.Email)+" shared a calendar with everyone", c.Name+" — "+roleText(in.Role), s.cfg.BaseURL)
			}
		}
	}
	cid := c.ID
	s.store.AddActivity(ctx, c.UserID, &cid, nil, "shared", c.Name, firstNonEmpty(email, map[bool]string{true: "everyone"}[in.Everyone])+" · "+in.Role)
	s.touch()
	list, _ := s.store.ListShares(ctx, c.ID)
	writeJSON(w, http.StatusOK, list)
}

func roleText(r string) string {
	return map[string]string{store.RoleFreeBusy: "sees when you're busy", store.RoleRead: "can see events",
		store.RoleWrite: "can add and change events", store.RoleManage: "can change events and sharing"}[r]
}

func (s *Server) shareForManager(r *http.Request) (*store.Share, *store.Calendar, string, error) {
	ctx := r.Context()
	sh, err := s.store.GetShare(ctx, idParam(r, "sid"))
	if err != nil {
		return nil, nil, "", err
	}
	role, c, err := s.store.CalendarRole(ctx, me(r), sh.CalendarID)
	if err != nil {
		return nil, nil, "", err
	}
	if !store.RoleAtLeast(role, store.RoleManage) {
		return nil, nil, "", errForbidden("only the owner can change sharing")
	}
	return sh, c, role, nil
}

func (s *Server) handleUpdateShare(w http.ResponseWriter, r *http.Request) {
	sh, c, role, err := s.shareForManager(r)
	if err != nil {
		s.fail(w, err, "share")
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := decode(r, &in); err != nil || !store.ValidRole(in.Role) {
		writeErr(w, http.StatusBadRequest, "role must be freebusy, read, write or manage")
		return
	}
	if role == store.RoleManage && in.Role == store.RoleManage {
		writeErr(w, http.StatusForbidden, "only the owner can hand out manage rights")
		return
	}
	sh.Role = in.Role
	if err := s.store.UpsertShare(r.Context(), sh, me(r)); err != nil {
		s.fail(w, err, "share")
		return
	}
	s.touch()
	list, _ := s.store.ListShares(r.Context(), c.ID)
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	sh, c, _, err := s.shareForManager(r)
	if err != nil {
		s.fail(w, err, "share")
		return
	}
	if err := s.store.DeleteShare(r.Context(), sh.ID); err != nil {
		s.fail(w, err, "share")
		return
	}
	s.touch()
	list, _ := s.store.ListShares(r.Context(), c.ID)
	writeJSON(w, http.StatusOK, list)
}

// handlePeople lists who is on the box (for share and invite pickers),
// saying whose free/busy the person may look at.
func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	people, err := s.store.People(ctx)
	if err != nil {
		s.fail(w, err, "people")
		return
	}
	shared := s.sharedOwners(ctx, u.ID)
	out := []map[string]any{}
	for _, p := range people {
		st, _ := s.store.GetSettings(ctx, p.ID)
		out = append(out, map[string]any{"id": p.ID, "name": p.Name, "email": p.Email, "me": p.ID == u.ID,
			"freebusy": p.ID == u.ID || st.FreeBusyShared || shared[p.ID]})
	}
	writeJSON(w, http.StatusOK, out)
}

// sharedOwners are the people who shared at least one calendar with uid.
func (s *Server) sharedOwners(ctx context.Context, uid int64) map[int64]bool {
	out := map[int64]bool{}
	cals, _ := s.store.ListCalendarsFor(ctx, uid)
	for _, c := range cals {
		if c.UserID != uid {
			out[c.UserID] = true
		}
	}
	return out
}

// handleFreeBusy returns busy blocks of people for scheduling: everything
// in calendars they shared with the asker, plus all their calendars when
// they chose to show free/busy to the box. Never titles.
func (s *Server) handleFreeBusy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	loc := s.loc(ctx, u.ID)
	from, to, err := parseRange(r.URL.Query().Get("from"), r.URL.Query().Get("to"), loc)
	if err != nil {
		s.fail(w, err, "range")
		return
	}
	shared := s.sharedOwners(ctx, u.ID)
	myCals, _ := s.store.ListCalendarsFor(ctx, u.ID)
	out := map[string][]map[string]any{}
	for _, part := range strings.Split(r.URL.Query().Get("users"), ",") {
		pid := parseInt64(strings.TrimSpace(part))
		if pid == 0 {
			continue
		}
		st, _ := s.store.GetSettings(ctx, pid)
		var calIDs []int64
		if pid == u.ID || st.FreeBusyShared {
			own, _ := s.store.OwnCalendars(ctx, pid)
			for _, c := range own {
				calIDs = append(calIDs, c.ID)
			}
		} else if shared[pid] {
			for _, c := range myCals {
				if c.UserID == pid {
					calIDs = append(calIDs, c.ID)
				}
			}
		} else {
			continue
		}
		rows, err := s.store.EventsInRange(ctx, calIDs, from, to)
		if err != nil {
			continue
		}
		pu, _ := s.store.GetUser(ctx, pid)
		blocks := []map[string]any{}
		for _, row := range rows {
			res, err := s.resource(ctx, row)
			if err != nil {
				continue
			}
			for _, o := range res.Expand(from, to) {
				if o.Event.Transp == "transparent" || o.Event.Status == "cancelled" || (pu != nil && myStatus(o.Event, pu.Email) == "DECLINED" && len(o.Event.Attendees) > 0 && o.Event.Organizer != nil && o.Event.Organizer.Email != pu.Email) {
					continue
				}
				blocks = append(blocks, map[string]any{"start": fmtWhen(o.Start, o.AllDay, loc), "end": fmtWhen(o.End, o.AllDay, loc), "all_day": o.AllDay})
			}
		}
		// and invitations they accepted
		if inv, err := s.store.InvitedEvents(ctx, pid, from, to); err == nil && pu != nil {
			for _, row := range inv {
				res, err := s.resource(ctx, row)
				if err != nil {
					continue
				}
				for _, o := range res.Expand(from, to) {
					if myStatus(o.Event, pu.Email) == "ACCEPTED" && o.Event.Transp != "transparent" {
						blocks = append(blocks, map[string]any{"start": fmtWhen(o.Start, o.AllDay, loc), "end": fmtWhen(o.End, o.AllDay, loc), "all_day": o.AllDay})
					}
				}
			}
		}
		out[itoa64(pid)] = blocks
	}
	writeJSON(w, http.StatusOK, map[string]any{"busy": out, "tz": loc.String()})
}

// ---------- import / export ----------

func (s *Server) handleExportCalendar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role, c, err := s.store.CalendarRole(ctx, me(r), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	if role == store.RoleFreeBusy {
		writeErr(w, http.StatusForbidden, "you can only see when this calendar is busy")
		return
	}
	rows, err := s.store.ListEvents(ctx, c.ID)
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+store.Slug(c.Name)+`.ics"`)
	_, _ = w.Write([]byte(s.feedICS(ctx, c, rows, role == store.RoleRead)))
}

// handleImport reads an .ics (file upload or text) into a calendar: every
// UID becomes one event; ones already there are replaced.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	calID := idParam(r, "id")
	cal, err := s.targetCalendar(ctx, u, &calID)
	if err != nil {
		s.fail(w, err, "calendar")
		return
	}
	text, err := readUpload(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	n, skipped, err := s.importICS(ctx, u, cal, text, "import")
	if err != nil {
		s.fail(w, err, "import")
		return
	}
	s.touch()
	cid := cal.ID
	s.store.AddActivity(ctx, cal.UserID, &cid, nil, "imported", cal.Name, itoa64(int64(n))+" events")
	writeJSON(w, http.StatusOK, map[string]any{"imported": n, "skipped": skipped})
}

// importICS splits a calendar file into one object per UID.
func (s *Server) importICS(ctx context.Context, u *store.User, cal *store.Calendar, text, source string) (int, int, error) {
	parsed, err := ical.Parse(text)
	if err != nil {
		return 0, 0, badRequest("this is not an iCalendar file")
	}
	byUID := map[string][]*ical.Component{}
	var order []string
	for _, ev := range parsed.ChildrenNamed("VEVENT") {
		uid := ev.Value("UID")
		if uid == "" {
			uid = store.RandomToken(16) + "@pocket-calendar"
			ev.SetRaw("UID", uid)
		}
		if _, ok := byUID[uid]; !ok {
			order = append(order, uid)
		}
		byUID[uid] = append(byUID[uid], ev)
	}
	tzs := parsed.ChildrenNamed("VTIMEZONE")
	loc := s.calLoc(ctx, cal.ID)
	n, skipped := 0, 0
	for _, uid := range order {
		c := ical.NewCalendar()
		c.Children = append(c.Children, tzs...)
		c.Children = append(c.Children, byUID[uid]...)
		existing, _ := s.store.GetEventByUID(ctx, cal.ID, uid)
		href := ""
		if existing != nil {
			href = existing.Href
		}
		if _, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: cal.ID, Href: href, ICS: c.String(), Loc: loc, Source: source, Actor: u.ID}); err != nil {
			skipped++
			continue
		}
		n++
	}
	return n, skipped, nil
}

func readUpload(r *http.Request) (string, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return "", errors.New("upload too large")
		}
		for _, files := range r.MultipartForm.File {
			for _, fh := range files {
				f, err := fh.Open()
				if err != nil {
					return "", err
				}
				defer f.Close()
				b := make([]byte, 0, fh.Size)
				buf := make([]byte, 32<<10)
				for {
					n, err := f.Read(buf)
					b = append(b, buf[:n]...)
					if err != nil {
						break
					}
				}
				return string(b), nil
			}
		}
		return "", errors.New("no file in the upload")
	}
	if strings.Contains(ct, "json") {
		var in struct {
			ICS string `json:"ics"`
		}
		if err := decode(r, &in); err != nil {
			return "", errors.New("invalid json")
		}
		return in.ICS, nil
	}
	b := make([]byte, 0, 1<<16)
	buf := make([]byte, 32<<10)
	body := http.MaxBytesReader(nil, r.Body, 32<<20)
	for {
		n, err := body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	return string(b), nil
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.ListActivity(r.Context(), me(r), 200)
	if err != nil {
		s.fail(w, err, "activity")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func parseInt64(s string) int64 {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int64(r-'0')
	}
	return n
}

var _ = chi.URLParam
var _ = time.Now
