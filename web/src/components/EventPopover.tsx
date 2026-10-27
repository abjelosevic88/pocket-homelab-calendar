// Event details (popover on desktop, bottom sheet on phones) and the small
// card for items from other Pocket apps.
import React, { useState } from 'react'
import { useApp } from '../lib/app-context'
import { useContactRefs, useContactsOn } from '../lib/contacts'
import { alarmLabel, parseWall, todayWall, whenText, zoneLabel } from '../lib/dates'
import { type Item, myStatus } from '../lib/model'
import type { Scope } from '../lib/types'
import { StatusPill } from './AttendeesEditor'
import { Icon, OVERLAY_ICON } from './icons'
import { type Anchor, Avatar, MenuButton, Popover, Segmented } from './ui'

export function Linkify({ text }: { text: string }) {
  const parts = text.split(/(https?:\/\/[^\s<>"')\]]+)/g)
  return <>{parts.map((p, i) => (i % 2 ? <a key={i} href={p} target="_blank" rel="noreferrer noopener">{p.length > 60 ? p.slice(0, 57) + '…' : p}</a> : <React.Fragment key={i}>{p}</React.Fragment>))}</>
}

export interface EventActions {
  onEdit: () => void
  onDuplicate: () => void
  onDelete: () => void
  onRsvp: (status: 'accepted' | 'tentative' | 'declined', scope: Scope) => void
  onDownload: () => void
  onCopyLink: () => void
  onToggleTask?: () => void
}

export function EventPopover({ item, anchor, onClose, actions }: { item: Item; anchor: Anchor; onClose: () => void; actions: EventActions }) {
  const { calendars, h12, tz, user } = useApp()
  const ev = item.ev!
  const today = todayWall(tz)
  const cal = calendars.find(c => c.id === ev.calendar_id)
  const [rsvpScope, setRsvpScope] = useState<Scope>('all')
  const status = myStatus(ev)
  const showTz = !ev.all_day && ev.tz && ev.tz !== tz
  const canSee = !ev.masked
  const isOrganizer = !ev.organizer || ev.organizer.email === user?.email
  const contactsOn = useContactsOn()
  const guests = ev.attendees?.length ? [ev.organizer?.email, ...ev.attendees.map(a => a.email)].filter(e => e && e !== user?.email) : []
  const contacts = useContactRefs(guests, contactsOn && canSee)
  const who = (name: string | undefined, email: string) => {
    const c = contacts[email.toLowerCase()]
    const text = name || c?.name || email
    return c ? <a href={c.url} target="_blank" rel="noreferrer noopener" title={`${email} · open in Pocket Contacts`}>{text}</a> : text
  }
  return (
    <Popover anchor={anchor} onClose={onClose} width={400} label={ev.title} className="evpop">
      <div className="evpop-bar">
        {ev.can_edit && <button className="btn ghost icon-only sm" onClick={actions.onEdit} aria-label="Edit" title="Edit (e)"><Icon name="edit" size={16} /></button>}
        {ev.can_edit && <button className="btn ghost icon-only sm" onClick={actions.onDelete} aria-label="Delete" title="Delete (⌫)"><Icon name="trash" size={16} /></button>}
        {canSee && <MenuButton label="More actions" className="btn ghost icon-only sm" align="right" items={[
          ...(cal?.writable || ev.can_edit ? [{ label: 'Duplicate', icon: 'copy', onSelect: actions.onDuplicate }] : []),
          { label: 'Download .ics', icon: 'download', onSelect: actions.onDownload },
          { label: 'Copy link', icon: 'link', onSelect: actions.onCopyLink },
        ]}><Icon name="more" size={16} /></MenuButton>}
        <button className="btn ghost icon-only sm" onClick={onClose} aria-label="Close"><Icon name="x" size={16} /></button>
      </div>
      <div className="evpop-body">
        <div className="evpop-title-row">
          <span className={`evpop-swatch ${ev.masked ? 'masked' : ''}`} style={{ background: item.color }} />
          <div className="grow">
            <h2 className={`evpop-title ${status === 'DECLINED' || ev.status === 'cancelled' ? 'struck' : ''}`}>{item.title}</h2>
            <div className="evpop-when">{whenText(ev.start, ev.end, ev.all_day, h12, today)}</div>
            {ev.repeat_text && <div className="evpop-sub"><Icon name="repeat" size={12} />{ev.repeat_text}{ev.override && ' · changed for this day'}</div>}
            {showTz && <div className="evpop-sub"><Icon name="globe" size={12} />{zoneLabel(ev.tz!)}</div>}
            {ev.status === 'cancelled' && <div className="evpop-sub danger-text">Cancelled</div>}
            {ev.status === 'tentative' && <div className="evpop-sub">Tentative</div>}
          </div>
        </div>

        {ev.invite && (
          <div className="rsvp-box">
            <div className="rsvp-q"><span>Going?</span>{ev.recurring && <Segmented size="sm" value={rsvpScope} onChange={setRsvpScope} options={[{ value: 'this', label: 'This event' }, { value: 'all', label: 'All events' }]} />}</div>
            <div className="rsvp-btns">
              <button className={`btn rsvp yes ${status === 'ACCEPTED' ? 'on' : ''}`} onClick={() => actions.onRsvp('accepted', rsvpScope)}><Icon name="check" size={15} />Yes</button>
              <button className={`btn rsvp maybe ${status === 'TENTATIVE' ? 'on' : ''}`} onClick={() => actions.onRsvp('tentative', rsvpScope)}>Maybe</button>
              <button className={`btn rsvp no ${status === 'DECLINED' ? 'on' : ''}`} onClick={() => actions.onRsvp('declined', rsvpScope)}><Icon name="x" size={15} />No</button>
            </div>
          </div>
        )}

        {ev.conference && (
          <div className="evpop-row">
            <Icon name="video" size={16} />
            <div className="grow row">
              <a className="btn primary sm join" href={ev.conference} target="_blank" rel="noreferrer noopener">Join video call</a>
              <span className="muted small ellipsis">{ev.conference.replace(/^https?:\/\//, '')}</span>
            </div>
          </div>
        )}
        {ev.location && (
          <div className="evpop-row">
            <Icon name="pin" size={16} />
            <div className="grow"><a href={/^https?:/.test(ev.location) ? ev.location : `https://www.openstreetmap.org/search?query=${encodeURIComponent(ev.location)}`} target="_blank" rel="noreferrer noopener">{ev.location}</a></div>
          </div>
        )}
        {ev.url && <div className="evpop-row"><Icon name="link" size={16} /><div className="grow ellipsis"><a href={ev.url} target="_blank" rel="noreferrer noopener">{ev.url}</a></div></div>}
        {(ev.attendees?.length > 0) && (
          <div className="evpop-row top">
            <Icon name="users" size={16} />
            <div className="grow">
              <div className="evpop-guests-sum">{ev.attendees.length + (ev.organizer ? 1 : 0)} guests <span className="muted">· {ev.attendees.filter(a => a.status === 'ACCEPTED').length} yes{ev.attendees.some(a => a.status === 'TENTATIVE') ? `, ${ev.attendees.filter(a => a.status === 'TENTATIVE').length} maybe` : ''}{ev.attendees.some(a => !a.status || a.status === 'NEEDS-ACTION') ? `, ${ev.attendees.filter(a => !a.status || a.status === 'NEEDS-ACTION').length} awaiting` : ''}</span></div>
              <ul className="evpop-guests">
                {ev.organizer && <li><Avatar name={ev.organizer.name} email={ev.organizer.email} src={contacts[ev.organizer.email.toLowerCase()]?.photo_url || undefined} size={22} /><span className="grow ellipsis">{who(ev.organizer.name, ev.organizer.email)}{isOrganizer && ' (you)'}</span><span className="muted xsmall">Organizer</span></li>}
                {ev.attendees.map(a => <li key={a.email}><Avatar name={a.name} email={a.email} src={contacts[a.email.toLowerCase()]?.photo_url || undefined} size={22} /><span className="grow ellipsis">{who(a.name, a.email)}{a.me && ' (you)'}</span><StatusPill status={a.status} /></li>)}
              </ul>
            </div>
          </div>
        )}
        {ev.description && <div className="evpop-row top"><Icon name="text" size={16} /><div className="grow evpop-desc"><Linkify text={ev.description} /></div></div>}
        {canSee && ev.alarms?.length > 0 && (
          <div className="evpop-row top"><Icon name="bell" size={16} /><div className="grow">{ev.alarms.map((a, i) => <div key={i}>{alarmLabel(a.trigger, ev.all_day, h12)}</div>)}{cal && !cal.notify && <div className="muted small">Reminders are off for this calendar</div>}</div></div>
        )}
        <div className="evpop-row">
          <Icon name="calendar" size={16} />
          <div className="grow row">
            <span className="dot" style={{ background: cal?.color || item.color }} />
            <span>{cal?.name || (ev.invite ? 'Invitation' : 'Calendar')}</span>
            {cal && cal.role !== 'owner' && <span className="muted small">· {cal.owner?.name || cal.owner?.email}</span>}
            {ev.invite && ev.organizer && <span className="muted small">from {ev.organizer.name || ev.organizer.email}</span>}
          </div>
        </div>
        {ev.masked && <p className="muted small evpop-note">You can see only that this time is taken.</p>}
        {ev.created_by && ev.created_by.email !== user?.email && <p className="muted xsmall evpop-note">Added by {ev.created_by.name || ev.created_by.email}{ev.source && ev.source !== 'web' && ev.source !== 'demo' ? ` via ${ev.source}` : ''}</p>}
        {ev.created_by && ev.created_by.email === user?.email && ev.source && !['web', 'demo', 'api'].includes(ev.source) && <p className="muted xsmall evpop-note">Added via {ev.source}</p>}
      </div>
    </Popover>
  )
}

const APP_NAME: Record<string, string> = { tasks: 'Pocket Tasks', travel: 'Pocket Travel', garage: 'Pocket Garage', invoicing: 'Pocket Invoicing', habits: 'Pocket Habits', review: 'Pocket Review', projects: 'Pocket Projects', birthdays: 'Pocket Contacts', health: 'Pocket Health', holidays: 'Public holidays' }
const KIND_TEXT: Record<string, string> = { task: 'Task', due: 'Deadline', done: 'Done', trip: 'Trip', service: 'Service due', invoice: 'Invoice due', habit: 'Habit', review: 'Review due', holiday: 'Public holiday', birthday: 'Birthday', anniversary: 'Anniversary' }

export function OverlayPopover({ item, anchor, onClose, onToggle }: { item: Item; anchor: Anchor; onClose: () => void; onToggle?: () => void }) {
  const { h12, tz } = useApp()
  const ov = item.ov!
  const today = todayWall(tz)
  const end = ov.all_day && parseWall(ov.end) <= parseWall(ov.start) ? ov.start : ov.end
  const canTick = ov.actions?.includes('complete')
  return (
    <Popover anchor={anchor} onClose={onClose} width={340} label={item.title} className="evpop ovpop">
      <div className="evpop-bar">
        <span className="ovpop-src"><Icon name={OVERLAY_ICON[ov.kind] || 'dot'} size={13} />{APP_NAME[ov.source] || ov.source}</span>
        <span className="spacer" />
        <button className="btn ghost icon-only sm" onClick={onClose} aria-label="Close"><Icon name="x" size={16} /></button>
      </div>
      <div className="evpop-body">
        <div className="evpop-title-row">
          <span className="evpop-swatch round" style={{ background: item.color }} />
          <div className="grow">
            <h2 className={`evpop-title ${ov.done ? 'struck' : ''}`}>{item.title}</h2>
            <div className="evpop-when">{KIND_TEXT[ov.kind] || ov.kind} · {whenText(ov.start, end, ov.all_day, h12, today)}</div>
            {ov.subtitle && <div className="evpop-sub">{ov.subtitle}</div>}
            {ov.overdue && <div className="evpop-sub danger-text">Overdue</div>}
          </div>
        </div>
        <div className="ovpop-actions">
          {canTick && <button className="btn sm" onClick={() => { onToggle?.(); onClose() }}><Icon name={ov.done ? 'circle' : 'check-circle'} size={14} />{ov.done ? 'Mark as not done' : 'Mark as done'}</button>}
          {ov.url && <a className="btn sm primary" href={ov.url} target="_blank" rel="noreferrer noopener">Open in {APP_NAME[ov.source] || ov.source}<Icon name="external" size={13} /></a>}
        </div>
      </div>
    </Popover>
  )
}
