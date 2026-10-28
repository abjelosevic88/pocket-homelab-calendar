package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Pocket Requests' coming-up list: episodes at their air time, release days all day, each linking to the request.
func TestRequestsOverlayItems(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/overlay" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad", 400)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"date":"2026-11-02","at":"2026-11-02T02:00:00Z","kind":"series","title":"Silo","subtitle":"S03E01 · Pilot","request_id":7},{"date":"2026-11-16","at":"2026-11-16T00:00:00Z","kind":"movie","title":"Project Hail Mary","subtitle":"Out to watch at home","request_id":9}]}`))
	}))
	defer ts.Close()
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	items, err := requestsItems(context.Background(), &sibling{base: ts.URL, token: "tok", public: "https://get.example"}, from, from.AddDate(0, 0, 30))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	ep, film := items[0], items[1]
	if ep.AllDay || ep.Start != "2026-11-02T02:00:00Z" || ep.End != "2026-11-02T02:30:00Z" || ep.URL != "https://get.example/requests/7" || ep.Subtitle != "S03E01 · Pilot" {
		t.Fatalf("episode = %+v", ep)
	}
	if !film.AllDay || film.Start != "2026-11-16" || film.Title != "🎬 Project Hail Mary" {
		t.Fatalf("film = %+v", film)
	}
	if _, ok := overlaySource("requests"); !ok {
		t.Fatal("requests overlay not declared")
	}
}
