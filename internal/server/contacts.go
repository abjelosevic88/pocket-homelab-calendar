package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// Pocket Contacts (contacts:read): birthdays and anniversaries as an overlay
// source ("birthdays"), and guest suggestions for the event editor. The
// source's mode (Core | Direct | Off) decides whether Contacts is reached at
// all; when it is off or unreachable the guest picker just shows nobody from
// Contacts.

// contactSummary is the part of a Contacts "Summary" the calendar reads.
type contactSummary struct {
	ID       int64    `json:"id"`
	FN       string   `json:"fn"`
	Given    string   `json:"given"`
	Family   string   `json:"family"`
	Org      string   `json:"org"`
	Emails   []string `json:"emails"`
	HasPhoto bool     `json:"has_photo"`
	PhotoURL string   `json:"photo_url"`
}

func (c contactSummary) name() string {
	return firstNonEmpty(strings.TrimSpace(c.FN), strings.TrimSpace(c.Given+" "+c.Family), c.Org, "(no name)")
}

// maxBirthdayDays is the longest stretch asked for at once. Contacts takes up
// to 366 days but lists only the NEXT occurrence of each date, and a yearly
// date can come back after 365 days (28 Feb 2027 → 29 Feb 2028 is 366, but
// 1 Jan → 1 Jan is 365), so a 366-day chunk would miss the second one.
const maxBirthdayDays = 365

// birthdayItems asks Contacts for the birthdays and anniversaries in the
// local days touched by [from, to), in chunks of at most 365 days. Contacts
// counts from the given day, so ranges across a year end and 29 February
// (shown on the 28th in other years) come out right.
func birthdayItems(ctx context.Context, sb *sibling, from, to time.Time, loc *time.Location) ([]OverlayItem, error) {
	f := from.In(loc)
	first := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, time.UTC)
	l := to.In(loc).Add(-time.Nanosecond)
	last := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
	days := int(last.Sub(first).Hours()/24) + 1
	out := []OverlayItem{}
	seen := map[string]bool{}
	for off := 0; off < days; off += maxBirthdayDays {
		n := min(maxBirthdayDays, days-off)
		var list []struct {
			Contact  contactSummary `json:"contact"`
			Kind     string         `json:"kind"`
			Date     string         `json:"date"`
			Original string         `json:"original"`
			Age      *int           `json:"age"`
		}
		q := fmt.Sprintf("/api/v1/birthdays?from=%s&days=%d", first.AddDate(0, 0, off).Format("2006-01-02"), n)
		if err := sb.call(ctx, "GET", q, nil, &list); err != nil {
			return nil, err
		}
		for _, b := range list {
			if len(b.Date) < 10 {
				continue
			}
			day := b.Date[:10]
			id := fmt.Sprintf("birthdays:%d:%s:%s", b.Contact.ID, b.Kind, day)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, birthdayItem(sb, b.Contact, b.Kind, day, b.Original, b.Age))
		}
	}
	return out, nil
}

func birthdayItem(sb *sibling, c contactSummary, kind, day, original string, age *int) OverlayItem {
	glyph, sub := "🎂", ""
	if kind == "anniversary" {
		glyph = "💍"
	} else {
		kind = "birthday"
	}
	title := glyph + " " + c.name()
	meta := map[string]any{"original": original}
	if age != nil && *age > 0 {
		title += fmt.Sprintf(" (%d)", *age)
		meta["age"] = *age
		if kind == "birthday" {
			sub = fmt.Sprintf("Turns %d", *age)
		} else {
			sub = fmt.Sprintf("%d years", *age)
		}
	}
	if strings.HasSuffix(original, "02-29") && !strings.HasSuffix(day, "02-29") {
		note := "born on 29 February"
		if kind == "anniversary" {
			note = "on 29 February"
		}
		if sub != "" {
			note = sub + " · " + note
		}
		sub = note
	}
	return OverlayItem{ID: fmt.Sprintf("birthdays:%d:%s:%s", c.ID, kind, day), Source: "birthdays", Ref: fmt.Sprint(c.ID), Kind: kind,
		Title: title, Subtitle: sub, Start: day, End: dayAfter(day), AllDay: true,
		URL: sb.public + fmt.Sprintf("/contacts/%d", c.ID), Meta: meta}
}

// ---------- the guest picker and the popover ----------

func (s *Server) contactsRoutes(r chi.Router) {
	r.Get("/contacts/suggest", s.handleContactSuggest)
	r.Get("/contacts/lookup", s.handleContactLookup)
	r.Get("/contacts/{id}/photo", s.handleContactPhoto)
}

type contactsCache struct {
	mu     sync.Mutex
	sibs   map[int64]contactsSib    // uid → how to reach Contacts, for a couple of minutes
	lookup map[string]contactLookup // uid|email → the contact (or none)
}

type contactsSib struct {
	at  time.Time
	sb  *sibling
	err error
}

type contactLookup struct {
	at  time.Time
	hit *ContactRef
}

// ContactRef is a contact the calendar points to.
type ContactRef struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	PhotoURL string `json:"photo_url"`
}

// contactsSibling reaches the person's Pocket Contacts the way the
// "birthdays" source is set up; a link token lives five minutes, so the
// answer is kept for two (a refusal for half a minute).
func (s *Server) contactsSibling(ctx context.Context, u *store.User) (*sibling, error) {
	s.contacts.mu.Lock()
	if e, ok := s.contacts.sibs[u.ID]; ok && (time.Since(e.at) < 30*time.Second || (e.err == nil && time.Since(e.at) < 2*time.Minute)) {
		s.contacts.mu.Unlock()
		return e.sb, e.err
	}
	s.contacts.mu.Unlock()
	src, _ := overlaySource("birthdays")
	st, _ := s.store.GetSettings(ctx, u.ID)
	sb, err := s.siblingFor(ctx, u, src, st)
	if errors.Is(ctx.Err(), context.Canceled) {
		return sb, err // the browser gave up; don't remember a half answer
	}
	s.contacts.mu.Lock()
	if s.contacts.sibs == nil || len(s.contacts.sibs) > 1000 {
		s.contacts.sibs = map[int64]contactsSib{}
	}
	s.contacts.sibs[u.ID] = contactsSib{at: time.Now(), sb: sb, err: err}
	s.contacts.mu.Unlock()
	return sb, err
}

// dropContactsCache forgets how a person reaches Contacts (after a settings change).
func (s *Server) dropContactsCache(uid int64) {
	prefix := fmt.Sprintf("%d|", uid)
	s.contacts.mu.Lock()
	delete(s.contacts.sibs, uid)
	for k := range s.contacts.lookup {
		if strings.HasPrefix(k, prefix) {
			delete(s.contacts.lookup, k)
		}
	}
	s.contacts.mu.Unlock()
}

func contactPhotoURL(id int64, has bool, photo string) string {
	if !has || photo == "" {
		return ""
	}
	out := fmt.Sprintf("/api/v1/contacts/%d/photo?s=64", id)
	if u, err := url.Parse(photo); err == nil && u.Query().Get("v") != "" {
		out += "&v=" + url.QueryEscape(u.Query().Get("v")) // changes with the picture, so the browser may cache
	}
	return out
}

// handleContactSuggest proxies Contacts' guest suggestions as the person:
// one row per email (rows with only a phone are no use as guests). Off or
// unreachable → [] (the editor still suggests the people on this box).
func (s *Server) handleContactSuggest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	out := []map[string]any{}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) < 2 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = 8
	}
	sb, err := s.contactsSibling(ctx, u)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var rows []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		PhotoURL string `json:"photo_url"`
	}
	if err := sb.call(cctx, "GET", fmt.Sprintf("/api/v1/suggest?q=%s&limit=%d", url.QueryEscape(q), limit*2), nil, &rows); err != nil {
		s.log.Debug("contacts suggest", "err", err)
		writeJSON(w, http.StatusOK, out)
		return
	}
	for _, c := range rows {
		if c.Email == "" {
			continue
		}
		out = append(out, map[string]any{"id": c.ID, "name": c.Name, "email": c.Email,
			"photo_url": contactPhotoURL(c.ID, c.PhotoURL != "", c.PhotoURL), "url": sb.public + fmt.Sprintf("/contacts/%d", c.ID)})
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleContactLookup maps guest emails (?email=a,b or repeated) to the
// contacts they belong to: {"a@x": ContactRef}; unknown emails are left out.
// The event popover asks after it opened, so nothing waits on it.
func (s *Server) handleContactLookup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	out := map[string]*ContactRef{}
	var emails []string
	seen := map[string]bool{}
	for _, v := range r.URL.Query()["email"] {
		for _, e := range strings.Split(v, ",") {
			e = strings.ToLower(strings.TrimSpace(e))
			if e != "" && strings.Contains(e, "@") && !seen[e] && len(emails) < 30 {
				seen[e] = true
				emails = append(emails, e)
			}
		}
	}
	if len(emails) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	sb, err := s.contactsSibling(ctx, u)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, e := range emails {
		key := fmt.Sprintf("%d|%s", u.ID, e)
		s.contacts.mu.Lock()
		hit, ok := s.contacts.lookup[key]
		s.contacts.mu.Unlock()
		if ok && time.Since(hit.at) < 5*time.Minute {
			if hit.hit != nil {
				out[e] = hit.hit
			}
			continue
		}
		wg.Add(1)
		go func(e, key string) {
			defer wg.Done()
			var res struct {
				Contacts []contactSummary `json:"contacts"`
			}
			if err := sb.call(cctx, "GET", "/api/v1/lookup?email="+url.QueryEscape(e), nil, &res); err != nil {
				return
			}
			var ref *ContactRef
			if len(res.Contacts) > 0 {
				c := res.Contacts[0]
				ref = &ContactRef{ID: c.ID, Name: c.name(), URL: sb.public + fmt.Sprintf("/contacts/%d", c.ID), PhotoURL: contactPhotoURL(c.ID, c.HasPhoto, c.PhotoURL)}
			}
			mu.Lock()
			if ref != nil {
				out[e] = ref
			}
			mu.Unlock()
			s.contacts.mu.Lock()
			if s.contacts.lookup == nil || len(s.contacts.lookup) > 5000 {
				s.contacts.lookup = map[string]contactLookup{}
			}
			s.contacts.lookup[key] = contactLookup{at: time.Now(), hit: ref}
			s.contacts.mu.Unlock()
		}(e, key)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

// handleContactPhoto passes a contact's thumbnail through as the person, so
// the browser needs no session in Contacts.
func (s *Server) handleContactPhoto(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusNotFound, "no such photo")
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("s"))
	if size < 32 || size > 512 {
		size = 64
	}
	sb, err := s.contactsSibling(ctx, u)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such photo")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", sb.base+fmt.Sprintf("/api/v1/contacts/%d/photo?s=%d", id, size), nil)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such photo")
		return
	}
	req.Header.Set("Authorization", "Bearer "+sb.token)
	req.Header.Set("User-Agent", "PocketCalendar/"+Version)
	res, err := siblingHTTP.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Pocket Contacts is not reachable")
		return
	}
	defer res.Body.Close()
	ct := res.Header.Get("Content-Type")
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "image/") {
		writeErr(w, http.StatusNotFound, "no such photo")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, io.LimitReader(res.Body, 4<<20))
}
