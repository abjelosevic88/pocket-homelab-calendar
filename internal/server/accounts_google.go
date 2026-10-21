package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/caldav"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// Google Calendar: OAuth 2.0 (web flow, offline access) with the box
// owner's own OAuth client; calendars are listed with the Calendar API v3
// and their events go through Google's CalDAV endpoint with a Bearer token,
// so they run through the same sync engine as every other account.

// googleAPI holds Google's endpoints (tests point them at fakes).
var googleAPI = struct {
	AuthURL         string
	TokenURL        string
	CalendarListURL string
	UserInfoURL     string
	CalDAV          string // origin of the CalDAV API
}{
	AuthURL:         "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:        "https://oauth2.googleapis.com/token",
	CalendarListURL: "https://www.googleapis.com/calendar/v3/users/me/calendarList",
	UserInfoURL:     "https://openidconnect.googleapis.com/v1/userinfo",
	CalDAV:          "https://apidata.googleusercontent.com",
}

// googleEventsHref is the CalDAV collection of a Google calendar.
var googleEventsHref = func(calendarID string) string { return "/caldav/v2/" + calendarID + "/events/" }

const googleScope = "openid email https://www.googleapis.com/auth/calendar"

const googleCookie = "pcal_google"

var errGoogleReconnect = errors.New("Google no longer accepts the sign-in of this account — connect it again (Settings → Accounts)")

var googleMu sync.Mutex // one token refresh at a time

// googleToken is what accounts.oauth holds (sealed).
type googleToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

func (s *Server) googleConfigured() bool {
	return s.cfg.GoogleClientID != "" && s.cfg.GoogleClientSecret != ""
}

func (s *Server) googleRedirectURI(r *http.Request) string {
	return s.absURL(r, "/api/v1/accounts/google/callback")
}

func (s *Server) googleSetupHelp(r *http.Request) string {
	return "Google Calendar needs an OAuth client of your own. In the Google Cloud console (console.cloud.google.com): " +
		"1) create a project; 2) under APIs & Services → Library enable the \"Google Calendar API\" and the \"CalDAV API\"; " +
		"3) set up the OAuth consent screen (External; publish it — in Testing, Google drops the sign-in after 7 days); " +
		"4) under Credentials create an OAuth client ID of type \"Web application\" with the authorized redirect URI " +
		s.googleRedirectURI(r) + " ; 5) set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET for Pocket Calendar and restart it."
}

// settingsURL is where the browser goes back to after the Google round trip.
func (s *Server) settingsURL(msg string) string {
	u := s.cfg.BaseURL + "/settings?tab=accounts"
	if msg != "" {
		u += "&error=" + url.QueryEscape(msg)
	}
	return u
}

// handleGoogleStart sends the browser to Google's consent page.
// ?account=<id> reconnects an existing Google account; ?json=1 answers
// {url} instead of redirecting.
func (s *Server) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if !s.googleConfigured() {
		writeErr(w, http.StatusBadRequest, s.googleSetupHelp(r))
		return
	}
	accID := queryInt64(r, "account")
	if accID > 0 {
		a, err := s.store.GetAccount(ctx, u.ID, accID)
		if err != nil || a.Kind != "google" {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	state := store.RandomToken(16)
	payload := fmt.Sprintf("%s|%d|%d|%d", state, u.ID, accID, time.Now().Unix())
	http.SetCookie(w, &http.Cookie{
		Name: googleCookie, Value: base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + s.sign(payload),
		Path: "/api/v1/accounts/google", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: 900,
	})
	q := url.Values{}
	q.Set("client_id", s.cfg.GoogleClientID)
	q.Set("redirect_uri", s.googleRedirectURI(r))
	q.Set("response_type", "code")
	q.Set("scope", googleScope)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("include_granted_scopes", "true")
	q.Set("state", state)
	if accID > 0 {
		if a, err := s.store.GetAccount(ctx, u.ID, accID); err == nil && strings.Contains(a.Username, "@") {
			q.Set("login_hint", a.Username)
		}
	}
	target := googleAPI.AuthURL + "?" + q.Encode()
	if queryBool(r, "json") {
		writeJSON(w, http.StatusOK, map[string]string{"url": target})
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// readGoogleState checks the state cookie against the answer from Google.
func (s *Server) readGoogleState(r *http.Request) (userID, accountID int64, err error) {
	c, err := r.Cookie(googleCookie)
	if err != nil {
		return 0, 0, errors.New("the sign-in with Google took too long or started in another browser — try again")
	}
	val, sig, ok := strings.Cut(c.Value, ".")
	raw, derr := base64.RawURLEncoding.DecodeString(val)
	if !ok || derr != nil || sig == "" || s.sign(string(raw)) != sig {
		return 0, 0, errors.New("the sign-in with Google could not be verified — try again")
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 4 {
		return 0, 0, errors.New("the sign-in with Google could not be verified — try again")
	}
	at, _ := strconv.ParseInt(parts[3], 10, 64)
	if time.Since(time.Unix(at, 0)) > 15*time.Minute {
		return 0, 0, errors.New("the sign-in with Google took too long — try again")
	}
	if parts[0] != r.URL.Query().Get("state") {
		return 0, 0, errors.New("the answer from Google does not belong to this sign-in — try again")
	}
	userID, _ = strconv.ParseInt(parts[1], 10, 64)
	accountID, _ = strconv.ParseInt(parts[2], 10, 64)
	return userID, accountID, nil
}

// handleGoogleCallback finishes the OAuth flow and stores the account.
func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	back := func(msg string) { http.Redirect(w, r, s.settingsURL(msg), http.StatusFound) }
	uid, accID, err := s.readGoogleState(r)
	http.SetCookie(w, &http.Cookie{Name: googleCookie, Value: "", Path: "/api/v1/accounts/google", MaxAge: -1, HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	if e := r.URL.Query().Get("error"); e != "" {
		if e == "access_denied" {
			back("Google: access was not granted")
		} else {
			back("Google: " + e)
		}
		return
	}
	if err != nil {
		back(err.Error())
		return
	}
	u, err := s.resolveUser(r)
	if err != nil || u == nil || u.ID != uid {
		back("sign in to Pocket Calendar first, then connect Google again")
		return
	}
	if !s.googleConfigured() {
		back("Google is not set up on this box (GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET)")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		back("Google did not send an authorization code")
		return
	}
	tok, idToken, err := s.googleTokenRequest(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.googleRedirectURI(r)},
		"client_id": {s.cfg.GoogleClientID}, "client_secret": {s.cfg.GoogleClientSecret},
	})
	if err != nil {
		back("Google: " + err.Error())
		return
	}
	email := emailFromIDToken(idToken)
	if email == "" {
		email, _ = s.googleUserEmail(ctx, tok.AccessToken)
	}
	if email == "" {
		back("Google did not say which account this is (the \"email\" scope is needed)")
		return
	}
	email = strings.ToLower(email)

	var a *store.Account
	if accID > 0 {
		if acc, err := s.store.GetAccount(ctx, u.ID, accID); err == nil && acc.Kind == "google" {
			a = acc
		}
	}
	if a == nil {
		if accs, err := s.store.ListAccounts(ctx, u.ID); err == nil {
			for _, acc := range accs {
				if acc.Kind == "google" && strings.EqualFold(acc.Username, email) {
					a = acc
				}
			}
		}
	}
	if tok.RefreshToken == "" && a != nil {
		if raw, err := s.open(a.OAuth); err == nil {
			var old googleToken
			if json.Unmarshal([]byte(raw), &old) == nil {
				tok.RefreshToken = old.RefreshToken
			}
		}
	}
	if tok.RefreshToken == "" {
		back("Google did not hand out offline access — remove Pocket Calendar at myaccount.google.com/permissions and connect again")
		return
	}
	b, _ := json.Marshal(tok)
	sealed, err := s.seal(string(b))
	if err != nil {
		back(err.Error())
		return
	}
	created := a == nil
	if created {
		a = &store.Account{UserID: u.ID, Kind: "google", Name: email, SyncMinutes: 15, Enabled: true}
	}
	a.URL, a.Username, a.OAuth, a.Via, a.Secret = googleAPI.CalDAV, email, sealed, "direct", ""
	if err := s.store.SaveAccount(ctx, a); err != nil {
		back("saving the account: " + err.Error())
		return
	}
	if created {
		s.store.AddActivity(ctx, u.ID, nil, nil, "account_added", a.Name, "google")
	}
	s.touch()
	s.kickSync(a.ID)
	http.Redirect(w, r, s.settingsURL("")+"&connected="+strconv.FormatInt(a.ID, 10), http.StatusFound)
}

// googleTokenRequest posts to Google's token endpoint.
func (s *Server) googleTokenRequest(ctx context.Context, form url.Values) (*googleToken, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleAPI.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.userAgent())
	res, err := s.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var out struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode != http.StatusOK || out.AccessToken == "" {
		if out.Error == "invalid_grant" && form.Get("grant_type") == "refresh_token" {
			return nil, "", errGoogleReconnect
		}
		msg := firstNonEmpty(out.Description, out.Error, fmt.Sprintf("HTTP %d", res.StatusCode))
		return nil, "", fmt.Errorf("token request refused: %s", msg)
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	return &googleToken{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken,
		Expiry: time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).UTC()}, out.IDToken, nil
}

// emailFromIDToken reads the email claim of the ID token Google just
// handed over directly (TLS to Google, so it needs no signature check here).
func emailFromIDToken(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(b, &claims) != nil {
		return ""
	}
	return claims.Email
}

func (s *Server) googleUserEmail(ctx context.Context, access string) (string, error) {
	var out struct {
		Email string `json:"email"`
	}
	err := s.googleGet(ctx, access, googleAPI.UserInfoURL, &out)
	return out.Email, err
}

func (s *Server) googleGet(ctx context.Context, access, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.userAgent())
	res, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized {
		return errGoogleReconnect
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := firstNonEmpty(e.Error.Message, fmt.Sprintf("HTTP %d", res.StatusCode))
		if strings.Contains(msg, "has not been used") || strings.Contains(msg, "is disabled") {
			msg += " — enable the Google Calendar API and the CalDAV API in your Google Cloud project"
		}
		return fmt.Errorf("Google: %s", msg)
	}
	return json.Unmarshal(raw, v)
}

// googleAccessToken returns a valid access token for an account,
// refreshing (and storing) it when it is about to expire.
func (s *Server) googleAccessToken(ctx context.Context, accountID int64) (string, error) {
	googleMu.Lock()
	defer googleMu.Unlock()
	a, err := s.store.GetAccount(ctx, 0, accountID)
	if err != nil {
		return "", err
	}
	raw, err := s.open(a.OAuth)
	if err != nil {
		return "", err
	}
	var t googleToken
	if raw == "" || json.Unmarshal([]byte(raw), &t) != nil {
		return "", errGoogleReconnect
	}
	if t.AccessToken != "" && time.Until(t.Expiry) > time.Minute {
		return t.AccessToken, nil
	}
	if t.RefreshToken == "" {
		return "", errGoogleReconnect
	}
	if !s.googleConfigured() {
		return "", errors.New("Google is not set up on this box any more (GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET)")
	}
	nt, _, err := s.googleTokenRequest(ctx, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken},
		"client_id": {s.cfg.GoogleClientID}, "client_secret": {s.cfg.GoogleClientSecret},
	})
	if err != nil {
		return "", err
	}
	t.AccessToken, t.Expiry = nt.AccessToken, nt.Expiry
	if nt.RefreshToken != "" {
		t.RefreshToken = nt.RefreshToken
	}
	b, _ := json.Marshal(t)
	sealed, err := s.seal(string(b))
	if err != nil {
		return "", err
	}
	if _, err := s.store.DB.ExecContext(ctx, `UPDATE accounts SET oauth = ? WHERE id = ?`, sealed, accountID); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// googleCalendars lists the account's calendars through the Calendar API.
func (s *Server) googleCalendars(ctx context.Context, a *store.Account) ([]caldav.Calendar, error) {
	access, err := s.googleAccessToken(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	var out []caldav.Calendar
	page := ""
	for i := 0; i < 20; i++ {
		q := url.Values{"maxResults": {"250"}}
		if page != "" {
			q.Set("pageToken", page)
		}
		var res struct {
			Items []struct {
				ID              string `json:"id"`
				Summary         string `json:"summary"`
				SummaryOverride string `json:"summaryOverride"`
				BackgroundColor string `json:"backgroundColor"`
				AccessRole      string `json:"accessRole"`
				Primary         bool   `json:"primary"`
				Deleted         bool   `json:"deleted"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := s.googleGet(ctx, access, googleAPI.CalendarListURL+"?"+q.Encode(), &res); err != nil {
			return nil, err
		}
		for _, it := range res.Items {
			if it.Deleted || it.ID == "" || it.AccessRole == "freeBusyReader" {
				continue
			}
			order := len(out) + 1
			if it.Primary {
				order = 0
			}
			out = append(out, caldav.Calendar{
				Href: googleEventsHref(it.ID), Name: firstNonEmpty(it.SummaryOverride, it.Summary, it.ID), Color: it.BackgroundColor,
				Components: []string{"VEVENT"}, ReadOnly: it.AccessRole != "owner" && it.AccessRole != "writer", Order: order,
			})
		}
		page = res.NextPageToken
		if page == "" {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out, nil
}
