// Left column: create, mini month, calendars, sources from other apps, invitations.
import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { type Wall, fmtDayShort, fmtTime, parseWall } from '../lib/dates'
import type { Calendar, Occurrence, OverlaySource } from '../lib/types'
import { CalendarEditDialog, ImportDialog, ShareDialog } from './CalendarDialogs'
import { isDefaultCal, makeDefaultPatch } from '../lib/model'
import { Icon } from './icons'
import { MiniMonth } from './MiniMonth'
import { ColorPalette } from './pickers'
import { type Anchor, type MenuItem, Confirm, Menu, Popover, copyText, download, errMsg, rectOf, useToast } from './ui'

export function Sidebar({ date, selected, range, today, onPick, onCreate, onOpenInvite, dots, onNavigate, sourceErrors = {} }: {
  date: Wall
  selected: Wall | null
  range: [Wall, Wall] | null
  today: Wall
  onPick: (d: Wall) => void
  onCreate: () => void
  onOpenInvite: (ev: Occurrence) => void
  dots?: Set<string>
  onNavigate?: () => void
  sourceErrors?: Record<string, string>
}) {
  const { calendars, settings, sources, invitations, status } = useApp()
  const [month, setMonth] = useState(date)
  const [lastDate, setLastDate] = useState(date)
  if (date !== lastDate) { setLastDate(date); setMonth(date) }
  const [dialog, setDialog] = useState<{ kind: 'edit' | 'share' | 'import' | 'new'; cal?: Calendar } | null>(null)
  const mine = calendars.filter(c => c.role === 'owner' && !c.account_id)
  const accounts = calendars.filter(c => c.role === 'owner' && c.account_id)
  const shared = calendars.filter(c => c.role !== 'owner')
  return (
    <aside className="sidebar" aria-label="Calendars">
      <div className="sb-brand">
        <img src="/favicon.svg" alt="" className="sb-logo" />
        <span className="sb-title">{status?.title && status.title !== 'Pocket Calendar' ? status.title : <>Pocket <b>Calendar</b></>}</span>
      </div>
      <button type="button" className="btn primary create-btn" onClick={onCreate}><Icon name="plus" size={17} strokeWidth={2.2} />Create<kbd className="kbd-on-primary">C</kbd></button>
      <MiniMonth month={month} onMonth={setMonth} selected={selected} range={range} today={today} weekStart={settings?.week_start ?? 1} onPick={d => { onPick(d); onNavigate?.() }} dots={dots} weekNumbers={settings?.show_week_numbers} />

      {invitations.length > 0 && (
        <section className="sb-sec">
          <h4 className="sb-h"><span>Invitations</span><span className="badge accent">{invitations.length}</span></h4>
          <ul className="sb-invites">
            {invitations.slice(0, 5).map(ev => (
              <li key={ev.id}><button type="button" className="sb-invite" onClick={() => { onOpenInvite(ev); onNavigate?.() }}>
                <span className="sb-inv-title ellipsis">{ev.title}</span>
                <span className="sb-inv-when">{fmtDayShort(parseWall(ev.start))}{!ev.all_day && ` · ${fmtTime(parseWall(ev.start), settings?.time_format === '12h', true)}`}{ev.organizer ? ` · ${ev.organizer.name || ev.organizer.email}` : ''}</span>
              </button></li>
            ))}
          </ul>
        </section>
      )}

      <CalGroup title="My calendars" cals={mine} onAction={(kind, cal) => setDialog({ kind, cal })} add={() => setDialog({ kind: 'new' })} />
      {accounts.length > 0 && <CalGroup title="Other calendars" cals={accounts} onAction={(kind, cal) => setDialog({ kind, cal })} />}
      {shared.length > 0 && <CalGroup title="Shared with me" cals={shared} onAction={(kind, cal) => setDialog({ kind, cal })} />}
      <SourcesGroup sources={sources} errors={sourceErrors} />

      <div className="sb-foot">
        <Link to="/settings" className="sb-foot-link"><Icon name="settings" size={15} />Settings</Link>
        <button type="button" className="sb-foot-link" onClick={() => window.dispatchEvent(new CustomEvent('pcal:shortcuts'))}><Icon name="keyboard" size={15} />Shortcuts</button>
      </div>
      {dialog?.kind === 'new' && <CalendarEditDialog onClose={() => setDialog(null)} />}
      {dialog?.kind === 'edit' && dialog.cal && <CalendarEditDialog cal={dialog.cal} onClose={() => setDialog(null)} />}
      {dialog?.kind === 'share' && dialog.cal && <ShareDialog cal={dialog.cal} onClose={() => setDialog(null)} />}
      {dialog?.kind === 'import' && dialog.cal && <ImportDialog cal={dialog.cal} onClose={() => setDialog(null)} />}
    </aside>
  )
}

function CalGroup({ title, cals, onAction, add }: { title: string; cals: Calendar[]; onAction: (k: 'edit' | 'share' | 'import', c: Calendar) => void; add?: () => void }) {
  const [open, setOpen] = useState(true)
  return (
    <section className="sb-sec">
      <h4 className="sb-h">
        <button type="button" className="sb-h-btn" aria-expanded={open} onClick={() => setOpen(o => !o)}><Icon name={open ? 'chevron-down' : 'chevron-right'} size={13} />{title}</button>
        {add && <button type="button" className="btn ghost icon-only xs" aria-label="New calendar" title="New calendar" onClick={add}><Icon name="plus" size={14} /></button>}
      </h4>
      {open && <ul className="cal-list">{cals.map(c => <CalRow key={c.id} cal={c} onAction={onAction} />)}</ul>}
    </section>
  )
}

function CalRow({ cal, onAction }: { cal: Calendar; onAction: (k: 'edit' | 'share' | 'import', c: Calendar) => void }) {
  const { patchCalendar, reloadCalendars, bump, calendars, settings, saveSettings } = useApp()
  const { toast } = useToast()
  const [menu, setMenu] = useState<Anchor | null>(null)
  const [colour, setColour] = useState<Anchor | null>(null)
  const [confirm, setConfirm] = useState<'delete' | 'leave' | null>(null)
  const canManage = cal.role === 'owner' || cal.role === 'manage'
  const toggle = () => patchCalendar(cal.id, { visible: !cal.visible }).catch(e => toast(errMsg(e), 'err'))
  const items: MenuItem[] = [
    { label: 'Display this only', icon: 'eye', onSelect: async () => { for (const c of calendars) if ((c.id === cal.id) !== c.visible) await patchCalendar(c.id, { visible: c.id === cal.id }).catch(() => {}) } },
    ...(canManage ? [{ label: 'Edit…', icon: 'edit', onSelect: () => onAction('edit', cal) }, { label: 'Share…', icon: 'share', onSelect: () => onAction('share', cal) }] : []),
    { label: 'Change colour…', icon: 'palette', onSelect: () => setColour(menu) },
    ...(cal.writable && !isDefaultCal(cal, calendars, settings) ? [{ label: 'Make default', icon: 'star', onSelect: () => (makeDefaultPatch(cal) === 'flag' ? patchCalendar(cal.id, { is_default: true }).then(() => saveSettings({ default_calendar_id: 0 })) : saveSettings({ default_calendar_id: cal.id })).then(() => toast(`New events go to “${cal.name}”`)).catch(e => toast(errMsg(e), 'err')) }] : []),
    { label: cal.notify ? 'Turn reminders off' : 'Turn reminders on', icon: cal.notify ? 'bell-off' : 'bell', onSelect: () => patchCalendar(cal.id, { notify: !cal.notify }).then(() => toast(cal.notify ? 'Reminders off for this calendar' : 'Reminders on')).catch(e => toast(errMsg(e), 'err')) },
    'sep',
    ...(cal.role !== 'freebusy' ? [{ label: 'Export .ics', icon: 'download', onSelect: () => download(`${V1}/calendars/${cal.id}/export.ics`) }] : []),
    ...(cal.writable ? [{ label: 'Import .ics…', icon: 'upload', onSelect: () => onAction('import', cal) }] : []),
    ...(cal.feed_url ? [{ label: 'Copy secret feed URL', icon: 'rss', onSelect: async () => toast(await copyText(cal.feed_url!) ? 'Feed URL copied — keep it private' : 'Could not copy') }] : []),
    ...(cal.caldav_url ? [{ label: 'Copy CalDAV URL', icon: 'link', onSelect: async () => toast(await copyText(cal.caldav_url!) ? 'CalDAV URL copied' : 'Could not copy') }] : []),
    'sep',
    ...(cal.role === 'owner' ? [{ label: 'Delete calendar…', icon: 'trash', danger: true, onSelect: () => setConfirm('delete') }] : [{ label: 'Remove from my list', icon: 'log-out', danger: true, onSelect: () => setConfirm('leave') }]),
  ]
  const doConfirm = async () => {
    const k = confirm
    setConfirm(null)
    try {
      if (k === 'delete') await api.del(`${V1}/calendars/${cal.id}`)
      else await api.post(`${V1}/calendars/${cal.id}/leave`)
      await reloadCalendars(); bump()
      toast(k === 'delete' ? `“${cal.name}” deleted` : `Removed “${cal.name}”`)
    } catch (e) { toast(errMsg(e), 'err') }
  }
  return (
    <li className={`cal-row ${cal.visible ? '' : 'off'}`} style={{ ['--c' as string]: cal.color }}>
      <button type="button" role="checkbox" aria-checked={cal.visible} className="cal-check" onClick={toggle} title={cal.visible ? 'Hide' : 'Show'}>
        <span className="cal-box">{cal.visible && <Icon name="check" size={11} strokeWidth={3.2} />}</span>
        <span className="cal-name ellipsis">{cal.name}</span>
        {cal.role !== 'owner' && <span className="cal-owner ellipsis">{(cal.owner?.name || cal.owner?.email || '').split(' ')[0]}</span>}
        {!cal.notify && <Icon name="bell-off" size={12} className="cal-flag" />}
        {(cal.shares?.length ?? 0) > 0 && <Icon name="users" size={12} className="cal-flag" />}
      </button>
      <button type="button" className="btn ghost icon-only xs cal-more" aria-label={`Options for ${cal.name}`} aria-haspopup="menu" onClick={e => setMenu(rectOf(e.currentTarget))}><Icon name="more" size={15} /></button>
      {menu && <Menu anchor={menu} items={items} onClose={() => setMenu(null)} align="left" width={220} />}
      {colour && (
        <Popover anchor={colour} prefer="below" width={252} onClose={() => setColour(null)} sheetOnMobile={false} label="Colour">
          <div className="colour-pop">
            <ColorPalette value={cal.color} onChange={c => { setColour(null); patchCalendar(cal.id, cal.role === 'owner' ? { color: c, my_color: c } : { my_color: c }).catch(e => toast(errMsg(e), 'err')) }} />
            {cal.role !== 'owner' && <p className="muted xsmall">Only you see this colour.</p>}
          </div>
        </Popover>
      )}
      {confirm && <Confirm title={confirm === 'delete' ? `Delete “${cal.name}”?` : `Remove “${cal.name}”?`}
        message={confirm === 'delete' ? <>All {cal.events} event{cal.events === 1 ? '' : 's'} in it are deleted for everyone it’s shared with. This can’t be undone — export it first if you might need it.</> : <>It stops showing up for you. {cal.owner?.name || 'The owner'} can share it again.</>}
        confirmLabel={confirm === 'delete' ? 'Delete calendar' : 'Remove'} onCancel={() => setConfirm(null)} onConfirm={doConfirm} />}
    </li>
  )
}

function SourcesGroup({ sources, errors }: { sources: OverlaySource[]; errors: Record<string, string> }) {
  const { setSources, bump } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const [open, setOpen] = useState(true)
  const [colour, setColour] = useState<{ src: OverlaySource; anchor: Anchor } | null>(null)
  if (!sources.length) return null
  const save = async (s: OverlaySource, patch: Partial<OverlaySource>) => {
    setSources(list => list.map(x => x.id === s.id ? { ...x, ...patch, ...(patch.mode === 'off' ? { status: 'off' } : patch.mode ? { status: x.status === 'off' ? 'ok' : x.status } : {}) } : x))
    try {
      await api.put(`${V1}/overlays/sources/${s.id}`, patch)
      setSources(await api.get<OverlaySource[]>(`${V1}/overlays/sources`))
      bump()
    } catch (e) { toast(errMsg(e), 'err') }
  }
  return (
    <section className="sb-sec">
      <h4 className="sb-h">
        <button type="button" className="sb-h-btn" aria-expanded={open} onClick={() => setOpen(o => !o)}><Icon name={open ? 'chevron-down' : 'chevron-right'} size={13} />From your apps</button>
        <Link to="/settings/apps" className="btn ghost icon-only xs" aria-label="Settings for apps" title="Settings"><Icon name="settings" size={13} /></Link>
      </h4>
      {open && (
        <ul className="cal-list">
          {sources.map(s => {
            const on = s.mode !== 'off'
            const color = s.color || s.default_color
            const err = on ? errors[s.id] || (s.status === 'error' ? s.detail || 'Not reachable' : '') : ''
            return (
              <li key={s.id} className={`cal-row src ${on ? '' : 'off'}`} style={{ ['--c' as string]: color }}>
                <button type="button" role="checkbox" aria-checked={on} className="cal-check" title={s.description}
                  onClick={() => save(s, { mode: on ? 'off' : (s.has_token && s.url ? 'direct' : 'core') })}>
                  <span className="cal-box round">{on && <Icon name="check" size={11} strokeWidth={3.2} />}</span>
                  <span className="cal-name ellipsis">{s.name}</span>
                  {err && <span className="src-warn" title={err} aria-label={`${s.name}: ${err}`}><Icon name="alert" size={13} /></span>}
                  {on && !err && s.status === 'ok' && <span className="src-ok" title={s.mode === 'direct' ? 'Connected directly' : 'Connected through Pocket Core'} />}
                </button>
                <button type="button" className="btn ghost icon-only xs cal-more" aria-label={`Colour of ${s.name}`} title="Colour" onClick={e => setColour({ src: s, anchor: rectOf(e.currentTarget) })}><Icon name="palette" size={13} /></button>
              </li>
            )
          })}
        </ul>
      )}
      {sources.some(s => s.mode !== 'off' && (s.status === 'error' || errors[s.id])) && open && (
        <button type="button" className="sb-src-note" onClick={() => navigate('/settings/apps')}>
          <Icon name="info" size={13} /><span>Some apps aren’t reachable. Connect them in Settings.</span>
        </button>
      )}
      {colour && (
        <Popover anchor={colour.anchor} prefer="below" width={252} onClose={() => setColour(null)} sheetOnMobile={false} label="Colour">
          <div className="colour-pop"><ColorPalette value={colour.src.color === colour.src.default_color ? '' : colour.src.color} allowNone noneLabel="Default colour" onChange={c => { save(colour.src, { color: c }); setColour(null) }} /></div>
        </Popover>
      )}
    </section>
  )
}
