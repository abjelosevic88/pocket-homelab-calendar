package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Pocket Still's practices come in as timed items with Still's colour and link.
func TestStillOverlayItems(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/overlay" || r.URL.Query().Get("from") != "2026-10-01" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad", 400)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"still-5","title":"Box breathing · 5 min","kind":"breath","start":"2026-10-03T07:00:00+02:00","end":"2026-10-03T07:05:00+02:00","all_day":false,"color":"#0ea5e9","url":"https://still.example/journal/5"}]}`))
	}))
	defer ts.Close()
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	items, err := stillItems(context.Background(), &sibling{base: ts.URL, token: "tok"}, from, from.AddDate(0, 0, 30))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "still:still-5" || items[0].Source != "still" || items[0].AllDay || items[0].Start != "2026-10-03T07:00:00+02:00" || items[0].URL != "https://still.example/journal/5" {
		t.Fatalf("items = %+v", items)
	}
	if _, ok := overlaySource("still"); !ok {
		t.Fatal("still overlay not declared")
	}
}
