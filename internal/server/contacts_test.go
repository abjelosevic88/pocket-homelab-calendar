package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeContacts is a small Pocket Contacts: /birthdays (next occurrence on or
// after from, 29 February on the 28th in other years), /suggest, /lookup and
// a photo, behind a bearer token.
type fakeContacts struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
}

func newFakeContacts(t *testing.T) *fakeContacts {
	t.Helper()
	f := &fakeContacts{}
	type person struct {
		id                         int64
		fn                         string
		email                      string
		birthday, anniversary, pic string
	}
	people := []person{
		{1, "Marko Petrović", "marko@example.com", "1988-12-30", "", "/api/v1/contacts/1/photo?v=ab12"},
		{2, "Ana Kovač", "ana@example.com", "--01-02", "2015-01-01", ""},
		{3, "Leap Day", "", "2000-02-29", "", ""},
	}
	next := func(orig string, from time.Time) (time.Time, *int) {
		y := 0
		if !strings.HasPrefix(orig, "--") {
			y, _ = strconv.Atoi(orig[:4])
		}
		m, _ := strconv.Atoi(orig[len(orig)-5 : len(orig)-3])
		d, _ := strconv.Atoi(orig[len(orig)-2:])
		on := func(year int) time.Time {
			dd := d
			if m == 2 && d == 29 && !(year%4 == 0 && (year%100 != 0 || year%400 == 0)) {
				dd = 28
			}
			return time.Date(year, time.Month(m), dd, 0, 0, 0, 0, time.UTC)
		}
		at := on(from.Year())
		if at.Before(from) {
			at = on(from.Year() + 1)
		}
		if y == 0 {
			return at, nil
		}
		age := at.Year() - y
		return at, &age
	}
	summary := func(p person) map[string]any {
		return map[string]any{"id": p.id, "fn": p.fn, "emails": []string{p.email}, "has_photo": p.pic != "", "photo_url": p.pic}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/birthdays", func(w http.ResponseWriter, r *http.Request) {
		from, err := time.Parse("2006-01-02", r.URL.Query().Get("from"))
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if err != nil || days <= 0 || days > 366 {
			http.Error(w, `{"error":"bad range"}`, http.StatusBadRequest)
			return
		}
		end := from.AddDate(0, 0, days)
		out := []map[string]any{}
		for _, p := range people {
			for kind, v := range map[string]string{"birthday": p.birthday, "anniversary": p.anniversary} {
				if v == "" {
					continue
				}
				at, age := next(v, from)
				if !at.Before(end) {
					continue
				}
				out = append(out, map[string]any{"contact": summary(p), "kind": kind, "date": at.Format("2006-01-02"), "original": v, "age": age,
					"days": int(at.Sub(from).Hours() / 24)})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/v1/suggest", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("q"))
		out := []map[string]any{}
		for _, p := range people {
			if !strings.Contains(strings.ToLower(p.fn), q) {
				continue
			}
			if p.email != "" {
				out = append(out, map[string]any{"id": p.id, "name": p.fn, "email": p.email, "phone": "", "photo_url": p.pic})
			} else {
				out = append(out, map[string]any{"id": p.id, "name": p.fn, "email": "", "phone": "+387 65 123 456", "photo_url": ""})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/v1/lookup", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		for _, p := range people {
			if p.email != "" && strings.EqualFold(p.email, r.URL.Query().Get("email")) {
				out = append(out, summary(p))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"contacts": out})
	})
	mux.HandleFunc("/api/v1/contacts/1/photo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8jpeg-" + r.URL.Query().Get("s")))
	})
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pcon_test" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.RequestURI())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeContacts) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.requests
	f.requests = nil
	return out
}

func birthdayOverlays(c *apiClient, from, to string) map[string]OverlayItem {
	c.t.Helper()
	var res struct {
		Items  []OverlayItem     `json:"items"`
		Errors map[string]string `json:"errors"`
	}
	c.json("GET", "/api/v1/overlays?sources=birthdays&from="+from+"&to="+to, nil, 200, &res)
	if len(res.Errors) > 0 {
		c.t.Fatalf("overlay errors: %v", res.Errors)
	}
	out := map[string]OverlayItem{}
	for _, it := range res.Items {
		out[it.Start+" "+it.Kind+" "+it.Ref] = it
	}
	return out
}

func TestBirthdayOverlay(t *testing.T) {
	s, h := apiServer(t)
	me := apiPerson(t, s, h, "aleksandar@example.com", "Aleksandar")
	fc := newFakeContacts(t)

	// The source is listed like the others and on by default (through Core).
	var sources []map[string]any
	me.json("GET", "/api/v1/overlays/sources", nil, 200, &sources)
	found := false
	for _, src := range sources {
		if src["id"] == "birthdays" {
			found = true
			if src["mode"] != "core" || src["app"] != "Pocket Contacts" || src["default_color"] != "#e11d48" || src["pocket"] != true {
				t.Fatalf("birthdays source: %v", src)
			}
		}
	}
	if !found {
		t.Fatal("no birthdays source")
	}
	uses := overlayUses()
	if !func() bool {
		for _, u := range uses {
			if u.App == "contacts" && len(u.Scopes) == 1 && u.Scopes[0] == "contacts:read" {
				return true
			}
		}
		return false
	}() {
		t.Fatalf("manifest uses lack contacts: %+v", uses)
	}

	me.json("PUT", "/api/v1/overlays/sources/birthdays", map[string]any{"mode": "direct", "url": fc.URL + "/", "token": "pcon_test"}, 200, nil)

	// Across a year end: the December birthday, then the New Year anniversary
	// and a birthday without a year.
	got := birthdayOverlays(me, "2026-12-28", "2027-01-05")
	if reqs := fc.take(); len(reqs) != 1 || !strings.Contains(reqs[0], "from=2026-12-28") || !strings.Contains(reqs[0], "days=8") {
		t.Fatalf("birthdays requests: %v", reqs)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 items, got %v", got)
	}
	m := got["2026-12-30 birthday 1"]
	if m.Title != "🎂 Marko Petrović (38)" || !m.AllDay || m.End != "2026-12-31" || m.URL != fc.URL+"/contacts/1" || m.Color != "#e11d48" || m.Subtitle != "Turns 38" {
		t.Fatalf("marko: %+v", m)
	}
	a := got["2027-01-01 anniversary 2"]
	if a.Title != "💍 Ana Kovač (12)" || a.Kind != "anniversary" || a.Subtitle != "12 years" {
		t.Fatalf("anniversary: %+v", a)
	}
	b := got["2027-01-02 birthday 2"]
	if b.Title != "🎂 Ana Kovač" || b.Subtitle != "" {
		t.Fatalf("birthday without a year: %+v", b)
	}

	// 29 February: on the 28th in 2027, on the 29th in 2028.
	got = birthdayOverlays(me, "2027-02-01", "2027-03-01")
	l := got["2027-02-28 birthday 3"]
	if l.Title != "🎂 Leap Day (27)" || !strings.Contains(l.Subtitle, "29 February") {
		t.Fatalf("leap day 2027: %v", got)
	}
	got = birthdayOverlays(me, "2028-02-01", "2028-03-01")
	if l := got["2028-02-29 birthday 3"]; l.Title != "🎂 Leap Day (28)" || strings.Contains(l.Subtitle, "29 February") {
		t.Fatalf("leap day 2028: %v", got)
	}
	fc.take()

	// A range longer than a year goes in chunks of at most 365 days (Contacts
	// lists only the next occurrence, so 1 January must not share a chunk with
	// the next 1 January).
	got = birthdayOverlays(me, "2026-01-01", "2027-02-05")
	reqs := fc.take()
	if len(reqs) != 2 || !strings.Contains(reqs[0], "from=2026-01-01&days=365") || !strings.Contains(reqs[1], "from=2027-01-01&days=35") {
		t.Fatalf("chunks: %v", reqs)
	}
	for _, k := range []string{"2026-12-30 birthday 1", "2026-01-01 anniversary 2", "2027-01-01 anniversary 2", "2026-01-02 birthday 2", "2027-01-02 birthday 2", "2026-02-28 birthday 3"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing %s in %v", k, got)
		}
	}

	// Switched off: nothing asked, nothing shown.
	me.json("PUT", "/api/v1/overlays/sources/birthdays", map[string]any{"mode": "off"}, 200, nil)
	if got := birthdayOverlays(me, "2026-12-28", "2027-01-05"); len(got) != 0 {
		t.Fatalf("off but got %v", got)
	}
	if reqs := fc.take(); len(reqs) != 0 {
		t.Fatalf("off but asked Contacts: %v", reqs)
	}
}

func TestContactSuggestions(t *testing.T) {
	s, h := apiServer(t)
	me := apiPerson(t, s, h, "aleksandar@example.com", "Aleksandar")
	fc := newFakeContacts(t)
	type row struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		PhotoURL string `json:"photo_url"`
		URL      string `json:"url"`
	}
	suggest := func(q string) []row {
		var out []row
		me.json("GET", "/api/v1/contacts/suggest?q="+q, nil, 200, &out)
		return out
	}

	// Through Core, but there is no Core here: quietly nobody.
	if got := suggest("mar"); got == nil || len(got) != 0 {
		t.Fatalf("no core: %v", got)
	}
	me.json("PUT", "/api/v1/overlays/sources/birthdays", map[string]any{"mode": "off"}, 200, nil)
	if got := suggest("mar"); len(got) != 0 {
		t.Fatalf("off: %v", got)
	}
	me.json("PUT", "/api/v1/overlays/sources/birthdays", map[string]any{"mode": "direct", "url": fc.URL, "token": "pcon_test"}, 200, nil)
	if got := suggest("m"); len(got) != 0 {
		t.Fatalf("one letter: %v", got)
	}
	if reqs := fc.take(); len(reqs) != 0 {
		t.Fatalf("asked Contacts too early: %v", reqs)
	}
	got := suggest("mar")
	if len(got) != 1 || got[0].Name != "Marko Petrović" || got[0].Email != "marko@example.com" || got[0].URL != fc.URL+"/contacts/1" ||
		got[0].PhotoURL != "/api/v1/contacts/1/photo?s=64&v=ab12" {
		t.Fatalf("suggest: %+v", got)
	}
	// Someone with only a phone number is no use as a guest.
	if got := suggest("leap"); len(got) != 0 {
		t.Fatalf("phone-only row: %+v", got)
	}

	code, body := me.call("GET", got[0].PhotoURL, nil)
	if code != 200 || string(body) != "\xff\xd8jpeg-64" {
		t.Fatalf("photo: %d %q", code, body)
	}
	if code, _ := me.call("GET", "/api/v1/contacts/2/photo", nil); code != 404 {
		t.Fatalf("missing photo: %d", code)
	}

	var refs map[string]ContactRef
	me.json("GET", "/api/v1/contacts/lookup?email=Marko@Example.com,nobody@example.com&email=ana@example.com", nil, 200, &refs)
	if len(refs) != 2 || refs["marko@example.com"].URL != fc.URL+"/contacts/1" || refs["ana@example.com"].Name != "Ana Kovač" || refs["ana@example.com"].PhotoURL != "" {
		t.Fatalf("lookup: %+v", refs)
	}

	// Off again: the cached connection is forgotten at once.
	me.json("PUT", "/api/v1/overlays/sources/birthdays", map[string]any{"mode": "off"}, 200, nil)
	fc.take()
	if got := suggest("mar"); len(got) != 0 {
		t.Fatalf("off after on: %v", got)
	}
	var none map[string]ContactRef
	me.json("GET", "/api/v1/contacts/lookup?email=marko@example.com", nil, 200, &none)
	if len(none) != 0 {
		t.Fatalf("lookup while off: %v", none)
	}
	if reqs := fc.take(); len(reqs) != 0 {
		t.Fatalf("off but asked Contacts: %v", reqs)
	}
}
