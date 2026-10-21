package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// DefaultTitle is the app title until someone changes it.
const DefaultTitle = "Pocket Calendar"

// Settings are the box settings plus one person's preferences.
type Settings struct {
	Title         string `json:"title"`
	SetupComplete bool   `json:"setup_complete"`

	Timezone           string   `json:"timezone"`   // "" = the box zone
	WeekStart          int      `json:"week_start"` // 0 Sunday, 1 Monday, 6 Saturday
	TimeFormat         string   `json:"time_format"`
	DefaultView        string   `json:"default_view"` // day | week | month | year | agenda | people
	DefaultCalendarID  int64    `json:"default_calendar_id"`
	DefaultDurationMin int      `json:"default_duration_min"`
	DefaultAlarms      []string `json:"default_alarms"` // for new timed events, e.g. ["-PT10M"]
	AllDayAlarms       []string `json:"all_day_alarms"` // for new all-day events, e.g. ["-PT15H"] (09:00 the day before)
	WorkStart          int      `json:"work_start"`     // hour the working day starts (shading, scroll position)
	WorkEnd            int      `json:"work_end"`
	WorkDays           []int    `json:"work_days"` // 0=Sun..6=Sat
	ShowWeekNumbers    bool     `json:"show_week_numbers"`
	ShowDeclined       bool     `json:"show_declined"`
	ShowWeekends       bool     `json:"show_weekends"`
	Density            string   `json:"density"` // comfortable | compact
	HolidayCountry     string   `json:"holiday_country"`
	FreeBusyShared     bool     `json:"freebusy_shared"` // others on the box may see when I'm busy
	InvitesColor       string   `json:"invites_color"`

	NotifyKind   string `json:"notify_kind"` // apprise | ntfy | webhook
	NotifyURL    string `json:"notify_url"`
	NotifyToken  string `json:"-"`
	NotifyAlarms bool   `json:"notify_alarms"`
	NotifyInvite bool   `json:"notify_invites"`
	DigestTime   string `json:"digest_time"` // "07:30" or ""

	// Pocket apps and other sources shown next to the calendars:
	// source → "core" | "direct" | "off".
	Overlays      map[string]string `json:"overlays"`
	OverlayColors map[string]string `json:"overlay_colors"`
	OverlayURLs   map[string]string `json:"overlay_urls"`
	OverlayTokens map[string]bool   `json:"overlay_tokens"` // which sources have a direct token saved
}

func defaults() Settings {
	return Settings{
		Title: DefaultTitle, WeekStart: 1, TimeFormat: "24h", DefaultView: "week", DefaultDurationMin: 60,
		DefaultAlarms: []string{"-PT10M"}, AllDayAlarms: []string{}, WorkStart: 8, WorkEnd: 18, WorkDays: []int{1, 2, 3, 4, 5},
		ShowWeekends: true, Density: "comfortable", InvitesColor: "#8b5cf6",
		NotifyKind: "apprise", NotifyAlarms: true, NotifyInvite: true,
		Overlays: map[string]string{}, OverlayColors: map[string]string{}, OverlayURLs: map[string]string{}, OverlayTokens: map[string]bool{},
	}
}

func splitList(v string) []string {
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// GetSettings loads the box settings plus the preferences of userID, with
// defaults applied. userID 0 gives the box settings with default preferences.
func (s *Store) GetSettings(ctx context.Context, userID int64) (Settings, error) {
	st := defaults()
	rows, err := s.DB.QueryContext(ctx, `SELECT key, value FROM settings WHERE key IN ('title', 'setup_complete')
		UNION ALL SELECT key, value FROM user_settings WHERE user_id = ?`, userID)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	atoi := func(v string, def int) int {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		return def
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return st, err
		}
		switch {
		case k == "title":
			if v != "" {
				st.Title = v
			}
		case k == "setup_complete":
			st.SetupComplete = v == "1"
		case k == "timezone":
			st.Timezone = v
		case k == "week_start":
			st.WeekStart = atoi(v, 1)
		case k == "time_format":
			st.TimeFormat = v
		case k == "default_view":
			st.DefaultView = v
		case k == "default_calendar_id":
			st.DefaultCalendarID, _ = strconv.ParseInt(v, 10, 64)
		case k == "default_duration_min":
			st.DefaultDurationMin = atoi(v, 60)
		case k == "default_alarms":
			st.DefaultAlarms = splitList(v)
		case k == "all_day_alarms":
			st.AllDayAlarms = splitList(v)
		case k == "work_start":
			st.WorkStart = atoi(v, 8)
		case k == "work_end":
			st.WorkEnd = atoi(v, 18)
		case k == "work_days":
			st.WorkDays = []int{}
			for _, p := range splitList(v) {
				st.WorkDays = append(st.WorkDays, atoi(p, 0))
			}
		case k == "show_week_numbers":
			st.ShowWeekNumbers = v == "1"
		case k == "show_declined":
			st.ShowDeclined = v == "1"
		case k == "show_weekends":
			st.ShowWeekends = v != "0"
		case k == "density":
			st.Density = v
		case k == "holiday_country":
			st.HolidayCountry = v
		case k == "freebusy_shared":
			st.FreeBusyShared = v == "1"
		case k == "invites_color":
			st.InvitesColor = v
		case k == "notify_kind":
			st.NotifyKind = v
		case k == "notify_url":
			st.NotifyURL = v
		case k == "notify_token":
			st.NotifyToken = v
		case k == "notify_alarms":
			st.NotifyAlarms = v != "0"
		case k == "notify_invites":
			st.NotifyInvite = v != "0"
		case k == "digest_time":
			st.DigestTime = v
		case strings.HasPrefix(k, "overlay.mode."):
			st.Overlays[strings.TrimPrefix(k, "overlay.mode.")] = v
		case strings.HasPrefix(k, "overlay.color."):
			st.OverlayColors[strings.TrimPrefix(k, "overlay.color.")] = v
		case strings.HasPrefix(k, "overlay.url."):
			st.OverlayURLs[strings.TrimPrefix(k, "overlay.url.")] = v
		case strings.HasPrefix(k, "overlay.token."):
			st.OverlayTokens[strings.TrimPrefix(k, "overlay.token.")] = v != ""
		}
	}
	return st, rows.Err()
}

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func itoa(n int) string { return strconv.Itoa(n) }

// SaveSettings stores a person's preferences (not the notify token, not overlays).
func (s *Store) SaveSettings(ctx context.Context, userID int64, st Settings) error {
	days := make([]string, 0, len(st.WorkDays))
	for _, d := range st.WorkDays {
		days = append(days, itoa(d))
	}
	return s.putUserSettings(ctx, userID, map[string]string{
		"timezone": st.Timezone, "week_start": itoa(st.WeekStart), "time_format": st.TimeFormat, "default_view": st.DefaultView,
		"default_calendar_id": strconv.FormatInt(st.DefaultCalendarID, 10), "default_duration_min": itoa(st.DefaultDurationMin),
		"default_alarms": strings.Join(st.DefaultAlarms, ","), "all_day_alarms": strings.Join(st.AllDayAlarms, ","),
		"work_start": itoa(st.WorkStart), "work_end": itoa(st.WorkEnd), "work_days": strings.Join(days, ","),
		"show_week_numbers": b2s(st.ShowWeekNumbers), "show_declined": b2s(st.ShowDeclined), "show_weekends": b2s(st.ShowWeekends),
		"density": st.Density, "holiday_country": st.HolidayCountry, "freebusy_shared": b2s(st.FreeBusyShared),
		"invites_color": st.InvitesColor, "notify_kind": st.NotifyKind, "notify_url": st.NotifyURL,
		"notify_alarms": b2s(st.NotifyAlarms), "notify_invites": b2s(st.NotifyInvite), "digest_time": st.DigestTime,
	})
}

// UserSetting reads one raw key of a person's settings ("" when missing).
func (s *Store) UserSetting(ctx context.Context, userID int64, key string) string {
	var v string
	_ = s.DB.QueryRowContext(ctx, `SELECT value FROM user_settings WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	return v
}

// SetUserSetting stores one key for a person.
func (s *Store) SetUserSetting(ctx context.Context, userID int64, key, value string) error {
	return s.putUserSettings(ctx, userID, map[string]string{key: value})
}

// SaveBoxSettings stores the settings of the box itself: title and setup state.
func (s *Store) SaveBoxSettings(ctx context.Context, st Settings) error {
	kv := map[string]string{"title": st.Title, "setup_complete": b2s(st.SetupComplete)}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) putUserSettings(ctx context.Context, userID int64, kv map[string]string) error {
	if userID <= 0 {
		return ErrNotFound
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_settings (user_id, key, value) VALUES (?, ?, ?)
			ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, userID, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// OwnerID is the account the box was set up with (the first user): keys
// given to the whole box through the environment are theirs.
func (s *Store) OwnerID(ctx context.Context) int64 {
	var id sql.NullInt64
	_ = s.DB.QueryRowContext(ctx, `SELECT MIN(id) FROM users`).Scan(&id)
	return id.Int64
}

// UserIDs lists every user id.
func (s *Store) UserIDs(ctx context.Context) []int64 {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM users ORDER BY id`)
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
