package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/config"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

func davTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("AUTH_MODE", "local")
	t.Setenv("TZ", "Europe/Sarajevo")
	t.Setenv("CORE_URL", "")
	t.Setenv("BASE_URL", "")
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
	if err := s.Bootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s, s.Router()
}

// davUser is a person on the box with an API token (their CalDAV password).
type davUser struct {
	t     *testing.T
	h     http.Handler
	u     *store.User
	token string
}

func newDavUser(t *testing.T, s *Server, h http.Handler, email, name string) *davUser {
	t.Helper()
	ctx := context.Background()
	u := &store.User{Email: email, Name: name, Role: "member"}
	if err := s.store.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	s.seedUser(ctx, u)
	tok, _, err := s.store.CreateAPIToken(ctx, u.ID, "phone")
	if err != nil {
		t.Fatal(err)
	}
	return &davUser{t: t, h: h, u: u, token: tok}
}

func (d *davUser) do(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	d.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if d.token != "" {
		req.SetBasicAuth(d.u.Username, d.token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	d.h.ServeHTTP(w, req)
	return w
}

func (d *davUser) mustStatus(w *httptest.ResponseRecorder, want int, what string) {
	d.t.Helper()
	if w.Code != want {
		d.t.Fatalf("%s: status %d, want %d; body:\n%s", what, w.Code, want, w.Body.String())
	}
}

// davResponse returns the <d:response> of a multistatus whose href is href ("" when absent).
func davResponse(body, href string) string {
	for _, part := range strings.Split(body, "<d:response>")[1:] {
		if strings.HasPrefix(part, "<d:href>"+href+"</d:href>") {
			return part
		}
	}
	return ""
}

func icsEvent(uid, summary, dtstart, extra string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:" + dtstart + "\r\nDURATION:PT1H\r\nSUMMARY:" + summary + "\r\n" + extra +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
}

const propfindAll = `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav" xmlns:cs="http://calendarserver.org/ns/" xmlns:ical="http://apple.com/ns/ical/"><d:prop>
<d:displayname/><d:resourcetype/><d:current-user-privilege-set/><cs:getctag/><d:sync-token/><ical:calendar-color/><ical:calendar-order/>
<cal:supported-calendar-component-set/><cal:calendar-home-set/><cal:calendar-user-address-set/><d:current-user-principal/><d:getetag/></d:prop></d:propfind>`

// shareCalendar makes a calendar of owner's and shares it with someone.
func shareCalendar(t *testing.T, s *Server, owner, with *davUser, name, role string) *store.Calendar {
	t.Helper()
	ctx := context.Background()
	c := &store.Calendar{UserID: owner.u.ID, Name: name, Color: "#10b981"}
	if err := s.store.CreateCalendar(ctx, c); err != nil {
		t.Fatal(err)
	}
	uid := with.u.ID
	if err := s.store.UpsertShare(ctx, &store.Share{CalendarID: c.ID, UserID: &uid, Role: role}, owner.u.ID); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDAVDiscovery(t *testing.T) {
	s, h := davTestServer(t)
	alice := newDavUser(t, s, h, "alice@example.com", "Alice")
	newDavUser(t, s, h, "bob@example.com", "Bob")
	anon := &davUser{t: t, h: h, u: alice.u}

	cases := []struct {
		name     string
		who      *davUser
		method   string
		path     string
		depth    string
		status   int
		contains []string
		header   [2]string
	}{
		{name: "options without login", who: anon, method: "OPTIONS", path: "/dav/", status: 200, header: [2]string{"DAV", "calendar-access"}},
		{name: "propfind without login", who: anon, method: "PROPFIND", path: "/dav/", status: 401, header: [2]string{"WWW-Authenticate", "Basic"}},
		{name: "root names the principal", who: alice, method: "PROPFIND", path: "/dav/", depth: "0", status: 207,
			contains: []string{"<d:current-user-principal><d:href>/dav/principals/alice/</d:href>"}},
		{name: "principal", who: alice, method: "PROPFIND", path: "/dav/principals/alice/", depth: "0", status: 207,
			contains: []string{"<cal:calendar-home-set><d:href>/dav/calendars/alice/</d:href>", "mailto:alice@example.com", "<d:displayname>Alice</d:displayname>"}},
		{name: "nextcloud-shaped principal", who: alice, method: "PROPFIND", path: "/remote.php/dav/principals/users/alice/", depth: "0", status: 207,
			contains: []string{"/remote.php/dav/calendars/alice/", "<d:href>/remote.php/dav/principals/users/alice/</d:href>"}},
		{name: "apple-derived principal path", who: alice, method: "PROPFIND", path: "/principals/alice/", depth: "0", status: 207,
			contains: []string{"/dav/calendars/alice/"}},
		{name: "someone else's principal is not there", who: alice, method: "PROPFIND", path: "/dav/principals/bob/", depth: "0", status: 404},
		{name: "someone else's home is not there", who: alice, method: "PROPFIND", path: "/dav/calendars/bob/", depth: "1", status: 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.who.t = t
			w := tc.who.do(tc.method, tc.path, propfindAll, "Depth", tc.depth)
			if tc.method == "OPTIONS" {
				w = tc.who.do(tc.method, tc.path, "")
			}
			tc.who.mustStatus(w, tc.status, tc.name)
			for _, c := range tc.contains {
				if !strings.Contains(w.Body.String(), c) {
					t.Errorf("body lacks %q:\n%s", c, w.Body.String())
				}
			}
			if tc.header[0] != "" && !strings.Contains(w.Header().Get(tc.header[0]), tc.header[1]) {
				t.Errorf("header %s = %q, want it to hold %q", tc.header[0], w.Header().Get(tc.header[0]), tc.header[1])
			}
		})
	}

	req := httptest.NewRequest("PROPFIND", "/.well-known/caldav", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/dav/" {
		t.Fatalf("well-known: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestDAVHomeListsOwnAndShared(t *testing.T) {
	s, h := davTestServer(t)
	alice := newDavUser(t, s, h, "alice@example.com", "Alice")
	bob := newDavUser(t, s, h, "bob@example.com", "Bob")
	family := shareCalendar(t, s, bob, alice, "Family", store.RoleWrite)
	work := shareCalendar(t, s, bob, alice, "Work", store.RoleRead)
	busy := shareCalendar(t, s, bob, alice, "Busy times", store.RoleFreeBusy)
	shareCalendar(t, s, alice, bob, "Alice's shared", store.RoleRead) // alice's own, shared out

	w := alice.do("PROPFIND", "/dav/calendars/alice/", propfindAll, "Depth", "1")
	alice.mustStatus(w, 207, "propfind home")
	body := w.Body.String()

	cases := []struct {
		href     string
		name     string
		writable bool
	}{
		{"/dav/calendars/alice/personal/", "Personal", true},
		{"/dav/calendars/alice/alices-shared/", "Alice&#39;s shared", true},
		{"/dav/calendars/alice/shared-" + strconv.FormatInt(family.ID, 10) + "/", "Family (Bob)", true},
		{"/dav/calendars/alice/shared-" + strconv.FormatInt(work.ID, 10) + "/", "Work (Bob)", false},
		{"/dav/calendars/alice/shared-" + strconv.FormatInt(busy.ID, 10) + "/", "Busy times (Bob)", false},
		{"/dav/calendars/alice/invitations/", "Invitations", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := davResponse(body, tc.href)
			if resp == "" {
				t.Fatalf("home lacks %s:\n%s", tc.href, body)
			}
			if !strings.Contains(resp, "<d:displayname>"+tc.name+"</d:displayname>") {
				t.Errorf("displayname of %s:\n%s", tc.href, resp)
			}
			if !strings.Contains(resp, "<cal:calendar/>") || !strings.Contains(resp, `<cal:comp name="VEVENT"/>`) {
				t.Errorf("%s is not a VEVENT calendar:\n%s", tc.href, resp)
			}
			if got := strings.Contains(resp, "<d:write-content/>"); got != tc.writable {
				t.Errorf("%s writable = %v, want %v:\n%s", tc.href, got, tc.writable, resp)
			}
			if !strings.Contains(resp, "<d:read/>") {
				t.Errorf("%s not readable", tc.href)
			}
		})
	}
	if strings.Contains(body, "/dav/calendars/bob/") {
		t.Errorf("alice's home shows bob's paths:\n%s", body)
	}
}

func TestDAVPutGetConditional(t *testing.T) {
	s, h := davTestServer(t)
	alice := newDavUser(t, s, h, "alice@example.com", "Alice")
	const p = "/dav/calendars/alice/personal/ev1.ics"

	var etag string
	steps := []struct {
		name    string
		method  string
		body    string
		headers func() []string
		status  int
		check   func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{name: "create", method: "PUT", body: icsEvent("ev1", "Dentist", "20261103T090000Z", ""),
			headers: func() []string { return []string{"If-None-Match", "*"} }, status: 201,
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				etag = w.Header().Get("ETag")
				if etag == "" {
					t.Fatal("no ETag on create")
				}
			}},
		{name: "create again", method: "PUT", body: icsEvent("ev1", "Dentist", "20261103T090000Z", ""),
			headers: func() []string { return []string{"If-None-Match", "*"} }, status: 412},
		{name: "stale If-Match", method: "PUT", body: icsEvent("ev1", "Dentist 2", "20261103T090000Z", ""),
			headers: func() []string { return []string{"If-Match", `"deadbeef"`} }, status: 412},
		{name: "update with If-Match", method: "PUT", body: icsEvent("ev1", "Dentist moved", "20261104T090000Z", ""),
			headers: func() []string { return []string{"If-Match", etag} }, status: 204,
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				ne := w.Header().Get("ETag")
				if ne == "" || ne == etag {
					t.Fatalf("ETag after update %q (was %q)", ne, etag)
				}
				etag = ne
			}},
		{name: "get", method: "GET", headers: func() []string { return nil }, status: 200,
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				if w.Header().Get("ETag") != etag {
					t.Errorf("GET ETag %q, want %q", w.Header().Get("ETag"), etag)
				}
				if !strings.Contains(w.Body.String(), "SUMMARY:Dentist moved") {
					t.Errorf("GET body:\n%s", w.Body.String())
				}
			}},
		{name: "two UIDs in one object", method: "PUT",
			body: strings.Replace(icsEvent("ev2", "A", "20261103T090000Z", ""), "END:VCALENDAR",
				"BEGIN:VEVENT\r\nUID:other\r\nDTSTART:20261105T090000Z\r\nSUMMARY:B\r\nEND:VEVENT\r\nEND:VCALENDAR", 1),
			headers: func() []string { return nil }, status: 400},
		{name: "a task is not an event", method: "PUT",
			body:    "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\nUID:t1\r\nSUMMARY:Task\r\nEND:VTODO\r\nEND:VCALENDAR\r\n",
			headers: func() []string { return nil }, status: 403},
		{name: "same UID under another name", method: "PUT", body: icsEvent("ev1", "Dup", "20261103T090000Z", ""),
			headers: func() []string { return nil }, status: 409},
		{name: "master and override of one UID", method: "PUT",
			body: strings.Replace(icsEvent("series", "Standup", "20261102T080000Z", "RRULE:FREQ=DAILY;COUNT=5\r\n"), "END:VCALENDAR",
				"BEGIN:VEVENT\r\nUID:series\r\nRECURRENCE-ID:20261103T080000Z\r\nDTSTART:20261103T090000Z\r\nDURATION:PT1H\r\nSUMMARY:Standup late\r\nEND:VEVENT\r\nEND:VCALENDAR", 1),
			headers: func() []string { return nil }, status: 201},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			alice.t = t
			path := p
			if st.name == "two UIDs in one object" || st.name == "a task is not an event" || st.name == "same UID under another name" {
				path = "/dav/calendars/alice/personal/other.ics"
			}
			if st.name == "master and override of one UID" {
				path = "/dav/calendars/alice/personal/series.ics"
			}
			w := alice.do(st.method, path, st.body, st.headers()...)
			alice.mustStatus(w, st.status, st.name)
			if st.check != nil {
				st.check(t, w)
			}
		})
	}

	// the write went through the store like any other: index, source, activity
	cal, err := s.store.GetCalendarByUID(context.Background(), alice.u.ID, "personal")
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.store.GetEventByHref(context.Background(), cal.ID, "ev1.ics")
	if err != nil {
		t.Fatal(err)
	}
	if row.Summary != "Dentist moved" || row.Source != "caldav" || row.UpdatedBy != alice.u.ID {
		t.Errorf("stored row: %+v", row)
	}
	acts, _ := s.store.ListActivity(context.Background(), alice.u.ID, 10)
	if len(acts) == 0 {
		t.Error("no activity logged for CalDAV writes")
	}
}

func TestDAVCalendarQueryTimeRange(t *testing.T) {
	s, h := davTestServer(t)
	alice := newDavUser(t, s, h, "alice@example.com", "Alice")
	base := "/dav/calendars/alice/personal/"
	alice.mustStatus(alice.do("PUT", base+"weekly.ics", icsEvent("weekly", "Gym", "20260105T170000Z", "RRULE:FREQ=WEEKLY\r\n")), 201, "put weekly")
	alice.mustStatus(alice.do("PUT", base+"once.ics", icsEvent("once", "Party", "20260110T200000Z", "")), 201, "put once")

	query := func(start, end string) string {
		return `<?xml version="1.0"?><cal:calendar-query xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/></d:prop>
<cal:filter><cal:comp-filter name="VCALENDAR"><cal:comp-filter name="VEVENT"><cal:time-range start="` + start + `" end="` + end + `"/></cal:comp-filter></cal:comp-filter></cal:filter></cal:calendar-query>`
	}
	cases := []struct {
		name       string
		start, end string
		want, not  []string
	}{
		{"a March week holds the weekly series", "20260302T000000Z", "20260309T000000Z", []string{"weekly.ics"}, []string{"once.ics"}},
		{"the party's evening", "20260110T000000Z", "20260111T000000Z", []string{"once.ics"}, []string{"weekly.ics"}},
		{"before anything", "20251201T000000Z", "20251208T000000Z", nil, []string{"weekly.ics", "once.ics"}},
		{"first week holds both", "20260105T000000Z", "20260112T000000Z", []string{"weekly.ics", "once.ics"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alice.t = t
			w := alice.do("REPORT", base, query(tc.start, tc.end), "Depth", "1")
			alice.mustStatus(w, 207, "calendar-query")
			for _, h := range tc.want {
				if !strings.Contains(w.Body.String(), base+h) {
					t.Errorf("want %s in:\n%s", h, w.Body.String())
				}
			}
			for _, h := range tc.not {
				if strings.Contains(w.Body.String(), base+h) {
					t.Errorf("did not want %s in:\n%s", h, w.Body.String())
				}
			}
		})
	}

	// multiget hands back the data of what was asked for
	mg := `<?xml version="1.0"?><cal:calendar-multiget xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/><cal:calendar-data/></d:prop>
<d:href>` + base + `once.ics</d:href><d:href>` + base + `missing.ics</d:href></cal:calendar-multiget>`
	w := alice.do("REPORT", base, mg, "Depth", "1")
	alice.mustStatus(w, 207, "multiget")
	if r := davResponse(w.Body.String(), base+"once.ics"); !strings.Contains(r, "SUMMARY:Party") {
		t.Errorf("multiget once.ics:\n%s", w.Body.String())
	}
	if r := davResponse(w.Body.String(), base+"missing.ics"); !strings.Contains(r, "404 Not Found") {
		t.Errorf("multiget missing.ics:\n%s", w.Body.String())
	}
}

func TestDAVSyncCollection(t *testing.T) {
	s, h := davTestServer(t)
	alice := newDavUser(t, s, h, "alice@example.com", "Alice")
	base := "/dav/calendars/alice/personal/"
	alice.mustStatus(alice.do("PUT", base+"a.ics", icsEvent("a", "A", "20261103T090000Z", "")), 201, "put a")
	alice.mustStatus(alice.do("PUT", base+"b.ics", icsEvent("b", "B", "20261104T090000Z", "")), 201, "put b")

	sync := func(token string) *httptest.ResponseRecorder {
		return alice.do("REPORT", base, `<?xml version="1.0"?><d:sync-collection xmlns:d="DAV:"><d:sync-token>`+token+
			`</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`)
	}
	tokenOf := func(body string) string {
		i := strings.LastIndex(body, "<d:sync-token>")
		j := strings.LastIndex(body, "</d:sync-token>")
		if i < 0 || j < i {
			t.Fatalf("no sync-token in:\n%s", body)
		}
		return body[i+len("<d:sync-token>") : j]
	}

	w := sync("")
	alice.mustStatus(w, 207, "first sync")
	first := w.Body.String()
	if !strings.Contains(first, base+"a.ics") || !strings.Contains(first, base+"b.ics") {
		t.Fatalf("first sync:\n%s", first)
	}
	tok := tokenOf(first)
	if !strings.HasPrefix(tok, syncPfx) {
		t.Fatalf("token %q", tok)
	}

	alice.mustStatus(alice.do("DELETE", base+"b.ics", ""), 204, "delete b")
	alice.mustStatus(alice.do("PUT", base+"c.ics", icsEvent("c", "C", "20261105T090000Z", "")), 201, "put c")

	w = sync(tok)
	alice.mustStatus(w, 207, "second sync")
	second := w.Body.String()
	cases := []struct {
		href   string
		listed bool
		gone   bool
	}{
		{base + "a.ics", false, false},
		{base + "b.ics", true, true},
		{base + "c.ics", true, false},
	}
	for _, tc := range cases {
		r := davResponse(second, tc.href)
		if (r != "") != tc.listed {
			t.Errorf("%s listed = %v, want %v:\n%s", tc.href, r != "", tc.listed, second)
		}
		if tc.listed && strings.Contains(r, "<d:status>HTTP/1.1 404 Not Found</d:status></d:response>") != tc.gone {
			t.Errorf("%s gone = %v:\n%s", tc.href, !tc.gone, r)
		}
	}
	if tokenOf(second) == tok {
		t.Error("the token did not move on")
	}

	w = sync(tokenOf(second))
	alice.mustStatus(w, 207, "nothing new")
	if strings.Contains(w.Body.String(), "<d:response>") {
		t.Errorf("nothing changed but:\n%s", w.Body.String())
	}
	alice.mustStatus(sync("http://elsewhere/sync/1"), 403, "foreign token")
}

func TestDAVSharedWriteRules(t *testing.T) {
	s, h := davTestServer(t)
	alice := newDavUser(t, s, h, "alice@example.com", "Alice")
	bob := newDavUser(t, s, h, "bob@example.com", "Bob")
	family := shareCalendar(t, s, bob, alice, "Family", store.RoleWrite)
	work := shareCalendar(t, s, bob, alice, "Work", store.RoleRead)
	sub := &store.Calendar{UserID: alice.u.ID, Name: "Holidays", Kind: "ics", ReadOnly: true}
	if err := s.store.CreateCalendar(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	// something to try deleting in the read-only share
	if _, _, err := s.store.SaveEvent(context.Background(), store.EventWrite{CalendarID: work.ID, ICS: icsEvent("w1", "Review", "20261103T090000Z", ""), Source: "web", Actor: bob.u.ID}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"write share takes a PUT", "PUT", "/dav/calendars/alice/shared-" + itoa64(family.ID) + "/f1.ics", 201},
		{"read share refuses a PUT", "PUT", "/dav/calendars/alice/shared-" + itoa64(work.ID) + "/x.ics", 403},
		{"read share refuses a DELETE", "DELETE", "/dav/calendars/alice/shared-" + itoa64(work.ID) + "/w1.ics", 403},
		{"read-only subscription refuses a PUT", "PUT", "/dav/calendars/alice/" + sub.UID + "/x.ics", 403},
		{"invitations refuse a PUT", "PUT", "/dav/calendars/alice/invitations/x.ics", 403},
		{"no such calendar", "PUT", "/dav/calendars/alice/nope/x.ics", 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alice.t = t
			body := ""
			if tc.method == "PUT" {
				body = icsEvent("u-"+strconv.Itoa(len(tc.name)), "Try", "20261103T090000Z", "")
			}
			alice.mustStatus(alice.do(tc.method, tc.path, body), tc.status, tc.name)
		})
	}

	// the event alice wrote lives in bob's calendar, written by her
	row, err := s.store.GetEventByHref(context.Background(), family.ID, "f1.ics")
	if err != nil {
		t.Fatal(err)
	}
	if row.CreatedBy != alice.u.ID {
		t.Errorf("created_by %d, want %d", row.CreatedBy, alice.u.ID)
	}
	// bob sees it in his own calendar over CalDAV
	w := bob.do("GET", "/dav/calendars/bob/"+family.UID+"/f1.ics", "")
	bob.mustStatus(w, 200, "bob reads alice's event")
}

func TestDAVMasksPrivateAndFreeBusy(t *testing.T) {
	s, h := davTestServer(t)
	alice := newDavUser(t, s, h, "alice@example.com", "Alice")
	bob := newDavUser(t, s, h, "bob@example.com", "Bob")
	work := shareCalendar(t, s, bob, alice, "Work", store.RoleRead)
	fb := shareCalendar(t, s, bob, alice, "Private life", store.RoleFreeBusy)
	ctx := context.Background()
	put := func(calID int64, uid, extra string) {
		if _, _, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: calID, ICS: icsEvent(uid, "Secret meeting", "20261103T090000Z",
			"DESCRIPTION:the secret agenda\r\nLOCATION:Room 7\r\n"+extra), Source: "web", Actor: bob.u.ID}); err != nil {
			t.Fatal(err)
		}
	}
	put(work.ID, "priv", "CLASS:PRIVATE\r\n")
	put(work.ID, "pub", "")
	put(fb.ID, "fbpub", "")

	cases := []struct {
		name   string
		path   string
		masked bool
	}{
		{"private event in a read share", "/dav/calendars/alice/shared-" + itoa64(work.ID) + "/priv.ics", true},
		{"public event in a read share", "/dav/calendars/alice/shared-" + itoa64(work.ID) + "/pub.ics", false},
		{"any event in a free/busy share", "/dav/calendars/alice/shared-" + itoa64(fb.ID) + "/fbpub.ics", true},
		{"the owner sees everything", "/dav/calendars/bob/" + work.UID + "/priv.ics", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			who := alice
			if strings.HasPrefix(tc.path, "/dav/calendars/bob/") {
				who = bob
			}
			who.t = t
			w := who.do("GET", tc.path, "")
			who.mustStatus(w, 200, tc.name)
			body := w.Body.String()
			leaked := strings.Contains(body, "secret") || strings.Contains(body, "Room 7")
			if tc.masked && (leaked || !strings.Contains(body, "SUMMARY:Busy") || !strings.Contains(body, "DTSTART:20261103T090000Z")) {
				t.Errorf("not masked:\n%s", body)
			}
			if !tc.masked && !strings.Contains(body, "SUMMARY:Secret meeting") {
				t.Errorf("masked but should not be:\n%s", body)
			}
			// the calendar-data in a REPORT says the same as GET
			q := `<?xml version="1.0"?><cal:calendar-query xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav"><d:prop><cal:calendar-data/></d:prop></cal:calendar-query>`
			dir := tc.path[:strings.LastIndex(tc.path, "/")+1]
			rw := who.do("REPORT", dir, q, "Depth", "1")
			who.mustStatus(rw, 207, "query")
			r := davResponse(rw.Body.String(), tc.path)
			if tc.masked && strings.Contains(r, "secret") {
				t.Errorf("REPORT leaks:\n%s", r)
			}
		})
	}
}

func TestDAVMkCalendarAndProppatch(t *testing.T) {
	s, h := davTestServer(t)
	alice := newDavUser(t, s, h, "alice@example.com", "Alice")
	mk := `<?xml version="1.0"?><cal:mkcalendar xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav" xmlns:ical="http://apple.com/ns/ical/"><d:set><d:prop>
<d:displayname>Trips</d:displayname><ical:calendar-color>#FF0000FF</ical:calendar-color><cal:calendar-description>Away</cal:calendar-description>
<cal:calendar-timezone-id>Europe/Berlin</cal:calendar-timezone-id>
<cal:supported-calendar-component-set><cal:comp name="VEVENT"/></cal:supported-calendar-component-set></d:prop></d:set></cal:mkcalendar>`
	todoOnly := `<?xml version="1.0"?><cal:mkcalendar xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav"><d:set><d:prop>
<cal:supported-calendar-component-set><cal:comp name="VTODO"/></cal:supported-calendar-component-set></d:prop></d:set></cal:mkcalendar>`

	cases := []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{"create", "/dav/calendars/alice/trips-uuid/", mk, 201},
		{"create again", "/dav/calendars/alice/trips-uuid/", mk, 405},
		{"reserved name", "/dav/calendars/alice/invitations/", mk, 405},
		{"reserved shared name", "/dav/calendars/alice/shared-99/", mk, 403},
		{"tasks only", "/dav/calendars/alice/todo/", todoOnly, 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alice.t = t
			alice.mustStatus(alice.do("MKCALENDAR", tc.path, tc.body), tc.status, tc.name)
		})
	}
	alice.t = t

	c, err := s.store.GetCalendarByUID(context.Background(), alice.u.ID, "trips-uuid")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Trips" || c.Color != "#ff0000" || c.Description != "Away" || c.Timezone != "Europe/Berlin" {
		t.Errorf("created calendar: %+v", c)
	}
	w := alice.do("PROPFIND", "/dav/calendars/alice/trips-uuid/", `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav" xmlns:ical="http://apple.com/ns/ical/"><d:prop><d:displayname/><ical:calendar-color/><cal:calendar-timezone/></d:prop></d:propfind>`, "Depth", "0")
	alice.mustStatus(w, 207, "propfind new calendar")
	for _, want := range []string{"<d:displayname>Trips</d:displayname>", "<ical:calendar-color>#FF0000FF</ical:calendar-color>", "TZID:Europe/Berlin", "BEGIN:DAYLIGHT"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("want %q in:\n%s", want, w.Body.String())
		}
	}

	pp := `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:" xmlns:ical="http://apple.com/ns/ical/" xmlns:x="urn:x"><d:set><d:prop>
<d:displayname>Travel</d:displayname><ical:calendar-color>#00FF00FF</ical:calendar-color><ical:calendar-order>7</ical:calendar-order><x:unknown>1</x:unknown></d:prop></d:set></d:propertyupdate>`
	w = alice.do("PROPPATCH", "/dav/calendars/alice/trips-uuid/", pp)
	alice.mustStatus(w, 207, "proppatch")
	if !strings.Contains(w.Body.String(), "403 Forbidden") {
		t.Errorf("unknown property was taken:\n%s", w.Body.String())
	}
	w = alice.do("PROPFIND", "/dav/calendars/alice/trips-uuid/", propfindAll, "Depth", "0")
	for _, want := range []string{"<d:displayname>Travel</d:displayname>", "<ical:calendar-color>#00FF00FF</ical:calendar-color>", "<ical:calendar-order>7</ical:calendar-order>"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("want %q in:\n%s", want, w.Body.String())
		}
	}

	alice.mustStatus(alice.do("DELETE", "/dav/calendars/alice/trips-uuid/", ""), 204, "delete calendar")
	alice.mustStatus(alice.do("DELETE", "/dav/calendars/alice/personal/", ""), 403, "delete the last calendar")
}

func TestDAVInvitations(t *testing.T) {
	s, h := davTestServer(t)
	alice := newDavUser(t, s, h, "alice@example.com", "Alice")
	bob := newDavUser(t, s, h, "bob@example.com", "Bob")
	bob.mustStatus(bob.do("PUT", "/dav/calendars/bob/personal/lunch.ics", icsEvent("lunch", "Lunch", "20261103T120000Z",
		"ORGANIZER:mailto:bob@example.com\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:alice@example.com\r\n")), 201, "bob invites alice")
	row, err := s.store.GetEventByHref(context.Background(), mustCal(t, s, bob.u.ID, "personal").ID, "lunch.ics")
	if err != nil {
		t.Fatal(err)
	}
	href := "/dav/calendars/alice/invitations/" + itoa64(row.ID) + ".ics"

	w := alice.do("PROPFIND", "/dav/calendars/alice/invitations/", propfindAll, "Depth", "1")
	alice.mustStatus(w, 207, "propfind invitations")
	if davResponse(w.Body.String(), href) == "" {
		t.Fatalf("invitation not listed:\n%s", w.Body.String())
	}
	w = alice.do("GET", href, "")
	alice.mustStatus(w, 200, "get invitation")
	if !strings.Contains(w.Body.String(), "SUMMARY:Lunch") {
		t.Errorf("invitation body:\n%s", w.Body.String())
	}
	alice.mustStatus(alice.do("PUT", href, icsEvent("lunch", "Lunch", "20261103T120000Z", "")), 403, "write into invitations")
	alice.mustStatus(alice.do("DELETE", href, ""), 403, "delete from invitations")
}

func mustCal(t *testing.T, s *Server, userID int64, uid string) *store.Calendar {
	t.Helper()
	c, err := s.store.GetCalendarByUID(context.Background(), userID, uid)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
