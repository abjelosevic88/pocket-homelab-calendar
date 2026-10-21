# CalDAV

Pocket Calendar is a CalDAV server: phones, Macs and desktop apps read and write your calendars directly, and every change goes through the same path as the web app (search index, invitations, reminders, activity log, Pocket Core events, push to a mirrored iCloud/Google/Nextcloud calendar).

## Connecting an app

| | |
|---|---|
| Server / URL | `https://calendar.example.com/dav/` (`/.well-known/caldav` redirects there) |
| User name | your CalDAV name (Settings → Profile; the part of your email before `@` by default). Your email works too. |
| Password | an API token from Settings → Tokens (`pcal_…`). With local accounts your account password also works, but a token per device can be revoked alone. |

Apps that assume Nextcloud can keep their Nextcloud settings and only change the host: `https://calendar.example.com/remote.php/dav/` with the calendar home at `/remote.php/dav/calendars/<name>/` and the principal at `/remote.php/dav/principals/users/<name>/`.

| App | How |
|---|---|
| iPhone / iPad Calendar | Settings → Apps → Calendar → Calendar Accounts → Add Account → Other → Add CalDAV Account. Server `calendar.example.com`, your CalDAV name and a token. If it cannot find the account, tap Advanced Settings and set the account URL to `https://calendar.example.com/dav/principals/<name>/` with SSL on, port 443. |
| macOS Calendar | System Settings → Internet Accounts → Add Other Account → CalDAV Account. Account type **Manual**, server address `https://calendar.example.com/dav/`. |
| Android (DAVx⁵) | + → Login with URL and user name. Base URL `https://calendar.example.com/dav/`, your CalDAV name and a token. Pick the calendars to sync; they show in any Android calendar app (Etar, Fossify, Google Calendar…). |
| Thunderbird | Calendar → New Calendar → On the Network. User name = your CalDAV name, location `https://calendar.example.com/dav/`, then pick the calendars it finds (it asks for the token as the password). |
| BusyCal / Fantastical / Evolution / GNOME Calendar | Add a CalDAV account with the `/dav/` address above. |

Apps that only subscribe to a feed (Google Calendar's "From URL", Outlook.com) use the webcal feed links from Settings instead; those are read-only.

## What you see

`/dav/calendars/<name>/` (your calendar home) holds:

| Collection | Path segment | Can you write? |
|---|---|---|
| Your own calendars | the calendar's `uid` (e.g. `personal`) | yes, unless it is a read-only subscription (ICS feed) |
| Calendars shared with you | `shared-<calendar id>`, named "Calendar (Owner)" | with the **write** or **manage** role. **read** shares are read-only and show other people's private events (`CLASS:PRIVATE` / `CONFIDENTIAL`) as "Busy", exactly like the web app. **freebusy** shares are read-only and show every event as a "Busy" block (times, repeats and transparency only; no title, place, notes, attendees or alarms). |
| Invitations | `invitations` | no. Events people on the box invited you to that are not in one of your calendars already (declined ones left out). Answer them in the web app. |

Each event is one `.ics` resource: the master `VEVENT` and its `RECURRENCE-ID` overrides, all with one UID. The server keeps the text the client sent, so `X-APPLE-*`, `VTIMEZONE`, alarms and the like survive.

A write into a read-only calendar answers `403`. Deleting a calendar someone shared with you from a phone **leaves** it (as "Leave" does on the web); deleting your last own calendar is refused.

Colour and order changed in an app are personal for shared calendars (they do not change the owner's calendar); name, description and time zone need the manage role.

## What the server speaks

* RFC 4791 calendar-access: `PROPFIND`, `PROPPATCH`, `MKCALENDAR` and extended `MKCOL` (RFC 5689), `GET`/`HEAD`, `PUT`, `DELETE`, `OPTIONS` (`DAV: 1, 3, calendar-access, extended-mkcol`).
* `REPORT calendar-query` with component filters, `time-range` (recurring series are expanded, so a weekly event matches a week months after its first date), `prop-filter` with `is-not-defined`, `text-match` and `time-range`; `calendar-multiget`; RFC 6578 `sync-collection` (tokens look like `http://pocket-calendar/sync/<n>`, deletions come back as `404` members).
* Conditional `PUT` / `DELETE` with `If-Match` and `If-None-Match: *` (`412` when the event changed meanwhile); the new `ETag` comes back on `PUT`.
* One UID per resource: an object with several UIDs is refused with `400` (`CALDAV:valid-calendar-object-resource`), a `VTODO`/`VJOURNAL` with `403` (`CALDAV:supported-calendar-component`), a UID that already lives under another name in the same calendar with `409` (`CALDAV:no-uid-conflict`).
* Properties: `current-user-principal`, `calendar-home-set`, `calendar-user-address-set` (`mailto:` your email), `displayname`, `current-user-privilege-set` (per role), `supported-calendar-component-set` (`VEVENT`), `calendar-description`, `calendar-timezone` / `calendar-timezone-id` (when the calendar has its own zone), Apple's `calendar-color` (`#RRGGBBFF`) and `calendar-order`, `getctag`, `sync-token`, `getetag`, `calendar-data`.

## Not supported

* Server-side scheduling (RFC 6638: schedule inbox/outbox, iMIP). Invitations to people on the box still work: an event with their email as `ATTENDEE` shows up in their Invitations and the web app notifies them. RSVP is done in the web app; the Invitations collection is read-only.
* `MOVE` / `COPY` (clients move an event between calendars with `DELETE` + `PUT`), `ACL`, `LOCK`, `free-busy-query`, and `calendar-data` with `<expand>` / `<limit-recurrence-set>` (the whole object is returned).
* `sync-collection` on Invitations only knows "nothing changed" or "start over" (a stale token answers `valid-sync-token`, and the client resyncs).
* A sync token older than the calendar's oldest remembered delete answers `valid-sync-token` too, and the client does one full resync.
* A web-created calendar whose uid is literally `invitations` or `shared-<n>` hides the virtual collection of that name in its owner's CalDAV home.
