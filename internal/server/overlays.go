package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// Overlays are things with a date that live in other Pocket apps (tasks,
// trips, service reminders, invoices due, birthdays…) or on the web (public holidays),
// shown next to the calendars. They are read live, as the person, through a
// Pocket Core app link (or a token they pasted: "direct"), never copied in.

// OverlaySource describes one source.
type OverlaySource struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	App         string   `json:"app"`
	Slug        string   `json:"-"`
	Scopes      []string `json:"scopes"`
	Reason      string   `json:"reason"`
	Color       string   `json:"default_color"`
	DefaultOn   bool     `json:"default_on"`
	Actions     []string `json:"actions"`
	Description string   `json:"description"`
}

var overlaySources = []OverlaySource{
	{ID: "tasks", Name: "Tasks", App: "Pocket Tasks", Slug: "tasks", Scopes: []string{"tasks:read", "tasks:write"}, Color: "#2563eb", DefaultOn: true,
		Actions: []string{"complete", "reschedule"}, Reason: "Show your dated tasks on the calendar; tick them off and drag them to another time",
		Description: "Tasks with a start or a deadline on their day (timed ones at their time), and what you finished."},
	{ID: "travel", Name: "Trips", App: "Pocket Travel", Slug: "travel", Scopes: []string{"trips:read"}, Color: "#0ea5e9", DefaultOn: true,
		Reason: "Show your trips as all-day bars across the days you are away", Description: "Every trip from its first to its last day."},
	{ID: "garage", Name: "Car service", App: "Pocket Garage", Slug: "garage", Scopes: []string{"vehicles:read"}, Color: "#64748b", DefaultOn: true,
		Reason: "Show the next service reminder of each vehicle on its due day", Description: "Each vehicle's next due service or inspection."},
	{ID: "invoicing", Name: "Invoices due", App: "Pocket Invoicing", Slug: "invoicing", Scopes: []string{"invoices:read"}, Color: "#16a34a", DefaultOn: true,
		Reason: "Show when unpaid invoices fall due", Description: "Open invoices on their due date, with the amount still owed."},
	{ID: "habits", Name: "Habits", App: "Pocket Habits", Slug: "habits", Scopes: []string{"habits:read"}, Color: "#f97316",
		Reason: "Show which habits are planned for each day", Description: "Habits on the weekdays they are planned for."},
	{ID: "collaborator", Name: "Reviews due", App: "Pocket Collaborator", Slug: "collaborator", Scopes: []string{"reviews:read"}, Color: "#c026d3",
		Reason: "Show the deadlines of document reviews", Description: "Open document reviews on their due date."},
	{ID: "projects", Name: "Project deadlines", App: "Pocket Projects", Slug: "projects", Scopes: []string{"projects:read"}, Color: "#5e6ad2", DefaultOn: true,
		Reason:      "Show the due dates of your issues, project milestones and target dates, and your teams' cycles",
		Description: "Issues assigned to you on their due date, milestones and project start/target dates of the projects you are in, and cycles as bars."},
	{ID: "birthdays", Name: "Birthdays", App: "Pocket Contacts", Slug: "contacts", Scopes: []string{"contacts:read"}, Color: "#e11d48", DefaultOn: true,
		Reason:      "Show your contacts' birthdays and anniversaries, and suggest guests from your contacts",
		Description: "Birthdays and anniversaries of your contacts on their day (with the age when the year is known); guests from your contacts in the event editor."},
	{ID: "health", Name: "Workouts & sleep", App: "Pocket Health", Slug: "health", Scopes: []string{"health:read"}, Color: "#e11d48",
		Reason: "Show your workouts at their time and each night's sleep on its morning", Description: "Workouts as timed items (kind, duration, distance, energy), nights as an all-day note with the hours slept."},
	{ID: "holidays", Name: "Public holidays", App: "date.nager.at", Color: "#e11d48", DefaultOn: true,
		Description: "Public holidays of the country chosen in Settings."},
}

func overlaySource(id string) (OverlaySource, bool) {
	for _, s := range overlaySources {
		if s.ID == id {
			return s, true
		}
	}
	return OverlaySource{}, false
}

// overlayUses are the links Core creates for this app.
func overlayUses() []core.Use {
	var out []core.Use
	for _, s := range overlaySources {
		if s.Slug != "" {
			out = append(out, core.Use{App: s.Slug, Scopes: s.Scopes, Reason: s.Reason})
		}
	}
	return out
}

// overlayMode is the mode in effect for a person: chosen, else direct when a
// token was saved, else on (through Core) or off by default.
func (s *Server) overlayMode(ctx context.Context, uid int64, st store.Settings, src OverlaySource) string {
	switch m := st.Overlays[src.ID]; m {
	case modeCore, modeDirect, modeOff:
		return m
	}
	if st.OverlayTokens[src.ID] {
		return modeDirect
	}
	if src.DefaultOn {
		if src.ID == "holidays" && st.HolidayCountry == "" && s.cfg.HolidayCountry == "" {
			return modeOff
		}
		return modeCore
	}
	return modeOff
}

const (
	modeCore   = "core"
	modeDirect = "direct"
	modeOff    = "off"
)

// OverlayItem is one dated thing from a source.
type OverlayItem struct {
	ID       string         `json:"id"`
	Source   string         `json:"source"`
	Ref      string         `json:"ref"`
	Kind     string         `json:"kind"`
	Title    string         `json:"title"`
	Subtitle string         `json:"subtitle,omitempty"`
	Start    string         `json:"start"`
	End      string         `json:"end"`
	AllDay   bool           `json:"all_day"`
	Color    string         `json:"color"`
	URL      string         `json:"url,omitempty"`
	Done     bool           `json:"done"`
	Overdue  bool           `json:"overdue,omitempty"`
	Actions  []string       `json:"actions"`
	Meta     map[string]any `json:"meta,omitempty"`
}

type overlayCacheEntry struct {
	at    time.Time
	items []OverlayItem
	err   string
}

// sibling is how this app reaches one person's other Pocket app.
type sibling struct {
	base, token, public string
}

func linkReason(app, reason string) string {
	switch reason {
	case "off_person":
		return "you switched the Calendar → " + app + " link off in Pocket Core (Settings → Apps acting for me)"
	case "off_admin":
		return "the Calendar → " + app + " link is switched off in Pocket Core (Links)"
	case "target_missing":
		return app + " is not installed on this box"
	case "not_offered":
		return "this " + app + " version does not accept app links yet"
	case "not_declared":
		return "Pocket Core has not seen this Calendar version yet; it re-registers within a minute of starting"
	case "not_in_audience":
		return "you do not have access to " + app + " in Pocket Core"
	case "unknown_user":
		return "your email is not a person in Pocket Core"
	}
	return ""
}

func (s *Server) siblingFor(ctx context.Context, u *store.User, src OverlaySource, st store.Settings) (*sibling, error) {
	switch s.overlayMode(ctx, u.ID, st, src) {
	case modeOff:
		return nil, errors.New("switched off")
	case modeDirect:
		token := s.store.UserSetting(ctx, u.ID, "overlay.token."+src.ID)
		base := st.OverlayURLs[src.ID]
		if token == "" || base == "" {
			return nil, fmt.Errorf("a direct connection needs %s's address and an API token", src.App)
		}
		return &sibling{base: strings.TrimRight(base, "/"), token: token, public: strings.TrimRight(base, "/")}, nil
	}
	if !s.core.Configured() {
		return nil, errors.New("there is no Pocket Core on this box; use a direct connection")
	}
	tok, err := s.core.ActAs(ctx, u.Email, src.Slug, src.Scopes...)
	if err != nil {
		var le *core.LinkError
		if errors.As(err, &le) {
			if t := linkReason(src.App, le.Reason); t != "" {
				return nil, errors.New(t)
			}
		}
		return nil, err
	}
	return &sibling{base: strings.TrimRight(tok.URL, "/"), token: tok.AccessToken, public: strings.TrimRight(firstNonEmpty(tok.PublicURL, tok.URL), "/")}, nil
}

var siblingHTTP = &http.Client{Timeout: 15 * time.Second}

func (sb *sibling) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, sb.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+sb.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PocketCalendar/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := siblingHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&e)
		return fmt.Errorf("%s answered %d %s", path, res.StatusCode, e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(out)
}

func (s *Server) overlayRoutes(r chi.Router) {
	r.Get("/overlays", s.handleOverlays)
	r.Get("/overlays/sources", s.handleOverlaySources)
	r.Put("/overlays/sources/{id}", s.handleSaveOverlaySource)
	r.Post("/overlays/tasks/{ref}/complete", s.handleOverlayTaskComplete)
	r.Put("/overlays/tasks/{ref}", s.handleOverlayTaskMove)
}

// handleOverlays returns the items of every source the person has on.
func (s *Server) handleOverlays(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	loc := s.loc(ctx, u.ID)
	if tz := r.URL.Query().Get("tz"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	from, to, err := parseRange(r.URL.Query().Get("from"), r.URL.Query().Get("to"), loc)
	if err != nil {
		s.fail(w, err, "range")
		return
	}
	st, _ := s.store.GetSettings(ctx, u.ID)
	only := map[string]bool{}
	for _, id := range strings.Split(r.URL.Query().Get("sources"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			only[id] = true
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	items := []OverlayItem{}
	errs := map[string]string{}
	for _, src := range overlaySources {
		if len(only) > 0 && !only[src.ID] {
			continue
		}
		if s.overlayMode(ctx, u.ID, st, src) == modeOff {
			continue
		}
		wg.Add(1)
		go func(src OverlaySource) {
			defer wg.Done()
			got, err := s.overlayItems(ctx, u, st, src, from, to, loc)
			color := firstNonEmpty(st.OverlayColors[src.ID], src.Color)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[src.ID] = err.Error()
				return
			}
			for _, it := range got {
				if it.Color == "" {
					it.Color = color
				}
				if it.Actions == nil {
					it.Actions = []string{}
				}
				items = append(items, it)
			}
		}(src)
	}
	wg.Wait()
	sort.SliceStable(items, func(i, j int) bool { return items[i].Start < items[j].Start })
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "errors": errs})
}

func (s *Server) overlayItems(ctx context.Context, u *store.User, st store.Settings, src OverlaySource, from, to time.Time, loc *time.Location) ([]OverlayItem, error) {
	key := fmt.Sprintf("%d|%s|%d|%d|%s", u.ID, src.ID, from.Unix(), to.Unix(), loc)
	s.overlayMu.Lock()
	if e, ok := s.overlayCache[key]; ok && time.Since(e.at) < time.Minute {
		s.overlayMu.Unlock()
		if e.err != "" {
			return nil, errors.New(e.err)
		}
		return e.items, nil
	}
	s.overlayMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var items []OverlayItem
	var err error
	if src.ID == "holidays" {
		items, err = s.holidayItems(ctx, firstNonEmpty(st.HolidayCountry, s.cfg.HolidayCountry), from, to)
	} else {
		var sb *sibling
		sb, err = s.siblingFor(ctx, u, src, st)
		if err == nil {
			switch src.ID {
			case "tasks":
				items, err = taskItems(ctx, sb, from, to, loc)
			case "travel":
				items, err = tripItems(ctx, sb, from, to)
			case "garage":
				items, err = garageItems(ctx, sb, from, to)
			case "invoicing":
				items, err = invoiceItems(ctx, sb, from, to)
			case "habits":
				items, err = habitItems(ctx, sb, from, to, loc)
			case "projects":
				items, err = projectItems(ctx, sb, from, to)
			case "collaborator":
				items, err = reviewItems(ctx, sb, from, to)
			case "birthdays":
				items, err = birthdayItems(ctx, sb, from, to, loc)
			case "health":
				items, err = healthItems(ctx, sb, from, to)
			}
		}
	}
	e := overlayCacheEntry{at: time.Now(), items: items}
	if err != nil {
		e.err = err.Error()
	}
	s.overlayMu.Lock()
	if len(s.overlayCache) > 2000 {
		s.overlayCache = map[string]overlayCacheEntry{}
	}
	s.overlayCache[key] = e
	s.overlayMu.Unlock()
	return items, err
}

func (s *Server) dropOverlayCache(uid int64, source string) {
	prefix := fmt.Sprintf("%d|%s|", uid, source)
	s.overlayMu.Lock()
	for k := range s.overlayCache {
		if strings.HasPrefix(k, prefix) {
			delete(s.overlayCache, k)
		}
	}
	s.overlayMu.Unlock()
}

func dayAfter(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

func inDays(d string, from, to time.Time) bool {
	if len(d) < 10 {
		return false
	}
	day := d[:10]
	return day >= from.AddDate(0, 0, -1).Format("2006-01-02") && day <= to.Format("2006-01-02")
}

// ---------- Pocket Tasks ----------

type taskJSON struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Notes       string `json:"notes"`
	Status      string `json:"status"`
	Start       string `json:"start"`
	Due         string `json:"due"`
	EstimateMin int    `json:"estimate_min"`
	Priority    int    `json:"priority"`
	CompletedAt string `json:"completed_at"`
	Overdue     bool   `json:"overdue"`
	List        *struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"list"`
	Repeat *struct {
		RRule string `json:"rrule"`
	} `json:"repeat"`
}

func taskItems(ctx context.Context, sb *sibling, from, to time.Time, loc *time.Location) ([]OverlayItem, error) {
	out := []OverlayItem{}
	seen := map[string]bool{}
	for chunk := from; chunk.Before(to); chunk = chunk.AddDate(0, 0, 90) {
		end := chunk.AddDate(0, 0, 90)
		if end.After(to) {
			end = to
		}
		var res struct {
			Days map[string]struct {
				Start []taskJSON `json:"start"`
				Due   []taskJSON `json:"due"`
				Done  []taskJSON `json:"done"`
			} `json:"days"`
		}
		q := fmt.Sprintf("/api/v1/calendar?from=%s&to=%s", chunk.In(loc).Format("2006-01-02"), end.In(loc).Format("2006-01-02"))
		if err := sb.call(ctx, "GET", q, nil, &res); err != nil {
			return nil, err
		}
		for day, d := range res.Days {
			add := func(t taskJSON, kind string) {
				id := fmt.Sprintf("tasks:%d:%s:%s", t.ID, kind, day)
				if seen[fmt.Sprintf("%d:%s", t.ID, day)] || seen[id] {
					return
				}
				seen[fmt.Sprintf("%d:%s", t.ID, day)] = true
				it := OverlayItem{ID: id, Source: "tasks", Ref: fmt.Sprint(t.ID), Kind: kind, Title: t.Title, AllDay: true,
					Start: day, End: dayAfter(day), URL: sb.public + fmt.Sprintf("/today?task=%d", t.ID), Done: kind == "done",
					Overdue: t.Overdue && kind != "done", Actions: []string{"complete", "reschedule"},
					Meta: map[string]any{"priority": t.Priority, "estimate_min": t.EstimateMin, "status": t.Status, "start": t.Start, "due": t.Due}}
				if t.List != nil {
					it.Subtitle = t.List.Name
					it.Meta["list_color"] = t.List.Color
				}
				if t.Repeat != nil && t.Repeat.RRule != "" {
					it.Meta["repeats"] = true
				}
				when := t.Start
				if kind == "due" {
					when = t.Due
				}
				if kind != "done" && len(when) > 10 {
					if st, err := time.ParseInLocation("2006-01-02T15:04:05", when, loc); err == nil {
						dur := time.Duration(t.EstimateMin) * time.Minute
						if dur <= 0 {
							dur = 30 * time.Minute
						}
						it.AllDay = false
						it.Start, it.End = st.Format(time.RFC3339), st.Add(dur).Format(time.RFC3339)
					}
				}
				out = append(out, it)
			}
			for _, t := range d.Start {
				add(t, "task")
			}
			for _, t := range d.Due {
				add(t, "due")
			}
			for _, t := range d.Done {
				add(t, "done")
			}
		}
	}
	return out, nil
}

func (s *Server) tasksSibling(ctx context.Context, u *store.User) (*sibling, error) {
	src, _ := overlaySource("tasks")
	st, _ := s.store.GetSettings(ctx, u.ID)
	return s.siblingFor(ctx, u, src, st)
}

func (s *Server) handleOverlayTaskComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	sb, err := s.tasksSibling(ctx, u)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Pocket Tasks: "+err.Error())
		return
	}
	var in struct {
		Done *bool `json:"done"`
	}
	_ = decode(r, &in)
	done := in.Done == nil || *in.Done
	var out map[string]any
	if err := sb.call(ctx, "POST", "/api/v1/tasks/"+chi.URLParam(r, "ref")+"/complete", map[string]any{"done": done}, &out); err != nil {
		writeErr(w, http.StatusBadGateway, "Pocket Tasks: "+err.Error())
		return
	}
	s.dropOverlayCache(u.ID, "tasks")
	writeJSON(w, http.StatusOK, out)
}

// handleOverlayTaskMove reschedules a task dragged on the grid: kind "task"
// moves its start (and estimate), "due" its deadline.
func (s *Server) handleOverlayTaskMove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Kind   string `json:"kind"`
		Start  string `json:"start"`
		End    string `json:"end"`
		AllDay bool   `json:"all_day"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	loc := s.loc(ctx, u.ID)
	st, err := parseWhen(in.Start, in.AllDay, loc)
	if err != nil {
		s.fail(w, err, "move task")
		return
	}
	val := st.Format("2006-01-02")
	body := map[string]any{}
	if !in.AllDay {
		val = st.In(loc).Format("2006-01-02T15:04:05")
		if in.End != "" {
			if en, err := parseWhen(in.End, false, loc); err == nil && en.After(st) {
				body["estimate_min"] = int(en.Sub(st).Minutes())
			}
		}
	}
	field := "start"
	if in.Kind == "due" {
		field = "due"
	}
	body[field] = val
	sb, err := s.tasksSibling(ctx, u)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Pocket Tasks: "+err.Error())
		return
	}
	var out map[string]any
	if err := sb.call(ctx, "PUT", "/api/v1/tasks/"+chi.URLParam(r, "ref"), body, &out); err != nil {
		writeErr(w, http.StatusBadGateway, "Pocket Tasks: "+err.Error())
		return
	}
	s.dropOverlayCache(u.ID, "tasks")
	writeJSON(w, http.StatusOK, out)
}

// ---------- Pocket Travel ----------

func tripItems(ctx context.Context, sb *sibling, from, to time.Time) ([]OverlayItem, error) {
	var trips []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Destination string `json:"destination"`
		StartDate   string `json:"start_date"`
		EndDate     string `json:"end_date"`
		Status      string `json:"status"`
		Color       string `json:"color"`
	}
	if err := sb.call(ctx, "GET", "/api/v1/trips", nil, &trips); err != nil {
		return nil, err
	}
	out := []OverlayItem{}
	f, t := from.Format("2006-01-02"), to.Format("2006-01-02")
	for _, tr := range trips {
		if len(tr.StartDate) < 10 || tr.Status == "cancelled" {
			continue
		}
		end := firstNonEmpty(tr.EndDate, tr.StartDate)
		if end[:10] < f || tr.StartDate[:10] > t {
			continue
		}
		out = append(out, OverlayItem{ID: fmt.Sprintf("travel:%d", tr.ID), Source: "travel", Ref: fmt.Sprint(tr.ID), Kind: "trip",
			Title: "✈ " + tr.Name, Subtitle: tr.Destination, Start: tr.StartDate[:10], End: dayAfter(end[:10]), AllDay: true,
			Color: tr.Color, URL: sb.public + fmt.Sprintf("/trips/%d", tr.ID), Meta: map[string]any{"status": tr.Status}})
	}
	return out, nil
}

// ---------- Pocket Health ----------

// healthItems reads Health's calendar overlay: workouts (timed) and nights (all-day).
func healthItems(ctx context.Context, sb *sibling, from, to time.Time) ([]OverlayItem, error) {
	var res struct {
		Items []struct {
			ID       string `json:"id"`
			Ref      string `json:"ref"`
			Kind     string `json:"kind"`
			Title    string `json:"title"`
			Subtitle string `json:"subtitle"`
			Start    string `json:"start"`
			End      string `json:"end"`
			AllDay   bool   `json:"all_day"`
			URL      string `json:"url"`
			Color    string `json:"color"`
		} `json:"items"`
	}
	q := url.Values{"from": {from.Format("2006-01-02")}, "to": {to.Format("2006-01-02")}}
	if err := sb.call(ctx, "GET", "/api/v1/overlay?"+q.Encode(), nil, &res); err != nil {
		return nil, err
	}
	out := []OverlayItem{}
	for _, it := range res.Items {
		item := OverlayItem{ID: "health:" + it.ID, Source: "health", Ref: it.Ref, Kind: it.Kind, Title: it.Title, Subtitle: it.Subtitle, Start: it.Start, End: it.End, AllDay: it.AllDay, Color: it.Color, URL: it.URL}
		if it.Kind == "workout" {
			item.Title = "🏃 " + it.Title
		} else if it.Kind == "sleep" {
			item.Title = "🌙 " + it.Title
			if len(it.Start) >= 10 {
				item.Start = it.Start[:10]
				item.End = dayAfter(it.Start[:10])
			}
		}
		if item.URL != "" && sb.public != "" && !strings.HasPrefix(item.URL, "http") {
			item.URL = sb.public + item.URL
		}
		out = append(out, item)
	}
	return out, nil
}

// ---------- Pocket Garage ----------

func garageItems(ctx context.Context, sb *sibling, from, to time.Time) ([]OverlayItem, error) {
	var vehicles []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Make    string `json:"make"`
		Model   string `json:"model"`
		Summary *struct {
			Next *struct {
				ID      int64  `json:"id"`
				Title   string `json:"title"`
				DueDate string `json:"due_date"`
				Urgency string `json:"urgency"`
				Status  string `json:"status"`
			} `json:"next_reminder"`
		} `json:"summary"`
	}
	if err := sb.call(ctx, "GET", "/api/v1/vehicles", nil, &vehicles); err != nil {
		return nil, err
	}
	out := []OverlayItem{}
	for _, v := range vehicles {
		if v.Summary == nil || v.Summary.Next == nil || len(v.Summary.Next.DueDate) < 10 || !inDays(v.Summary.Next.DueDate, from, to) {
			continue
		}
		n := v.Summary.Next
		name := firstNonEmpty(v.Name, strings.TrimSpace(v.Make+" "+v.Model))
		out = append(out, OverlayItem{ID: fmt.Sprintf("garage:%d", n.ID), Source: "garage", Ref: fmt.Sprint(n.ID), Kind: "service",
			Title: "🔧 " + n.Title, Subtitle: name, Start: n.DueDate[:10], End: dayAfter(n.DueDate[:10]), AllDay: true,
			Overdue: n.Urgency == "overdue", URL: sb.public + fmt.Sprintf("/vehicles/%d", v.ID), Meta: map[string]any{"urgency": n.Urgency}})
	}
	return out, nil
}

// ---------- Pocket Invoicing ----------

func invoiceItems(ctx context.Context, sb *sibling, from, to time.Time) ([]OverlayItem, error) {
	var res struct {
		Items []struct {
			ID         int64   `json:"id"`
			Number     string  `json:"number"`
			ClientName string  `json:"client_name"`
			Status     string  `json:"status"`
			DueDate    string  `json:"due_date"`
			Currency   string  `json:"currency"`
			Total      float64 `json:"total"`
			Balance    float64 `json:"balance"`
		} `json:"items"`
	}
	if err := sb.call(ctx, "GET", "/api/v1/invoices?status=outstanding&limit=200", nil, &res); err != nil {
		return nil, err
	}
	out := []OverlayItem{}
	today := time.Now().Format("2006-01-02")
	for _, inv := range res.Items {
		if len(inv.DueDate) < 10 || !inDays(inv.DueDate, from, to) {
			continue
		}
		out = append(out, OverlayItem{ID: fmt.Sprintf("invoicing:%d", inv.ID), Source: "invoicing", Ref: fmt.Sprint(inv.ID), Kind: "invoice",
			Title: "Invoice " + inv.Number + " due", Subtitle: fmt.Sprintf("%s · %.2f %s", inv.ClientName, inv.Balance, inv.Currency),
			Start: inv.DueDate[:10], End: dayAfter(inv.DueDate[:10]), AllDay: true, Overdue: inv.DueDate[:10] < today,
			URL: sb.public + fmt.Sprintf("/invoices/%d", inv.ID), Meta: map[string]any{"balance": inv.Balance, "currency": inv.Currency, "status": inv.Status}})
	}
	return out, nil
}

// ---------- Pocket Habits ----------

func habitItems(ctx context.Context, sb *sibling, from, to time.Time, loc *time.Location) ([]OverlayItem, error) {
	var habits []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Color     string `json:"color"`
		Days      string `json:"days"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		Archived  bool   `json:"archived"`
	}
	if err := sb.call(ctx, "GET", "/api/v1/habits", nil, &habits); err != nil {
		return nil, err
	}
	out := []OverlayItem{}
	if to.Sub(from) > 45*24*time.Hour {
		return out, nil // too many to be useful on a year view
	}
	for d := from.In(loc); d.Before(to); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		wd := int(d.Weekday())
		if wd == 0 {
			wd = 7
		}
		for _, h := range habits {
			if h.Archived || (h.StartDate != "" && day < h.StartDate) || (h.EndDate != "" && day > h.EndDate) {
				continue
			}
			if h.Days != "" && !strings.Contains(h.Days, fmt.Sprint(wd)) {
				continue
			}
			out = append(out, OverlayItem{ID: fmt.Sprintf("habits:%d:%s", h.ID, day), Source: "habits", Ref: fmt.Sprint(h.ID), Kind: "habit",
				Title: h.Name, Start: day, End: dayAfter(day), AllDay: true, Color: h.Color, URL: sb.public + "/"})
		}
	}
	return out, nil
}

// ---------- Pocket Collaborator ----------

func reviewItems(ctx context.Context, sb *sibling, from, to time.Time) ([]OverlayItem, error) {
	var reviews []struct {
		ID        int64   `json:"id"`
		Title     string  `json:"title"`
		Status    string  `json:"status"`
		DueDate   string  `json:"due_date"`
		DecidedAt *string `json:"decided_at"`
	}
	if err := sb.call(ctx, "GET", "/api/v1/reviews", nil, &reviews); err != nil {
		return nil, err
	}
	out := []OverlayItem{}
	for _, rv := range reviews {
		if len(rv.DueDate) < 10 || (rv.DecidedAt != nil && *rv.DecidedAt != "") || !inDays(rv.DueDate, from, to) {
			continue
		}
		out = append(out, OverlayItem{ID: fmt.Sprintf("collaborator:%d", rv.ID), Source: "collaborator", Ref: fmt.Sprint(rv.ID), Kind: "review",
			Title: "Review: " + rv.Title, Subtitle: rv.Status, Start: rv.DueDate[:10], End: dayAfter(rv.DueDate[:10]), AllDay: true,
			URL: sb.public + fmt.Sprintf("/reviews/%d", rv.ID)})
	}
	return out, nil
}

// projectItems reads Pocket Projects' calendar: issue due dates, milestones,
// project starts/targets (all-day) and cycles (bars over their days).
func projectItems(ctx context.Context, sb *sibling, from, to time.Time) ([]OverlayItem, error) {
	var res struct {
		Items []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Title    string `json:"title"`
			Subtitle string `json:"subtitle"`
			Date     string `json:"date"`
			End      string `json:"end"`
			URL      string `json:"url"`
			Done     bool   `json:"done"`
			RefID    int64  `json:"ref_id"`
		} `json:"items"`
	}
	q := "/api/v1/calendar?scope=mine&from=" + from.AddDate(0, 0, -1).Format("2006-01-02") + "&to=" + to.Format("2006-01-02")
	if err := sb.call(ctx, "GET", q, nil, &res); err != nil {
		return nil, err
	}
	today := time.Now().Format("2006-01-02")
	out := []OverlayItem{}
	for _, it := range res.Items {
		if len(it.Date) < 10 {
			continue
		}
		end := dayAfter(it.Date[:10])
		if it.Kind == "cycle" && len(it.End) >= 10 {
			end = dayAfter(it.End[:10])
		}
		out = append(out, OverlayItem{ID: "projects:" + it.ID, Source: "projects", Ref: fmt.Sprint(it.RefID), Kind: it.Kind, Title: it.Title, Subtitle: it.Subtitle,
			Start: it.Date[:10], End: end, AllDay: true, URL: it.URL, Done: it.Done,
			Overdue: it.Kind == "issue" && !it.Done && it.Date[:10] < today})
	}
	return out, nil
}

// ---------- public holidays ----------

var holidayCache = struct {
	sync.Mutex
	m map[string]holidayYear
}{m: map[string]holidayYear{}}

type holidayYear struct {
	at   time.Time
	days []struct {
		Date      string   `json:"date"`
		LocalName string   `json:"localName"`
		Name      string   `json:"name"`
		Global    bool     `json:"global"`
		Counties  []string `json:"counties"`
		Types     []string `json:"types"`
	}
}

func (s *Server) holidayItems(ctx context.Context, country string, from, to time.Time) ([]OverlayItem, error) {
	out := []OverlayItem{}
	if country == "" {
		return out, nil
	}
	country = strings.ToUpper(country)
	for y := from.Year(); y <= to.Year(); y++ {
		key := fmt.Sprintf("%s-%d", country, y)
		holidayCache.Lock()
		hy, ok := holidayCache.m[key]
		holidayCache.Unlock()
		if !ok || time.Since(hy.at) > 24*time.Hour {
			req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://date.nager.at/api/v3/PublicHolidays/%d/%s", y, country), nil)
			res, err := siblingHTTP.Do(req)
			if err != nil {
				if ok {
					hy.at = time.Now()
				} else {
					return nil, fmt.Errorf("holidays: %w", err)
				}
			} else {
				func() {
					defer res.Body.Close()
					if res.StatusCode == http.StatusOK {
						hy = holidayYear{at: time.Now()}
						_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&hy.days)
					}
				}()
			}
			holidayCache.Lock()
			holidayCache.m[key] = hy
			holidayCache.Unlock()
		}
		for _, d := range hy.days {
			if !inDays(d.Date, from, to) {
				continue
			}
			sub := d.Name
			if d.LocalName == d.Name {
				sub = ""
			}
			if !d.Global && len(d.Counties) > 0 {
				sub = strings.TrimSpace(sub + " (" + strings.Join(d.Counties, ", ") + ")")
			}
			out = append(out, OverlayItem{ID: "holidays:" + country + ":" + d.Date + ":" + d.Name, Source: "holidays", Ref: d.Date,
				Kind: "holiday", Title: d.LocalName, Subtitle: sub, Start: d.Date, End: dayAfter(d.Date), AllDay: true})
		}
	}
	return out, nil
}

// ---------- settings ----------

func (s *Server) handleOverlaySources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	out := []map[string]any{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, src := range overlaySources {
		mode := s.overlayMode(ctx, u.ID, st, src)
		row := map[string]any{"id": src.ID, "name": src.Name, "app": src.App, "description": src.Description, "scopes": src.Scopes,
			"default_on": src.DefaultOn, "actions": src.Actions, "mode": mode, "color": firstNonEmpty(st.OverlayColors[src.ID], src.Color),
			"default_color": src.Color, "url": st.OverlayURLs[src.ID], "has_token": st.OverlayTokens[src.ID], "pocket": src.Slug != "",
			"status": "", "detail": ""}
		out = append(out, row)
		if src.Slug == "" {
			if mode != modeOff {
				row["status"] = "ok"
			}
			continue
		}
		wg.Add(1)
		go func(src OverlaySource, row map[string]any, mode string) {
			defer wg.Done()
			status, detail := "off", ""
			switch mode {
			case modeDirect:
				status = "ok"
				if st.OverlayURLs[src.ID] == "" || !st.OverlayTokens[src.ID] {
					status, detail = "error", "needs the app's address and an API token"
				}
			case modeCore:
				if !s.core.Configured() {
					status, detail = "error", "there is no Pocket Core on this box"
				} else if on, reason := s.core.LinkOn(ctx, u.Email, src.Slug, src.Scopes...); on {
					status = "ok"
				} else {
					status, detail = "error", firstNonEmpty(linkReason(src.App, reason), reason)
				}
			}
			mu.Lock()
			row["status"], row["detail"] = status, detail
			mu.Unlock()
		}(src, row, mode)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSaveOverlaySource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	src, ok := overlaySource(chi.URLParam(r, "id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such source")
		return
	}
	var in struct {
		Mode  *string `json:"mode"`
		Color *string `json:"color"`
		URL   *string `json:"url"`
		Token *string `json:"token"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Mode != nil {
		switch *in.Mode {
		case modeCore, modeDirect, modeOff:
		default:
			writeErr(w, http.StatusBadRequest, "mode must be core, direct or off")
			return
		}
		_ = s.store.SetUserSetting(ctx, u.ID, "overlay.mode."+src.ID, *in.Mode)
	}
	if in.Color != nil {
		if !validColor(*in.Color) {
			writeErr(w, http.StatusBadRequest, "colors look like #3b82f6")
			return
		}
		_ = s.store.SetUserSetting(ctx, u.ID, "overlay.color."+src.ID, *in.Color)
	}
	if in.URL != nil {
		_ = s.store.SetUserSetting(ctx, u.ID, "overlay.url."+src.ID, strings.TrimRight(strings.TrimSpace(*in.URL), "/"))
	}
	if in.Token != nil {
		_ = s.store.SetUserSetting(ctx, u.ID, "overlay.token."+src.ID, strings.TrimSpace(*in.Token))
	}
	s.dropOverlayCache(u.ID, src.ID)
	if src.ID == "birthdays" {
		s.dropContactsCache(u.ID)
	}
	go s.reportModes(context.WithoutCancel(ctx))
	s.handleOverlaySources(w, r)
}
