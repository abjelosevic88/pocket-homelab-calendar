// Find a time: busy blocks of the chosen people side by side, per day, with
// the stretches when everyone is free marked; a click there schedules it.
import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { api, V1, qs } from '../lib/api'
import { useApp } from '../lib/app-context'
import { type Wall, DAY, MIN, addDays, dayKey, fmtHour, fmtTime, minutesOfDay, parseWall, WD_SHORT } from '../lib/dates'
import type { BusyBlock, PeopleEntry } from '../lib/types'
import { Icon } from '../components/icons'
import { type Anchor, Avatar, Empty } from '../components/ui'

const PEOPLE_COLORS = ['#3b82f6', '#a855f7', '#f97316', '#ec4899', '#14b8a6', '#eab308', '#ef4444', '#06b6d4', '#84cc16']

export function PeopleView({ days, today, now, selected, onSelected, onCreate }: {
  days: Wall[]
  today: Wall
  now: Wall
  selected: number[]
  onSelected: (ids: number[]) => void
  onCreate: (start: Wall, end: Wall, people: PeopleEntry[], anchor: Anchor) => void
}) {
  const { people, settings, h12, version, tzParam, reloadPeople } = useApp()
  const me = people.find(p => p.me)
  const others = people.filter(p => !p.me)
  const colorIndex = new Map<number, string>([me, ...others].filter(Boolean).map((p, i) => [p!.id, PEOPLE_COLORS[i % PEOPLE_COLORS.length]]))
  const personColor = (p: { id: number }) => colorIndex.get(p.id) || '#64748b'
  const chosen = useMemo(() => [me, ...others.filter(p => selected.includes(p.id))].filter(Boolean) as PeopleEntry[], [me, others, selected])
  const [busy, setBusy] = useState<Record<string, BusyBlock[]>>({})
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)
  const colsRef = useRef<HTMLDivElement>(null)
  const hourH = settings?.density === 'compact' ? 40 : 52
  const ppm = hourH / 60
  const from = days[0], to = addDays(days[days.length - 1], 1)
  const [hover, setHover] = useState<{ c: number; m: number } | null>(null)

  useEffect(() => { reloadPeople() }, [reloadPeople])
  useEffect(() => {
    if (!chosen.length) return
    let live = true
    setLoading(true)
    api.get<{ busy: Record<string, BusyBlock[]> }>(`${V1}/freebusy${qs({ users: chosen.map(p => p.id).join(','), from: dayKey(from), to: dayKey(to), tz: tzParam })}`)
      .then(r => { if (live) { setBusy(r.busy || {}); setErr('') } })
      .catch(e => live && setErr((e as Error).message))
      .finally(() => live && setLoading(false))
    return () => { live = false }
  }, [chosen.map(p => p.id).join(','), from, to, version, tzParam]) // eslint-disable-line react-hooks/exhaustive-deps

  useLayoutEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = Math.max(0, ((settings?.work_start ?? 8) * 60 - 30) * ppm)
  }, [hourH]) // eslint-disable-line react-hooks/exhaustive-deps

  // minutes of each day when every chosen person is free, inside working hours
  const freeSpans = useMemo(() => days.map(d => {
    const ws = (settings?.work_start ?? 8) * 60, we = (settings?.work_end ?? 18) * 60
    const taken: [number, number][] = []
    for (const p of chosen) for (const b of busy[String(p.id)] || []) {
      const s = parseWall(b.start), e = parseWall(b.end)
      if (b.all_day) continue
      if (e <= d || s >= d + DAY) continue
      taken.push([Math.max(0, (s - d) / MIN), Math.min(1440, (e - d) / MIN)])
    }
    taken.sort((a, b) => a[0] - b[0])
    const spans: [number, number][] = []
    let cur = ws
    for (const [s, e] of taken) {
      if (e <= cur) continue
      if (s > cur) spans.push([cur, Math.min(s, we)])
      cur = Math.max(cur, e)
      if (cur >= we) break
    }
    if (cur < we) spans.push([cur, we])
    return spans.filter(([s, e]) => e - s >= 15)
  }), [days, chosen, busy, settings?.work_start, settings?.work_end])

  const n = days.length
  const lanes = chosen.length
  const minAt = (y: number) => { const r = colsRef.current!.getBoundingClientRect(); return Math.max(0, Math.min(1440, (y - r.top) / ppm)) }
  const colAt = (x: number) => { const r = colsRef.current!.getBoundingClientRect(); return Math.max(0, Math.min(n - 1, Math.floor((x - r.left) / (r.width / n)))) }
  const click = (e: React.MouseEvent) => {
    const c = colAt(e.clientX), m = Math.floor(minAt(e.clientY) / 30) * 30
    const s = days[c] + m * MIN
    const dur = (settings?.default_duration_min || 60) * MIN
    const r = colsRef.current!.getBoundingClientRect()
    onCreate(s, s + dur, chosen.filter(p => !p.me), { x: r.left + c * (r.width / n), y: r.top + m * ppm, w: r.width / n, h: dur / MIN * ppm })
  }

  if (!people.length) return <Empty title="Loading people…" icon="users" />
  return (
    <div className="pv">
      <div className="pv-people">
        {me && <span className="person-chip me on" style={{ ['--pc' as string]: personColor(me) }}><Avatar name={me.name} email={me.email} size={20} color={personColor(me)} /><span>You</span></span>}
        <span className="pv-label">with</span>
        {others.length === 0 && <span className="muted small">Nobody else is on this box yet — add people in Settings → People.</span>}
        {others.map(p => {
          const on = selected.includes(p.id)
          return (
            <button key={p.id} type="button" disabled={!p.freebusy} aria-pressed={on} className={`person-chip ${on ? 'on' : ''}`} style={{ ['--pc' as string]: personColor(p) }}
              title={p.freebusy ? p.email : `${p.name || p.email} doesn’t share free/busy with you`} onClick={() => onSelected(on ? selected.filter(x => x !== p.id) : [...selected, p.id])}>
              <Avatar name={p.name} email={p.email} size={20} color={personColor(p)} />
              <span>{p.name || p.email}</span>
              {!p.freebusy && <Icon name="lock" size={12} />}
              {on && <Icon name="check" size={13} />}
            </button>
          )
        })}
        <span className="spacer" />
        <span className="pv-legend"><i className="pv-free-sw" /> Everyone free {loading && <span className="spinner" />}</span>
      </div>
      {err && <div className="callout danger m">{err}</div>}
      <div className="tg pv-grid" style={{ ['--hour' as string]: `${hourH}px`, ['--cols' as string]: n }}>
        <div className="tg-scroll" ref={scrollRef}>
          <div className="tg-top">
            <div className="tg-head">
              <div className="tg-corner" />
              {days.map(d => (
                <div key={d} className={`tg-dayhead ${d === today ? 'today' : ''}`}>
                  <div className="dh-btn static"><span className="dh-wd">{WD_SHORT[new Date(d).getUTCDay()]}</span><span className="dh-num">{new Date(d).getUTCDate()}</span></div>
                  {n <= 3 && <div className="pv-lanes-head">
                    {chosen.map(p => <span key={p.id} title={p.name || p.email}><Avatar name={p.name} email={p.email} size={20} color={personColor(p)} /><span className="pv-name">{p.me ? 'You' : (p.name || p.email).split(' ')[0]}</span></span>)}
                  </div>}
                </div>
              ))}
            </div>
          </div>
          <div className="tg-body" style={{ height: 24 * hourH }}>
            <div className="tg-gutter" aria-hidden="true">
              {Array.from({ length: 24 }, (_, h) => h > 0 && <span key={h} className="hour-label" style={{ top: h * hourH }}>{fmtHour(h, h12)}</span>)}
            </div>
            <div className="tg-cols pv-cols" ref={colsRef} onClick={click} onMouseMove={e => setHover({ c: colAt(e.clientX), m: Math.floor(minAt(e.clientY) / 30) * 30 })} onMouseLeave={() => setHover(null)}>
              {days.map((d, c) => (
                <div key={d} className={`tg-col ${d === today ? 'today' : ''}`}>
                  {freeSpans[c].map(([s, e]) => <div key={s} className="pv-free" style={{ top: s * ppm, height: (e - s) * ppm }} />)}
                  {chosen.map((p, li) => (busy[String(p.id)] || []).filter(b => !b.all_day).map((b, k) => {
                    const s = parseWall(b.start), e = parseWall(b.end)
                    if (e <= d || s >= d + DAY) return null
                    const sm = Math.max(0, (s - d) / MIN), em = Math.min(1440, (e - d) / MIN)
                    return <div key={p.id + ':' + k} className="pv-busy" title={`${p.me ? 'You' : p.name || p.email}: busy ${fmtTime(s, h12)} – ${fmtTime(e, h12)}`}
                      style={{ ['--pc' as string]: personColor(p), top: sm * ppm, height: Math.max(4, (em - sm) * ppm - 1), left: `calc(${(li / lanes) * 100}% + 2px)`, width: `calc(${100 / lanes}% - 4px)` }} />
                  }))}
                  {chosen.map((p, li) => (busy[String(p.id)] || []).some(b => b.all_day && parseWall(b.start) <= d && parseWall(b.end) > d) &&
                    <div key={'ad' + p.id} className="pv-allday" style={{ left: `calc(${(li / lanes) * 100}% + 2px)`, width: `calc(${100 / lanes}% - 4px)` }} title="Busy all day" />)}
                  {hover && hover.c === c && <div className="pv-hover" style={{ top: hover.m * ppm, height: (settings?.default_duration_min || 60) * ppm }}><span>{fmtTime(d + hover.m * MIN, h12, true)}</span></div>}
                  {d === today && <div className="now-line" style={{ top: minutesOfDay(now) * ppm }}><span className="now-dot" /></div>}
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
