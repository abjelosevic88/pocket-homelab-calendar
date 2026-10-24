# Pocket Calendar API

JSON over HTTP under `/api/v1`. Authenticate with the session cookie (web app), `Authorization: Bearer pcal_…`
(an API token from Settings → Tokens), a Pocket Core token (`pcd_`/`pcu_`/`pco_`) or a Pocket Core link token
(`pcx_`, only the routes of the link's scope). Errors are `{"error": "…"}` with a 4xx/5xx status. Other people's
ids answer 404.

The web app sends `X-Pocket-Source: web`; every change records its source (`web`, `api`, `caldav`, `remote`,
`import`, or the slug of the Pocket app acting through a link).

## Dates

* All-day values are `YYYY-MM-DD`; the **end is exclusive** (a one-day event on the 21st ends on the 22nd).
* Timed values come back as RFC 3339 in the viewer's zone (`2026-10-21T13:00:00+02:00`). They are accepted as
  RFC 3339 or as a local `YYYY-MM-DDTHH:MM[:SS]` in the event's `tz` (default: the person's zone).
* `tz` (query) overrides the person's zone for one request (the web app sends the browser's zone only when the
  person did not choose one in Settings).

## Occurrences

`GET /events?from=&to=[&calendars=1,2][&tz=][&invites=0]` → `{events: Occurrence[], from, to, tz}`.
Recurring events are expanded; at most 400 days per request. Without `calendars` the visible ones are used.
Events the person is invited to (as an attendee, from someone else's calendar) are included with `invite`
unless `invites=0`.

```jsonc
{
  "id": "12:20261021T071500Z",   // "<event_id>" or "<event_id>:<key>" for one occurrence of a series
  "event_id": 12, "key": "20261021T071500Z", "uid": "…@pocket-calendar", "calendar_id": 3,
  "title": "Standup", "description": "", "location": "", "url": "", "conference": "https://meet.…",
  "start": "2026-10-21T09:15:00+02:00", "end": "2026-10-21T09:30:00+02:00", "all_day": false, "tz": "Europe/Sarajevo",
  "status": "confirmed|tentative|cancelled", "busy": true, "private": false, "color": "",   // own colour, "" = calendar's
  "categories": [], "recurring": true, "rrule": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", "repeat_text": "Every weekday",
  "override": false,              // this occurrence was edited on its own
  "alarms": [{"trigger": "-PT10M"}],   // relative to the start, or an absolute RFC 3339 UTC moment
  "organizer": {"email": "…", "name": "…"},
  "attendees": [{"email": "…", "name": "…", "role": "REQ-PARTICIPANT", "status": "ACCEPTED|DECLINED|TENTATIVE|NEEDS-ACTION", "user_id": 2, "me": false}],
  "invite": {"status": "NEEDS-ACTION"},   // only when shown because the person is invited
  "can_edit": true,
  "masked": false,                // true: free/busy only — title "Busy", no details
  "source": "web", "created_by": {"id": 1, "name": "…", "email": "…"}, "updated_at": "…", "etag": "\"…\""
}
```

`GET /events/{id}[?key=]` (`id` may be the occurrence id) → `{event: Occurrence, role, calendar: {id, name, color, owner}}`.

### Create

`POST /events` with an event body → the first Occurrence (201).

```jsonc
{
  "calendar_id": 3,          // default: the person's default calendar
  "title": "Lunch", "description": "", "location": "", "url": "", "conference": "",
  "all_day": false, "start": "2026-10-23T13:00", "end": "2026-10-23T14:30", "tz": "Europe/Sarajevo",
  "rrule": "FREQ=WEEKLY;BYDAY=FR",   // "" = does not repeat
  "status": "confirmed", "busy": true, "private": false, "color": "#f97316", "categories": [],
  "alarms": [{"trigger": "-PT30M"}], // absent: the calendar's / person's defaults
  "attendees": [{"email": "sani@example.com", "name": "Sani"}]   // the person becomes organizer
}
```

### Update

`PUT /events/{id}[?key=][&scope=this|following|all]` with any subset of the create body → the edited Occurrence.

* `scope=this` (default when a key is given) changes one occurrence (an override).
* `scope=following` ends the series before this occurrence and starts a new series from it with the change
  (the response is the new series' first occurrence).
* `scope=all` changes the series. Moving the start of one occurrence with `all` moves the whole series by the
  same amount (and drops its exceptions).
* `calendar_id` moves the whole series to another calendar (needs write access there).
* Send `If-Match: <etag>` to refuse when someone changed the event meanwhile (412).

### Delete

`DELETE /events/{id}[?key=][&scope=this|following|all]` → `{ok, restore_id}`. A whole delete can be undone:
`POST /deleted/{restore_id}/restore`. `GET /deleted` lists the last 30 days, `DELETE /deleted/{id}` forgets one.

### More

| Route | |
|---|---|
| `POST /events/quick` `{text, calendar_id?, preview?, tz?}` | Plain words → event. `preview: true` only reads: `{parsed: {title, start, end, all_day, rrule, repeat_text, location, calendar_id, calendar, alarms, attendees, recognized[]}}`; otherwise also `{event}` (201). Understands dates (today, tomorrow, fri, next mon, 21.10., oct 21, 2026-10-21, in 3 days), times (15:00, 3pm, 9-10:30, from 2 to 4pm, noon), `for 90m`, `at <place>`, `#calendar`, `every weekday / every 2 weeks / every mon and wed / daily`, `remind 30m`, `with Sani` (invites people on the box), `private`. |
| `POST /events/{id}/rsvp` `{status: accepted\|declined\|tentative, key?, scope?: this\|all}` | Answer an invitation. |
| `POST /events/{id}/duplicate` `{calendar_id?}` | Copy a series (without attendees). |
| `GET /events/{id}/ics` | The calendar object as `.ics`. |
| `GET /invitations` | Invitations waiting for an answer (next occurrence of each). |
| `GET /search?q=` | Events by words (title, place, notes, attendees): the next occurrence of each. |
| `GET /freebusy?users=1,2&from=&to=` | `{busy: {"1": [{start, end, all_day}]}, tz}` — only for people who share a calendar with you or chose to show free/busy to the box. |
| `GET /people` | Everyone on the box `[{id, name, email, me, freebusy}]` for share and invite pickers. |
| `GET /activity` | The person's change log. |
| `GET /changes` | `{stamp}` — changes whenever anything was written; the web app polls it. |

## Calendars

`GET /calendars` → calendars the person owns or that are shared with them:

```jsonc
{
  "id": 3, "uid": "family", "name": "Family", "color": "#10b981",   // the viewer's colour (own override or the calendar's)
  "calendar_color": "#10b981", "description": "", "timezone": "", "kind": "local|caldav|google|ics|nextcloud",
  "account_id": null, "read_only": false, "writable": true, "is_default": false, "default_alarms": ["-PT10M"],
  "role": "owner|manage|write|read|freebusy", "visible": true, "notify": true,
  "owner": {"id": 1, "name": "…", "email": "…"}, "events": 42,
  "feed_url": "https://…/feed/cal/<secret>.ics", "caldav_url": "https://…/dav/calendars/<user>/<uid>/",  // owner/manage only
  "shares": [Share]                                                                            // owner/manage only
}
```

| Route | |
|---|---|
| `POST /calendars` `{name, color?, description?, timezone?, default_alarms?, is_default?}` | Create. |
| `PUT /calendars/{id}` | Calendar fields (owner/manage) and/or the viewer's own `visible`, `notify` (reminders), `my_color`, `sort_order`, `is_default`. |
| `DELETE /calendars/{id}` | Owner only; not the last one. |
| `POST /calendars/reorder` `{ids: []}` | The viewer's order. |
| `POST /calendars/{id}/leave` | Stop seeing a calendar shared with you. |
| `POST /calendars/{id}/feed/rotate` | New secret feed address. |
| `GET /calendars/{id}/export.ics`, `POST /calendars/{id}/import` (file, `{ics}` or text/calendar) | → `{imported, skipped}` |

### Sharing

Share = `{id, calendar_id, user_id, email, everyone, role, person: {id, name, email}, pending, created_at}`.
Roles: `freebusy` (sees only busy blocks), `read` (sees events; private ones as busy), `write` (adds and changes
events), `manage` (also renames and shares). `pending` = shared by email with someone who has not signed in yet;
it binds when they do.

| Route | |
|---|---|
| `GET /calendars/{id}/shares` | |
| `POST /calendars/{id}/shares` `{user_id \| email \| everyone: true, role}` | → the share list |
| `PUT /shares/{sid}` `{role}` / `DELETE /shares/{sid}` | → the share list |

## From your other apps (overlays)

Dated things from other Pocket apps, read live as the person through Pocket Core app links (or a pasted token),
plus public holidays.

`GET /overlays?from=&to=[&sources=tasks,travel][&tz=]` → `{items: OverlayItem[], errors: {source: message}}`

```jsonc
{
  "id": "tasks:12:task:2026-10-21", "source": "tasks", "ref": "12",
  "kind": "task|due|done|trip|service|invoice|habit|review|holiday|birthday|anniversary",
  "title": "Pay rent", "subtitle": "Home", "start": "2026-10-21" | RFC3339, "end": "…", "all_day": true,
  "color": "#2563eb", "url": "https://tasks…/today?task=12", "done": false, "overdue": false,
  "actions": ["complete", "reschedule"], "meta": {…}
}
```

| Route | |
|---|---|
| `GET /overlays/sources` | `[{id, name, app, description, mode: core\|direct\|off, status: ok\|error\|off, detail, color, default_color, url, has_token, pocket, actions}]` |
| `PUT /overlays/sources/{id}` `{mode?, color?, url?, token?}` | Switch a source, recolour it, or connect it directly. |
| `POST /overlays/tasks/{ref}/complete` `{done: true\|false}` | Tick a task in Pocket Tasks. |
| `PUT /overlays/tasks/{ref}` `{kind: task\|due, start, end?, all_day}` | Reschedule a task dragged on the grid (start or deadline; `end` sets its estimate). |

Birthdays (`source: "birthdays"`, Pocket Contacts) are all-day items titled `🎂 Marko Petrović (38)` or
`💍 Ana Kovač (12)` (the age only when the year is known), `ref` = the contact id, `url` = its page in Contacts,
`meta: {age?, original}`. A contact born on 29 February shows on 28 February in other years.

### Pocket Contacts (guests)

Reached the way the *birthdays* source is set up (Core, Direct or off); off or unreachable answers empty, never
an error.

| Route | |
|---|---|
| `GET /contacts/suggest?q=&limit=8` | `[{id, name, email, photo_url, url}]` — guests from Contacts for the event editor, one row per email (2+ characters). `photo_url` is this app's proxy (below) or `""`; `url` the contact's page. |
| `GET /contacts/lookup?email=a@x,b@y` | `{"a@x": {id, name, url, photo_url}}` — which guests are contacts (lower-cased emails, up to 30; unknown ones left out). |
| `GET /contacts/{id}/photo?s=64` | The contact's thumbnail, fetched from Contacts as the person. 404 when there is none. |

## Accounts (other calendars)

Every other calendar account a person has, mirrored here: each remote calendar becomes a calendar of kind
`caldav`, `nextcloud`, `google` or `ics` (with `account_id` set) that works like any other — shown on the grid,
shareable, in the CalDAV server and the feeds. Accounts are strictly per person; other people's ids answer 404.

| kind | address | sign-in |
|---|---|---|
| `caldav` | Any CalDAV server: `https://caldav.icloud.com`, `https://caldav.fastmail.com`, `https://dav.mailbox.org`, `https://calendar.zoho.com`, Radicale, Baïkal, Synology, a Nextcloud's `…/remote.php/dav` | user name + password (iCloud and Fastmail: an app-specific password) |
| `nextcloud` | The Nextcloud address (`https://cloud.example.com`; `/remote.php/dav` is added) | `via: direct` with user name + app password, or `via: core` — the login Pocket Core holds for the person (service `nextcloud`), no address or password here |
| `google` | — | Google's sign-in (`GET /accounts/google/start`) |
| `ics` | Any `.ics` / `webcal://` address: Outlook/Office 365 published calendars, Google's "secret address in iCal format", fixtures, school terms | none (the address is the secret) — read-only |

Discovery follows RFC 6764 (the address, `/.well-known/caldav`, the server root → current-user-principal →
calendar-home-set). Every calendar that holds events is mirrored; ones the server marks read-only (no write
privilege, Google `reader`, Nextcloud's `contact_birthdays`) are read-only here too. Calendars added over there
appear on the next sync, removed ones go. A rename over there carries over unless it was renamed here; a
mirrored calendar deleted here is not mirrored again (see `restore_calendars`).

Sync: every `sync_minutes` (default 15, `ics` 60, 5…1440), right after the account is added or changed, on
`POST /accounts/{id}/sync`, and local changes (web app, API, CalDAV clients) are pushed within ~20 seconds.
Pull = sync-collection with the stored token when the server supports it, else a listing of etags; changed
events are fetched with calendar-multiget in batches of 50. Push = `PUT` with `If-Match` (new events
`If-None-Match: *`), deletes with `If-Match`. When an event changed on both sides the newer `LAST-MODIFIED`
wins; an event deleted over there that was changed here meanwhile is kept and sent back as new. `ics` feeds are
fetched with `If-None-Match`/`If-Modified-Since`, compared per UID (ignoring `DTSTAMP`), and events that left the
feed are removed.

Passwords and Google tokens are sealed (AES-256-GCM) with `DATA_DIR/secret.key` (made on first start, mode
0600) and never returned. Back the key up with the database; without it, passwords must be entered again.

Account:

```jsonc
{
  "id": 4, "kind": "caldav|google|ics|nextcloud", "name": "iCloud", "url": "https://caldav.icloud.com",
  "username": "me@icloud.com", "via": "direct|core", "sync_minutes": 15, "enabled": true,
  "last_sync_at": "2026-10-21T08:15:00Z", "last_error": "",   // "" = the last sync went through
  "has_secret": true, "syncing": false,
  "calendars": 3, "calendar_list": [{"id": 12, "name": "Home", "color": "#34aadc", "read_only": false}],
  "created_at": "…", "updated_at": "…"
}
```

| Route | |
|---|---|
| `GET /accounts` | The person's accounts. |
| `GET /accounts/{id}` | One account. |
| `POST /accounts` `{kind, name?, url, username?, password?, via?: direct\|core, sync_minutes?, enabled?}` | Connects first (a wrong password or address answers 400 with the reason and nothing is saved), mirrors the calendars at once (an `ics` feed with its events) and syncs the events in the background → the account (201). `name` defaults to the provider (iCloud, Fastmail, Nextcloud…) or the feed's `X-WR-CALNAME`. The same account twice → 409. `kind: google` → 400 (use the Google route). |
| `POST /accounts/test` (same body) | Connects without saving → `{ok: true, name, calendars: [{name, color, href, read_only}]}` or `{ok: false, error, calendars: []}`. |
| `PUT /accounts/{id}` `{name?, url?, username?, password?, via?, sync_minutes?, enabled?, restore_calendars?}` | A changed address, user name, password or `via` is tried before it is stored (400 when it fails). An empty `password` keeps the stored one. `restore_calendars: true` mirrors calendars deleted here again. The kind cannot change. |
| `DELETE /accounts/{id}` | Removes the account and its calendars here (nothing is deleted at the provider) → 204. |
| `POST /accounts/{id}/sync[?wait=1]` | Sync now → the account (`syncing: true`); with `wait=1` the sync runs before the answer. |
| `GET /accounts/google/start[?account=<id>][&json=1]` | Browser redirect to Google's consent page (calendar scope, offline access); `account` reconnects an existing Google account, `json=1` answers `{url}` instead. Without `GOOGLE_CLIENT_ID`/`GOOGLE_CLIENT_SECRET` → 400 with how to create the OAuth client. |
| `GET /accounts/google/callback` | Where Google sends the browser back (no API auth; the person comes from the session cookie and must be the one who started). Stores the account (name = the Google email) and goes to `/settings?tab=accounts&connected=<id>`, or `/settings?tab=accounts&error=<message>`. |

Google setup (once per box): in the Google Cloud console create a project, enable the **Google Calendar API**
and the **CalDAV API**, configure the OAuth consent screen (publish it — in Testing, Google drops the sign-in
after 7 days), create an OAuth client ID of type **Web application** with the redirect URI
`BASE_URL/api/v1/accounts/google/callback`, then set `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET`. Calendars are
listed with the Calendar API (`calendarList`; free/busy-only ones are skipped) and their events go through
Google's CalDAV endpoint (`https://apidata.googleusercontent.com/caldav/v2/<calendarId>/events/`) with the
access token, refreshed automatically.

## Settings

`GET /settings` → `{settings, effective_tz, box_tz, box_holidays, notify_has_token, notify_box_route, google_configured, core}`.
`PUT /settings` accepts any of: `title` (admin), `timezone`, `week_start` (0/1/6), `time_format` (24h/12h),
`default_view` (day/3day/week/month/year/agenda/people), `default_calendar_id`, `default_duration_min`,
`default_alarms` / `all_day_alarms` (`["-PT10M"]`), `work_start`, `work_end`, `work_days` (`[1,2,3,4,5]`, 0 = Sunday),
`show_week_numbers`, `show_declined`, `show_weekends`, `density` (comfortable/compact), `holiday_country` (ISO code),
`freebusy_shared`, `invites_color`, `notify_kind` (apprise/ntfy/webhook), `notify_url`, `notify_token`,
`notify_alarms`, `notify_invites`, `digest_time` (`"07:30"` or `""`).

`POST /notify/test`, `GET /settings/system`, `GET /feeds` (`{all_url, all_webcal, caldav_url, caldav_user, principal}`),
`POST /feeds/rotate`, `GET /export.ics` (all own calendars), `GET /backup` (admin, single-person box only).

Users (`GET/POST /users`, `PUT/DELETE /users/{id}`, admin) and tokens (`GET/POST /tokens`, `DELETE /tokens/{id}`)
work like the other Pocket apps. Auth: `GET /auth/status`, `POST /auth/login`, `POST /auth/setup`,
`POST /auth/logout`, `GET/PUT /auth/me`, `GET /auth/oidc/start?next=`.

## Feeds and CalDAV

* `GET /feed/cal/<secret>.ics` — one calendar; `GET /feed/user/<secret>.ics` — everything a person sees.
* CalDAV at `/dav/` (and the Nextcloud-shaped `/remote.php/dav/`), `/.well-known/caldav` redirects there.
  User name = the CalDAV name (Settings → Profile), password = an API token or the account password.
  See [caldav.md](caldav.md).

## Pocket Core

Registers as slug `calendar` (env prefix `CALENDAR`). Publishes `calendar.created`, `calendar.updated`,
`calendar.deleted` (owner's `user_email`), `calendar.invited` (the invitee's), `calendar.rsvp`, `calendar.reminder`.

Offers to other Pocket apps:

* `calendar:read` — `GET /calendars`, `/events`, `/events/{id}`, `/events/{id}/ics`, `/invitations`, `/search`, `/changes`
* `calendar:write` — `POST /events`, `/events/quick`, `PUT`/`DELETE /events/{id}` — an app may change or delete
  only events it created (`source` = its slug).

Uses: `tasks` (tasks:read, tasks:write), `travel` (trips:read), `garage` (vehicles:read), `invoicing`
(invoices:read), `habits` (habits:read), `collaborator` (reviews:read), `contacts` (contacts:read), `health`
(health:read: workouts and nights as an overlay), and the `nextcloud` service.
