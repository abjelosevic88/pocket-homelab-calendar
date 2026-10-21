package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/caldav"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// The sync engine of external accounts. Every account's calendars are
// mirrored here as calendars of kind caldav/nextcloud/google/ics. A sync
// pulls (sync-collection with the stored token, else a listing of etags,
// then calendar-multiget of what changed) and pushes (local edits with
// If-Match, local deletes). When both sides changed an event, the newer
// LAST-MODIFIED wins. ICS subscriptions are fetched whole and read-only.

var (
	accountSyncTick = time.Minute      // how often due accounts are looked for
	accountPushTick = 20 * time.Second // how often local changes are pushed
	accountPushWait = 5 * time.Minute  // no push retry for an account before this after a failure
)

var errSyncBusy = errors.New("a sync of this account is running")

type accountRuntime struct {
	dueBusy  atomic.Bool
	pushBusy atomic.Bool
	pushFail sync.Map // account id → time.Time of the last failed push
}

var accountRuntimes sync.Map // *Server → *accountRuntime

func (s *Server) acctRuntime() *accountRuntime {
	v, _ := accountRuntimes.LoadOrStore(s, &accountRuntime{})
	return v.(*accountRuntime)
}

// syncLoop syncs every enabled account on its interval, right away when
// asked through s.syncNow, and pushes local changes every ~20 s.
func (s *Server) syncLoop(ctx context.Context) {
	tick := time.NewTicker(accountSyncTick)
	defer tick.Stop()
	push := time.NewTicker(accountPushTick)
	defer push.Stop()
	first := time.NewTimer(15 * time.Second)
	defer first.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.syncNow:
			go func() { _ = s.syncAccount(ctx, id, false) }()
		case <-first.C:
			go s.syncDue(ctx)
		case <-tick.C:
			go s.syncDue(ctx)
		case <-push.C:
			go s.pushPending(ctx)
		}
	}
}

// kickSync asks the loop to sync an account soon.
func (s *Server) kickSync(id int64) {
	select {
	case s.syncNow <- id:
	default:
	}
}

func (s *Server) isSyncing(id int64) bool {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	return s.syncing[id]
}

func (s *Server) beginSync(id int64) bool {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if s.syncing[id] {
		return false
	}
	s.syncing[id] = true
	return true
}

func (s *Server) endSync(id int64) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	delete(s.syncing, id)
}

func accountDue(a *store.Account, now time.Time) bool {
	if !a.Enabled {
		return false
	}
	last := store.ParseTime(a.LastSyncAt)
	every := time.Duration(a.SyncMinutes) * time.Minute
	if every <= 0 {
		every = 15 * time.Minute
	}
	return last.IsZero() || now.Sub(last) >= every-30*time.Second
}

// syncDue syncs the accounts whose interval is up, one after another.
func (s *Server) syncDue(ctx context.Context) {
	rt := s.acctRuntime()
	if !rt.dueBusy.CompareAndSwap(false, true) {
		return
	}
	defer rt.dueBusy.Store(false)
	accs, err := s.store.ListAccounts(ctx, 0)
	if err != nil {
		return
	}
	for _, a := range accs {
		if ctx.Err() != nil {
			return
		}
		if accountDue(a, time.Now()) {
			_ = s.syncAccount(ctx, a.ID, false)
		}
	}
}

// pushPending sends local changes of account calendars without waiting for
// the next full sync.
func (s *Server) pushPending(ctx context.Context) {
	rt := s.acctRuntime()
	if !rt.pushBusy.CompareAndSwap(false, true) {
		return
	}
	defer rt.pushBusy.Store(false)
	for _, id := range s.accountsWithLocalChanges(ctx) {
		if v, ok := rt.pushFail.Load(id); ok && time.Since(v.(time.Time)) < accountPushWait {
			continue
		}
		_ = s.syncAccount(ctx, id, true)
	}
}

func (s *Server) accountsWithLocalChanges(ctx context.Context) []int64 {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT DISTINCT c.account_id FROM calendars c JOIN accounts a ON a.id = c.account_id
		WHERE a.enabled = 1 AND a.kind <> 'ics' AND c.read_only = 0 AND c.remote_href <> ''
		AND (EXISTS (SELECT 1 FROM events e WHERE e.calendar_id = c.id AND e.dirty = 1)
		  OR EXISTS (SELECT 1 FROM remote_deletes d WHERE d.calendar_id = c.id))`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// syncAccount runs one account's sync (pushOnly: just send local changes).
// It records the outcome on the account.
func (s *Server) syncAccount(ctx context.Context, id int64, pushOnly bool) error {
	if !s.beginSync(id) {
		return errSyncBusy
	}
	defer s.endSync(id)
	a, err := s.store.GetAccount(ctx, 0, id)
	if err != nil {
		return err
	}
	if pushOnly && a.Kind == "ics" {
		return nil
	}
	sctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	start := time.Now()
	var n int
	switch a.Kind {
	case "ics":
		n, err = s.syncICS(sctx, a)
	default:
		n, err = s.syncDAV(sctx, a, pushOnly)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	rt := s.acctRuntime()
	if pushOnly {
		if err != nil {
			rt.pushFail.Store(id, time.Now())
			_, _ = s.store.DB.ExecContext(ctx, `UPDATE accounts SET last_error = ? WHERE id = ?`, errText(err), id)
		} else {
			rt.pushFail.Delete(id)
		}
	} else {
		rt.pushFail.Delete(id)
		s.store.SetAccountSync(ctx, id, errText(err))
	}
	if n > 0 {
		s.touch()
		if !pushOnly {
			s.store.AddActivity(ctx, a.UserID, nil, nil, "synced", a.Name, fmt.Sprintf("%d changes", n))
		}
	}
	if err != nil {
		s.log.Warn("account sync failed", "account", a.ID, "kind", a.Kind, "name", a.Name, "push_only", pushOnly, "err", err)
	} else if n > 0 || !pushOnly {
		s.log.Debug("account synced", "account", a.ID, "kind", a.Kind, "changes", n, "push_only", pushOnly, "took", time.Since(start).Round(time.Millisecond).String())
	}
	return err
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	msg := friendlyAccountError(err)
	if len(msg) > 600 {
		msg = msg[:600] + "…"
	}
	return msg
}

// friendlyAccountError turns client errors into something a person can act on.
func friendlyAccountError(err error) string {
	switch {
	case errors.Is(err, caldav.ErrAuth):
		return "the server refused the user name or password (iCloud, Fastmail and Google-made passwords must be app-specific passwords)"
	case errors.Is(err, caldav.ErrNoCalendars):
		return "no CalDAV calendars were found at this address — check the server address"
	case errors.Is(err, errGoogleReconnect):
		return err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "the server took too long to answer"
	}
	return err.Error()
}

// ---------- CalDAV accounts (caldav, nextcloud, google) ----------

func (s *Server) userAgent() string { return "PocketCalendar/" + Version }

// davClient builds the CalDAV client for an account.
func (s *Server) davClient(ctx context.Context, a *store.Account) (*caldav.Client, error) {
	var cl *caldav.Client
	switch a.Kind {
	case "google":
		id := a.ID
		cl = caldav.NewBearer(googleAPI.CalDAV, func(ctx context.Context) (string, error) { return s.googleAccessToken(ctx, id) })
	case "nextcloud":
		if a.Via == "core" {
			if !s.core.Configured() {
				return nil, errors.New("Pocket Core is not configured on this box")
			}
			cred, err := s.core.ServiceFor(ctx, s.userEmail(ctx, a.UserID), "nextcloud")
			if err != nil {
				var le *core.LinkError
				if errors.As(err, &le) {
					return nil, fmt.Errorf("Nextcloud through Pocket Core: %s", le.Message)
				}
				return nil, fmt.Errorf("Nextcloud through Pocket Core: %w", err)
			}
			user := cred.Fields["USERNAME"]
			pass := firstNonEmpty(cred.Fields["APP_PASSWORD"], cred.Fields["PASSWORD"], cred.Key)
			if cred.URL == "" || user == "" || pass == "" {
				return nil, errors.New("Pocket Core has no Nextcloud login for you (address, username and app password)")
			}
			cl = caldav.New(nextcloudDAV(cred.URL), user, pass)
		} else {
			pass, err := s.open(a.Secret)
			if err != nil {
				return nil, err
			}
			cl = caldav.New(nextcloudDAV(a.URL), a.Username, pass)
		}
	default:
		pass, err := s.open(a.Secret)
		if err != nil {
			return nil, err
		}
		cl = caldav.New(a.URL, a.Username, pass)
	}
	cl.HTTP, cl.UA = s.http, s.userAgent()
	return cl, nil
}

// nextcloudDAV turns a Nextcloud address into its WebDAV root.
func nextcloudDAV(base string) string {
	base = strings.TrimRight(base, "/")
	for _, cut := range []string{"/remote.php/", "/index.php", "/apps/"} {
		if i := strings.Index(base, cut); i >= 0 {
			base = base[:i]
		}
	}
	return strings.TrimRight(base, "/") + "/remote.php/dav"
}

// remoteCalendars lists the event calendars of an account.
func (s *Server) remoteCalendars(ctx context.Context, a *store.Account, cl *caldav.Client) ([]caldav.Calendar, error) {
	if a.Kind == "google" {
		return s.googleCalendars(ctx, a)
	}
	home, err := cl.Home(ctx)
	if err != nil {
		return nil, err
	}
	all, err := cl.Calendars(ctx, home)
	if err != nil {
		return nil, err
	}
	var out []caldav.Calendar
	for _, c := range all {
		if !c.HoldsEvents() {
			continue
		}
		if lastSegment(c.Href) == "contact_birthdays" {
			c.ReadOnly = true // Nextcloud's birthdays come from the address book
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out, nil
}

func lastSegment(href string) string {
	h := strings.TrimSuffix(href, "/")
	return h[strings.LastIndex(h, "/")+1:]
}

// normColor turns #RRGGBB / #RRGGBBAA / RRGGBB into #rrggbb ("" when not a colour).
func normColor(c string) string {
	c = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c), "#"))
	if len(c) == 8 {
		c = c[:6]
	}
	if len(c) != 6 {
		return ""
	}
	c = "#" + strings.ToLower(c)
	if !validColor(c) {
		return ""
	}
	return c
}

// seenCalendar is what a remote calendar looked like at the last sync, so
// a rename over there carries over while a rename here sticks, and a
// calendar deleted here is not mirrored again.
type seenCalendar struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func accountMemoryKey(id int64) string { return fmt.Sprintf("account.%d.calendars", id) }

func (s *Server) accountMemory(ctx context.Context, a *store.Account) map[string]seenCalendar {
	m := map[string]seenCalendar{}
	_ = json.Unmarshal([]byte(s.store.UserSetting(ctx, a.UserID, accountMemoryKey(a.ID))), &m)
	return m
}

func (s *Server) saveAccountMemory(ctx context.Context, a *store.Account, m map[string]seenCalendar) {
	b, _ := json.Marshal(m)
	_ = s.store.SetUserSetting(ctx, a.UserID, accountMemoryKey(a.ID), string(b))
}

// mirrorCalendars makes the account's local calendars match the remote
// list: new ones appear, removed ones go, names and colours follow.
func (s *Server) mirrorCalendars(ctx context.Context, a *store.Account, remote []caldav.Calendar) (int, error) {
	locals, err := s.store.CalendarsOfAccount(ctx, a.ID)
	if err != nil {
		return 0, err
	}
	if len(remote) == 0 && len(locals) > 0 {
		return 0, errors.New("the server listed no calendars; keeping the ones here")
	}
	byHref := map[string]*store.Calendar{}
	for _, c := range locals {
		byHref[c.RemoteHref] = c
	}
	mem := s.accountMemory(ctx, a)
	firstRun := len(mem) == 0 && len(locals) == 0
	seen := map[string]bool{}
	next := map[string]seenCalendar{}
	changed := 0
	for i, rc := range remote {
		seen[rc.Href] = true
		color := normColor(rc.Color)
		prev, known := mem[rc.Href]
		next[rc.Href] = seenCalendar{Name: rc.Name, Color: color}
		lc := byHref[rc.Href]
		if lc == nil {
			if known && !firstRun {
				continue // deleted here on purpose
			}
			own, _ := s.store.OwnCalendars(ctx, a.UserID)
			nc := &store.Calendar{UserID: a.UserID, Name: firstNonEmpty(rc.Name, lastSegment(rc.Href), "Calendar"),
				Color: firstNonEmpty(color, palette[(len(own)+i)%len(palette)]), Kind: a.Kind, AccountID: &a.ID,
				RemoteHref: rc.Href, ReadOnly: rc.ReadOnly}
			if err := s.store.CreateCalendar(ctx, nc); err != nil {
				return changed, fmt.Errorf("mirror %s: %w", nc.Name, err)
			}
			changed++
			continue
		}
		name, col, ro := lc.Name, lc.Color, lc.ReadOnly
		if rc.Name != "" && rc.Name != prev.Name {
			name = rc.Name
		}
		if color != "" && color != prev.Color {
			col = color
		}
		ro = rc.ReadOnly
		if name != lc.Name || col != lc.Color || ro != lc.ReadOnly {
			if _, err := s.store.DB.ExecContext(ctx, `UPDATE calendars SET name = ?, color = ?, read_only = ?, updated_at = ? WHERE id = ?`,
				name, col, ro, store.Now(), lc.ID); err != nil {
				return changed, err
			}
			changed++
		}
	}
	for _, lc := range locals {
		if !seen[lc.RemoteHref] {
			if err := s.store.DeleteCalendar(ctx, lc.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return changed, err
			}
			s.store.AddActivity(ctx, a.UserID, nil, nil, "calendar_deleted", lc.Name, "removed at "+a.Name)
			changed++
		}
	}
	s.saveAccountMemory(ctx, a, next)
	return changed, nil
}

func (s *Server) setCalendarRemote(ctx context.Context, calID int64, ctag, token string) {
	_, _ = s.store.DB.ExecContext(ctx, `UPDATE calendars SET remote_ctag = ?, remote_token = ? WHERE id = ?`, ctag, token, calID)
}

// syncDAV syncs a CalDAV-shaped account.
func (s *Server) syncDAV(ctx context.Context, a *store.Account, pushOnly bool) (int, error) {
	cl, err := s.davClient(ctx, a)
	if err != nil {
		return 0, err
	}
	changed := 0
	remote := map[string]caldav.Calendar{}
	if !pushOnly {
		list, err := s.remoteCalendars(ctx, a, cl)
		if err != nil {
			return 0, err
		}
		n, err := s.mirrorCalendars(ctx, a, list)
		changed += n
		if err != nil {
			return changed, err
		}
		for _, rc := range list {
			remote[rc.Href] = rc
		}
	}
	locals, err := s.store.CalendarsOfAccount(ctx, a.ID)
	if err != nil {
		return changed, err
	}
	var errs []string
	for _, lc := range locals {
		if lc.RemoteHref == "" || ctx.Err() != nil {
			continue
		}
		if !pushOnly {
			rc, ok := remote[lc.RemoteHref]
			if !ok {
				continue
			}
			n, err := s.pullCalendar(ctx, cl, lc, rc)
			changed += n
			if err != nil {
				errs = append(errs, lc.Name+": "+friendlyAccountError(err))
				continue
			}
		}
		if lc.ReadOnly {
			continue
		}
		n, err := s.pushCalendar(ctx, cl, lc)
		changed += n
		if err != nil {
			errs = append(errs, lc.Name+": "+friendlyAccountError(err))
		}
	}
	if ctx.Err() != nil {
		return changed, ctx.Err()
	}
	if len(errs) > 0 {
		return changed, errors.New(strings.Join(errs, "; "))
	}
	return changed, nil
}

func (s *Server) pendingDeletes(ctx context.Context, calID int64) map[string]bool {
	out := map[string]bool{}
	if dels, err := s.store.RemoteDeletes(ctx, calID); err == nil {
		for _, d := range dels {
			out[d.RemoteHref] = true
		}
	}
	return out
}

// pullCalendar brings the remote changes of one calendar here.
func (s *Server) pullCalendar(ctx context.Context, cl *caldav.Client, lc *store.Calendar, rc caldav.Calendar) (int, error) {
	href := lc.RemoteHref
	if rc.CTag == "" && rc.SyncToken == "" {
		if info, err := cl.Collection(ctx, href); err == nil {
			rc.CTag, rc.SyncToken = info.CTag, info.SyncToken
		}
	}
	if rc.CTag != "" && rc.CTag == lc.RemoteCTag {
		return 0, nil // nothing changed over there
	}
	pending := s.pendingDeletes(ctx, lc.ID)
	var changes []caldav.Change
	token := lc.RemoteToken
	full := token == ""
	noSync := false
	if !full {
		ch, tok, err := cl.Sync(ctx, href, token)
		switch {
		case errors.Is(err, caldav.ErrInvalidToken):
			full = true
		case errors.Is(err, caldav.ErrAuth), ctx.Err() != nil:
			return 0, err
		case err != nil:
			// the server does not do sync-collection after all: list from now on
			full, noSync = true, true
		default:
			changes, token = ch, tok
		}
	}
	var locals []*store.EventRow
	if full {
		ch, err := cl.ListEvents(ctx, href)
		if err != nil {
			return 0, err
		}
		locals, err = s.store.ListEvents(ctx, lc.ID)
		if err != nil {
			return 0, err
		}
		if len(ch) == 0 && len(locals) > 0 {
			// an empty answer to calendar-query: make sure with a plain listing
			if ch, err = cl.ListETags(ctx, href); err != nil {
				return 0, err
			}
		}
		changes, token = ch, rc.SyncToken
		if noSync {
			token = ""
		}
	}
	n := 0
	listed := map[string]bool{}
	var fetch []string
	for _, ch := range changes {
		if pending[ch.Href] || strings.HasSuffix(ch.Href, "/") {
			continue
		}
		listed[ch.Href] = true
		local, lerr := s.store.GetEventByRemote(ctx, lc.ID, ch.Href)
		if ch.Deleted {
			if lerr == nil && s.remoteGone(ctx, local) {
				n++
			}
			continue
		}
		if lerr == nil && ch.ETag != "" && local.RemoteETag == ch.ETag {
			continue
		}
		fetch = append(fetch, ch.Href)
	}
	if full {
		// what the listing no longer has was deleted over there
		for _, e := range locals {
			if e.RemoteHref != "" && !listed[e.RemoteHref] && !pending[e.RemoteHref] {
				if s.remoteGone(ctx, e) {
					n++
				}
			}
		}
	}
	if len(fetch) > 0 {
		res, err := cl.Multiget(ctx, href, fetch)
		if err != nil {
			return n, err
		}
		if len(res) == 0 {
			// a server that does not multiget: one by one
			for _, h := range fetch {
				if r, err := cl.Get(ctx, h); err == nil {
					res = append(res, *r)
				}
			}
		}
		for _, r := range res {
			if s.applyRemote(ctx, lc, r) {
				n++
			}
		}
	}
	s.setCalendarRemote(ctx, lc.ID, rc.CTag, token)
	return n, nil
}

// remoteGone handles an event deleted over there; a local edit not pushed
// yet keeps it (it goes back as a new event).
func (s *Server) remoteGone(ctx context.Context, e *store.EventRow) bool {
	if e.Dirty {
		_ = s.store.MarkPushed(ctx, e.ID, -1, "", "")
		return false
	}
	if err := s.store.DeleteEvent(ctx, e, store.DeleteEventOpts{FromRemote: true}); err != nil {
		s.log.Warn("account sync: cannot delete event", "event", e.ID, "err", err)
		return false
	}
	return true
}

// lastModified is the newest LAST-MODIFIED (else DTSTAMP) of a resource.
func lastModified(ics string) time.Time {
	c, err := ical.Parse(ics)
	if err != nil {
		return time.Time{}
	}
	var out time.Time
	for _, ev := range c.ChildrenNamed("VEVENT") {
		t, _ := ical.ReadUTC(ev.Get("LAST-MODIFIED"), time.UTC)
		if t.IsZero() {
			t, _ = ical.ReadUTC(ev.Get("DTSTAMP"), time.UTC)
		}
		if t.After(out) {
			out = t
		}
	}
	return out
}

// localModified is when the local version last changed.
func localModified(e *store.EventRow) time.Time {
	t := lastModified(e.ICS)
	if u := store.ParseTime(e.UpdatedAt); u.After(t) {
		t = u
	}
	return t
}

// applyRemote stores one fetched resource; reports whether anything changed.
func (s *Server) applyRemote(ctx context.Context, lc *store.Calendar, r caldav.Resource) bool {
	parsed, err := ical.Parse(strings.TrimSpace(r.Data))
	if err != nil {
		s.log.Debug("account sync: unreadable resource", "href", r.Href, "err", err)
		return false
	}
	evs := parsed.ChildrenNamed("VEVENT")
	if len(evs) == 0 {
		return false // a task or a journal entry: not for a calendar
	}
	uid := evs[0].Value("UID")
	if uid == "" {
		return false
	}
	text := parsed.String()
	local, err := s.store.GetEventByRemote(ctx, lc.ID, r.Href)
	if err != nil {
		local = nil
		if byUID, err := s.store.GetEventByUID(ctx, lc.ID, uid); err == nil {
			if byUID.RemoteHref != "" && byUID.RemoteHref != r.Href {
				s.log.Debug("account sync: two remote resources share a UID", "uid", uid, "href", r.Href)
				return false
			}
			local = byUID
		}
	}
	if local != nil && local.Dirty {
		if !lastModified(text).After(localModified(local)) {
			return false // ours is newer; the push carries it
		}
		s.log.Info("account sync: the remote edit is newer and wins", "event", local.ID, "summary", local.Summary)
	}
	if local != nil && local.UID != uid {
		// the resource over there now holds another event
		_ = s.store.DeleteEvent(ctx, local, store.DeleteEventOpts{FromRemote: true})
		local = nil
	}
	href := ""
	if local != nil {
		href = local.Href
	}
	w := store.EventWrite{CalendarID: lc.ID, Href: href, ICS: text, Loc: s.calLoc(ctx, lc.ID), Source: "remote",
		FromRemote: true, RemoteHref: r.Href, RemoteETag: r.ETag}
	_, _, err = s.store.SaveEvent(ctx, w)
	if errors.Is(err, store.ErrConflict) && href == "" {
		w.Href = store.RandomToken(12) + ".ics"
		_, _, err = s.store.SaveEvent(ctx, w)
	}
	if err != nil {
		s.log.Warn("account sync: cannot store event", "calendar", lc.ID, "href", r.Href, "err", err)
		return false
	}
	return true
}

// resourceName is the file name a new event gets on the remote calendar.
func remoteResourceName(uid string) string {
	var b strings.Builder
	for _, r := range uid {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := b.String()
	if name == "" || len(name) > 120 {
		name = store.RandomToken(16)
	}
	return name + ".ics"
}

// pushCalendar sends the local deletes and edits of one calendar.
func (s *Server) pushCalendar(ctx context.Context, cl *caldav.Client, lc *store.Calendar) (int, error) {
	n := 0
	var firstErr error
	dels, err := s.store.RemoteDeletes(ctx, lc.ID)
	if err != nil {
		return 0, err
	}
	for _, d := range dels {
		err := cl.Delete(ctx, d.RemoteHref, d.RemoteETag)
		switch {
		case err == nil, errors.Is(err, caldav.ErrNotFound):
			_ = s.store.DoneRemoteDelete(ctx, d.ID)
		case errors.Is(err, caldav.ErrPrecondition):
			// changed over there after we last saw it: that edit is newer than
			// what was deleted here, so it comes back
			_ = s.store.DoneRemoteDelete(ctx, d.ID)
			if res, gerr := cl.Get(ctx, d.RemoteHref); gerr == nil && s.applyRemote(ctx, lc, *res) {
				n++
			}
		default:
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	dirty, err := s.store.DirtyEvents(ctx, lc.ID)
	if err != nil {
		return n, err
	}
	for _, e := range dirty {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		changed, err := s.pushEvent(ctx, cl, lc, e)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if changed {
			n++
		}
	}
	return n, firstErr
}

// pushEvent writes one local change over there; true when the local copy
// changed because the remote version won.
func (s *Server) pushEvent(ctx context.Context, cl *caldav.Client, lc *store.Calendar, e *store.EventRow) (bool, error) {
	href := e.RemoteHref
	create := href == ""
	if create {
		href = strings.TrimSuffix(lc.RemoteHref, "/") + "/" + remoteResourceName(e.UID)
	}
	etag, err := cl.Put(ctx, href, e.ICS, e.RemoteETag, create)
	if errors.Is(err, caldav.ErrNotFound) && !create {
		// gone over there meanwhile: put it back as new
		etag, err = cl.Put(ctx, href, e.ICS, "", true)
	}
	if errors.Is(err, caldav.ErrPrecondition) {
		res, gerr := cl.Get(ctx, href)
		if gerr != nil {
			return false, gerr
		}
		if c, perr := ical.Parse(res.Data); perr == nil {
			if evs := c.ChildrenNamed("VEVENT"); len(evs) > 0 && evs[0].Value("UID") != e.UID {
				// the name is taken by another event over there
				href = strings.TrimSuffix(lc.RemoteHref, "/") + "/" + store.RandomToken(16) + ".ics"
				etag, err = cl.Put(ctx, href, e.ICS, "", true)
				if err != nil {
					return false, err
				}
				return false, s.store.MarkPushed(ctx, e.ID, e.Seq, href, etag)
			}
		}
		if lastModified(res.Data).After(localModified(e)) {
			// changed on both sides: the newer edit (theirs) wins
			return s.applyRemote(ctx, lc, *res), nil
		}
		etag, err = cl.Put(ctx, href, e.ICS, res.ETag, false)
	}
	if err != nil {
		return false, err
	}
	return false, s.store.MarkPushed(ctx, e.ID, e.Seq, href, etag)
}
