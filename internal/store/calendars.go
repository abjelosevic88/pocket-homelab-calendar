package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Roles, weakest first. The owner can do everything.
const (
	RoleFreeBusy = "freebusy"
	RoleRead     = "read"
	RoleWrite    = "write"
	RoleManage   = "manage"
	RoleOwner    = "owner"
)

var roleRank = map[string]int{RoleFreeBusy: 1, RoleRead: 2, RoleWrite: 3, RoleManage: 4, RoleOwner: 5}

// RoleAtLeast reports whether have grants want.
func RoleAtLeast(have, want string) bool { return roleRank[have] >= roleRank[want] }

// ValidRole reports whether r can be granted through a share.
func ValidRole(r string) bool {
	return r == RoleFreeBusy || r == RoleRead || r == RoleWrite || r == RoleManage
}

const calCols = `c.id, c.user_id, c.uid, c.name, c.color, c.description, c.timezone, c.kind, c.account_id, c.remote_href,
	c.remote_ctag, c.remote_token, c.read_only, c.is_default, c.default_alarms, c.feed_token, c.public, c.sort_order, c.seq,
	c.created_at, c.updated_at`

func scanCalendar(row interface{ Scan(...any) error }, extra ...any) (*Calendar, error) {
	var c Calendar
	var acc sql.NullInt64
	var alarms string
	dest := []any{&c.ID, &c.UserID, &c.UID, &c.Name, &c.Color, &c.Description, &c.Timezone, &c.Kind, &acc, &c.RemoteHref,
		&c.RemoteCTag, &c.RemoteToken, &c.ReadOnly, &c.IsDefault, &alarms, &c.FeedToken, &c.Public, &c.SortOrder, &c.Seq,
		&c.CreatedAt, &c.UpdatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, notFound(err)
	}
	if acc.Valid {
		c.AccountID = &acc.Int64
	}
	c.DefaultAlarms = []string{}
	_ = jsonUnmarshal(alarms, &c.DefaultAlarms)
	return &c, nil
}

// GetCalendar returns a calendar by id (no access check).
func (s *Store) GetCalendar(ctx context.Context, id int64) (*Calendar, error) {
	return scanCalendar(s.DB.QueryRowContext(ctx, `SELECT `+calCols+` FROM calendars c WHERE c.id = ?`, id))
}

// GetCalendarByUID returns one of a person's own calendars by its path segment.
func (s *Store) GetCalendarByUID(ctx context.Context, userID int64, uid string) (*Calendar, error) {
	return scanCalendar(s.DB.QueryRowContext(ctx, `SELECT `+calCols+` FROM calendars c WHERE c.user_id = ? AND c.uid = ?`, userID, uid))
}

// GetCalendarByFeed resolves a calendar's secret feed token.
func (s *Store) GetCalendarByFeed(ctx context.Context, tok string) (*Calendar, error) {
	if tok == "" {
		return nil, ErrNotFound
	}
	return scanCalendar(s.DB.QueryRowContext(ctx, `SELECT `+calCols+` FROM calendars c WHERE c.feed_token = ?`, tok))
}

// CalendarRole returns what userID may do with a calendar; ErrNotFound when
// they may not see it at all (so other people's ids answer 404).
func (s *Store) CalendarRole(ctx context.Context, userID, calID int64) (string, *Calendar, error) {
	c, err := s.GetCalendar(ctx, calID)
	if err != nil {
		return "", nil, err
	}
	if c.UserID == userID {
		return RoleOwner, c, nil
	}
	role := ""
	rows, err := s.DB.QueryContext(ctx, `SELECT role FROM calendar_shares WHERE calendar_id = ? AND (user_id = ? OR everyone = 1)`, calID, userID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		if rows.Scan(&r) == nil && roleRank[r] > roleRank[role] {
			role = r
		}
	}
	if role == "" {
		return "", nil, ErrNotFound
	}
	return role, c, nil
}

// ListCalendarsFor returns every calendar a person owns or that is shared
// with them, with their role and own preferences.
func (s *Store) ListCalendarsFor(ctx context.Context, userID int64) ([]*Calendar, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+calCols+`, COALESCE(p.visible, 1), COALESCE(p.notify, CASE WHEN c.user_id = ? THEN 1 ELSE 0 END), COALESCE(p.color, ''),
		COALESCE(p.sort_order, c.sort_order),
		CASE WHEN c.user_id = ? THEN 'owner' ELSE (SELECT role FROM calendar_shares sh WHERE sh.calendar_id = c.id AND (sh.user_id = ? OR sh.everyone = 1)
			ORDER BY CASE role WHEN 'manage' THEN 4 WHEN 'write' THEN 3 WHEN 'read' THEN 2 ELSE 1 END DESC LIMIT 1) END,
		(SELECT COUNT(1) FROM events e WHERE e.calendar_id = c.id),
		u.id, u.name, u.email
		FROM calendars c
		JOIN users u ON u.id = c.user_id
		LEFT JOIN calendar_prefs p ON p.calendar_id = c.id AND p.user_id = ?
		WHERE c.user_id = ? OR EXISTS (SELECT 1 FROM calendar_shares sh WHERE sh.calendar_id = c.id AND (sh.user_id = ? OR sh.everyone = 1))
		ORDER BY CASE WHEN c.user_id = ? THEN 0 ELSE 1 END, COALESCE(p.sort_order, c.sort_order), c.id`,
		userID, userID, userID, userID, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Calendar
	for rows.Next() {
		var p Person
		var sort float64
		var c *Calendar
		var visible, notify bool
		var myColor, role string
		var n int
		c, err = scanCalendar(rows, &visible, &notify, &myColor, &sort, &role, &n, &p.ID, &p.Name, &p.Email)
		if err != nil {
			return nil, err
		}
		c.Visible, c.Notify, c.MyColor, c.Role, c.Events, c.SortOrder = visible, notify, myColor, role, n, sort
		c.Owner = &p
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateCalendar inserts a calendar (uid and feed token are made up when empty).
func (s *Store) CreateCalendar(ctx context.Context, c *Calendar) error {
	if c.UID == "" {
		c.UID = s.freeCalendarUID(ctx, c.UserID, c.Name)
	}
	if c.FeedToken == "" {
		c.FeedToken = RandomToken(18)
	}
	if c.Kind == "" {
		c.Kind = "local"
	}
	if c.DefaultAlarms == nil {
		c.DefaultAlarms = []string{}
	}
	if c.SortOrder == 0 {
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), 0) + 1 FROM calendars WHERE user_id = ?`, c.UserID).Scan(&c.SortOrder)
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO calendars (user_id, uid, name, color, description, timezone, kind, account_id, remote_href,
		read_only, is_default, default_alarms, feed_token, public, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.UserID, c.UID, c.Name, c.Color, c.Description, c.Timezone, c.Kind, nullInt(c.AccountID), c.RemoteHref,
		c.ReadOnly, c.IsDefault, jsonMarshal(c.DefaultAlarms), c.FeedToken, c.Public, c.SortOrder)
	if err != nil {
		return conflict(err)
	}
	c.ID, _ = res.LastInsertId()
	c.CreatedAt, c.UpdatedAt = Now(), Now()
	return nil
}

// UpdateCalendar stores the editable columns.
func (s *Store) UpdateCalendar(ctx context.Context, c *Calendar) error {
	if c.DefaultAlarms == nil {
		c.DefaultAlarms = []string{}
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE calendars SET name = ?, color = ?, description = ?, timezone = ?, read_only = ?,
		is_default = ?, default_alarms = ?, feed_token = ?, public = ?, sort_order = ?, remote_href = ?, remote_ctag = ?,
		remote_token = ?, seq = ?, updated_at = ? WHERE id = ?`,
		c.Name, c.Color, c.Description, c.Timezone, c.ReadOnly, c.IsDefault, jsonMarshal(c.DefaultAlarms), c.FeedToken, c.Public,
		c.SortOrder, c.RemoteHref, c.RemoteCTag, c.RemoteToken, c.Seq, Now(), c.ID)
	return conflict(err)
}

// SetDefaultCalendar marks one of a person's calendars as where new events go.
func (s *Store) SetDefaultCalendar(ctx context.Context, userID, calID int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE calendars SET is_default = CASE WHEN id = ? THEN 1 ELSE 0 END WHERE user_id = ?`, calID, userID)
	return err
}

// DeleteCalendar removes a calendar and its events.
func (s *Store) DeleteCalendar(ctx context.Context, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM events_fts WHERE rowid IN (SELECT id FROM events WHERE calendar_id = ?)`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM calendars WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, _ = tx.ExecContext(ctx, `DELETE FROM tombstones WHERE calendar_id = ?`, id)
	return tx.Commit()
}

// DefaultCalendar returns where a person's new events go: their default
// calendar, else their first writable local one.
func (s *Store) DefaultCalendar(ctx context.Context, userID int64) (*Calendar, error) {
	c, err := scanCalendar(s.DB.QueryRowContext(ctx, `SELECT `+calCols+` FROM calendars c WHERE c.user_id = ? AND c.read_only = 0
		ORDER BY c.is_default DESC, CASE c.kind WHEN 'local' THEN 0 ELSE 1 END, c.sort_order, c.id LIMIT 1`, userID))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	return c, err
}

// CalendarsOfAccount lists the calendars mirrored from an account.
func (s *Store) CalendarsOfAccount(ctx context.Context, accountID int64) ([]*Calendar, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+calCols+` FROM calendars c WHERE c.account_id = ? ORDER BY c.sort_order, c.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Calendar
	for rows.Next() {
		c, err := scanCalendar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// OwnCalendars lists a person's own calendars (CalDAV home).
func (s *Store) OwnCalendars(ctx context.Context, userID int64) ([]*Calendar, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+calCols+` FROM calendars c WHERE c.user_id = ? ORDER BY c.sort_order, c.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Calendar
	for rows.Next() {
		c, err := scanCalendar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SavePrefs stores a person's own view of a calendar.
func (s *Store) SavePrefs(ctx context.Context, userID, calID int64, visible, notify *bool, color *string, sort *float64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO calendar_prefs (user_id, calendar_id, notify)
		VALUES (?, ?, (SELECT CASE WHEN user_id = ? THEN 1 ELSE 0 END FROM calendars WHERE id = ?)) ON CONFLICT DO NOTHING`, userID, calID, userID, calID)
	if err != nil {
		return err
	}
	if visible != nil {
		if _, err := s.DB.ExecContext(ctx, `UPDATE calendar_prefs SET visible = ? WHERE user_id = ? AND calendar_id = ?`, *visible, userID, calID); err != nil {
			return err
		}
	}
	if notify != nil {
		if _, err := s.DB.ExecContext(ctx, `UPDATE calendar_prefs SET notify = ? WHERE user_id = ? AND calendar_id = ?`, *notify, userID, calID); err != nil {
			return err
		}
	}
	if color != nil {
		if _, err := s.DB.ExecContext(ctx, `UPDATE calendar_prefs SET color = ? WHERE user_id = ? AND calendar_id = ?`, *color, userID, calID); err != nil {
			return err
		}
	}
	if sort != nil {
		if _, err := s.DB.ExecContext(ctx, `UPDATE calendar_prefs SET sort_order = ? WHERE user_id = ? AND calendar_id = ?`, *sort, userID, calID); err != nil {
			return err
		}
	}
	return nil
}

// BumpCalendar hands a calendar a new change number (CalDAV ctag/sync token).
func (s *Store) BumpCalendar(ctx context.Context, tx *sql.Tx, calID int64) (int64, error) {
	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'seq'`).Scan(&n); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	n++
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('seq', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, n); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE calendars SET seq = ?, updated_at = ? WHERE id = ?`, n, Now(), calID); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) freeCalendarUID(ctx context.Context, userID int64, name string) string {
	base := Slug(name)
	if base == "" {
		base = "calendar"
	}
	uid := base
	for i := 2; i < 1000; i++ {
		var n int
		_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM calendars WHERE user_id = ? AND uid = ?`, userID, uid).Scan(&n)
		if n == 0 {
			return uid
		}
		uid = base + "-" + itoa(i)
	}
	return base + "-" + RandomToken(4)
}

// ---------- sharing ----------

// ListShares returns who a calendar is shared with.
func (s *Store) ListShares(ctx context.Context, calID int64) ([]Share, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT sh.id, sh.calendar_id, sh.user_id, sh.email, sh.everyone, sh.role, sh.created_at,
		COALESCE(u.name, ''), COALESCE(u.email, '')
		FROM calendar_shares sh LEFT JOIN users u ON u.id = sh.user_id WHERE sh.calendar_id = ? ORDER BY sh.everyone DESC, sh.id`, calID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		var sh Share
		var uid sql.NullInt64
		var name, email string
		if err := rows.Scan(&sh.ID, &sh.CalendarID, &uid, &sh.Email, &sh.Everyone, &sh.Role, &sh.CreatedAt, &name, &email); err != nil {
			return nil, err
		}
		if uid.Valid {
			sh.UserID = &uid.Int64
			sh.Person = &Person{ID: uid.Int64, Name: name, Email: email}
		} else if !sh.Everyone {
			sh.Pending = true
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

// UpsertShare grants (or changes) access for a person, an email or everyone.
func (s *Store) UpsertShare(ctx context.Context, sh *Share, by int64) error {
	var err error
	switch {
	case sh.Everyone:
		_, err = s.DB.ExecContext(ctx, `INSERT INTO calendar_shares (calendar_id, everyone, role, created_by) VALUES (?, 1, ?, ?)
			ON CONFLICT (calendar_id) WHERE everyone = 1 DO UPDATE SET role = excluded.role`, sh.CalendarID, sh.Role, by)
	case sh.UserID != nil:
		_, err = s.DB.ExecContext(ctx, `INSERT INTO calendar_shares (calendar_id, user_id, role, created_by) VALUES (?, ?, ?, ?)
			ON CONFLICT (calendar_id, user_id) WHERE user_id IS NOT NULL DO UPDATE SET role = excluded.role`, sh.CalendarID, *sh.UserID, sh.Role, by)
	default:
		_, err = s.DB.ExecContext(ctx, `INSERT INTO calendar_shares (calendar_id, email, role, created_by) VALUES (?, ?, ?, ?)
			ON CONFLICT (calendar_id, email) WHERE user_id IS NULL AND email <> '' DO UPDATE SET role = excluded.role`, sh.CalendarID, strings.ToLower(sh.Email), sh.Role, by)
	}
	return err
}

// GetShare returns one share.
func (s *Store) GetShare(ctx context.Context, id int64) (*Share, error) {
	var sh Share
	var uid sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT id, calendar_id, user_id, email, everyone, role, created_at FROM calendar_shares WHERE id = ?`, id).
		Scan(&sh.ID, &sh.CalendarID, &uid, &sh.Email, &sh.Everyone, &sh.Role, &sh.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	if uid.Valid {
		sh.UserID = &uid.Int64
	}
	return &sh, nil
}

// DeleteShare removes a share.
func (s *Store) DeleteShare(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM calendar_shares WHERE id = ?`, id)
	return err
}

// LeaveCalendar removes a person's own share of someone else's calendar
// (a calendar shared with everyone is hidden instead).
func (s *Store) LeaveCalendar(ctx context.Context, userID, calID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM calendar_shares WHERE calendar_id = ? AND user_id = ?`, calID, userID)
	if err != nil {
		return err
	}
	f := false
	return s.SavePrefs(ctx, userID, calID, &f, nil, nil, nil)
}

// BindPendingShares turns shares and attendee rows addressed to an email into
// ones for the account that now has it.
func (s *Store) BindPendingShares(ctx context.Context, userID int64, email string) {
	email = strings.ToLower(email)
	_, _ = s.DB.ExecContext(ctx, `UPDATE OR IGNORE calendar_shares SET user_id = ?, email = '' WHERE user_id IS NULL AND everyone = 0 AND email = ?`, userID, email)
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM calendar_shares WHERE user_id IS NULL AND everyone = 0 AND email = ?`, email)
	_, _ = s.DB.ExecContext(ctx, `UPDATE attendees SET user_id = ? WHERE user_id IS NULL AND email = ?`, userID, email)
}

// People lists everyone on the box (name and email only) for share and
// invite pickers.
func (s *Store) People(ctx context.Context) ([]Person, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, email FROM users ORDER BY name COLLATE NOCASE, email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.Email); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UserIDByEmail returns the account with that email (0 when none).
func (s *Store) UserIDByEmail(ctx context.Context, email string) int64 {
	var id int64
	_ = s.DB.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))).Scan(&id)
	return id
}

// AlarmRecipients are who get the reminders of a calendar's events: the
// owner unless they muted it, and people it is shared with who turned
// reminders on for it.
func (s *Store) AlarmRecipients(ctx context.Context, calID int64) []int64 {
	rows, err := s.DB.QueryContext(ctx, `SELECT c.user_id FROM calendars c LEFT JOIN calendar_prefs p ON p.calendar_id = c.id AND p.user_id = c.user_id
		WHERE c.id = ? AND COALESCE(p.notify, 1) = 1
		UNION SELECT p.user_id FROM calendar_prefs p JOIN calendars c ON c.id = p.calendar_id
		WHERE p.calendar_id = ? AND p.user_id <> c.user_id AND p.notify = 1`, calID, calID)
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
