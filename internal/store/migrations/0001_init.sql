CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL DEFAULT '',
  username      TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'admin',
  feed_token    TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT
);
CREATE UNIQUE INDEX users_username ON users(username) WHERE username <> '';
CREATE UNIQUE INDEX users_feed_token ON users(feed_token) WHERE feed_token <> '';

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Box-wide settings (title, setup_complete, seq). Everything a person
-- chooses is in user_settings under their id.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);
CREATE TABLE user_settings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, key)
);

-- An external calendar account: CalDAV (iCloud, Fastmail, Nextcloud,
-- Radicale…), Google (OAuth + Google's CalDAV) or a read-only ICS feed.
-- Secrets are AES-GCM sealed with DATA_DIR/secret.key.
CREATE TABLE accounts (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL,              -- caldav | google | ics | nextcloud
  name          TEXT NOT NULL DEFAULT '',
  url           TEXT NOT NULL DEFAULT '',
  username      TEXT NOT NULL DEFAULT '',
  secret        TEXT NOT NULL DEFAULT '',   -- sealed password / app password
  oauth         TEXT NOT NULL DEFAULT '',   -- sealed JSON {access_token, refresh_token, expiry}
  via           TEXT NOT NULL DEFAULT 'direct', -- direct | core (Nextcloud through Pocket Core)
  sync_minutes  INTEGER NOT NULL DEFAULT 15,
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_sync_at  TEXT NOT NULL DEFAULT '',
  last_error    TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX accounts_user ON accounts(user_id);

-- A calendar is a CalDAV collection of VEVENTs (uid = its path segment).
-- kind = local, or the account kind when it mirrors a remote calendar.
CREATE TABLE calendars (
  id             INTEGER PRIMARY KEY,
  user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  uid            TEXT NOT NULL,
  name           TEXT NOT NULL,
  color          TEXT NOT NULL DEFAULT '',
  description    TEXT NOT NULL DEFAULT '',
  timezone       TEXT NOT NULL DEFAULT '',
  kind           TEXT NOT NULL DEFAULT 'local',
  account_id     INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
  remote_href    TEXT NOT NULL DEFAULT '',
  remote_ctag    TEXT NOT NULL DEFAULT '',
  remote_token   TEXT NOT NULL DEFAULT '',
  read_only      INTEGER NOT NULL DEFAULT 0,
  is_default     INTEGER NOT NULL DEFAULT 0,
  default_alarms TEXT NOT NULL DEFAULT '',  -- JSON ["-PT10M"] applied to new timed events
  feed_token     TEXT NOT NULL DEFAULT '',
  public         INTEGER NOT NULL DEFAULT 0, -- feed readable without a login
  sort_order     REAL NOT NULL DEFAULT 0,
  seq            INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (user_id, uid)
);
CREATE INDEX calendars_account ON calendars(account_id);
CREATE UNIQUE INDEX calendars_feed ON calendars(feed_token) WHERE feed_token <> '';

-- Who else sees a calendar: one person, someone invited by email who has
-- not signed in yet (bound on first sign-in), or everyone on the box.
-- role: freebusy (busy blocks only) | read | write | manage.
CREATE TABLE calendar_shares (
  id          INTEGER PRIMARY KEY,
  calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  user_id     INTEGER REFERENCES users(id) ON DELETE CASCADE,
  email       TEXT NOT NULL DEFAULT '',
  everyone    INTEGER NOT NULL DEFAULT 0,
  role        TEXT NOT NULL DEFAULT 'read',
  created_by  INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX shares_user ON calendar_shares(calendar_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX shares_email ON calendar_shares(calendar_id, email) WHERE user_id IS NULL AND email <> '';
CREATE UNIQUE INDEX shares_everyone ON calendar_shares(calendar_id) WHERE everyone = 1;
CREATE INDEX shares_grantee ON calendar_shares(user_id);

-- A person's own view of a calendar they own or that is shared with them.
CREATE TABLE calendar_prefs (
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  visible     INTEGER NOT NULL DEFAULT 1,
  color       TEXT NOT NULL DEFAULT '',
  notify      INTEGER NOT NULL DEFAULT 1,
  sort_order  REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, calendar_id)
);

-- An event is one .ics resource: the master VEVENT plus its overrides
-- (RECURRENCE-ID). ics is the source of truth; the columns are the index.
-- range_start/range_end (UTC) cover every occurrence of the series.
CREATE TABLE events (
  id           INTEGER PRIMARY KEY,
  calendar_id  INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  uid          TEXT NOT NULL,
  href         TEXT NOT NULL,
  etag         TEXT NOT NULL DEFAULT '',
  ics          TEXT NOT NULL DEFAULT '',
  summary      TEXT NOT NULL DEFAULT '',
  location     TEXT NOT NULL DEFAULT '',
  description  TEXT NOT NULL DEFAULT '',
  all_day      INTEGER NOT NULL DEFAULT 0,
  dtstart      TEXT NOT NULL DEFAULT '',
  dtend        TEXT NOT NULL DEFAULT '',
  tzid         TEXT NOT NULL DEFAULT '',
  rrule        TEXT NOT NULL DEFAULT '',
  recurring    INTEGER NOT NULL DEFAULT 0,
  range_start  TEXT NOT NULL DEFAULT '',
  range_end    TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'confirmed',
  transp       TEXT NOT NULL DEFAULT 'opaque',
  class        TEXT NOT NULL DEFAULT 'public',
  color        TEXT NOT NULL DEFAULT '',
  organizer    TEXT NOT NULL DEFAULT '',
  source       TEXT NOT NULL DEFAULT '',   -- web | caldav | remote | import | <app slug>
  created_by   INTEGER NOT NULL DEFAULT 0,
  updated_by   INTEGER NOT NULL DEFAULT 0,
  seq          INTEGER NOT NULL DEFAULT 0,
  remote_href  TEXT NOT NULL DEFAULT '',
  remote_etag  TEXT NOT NULL DEFAULT '',
  dirty        INTEGER NOT NULL DEFAULT 0, -- local change not yet pushed to the remote calendar
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX events_href ON events(calendar_id, href);
CREATE INDEX events_uid ON events(calendar_id, uid);
CREATE INDEX events_range ON events(calendar_id, range_start, range_end);
CREATE INDEX events_seq ON events(calendar_id, seq);
CREATE INDEX events_dirty ON events(calendar_id) WHERE dirty = 1;
CREATE INDEX events_remote ON events(calendar_id, remote_href) WHERE remote_href <> '';

CREATE VIRTUAL TABLE events_fts USING fts5(summary, location, description, attendees, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 2');

-- Where a resource used to be, for CalDAV sync-collection after a delete or a move.
CREATE TABLE tombstones (
  id          INTEGER PRIMARY KEY,
  calendar_id INTEGER NOT NULL,
  href        TEXT NOT NULL,
  seq         INTEGER NOT NULL
);
CREATE INDEX tombstones_cal ON tombstones(calendar_id, seq);

-- Deletes the remote calendar has not heard of yet.
CREATE TABLE remote_deletes (
  id          INTEGER PRIMARY KEY,
  calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  remote_href TEXT NOT NULL,
  remote_etag TEXT NOT NULL DEFAULT ''
);

-- Recently deleted events, restorable for 30 days.
CREATE TABLE deleted_events (
  id          INTEGER PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  uid         TEXT NOT NULL,
  summary     TEXT NOT NULL DEFAULT '',
  dtstart     TEXT NOT NULL DEFAULT '',
  ics         TEXT NOT NULL,
  deleted_by  INTEGER NOT NULL DEFAULT 0,
  deleted_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX deleted_user ON deleted_events(user_id, deleted_at);

-- Attendees per event (organizer included), matched to people on the box
-- by email so their invitations show up in their own calendar.
CREATE TABLE attendees (
  event_id     INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  email        TEXT NOT NULL,
  name         TEXT NOT NULL DEFAULT '',
  user_id      INTEGER REFERENCES users(id) ON DELETE SET NULL,
  role         TEXT NOT NULL DEFAULT 'REQ-PARTICIPANT',
  partstat     TEXT NOT NULL DEFAULT 'NEEDS-ACTION',
  is_organizer INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (event_id, email)
);
CREATE INDEX attendees_user ON attendees(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX attendees_email ON attendees(email);

-- Which reminders already went out (per person, occurrence and trigger).
CREATE TABLE alarm_log (
  user_id    INTEGER NOT NULL,
  event_id   INTEGER NOT NULL,
  occurrence TEXT NOT NULL,
  trigger    TEXT NOT NULL,
  fired_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (user_id, event_id, occurrence, trigger)
);

CREATE TABLE activity (
  id          INTEGER PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  calendar_id INTEGER,
  event_id    INTEGER,
  action      TEXT NOT NULL,
  summary     TEXT NOT NULL DEFAULT '',
  detail      TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX activity_user ON activity(user_id, id);
