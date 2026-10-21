// ⌘K / "/" — search events and run commands from the keyboard.
import React, { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { api, V1, qs } from '../lib/api'
import { useApp } from '../lib/app-context'
import { type Wall, addDays, fmtDayLong, MONTHS, todayWall, whenText } from '../lib/dates'
import type { Occurrence, ViewName } from '../lib/types'
import { Icon } from './icons'
import { useDebounce } from './ui'

export interface PaletteCommand { id: string; label: string; icon: string; hint?: string; keywords?: string; run: () => void }

/** "2026-10-21", "21.10.", "21.10.2026", "oct 21", "21 october 2026", "today", "tomorrow" → a day. */
export function parseDateQuery(q: string, today: Wall): Wall | null {
  const s = q.trim().toLowerCase().replace(/^(go to|goto|jump to|date)\s+/, '')
  if (!s) return null
  if (s === 'today') return today
  if (s === 'tomorrow') return addDays(today, 1)
  if (s === 'yesterday') return addDays(today, -1)
  let m = /^(\d{4})-(\d{1,2})-(\d{1,2})$/.exec(s)
  if (m) return Date.UTC(+m[1], +m[2] - 1, +m[3])
  m = /^(\d{1,2})[./](\d{1,2})[./]?(\d{4})?\.?$/.exec(s)
  if (m) return Date.UTC(m[3] ? +m[3] : new Date(today).getUTCFullYear(), +m[2] - 1, +m[1])
  const mon = (t: string) => MONTHS.findIndex(x => x.toLowerCase().startsWith(t.slice(0, 3)))
  m = /^([a-z]{3,})\.?\s+(\d{1,2})(?:,?\s+(\d{4}))?$/.exec(s)
  if (m && mon(m[1]) >= 0) return Date.UTC(m[3] ? +m[3] : new Date(today).getUTCFullYear(), mon(m[1]), +m[2])
  m = /^(\d{1,2})\.?\s+([a-z]{3,})(?:\s+(\d{4}))?$/.exec(s)
  if (m && mon(m[2]) >= 0) return Date.UTC(m[3] ? +m[3] : new Date(today).getUTCFullYear(), mon(m[2]), +m[1])
  m = /^([a-z]{3,})\s+(\d{4})$/.exec(s)
  if (m && mon(m[1]) >= 0) return Date.UTC(+m[2], mon(m[1]), 1)
  return null
}

export function CommandPalette({ onClose, commands, goDate, openEvent }: { onClose: () => void; commands: PaletteCommand[]; goDate: (d: Wall, view?: ViewName) => void; openEvent: (ev: Occurrence) => void }) {
  const { tzParam, tz, h12, calendars } = useApp()
  const [q, setQ] = useState('')
  const [hi, setHi] = useState(0)
  const [results, setResults] = useState<Occurrence[]>([])
  const [searching, setSearching] = useState(false)
  const dq = useDebounce(q, 180)
  const list = useRef<HTMLDivElement>(null)
  const today = todayWall(tz)

  useEffect(() => {
    if (dq.trim().length < 2) { setResults([]); return }
    let live = true
    setSearching(true)
    api.get<Occurrence[]>(`${V1}/search${qs({ q: dq.trim(), tz: tzParam })}`).then(r => live && setResults(r || [])).catch(() => live && setResults([])).finally(() => live && setSearching(false))
    return () => { live = false }
  }, [dq, tzParam])

  type Row = { key: string; group: string; icon: React.ReactNode; label: React.ReactNode; sub?: React.ReactNode; hint?: string; run: () => void }
  const rows = useMemo<Row[]>(() => {
    const out: Row[] = []
    const n = q.trim().toLowerCase()
    const date = parseDateQuery(q, today)
    if (date !== null && !isNaN(date)) out.push({ key: 'date', group: 'Go to', icon: <Icon name="calendar" size={16} />, label: `Go to ${fmtDayLong(date)}`, run: () => goDate(date) })
    for (const ev of results) {
      const cal = calendars.find(c => c.id === ev.calendar_id)
      out.push({
        key: 'ev' + ev.id, group: 'Events', icon: <span className="pal-dot" style={{ background: ev.color || cal?.color }} />,
        label: ev.title, sub: <>{whenText(ev.start, ev.end, ev.all_day, h12, today)}{ev.location ? ` · ${ev.location}` : ''}</>, run: () => openEvent(ev),
      })
    }
    for (const c of commands) {
      if (n && !`${c.label} ${c.keywords || ''}`.toLowerCase().includes(n)) continue
      out.push({ key: c.id, group: 'Commands', icon: <Icon name={c.icon} size={16} />, label: c.label, hint: c.hint, run: c.run })
    }
    return out
  }, [q, results, commands, calendars, h12, today, goDate, openEvent])
  useEffect(() => setHi(0), [q, results.length])
  useEffect(() => { list.current?.querySelector(`[data-i="${hi}"]`)?.scrollIntoView({ block: 'nearest' }) }, [hi])

  const run = (r: Row | undefined) => { if (!r) return; onClose(); r.run() }
  let last = ''
  return createPortal(
    <div className="palette-backdrop" onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="palette-box" role="dialog" aria-label="Search and commands">
        <div className="pal-input">
          <Icon name="search" size={18} />
          <input autoFocus value={q} onChange={e => setQ(e.target.value)} placeholder="Search events, jump to a date, or run a command…" aria-label="Search"
            onKeyDown={e => {
              if (e.key === 'ArrowDown') { e.preventDefault(); setHi(h => Math.min(rows.length - 1, h + 1)) }
              else if (e.key === 'ArrowUp') { e.preventDefault(); setHi(h => Math.max(0, h - 1)) }
              else if (e.key === 'Enter') { e.preventDefault(); run(rows[hi]) }
              else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); onClose() }
            }} />
          {searching && <span className="spinner" />}
          <kbd>esc</kbd>
        </div>
        <div className="pal-list" ref={list} role="listbox">
          {rows.length === 0 && <div className="pal-empty">{q.trim().length >= 2 && !searching ? 'No events match.' : 'Type to search.'}</div>}
          {rows.map((r, i) => {
            const head = r.group !== last ? <div className="pal-group" key={'g' + r.group}>{r.group}</div> : null
            last = r.group
            return (
              <React.Fragment key={r.key}>
                {head}
                <div data-i={i} role="option" aria-selected={i === hi} className={`pal-row ${i === hi ? 'hi' : ''}`} onMouseEnter={() => setHi(i)} onClick={() => run(r)}>
                  <span className="pal-ico">{r.icon}</span>
                  <span className="grow"><span className="pal-label">{r.label}</span>{r.sub && <span className="pal-sub">{r.sub}</span>}</span>
                  {r.hint && <kbd>{r.hint}</kbd>}
                  {i === hi && <Icon name="chevron-right" size={14} className="muted" />}
                </div>
              </React.Fragment>
            )
          })}
        </div>
      </div>
    </div>, document.body)
}

export const VIEW_LABEL: Record<ViewName, string> = { day: 'Day', '3day': '3 days', week: 'Week', month: 'Month', year: 'Year', agenda: 'Agenda', people: 'People' }
export const VIEW_KEY: Record<ViewName, string> = { day: 'd', '3day': 'x', week: 'w', month: 'm', year: 'y', agenda: 'a', people: 'p' }
