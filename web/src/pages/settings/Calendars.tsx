import { useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { Calendar } from '../../lib/types'
import { isDefaultCal, makeDefaultPatch } from '../../lib/model'
import { CalendarEditDialog, ImportDialog, ShareDialog } from '../../components/CalendarDialogs'
import { Icon } from '../../components/icons'
import { Confirm, download, Empty, errMsg, Loading, MenuButton, type MenuItem, Switch, useAsync, useToast } from '../../components/ui'
import { ago, Section } from './common'

interface Deleted { id: number; calendar_id: number; uid: string; summary: string; dtstart: string; deleted_at: string }

const ROLE: Record<string, string> = { owner: 'Owner', manage: 'Can manage', write: 'Can edit', read: 'Can view', freebusy: 'Free/busy only' }
const KIND: Record<string, string> = { caldav: 'CalDAV', google: 'Google', ics: 'Subscription', nextcloud: 'Nextcloud', local: '' }

export function CalendarsTab() {
  const { calendars, user, patchCalendar, reloadCalendars, bump, settings, saveSettings } = useApp()
  const { toast } = useToast()
  const [edit, setEdit] = useState<Calendar | null | 'new'>(null)
  const [share, setShare] = useState<Calendar | null>(null)
  const [imp, setImp] = useState<Calendar | null>(null)
  const [del, setDel] = useState<Calendar | null>(null)
  const deleted = useAsync(() => api.get<Deleted[]>(`${V1}/deleted`), [])

  const mine = calendars.filter(c => c.role === 'owner' && !c.account_id)
  const shared = calendars.filter(c => c.role !== 'owner' && !c.account_id)
  const accounts = calendars.filter(c => !!c.account_id)
  const calName = new Map(calendars.map(c => [c.id, c]))

  const run = async (fn: () => Promise<unknown>, ok?: string) => { try { await fn(); if (ok) toast(ok) } catch (e) { toast(errMsg(e), 'err') } }

  const row = (c: Calendar) => {
    const manage = c.role === 'owner' || c.role === 'manage'
    const items: MenuItem[] = [
      { label: 'Edit…', icon: 'edit', disabled: !manage, onSelect: () => setEdit(c) },
      { label: 'Share…', icon: 'share', disabled: !manage, onSelect: () => setShare(c) },
      { label: 'Import .ics…', icon: 'upload', disabled: !c.writable, onSelect: () => setImp(c) },
      { label: 'Export .ics', icon: 'download', disabled: c.role === 'freebusy', onSelect: () => download(`${V1}/calendars/${c.id}/export.ics`) },
      { label: 'Make default', icon: 'star', disabled: !c.writable || isDefaultCal(c, calendars, settings), onSelect: () => run(() => makeDefaultPatch(c) === 'flag' ? patchCalendar(c.id, { is_default: true }).then(() => { saveSettings({ default_calendar_id: 0 }) }) : saveSettings({ default_calendar_id: c.id }).then(() => {}), `${c.name} is now the default`) },
      'sep',
      c.role === 'owner'
        ? { label: 'Delete calendar…', icon: 'trash', danger: true, disabled: mine.length <= 1 && !c.account_id, onSelect: () => setDel(c) }
        : { label: 'Leave calendar', icon: 'log-out', danger: true, onSelect: () => run(async () => { await api.post(`${V1}/calendars/${c.id}/leave`); await reloadCalendars(); bump() }, `You left ${c.name}`) },
    ]
    return (
      <div key={c.id} className="set-cal-row">
        <span className="set-cal-dot" style={{ background: c.color }} />
        <div className="grow">
          <div className="set-cal-name">
            <span className="ellipsis">{c.name}</span>
            {isDefaultCal(c, calendars, settings) && <span className="badge accent">Default</span>}
            {c.role !== 'owner' && <span className="badge">{ROLE[c.role]}</span>}
            {KIND[c.kind] && <span className="badge">{KIND[c.kind]}</span>}
            {c.read_only && c.role === 'owner' && <span className="badge">Read-only</span>}
            {(c.shares?.length || 0) > 0 && <span className="badge" title="Shared with others"><Icon name="users" size={11} />&nbsp;{c.shares!.length}</span>}
          </div>
          <div className="set-cal-meta">
            {c.role !== 'owner' ? `${c.owner?.name || c.owner?.email} · ` : ''}{c.events} event{c.events === 1 ? '' : 's'}{c.description ? ` · ${c.description}` : ''}
          </div>
        </div>
        <Switch size="sm" label={`Show ${c.name}`} checked={c.visible} onChange={v => run(() => patchCalendar(c.id, { visible: v }))} />
        {manage && <button type="button" className="btn ghost sm" onClick={() => setShare(c)}><Icon name="share" size={14} />Share</button>}
        <MenuButton label={`More for ${c.name}`} items={items} align="right"><Icon name="more" size={16} /></MenuButton>
      </div>
    )
  }

  return (
    <div className="stack loose">
      <Section title="My calendars" actions={<button className="btn primary sm" onClick={() => setEdit('new')}><Icon name="plus" size={14} />New calendar</button>}>
        <div className="set-cal-list">{mine.map(row)}</div>
      </Section>
      {shared.length > 0 && <Section title="Shared with me" sub="Calendars other people on this box share with you."><div className="set-cal-list">{shared.map(row)}</div></Section>}
      {accounts.length > 0 && <Section title="From accounts" sub="Mirrored from iCloud, Google, Nextcloud and other servers (Settings → Accounts)."><div className="set-cal-list">{accounts.map(row)}</div></Section>}

      <Section title="Recently deleted" sub="Whole events you deleted in the last 30 days. Restore puts them back where they were.">
        {deleted.loading && !deleted.data ? <Loading /> : !deleted.data?.length ? <Empty title="Nothing deleted lately" icon="trash" /> : (
          <div className="set-cal-list">
            {deleted.data.map(d => {
              const cal = calName.get(d.calendar_id)
              return (
                <div key={d.id} className="set-cal-row">
                  <span className="set-cal-dot" style={{ background: cal?.color || 'var(--faint)' }} />
                  <div className="grow">
                    <div className="set-cal-name"><span className="ellipsis">{d.summary || '(No title)'}</span></div>
                    <div className="set-cal-meta">{cal?.name || 'Calendar'} · deleted {ago(d.deleted_at)}</div>
                  </div>
                  <button className="btn sm" onClick={() => run(async () => { await api.post(`${V1}/deleted/${d.id}/restore`); deleted.reload(); bump() }, 'Restored')}><Icon name="undo" size={14} />Restore</button>
                  <button className="btn ghost sm" onClick={() => run(async () => { await api.del(`${V1}/deleted/${d.id}`); deleted.reload() })}>Forget</button>
                </div>
              )
            })}
          </div>
        )}
      </Section>

      <Section title="Export">
        <div className="row wrap">
          <button className="btn" onClick={() => download(`${V1}/export.ics`)}><Icon name="download" size={15} />Export all my calendars (.ics)</button>
          {user?.role === 'admin' && <button className="btn" onClick={() => download(`${V1}/backup`)}><Icon name="server" size={15} />Download database backup</button>}
        </div>
        {user?.role === 'admin' && <p className="muted small mt-s">The database backup only works while you are the only person on this box; otherwise back it up on the server.</p>}
      </Section>

      {edit && <CalendarEditDialog cal={edit === 'new' ? null : edit} onClose={() => { setEdit(null); reloadCalendars() }} />}
      {share && <ShareDialog cal={share} onClose={() => { setShare(null); reloadCalendars() }} />}
      {imp && <ImportDialog cal={imp} onClose={() => { setImp(null); reloadCalendars() }} />}
      {del && <Confirm title={`Delete “${del.name}”?`} message={<>All {del.events} event{del.events === 1 ? '' : 's'} in it are deleted for everyone it is shared with. This can’t be undone.</>}
        onCancel={() => setDel(null)} onConfirm={() => { const c = del; setDel(null); run(async () => { await api.del(`${V1}/calendars/${c.id}`); await reloadCalendars(); bump() }, 'Calendar deleted') }} />}
    </div>
  )
}
