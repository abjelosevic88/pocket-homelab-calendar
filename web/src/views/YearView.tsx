import { useMemo } from 'react'
import { type Wall, addDays, addMonths, dayKey, MONTHS, startOfWeek, WD_MIN } from '../lib/dates'
import type { Item } from '../lib/model'

export function YearView({ year, items, today, weekStart, goDay, goMonth, loading }: { year: Wall; items: Item[]; today: Wall; weekStart: number; goDay: (d: Wall) => void; goMonth: (m: Wall) => void; loading: boolean }) {
  const counts = useMemo(() => {
    const m = new Map<string, number>()
    for (const it of items) {
      if (it.ov && it.ov.kind !== 'trip') continue
      let d = Math.floor(it.start / 86400000) * 86400000
      const end = it.end
      let guard = 0
      do {
        const k = dayKey(d)
        m.set(k, (m.get(k) || 0) + 1)
        d = addDays(d, 1)
      } while (d < end && ++guard < 60)
    }
    return m
  }, [items])
  const level = (n: number) => (n <= 0 ? 0 : n === 1 ? 1 : n === 2 ? 2 : n <= 4 ? 3 : 4)
  return (
    <div className={`yv ${loading ? 'is-loading' : ''}`}>
      <div className="yv-grid">
        {Array.from({ length: 12 }, (_, i) => {
          const first = addMonths(year, i)
          const gs = startOfWeek(first, weekStart)
          const mi = new Date(first).getUTCMonth()
          return (
            <section key={i} className="yv-month">
              <button type="button" className="yv-title" onClick={() => goMonth(first)}>{MONTHS[mi]}</button>
              <div className="yv-days">
                {Array.from({ length: 7 }, (_, k) => <span key={'h' + k} className="yv-wd">{WD_MIN[(weekStart + k) % 7][0]}</span>)}
                {Array.from({ length: 42 }, (_, k) => {
                  const d = addDays(gs, k)
                  const inMonth = new Date(d).getUTCMonth() === mi
                  if (!inMonth) return <span key={k} className="yv-day blank" />
                  const n = counts.get(dayKey(d)) || 0
                  return (
                    <button key={k} type="button" className={`yv-day h${level(n)} ${d === today ? 'today' : ''}`} onClick={() => goDay(d)}
                      title={`${dayKey(d)}${n ? ` · ${n} event${n > 1 ? 's' : ''}` : ''}`} aria-label={`${dayKey(d)}, ${n} events`}>
                      {new Date(d).getUTCDate()}
                    </button>
                  )
                })}
              </div>
            </section>
          )
        })}
      </div>
      <div className="yv-legend" aria-hidden="true">
        <span>Fewer</span>{[0, 1, 2, 3, 4].map(l => <i key={l} className={`yv-swatch h${l}`} />)}<span>More</span>
      </div>
    </div>
  )
}
