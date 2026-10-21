package caldav_test

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/caldav"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/caldav/caldavtest"
)

func TestDiscoveryAndListing(t *testing.T) {
	f := caldavtest.New("alice", "secret")
	defer f.Close()
	f.AddCalendar("work", "Work", "#FF0000FF", false)
	f.AddCalendar("tasks", "Tasks", "", false, "VTODO")
	f.AddCalendar("holidays", "Holidays", "", true)
	f.PutObject("work", "a.ics", caldavtest.Event("a", "Alpha", "20261021T090000Z", ""))
	f.PutObject("work", "t.ics", caldavtest.Todo("t", "Todo"))

	ctx := context.Background()
	c := caldav.New(f.URL, "alice", "secret")
	home, err := c.Home(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if home != "/calendars/alice/" {
		t.Fatalf("home = %q", home)
	}
	cals, err := c.Calendars(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(cals) != 3 {
		t.Fatalf("calendars = %+v", cals)
	}
	byName := map[string]caldav.Calendar{}
	for _, cal := range cals {
		byName[cal.Name] = cal
	}
	if !byName["Work"].HoldsEvents() || byName["Tasks"].HoldsEvents() || !byName["Holidays"].ReadOnly || byName["Work"].ReadOnly {
		t.Fatalf("calendar flags wrong: %+v", cals)
	}
	work := byName["Work"].Href

	evs, err := c.ListEvents(ctx, work)
	if err != nil || len(evs) != 1 || evs[0].Href != work+"a.ics" {
		t.Fatalf("ListEvents = %+v, %v", evs, err)
	}
	changes, token, err := c.Sync(ctx, work, "")
	if err != nil || len(changes) != 2 || token == "" {
		t.Fatalf("Sync = %+v %q %v", changes, token, err)
	}
	f.DeleteObject("work", "t.ics")
	changes, _, err = c.Sync(ctx, work, token)
	if err != nil || len(changes) != 1 || !changes[0].Deleted {
		t.Fatalf("Sync after delete = %+v %v", changes, err)
	}
	if _, _, err := c.Sync(ctx, work, "http://fake.example/sync/999999"); !errors.Is(err, caldav.ErrInvalidToken) {
		t.Fatalf("bad token: %v", err)
	}
	res, err := c.Multiget(ctx, work, []string{work + "a.ics"})
	if err != nil || len(res) != 1 || res[0].ETag == "" {
		t.Fatalf("Multiget = %+v %v", res, err)
	}
	etag, err := c.Put(ctx, work+"b c.ics", caldavtest.Event("b", "B", "20261022T090000Z", ""), "", true)
	if err != nil || etag == "" {
		t.Fatalf("Put = %q %v", etag, err)
	}
	if f.Object("work", "b c.ics") == nil {
		t.Fatal("resource with a space did not arrive")
	}
	if _, err := c.Put(ctx, work+"b c.ics", "x", `"stale"`, false); !errors.Is(err, caldav.ErrPrecondition) {
		t.Fatalf("stale put: %v", err)
	}
	if err := c.Delete(ctx, work+"b c.ics", etag); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, work+"b c.ics", ""); err != nil {
		t.Fatalf("404 delete should be fine: %v", err)
	}

	bad := caldav.New(f.URL, "alice", "wrong")
	if _, err := bad.Home(ctx); !errors.Is(err, caldav.ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestBearerAndBasePath(t *testing.T) {
	f := caldavtest.New("bob", "")
	f.Root = "/remote.php/dav"
	f.Bearer = "tok-1"
	defer f.Close()
	f.AddCalendar("personal", "Personal", "", false)
	calls := 0
	c := caldav.NewBearer(f.URL+"/remote.php/dav", func(context.Context) (string, error) { calls++; return "tok-1", nil })
	home, err := c.Home(context.Background())
	if err != nil || home != "/remote.php/dav/calendars/bob/" {
		t.Fatalf("home = %q %v", home, err)
	}
	if calls == 0 {
		t.Fatal("token func not used")
	}
	if last := f.Last("PROPFIND"); last == nil || last.Auth != "Bearer tok-1" {
		t.Fatalf("auth header = %+v", last)
	}
}

func TestURLForms(t *testing.T) {
	c := caldav.New("https://caldav.example.com/dav", "u", "p")
	for in, want := range map[string]string{
		"/calendars/u/work/":                       "https://caldav.example.com/calendars/u/work/",
		"/caldav/v2/a@b.com/events/x y.ics":        "https://caldav.example.com/caldav/v2/a@b.com/events/x%20y.ics",
		"https://p12-caldav.icloud.com:443/1/cal/": "https://p12-caldav.icloud.com:443/1/cal/",
		"/x/en#holiday@group/events/":              "https://caldav.example.com/x/en%23holiday@group/events/",
	} {
		if got := c.URL(in); got != want {
			t.Errorf("URL(%q) = %q, want %q", in, got, want)
		}
		if _, err := url.Parse(c.URL(in)); err != nil {
			t.Error(err)
		}
	}
}
