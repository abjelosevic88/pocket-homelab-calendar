import { type Wall, addDays, addMonths, dayKey, isoWeek, MONTHS, startOfMonth, startOfWeek, WD_MIN } from '../lib/dates'
import { Icon } from './icons'

export function MiniMonth({ month, onMonth, selected, range, today, weekStart, onPick, dots, weekNumbers, compact }: {
  month: Wall
  onMonth: (m: Wall) => void
  selected?: Wall | null
  range?: [Wall, Wall] | null
  today: Wall
  weekStart: number
  onPick: (d: Wall) => void
  dots?: Set<string>
  weekNumbers?: boolean
  compact?: boolean
}) {
  const first = startOfMonth(month)
  const gridStart = startOfWeek(first, weekStart)
  const days = Array.from({ length: 42 }, (_, i) => addDays(gridStart, i))
  const m = new Date(first).getUTCMonth()
  const rows = [0, 1, 2, 3, 4, 5]
  return (
    <div className={`mini ${compact ? 'compact' : ''} ${weekNumbers ? 'wk' : ''}`}>
      <div className="mini-head">
        <span className="mini-title">{MONTHS[m]} {new Date(first).getUTCFullYear()}</span>
        <button type="button" className="btn ghost icon-only xs" aria-label="Previous month" onClick={() => onMonth(addMonths(first, -1))}><Icon name="chevron-left" size={15} /></button>
        <button type="button" className="btn ghost icon-only xs" aria-label="Next month" onClick={() => onMonth(addMonths(first, 1))}><Icon name="chevron-right" size={15} /></button>
      </div>
      <div className="mini-grid" role="grid">
        <div className="mini-row mini-wds" role="row">
          {weekNumbers && <span className="mini-wn" />}
          {Array.from({ length: 7 }, (_, i) => <span key={i} className="mini-wd">{WD_MIN[(weekStart + i) % 7]}</span>)}
        </div>
        {rows.map(r => (
          <div className="mini-row" key={r} role="row">
            {weekNumbers && <span className="mini-wn">{isoWeek(days[r * 7 + 3])}</span>}
            {days.slice(r * 7, r * 7 + 7).map(d => {
              const k = dayKey(d)
              const out = new Date(d).getUTCMonth() !== m
              const inRange = range && d >= range[0] && d < range[1]
              const cls = ['mini-day', out && 'out', d === today && 'today', selected === d && 'sel', inRange && 'range',
                inRange && d === range![0] && 'range-start', inRange && addDays(d, 1) === range![1] && 'range-end'].filter(Boolean).join(' ')
              return (
                <button type="button" role="gridcell" key={k} className={cls} onClick={() => onPick(d)} aria-label={k} aria-current={d === today ? 'date' : undefined}>
                  <span>{new Date(d).getUTCDate()}</span>
                  {dots?.has(k) && <i className="mini-dot" />}
                </button>
              )
            })}
          </div>
        ))}
      </div>
    </div>
  )
}
