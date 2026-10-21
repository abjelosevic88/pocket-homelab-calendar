// Guests: people on the box (with avatars) or any email; shows each answer.
import React, { useMemo, useRef, useState } from 'react'
import { useApp } from '../lib/app-context'
import type { Attendee, PartStat } from '../lib/types'
import { Icon } from './icons'
import { type Option, OptionList } from './Select'
import { type Anchor, Avatar, rectOf } from './ui'

export const STATUS_TEXT: Record<string, string> = { ACCEPTED: 'Going', DECLINED: 'Not going', TENTATIVE: 'Maybe', 'NEEDS-ACTION': 'Awaiting', DELEGATED: 'Delegated' }
export function StatusPill({ status }: { status?: PartStat | string }) {
  const s = status || 'NEEDS-ACTION'
  const cls = s === 'ACCEPTED' ? 'yes' : s === 'DECLINED' ? 'no' : s === 'TENTATIVE' ? 'maybe' : 'wait'
  return <span className={`rsvp-pill ${cls}`}>{STATUS_TEXT[s] || s}</span>
}

const isEmail = (s: string) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(s.trim())

export function AttendeesEditor({ value, onChange, organizer }: { value: Attendee[]; onChange: (v: Attendee[]) => void; organizer?: { email: string; name?: string } }) {
  const { people, user } = useApp()
  const [q, setQ] = useState('')
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const have = new Set(value.map(a => a.email.toLowerCase()))
  const options = useMemo<Option<string>[]>(() => {
    const n = q.trim().toLowerCase()
    const list: Option<string>[] = people.filter(p => !p.me && !have.has(p.email.toLowerCase()) && (!n || `${p.name} ${p.email}`.toLowerCase().includes(n)))
      .map(p => ({ value: p.email, label: p.name || p.email, description: p.email, icon: <Avatar name={p.name} email={p.email} size={22} /> }))
    if (isEmail(q) && !have.has(q.trim().toLowerCase()) && !list.some(o => o.value.toLowerCase() === q.trim().toLowerCase())) list.push({ value: q.trim(), label: `Invite ${q.trim()}`, icon: <span className="who-ico"><Icon name="mail" size={13} /></span> })
    return list
  }, [q, people, value]) // eslint-disable-line react-hooks/exhaustive-deps
  const add = (email: string) => {
    const p = people.find(x => x.email.toLowerCase() === email.toLowerCase())
    onChange([...value, { email: p?.email || email, name: p?.name || undefined, status: 'NEEDS-ACTION' }])
    setQ('')
    input.current?.focus()
  }
  const open = () => { if (input.current) setAnchor(rectOf(input.current)) }
  return (
    <div className="attendees">
      <div className="att-input">
        <input ref={input} value={q} placeholder="Add guests — people here or any email" aria-label="Add guests"
          onFocus={open} onChange={e => { setQ(e.target.value); open() }}
          onKeyDown={(e: React.KeyboardEvent) => {
            if (e.key === 'Enter' && isEmail(q) && !options.length) { e.preventDefault(); add(q.trim()) }
            if (e.key === 'Backspace' && !q && value.length) onChange(value.slice(0, -1))
          }} />
      </div>
      {anchor && options.length > 0 && <OptionList anchor={anchor} options={options} search={false} width={Math.max(anchor.w, 260)} onPick={add} onClose={() => setAnchor(null)} />}
      {(value.length > 0 || organizer) && (
        <ul className="att-list">
          {organizer && (
            <li className="att-row"><Avatar name={organizer.name} email={organizer.email} size={26} />
              <span className="grow"><span className="att-name">{organizer.name || organizer.email}{organizer.email === user?.email && ' (you)'}</span><span className="att-sub">Organizer</span></span>
            </li>
          )}
          {value.map((a, i) => (
            <li key={a.email} className="att-row">
              <Avatar name={a.name} email={a.email} size={26} />
              <span className="grow"><span className="att-name">{a.name || a.email}{a.email === user?.email && ' (you)'}</span>{a.name && <span className="att-sub">{a.email}</span>}</span>
              <StatusPill status={a.status} />
              <button type="button" className="btn ghost icon-only xs" aria-label={`Remove ${a.name || a.email}`} onClick={() => onChange(value.filter((_, k) => k !== i))}><Icon name="x" size={13} /></button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
