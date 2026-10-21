package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/caldav/caldavtest"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/config"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// ---------- harness ----------

type accEnv struct {
	t     *testing.T
	s     *Server
	h     http.Handler
	st    *store.Store
	ann   *store.User
	bob   *store.User
	annTk string
	bobTk string
}

func newAccEnv(t *testing.T) *accEnv {
	t.Helper()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(dir, "cal.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{DataDir: dir, AuthMode: "local", Timezone: "UTC", SessionTTL: time.Hour}
	s := New(cfg, st, log, nil)
	if err := s.loadSecretKey(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e := &accEnv{t: t, s: s, h: s.Router(), st: st}
	for _, p := range []**store.User{&e.ann, &e.bob} {
		name := "ann"
		if p == &e.bob {
			name = "bob"
		}
		u := &store.User{Email: name + "@example.com", Name: name, Role: "admin"}
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		s.seedUser(ctx, u)
		tok, _, err := st.CreateAPIToken(ctx, u.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
		*p = u
		if name == "ann" {
			e.annTk = tok
		} else {
			e.bobTk = tok
		}
	}
	return e
}

func (e *accEnv) call(tok, method, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (e *accEnv) mustCall(tok, method, path string, body any, want int, out any) {
	e.t.Helper()
	code, b := e.call(tok, method, path, body)
	if code != want {
		e.t.Fatalf("%s %s: HTTP %d (want %d): %s", method, path, code, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

func (e *accEnv) sync(id int64, pushOnly bool) {
	e.t.Helper()
	if err := e.s.syncAccount(context.Background(), id, pushOnly); err != nil {
		e.t.Fatalf("sync: %v", err)
	}
}

func (e *accEnv) calendarByName(acc int64, name string) *store.Calendar {
	e.t.Helper()
	cals, err := e.st.CalendarsOfAccount(context.Background(), acc)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, c := range cals {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (e *accEnv) events(calID int64) map[string]*store.EventRow {
	e.t.Helper()
	rows, err := e.st.ListEvents(context.Background(), calID)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]*store.EventRow{}
	for _, r := range rows {
		out[r.UID] = r
	}
	return out
}

type accJSON struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Calendars int    `json:"calendars"`
	LastError string `json:"last_error"`
	HasSecret bool   `json:"has_secret"`
	List      []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		ReadOnly bool   `json:"read_only"`
	} `json:"calendar_list"`
}

func objectByUID(f *caldavtest.Server, cal, uid string) (string, *caldavtest.Object) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for name, o := range f.Calendars[cal].Objects {
		if strings.Contains(o.Data, "UID:"+uid+"\r\n") || strings.Contains(o.Data, "UID:"+uid+"\n") {
			return name, o
		}
	}
	return "", nil
}

// ---------- CalDAV ----------

func TestAccountCalDAVTwoWay(t *testing.T) {
	e := newAccEnv(t)
	ctx := context.Background()
	f := caldavtest.New("alice", "app-pass-123")
	defer f.Close()
	f.AddCalendar("work", "Work", "#FF8800FF", false, "VEVENT", "VTODO")
	f.AddCalendar("todo", "Todo", "", false, "VTODO")
	f.AddCalendar("holidays", "Holidays", "", true)
	f.PutObject("work", "a.ics", caldavtest.Event("a@x", "Alpha", "20261021T090000Z", "20261001T000000Z"))
	f.PutObject("work", "t.ics", caldavtest.Todo("t@x", "A task"))
	f.PutObject("holidays", "h.ics", caldavtest.Event("h@x", "Holiday", "20261225T000000Z", ""))

	// test without saving
	var probe struct {
		OK        bool `json:"ok"`
		Calendars []struct {
			Name string `json:"name"`
		} `json:"calendars"`
	}
	e.mustCall(e.annTk, "POST", "/api/v1/accounts/test", map[string]any{"kind": "caldav", "url": f.URL, "username": "alice", "password": "app-pass-123"}, 200, &probe)
	if !probe.OK || len(probe.Calendars) != 2 {
		t.Fatalf("probe = %+v", probe)
	}
	e.mustCall(e.annTk, "POST", "/api/v1/accounts/test", map[string]any{"kind": "caldav", "url": f.URL, "username": "alice", "password": "nope"}, 200, &probe)
	if probe.OK {
		t.Fatal("a wrong password passed the test")
	}
	if code, _ := e.call(e.annTk, "POST", "/api/v1/accounts", map[string]any{"kind": "caldav", "url": f.URL, "username": "alice", "password": "nope"}); code != 400 {
		t.Fatalf("wrong password saved: %d", code)
	}

	var acc accJSON
	e.mustCall(e.annTk, "POST", "/api/v1/accounts", map[string]any{"kind": "caldav", "url": f.URL, "username": "alice", "password": "app-pass-123"}, 201, &acc)
	if acc.Calendars != 2 || acc.Kind != "caldav" || !acc.HasSecret {
		t.Fatalf("account = %+v", acc)
	}
	if code, _ := e.call(e.annTk, "POST", "/api/v1/accounts", map[string]any{"kind": "caldav", "url": f.URL, "username": "alice", "password": "app-pass-123"}); code != 409 {
		t.Fatalf("duplicate account: %d", code)
	}
	work, hol := e.calendarByName(acc.ID, "Work"), e.calendarByName(acc.ID, "Holidays")
	if work == nil || hol == nil || work.Kind != "caldav" || work.Color != "#ff8800" || work.ReadOnly || !hol.ReadOnly {
		t.Fatalf("mirrored calendars wrong: %+v %+v", work, hol)
	}

	// pull
	e.sync(acc.ID, false)
	evs := e.events(work.ID)
	if len(evs) != 1 || evs["a@x"] == nil || evs["a@x"].Summary != "Alpha" || evs["a@x"].Dirty || evs["a@x"].RemoteHref != "/calendars/alice/work/a.ics" {
		t.Fatalf("pulled events = %+v", evs)
	}
	if len(e.events(hol.ID)) != 1 {
		t.Fatal("read-only calendar not pulled")
	}

	// read-only calendars refuse local events
	if code, _ := e.call(e.annTk, "POST", "/api/v1/events", map[string]any{"calendar_id": hol.ID, "title": "x", "start": "2026-10-23T10:00", "end": "2026-10-23T11:00"}); code != 403 {
		t.Fatalf("event in a read-only calendar: %d", code)
	}

	// local edit → PUT with If-Match
	alpha := evs["a@x"]
	oldETag := f.Object("work", "a.ics").ETag
	e.mustCall(e.annTk, "PUT", fmt.Sprintf("/api/v1/events/%d", alpha.ID), map[string]any{"title": "Alpha edited"}, 200, nil)
	if row, _ := e.st.GetEvent(ctx, alpha.ID); !row.Dirty {
		t.Fatal("a local edit of an account calendar is not dirty")
	}
	if got := e.s.accountsWithLocalChanges(ctx); len(got) != 1 || got[0] != acc.ID {
		t.Fatalf("accounts with local changes = %v", got)
	}
	e.s.pushPending(ctx)
	put := f.Last("PUT")
	if put == nil || put.IfMatch != oldETag || put.Path != "/calendars/alice/work/a.ics" {
		t.Fatalf("push = %+v (want If-Match %s)", put, oldETag)
	}
	if o := f.Object("work", "a.ics"); !strings.Contains(o.Data, "Alpha edited") {
		t.Fatalf("remote not updated: %s", o.Data)
	}
	if row, _ := e.st.GetEvent(ctx, alpha.ID); row.Dirty || row.RemoteETag != f.Object("work", "a.ics").ETag {
		t.Fatalf("after push: dirty=%v etag=%s", row.Dirty, row.RemoteETag)
	}

	// local create → PUT If-None-Match into the calendar
	var occ struct {
		EventID int64  `json:"event_id"`
		UID     string `json:"uid"`
	}
	e.mustCall(e.annTk, "POST", "/api/v1/events", map[string]any{"calendar_id": work.ID, "title": "Brand new", "start": "2026-10-24T10:00", "end": "2026-10-24T11:00"}, 201, &occ)
	e.sync(acc.ID, true)
	name, obj := objectByUID(f, "work", occ.UID)
	if obj == nil || !strings.Contains(obj.Data, "Brand new") {
		t.Fatal("new local event did not reach the server")
	}
	if put := f.Last("PUT"); put.IfNoneMatch != "*" {
		t.Fatalf("create without If-None-Match: %+v", put)
	}
	if row, _ := e.st.GetEvent(ctx, occ.EventID); row.Dirty || row.RemoteHref != "/calendars/alice/work/"+name {
		t.Fatalf("new event after push: %+v", row)
	}

	// remote edit and remote delete → pulled (incrementally)
	syncs := f.Count("REPORT", "sync-collection")
	f.PutObject("work", name, caldavtest.Event(occ.UID, "Renamed over there", "20261024T100000Z", "20300101T000000Z"))
	f.DeleteObject("work", "a.ics")
	e.sync(acc.ID, false)
	if f.Count("REPORT", "sync-collection") == syncs {
		t.Fatal("the second pull did not use sync-collection")
	}
	evs = e.events(work.ID)
	if evs["a@x"] != nil {
		t.Fatal("remote delete not applied")
	}
	if evs[occ.UID] == nil || evs[occ.UID].Summary != "Renamed over there" {
		t.Fatalf("remote edit not applied: %+v", evs[occ.UID])
	}

	// conflict: both sides changed, the remote LAST-MODIFIED is newer → remote wins
	e.mustCall(e.annTk, "PUT", fmt.Sprintf("/api/v1/events/%d", occ.EventID), map[string]any{"title": "Local change"}, 200, nil)
	f.PutObject("work", name, caldavtest.Event(occ.UID, "Remote change (newer)", "20261024T100000Z", "20310101T000000Z"))
	e.sync(acc.ID, true)
	if row, _ := e.st.GetEvent(ctx, occ.EventID); row.Summary != "Remote change (newer)" || row.Dirty {
		t.Fatalf("newer remote edit lost: %+v", row)
	}
	// … and the other way: the remote edit is older → ours is written with the fresh etag
	e.mustCall(e.annTk, "PUT", fmt.Sprintf("/api/v1/events/%d", occ.EventID), map[string]any{"title": "Local wins"}, 200, nil)
	f.PutObject("work", name, caldavtest.Event(occ.UID, "Old remote change", "20261024T100000Z", "20200101T000000Z"))
	e.sync(acc.ID, false)
	if _, o := objectByUID(f, "work", occ.UID); !strings.Contains(o.Data, "Local wins") {
		t.Fatalf("newer local edit lost: %s", o.Data)
	}
	if row, _ := e.st.GetEvent(ctx, occ.EventID); row.Summary != "Local wins" || row.Dirty {
		t.Fatalf("local after conflict: %+v", row)
	}

	// local delete → DELETE with If-Match
	remoteETag := f.Object("work", name).ETag
	e.mustCall(e.annTk, "DELETE", fmt.Sprintf("/api/v1/events/%d", occ.EventID), nil, 200, nil)
	e.sync(acc.ID, true)
	if f.Object("work", name) != nil {
		t.Fatal("local delete did not reach the server")
	}
	if del := f.Last("DELETE"); del == nil || del.IfMatch != remoteETag {
		t.Fatalf("delete = %+v", del)
	}
	if left, _ := e.st.RemoteDeletes(ctx, work.ID); len(left) != 0 {
		t.Fatal("remote delete still queued")
	}

	// calendars added and removed over there
	f.AddCalendar("family", "Family", "#00AA00", false)
	f.RemoveCalendar("holidays")
	e.sync(acc.ID, false)
	if e.calendarByName(acc.ID, "Family") == nil || e.calendarByName(acc.ID, "Holidays") != nil {
		t.Fatal("calendar list not followed")
	}
	// a mirrored calendar deleted here stays gone…
	fam := e.calendarByName(acc.ID, "Family")
	if err := e.st.DeleteCalendar(ctx, fam.ID); err != nil {
		t.Fatal(err)
	}
	e.sync(acc.ID, false)
	if e.calendarByName(acc.ID, "Family") != nil {
		t.Fatal("a calendar deleted here came back")
	}
	// … until asked for
	e.mustCall(e.annTk, "PUT", fmt.Sprintf("/api/v1/accounts/%d", acc.ID), map[string]any{"restore_calendars": true}, 200, nil)
	e.sync(acc.ID, false)
	if e.calendarByName(acc.ID, "Family") == nil {
		t.Fatal("restore_calendars did not bring it back")
	}

	// sync now through the API
	var after accJSON
	e.mustCall(e.annTk, "POST", fmt.Sprintf("/api/v1/accounts/%d/sync?wait=1", acc.ID), nil, 200, &after)
	if after.LastError != "" {
		t.Fatalf("sync error: %s", after.LastError)
	}

	// deleting the account removes its calendars here, nothing over there
	e.mustCall(e.annTk, "DELETE", fmt.Sprintf("/api/v1/accounts/%d", acc.ID), nil, 204, nil)
	if cals, _ := e.st.CalendarsOfAccount(ctx, acc.ID); len(cals) != 0 {
		t.Fatal("mirrored calendars left behind")
	}
	if f.Object("work", "t.ics") == nil {
		t.Fatal("something was deleted on the server")
	}
}

func TestAccountNextcloudAndFullListing(t *testing.T) {
	e := newAccEnv(t)
	f := caldavtest.New("ann", "nc-app-pw")
	f.Root = "/remote.php/dav"
	f.NoSync = true
	defer f.Close()
	f.AddCalendar("personal", "Personal", "", false)
	f.AddCalendar("contact_birthdays", "Contact birthdays", "", false)
	f.PutObject("personal", "p1.ics", caldavtest.Event("p1", "One", "20261021T090000Z", ""))
	f.PutObject("personal", "p2.ics", caldavtest.Event("p2", "Two", "20261022T090000Z", ""))

	var acc accJSON
	e.mustCall(e.annTk, "POST", "/api/v1/accounts", map[string]any{"kind": "nextcloud", "url": f.URL + "/index.php/apps/calendar/", "username": "ann", "password": "nc-app-pw"}, 201, &acc)
	if acc.Name != "Nextcloud" || acc.Calendars != 2 {
		t.Fatalf("account = %+v", acc)
	}
	bday := e.calendarByName(acc.ID, "Contact birthdays")
	if bday == nil || !bday.ReadOnly || bday.Kind != "nextcloud" {
		t.Fatalf("birthdays calendar = %+v", bday)
	}
	e.sync(acc.ID, false)
	pers := e.calendarByName(acc.ID, "Personal")
	if len(e.events(pers.ID)) != 2 {
		t.Fatal("events not pulled without sync-collection")
	}
	f.DeleteObject("personal", "p2.ics")
	e.sync(acc.ID, false)
	if evs := e.events(pers.ID); len(evs) != 1 || evs["p1"] == nil {
		t.Fatalf("listing-based delete: %+v", evs)
	}
}

func TestAccountSecretsAndOwnership(t *testing.T) {
	e := newAccEnv(t)
	ctx := context.Background()
	f := caldavtest.New("ann", "sup3r-secret-pw-XYZ")
	defer f.Close()
	f.AddCalendar("cal", "Cal", "", false)

	var acc accJSON
	e.mustCall(e.annTk, "POST", "/api/v1/accounts", map[string]any{"kind": "caldav", "url": f.URL, "username": "ann", "password": "sup3r-secret-pw-XYZ"}, 201, &acc)

	// the password is sealed in the database and never in the API
	rows, err := e.st.DB.QueryContext(ctx, `SELECT id, user_id, kind, name, url, username, secret, oauth, via FROM accounts`)
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for rows.Next() {
		var id, uid int64
		var a, b, c, d, sec, o, v string
		if err := rows.Scan(&id, &uid, &a, &b, &c, &d, &sec, &o, &v); err != nil {
			t.Fatal(err)
		}
		dump.WriteString(strings.Join([]string{a, b, c, d, sec, o, v}, "|"))
		if !strings.HasPrefix(sec, sealPrefix) {
			t.Fatalf("secret not sealed: %q", sec)
		}
		if plain, err := e.s.open(sec); err != nil || plain != "sup3r-secret-pw-XYZ" {
			t.Fatalf("open = %q %v", plain, err)
		}
	}
	rows.Close()
	if strings.Contains(dump.String(), "sup3r-secret-pw") {
		t.Fatal("the raw password is in the accounts table")
	}
	_, list := e.call(e.annTk, "GET", "/api/v1/accounts", nil)
	if strings.Contains(string(list), "sup3r") || strings.Contains(string(list), sealPrefix) {
		t.Fatalf("secret in the API: %s", list)
	}
	if fi, err := os.Stat(filepath.Join(e.s.cfg.DataDir, "secret.key")); err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != 32 {
		t.Fatalf("secret.key: %v %v", fi, err)
	}
	// the same key is read back on the next start
	s2 := New(e.s.cfg, e.st, e.s.log, nil)
	if err := s2.loadSecretKey(); err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.GetAccount(ctx, 0, acc.ID)
	if plain, err := s2.open(a.Secret); err != nil || plain != "sup3r-secret-pw-XYZ" {
		t.Fatalf("reopen after restart: %q %v", plain, err)
	}

	// strictly per person
	var bobs []accJSON
	e.mustCall(e.bobTk, "GET", "/api/v1/accounts", nil, 200, &bobs)
	if len(bobs) != 0 {
		t.Fatalf("bob sees %+v", bobs)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/accounts/%d"}, {"PUT", "/api/v1/accounts/%d"}, {"DELETE", "/api/v1/accounts/%d"}, {"POST", "/api/v1/accounts/%d/sync"},
	} {
		if code, b := e.call(e.bobTk, c.method, fmt.Sprintf(c.path, acc.ID), map[string]any{"name": "mine"}); code != 404 {
			t.Fatalf("bob %s %s: %d %s", c.method, c.path, code, b)
		}
	}
	var cals []map[string]any
	e.mustCall(e.bobTk, "GET", "/api/v1/calendars", nil, 200, &cals)
	for _, c := range cals {
		if c["account_id"] != nil {
			t.Fatalf("bob sees ann's mirrored calendar: %v", c)
		}
	}

	// a password change is tested before it is stored
	if code, _ := e.call(e.annTk, "PUT", fmt.Sprintf("/api/v1/accounts/%d", acc.ID), map[string]any{"password": "wrong"}); code != 400 {
		t.Fatalf("wrong new password accepted: %d", code)
	}
	var upd accJSON
	e.mustCall(e.annTk, "PUT", fmt.Sprintf("/api/v1/accounts/%d", acc.ID), map[string]any{"name": "My server", "sync_minutes": 1}, 200, &upd)
	if upd.Name != "My server" {
		t.Fatalf("update = %+v", upd)
	}
	if a, _ := e.st.GetAccount(ctx, 0, acc.ID); a.SyncMinutes != 5 {
		t.Fatalf("sync interval not clamped: %d", a.SyncMinutes)
	}
}

func TestNormalizeAccountURL(t *testing.T) {
	for in, want := range map[string]string{
		"webcal://example.com/a.ics":   "https://example.com/a.ics",
		"WEBCALS://example.com/a.ics":  "https://example.com/a.ics",
		"caldav.icloud.com":            "https://caldav.icloud.com",
		"http://radicale.lan:5232/u/":  "http://radicale.lan:5232/u/",
		"https://x.example/cal#frag":   "https://x.example/cal",
		"ftp://example.com/a.ics":      "",
		"javascript:alert(1)":          "",
		"https://user:pw@example.com/": "",
	} {
		got, err := normalizeAccountURL(in)
		if want == "" {
			if err == nil {
				t.Errorf("%q accepted as %q", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("normalizeAccountURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// ---------- ICS ----------

func TestAccountICSSubscription(t *testing.T) {
	e := newAccEnv(t)
	ctx := context.Background()
	var mu sync.Mutex
	stamp := "20261001T000000Z"
	items := map[string]string{
		"u1": "SUMMARY:Match one\r\nDTSTART;TZID=Europe/Sarajevo:20261101T200000\r\nDTEND;TZID=Europe/Sarajevo:20261101T220000",
		"u2": "SUMMARY:Match two\r\nDTSTART:20261108T190000Z\r\nDURATION:PT2H",
		"u4": "SUMMARY:Stable\r\nDTSTART;VALUE=DATE:20261201",
	}
	etag := `"v1"`
	var hits, notMod int
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		if r.Header.Get("If-None-Match") == etag {
			notMod++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		var b strings.Builder
		b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//feed//EN\r\nX-WR-CALNAME:Football club\r\n")
		b.WriteString("BEGIN:VTIMEZONE\r\nTZID:Europe/Sarajevo\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n")
		b.WriteString("BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\nBEGIN:STANDARD\r\nDTSTART:19701101T020000\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n")
		for _, uid := range []string{"u1", "u2", "u3", "u4"} {
			if body, ok := items[uid]; ok {
				b.WriteString("BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:" + stamp + "\r\n" + body + "\r\nEND:VEVENT\r\n")
			}
		}
		b.WriteString("END:VCALENDAR\r\n")
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = io.WriteString(w, b.String())
	}))
	defer feed.Close()

	var acc accJSON
	e.mustCall(e.annTk, "POST", "/api/v1/accounts", map[string]any{"kind": "ics", "url": feed.URL + "/club.ics"}, 201, &acc)
	if acc.Name != "Football club" || acc.Calendars != 1 || !acc.List[0].ReadOnly {
		t.Fatalf("subscription = %+v", acc)
	}
	a, _ := e.st.GetAccount(ctx, 0, acc.ID)
	if a.SyncMinutes != 60 {
		t.Fatalf("ics interval = %d", a.SyncMinutes)
	}
	cal := e.calendarByName(acc.ID, "Football club")
	evs := e.events(cal.ID)
	if len(evs) != 3 || cal.Kind != "ics" {
		t.Fatalf("events = %d kind %s", len(evs), cal.Kind)
	}
	if !strings.Contains(evs["u1"].ICS, "BEGIN:VTIMEZONE") || strings.Contains(evs["u1"].ICS, "America/New_York") || strings.Contains(evs["u2"].ICS, "VTIMEZONE") {
		t.Fatal("time zones not carried per event")
	}
	stableSeq := evs["u4"].Seq

	// unchanged feed: 304
	e.sync(acc.ID, false)
	if notMod != 1 {
		t.Fatalf("no conditional request (304s: %d)", notMod)
	}

	// changed feed: u1 edited, u2 gone, u3 new, DTSTAMP moves on everything
	mu.Lock()
	etag = `"v2"`
	stamp = "20261021T120000Z"
	items["u1"] = strings.Replace(items["u1"], "Match one", "Match one (moved)", 1)
	delete(items, "u2")
	items["u3"] = "SUMMARY:Match three\r\nDTSTART:20261115T190000Z"
	mu.Unlock()
	e.sync(acc.ID, false)
	evs = e.events(cal.ID)
	if len(evs) != 3 || evs["u2"] != nil || evs["u3"] == nil || evs["u1"].Summary != "Match one (moved)" {
		t.Fatalf("after change: %+v", evs)
	}
	if evs["u4"].Seq != stableSeq {
		t.Fatal("an unchanged event (only DTSTAMP moved) was rewritten")
	}
	if code, _ := e.call(e.annTk, "POST", "/api/v1/events", map[string]any{"calendar_id": cal.ID, "title": "x", "start": "2026-10-23T10:00", "end": "2026-10-23T11:00"}); code != 403 {
		t.Fatalf("event in a subscription: %d", code)
	}

	// not a feed
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "<html>nope</html>") }))
	defer bad.Close()
	if code, b := e.call(e.annTk, "POST", "/api/v1/accounts", map[string]any{"kind": "ics", "url": bad.URL}); code != 400 || !strings.Contains(string(b), "iCalendar") {
		t.Fatalf("html as feed: %d %s", code, b)
	}
}

// ---------- Google ----------

type fakeGoogle struct {
	*httptest.Server
	mu        sync.Mutex
	access    string
	refreshes int
	invalid   bool
	codes     []url.Values
}

func newFakeGoogle(t *testing.T, email string, calIDs map[string]string) *fakeGoogle {
	g := &fakeGoogle{access: "at-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g.mu.Lock()
		defer g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("client_id") != "cid" || r.Form.Get("client_secret") != "csec" {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			g.codes = append(g.codes, r.Form)
			claims, _ := json.Marshal(map[string]any{"email": email, "sub": "123"})
			idt := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
			_, _ = fmt.Fprintf(w, `{"access_token":"%s","expires_in":3600,"refresh_token":"rt-1","id_token":"%s","token_type":"Bearer"}`, g.access, idt)
		case "refresh_token":
			if g.invalid || r.Form.Get("refresh_token") != "rt-1" {
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
				return
			}
			g.refreshes++
			g.access = fmt.Sprintf("at-%d", g.refreshes+1)
			_, _ = fmt.Fprintf(w, `{"access_token":"%s","expires_in":3599,"token_type":"Bearer"}`, g.access)
		}
	})
	mux.HandleFunc("/calendarList", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		ok := r.Header.Get("Authorization") == "Bearer "+g.access
		g.mu.Unlock()
		if !ok {
			w.WriteHeader(401)
			return
		}
		var items []string
		for id, role := range calIDs {
			items = append(items, fmt.Sprintf(`{"id":%q,"summary":%q,"backgroundColor":"#9fe1e7","accessRole":%q,"primary":%v}`, id, "Cal "+id, role, id == email))
		}
		items = append(items, `{"id":"busy@x","summary":"Busy only","accessRole":"freeBusyReader"}`)
		_, _ = io.WriteString(w, `{"items":[`+strings.Join(items, ",")+`]}`)
	})
	g.Server = httptest.NewServer(mux)
	return g
}

func TestAccountGoogle(t *testing.T) {
	e := newAccEnv(t)
	ctx := context.Background()

	// not configured: a helpful 400
	code, b := e.call(e.annTk, "GET", "/api/v1/accounts/google/start", nil)
	if code != 400 || !strings.Contains(string(b), "GOOGLE_CLIENT_ID") || !strings.Contains(string(b), "/api/v1/accounts/google/callback") || !strings.Contains(string(b), "CalDAV API") {
		t.Fatalf("unconfigured start: %d %s", code, b)
	}

	const email = "ann.g@gmail.com"
	const holiday = "en.usa#holiday@group.v.calendar.google.com"
	g := newFakeGoogle(t, email, map[string]string{email: "owner", holiday: "reader"})
	defer g.Close()
	dav := caldavtest.New("google", "")
	dav.Bearer = "at-1"
	defer dav.Close()
	dav.AddCalendar(email, "", "", false)
	dav.AddCalendar(holiday, "", "", false)
	dav.PutObject(email, "g1.ics", caldavtest.Event("g1", "Dentist", "20261030T080000Z", ""))
	dav.PutObject(holiday, "x.ics", caldavtest.Event("hol1", "Thanksgiving", "20261126T000000Z", ""))

	saved, savedHref := googleAPI, googleEventsHref
	t.Cleanup(func() { googleAPI, googleEventsHref = saved, savedHref })
	googleAPI.AuthURL = g.URL + "/auth"
	googleAPI.TokenURL = g.URL + "/token"
	googleAPI.CalendarListURL = g.URL + "/calendarList"
	googleAPI.UserInfoURL = g.URL + "/userinfo"
	googleAPI.CalDAV = dav.URL
	googleEventsHref = dav.CalendarHref
	e.s.cfg.GoogleClientID, e.s.cfg.GoogleClientSecret = "cid", "csec"
	e.s.cfg.BaseURL = "https://cal.example.com"

	// start: redirect to Google with offline access and a bound state cookie
	req := httptest.NewRequest("GET", "/api/v1/accounts/google/start", nil)
	req.Header.Set("Authorization", "Bearer "+e.annTk)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 302 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), g.URL+"/auth?") || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" ||
		q.Get("client_id") != "cid" || q.Get("redirect_uri") != "https://cal.example.com/api/v1/accounts/google/callback" ||
		!strings.Contains(q.Get("scope"), "https://www.googleapis.com/auth/calendar") || !strings.Contains(q.Get("scope"), "email") || q.Get("state") == "" {
		t.Fatalf("auth URL = %s", loc)
	}
	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == googleCookie {
			stateCookie = c
		}
	}
	if stateCookie == nil || !stateCookie.HttpOnly {
		t.Fatal("no state cookie")
	}

	session, err := e.st.CreateSession(ctx, e.ann.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	callback := func(state, sessionID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/accounts/google/callback?code=the-code&state="+url.QueryEscape(state), nil)
		req.AddCookie(stateCookie)
		if sessionID != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionID})
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	// a forged state or someone else's browser session is refused
	if rec := callback("forged", session); !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Fatalf("forged state accepted: %s", rec.Header().Get("Location"))
	}
	bobSession, _ := e.st.CreateSession(ctx, e.bob.ID, time.Hour)
	if rec := callback(q.Get("state"), bobSession); !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Fatalf("other person's session accepted: %s", rec.Header().Get("Location"))
	}
	rec = callback(q.Get("state"), session)
	if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "https://cal.example.com/settings?tab=accounts&connected=") {
		t.Fatalf("callback: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if len(g.codes) != 1 || g.codes[0].Get("code") != "the-code" || g.codes[0].Get("redirect_uri") != "https://cal.example.com/api/v1/accounts/google/callback" {
		t.Fatalf("code exchange = %+v", g.codes)
	}
	accs, _ := e.st.ListAccounts(ctx, e.ann.ID)
	if len(accs) != 1 || accs[0].Kind != "google" || accs[0].Name != email || accs[0].Username != email {
		t.Fatalf("accounts = %+v", accs)
	}
	a := accs[0]
	if strings.Contains(a.OAuth, "rt-1") || strings.Contains(a.OAuth, "at-1") || !strings.HasPrefix(a.OAuth, sealPrefix) {
		t.Fatal("OAuth tokens not sealed")
	}
	if bobs, _ := e.st.ListAccounts(ctx, e.bob.ID); len(bobs) != 0 {
		t.Fatal("the account landed at the wrong person")
	}

	// sync: calendars from the Calendar API, events over CalDAV with Bearer
	e.sync(a.ID, false)
	primary, hol := e.calendarByName(a.ID, "Cal "+email), e.calendarByName(a.ID, "Cal "+holiday)
	if primary == nil || hol == nil || primary.ReadOnly || !hol.ReadOnly || primary.Kind != "google" || primary.Color != "#9fe1e7" {
		t.Fatalf("google calendars: %+v %+v", primary, hol)
	}
	if e.calendarByName(a.ID, "Busy only") != nil {
		t.Fatal("free/busy-only calendar mirrored")
	}
	if evs := e.events(primary.ID); evs["g1"] == nil || evs["g1"].Summary != "Dentist" {
		t.Fatalf("google events: %+v", evs)
	}
	if len(e.events(hol.ID)) != 1 {
		t.Fatal("holiday calendar (id with # and @) not pulled")
	}

	// an expired token is refreshed (and the new one stored) before use
	expire := func() {
		acc, _ := e.st.GetAccount(ctx, 0, a.ID)
		raw, _ := e.s.open(acc.OAuth)
		var tk googleToken
		_ = json.Unmarshal([]byte(raw), &tk)
		tk.Expiry = time.Now().Add(-time.Minute)
		bb, _ := json.Marshal(tk)
		sealed, _ := e.s.seal(string(bb))
		_, _ = e.st.DB.Exec(`UPDATE accounts SET oauth = ? WHERE id = ?`, sealed, a.ID)
	}
	expire()
	dav.Mu.Lock()
	dav.Bearer = "at-2"
	dav.Mu.Unlock()
	tok, err := e.s.googleAccessToken(ctx, a.ID)
	if err != nil || tok != "at-2" || g.refreshes != 1 {
		t.Fatalf("refresh = %q %v (refreshes %d)", tok, err, g.refreshes)
	}
	if tok, _ := e.s.googleAccessToken(ctx, a.ID); tok != "at-2" || g.refreshes != 1 {
		t.Fatal("a fresh token was refreshed again")
	}
	// local edit pushed with the bearer token
	g1 := e.events(primary.ID)["g1"]
	e.mustCall(e.annTk, "PUT", fmt.Sprintf("/api/v1/events/%d", g1.ID), map[string]any{"title": "Dentist (moved)"}, 200, nil)
	e.sync(a.ID, true)
	if put := dav.Last("PUT"); put == nil || put.Auth != "Bearer at-2" || !strings.Contains(dav.Object(email, "g1.ics").Data, "Dentist (moved)") {
		t.Fatalf("google push: %+v", put)
	}

	// a revoked grant asks to reconnect
	expire()
	g.mu.Lock()
	g.invalid = true
	g.mu.Unlock()
	if err := e.s.syncAccount(ctx, a.ID, false); err == nil {
		t.Fatal("sync with a revoked grant passed")
	}
	if acc, _ := e.st.GetAccount(ctx, 0, a.ID); !strings.Contains(acc.LastError, "connect it again") {
		t.Fatalf("last error = %q", acc.LastError)
	}
	// reconnect through ?account=
	code, b = e.call(e.annTk, "GET", fmt.Sprintf("/api/v1/accounts/google/start?account=%d&json=1", a.ID), nil)
	if code != 200 || !strings.Contains(string(b), "login_hint=ann.g") {
		t.Fatalf("reconnect start: %d %s", code, b)
	}
	if code, _ := e.call(e.bobTk, "GET", fmt.Sprintf("/api/v1/accounts/google/start?account=%d", a.ID), nil); code != 404 {
		t.Fatalf("bob reconnecting ann's account: %d", code)
	}
}
