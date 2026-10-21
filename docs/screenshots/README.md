# README screenshots

These are browser captures of the built app with its demo data. Desktop views are
1440 × 900; phone views are 390 × 844 at 2× scale. Dates follow the week the demo
database is created in. No live account or external service is used.

| | |
|---|---|
| `week.png`, `week-dark.png` | Week view: all-day lane with tasks and deadlines, a weekend trip, overlapping events, a free/busy-only calendar (hatched), invitations (dashed), the now line |
| `event.png`, `invitation.png` | Event details popover; an invitation with Yes / Maybe / No |
| `editor.png` | The full event editor |
| `quick-add.png` | Natural-language quick add with its live preview |
| `month.png`, `year.png`, `agenda.png` | Month grid, year heat map, agenda |
| `people.png` | Find a time: busy blocks of three people side by side, everyone-free stretches |
| `settings.png`, `settings-apps.png` | Settings → General and → From your apps |
| `mobile-week.png`, `mobile-agenda.png`, `mobile-event.png`, `mobile-dark.png` | Phone: 3-day view, agenda, event sheet, month in dark mode |

To refresh them, run from the repository root (Go, npm, Node 22+):

```sh
make web build
shots_data=$(mktemp -d)
HOST=127.0.0.1 PORT=18191 DATA_DIR="$shots_data" DEMO_DATA=true TZ=Europe/Sarajevo HOLIDAY_COUNTRY=BA \
  AUTH_MODE=local ADMIN_EMAIL=alex@example.com ADMIN_PASSWORD=demo-password ADMIN_NAME='Alex Petrović' \
  ./pocket-calendar
```

Local sign-in is used (not `AUTH_MODE=none`) because the script signs in as a few
demo people to give the People view and the invitations something to show.

Leave that running. In another terminal, start a headless Chrome with remote
debugging, for example:

```sh
docker run -d --rm --name pocket-calendar-screenshot-chrome --network host --shm-size=1g \
  zenika/alpine-chrome:124 --no-sandbox --headless --disable-gpu \
  --disable-dev-shm-usage --hide-scrollbars \
  --remote-debugging-port=19228 --remote-debugging-address=127.0.0.1 \
  --remote-allow-origins='*' about:blank
node scripts/screenshots.mjs
docker stop pocket-calendar-screenshot-chrome
```

(A Playwright `chrome-headless-shell` started with the same flags works too.)

`BASE` and `CHROME` override the demo and DevTools URLs; `ADMIN_EMAIL` and
`ADMIN_PASSWORD` the account the script signs in with. `FONT=/path/to/inter.woff2`
injects a web font so captures look the same on machines without Inter or the
system UI fonts (the app itself uses the system font stack).

What the script does, once per database: adds three people (two share free/busy,
one shares a calendar as free/busy only), a few invitations, overlapping events and
a multi-day event; serves a stand-in for Pocket Tasks, Travel, Garage, Invoicing and
Collaborator on `127.0.0.1:18199` and connects those sources directly, so the
overlays (tasks with checkboxes, deadlines, a trip, a service reminder, an invoice)
appear. It types a quick-add phrase without submitting it and opens the editor
without saving. Stop the demo server afterwards; the temporary database can be
discarded.
