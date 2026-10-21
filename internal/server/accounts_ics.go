package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// ICS subscriptions: a read-only calendar fed by any .ics / webcal address
// (Outlook and Office 365 published calendars, Google's secret iCal
// address, sports fixtures, school terms). The feed is fetched whole with
// If-None-Match / If-Modified-Since, split by UID and compared per UID.

// icsFeed is a fetched and split feed.
type icsFeed struct {
	Name  string
	Color string
	Order []string          // UIDs in feed order
	Items map[string]string // UID → one VCALENDAR with its VEVENTs and the time zones they use
	ETag  string            // of the HTTP answer
	Mod   string            // Last-Modified of the HTTP answer
}

const maxFeedBytes = 32 << 20

// fetchFeed downloads a feed; notModified when the server says 304.
func (s *Server) fetchFeed(ctx context.Context, rawURL, etag, lastMod string) (feed *icsFeed, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", s.userAgent())
	req.Header.Set("Accept", "text/calendar, application/calendar+xml;q=0.5, */*;q=0.1")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastMod != "" {
		req.Header.Set("If-Modified-Since", lastMod)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified {
		return nil, true, nil
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return nil, false, fmt.Errorf("the feed refused access (HTTP %d) — is the address still valid?", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("fetching the feed: HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxFeedBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > maxFeedBytes {
		return nil, false, errors.New("the feed is larger than 32 MB")
	}
	feed, err = splitFeed(string(b))
	if err != nil {
		return nil, false, err
	}
	feed.ETag, feed.Mod = res.Header.Get("ETag"), res.Header.Get("Last-Modified")
	return feed, false, nil
}

// splitFeed cuts a VCALENDAR into one object per UID, each with the
// VTIMEZONEs its events refer to.
func splitFeed(text string) (*icsFeed, error) {
	parsed, err := ical.Parse(text)
	if err != nil || parsed.Name != "VCALENDAR" {
		return nil, errors.New("this address does not answer with an iCalendar (.ics) feed")
	}
	f := &icsFeed{Name: strings.TrimSpace(parsed.Text("X-WR-CALNAME")), Items: map[string]string{},
		Color: normColor(firstNonEmpty(parsed.Value("X-APPLE-CALENDAR-COLOR"), parsed.Value("COLOR")))}
	byUID := map[string][]*ical.Component{}
	for _, ev := range parsed.ChildrenNamed("VEVENT") {
		uid := ev.Value("UID")
		if uid == "" {
			// no UID: one made of what the event is, so it stays the same across fetches
			sum := sha256.Sum256([]byte(ev.Value("DTSTART") + "\x00" + ev.Value("SUMMARY") + "\x00" + ev.Value("DESCRIPTION")))
			uid = "pcal-" + hex.EncodeToString(sum[:12]) + "@feed"
			ev.SetRaw("UID", uid)
		}
		if _, ok := byUID[uid]; !ok {
			f.Order = append(f.Order, uid)
		}
		byUID[uid] = append(byUID[uid], ev)
	}
	for _, uid := range f.Order {
		c := ical.NewCalendar()
		used := map[string]bool{}
		for _, ev := range byUID[uid] {
			for _, p := range ev.Props {
				if z := p.Param("TZID"); z != "" {
					used[z] = true
				}
			}
		}
		for _, tz := range parsed.ChildrenNamed("VTIMEZONE") {
			if used[tz.Value("TZID")] {
				c.Children = append(c.Children, tz)
			}
		}
		c.Children = append(c.Children, byUID[uid]...)
		f.Items[uid] = c.String()
	}
	return f, nil
}

// contentETag compares feed items without DTSTAMP (many feeds stamp every
// event with the time of the download).
func contentETag(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "DTSTAMP:") || strings.HasPrefix(line, "DTSTAMP;") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return ical.ETag(b.String())
}

// syncICS refreshes a subscription.
func (s *Server) syncICS(ctx context.Context, a *store.Account) (int, error) {
	cals, err := s.store.CalendarsOfAccount(ctx, a.ID)
	if err != nil {
		return 0, err
	}
	etag, mod := "", ""
	if len(cals) > 0 {
		etag, mod = cals[0].RemoteCTag, cals[0].RemoteToken
	}
	feed, notModified, err := s.fetchFeed(ctx, a.URL, etag, mod)
	if err != nil || notModified {
		return 0, err
	}
	return s.applyFeed(ctx, a, feed)
}

// applyFeed makes the subscription's calendar hold exactly the feed.
func (s *Server) applyFeed(ctx context.Context, a *store.Account, feed *icsFeed) (int, error) {
	cals, err := s.store.CalendarsOfAccount(ctx, a.ID)
	if err != nil {
		return 0, err
	}
	changed := 0
	var lc *store.Calendar
	if len(cals) > 0 {
		lc = cals[0]
	} else {
		own, _ := s.store.OwnCalendars(ctx, a.UserID)
		lc = &store.Calendar{UserID: a.UserID, Name: firstNonEmpty(a.Name, feed.Name, "Subscription"),
			Color: firstNonEmpty(feed.Color, palette[len(own)%len(palette)]), Kind: "ics", AccountID: &a.ID,
			RemoteHref: a.URL, ReadOnly: true}
		if err := s.store.CreateCalendar(ctx, lc); err != nil {
			return 0, err
		}
		changed++
	}
	existing := map[string]*store.EventRow{}
	rows, err := s.store.ListEvents(ctx, lc.ID)
	if err != nil {
		return changed, err
	}
	for _, e := range rows {
		existing[e.UID] = e
	}
	loc := s.calLoc(ctx, lc.ID)
	skipped := 0
	for _, uid := range feed.Order {
		text := feed.Items[uid]
		ce := contentETag(text)
		row, ok := existing[uid]
		delete(existing, uid)
		if ok && row.RemoteETag == ce {
			continue
		}
		href := ""
		if ok {
			href = row.Href
		}
		w := store.EventWrite{CalendarID: lc.ID, Href: href, ICS: text, Loc: loc, Source: "remote", FromRemote: true, RemoteETag: ce}
		_, _, err := s.store.SaveEvent(ctx, w)
		if errors.Is(err, store.ErrConflict) && href == "" {
			w.Href = store.RandomToken(12) + ".ics"
			_, _, err = s.store.SaveEvent(ctx, w)
		}
		if err != nil {
			skipped++
			continue
		}
		changed++
	}
	for _, e := range existing {
		if err := s.store.DeleteEvent(ctx, e, store.DeleteEventOpts{FromRemote: true}); err == nil {
			changed++
		}
	}
	if skipped > 0 {
		s.log.Info("subscription: some events could not be read", "account", a.ID, "skipped", skipped)
	}
	s.setCalendarRemote(ctx, lc.ID, feed.ETag, feed.Mod)
	return changed, nil
}
