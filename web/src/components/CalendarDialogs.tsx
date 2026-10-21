// Create/edit a calendar, share it, import an .ics into it.
import React, { useMemo, useRef, useState } from 'react'
import { api, upload, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { Calendar, Role, Share } from '../lib/types'
import { AlarmsEditor } from './AlarmsEditor'
import { Icon } from './icons'
import { ColorPalette, PALETTE, TimezoneSelect } from './pickers'
import { type Option, Select } from './Select'
import { Avatar, Confirm, CopyField, Field, Modal, SettingRow, Switch, errMsg, useToast } from './ui'

export function CalendarEditDialog({ cal, onClose }: { cal?: Calendar | null; onClose: () => void }) {
  const { reloadCalendars, bump, calendars } = useApp()
  const { toast } = useToast()
  const isNew = !cal
  const [name, setName] = useState(cal?.name || '')
  const [color, setColor] = useState(cal?.calendar_color || PALETTE[(calendars.filter(c => c.role === 'owner').length * 3) % PALETTE.length])
  const [description, setDescription] = useState(cal?.description || '')
  const [timezone, setTimezone] = useState(cal?.timezone || '')
  const [alarms, setAlarms] = useState<string[]>(cal?.default_alarms || [])
  const [isDefault, setIsDefault] = useState(!!cal?.is_default)
  const [busy, setBusy] = useState(false)
  const save = async (e?: React.FormEvent) => {
    e?.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    try {
      const body = { name: name.trim(), color, description, timezone, default_alarms: alarms, is_default: isDefault }
      if (isNew) await api.post(`${V1}/calendars`, body)
      else await api.put(`${V1}/calendars/${cal!.id}`, body)
      await reloadCalendars()
      bump()
      toast(isNew ? `“${name.trim()}” created` : 'Calendar saved')
      onClose()
    } catch (err) { toast(errMsg(err), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={isNew ? 'New calendar' : 'Edit calendar'} onClose={onClose} footer={<>
      <button className="btn" onClick={onClose}>Cancel</button>
      <button className="btn primary" disabled={busy || !name.trim()} onClick={() => save()}>{isNew ? 'Create calendar' : 'Save'}</button>
    </>}>
      <form className="stack" onSubmit={save}>
        <div className="cal-name-row">
          <span className="cal-name-dot" style={{ background: color }} />
          <input className="cal-name-input" autoFocus value={name} onChange={e => setName(e.target.value)} placeholder="Calendar name" aria-label="Name" />
        </div>
        <Field label="Colour" group><ColorPalette value={color} onChange={setColor} /></Field>
        <Field label="Description"><textarea rows={2} value={description} onChange={e => setDescription(e.target.value)} placeholder="What goes here? (optional)" /></Field>
        <Field label="Time zone" group help="New events in this calendar use it; leave it on your own zone unless the calendar belongs somewhere else.">
          <TimezoneSelect value={timezone} onChange={setTimezone} allowEmpty emptyLabel="Your time zone" />
        </Field>
        <Field label="Default reminders" group help="Used for new events in this calendar when you don’t set any.">
          <AlarmsEditor value={alarms} onChange={setAlarms} />
        </Field>
        <SettingRow title="Default calendar" help="New events go here unless you pick another.">
          <Switch checked={isDefault} onChange={setIsDefault} label="Default calendar" />
        </SettingRow>
        <button type="submit" hidden />
      </form>
    </Modal>
  )
}

export const ROLES: { value: Role; label: string; description: string }[] = [
  { value: 'freebusy', label: 'See only free/busy', description: 'Sees when you are busy — no titles or details.' },
  { value: 'read', label: 'See all event details', description: 'Sees every event; private ones show as busy.' },
  { value: 'write', label: 'Make changes to events', description: 'Adds, changes and deletes events.' },
  { value: 'manage', label: 'Make changes and manage sharing', description: 'Also renames the calendar and shares it with others.' },
]
const roleOptions = (canManage: boolean): Option<Role>[] => ROLES.map(r => ({ value: r.value, label: r.label, description: r.description, disabled: r.value === 'manage' && !canManage }))

export function ShareDialog({ cal, onClose }: { cal: Calendar; onClose: () => void }) {
  const { people, reloadCalendars, setCalendars } = useApp()
  const { toast } = useToast()
  const [shares, setShares] = useState<Share[]>(cal.shares || [])
  const [who, setWho] = useState<string>('')
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Role>('read')
  const [feedUrl, setFeedUrl] = useState(cal.feed_url || '')
  const [rotate, setRotate] = useState(false)
  const isOwner = cal.role === 'owner'
  const taken = new Set(shares.map(s => s.user_id ?? (s.everyone ? 'all' : s.email)))
  const whoOptions = useMemo<Option<string>[]>(() => [
    ...(!shares.some(s => s.everyone) ? [{ value: 'everyone', label: 'Everyone on this box', icon: <span className="who-ico"><Icon name="users" size={13} /></span> }] : []),
    ...people.filter(p => !p.me && p.id !== cal.owner?.id && !taken.has(p.id)).map(p => ({ value: String(p.id), label: p.name || p.email, description: p.email, keywords: p.email, icon: <Avatar name={p.name} email={p.email} size={20} /> })),
    { value: 'email', label: 'Someone else, by email…', icon: <span className="who-ico"><Icon name="mail" size={13} /></span> },
  ], [people, shares]) // eslint-disable-line react-hooks/exhaustive-deps

  const apply = (list: Share[]) => {
    setShares(list)
    setCalendars(cs => cs.map(c => c.id === cal.id ? { ...c, shares: list } : c))
  }
  const add = async () => {
    const body: Record<string, unknown> = { role }
    if (who === 'everyone') body.everyone = true
    else if (who === 'email') { if (!email.includes('@')) { toast('Type an email address', 'err'); return } body.email = email.trim() }
    else if (who) body.user_id = Number(who)
    else return
    try {
      apply(await api.post<Share[]>(`${V1}/calendars/${cal.id}/shares`, body))
      setWho(''); setEmail('')
      toast('Shared')
    } catch (e) { toast(errMsg(e), 'err') }
  }
  const change = async (s: Share, r: Role) => { try { apply(await api.put<Share[]>(`${V1}/shares/${s.id}`, { role: r })) } catch (e) { toast(errMsg(e), 'err') } }
  const remove = async (s: Share) => { try { apply(await api.del<Share[]>(`${V1}/shares/${s.id}`) || shares.filter(x => x.id !== s.id)) } catch (e) { toast(errMsg(e), 'err') } }
  const doRotate = async () => {
    setRotate(false)
    try { const c = await api.post<Calendar>(`${V1}/calendars/${cal.id}/feed/rotate`); setFeedUrl(c.feed_url || ''); reloadCalendars(); toast('The old address stopped working') } catch (e) { toast(errMsg(e), 'err') }
  }
  return (
    <Modal title={<span className="row"><span className="dot" style={{ background: cal.color }} />Share “{cal.name}”</span>} onClose={onClose} size="lg">
      <div className="stack loose">
        <section className="stack tight">
          <h3 className="share-h">Add people</h3>
          <div className="share-add">
            <div className="grow"><Select value={who} options={whoOptions} onChange={setWho} placeholder="Choose a person…" searchable label="Person" /></div>
            <div className="share-role"><Select value={role} options={roleOptions(isOwner)} onChange={setRole} label="Permission" width={300} /></div>
            <button className="btn primary" disabled={!who || (who === 'email' && !email)} onClick={add}>Share</button>
          </div>
          {who === 'email' && <input type="email" value={email} onChange={e => setEmail(e.target.value)} placeholder="name@example.com — they see it once they sign in with this address" autoFocus />}
        </section>
        <section className="stack tight">
          <h3 className="share-h">Shared with</h3>
          {shares.length === 0 && <p className="muted small">Only you can see this calendar.</p>}
          <div className="share-list">
            {shares.map(s => (
              <div key={s.id} className="share-row">
                {s.everyone ? <span className="who-ico lg"><Icon name="users" size={16} /></span> : <Avatar name={s.person?.name} email={s.person?.email || s.email} size={30} />}
                <div className="grow">
                  <div className="share-name">{s.everyone ? 'Everyone on this box' : s.person?.name || s.person?.email || s.email}{s.pending && <span className="badge warn" title="Shared by email with someone who has not signed in yet; it binds when they do.">Pending</span>}</div>
                  <div className="muted small">{s.everyone ? 'Anyone who signs in here' : s.person?.email || s.email}</div>
                </div>
                <div className="share-role"><Select value={s.role} options={roleOptions(isOwner)} onChange={r => change(s, r)} size="sm" label="Permission" width={300} /></div>
                <button className="btn ghost icon-only sm" aria-label="Stop sharing" title="Stop sharing" onClick={() => remove(s)}><Icon name="x" size={15} /></button>
              </div>
            ))}
          </div>
        </section>
        {feedUrl && (
          <section className="stack tight">
            <h3 className="share-h">Secret address in iCal format</h3>
            <p className="muted small">Subscribe from any calendar app. Anyone with this link can see all event details — don’t share it publicly.</p>
            <CopyField value={feedUrl} secret label="Secret feed address" />
            <div><button className="btn sm" onClick={() => setRotate(true)}><Icon name="refresh" size={13} />Reset the secret address</button></div>
          </section>
        )}
        {cal.caldav_url && (
          <section className="stack tight">
            <h3 className="share-h">CalDAV address</h3>
            <p className="muted small">For apps that sync (iPhone, macOS, DAVx⁵, Thunderbird): sign in with your CalDAV user name and an API token.</p>
            <CopyField value={cal.caldav_url} label="CalDAV address" />
          </section>
        )}
      </div>
      {rotate && <Confirm title="Reset the secret address?" message="Apps subscribed to the current address stop updating; you’ll need to give them the new one." confirmLabel="Reset" onCancel={() => setRotate(false)} onConfirm={doRotate} />}
    </Modal>
  )
}

export function ImportDialog({ cal, onClose }: { cal: Calendar; onClose: () => void }) {
  const { bump, reloadCalendars } = useApp()
  const { toast } = useToast()
  const [file, setFile] = useState<File | null>(null)
  const [busy, setBusy] = useState(false)
  const [over, setOver] = useState(false)
  const [result, setResult] = useState<{ imported: number; skipped: number } | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const go = async () => {
    if (!file) return
    setBusy(true)
    try {
      const r = await upload<{ imported: number; skipped: number }>(`${V1}/calendars/${cal.id}/import`, 'file', file)
      setResult(r)
      bump(); reloadCalendars()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={`Import into “${cal.name}”`} onClose={onClose} footer={result ? <button className="btn primary" onClick={onClose}>Done</button> : <>
      <button className="btn" onClick={onClose}>Cancel</button>
      <button className="btn primary" disabled={!file || busy} onClick={go}>{busy ? 'Importing…' : 'Import'}</button>
    </>}>
      {result ? (
        <div className="import-done"><span className="import-ok"><Icon name="check" size={20} /></span><div><b>{result.imported} event{result.imported === 1 ? '' : 's'} imported</b>{result.skipped > 0 && <div className="muted small">{result.skipped} skipped</div>}</div></div>
      ) : (
        <div className="stack">
          <button type="button" className={`dropzone ${over ? 'over' : ''} ${file ? 'has' : ''}`} onClick={() => input.current?.click()}
            onDragOver={e => { e.preventDefault(); setOver(true) }} onDragLeave={() => setOver(false)}
            onDrop={e => { e.preventDefault(); setOver(false); const f = e.dataTransfer.files[0]; if (f) setFile(f) }}>
            <Icon name="upload" size={22} />
            {file ? <><b>{file.name}</b><span className="muted small">{Math.round(file.size / 1024)} KB · click to choose another</span></> : <><b>Drop an .ics file here</b><span className="muted small">or click to choose one</span></>}
          </button>
          <input ref={input} type="file" accept=".ics,text/calendar" hidden onChange={e => setFile(e.target.files?.[0] || null)} />
          <p className="muted small">Events with the same UID as one already in this calendar replace it, so importing the same file twice is safe.</p>
        </div>
      )}
    </Modal>
  )
}
