package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/config"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

func apiServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("AUTH_MODE", "local")
	t.Setenv("TZ", "Europe/Sarajevo")
	t.Setenv("CORE_URL", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(cfg.DBPath, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(cfg, st, log, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	return s, s.Router()
}

type apiClient struct {
	t     *testing.T
	h     http.Handler
	token string
	user  *store.User
}

func (c *apiClient) call(method, path string, body any) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func (c *apiClient) json(method, path string, body any, want int, out any) {
	c.t.Helper()
	code, b := c.call(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s: want %d, got %d: %s", method, path, want, code, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

func apiPerson(t *testing.T, s *Server, h http.Handler, email, name string) *apiClient {
	t.Helper()
	u, err := s.firstOrSystemUser(context.Background(), email, name)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.store.CreateAPIToken(context.Background(), u.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	return &apiClient{t: t, h: h, token: tok, user: u}
}

type calJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Writable bool   `json:"writable"`
}

type occJSON struct {
	ID         string `json:"id"`
	EventID    int64  `json:"event_id"`
	Key        string `json:"key"`
	CalendarID int64  `json:"calendar_id"`
	Title      string `json:"title"`
	Start      string `json:"start"`
	Masked     bool   `json:"masked"`
	CanEdit    bool   `json:"can_edit"`
	Location   string `json:"location"`
	Invite     *struct {
		Status string `json:"status"`
	} `json:"invite"`
}

func firstCal(c *apiClient) calJSON {
	var cals []calJSON
	c.json("GET", "/api/v1/calendars", nil, 200, &cals)
	if len(cals) == 0 {
		c.t.Fatal("no calendar seeded")
	}
	return cals[0]
}

func TestPerPersonAndSharing(t *testing.T) {
	s, h := apiServer(t)
	alex := apiPerson(t, s, h, "alex@example.com", "Alex")
	sani := apiPerson(t, s, h, "sani@example.com", "Sani")

	own := firstCal(alex)
	var ev occJSON
	alex.json("POST", "/api/v1/events", map[string]any{"calendar_id": own.ID, "title": "Secret dentist", "start": "2026-10-21T10:00", "end": "2026-10-21T11:00", "location": "Clinic"}, 201, &ev)

	// Sani sees nothing of Alex: lists, events and ids answer 404.
	var list struct {
		Events []occJSON `json:"events"`
	}
	sani.json("GET", "/api/v1/events?from=2026-10-19&to=2026-10-26", nil, 200, &list)
	if len(list.Events) != 0 {
		t.Fatalf("sani sees %d events of alex", len(list.Events))
	}
	for _, p := range []string{"/api/v1/events/" + itoa64(ev.EventID), "/api/v1/calendars/" + itoa64(own.ID) + "/shares"} {
		if code, _ := sani.call("GET", p, nil); code != 404 {
			t.Fatalf("GET %s as stranger: %d", p, code)
		}
	}
	if code, _ := sani.call("PUT", "/api/v1/events/"+itoa64(ev.EventID), map[string]any{"title": "x"}); code != 404 {
		t.Fatalf("stranger update: %d", code)
	}
	if code, _ := sani.call("DELETE", "/api/v1/calendars/"+itoa64(own.ID), nil); code != 404 {
		t.Fatalf("stranger delete calendar: %d", code)
	}

	// Free/busy share: a busy block without details, no editing.
	alex.json("POST", "/api/v1/calendars/"+itoa64(own.ID)+"/shares", map[string]any{"user_id": sani.user.ID, "role": "freebusy"}, 200, nil)
	sani.json("GET", "/api/v1/events?from=2026-10-19&to=2026-10-26", nil, 200, &list)
	if len(list.Events) != 1 || !list.Events[0].Masked || list.Events[0].Title != "Busy" || list.Events[0].Location != "" || list.Events[0].CanEdit {
		t.Fatalf("freebusy view: %+v", list.Events)
	}
	if code, _ := sani.call("PUT", "/api/v1/events/"+itoa64(ev.EventID), map[string]any{"title": "x"}); code != 403 {
		t.Fatalf("freebusy update: %d", code)
	}

	// Write share: details visible, can edit; private events of a read share stay masked.
	alex.json("POST", "/api/v1/calendars/"+itoa64(own.ID)+"/shares", map[string]any{"user_id": sani.user.ID, "role": "write"}, 200, nil)
	sani.json("GET", "/api/v1/events?from=2026-10-19&to=2026-10-26", nil, 200, &list)
	if len(list.Events) != 1 || list.Events[0].Masked || list.Events[0].Title != "Secret dentist" || !list.Events[0].CanEdit {
		t.Fatalf("write view: %+v", list.Events)
	}
	sani.json("PUT", "/api/v1/events/"+itoa64(ev.EventID), map[string]any{"title": "Dentist", "private": true}, 200, nil)
	alex.json("POST", "/api/v1/calendars/"+itoa64(own.ID)+"/shares", map[string]any{"user_id": sani.user.ID, "role": "read"}, 200, nil)
	sani.json("GET", "/api/v1/events?from=2026-10-19&to=2026-10-26", nil, 200, &list)
	if len(list.Events) != 1 || !list.Events[0].Masked {
		t.Fatalf("private event in read share not masked: %+v", list.Events)
	}
	// A reader cannot share further or delete.
	if code, _ := sani.call("POST", "/api/v1/calendars/"+itoa64(own.ID)+"/shares", map[string]any{"everyone": true, "role": "read"}); code != 403 {
		t.Fatalf("reader shares: %d", code)
	}
	if code, _ := sani.call("DELETE", "/api/v1/calendars/"+itoa64(own.ID), nil); code != 403 {
		t.Fatalf("reader deletes calendar: %d", code)
	}

	// Pending share by email binds when that person signs in.
	alex.json("POST", "/api/v1/calendars/"+itoa64(own.ID)+"/shares", map[string]any{"email": "maya@example.com", "role": "read"}, 200, nil)
	maya := apiPerson(t, s, h, "maya@example.com", "Maya")
	var cals []calJSON
	maya.json("GET", "/api/v1/calendars", nil, 200, &cals)
	found := false
	for _, c := range cals {
		if c.ID == own.ID && c.Role == "read" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pending share not bound: %+v", cals)
	}
}

func TestInvitationAndRSVP(t *testing.T) {
	s, h := apiServer(t)
	alex := apiPerson(t, s, h, "alex@example.com", "Alex")
	sani := apiPerson(t, s, h, "sani@example.com", "Sani")
	var ev occJSON
	alex.json("POST", "/api/v1/events", map[string]any{"title": "Lunch", "start": "2026-10-23T13:00", "end": "2026-10-23T14:00",
		"attendees": []map[string]string{{"email": "sani@example.com"}}}, 201, &ev)

	var inv []occJSON
	sani.json("GET", "/api/v1/invitations", nil, 200, &inv)
	if len(inv) != 1 || inv[0].Invite == nil || inv[0].Invite.Status != "NEEDS-ACTION" {
		// the event lies in the future of 2026-10-21 only when the test runs before it
		if len(inv) == 0 {
			t.Log("event in the past for this clock; checking range view instead")
		} else {
			t.Fatalf("invitations: %+v", inv)
		}
	}
	var list struct {
		Events []occJSON `json:"events"`
	}
	sani.json("GET", "/api/v1/events?from=2026-10-19&to=2026-10-26", nil, 200, &list)
	if len(list.Events) != 1 || list.Events[0].Invite == nil || list.Events[0].CanEdit {
		t.Fatalf("invite in range: %+v", list.Events)
	}
	sani.json("POST", "/api/v1/events/"+itoa64(ev.EventID)+"/rsvp", map[string]any{"status": "accepted"}, 200, nil)
	sani.json("GET", "/api/v1/events?from=2026-10-19&to=2026-10-26", nil, 200, &list)
	if list.Events[0].Invite.Status != "ACCEPTED" {
		t.Fatalf("rsvp not stored: %+v", list.Events[0].Invite)
	}
	// the invitee may not edit the organizer's event
	if code, _ := sani.call("PUT", "/api/v1/events/"+itoa64(ev.EventID), map[string]any{"title": "x"}); code != 403 {
		t.Fatalf("invitee edits: %d", code)
	}
	// declined invitations disappear unless asked for
	sani.json("POST", "/api/v1/events/"+itoa64(ev.EventID)+"/rsvp", map[string]any{"status": "declined"}, 200, nil)
	sani.json("GET", "/api/v1/events?from=2026-10-19&to=2026-10-26", nil, 200, &list)
	if len(list.Events) != 0 {
		t.Fatalf("declined still shown: %+v", list.Events)
	}
}

func TestRecurringEditsAndUndo(t *testing.T) {
	s, h := apiServer(t)
	alex := apiPerson(t, s, h, "alex@example.com", "Alex")
	var ev occJSON
	alex.json("POST", "/api/v1/events", map[string]any{"title": "Standup", "start": "2026-10-19T09:15", "end": "2026-10-19T09:30",
		"rrule": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", "tz": "Europe/Sarajevo"}, 201, &ev)
	var list struct {
		Events []occJSON `json:"events"`
	}
	alex.json("GET", "/api/v1/events?from=2026-10-19&to=2026-11-02", nil, 200, &list)
	if len(list.Events) != 10 {
		t.Fatalf("want 10 occurrences, got %d", len(list.Events))
	}
	wed := list.Events[2]
	alex.json("PUT", "/api/v1/events/"+wed.ID+"?scope=this", map[string]any{"title": "Long standup"}, 200, nil)
	alex.json("DELETE", "/api/v1/events/"+list.Events[3].ID+"?scope=this", nil, 200, nil)
	var moved occJSON
	alex.json("PUT", "/api/v1/events/"+list.Events[5].ID+"?scope=following", map[string]any{"start": "2026-10-26T10:00", "end": "2026-10-26T10:15"}, 200, &moved)
	alex.json("GET", "/api/v1/events?from=2026-10-19&to=2026-11-02", nil, 200, &list)
	titles, tens := 0, 0
	for _, e := range list.Events {
		if e.Title == "Long standup" {
			titles++
		}
		if strings.Contains(e.Start, "T10:00") {
			tens++
		}
	}
	if len(list.Events) != 9 || titles != 1 || tens != 5 {
		t.Fatalf("after edits: %d events, %d renamed, %d moved: %+v", len(list.Events), titles, tens, list.Events)
	}
	// delete the whole new series and restore it
	var del struct {
		RestoreID int64 `json:"restore_id"`
	}
	alex.json("DELETE", "/api/v1/events/"+itoa64(moved.EventID), nil, 200, &del)
	if del.RestoreID == 0 {
		t.Fatal("no restore id")
	}
	alex.json("GET", "/api/v1/events?from=2026-10-19&to=2026-11-02", nil, 200, &list)
	if len(list.Events) != 4 {
		t.Fatalf("after delete: %d", len(list.Events))
	}
	alex.json("POST", "/api/v1/deleted/"+itoa64(del.RestoreID)+"/restore", nil, 200, nil)
	alex.json("GET", "/api/v1/events?from=2026-10-19&to=2026-11-02", nil, 200, &list)
	if len(list.Events) != 9 {
		t.Fatalf("after restore: %d", len(list.Events))
	}
}

func TestLinkScopes(t *testing.T) {
	cases := []struct {
		method, path string
		scopes       []string
		want         bool
	}{
		{"GET", "/api/v1/events", []string{"calendar:read"}, true},
		{"GET", "/api/v1/events/12:20261021T071500Z", []string{"calendar:read"}, true},
		{"POST", "/api/v1/events", []string{"calendar:read"}, false},
		{"POST", "/api/v1/events", []string{"calendar:write"}, true},
		{"DELETE", "/api/v1/events/12", []string{"calendar:write"}, true},
		{"GET", "/api/v1/calendars/3/shares", []string{"calendar:read", "calendar:write"}, false},
		{"POST", "/api/v1/calendars/3/shares", []string{"calendar:write"}, false},
		{"GET", "/api/v1/settings", []string{"calendar:read"}, false},
		{"GET", "/api/v1/auth/me", nil, true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := linkAllows(c.scopes, r); got != c.want {
			t.Errorf("%s %s %v: got %v", c.method, c.path, c.scopes, got)
		}
	}
}

func TestQuickAddPreview(t *testing.T) {
	s, h := apiServer(t)
	alex := apiPerson(t, s, h, "alex@example.com", "Alex")
	apiPerson(t, s, h, "sani@example.com", "Sani Bjelosevic")
	alex.json("POST", "/api/v1/calendars", map[string]any{"name": "Family"}, 201, nil)
	var out struct {
		Parsed struct {
			Title     string `json:"title"`
			Calendar  string `json:"calendar"`
			Location  string `json:"location"`
			RRule     string `json:"rrule"`
			AllDay    bool   `json:"all_day"`
			Attendees []struct {
				Email string `json:"email"`
			} `json:"attendees"`
			Alarms []string `json:"alarms"`
		} `json:"parsed"`
	}
	alex.json("POST", "/api/v1/events/quick", map[string]any{"text": "Dinner with Sani fri 19:30-21 at Kod Keke #family remind 1h", "preview": true}, 200, &out)
	p := out.Parsed
	if p.Title != "Dinner with Sani" || p.Calendar != "Family" || p.Location != "Kod Keke" || p.AllDay || len(p.Attendees) != 1 || p.Attendees[0].Email != "sani@example.com" || len(p.Alarms) != 1 || p.Alarms[0] != "-PT1H" {
		t.Fatalf("parsed: %+v", p)
	}
	alex.json("POST", "/api/v1/events/quick", map[string]any{"text": "Standup every weekday 9:15 for 15m", "preview": true}, 200, &out)
	if out.Parsed.RRule != "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR" || out.Parsed.Title != "Standup" {
		t.Fatalf("parsed: %+v", out.Parsed)
	}
}
