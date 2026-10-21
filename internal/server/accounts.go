package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/caldav"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// External calendar accounts (CalDAV, Google, ICS, Nextcloud through Core):
// the API. The sync engine is in accounts_sync.go, subscriptions in
// accounts_ics.go, Google in accounts_google.go, secrets in accounts_secret.go.
// Accounts are strictly per person: other people's ids answer 404.

func (s *Server) accountRoutes(r chi.Router) {
	r.Get("/accounts", s.handleListAccounts)
	r.Post("/accounts", s.handleCreateAccount)
	r.Post("/accounts/test", s.handleTestAccount)
	r.Get("/accounts/google/start", s.handleGoogleStart)
	r.Get("/accounts/{id}", s.handleGetAccount)
	r.Put("/accounts/{id}", s.handleUpdateAccount)
	r.Delete("/accounts/{id}", s.handleDeleteAccount)
	r.Post("/accounts/{id}/sync", s.handleSyncAccount)
}

// publicAccountRoutes are reached without the auth middleware: Google's
// redirect back (the person is read from the session cookie there).
func (s *Server) publicAccountRoutes(r chi.Router) {
	r.Get("/accounts/google/callback", s.handleGoogleCallback)
}

var accountKinds = map[string]bool{"caldav": true, "nextcloud": true, "google": true, "ics": true}

type accountIn struct {
	Kind             *string `json:"kind"`
	Name             *string `json:"name"`
	URL              *string `json:"url"`
	Username         *string `json:"username"`
	Password         *string `json:"password"`
	Via              *string `json:"via"`
	SyncMinutes      *int    `json:"sync_minutes"`
	Enabled          *bool   `json:"enabled"`
	RestoreCalendars bool    `json:"restore_calendars"` // mirror calendars deleted here again
}

// accountView is the JSON of an account (never its secrets).
func (s *Server) accountView(ctx context.Context, a *store.Account) map[string]any {
	cals, _ := s.store.CalendarsOfAccount(ctx, a.ID)
	list := []map[string]any{}
	for _, c := range cals {
		list = append(list, map[string]any{"id": c.ID, "name": c.Name, "color": c.Color, "read_only": c.ReadOnly})
	}
	return map[string]any{
		"id": a.ID, "kind": a.Kind, "name": a.Name, "url": a.URL, "username": a.Username, "via": a.Via,
		"sync_minutes": a.SyncMinutes, "enabled": a.Enabled, "last_sync_at": a.LastSyncAt, "last_error": a.LastError,
		"has_secret": a.HasSecret, "calendars": len(cals), "calendar_list": list, "syncing": s.isSyncing(a.ID),
		"created_at": a.CreatedAt, "updated_at": a.UpdatedAt,
	}
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accs, err := s.store.ListAccounts(ctx, me(r))
	if err != nil {
		s.fail(w, err, "list accounts")
		return
	}
	out := []map[string]any{}
	for _, a := range accs {
		out = append(out, s.accountView(ctx, a))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAccount(r.Context(), me(r), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "account")
		return
	}
	writeJSON(w, http.StatusOK, s.accountView(r.Context(), a))
}

// normalizeAccountURL checks an address: http(s) only, webcal → https,
// a missing scheme means https.
func normalizeAccountURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	low := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(low, "webcals://"):
		raw = "https://" + raw[len("webcals://"):]
	case strings.HasPrefix(low, "webcal://"):
		raw = "https://" + raw[len("webcal://"):]
	case !strings.Contains(raw, "://"):
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", badRequest("this does not look like an address (https://…)")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", badRequest("only http:// and https:// (or webcal://) addresses work")
	}
	if u.User != nil {
		return "", badRequest("put the user name and password in their own fields, not in the address")
	}
	u.Fragment = ""
	return u.String(), nil
}

func defaultSyncMinutes(kind string) int {
	if kind == "ics" {
		return 60
	}
	return 15
}

// applyAccountIn copies the input onto an account (new or stored) and
// checks what the kind needs. It reports whether the connection changed.
func (s *Server) applyAccountIn(a *store.Account, in *accountIn) (bool, error) {
	conn := false
	if in.Name != nil {
		a.Name = strings.TrimSpace(*in.Name)
	}
	if in.URL != nil && a.Kind != "google" {
		u, err := normalizeAccountURL(*in.URL)
		if err != nil {
			return false, err
		}
		if u != a.URL {
			a.URL, conn = u, true
		}
	}
	if in.Username != nil && a.Kind != "google" {
		if u := strings.TrimSpace(*in.Username); u != a.Username {
			a.Username, conn = u, true
		}
	}
	if in.Password != nil && *in.Password != "" && a.Kind != "google" {
		sealed, err := s.seal(*in.Password)
		if err != nil {
			return false, err
		}
		a.Secret, conn = sealed, true
	}
	if in.Via != nil && a.Kind == "nextcloud" {
		v := strings.TrimSpace(*in.Via)
		if v == "" {
			v = "direct"
		}
		if v != "direct" && v != "core" {
			return false, badRequest("via is direct or core")
		}
		if v != a.Via {
			a.Via, conn = v, true
		}
	}
	if in.SyncMinutes != nil {
		m := *in.SyncMinutes
		if m < 5 {
			m = 5
		}
		if m > 1440 {
			m = 1440
		}
		a.SyncMinutes = m
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if a.Via == "" {
		a.Via = "direct"
	}
	switch a.Kind {
	case "caldav":
		if a.URL == "" {
			return conn, badRequest("the server address is needed (e.g. https://caldav.icloud.com or https://caldav.fastmail.com)")
		}
	case "nextcloud":
		if a.Via == "core" {
			if !s.core.Configured() {
				return conn, badRequest("Pocket Core is not configured on this box — use the Nextcloud address and an app password instead")
			}
		} else if a.URL == "" || a.Username == "" || a.Secret == "" {
			return conn, badRequest("the Nextcloud address, user name and an app password are needed")
		}
	case "ics":
		if a.URL == "" {
			return conn, badRequest("the address of the .ics feed is needed")
		}
	}
	return conn, nil
}

// probe connects to an account and lists what it would mirror.
type probeResult struct {
	calendars []caldav.Calendar
	feed      *icsFeed
}

func (s *Server) probeAccount(ctx context.Context, a *store.Account) (*probeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if a.Kind == "ics" {
		feed, _, err := s.fetchFeed(ctx, a.URL, "", "")
		if err != nil {
			return nil, err
		}
		return &probeResult{feed: feed, calendars: []caldav.Calendar{{Href: a.URL, Name: firstNonEmpty(feed.Name, hostOf(a.URL)), Color: feed.Color, ReadOnly: true}}}, nil
	}
	cl, err := s.davClient(ctx, a)
	if err != nil {
		return nil, err
	}
	cals, err := s.remoteCalendars(ctx, a, cl)
	if err != nil {
		return nil, err
	}
	return &probeResult{calendars: cals}, nil
}

// connectError is the 400 a failed connection answers.
func connectError(err error) error {
	var ue userError
	if errors.As(err, &ue) {
		return err
	}
	msg := friendlyAccountError(err)
	var dnsErr *net.DNSError
	var opErr *net.OpError
	if errors.As(err, &dnsErr) || errors.As(err, &opErr) {
		msg = "the server cannot be reached: " + err.Error()
	}
	return badRequest("could not connect: " + msg)
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return strings.TrimPrefix(u.Hostname(), "www.")
	}
	return raw
}

// defaultAccountName names an account after its provider.
func defaultAccountName(a *store.Account, p *probeResult) string {
	if a.Kind == "ics" && p != nil && p.feed != nil && p.feed.Name != "" {
		return p.feed.Name
	}
	host := hostOf(a.URL)
	switch {
	case a.Kind == "nextcloud":
		return "Nextcloud"
	case strings.Contains(host, "icloud.com"):
		return "iCloud"
	case strings.Contains(host, "fastmail"):
		return "Fastmail"
	case strings.Contains(host, "mailbox.org"):
		return "mailbox.org"
	case strings.Contains(host, "zoho"):
		return "Zoho"
	case strings.Contains(host, "yahoo"):
		return "Yahoo"
	case strings.Contains(host, "gmx"):
		return "GMX"
	case a.Username != "" && a.Kind == "caldav":
		return a.Username + " @ " + host
	}
	return host
}

func (s *Server) newAccountFromBody(r *http.Request) (*store.Account, *accountIn, error) {
	var in accountIn
	if err := decode(r, &in); err != nil {
		return nil, nil, badRequest("invalid json")
	}
	kind := ""
	if in.Kind != nil {
		kind = strings.ToLower(strings.TrimSpace(*in.Kind))
	}
	if !accountKinds[kind] {
		return nil, nil, badRequest("kind is caldav, nextcloud, google or ics")
	}
	if kind == "google" {
		return nil, nil, badRequest("Google accounts are connected through Google's sign-in: open /api/v1/accounts/google/start")
	}
	a := &store.Account{UserID: me(r), Kind: kind, Via: "direct", Enabled: true, SyncMinutes: defaultSyncMinutes(kind)}
	if _, err := s.applyAccountIn(a, &in); err != nil {
		return nil, nil, err
	}
	return a, &in, nil
}

// handleTestAccount connects without saving: {ok, calendars:[{name, color, href, read_only}]}.
func (s *Server) handleTestAccount(w http.ResponseWriter, r *http.Request) {
	a, _, err := s.newAccountFromBody(r)
	if err != nil {
		s.fail(w, err, "account")
		return
	}
	p, err := s.probeAccount(r.Context(), a)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": connectError(err).Error(), "calendars": []any{}})
		return
	}
	cals := []map[string]any{}
	for _, c := range p.calendars {
		cals = append(cals, map[string]any{"name": c.Name, "color": normColor(c.Color), "href": c.Href, "read_only": c.ReadOnly})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": defaultAccountName(a, p), "calendars": cals})
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, _, err := s.newAccountFromBody(r)
	if err != nil {
		s.fail(w, err, "account")
		return
	}
	if accs, err := s.store.ListAccounts(ctx, a.UserID); err == nil {
		for _, o := range accs {
			if o.Kind == a.Kind && o.URL == a.URL && strings.EqualFold(o.Username, a.Username) && (a.Kind != "nextcloud" || o.Via == a.Via) {
				writeErr(w, http.StatusConflict, "this account is connected already ("+o.Name+")")
				return
			}
		}
	}
	p, err := s.probeAccount(ctx, a)
	if err != nil {
		s.fail(w, connectError(err), "account")
		return
	}
	if a.Name == "" {
		a.Name = defaultAccountName(a, p)
	}
	if err := s.store.SaveAccount(ctx, a); err != nil {
		s.fail(w, err, "save account")
		return
	}
	// the calendars appear at once; their events follow in the background
	if s.beginSync(a.ID) {
		if a.Kind == "ics" {
			_, err = s.applyFeed(ctx, a, p.feed)
			s.store.SetAccountSync(ctx, a.ID, errText(err))
		} else {
			_, err = s.mirrorCalendars(ctx, a, p.calendars)
		}
		s.endSync(a.ID)
		if err != nil {
			s.log.Warn("account: first sync", "account", a.ID, "err", err)
		}
	}
	if a.Kind != "ics" {
		s.kickSync(a.ID)
	}
	if a.Kind == "nextcloud" {
		go s.reportModes(context.WithoutCancel(ctx))
	}
	s.store.AddActivity(ctx, a.UserID, nil, nil, "account_added", a.Name, a.Kind)
	s.touch()
	a, err = s.store.GetAccount(ctx, a.UserID, a.ID)
	if err != nil {
		s.fail(w, err, "account")
		return
	}
	writeJSON(w, http.StatusCreated, s.accountView(ctx, a))
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, err := s.store.GetAccount(ctx, me(r), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "account")
		return
	}
	var in accountIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Kind != nil && strings.ToLower(strings.TrimSpace(*in.Kind)) != a.Kind {
		writeErr(w, http.StatusBadRequest, "the kind of an account cannot change — add a new one")
		return
	}
	oldURL := a.URL
	conn, err := s.applyAccountIn(a, &in)
	if err != nil {
		s.fail(w, err, "account")
		return
	}
	if a.Name == "" {
		a.Name = defaultAccountName(a, nil)
	}
	if conn && a.Enabled {
		if _, err := s.probeAccount(ctx, a); err != nil {
			s.fail(w, connectError(err), "account")
			return
		}
	}
	if err := s.store.SaveAccount(ctx, a); err != nil {
		s.fail(w, err, "save account")
		return
	}
	if a.Kind == "ics" && a.URL != oldURL {
		if cals, err := s.store.CalendarsOfAccount(ctx, a.ID); err == nil {
			for _, c := range cals {
				s.setCalendarRemote(ctx, c.ID, "", "")
			}
		}
	}
	if in.RestoreCalendars {
		_ = s.store.SetUserSetting(ctx, a.UserID, accountMemoryKey(a.ID), "")
	}
	if a.Enabled && (conn || in.RestoreCalendars || in.Enabled != nil) {
		s.kickSync(a.ID)
	}
	if a.Kind == "nextcloud" {
		go s.reportModes(context.WithoutCancel(ctx))
	}
	s.touch()
	a, err = s.store.GetAccount(ctx, a.UserID, a.ID)
	if err != nil {
		s.fail(w, err, "account")
		return
	}
	writeJSON(w, http.StatusOK, s.accountView(ctx, a))
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := me(r)
	a, err := s.store.GetAccount(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "account")
		return
	}
	if err := s.store.DeleteAccount(ctx, uid, a.ID); err != nil {
		s.fail(w, err, "delete account")
		return
	}
	_ = s.store.SetUserSetting(ctx, uid, accountMemoryKey(a.ID), "")
	s.acctRuntime().pushFail.Delete(a.ID)
	if a.Kind == "nextcloud" {
		go s.reportModes(context.WithoutCancel(ctx))
	}
	s.store.AddActivity(ctx, uid, nil, nil, "account_removed", a.Name, a.Kind)
	s.touch()
	w.WriteHeader(http.StatusNoContent)
}

// handleSyncAccount starts a sync (?wait=1 runs it before answering) and
// answers the account with its status.
func (s *Server) handleSyncAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, err := s.store.GetAccount(ctx, me(r), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "account")
		return
	}
	if queryBool(r, "wait") {
		if err := s.syncAccount(ctx, a.ID, false); err != nil && !errors.Is(err, errSyncBusy) {
			s.log.Debug("account sync on request", "account", a.ID, "err", err)
		}
	} else if !s.isSyncing(a.ID) {
		go func() { _ = s.syncAccount(context.WithoutCancel(ctx), a.ID, false) }()
	}
	if a, err = s.store.GetAccount(ctx, a.UserID, a.ID); err != nil {
		s.fail(w, err, "account")
		return
	}
	v := s.accountView(ctx, a)
	if !queryBool(r, "wait") {
		v["syncing"] = true
	}
	writeJSON(w, http.StatusOK, v)
}
