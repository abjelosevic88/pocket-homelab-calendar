package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
)

// RangeLayout is how range columns are written (sortable as text).
const RangeLayout = "2006-01-02T15:04:05Z"

// ErrPrecondition is returned when If-Match / If-None-Match does not hold.
var ErrPrecondition = errors.New("precondition failed")

// ErrUIDTaken is returned when another resource of the calendar has the UID.
var ErrUIDTaken = errors.New("another event of this calendar has that UID")

// Hook runs inside a write's transaction before it commits (Core events).
type Hook = func(*sql.Tx) error

const eventCols = `e.id, e.calendar_id, e.uid, e.href, e.etag, e.ics, e.summary, e.location, e.all_day, e.dtstart, e.dtend, e.tzid,
	e.rrule, e.recurring, e.range_start, e.range_end, e.status, e.transp, e.class, e.color, e.organizer, e.source, e.created_by,
	e.updated_by, e.seq, e.remote_href, e.remote_etag, e.dirty, e.created_at, e.updated_at`

func scanEvent(row interface{ Scan(...any) error }) (*EventRow, error) {
	var e EventRow
	if err := row.Scan(&e.ID, &e.CalendarID, &e.UID, &e.Href, &e.ETag, &e.ICS, &e.Summary, &e.Location, &e.AllDay, &e.DTStart,
		&e.DTEnd, &e.TZID, &e.RRule, &e.Recurring, &e.RangeStart, &e.RangeEnd, &e.Status, &e.Transp, &e.Class, &e.Color,
		&e.Organizer, &e.Source, &e.CreatedBy, &e.UpdatedBy, &e.Seq, &e.RemoteHref, &e.RemoteETag, &e.Dirty, &e.CreatedAt,
		&e.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	return &e, nil
}

func scanEvents(rows *sql.Rows, err error) ([]*EventRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EventRow
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetEvent returns an event by id (no access check).
func (s *Store) GetEvent(ctx context.Context, id int64) (*EventRow, error) {
	return scanEvent(s.DB.QueryRowContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.id = ?`, id))
}

// GetEventByHref returns a calendar's resource by its name.
func (s *Store) GetEventByHref(ctx context.Context, calID int64, href string) (*EventRow, error) {
	return scanEvent(s.DB.QueryRowContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.calendar_id = ? AND e.href = ?`, calID, href))
}

// GetEventByUID returns a calendar's resource by UID.
func (s *Store) GetEventByUID(ctx context.Context, calID int64, uid string) (*EventRow, error) {
	return scanEvent(s.DB.QueryRowContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.calendar_id = ? AND e.uid = ?`, calID, uid))
}

// GetEventByRemote returns the event mirrored from a remote resource.
func (s *Store) GetEventByRemote(ctx context.Context, calID int64, remoteHref string) (*EventRow, error) {
	return scanEvent(s.DB.QueryRowContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.calendar_id = ? AND e.remote_href = ?`, calID, remoteHref))
}

// ListEvents returns every event of a calendar.
func (s *Store) ListEvents(ctx context.Context, calID int64) ([]*EventRow, error) {
	return scanEvents(s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.calendar_id = ? ORDER BY e.id`, calID))
}

// EventsInRange returns the events of the calendars with an occurrence
// possibly inside [from, to).
func (s *Store) EventsInRange(ctx context.Context, calIDs []int64, from, to time.Time) ([]*EventRow, error) {
	if len(calIDs) == 0 {
		return nil, nil
	}
	ph, args := inList(calIDs)
	args = append(args, to.UTC().Format(RangeLayout), from.UTC().Format(RangeLayout))
	return scanEvents(s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.calendar_id IN (`+ph+`)
		AND e.range_start < ? AND e.range_end > ?`, args...))
}

// InvitedEvents returns events a person is an attendee of (not organizer)
// with an occurrence possibly inside [from, to).
func (s *Store) InvitedEvents(ctx context.Context, userID int64, from, to time.Time) ([]*EventRow, error) {
	return scanEvents(s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events e JOIN attendees a ON a.event_id = e.id
		WHERE a.user_id = ? AND a.is_organizer = 0 AND e.range_start < ? AND e.range_end > ?`,
		userID, to.UTC().Format(RangeLayout), from.UTC().Format(RangeLayout)))
}

// PendingInvites returns events waiting for a person's answer.
func (s *Store) PendingInvites(ctx context.Context, userID int64) ([]*EventRow, error) {
	return scanEvents(s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events e JOIN attendees a ON a.event_id = e.id
		WHERE a.user_id = ? AND a.is_organizer = 0 AND a.partstat = 'NEEDS-ACTION' AND e.range_end > ?
		ORDER BY e.range_start LIMIT 200`, userID, time.Now().UTC().Format(RangeLayout)))
}

// IsAttendee reports whether a person is invited to an event.
func (s *Store) IsAttendee(ctx context.Context, eventID, userID int64) bool {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM attendees WHERE event_id = ? AND user_id = ?`, eventID, userID).Scan(&n)
	return n > 0
}

// Search finds events by words (title, place, notes, attendees).
func (s *Store) Search(ctx context.Context, calIDs []int64, q string, limit int) ([]*EventRow, error) {
	if len(calIDs) == 0 {
		return nil, nil
	}
	match := ftsQuery(q)
	if match == "" {
		return nil, nil
	}
	ph, args := inList(calIDs)
	args = append([]any{match}, args...)
	args = append(args, limit)
	return scanEvents(s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.id IN (SELECT rowid FROM events_fts WHERE events_fts MATCH ?)
		AND e.calendar_id IN (`+ph+`) ORDER BY e.range_start DESC LIMIT ?`, args...))
}

func ftsQuery(q string) string {
	var parts []string
	for _, w := range strings.Fields(q) {
		w = strings.Map(func(r rune) rune {
			if r == '"' || r == '*' || r == ':' || r == '(' || r == ')' || r == '^' {
				return -1
			}
			return r
		}, w)
		if w != "" {
			parts = append(parts, `"`+w+`"*`)
		}
	}
	return strings.Join(parts, " ")
}

func inList(ids []int64) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i], args[i] = "?", id
	}
	return strings.Join(ph, ","), args
}

// EventWrite is one create-or-replace of a calendar object.
type EventWrite struct {
	CalendarID  int64
	Href        string // resource name; defaults to the existing one or <uid>.ics
	ICS         string
	Loc         *time.Location // floating times
	Source      string
	Actor       int64
	IfMatch     string // etag the client last saw ("" = no check)
	IfNoneMatch bool   // create only
	FromRemote  bool   // the write comes from the remote calendar (not dirty)
	RemoteHref  string
	RemoteETag  string
}

// index is what the columns keep of a resource.
type index struct {
	uid, summary, location, description, attendeesText string
	allDay                                             bool
	dtstart, dtend, tzid, rrule                        string
	recurring                                          bool
	rangeStart, rangeEnd                               string
	status, transp, class, color, organizer            string
	attendees                                          []ical.Attendee
	organizerA                                         *ical.Attendee
}

func indexOf(ics string, loc *time.Location) (*index, *ical.Resource, error) {
	r, err := ical.ParseResource(ics, loc)
	if err != nil {
		return nil, nil, err
	}
	m := r.MasterEvent()
	ix := &index{uid: r.UID(), summary: m.Summary, location: m.Location, description: m.Description, allDay: m.AllDay,
		tzid: m.TZID, rrule: m.RRule, recurring: m.RRule != "" || len(m.RDates) > 0 || len(r.Overrides) > 0,
		status: m.Status, transp: m.Transp, class: m.Class, color: m.Color, attendees: m.Attendees, organizerA: m.Organizer}
	if ix.uid == "" {
		return nil, nil, errors.New("the event has no UID")
	}
	if m.Start.IsZero() {
		return nil, nil, errors.New("the event has no start (DTSTART)")
	}
	if m.AllDay {
		ix.dtstart, ix.dtend = m.Start.Format("2006-01-02"), m.End.Format("2006-01-02")
	} else {
		ix.dtstart, ix.dtend = m.Start.UTC().Format(time.RFC3339), m.End.UTC().Format(time.RFC3339)
	}
	if m.Organizer != nil {
		ix.organizer = m.Organizer.Email
	}
	rs, re := r.Span()
	ix.rangeStart, ix.rangeEnd = rs.Format(RangeLayout), re.Format(RangeLayout)
	if re.Year() >= 9999 {
		ix.rangeEnd = ical.Forever.Format(RangeLayout)
	}
	var names []string
	for _, a := range m.Attendees {
		names = append(names, a.Name, a.Email)
	}
	ix.attendeesText = strings.Join(names, " ")
	return ix, r, nil
}

// ValidateICS checks that a text is one event series and returns its UID.
func ValidateICS(ics string, loc *time.Location) (string, error) {
	ix, _, err := indexOf(ics, loc)
	if err != nil {
		return "", err
	}
	return ix.uid, nil
}

// SaveEvent creates or replaces a calendar object and keeps the index,
// search, attendees, change numbers and the remote mirror state in step.
func (s *Store) SaveEvent(ctx context.Context, w EventWrite, hooks ...Hook) (*EventRow, bool, error) {
	ix, _, err := indexOf(w.ICS, w.Loc)
	if err != nil {
		return nil, false, err
	}
	cal, err := s.GetCalendar(ctx, w.CalendarID)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback() //nolint:errcheck

	var existing *EventRow
	if w.Href != "" {
		existing, err = scanEvent(tx.QueryRowContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.calendar_id = ? AND e.href = ?`, w.CalendarID, w.Href))
	} else {
		existing, err = scanEvent(tx.QueryRowContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.calendar_id = ? AND e.uid = ?`, w.CalendarID, ix.uid))
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	if w.IfNoneMatch && existing != nil {
		return nil, false, ErrPrecondition
	}
	if w.IfMatch != "" && w.IfMatch != "*" && (existing == nil || existing.ETag != w.IfMatch) {
		return nil, false, ErrPrecondition
	}
	if w.IfMatch == "*" && existing == nil {
		return nil, false, ErrPrecondition
	}
	if existing != nil && existing.UID != ix.uid {
		return nil, false, fmt.Errorf("%w (the resource holds %q)", ErrUIDTaken, existing.UID)
	}
	if existing == nil {
		var other int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM events WHERE calendar_id = ? AND uid = ?`, w.CalendarID, ix.uid).Scan(&other)
		if other > 0 {
			return nil, false, ErrUIDTaken
		}
	}
	href := w.Href
	if href == "" {
		href = safeHref(ix.uid) + ".ics"
	}
	seq, err := s.BumpCalendar(ctx, tx, w.CalendarID)
	if err != nil {
		return nil, false, err
	}
	etag := ical.ETag(w.ICS)
	dirty := cal.AccountID != nil && !w.FromRemote
	now := Now()
	var id int64
	created := existing == nil
	if existing == nil {
		res, err := tx.ExecContext(ctx, `INSERT INTO events (calendar_id, uid, href, etag, ics, summary, location, description, all_day,
			dtstart, dtend, tzid, rrule, recurring, range_start, range_end, status, transp, class, color, organizer, source,
			created_by, updated_by, seq, remote_href, remote_etag, dirty, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			w.CalendarID, ix.uid, href, etag, w.ICS, ix.summary, ix.location, ix.description, ix.allDay, ix.dtstart, ix.dtend,
			ix.tzid, ix.rrule, ix.recurring, ix.rangeStart, ix.rangeEnd, ix.status, ix.transp, ix.class, ix.color, ix.organizer,
			w.Source, w.Actor, w.Actor, seq, w.RemoteHref, w.RemoteETag, dirty, now, now)
		if err != nil {
			return nil, false, conflict(err)
		}
		id, _ = res.LastInsertId()
		// a resource name that comes back after a delete is no longer a tombstone
		_, _ = tx.ExecContext(ctx, `DELETE FROM tombstones WHERE calendar_id = ? AND href = ?`, w.CalendarID, href)
	} else {
		id = existing.ID
		remoteHref, remoteETag := existing.RemoteHref, existing.RemoteETag
		if w.FromRemote {
			remoteHref, remoteETag = w.RemoteHref, w.RemoteETag
		}
		dirtyArg := dirty || (existing.Dirty && !w.FromRemote)
		if _, err := tx.ExecContext(ctx, `UPDATE events SET etag = ?, ics = ?, summary = ?, location = ?, description = ?, all_day = ?,
			dtstart = ?, dtend = ?, tzid = ?, rrule = ?, recurring = ?, range_start = ?, range_end = ?, status = ?, transp = ?, class = ?,
			color = ?, organizer = ?, source = CASE WHEN source = '' THEN ? ELSE source END, updated_by = ?, seq = ?, remote_href = ?,
			remote_etag = ?, dirty = ?, updated_at = ? WHERE id = ?`,
			etag, w.ICS, ix.summary, ix.location, ix.description, ix.allDay, ix.dtstart, ix.dtend, ix.tzid, ix.rrule, ix.recurring,
			ix.rangeStart, ix.rangeEnd, ix.status, ix.transp, ix.class, ix.color, ix.organizer, w.Source, w.Actor, seq,
			remoteHref, remoteETag, dirtyArg, now, id); err != nil {
			return nil, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events_fts WHERE rowid = ?`, id); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events_fts (rowid, summary, location, description, attendees) VALUES (?, ?, ?, ?, ?)`,
		id, ix.summary, ix.location, ix.description, ix.attendeesText); err != nil {
		return nil, false, err
	}
	if err := writeAttendees(ctx, tx, id, ix); err != nil {
		return nil, false, err
	}
	for _, h := range hooks {
		if h != nil {
			if err := h(tx); err != nil {
				return nil, false, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	e, err := s.GetEvent(ctx, id)
	return e, created, err
}

func writeAttendees(ctx context.Context, tx *sql.Tx, eventID int64, ix *index) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM attendees WHERE event_id = ?`, eventID); err != nil {
		return err
	}
	put := func(a ical.Attendee, organizer bool) error {
		if a.Email == "" {
			return nil
		}
		var uid sql.NullInt64
		_ = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, a.Email).Scan(&uid)
		st := a.Status
		if st == "" {
			st = "NEEDS-ACTION"
		}
		if organizer {
			st = "ACCEPTED"
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO attendees (event_id, email, name, user_id, role, partstat, is_organizer) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (event_id, email) DO UPDATE SET is_organizer = MAX(is_organizer, excluded.is_organizer)`,
			eventID, a.Email, a.Name, uid, a.Role, st, organizer)
		return err
	}
	if ix.organizerA != nil {
		if err := put(*ix.organizerA, true); err != nil {
			return err
		}
	}
	for _, a := range ix.attendees {
		if err := put(a, false); err != nil {
			return err
		}
	}
	return nil
}

func safeHref(uid string) string {
	var b strings.Builder
	for _, r := range uid {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '@':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 || b.Len() > 200 {
		return RandomToken(16)
	}
	return b.String()
}

// DeleteEventOpts says how a delete happens.
type DeleteEventOpts struct {
	Actor      int64
	KeepCopy   bool // restorable from "Recently deleted"
	FromRemote bool // the remote calendar deleted it already
	IfMatch    string
}

// DeleteEvent removes a calendar object.
func (s *Store) DeleteEvent(ctx context.Context, e *EventRow, o DeleteEventOpts, hooks ...Hook) error {
	cal, err := s.GetCalendar(ctx, e.CalendarID)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if o.IfMatch != "" && o.IfMatch != "*" {
		var etag string
		if err := tx.QueryRowContext(ctx, `SELECT etag FROM events WHERE id = ?`, e.ID).Scan(&etag); err != nil || etag != o.IfMatch {
			return ErrPrecondition
		}
	}
	seq, err := s.BumpCalendar(ctx, tx, e.CalendarID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tombstones (calendar_id, href, seq) VALUES (?, ?, ?)`, e.CalendarID, e.Href, seq); err != nil {
		return err
	}
	if cal.AccountID != nil && !o.FromRemote && e.RemoteHref != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO remote_deletes (calendar_id, remote_href, remote_etag) VALUES (?, ?, ?)`, e.CalendarID, e.RemoteHref, e.RemoteETag); err != nil {
			return err
		}
	}
	if o.KeepCopy {
		uid := o.Actor
		if uid == 0 {
			uid = cal.UserID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deleted_events (user_id, calendar_id, uid, summary, dtstart, ics, deleted_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			uid, e.CalendarID, e.UID, e.Summary, e.DTStart, e.ICS, o.Actor); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events_fts WHERE rowid = ?`, e.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, e.ID); err != nil {
		return err
	}
	for _, h := range hooks {
		if h != nil {
			if err := h(tx); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ---------- CalDAV sync ----------

// Change is one resource changed (or gone) since a sync token.
type Change struct {
	Href    string
	ETag    string
	Deleted bool
}

// ChangesSince lists resources changed after seq (and those deleted).
func (s *Store) ChangesSince(ctx context.Context, calID, seq int64) ([]Change, error) {
	var out []Change
	rows, err := s.DB.QueryContext(ctx, `SELECT href, etag FROM events WHERE calendar_id = ? AND seq > ? ORDER BY seq`, calID, seq)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.Href, &c.ETag); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	rows, err = s.DB.QueryContext(ctx, `SELECT DISTINCT href FROM tombstones WHERE calendar_id = ? AND seq > ?
		AND href NOT IN (SELECT href FROM events WHERE calendar_id = ?)`, calID, seq, calID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.Href); err != nil {
			return nil, err
		}
		c.Deleted = true
		out = append(out, c)
	}
	return out, rows.Err()
}

// OldestTombstone is the lowest change number sync tokens can still be read from.
func (s *Store) OldestTombstone(ctx context.Context, calID int64) int64 {
	var n sql.NullInt64
	_ = s.DB.QueryRowContext(ctx, `SELECT MIN(seq) FROM tombstones WHERE calendar_id = ?`, calID).Scan(&n)
	return n.Int64
}

// ---------- remote mirror ----------

// DirtyEvents lists local changes the remote calendar has not seen.
func (s *Store) DirtyEvents(ctx context.Context, calID int64) ([]*EventRow, error) {
	return scanEvents(s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.calendar_id = ? AND e.dirty = 1`, calID))
}

// MarkPushed records that a local version reached the remote calendar;
// a change made meanwhile (seq moved on) stays dirty.
func (s *Store) MarkPushed(ctx context.Context, id, seq int64, remoteHref, remoteETag string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE events SET remote_href = ?, remote_etag = ?, dirty = CASE WHEN seq = ? THEN 0 ELSE dirty END WHERE id = ?`,
		remoteHref, remoteETag, seq, id)
	return err
}

// RemoteDelete is a delete waiting to reach the remote calendar.
type RemoteDelete struct {
	ID         int64
	RemoteHref string
	RemoteETag string
}

// RemoteDeletes lists them for a calendar.
func (s *Store) RemoteDeletes(ctx context.Context, calID int64) ([]RemoteDelete, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, remote_href, remote_etag FROM remote_deletes WHERE calendar_id = ?`, calID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RemoteDelete
	for rows.Next() {
		var d RemoteDelete
		if err := rows.Scan(&d.ID, &d.RemoteHref, &d.RemoteETag); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DoneRemoteDelete forgets a delete that reached the remote.
func (s *Store) DoneRemoteDelete(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM remote_deletes WHERE id = ?`, id)
	return err
}

// ---------- recently deleted ----------

// ListDeleted returns a person's restorable events.
func (s *Store) ListDeleted(ctx context.Context, userID int64) ([]DeletedEvent, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, calendar_id, uid, summary, dtstart, ics, deleted_at FROM deleted_events
		WHERE user_id = ? ORDER BY deleted_at DESC LIMIT 500`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeletedEvent{}
	for rows.Next() {
		var d DeletedEvent
		if err := rows.Scan(&d.ID, &d.CalendarID, &d.UID, &d.Summary, &d.DTStart, &d.ICS, &d.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDeleted returns one of a person's deleted events.
func (s *Store) GetDeleted(ctx context.Context, userID, id int64) (*DeletedEvent, error) {
	var d DeletedEvent
	err := s.DB.QueryRowContext(ctx, `SELECT id, calendar_id, uid, summary, dtstart, ics, deleted_at FROM deleted_events WHERE user_id = ? AND id = ?`, userID, id).
		Scan(&d.ID, &d.CalendarID, &d.UID, &d.Summary, &d.DTStart, &d.ICS, &d.DeletedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &d, nil
}

// ForgetDeleted drops a deleted event for good (or all of a person's when id is 0).
func (s *Store) ForgetDeleted(ctx context.Context, userID, id int64) error {
	if id == 0 {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM deleted_events WHERE user_id = ?`, userID)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM deleted_events WHERE user_id = ? AND id = ?`, userID, id)
	return err
}

// PurgeDeleted drops deleted events older than days, and tombstones older than 90 days.
func (s *Store) PurgeDeleted(ctx context.Context, days int) {
	cut := time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339Nano)
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM deleted_events WHERE deleted_at < ?`, cut)
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM alarm_log WHERE fired_at < ?`, time.Now().AddDate(0, 0, -60).UTC().Format(time.RFC3339Nano))
}

// ---------- attendees ----------

// AttendeeRow is an attendee with the account it belongs to.
type AttendeeRow struct {
	EventID     int64  `json:"event_id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	UserID      *int64 `json:"user_id"`
	Role        string `json:"role"`
	PartStat    string `json:"status"`
	IsOrganizer bool   `json:"organizer"`
}

// MyAttendance returns a person's attendee row of an event (nil when not invited).
func (s *Store) MyAttendance(ctx context.Context, eventID, userID int64) *AttendeeRow {
	var a AttendeeRow
	var uid sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT event_id, email, name, user_id, role, partstat, is_organizer FROM attendees WHERE event_id = ? AND user_id = ? ORDER BY is_organizer DESC LIMIT 1`, eventID, userID).
		Scan(&a.EventID, &a.Email, &a.Name, &uid, &a.Role, &a.PartStat, &a.IsOrganizer)
	if err != nil {
		return nil
	}
	if uid.Valid {
		a.UserID = &uid.Int64
	}
	return &a
}

// AddActivity logs one change for a person.
func (s *Store) AddActivity(ctx context.Context, userID int64, calID, eventID *int64, action, summary, detail string) {
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO activity (user_id, calendar_id, event_id, action, summary, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, nullInt(calID), nullInt(eventID), action, summary, detail)
}

// ListActivity returns a person's newest log entries.
func (s *Store) ListActivity(ctx context.Context, userID int64, limit int) ([]Activity, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, calendar_id, event_id, action, summary, detail, created_at FROM activity WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		var c, e sql.NullInt64
		if err := rows.Scan(&a.ID, &c, &e, &a.Action, &a.Summary, &a.Detail, &a.CreatedAt); err != nil {
			return nil, err
		}
		if c.Valid {
			a.CalendarID = &c.Int64
		}
		if e.Valid {
			a.EventID = &e.Int64
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PruneActivity keeps a person's newest n entries.
func (s *Store) PruneActivity(ctx context.Context, userID int64, keep int) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM activity WHERE user_id = ? AND id NOT IN (SELECT id FROM activity WHERE user_id = ? ORDER BY id DESC LIMIT ?)`, userID, userID, keep)
}

// AlarmFired records a reminder; false when it went out already.
func (s *Store) AlarmFired(ctx context.Context, userID, eventID int64, occurrence, trigger string) bool {
	res, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO alarm_log (user_id, event_id, occurrence, trigger) VALUES (?, ?, ?, ?)`, userID, eventID, occurrence, trigger)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// EventsWithAlarms returns events that carry a VALARM and may occur in the window.
func (s *Store) EventsWithAlarms(ctx context.Context, from, to time.Time) ([]*EventRow, error) {
	return scanEvents(s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.ics LIKE '%BEGIN:VALARM%'
		AND e.range_start < ? AND e.range_end > ?`, to.UTC().Format(RangeLayout), from.UTC().Format(RangeLayout)))
}

// ---------- accounts ----------

const accountCols = `a.id, a.user_id, a.kind, a.name, a.url, a.username, a.secret, a.oauth, a.via, a.sync_minutes, a.enabled,
	a.last_sync_at, a.last_error, a.created_at, a.updated_at, (SELECT COUNT(1) FROM calendars c WHERE c.account_id = a.id)`

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	if err := row.Scan(&a.ID, &a.UserID, &a.Kind, &a.Name, &a.URL, &a.Username, &a.Secret, &a.OAuth, &a.Via, &a.SyncMinutes,
		&a.Enabled, &a.LastSyncAt, &a.LastError, &a.CreatedAt, &a.UpdatedAt, &a.Calendars); err != nil {
		return nil, notFound(err)
	}
	a.HasSecret = a.Secret != "" || a.OAuth != ""
	return &a, nil
}

// ListAccounts returns a person's accounts (userID 0 = every account, for the sync loop).
func (s *Store) ListAccounts(ctx context.Context, userID int64) ([]*Account, error) {
	q := `SELECT ` + accountCols + ` FROM accounts a`
	var args []any
	if userID > 0 {
		q += ` WHERE a.user_id = ?`
		args = append(args, userID)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY a.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAccount returns one of a person's accounts.
func (s *Store) GetAccount(ctx context.Context, userID, id int64) (*Account, error) {
	return scanAccount(s.DB.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts a WHERE a.id = ? AND (a.user_id = ? OR ? = 0)`, id, userID, userID))
}

// SaveAccount inserts or updates an account.
func (s *Store) SaveAccount(ctx context.Context, a *Account) error {
	if a.SyncMinutes <= 0 {
		a.SyncMinutes = 15
	}
	if a.Via == "" {
		a.Via = "direct"
	}
	if a.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO accounts (user_id, kind, name, url, username, secret, oauth, via, sync_minutes, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.UserID, a.Kind, a.Name, a.URL, a.Username, a.Secret, a.OAuth, a.Via, a.SyncMinutes, a.Enabled)
		if err != nil {
			return err
		}
		a.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE accounts SET name = ?, url = ?, username = ?, secret = ?, oauth = ?, via = ?, sync_minutes = ?,
		enabled = ?, updated_at = ? WHERE id = ?`, a.Name, a.URL, a.Username, a.Secret, a.OAuth, a.Via, a.SyncMinutes, a.Enabled, Now(), a.ID)
	return err
}

// SetAccountSync records the outcome of a sync run.
func (s *Store) SetAccountSync(ctx context.Context, id int64, errText string) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE accounts SET last_sync_at = ?, last_error = ? WHERE id = ?`, Now(), errText, id)
}

// DeleteAccount removes an account with the calendars mirrored from it.
func (s *Store) DeleteAccount(ctx context.Context, userID, id int64) error {
	cals, _ := s.CalendarsOfAccount(ctx, id)
	for _, c := range cals {
		if c.UserID == userID {
			if err := s.DeleteCalendar(ctx, c.ID); err != nil {
				return err
			}
		}
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM accounts WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
