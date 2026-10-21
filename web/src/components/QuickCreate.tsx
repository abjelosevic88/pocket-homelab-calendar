// The small "new event" card that opens on a clicked or dragged slot.
import { useState } from 'react'
import { useApp } from '../lib/app-context'
import { defaultCalendar } from '../lib/model'
import { type Wall, DAY, addDays, dayKey, localString, todayWall, whenText } from '../lib/dates'
import type { Attendee, EventBody } from '../lib/types'
import { Icon } from './icons'
import { CalendarSelect } from './pickers'
import { type Anchor, Avatar, Popover, Switch } from './ui'

export function QuickCreate({ anchor, start, end, allDay, attendees = [], onClose, onCreate, onMore }: {
  anchor: Anchor
  start: Wall
  end: Wall
  allDay: boolean
  attendees?: Attendee[]
  onClose: () => void
  onCreate: (body: EventBody) => Promise<boolean>
  onMore: (d: { title: string; start: Wall; end: Wall; allDay: boolean; calendarId: number; attendees: Attendee[] }) => void
}) {
  const { calendars, settings, h12, tz } = useApp()
  const def = defaultCalendar(calendars, settings)
  const [title, setTitle] = useState('')
  const [calId, setCalId] = useState(def?.id ?? 0)
  const [ad, setAd] = useState(allDay)
  const [busy, setBusy] = useState(false)
  // switching all-day off on a day: 09:00 + default duration
  const s = ad ? Math.floor(start / DAY) * DAY : allDay ? start + 9 * 3600000 : start
  const e = ad ? (allDay ? end : addDays(Math.floor(start / DAY) * DAY, 1)) : allDay ? s + (settings?.default_duration_min || 60) * 60000 : end
  const save = async () => {
    if (busy) return
    setBusy(true)
    const body: EventBody = { title: title.trim() || '(No title)', calendar_id: calId, all_day: ad, start: ad ? dayKey(s) : localString(s), end: ad ? dayKey(e) : localString(e) }
    if (!ad) body.tz = tz
    if (attendees.length) body.attendees = attendees
    const ok = await onCreate(body)
    setBusy(false)
    if (ok) onClose()
  }
  const cal = calendars.find(c => c.id === calId)
  return (
    <Popover anchor={anchor} onClose={onClose} width={360} label="New event" className="quick-create">
      <form className="qc" onSubmit={ev => { ev.preventDefault(); save() }}>
        <div className="qc-head">
          <span className="dot" style={{ background: cal?.color }} />
          <input autoFocus className="qc-title" value={title} onChange={ev => setTitle(ev.target.value)} placeholder="Add a title" aria-label="Title" />
        </div>
        <div className="qc-when"><Icon name="clock" size={14} /><span className="grow">{whenText(ad ? dayKey(s) : localString(s), ad ? dayKey(e) : localString(e), ad, h12, todayWall(tz))}</span>
          <label className="inline-switch small"><Switch size="sm" checked={ad} onChange={setAd} label="All day" />All day</label>
        </div>
        {attendees.length > 0 && <div className="qc-guests"><Icon name="users" size={14} />{attendees.map(a => <span key={a.email} className="qc-guest"><Avatar name={a.name} email={a.email} size={18} />{a.name || a.email}</span>)}</div>}
        <div className="qc-cal"><CalendarSelect value={calId} onChange={setCalId} /></div>
        <div className="qc-actions">
          <button type="button" className="btn ghost sm" onClick={() => onMore({ title, start: s, end: e, allDay: ad, calendarId: calId, attendees })}>More options</button>
          <span className="spacer" />
          <button type="submit" className="btn primary sm" disabled={busy || !calId}>Save</button>
        </div>
      </form>
    </Popover>
  )
}
