# Pocket Calendar and the other Pocket apps

Pocket Calendar talks to the other Pocket apps only through Pocket Core: **app links** (the calendar acts for a
person in another app with a 5-minute token, within scopes the person and the admin can switch off) and the
**event bus**. Nothing is copied: the calendar reads other apps live, as the person, so each person sees only
their own tasks, trips and invoices. Every source can also be connected *directly* with an API token pasted in
Settings → From your apps, for boxes without Core.

## Live in 0.1

| Source | Link scopes | On the calendar |
|---|---|---|
| **Pocket Tasks** | `tasks:read`, `tasks:write` | Every task with a *when* or a deadline on its day (timed ones at their time, as long as their estimate), finished tasks on the day they were done. Tick them off from the calendar; drag them to another day or time (that moves *when* or the deadline; resizing sets the estimate). On by default. |
| **Pocket Travel** | `trips:read` | Trips as all-day bars from the first to the last day. On by default. |
| **Pocket Garage** | `vehicles:read` | Each vehicle's next service or inspection on its due day (red when overdue). On by default. |
| **Pocket Invoicing** | `invoices:read` | Unpaid invoices on their due date with the amount still owed. On by default. |
| **Pocket Habits** | `habits:read` | Habits on the weekdays they are planned (off by default: busy on a month view). |
| **Pocket Collaborator** | `reviews:read` | Open document reviews on their deadline (off by default). |
| Public holidays | — | From date.nager.at for the country in Settings (`HOLIDAY_COUNTRY` by default). |
| **Nextcloud** (Core service) | `service:nextcloud` | Nextcloud calendars (and contact birthdays) mirrored and kept in sync both ways, with the person's own Nextcloud account from Core — no password typed into the calendar. |

The calendar offers two scopes to the other apps:

* `calendar:read` — calendars, events in a range (recurring events expanded), invitations, search.
* `calendar:write` — create events, and change or delete only the events that app created.

It publishes `calendar.created`, `calendar.updated`, `calendar.deleted`, `calendar.invited`, `calendar.rsvp` and
`calendar.reminder` on the bus, each attributed to the person (`user_email`), so `events:read` links work.

## Proposed next steps (each needs a small change in the other app)

1. **Day → Calendar (`calendar:read`).** The journal gets the day's events as a source next to tasks, trips and
   habits ("Lunch with Sani at Kod Keke, 13:00–14:30"). Day already has the source framework; it needs
   one new source and a `uses` line.
2. **Hub → Calendar (`calendar:read` + `events:read`).** A "Next up" card on the home screen (the next three
   events and the first pending invitation), and calendar activity in the Activity feed.
3. **Tasks → Calendar (`calendar:read`).** Time blocking in the Today view: show the day's events next to the
   tasks so free gaps are visible, and "Plan this task" drops it into the first free slot (Tasks writes its own
   *when*; the calendar already shows it).
4. **Travel → Calendar (`calendar:write`).** Flights, trains and stays (`transport.depart_at/arrive_at`,
   `lodging.check_in/check_out`) as timed events in a "Travel" calendar, with the trip's members invited — the
   trip bar stays an overlay, the bookings become real events that sync to phones.
5. **Kitchen: offer `plan:read`** (`GET /api/v1/plan?from&to`). The calendar shows the meal plan as a small
   "🍝 Dinner: Lasagne" chip in the evening; clicking opens the recipe.
6. **Money: offer `subscriptions:read`** (`GET /api/v1/subscriptions`, its `upcoming` list). Bills and
   subscriptions on their due dates with amounts — the monthly view becomes a cash calendar.
7. **Garage: open `GET /api/v1/reminders` and document expiry dates to `vehicles:read`.** All reminders, not just
   the next one, plus registration and insurance expiry; and Garage → Calendar (`calendar:write`) to put a booked
   service appointment on the calendar.
8. **Habits: open `GET /api/v1/months/{YYYY-MM}`** (and use `entries:write`). Planned habits with done/missed per
   day, ticked off from the calendar like tasks.
9. **Notes → Calendar meeting notes (`notes:write` from the calendar).** "Meeting notes" on an event creates (or
   opens) a note titled with the event, date and attendees, linked back to the event; the event shows the link.
10. **Inventory (`items:read`).** Food and medicine expiry dates and warranty ends as all-day reminders.
11. **Core: people directory for apps.** Invites and the share picker list only people who have signed in to the
    calendar once; a Core endpoint listing the household (name + email, audience-checked) would let you invite
    someone who hasn't opened the calendar yet. Today the calendar covers that with share-by-email, which binds
    on their first sign-in.
