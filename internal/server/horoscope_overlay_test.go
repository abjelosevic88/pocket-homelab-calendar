package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Pocket Horoscope's sky events come in timed; exact transit days come in all day, ending the next day.
func TestHoroscopeOverlayItems(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/overlay" || r.URL.Query().Get("from") != "2026-08-01" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad", 400)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"sky-eclipse-sun-1786","kind":"eclipse","title":"🌑 Total solar eclipse in Leo","start":"2026-08-12T19:46:00+02:00","end":"2026-08-12T20:16:00+02:00","all_day":false,"color":"#b45309","url":"https://horoscope.example/sky"},` +
			`{"id":"transit-saturn-square-sun-1","kind":"transit","title":"✦ Saturn square your Sun","start":"2026-08-20","end":"2026-08-20","all_day":true,"color":"#7c3aed","url":"/transits"}]}`))
	}))
	defer ts.Close()
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	items, err := horoscopeItems(context.Background(), &sibling{base: ts.URL, token: "tok", public: "https://horoscope.example"}, from, from.AddDate(0, 0, 30))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "horoscope:sky-eclipse-sun-1786" || items[0].AllDay || items[0].Source != "horoscope" {
		t.Fatalf("items = %+v", items)
	}
	if !items[1].AllDay || items[1].End != "2026-08-21" || items[1].URL != "https://horoscope.example/transits" {
		t.Fatalf("transit day = %+v", items[1])
	}
	if _, ok := overlaySource("horoscope"); !ok {
		t.Fatal("horoscope overlay not declared")
	}
}
