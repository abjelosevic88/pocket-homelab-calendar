package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/config"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// moveServer is apiServer with a fake Core that knows Pocket Move's link
// tokens for alice: pcx_export (move:export) and pcx_import (move:import).
func moveServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	return linkServer(t, "move", map[string]string{"pcx_export": core.ScopeMoveExport, "pcx_import": core.ScopeMoveImport})
}

// linkServer is apiServer with a fake Core that knows these link tokens
// (token → scopes): each acts for alice, on behalf of the app with that slug.
func linkServer(t *testing.T, app string, tokens map[string]string) (*Server, http.Handler) {
	t.Helper()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/auth/introspect" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		var in struct{ Token string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		scope := tokens[in.Token]
		if scope == "" {
			_, _ = w.Write([]byte(`{"active":false}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "kind": "delegated", "iss": "http://core.test", "scope": scope,
			"audience": "calendar", "app": map[string]any{"id": 9, "slug": app, "name": "Pocket " + strings.ToUpper(app[:1]) + app[1:]},
			"user": map[string]any{"id": 1, "sub": "alice-sub", "email": "alice@example.com", "name": "Alice"}})
	}))
	t.Cleanup(fake.Close)
	for _, k := range []string{"CORE_APP_URL", "CORE_REGISTRATION_KEY", "BASE_URL", "ADMIN_EMAIL", "ADMIN_PASSWORD", "NOTIFY_URL", "OIDC_ISSUER", "DB_PATH", "DEMO_DATA"} {
		t.Setenv(k, "")
	}
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("AUTH_MODE", "local")
	t.Setenv("TZ", "Europe/Sarajevo")
	t.Setenv("CORE_URL", fake.URL)
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

func (c *apiClient) raw(method, path, contentType string, body []byte) (int, []byte) {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func (c *apiClient) importICS(text string) (int, importResult) {
	code, b := c.raw("POST", "/api/v1/import", "text/calendar", []byte(text))
	var out importResult
	_ = json.Unmarshal(b, &out)
	if code != 200 {
		c.t.Logf("import: %d %s", code, b)
	}
	return code, out
}

// eventsOf lists a person's own events as "calendar/summary".
func eventsOf(t *testing.T, s *Server, email string) []string {
	t.Helper()
	ctx := context.Background()
	u, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	own, _ := s.store.OwnCalendars(ctx, u.ID)
	out := []string{}
	for _, c := range own {
		rows, _ := s.store.ListEvents(ctx, c.ID)
		for _, r := range rows {
			out = append(out, c.Name+"/"+r.Summary)
		}
	}
	sort.Strings(out)
	return out
}

func TestPortableLinkScopes(t *testing.T) {
	s, h := moveServer(t)
	alice := apiPerson(t, s, h, "alice@example.com", "Alice")
	apiPerson(t, s, h, "bob@example.com", "Bob")
	cal := firstCal(alice)
	alice.json("POST", "/api/v1/events", map[string]any{"title": "Dentist", "start": "2026-12-01T10:00", "calendar_id": cal.ID}, 201, nil)
	exp := &apiClient{t: t, h: h, token: "pcx_export"}
	imp := &apiClient{t: t, h: h, token: "pcx_import"}

	var desc core.Portable
	exp.json("GET", core.PortablePath, nil, 200, &desc)
	if len(desc.Exports) != 1 || desc.Exports[0].Kind != core.KindICalEvents || len(desc.Imports) != 1 || desc.Imports[0].Kind != core.KindICalEvents || desc.Imports[0].ContentType != "text/calendar" || desc.Imports[0].Form != "" {
		t.Fatalf("description: %+v", desc)
	}
	if code, b := exp.call("GET", "/api/v1/export.ics", nil); code != 200 || !strings.Contains(string(b), "Dentist") || !strings.Contains(string(b), "X-WR-CALNAME:"+cal.Name) {
		t.Errorf("export.ics: %d %s", code, b)
	}
	// move:export can't bring anything in or reach anything else
	if code, _ := exp.raw("POST", "/api/v1/import", "text/calendar", []byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")); code != 403 {
		t.Errorf("export token imports: %d", code)
	}
	for _, p := range []string{"/api/v1/settings", "/api/v1/events?from=2026-01-01&to=2027-01-01", "/api/v1/calendars", "/api/v1/users", "/api/v1/backup", "/api/v1/calendars/1/export.ics"} {
		if code, _ := exp.call("GET", p, nil); code != 403 {
			t.Errorf("export token GET %s: %d", p, code)
		}
	}
	// move:import reads the description and posts the import, nothing else
	if code, _ := imp.call("GET", core.PortablePath, nil); code != 200 {
		t.Errorf("import token description: %d", code)
	}
	for _, p := range []string{"/api/v1/export.ics", "/api/v1/settings", "/api/v1/calendars"} {
		if code, _ := imp.call("GET", p, nil); code != 403 {
			t.Errorf("import token GET %s: %d", p, code)
		}
	}
	if code, _ := imp.call("POST", "/api/v1/events", map[string]any{"title": "nope", "start": "2026-12-01T10:00"}); code != 403 {
		t.Errorf("import token creates an event: %d", code)
	}
	if code, _ := imp.raw("POST", "/api/v1/calendars/1/import", "text/calendar", []byte(googleICS)); code != 403 {
		t.Errorf("import token per-calendar import: %d", code)
	}
	if code, _ := imp.call("PUT", "/api/v1/settings", map[string]any{}); code != 403 {
		t.Errorf("import token settings: %d", code)
	}
	code, out := imp.importICS(googleICS)
	if code != 200 || out.Imported != 4 {
		t.Fatalf("link import: %d %+v", code, out)
	}
	if got := eventsOf(t, s, "bob@example.com"); len(got) != 0 {
		t.Errorf("bob got: %v", got)
	}
	if got := eventsOf(t, s, "alice@example.com"); len(got) != 5 {
		t.Errorf("alice: %v", got)
	}
}

// what Google Calendar exports, more or less: a zone, a weekly series with
// an exception and a moved occurrence, an all-day event, an alarm, and one
// event without a UID.
const googleICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Google Inc//Google Calendar 70.9054//EN\r\n" +
	"X-WR-CALNAME:Work\r\nX-WR-TIMEZONE:America/New_York\r\nX-APPLE-CALENDAR-COLOR:#FF2968FF\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\nBEGIN:STANDARD\r\nDTSTART:19701101T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nDTSTART:19700308T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nUID:standup@google.com\r\nDTSTART;TZID=America/New_York:20261102T090000\r\nDTEND;TZID=America/New_York:20261102T091500\r\nRRULE:FREQ=WEEKLY;BYDAY=MO\r\nEXDATE;TZID=America/New_York:20261109T090000\r\nSUMMARY:Standup\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT10M\r\nDESCRIPTION:Standup\r\nEND:VALARM\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:standup@google.com\r\nRECURRENCE-ID;TZID=America/New_York:20261116T090000\r\nDTSTART;TZID=America/New_York:20261116T100000\r\nDTEND;TZID=America/New_York:20261116T101500\r\nSUMMARY:Standup (late)\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:holiday@google.com\r\nDTSTART;VALUE=DATE:20261126\r\nDTEND;VALUE=DATE:20261127\r\nSUMMARY:Thanksgiving\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:dinner@google.com\r\nDTSTART:20261205T180000Z\r\nDTEND:20261205T200000Z\r\nSUMMARY:Family dinner\r\nX-WR-CALNAME:Family\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nDTSTART:20261210T120000Z\r\nDTEND:20261210T130000Z\r\nSUMMARY:No uid\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestImportICalEventsTwice(t *testing.T) {
	s, h := apiServer(t)
	alice := apiPerson(t, s, h, "alice@example.com", "Alice")
	bob := apiPerson(t, s, h, "bob@example.com", "Bob")
	firstCal(alice)
	alice.json("POST", "/api/v1/calendars", map[string]any{"name": "family"}, 201, nil)

	code, out := alice.importICS(googleICS)
	if code != 200 || out.Imported != 4 || out.Skipped != 0 || out.Created != 1 {
		t.Fatalf("first: %d %+v", code, out)
	}
	want := "Work/No uid,Work/Standup,Work/Thanksgiving,family/Family dinner"
	if got := strings.Join(eventsOf(t, s, "alice@example.com"), ","); got != want {
		t.Errorf("after first:\n got %s\nwant %s", got, want)
	}
	ctx := context.Background()
	own, _ := s.store.OwnCalendars(ctx, alice.user.ID)
	var work *store.Calendar
	for _, c := range own {
		if c.Name == "Work" {
			work = c
		}
	}
	if work == nil || work.Color != "#ff2968" || work.Timezone != "America/New_York" {
		t.Fatalf("work calendar: %+v", work)
	}
	row, err := s.store.GetEventByUID(ctx, work.ID, "standup@google.com")
	if err != nil || !row.Recurring || !strings.Contains(row.ICS, "RECURRENCE-ID") || !strings.Contains(row.ICS, "EXDATE") || !strings.Contains(row.ICS, "VALARM") || !strings.Contains(row.ICS, "BEGIN:VTIMEZONE") {
		t.Fatalf("standup: %v %+v", err, row)
	}
	// the series reads right: the 9th is gone, the 16th moved to 10:00 New York
	var occ struct {
		Events []occJSON `json:"events"`
	}
	alice.json("GET", "/api/v1/events?from=2026-11-01&to=2026-11-30&tz=America/New_York", nil, 200, &occ)
	got := []string{}
	for _, o := range occ.Events {
		if strings.HasPrefix(o.Title, "Standup") || o.Title == "Thanksgiving" {
			got = append(got, o.Title+"@"+o.Start)
		}
	}
	sort.Strings(got)
	if len(got) != 4 || !strings.Contains(strings.Join(got, ","), "Standup (late)@2026-11-16T10:00") || strings.Contains(strings.Join(got, ","), "2026-11-09") || !strings.Contains(strings.Join(got, ","), "Thanksgiving@2026-11-26") {
		t.Errorf("occurrences: %v", got)
	}
	if dinner, _ := s.store.GetEventByUID(ctx, work.ID, "dinner@google.com"); dinner != nil {
		t.Errorf("dinner landed in Work")
	}
	nCals := len(own)

	// the same file again changes nothing
	code, out = alice.importICS(googleICS)
	if code != 200 || out.Imported != 0 || out.Skipped != 4 || out.Created != 0 {
		t.Fatalf("second: %d %+v", code, out)
	}
	if got := strings.Join(eventsOf(t, s, "alice@example.com"), ","); got != want {
		t.Errorf("after second: %s", got)
	}
	if own, _ := s.store.OwnCalendars(ctx, alice.user.ID); len(own) != nCals {
		t.Errorf("calendars: %d then %d", nCals, len(own))
	}
	// a multipart upload works the same way
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "work.ics")
	_, _ = fw.Write([]byte(googleICS))
	_ = mw.Close()
	if code, b := alice.raw("POST", "/api/v1/import", mw.FormDataContentType(), buf.Bytes()); code != 200 || !strings.Contains(string(b), `"skipped":4`) {
		t.Errorf("multipart: %d %s", code, b)
	}

	// bob gets his own copy; alice's stays as it is
	if code, out := bob.importICS(googleICS); code != 200 || out.Imported != 4 || out.Created != 2 {
		t.Fatalf("bob: %d %+v", code, out)
	}
	if got := strings.Join(eventsOf(t, s, "alice@example.com"), ","); got != want {
		t.Errorf("alice after bob: %s", got)
	}
	// a file with no name goes to "Moved in"
	if code, out := bob.importICS("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:x-1\r\nDTSTART:20261201T100000Z\r\nSUMMARY:Loose\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"); code != 200 || out.Imported != 1 || len(out.Calendars) != 1 || out.Calendars[0] != "Moved in" {
		t.Errorf("no name: %d %+v", code, out)
	}
	if code, _ := bob.importICS("not a calendar"); code != 400 {
		t.Errorf("garbage: %d", code)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	s, h := apiServer(t)
	alice := apiPerson(t, s, h, "alice@example.com", "Alice")
	carol := apiPerson(t, s, h, "carol@example.com", "Carol")
	home := firstCal(alice)
	var trips calJSON
	alice.json("POST", "/api/v1/calendars", map[string]any{"name": "Trips"}, 201, nil)
	var cals []calJSON
	alice.json("GET", "/api/v1/calendars", nil, 200, &cals)
	for _, c := range cals {
		if c.Name == "Trips" {
			trips = c
		}
	}
	alice.json("POST", "/api/v1/events", map[string]any{"title": "Gym", "start": "2026-12-01T07:00", "calendar_id": home.ID, "rrule": "FREQ=WEEKLY"}, 201, nil)
	alice.json("POST", "/api/v1/events", map[string]any{"title": "Rome", "start": "2026-12-10", "end": "2026-12-14", "all_day": true, "calendar_id": trips.ID}, 201, nil)
	code, b := alice.call("GET", "/api/v1/export.ics", nil)
	if code != 200 {
		t.Fatalf("export: %d", code)
	}
	// back into alice: nothing doubles
	if code, out := alice.importICS(string(b)); code != 200 || out.Imported != 0 || out.Skipped != 2 {
		t.Errorf("alice again: %d %+v", code, out)
	}
	// into carol: the same calendars
	if code, out := carol.importICS(string(b)); code != 200 || out.Imported != 2 {
		t.Fatalf("carol: %d %+v", code, out)
	}
	want := home.Name + "/Gym,Trips/Rome"
	if got := strings.Join(eventsOf(t, s, "carol@example.com"), ","); got != want {
		t.Errorf("carol: %s, want %s", got, want)
	}
	if got := strings.Join(eventsOf(t, s, "alice@example.com"), ","); got != want {
		t.Errorf("alice: %s", got)
	}
}
