# Configuration

Everything is an environment variable. Nothing is required: `docker run` with one volume works.

| Variable | Default | What it does |
|---|---|---|
| `PORT`, `HOST` | `8080`, `0.0.0.0` | Where to listen |
| `BASE_URL` | | The public address (`https://calendar.example.com`). Needed for links in reminders, the CalDAV and feed addresses shown in Settings, OIDC and Google callbacks |
| `DATA_DIR` | `./data` | Database, `secret.key`, backups |
| `DB_PATH` | `$DATA_DIR/pocket-calendar.db` | |
| `TZ` | `UTC` | The box's time zone; each person may pick their own in Settings |
| `LOG_LEVEL`, `LOG_FORMAT` | `info`, `text` | `debug` logs every request; `json` for log shippers |
| `TRUST_PROXY` | `true` | Read the client address from `X-Forwarded-For` |
| `METRICS_ENABLED` | `true` | `/metrics` for Prometheus |
| `AUTH_MODE` | `local` | `local` (accounts and passwords), `proxy` (trust `AUTH_PROXY_HEADER`), `none` (everyone is the admin; demos only) |
| `AUTH_PROXY_HEADER`, `AUTH_PROXY_EMAIL_HEADER` | `Remote-User`, `Remote-Email` | For `AUTH_MODE=proxy` |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD`, `ADMIN_NAME` | | Create the admin on first start instead of the setup screen |
| `SESSION_TTL` | `720h` | |
| `SECURE_COOKIES` | `false` | Set behind HTTPS |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_NAME`, `OIDC_AUTO_CREATE` | | Sign in with any OpenID Connect provider. Not needed with Pocket Core |
| `CORE_URL`, `CORE_REGISTRATION_KEY`, `CORE_APP_URL` | | Pocket Core: registration, sign-in, links to the other Pocket apps, the person's Nextcloud account, events |
| `NOTIFY_URL`, `NOTIFY_KIND` | | The owner's default route for reminders, invitations and the morning agenda: an Apprise route (`http://apprise:8000/notify/calendar`), an ntfy topic URL or a webhook; `apprise` / `ntfy` / `webhook`. Everyone else sets their own in Settings |
| `HOLIDAY_COUNTRY` | | ISO country code (`BA`, `DE`, `US`…) for the public holidays shown by default (from date.nager.at); each person can choose another |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | | Google Calendar accounts. Create an OAuth client of type *Web application* in the Google Cloud console, enable the *Google Calendar API* and the *CalDAV API*, and add `BASE_URL/api/v1/accounts/google/callback` as redirect URI |
| `DEMO_DATA` | `false` | Seed a made-up week into an empty box |

## Commands

```
pocket-calendar                       start
pocket-calendar backup [-o FILE]      consistent SQLite copy (VACUUM INTO)
pocket-calendar reset-password [-email USER] [-password NEW]
pocket-calendar version
```

## Files in the volume

- `pocket-calendar.db` (+ `-wal`, `-shm`): everything. Back it up with `pocket-calendar backup`, never by copying a live file.
- `secret.key`: seals the passwords and tokens of connected calendar accounts. Back it up with the database; without it those accounts must be connected again.
- `core.token`, `core.token.oidc.json`: the app's Pocket Core credentials.
- `backups/`: snapshots made by the backup command.
