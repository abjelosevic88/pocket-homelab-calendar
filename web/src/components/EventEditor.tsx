// The full event editor: a dialog on desktop, a full-screen sheet on phones.
import React, { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { defaultCalendar } from '../lib/model'
import { type Wall, DAY, MIN, addDays, dayKey, instantToWall, parseWall, wallToInstant, zoneLabel } from '../lib/dates'
import type { Attendee, EventBody, Occurrence, Scope } from '../lib/types'
import { AlarmsEditor } from './AlarmsEditor'
import { AttendeesEditor } from './AttendeesEditor'
import { Icon } from './icons'
import { CalendarSelect, ColorPalette, DatePicker, TimePicker, TimezoneSelect } from './pickers'
import { RepeatPicker } from './RepeatPicker'
import { ScopeDialog } from './ScopeDialog'
import { Modal, Segmented, Switch, useIsMobile, useToast } from './ui'

export interface EditorDraft {
  ev?: Occurrence            // editing an existing occurrence
  title?: string
  start: Wall                // in the viewer's zone
  end: Wall
  allDay: boolean
  calendarId?: number
  attendees?: Attendee[]
  description?: string
  location?: string
}

interface Form {
  title: string
  allDay: boolean
  startDate: string
  startMin: number
  endDate: string            // inclusive for all-day
  endMin: number
  tz: string
  rrule: string
  calendarId: number
  color: string
  location: string
  conference: string
  description: string
  busy: boolean
  private: boolean
  alarms: string[]
  attendees: Attendee[]
}

const mins = (w: Wall) => Math.round((w - Math.floor(w / DAY) * DAY) / MIN)

export function EventEditor({ draft, onClose, onSave }: { draft: EditorDraft; onClose: () => void; onSave: (body: EventBody, scope: Scope | null, ev?: Occurrence) => Promise<boolean> }) {
  const { settings, calendars, tz, user, people } = useApp()
  const { toast } = useToast()
  const mobile = useIsMobile()
  const navigate = useNavigate()
  const ev = draft.ev
  const defaultCal = defaultCalendar(calendars, settings)
  const initial = useMemo<Form>(() => {
    const evTz = ev && !ev.all_day && ev.tz ? ev.tz : tz
    const conv = (w: Wall) => (evTz === tz ? w : instantToWall(wallToInstant(w, tz), evTz))
    const s = draft.allDay ? draft.start : conv(draft.start)
    const e = draft.allDay ? draft.end : conv(draft.end)
    const calId = ev?.calendar_id ?? draft.calendarId ?? defaultCal?.id ?? 0
    const cal = calendars.find(c => c.id === calId)
    const defAlarms = draft.allDay ? settings?.all_day_alarms || [] : (cal?.default_alarms?.length ? cal.default_alarms : settings?.default_alarms || [])
    return {
      title: ev?.title ?? draft.title ?? '',
      allDay: draft.allDay,
      startDate: dayKey(s), startMin: draft.allDay ? 9 * 60 : mins(s),
      endDate: draft.allDay ? dayKey(addDays(e, -1)) : dayKey(e), endMin: draft.allDay ? 10 * 60 : mins(e),
      tz: evTz,
      rrule: ev?.rrule || '',
      calendarId: calId,
      color: ev?.color || '',
      location: ev?.location ?? draft.location ?? '',
      conference: ev?.conference || '',
      description: ev?.description ?? draft.description ?? '',
      busy: ev ? ev.busy : true,
      private: ev ? ev.private : false,
      alarms: ev ? (ev.alarms || []).map(a => a.trigger) : defAlarms,
      attendees: ev?.attendees ?? draft.attendees ?? [],
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const [f, setF] = useState<Form>(initial)
  const [alarmsTouched, setAlarmsTouched] = useState(!!ev)
  const [showTz, setShowTz] = useState(initial.tz !== tz)
  const [busy, setBusy] = useState(false)
  const [scopeAsk, setScopeAsk] = useState<EventBody | null>(null)
  const set = (p: Partial<Form>) => setF(x => ({ ...x, ...p }))

  // derived
  const startW = parseWall(f.startDate) + (f.allDay ? 0 : f.startMin * MIN)
  const endW = f.allDay ? addDays(parseWall(f.endDate), 1) : parseWall(f.endDate) + f.endMin * MIN
  const invalid = !f.allDay ? endW <= startW : parseWall(f.endDate) < parseWall(f.startDate)
  const multiDay = f.startDate !== f.endDate
  const cal = calendars.find(c => c.id === f.calendarId)

  // start moves → end keeps the duration
  const moveStart = (date: string, min: number) => {
    const dur = endW - startW
    const ns = parseWall(date) + (f.allDay ? 0 : min * MIN)
    const ne = ns + (dur > 0 ? dur : (f.allDay ? DAY : (settings?.default_duration_min || 60) * MIN))
    if (f.allDay) set({ startDate: date, endDate: dayKey(addDays(ne, -1)) })
    else set({ startDate: date, startMin: min, endDate: dayKey(ne), endMin: mins(ne) })
  }
  const setEndTime = (m: number) => {
    // the end list starts at the start time; values ≥ 1440 roll to the next day
    if (m >= 1440) set({ endDate: dayKey(addDays(parseWall(f.startDate), Math.floor(m / 1440))), endMin: m % 1440 })
    else if (f.endDate === f.startDate && m <= f.startMin) set({ endDate: dayKey(addDays(parseWall(f.startDate), 1)), endMin: m })
    else set({ endMin: m })
  }
  useEffect(() => {
    if (alarmsTouched) return
    const c = calendars.find(x => x.id === f.calendarId)
    set({ alarms: f.allDay ? settings?.all_day_alarms || [] : (c?.default_alarms?.length ? c.default_alarms : settings?.default_alarms || []) })
  }, [f.calendarId, f.allDay]) // eslint-disable-line react-hooks/exhaustive-deps

  const body = (): EventBody => {
    const b: EventBody = {
      calendar_id: f.calendarId, title: f.title.trim(), description: f.description, location: f.location.trim(), conference: f.conference.trim(),
      all_day: f.allDay, rrule: f.rrule, busy: f.busy, private: f.private, color: f.color,
      alarms: f.alarms.map(t => ({ trigger: t })), attendees: f.attendees.map(a => ({ email: a.email, name: a.name })),
    }
    if (f.allDay) { b.start = f.startDate; b.end = dayKey(addDays(parseWall(f.endDate), 1)) }
    else {
      b.start = `${f.startDate}T${String(Math.floor(f.startMin / 60)).padStart(2, '0')}:${String(f.startMin % 60).padStart(2, '0')}`
      b.end = `${f.endDate}T${String(Math.floor(f.endMin / 60)).padStart(2, '0')}:${String(f.endMin % 60).padStart(2, '0')}`
      b.tz = f.tz
    }
    return b
  }
  const save = async () => {
    if (invalid) { toast('The end is before the start', 'err'); return }
    if (!f.calendarId) { toast('Choose a calendar', 'err'); return }
    const b = body()
    if (ev?.recurring) { setScopeAsk(b); return }
    await doSave(b, null)
  }
  const doSave = async (b: EventBody, scope: Scope | null) => {
    setScopeAsk(null)
    // an occurrence can't carry its own rule: only send it for series edits
    if (ev && scope === 'this') delete b.rrule
    if (ev && (b.rrule ?? '') === (ev.rrule ?? '') && scope !== null) delete b.rrule
    setBusy(true)
    const ok = await onSave(b, scope, ev)
    setBusy(false)
    if (ok) onClose()
  }
  useEffect(() => {
    const k = (e: KeyboardEvent) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); save() } }
    window.addEventListener('keydown', k)
    return () => window.removeEventListener('keydown', k)
  })

  const ruleChanged = !!ev && (f.rrule || '') !== (ev.rrule || '')
  const isOrganizer = !ev?.organizer || ev.organizer.email === user?.email
  const findTime = () => {
    const ids = f.attendees.map(a => a.user_id ?? people.find(p => p.email.toLowerCase() === a.email.toLowerCase())?.id ?? 0).filter(Boolean)
    onClose()
    navigate(`/people/${f.startDate}${ids.length ? `?with=${ids.join(',')}` : ''}`)
  }

  return (
    <Modal title={ev ? 'Edit event' : 'New event'} onClose={onClose} size="lg" className={`editor ${mobile ? 'full-mobile' : ''}`} footer={<>
      {!mobile && <span className="muted small editor-hint"><kbd>⌘</kbd><kbd>↵</kbd> to save</span>}
      <span className="spacer" />
      <button className="btn" onClick={onClose}>Cancel</button>
      <button className="btn primary" onClick={save} disabled={busy || invalid}>{busy ? 'Saving…' : 'Save'}</button>
    </>}>
      <div className="editor-body">
        <input className="editor-title" autoFocus={!ev} value={f.title} onChange={e => set({ title: e.target.value })} placeholder="Add a title" aria-label="Title"
          onKeyDown={e => { if (e.key === 'Enter' && !e.nativeEvent.isComposing) { e.preventDefault(); save() } }} />

        <div className="ed-row">
          <span className="ed-ico"><Icon name="clock" size={17} /></span>
          <div className="ed-main">
            <div className="when-row">
              <DatePicker value={f.startDate} onChange={d => moveStart(d, f.startMin)} label="Start date" />
              {!f.allDay && <TimePicker value={f.startMin} onChange={m => moveStart(f.startDate, Math.min(m, 1439))} label="Start time" />}
              <span className="when-dash">–</span>
              {!f.allDay && <TimePicker value={f.endDate === f.startDate ? f.endMin : f.endMin + 1440 * Math.max(0, Math.round((parseWall(f.endDate) - parseWall(f.startDate)) / DAY))} from={f.startMin} onChange={setEndTime} label="End time" />}
              {(f.allDay || multiDay) && <DatePicker value={f.endDate} onChange={d => set({ endDate: d < f.startDate ? f.startDate : d })} min={f.startDate} label="End date" />}
            </div>
            {invalid && <div className="ed-error">The event ends before it starts.</div>}
            <div className="when-opts">
              <label className="inline-switch"><Switch size="sm" checked={f.allDay} onChange={v => set({ allDay: v, ...(v ? { endDate: f.endDate < f.startDate ? f.startDate : f.endDate } : { endDate: f.startDate, startMin: f.startMin || 9 * 60, endMin: Math.min(1439, (f.startMin || 9 * 60) + (settings?.default_duration_min || 60)) }) })} label="All day" /> All day</label>
              {!f.allDay && (showTz
                ? <div className="tz-pick"><TimezoneSelect value={f.tz} onChange={z => set({ tz: z })} /></div>
                : <button type="button" className="link-btn small" onClick={() => setShowTz(true)}>{zoneLabel(f.tz)}</button>)}
            </div>
            <div className="repeat-row">
              <Icon name="repeat" size={14} className="muted" />
              <RepeatPicker value={f.rrule} start={startW} onChange={r => set({ rrule: r })} weekStart={settings?.week_start ?? 1} />
            </div>
          </div>
        </div>

        <div className="ed-row">
          <span className="ed-ico"><span className="dot lg" style={{ background: f.color || cal?.color || '#888' }} /></span>
          <div className="ed-main row wrap">
            <div className="ed-cal"><CalendarSelect value={f.calendarId} onChange={id => set({ calendarId: id })} /></div>
            <details className="color-over">
              <summary className="btn ghost sm"><Icon name="palette" size={14} />{f.color ? 'Own colour' : 'Colour'}</summary>
              <div className="color-over-pop"><ColorPalette value={f.color} onChange={c => set({ color: c })} allowNone noneLabel="Use the calendar’s colour" /></div>
            </details>
          </div>
        </div>

        <div className="ed-row">
          <span className="ed-ico"><Icon name="pin" size={17} /></span>
          <div className="ed-main"><input value={f.location} onChange={e => set({ location: e.target.value })} placeholder="Add a place" aria-label="Location" /></div>
        </div>

        <div className="ed-row">
          <span className="ed-ico"><Icon name="video" size={17} /></span>
          <div className="ed-main row">
            <input value={f.conference} onChange={e => set({ conference: e.target.value })} placeholder="Video call link" aria-label="Video call link" />
            {!f.conference && <button type="button" className="btn sm" onClick={() => set({ conference: `https://meet.jit.si/pocket-${Math.random().toString(36).slice(2, 10)}` })}>Add Jitsi</button>}
          </div>
        </div>

        <div className="ed-row">
          <span className="ed-ico"><Icon name="users" size={17} /></span>
          <div className="ed-main">
            <AttendeesEditor value={f.attendees} onChange={a => set({ attendees: a })} organizer={f.attendees.length ? ev?.organizer || (user ? { email: user.email, name: user.name } : undefined) : undefined} />
            <button type="button" className="btn ghost sm find-time" onClick={findTime}><Icon name="search" size={13} />Find a time</button>
            {!isOrganizer && <p className="muted small">You’re not the organizer — your changes only affect your copy.</p>}
          </div>
        </div>

        <div className="ed-row">
          <span className="ed-ico"><Icon name="bell" size={17} /></span>
          <div className="ed-main"><AlarmsEditor value={f.alarms} onChange={a => { setAlarmsTouched(true); set({ alarms: a }) }} allDay={f.allDay} /></div>
        </div>

        <div className="ed-row">
          <span className="ed-ico"><Icon name="text" size={17} /></span>
          <div className="ed-main"><textarea rows={mobile ? 3 : 4} value={f.description} onChange={e => set({ description: e.target.value })} placeholder="Add a description" aria-label="Description" /></div>
        </div>

        <div className="ed-row">
          <span className="ed-ico"><Icon name="eye" size={17} /></span>
          <div className="ed-main row wrap">
            <Segmented size="sm" value={f.busy ? 'busy' : 'free'} onChange={v => set({ busy: v === 'busy' })} options={[{ value: 'busy', label: 'Busy' }, { value: 'free', label: 'Free' }]} label="Show as" />
            <label className="inline-switch" title="People you share the calendar with see only “busy”"><Switch size="sm" checked={f.private} onChange={v => set({ private: v })} label="Private" /> Private</label>
            <span className="muted small">{f.private ? 'Others see only “busy”.' : 'Visible to people you share with.'}</span>
          </div>
        </div>
      </div>
      {scopeAsk && <ScopeDialog verb="Change" allowThis={!ruleChanged} onCancel={() => setScopeAsk(null)} onPick={s => doSave({ ...scopeAsk }, s)} />}
    </Modal>
  )
}
