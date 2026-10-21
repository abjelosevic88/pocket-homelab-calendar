import { useEffect, useMemo, useRef } from 'react'
import { type Wall, addDays, dayKey, DAY, fmtTime, MONTHS_SHORT, relativeDay, startOfDay, WEEKDAYS } from '../lib/dates'
import { type Item, itemClasses } from '../lib/model'
import type { Calendar, OverlayItem } from '../lib/types'
import { Icon } from '../components/icons'
import { Empty, Spinner } from '../components/ui'
import { ItemGlyph, colorStyle } from './EventChip'
import { rectFromEl } from './TimeGrid'
import type { ViewProps } from './types'

export function AgendaView(props: ViewProps & { from: Wall; days: number; h12: boolean; calendars: Calendar[]; onMore: () => void }) {
  const { from, days, items, holidays, today, h12, calendars, selectedId, highlightId, loading } = props
  const calName = useMemo(() => new Map(calendars.map(c => [c.id, c.name])), [calendars])
  const ref = useRef<HTMLDivElement>(null)
  const groups = useMemo(() => {
    const out: { day: Wall; items: Item[]; hol: OverlayItem[] }[] = []
    for (let i = 0; i < days; i++) {
      const d = addDays(from, i), d1 = d + DAY
      const list = items.filter(it => it.start < d1 && it.end > d)
      const hol = holidays.get(dayKey(d)) || []
      if (list.length || hol.length || d === today) out.push({ day: d, items: list, hol })
    }
    return out
  }, [from, days, items, holidays, today])
  const scrolledTo = useRef<string | null>(null)
  useEffect(() => {
    if (!highlightId || scrolledTo.current === highlightId) return
    const el = ref.current?.querySelector<HTMLElement>(`[data-id="${CSS.escape(highlightId)}"]`)
    if (!el) return
    scrolledTo.current = highlightId
    el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }, [highlightId, groups])
  // load more when the end comes into view
  const sentinel = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = sentinel.current
    if (!el) return
    const io = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting) && !loading) props.onMore() }, { root: ref.current, rootMargin: '200px' })
    io.observe(el)
    return () => io.disconnect()
  }, [loading, props.onMore]) // eslint-disable-line react-hooks/exhaustive-deps

  const total = groups.reduce((n, g) => n + g.items.length, 0)
  return (
    <div className="agenda" ref={ref}>
      {groups.map(g => {
        const dt = new Date(g.day)
        const rel = relativeDay(g.day, today)
        return (
          <section key={g.day} className={`ag-day ${g.day === today ? 'today' : ''}`}>
            <header className="ag-head">
              <button type="button" className="ag-date" onClick={() => props.goDay(g.day, 'day')}>
                <span className="ag-num">{dt.getUTCDate()}</span>
                <span className="ag-meta"><span className="ag-wd">{rel || WEEKDAYS[dt.getUTCDay()]}</span><span className="ag-mon">{rel ? WEEKDAYS[dt.getUTCDay()].slice(0, 3) + ', ' : ''}{MONTHS_SHORT[dt.getUTCMonth()]}{dt.getUTCFullYear() !== new Date(today).getUTCFullYear() ? ' ' + dt.getUTCFullYear() : ''}</span></span>
              </button>
              {g.hol.map(h => <span key={h.id} className="ag-hol" title={h.subtitle}><Icon name="star" size={11} />{h.title}</span>)}
            </header>
            <div className="ag-items">
              {g.items.length === 0 && <div className="ag-none">Nothing planned</div>}
              {g.items.map(it => {
                const startsToday = startOfDay(it.start) === g.day
                const endsToday = it.end <= g.day + DAY
                const time = it.allDay ? 'All day' : startsToday && endsToday ? `${fmtTime(it.start, h12)} – ${fmtTime(it.end, h12)}` : startsToday ? `From ${fmtTime(it.start, h12)}` : endsToday ? `Until ${fmtTime(it.end, h12)}` : 'All day'
                const sub = it.ev ? [it.ev.location, it.ev.invite ? 'Invitation' : calName.get(it.ev.calendar_id)].filter(Boolean).join(' · ') : it.ov?.subtitle || ''
                return (
                  <button key={it.id} data-id={it.id} type="button" className={`ag-item ${itemClasses(it)} ${selectedId === it.id ? 'selected' : ''} ${highlightId === it.id ? 'flash' : ''}`} style={colorStyle(it.color)}
                    onClick={e => props.onOpen(it, rectFromEl(e.currentTarget))}>
                    <span className="ag-time tnum">{time}</span>
                    <span className="ag-mark">{it.ov ? <ItemGlyph it={it} onToggle={props.onToggleTask} /> : <span className="ag-dot" />}</span>
                    <span className="ag-body">
                      <span className="ag-title">{it.title}{it.ev?.recurring && <Icon name="repeat" size={11} className="ag-rep" />}</span>
                      {sub && <span className="ag-sub">{sub}</span>}
                    </span>
                    {it.ev?.conference && <Icon name="video" size={14} className="ag-ico" />}
                    {it.ev?.invite && <span className="badge">RSVP</span>}
                  </button>
                )
              })}
            </div>
          </section>
        )
      })}
      {loading && groups.every(g => g.items.length === 0) && (
        <div className="ag-skel" aria-hidden="true">{[0, 1, 2].map(i => <div key={i} className="ag-skel-day"><span className="skeleton" style={{ width: 120, height: 36, borderRadius: 18 }} /><div className="ag-skel-rows">{[0, 1, 2].map(k => <span key={k} className="skeleton" style={{ height: 34, width: `${70 - k * 12}%` }} />)}</div></div>)}</div>
      )}
      {!loading && total === 0 && <Empty title="A clear calendar" icon="calendar">Nothing in the next {days} days. Press <kbd>c</kbd> to add something.</Empty>}
      <div ref={sentinel} className="ag-more">
        {loading ? <Spinner /> : <button type="button" className="btn ghost sm" onClick={props.onMore}>Show more</button>}
      </div>
    </div>
  )
}

export const AGENDA_DAYS = 60
