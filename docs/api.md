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
  "kind": "task|due|done|trip|service|invoice|habit|review|holiday",
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

## Accounts (other calendars)

| Route | |
|---|---|
| `GET /accounts` | `[{id, kind: caldav\|google\|ics\|nextcloud, name, url, username, via, sync_minutes, enabled, last_sync_at, last_error, has_secret, calendars}]` |
| `POST /accounts` `{kind, name?, url, username?, password?, via?: direct\|core}` | Add and sync. `nextcloud` with `via: core` needs no address or password. `ics` = a read-only subscription. |
| `POST /accounts/test` (same body) | Try without saving → `{ok, calendars: [{name, color, href}]}` |
| `PUT /accounts/{id}`, `DELETE /accounts/{id}` | Deleting removes the mirrored calendars here (nothing is deleted at the provider). |
| `POST /accounts/{id}/sync` | Sync now. |
| `GET /accounts/google/start` | Browser redirect to Google; comes back to `/settings/accounts`. Needs `GOOGLE_CLIENT_ID`. |

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
(invoices:read), `habits` (habits:read), `collaborator` (reviews:read), and the `nextcloud` service.
