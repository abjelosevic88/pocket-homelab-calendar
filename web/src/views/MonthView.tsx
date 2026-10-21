// A six-week month grid: multi-day bars spanning cells, timed events as
// dotted lines, "+N more" per day, drag to another day, click/drag to create.
import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { type Wall, DAY, addDays, dayKey, fmtDayLong, fmtTime, isoWeek, MONTHS_SHORT, startOfDay, WD_SHORT } from '../lib/dates'
import { type Item, itemClasses, itemsOnDay, layoutBars } from '../lib/model'
import type { Settings } from '../lib/types'
import { Icon } from '../components/icons'
import { type Anchor, Popover } from '../components/ui'
import { ItemGlyph, colorStyle, itemAria } from './EventChip'
import { rectFromEl } from './TimeGrid'
import { type ViewProps, canDrag, touchDrag } from './types'

const BAR = 22
const HEAD = 28

type Drag = { kind: 'move' | 'create'; it?: Item; pointerId: number; x0: number; y0: number; cell0: number; started: boolean; touch: boolean; timer?: number; el?: HTMLElement; cancelled?: boolean }

export function MonthView(props: ViewProps & { gridStart: Wall; month: number; settings: Settings; h12: boolean }) {
  const { gridStart, month, items, holidays, today, settings, h12, selectedId, highlightId, draft } = props
  const weeks = Array.from({ length: 6 }, (_, w) => addDays(gridStart, w * 7))
  const gridRef = useRef<HTMLDivElement>(null)
  const [rowH, setRowH] = useState(110)
  const [more, setMore] = useState<{ day: Wall; anchor: Anchor } | null>(null)
  const [ghost, setGhostState] = useState<{ id?: string; start: Wall; end: Wall } | null>(null)
  const ghostRef = useRef<typeof ghost>(null)
  const setGhost = (g: typeof ghost) => { ghostRef.current = g; setGhostState(g) }
  const drag = useRef<Drag | null>(null)

  useLayoutEffect(() => {
    const el = gridRef.current
    if (!el) return
    const ro = new ResizeObserver(() => { const r = el.querySelector('.mv-week'); if (r) setRowH(r.getBoundingClientRect().height) })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const maxBars = Math.max(1, Math.floor((rowH - HEAD - 4) / BAR))

  const perWeek = useMemo(() => weeks.map(ws => {
    const shown = ghost?.id ? items.map(it => it.id === ghost.id ? { ...it, start: ghost.start, end: ghost.end } : it) : items
    return layoutBars(shown, ws, 7)
  }), [items, gridStart, ghost]) // eslint-disable-line react-hooks/exhaustive-deps


  const cellAt = (x: number, y: number) => {
    const r = gridRef.current!.getBoundingClientRect()
    const wn = settings.show_week_numbers ? 28 : 0
    const c = Math.max(0, Math.min(6, Math.floor((x - r.left - wn) / ((r.width - wn) / 7))))
    const w = Math.max(0, Math.min(5, Math.floor((y - r.top) / (r.height / 6))))
    return w * 7 + c
  }
  const dayOfCell = (i: number) => addDays(gridStart, i)
  const anchorOfCells = (a: number, b: number): Anchor => {
    const el = gridRef.current!.querySelectorAll<HTMLElement>('.mv-cell')
    const ra = el[a]?.getBoundingClientRect(), rb = el[b]?.getBoundingClientRect()
    if (!ra || !rb) return { x: 0, y: 0, w: 0, h: 0 }
    return { x: ra.left, y: ra.top, w: (ra.top === rb.top ? rb.right : ra.right) - ra.left, h: ra.height }
  }

  const begin = (e: React.PointerEvent, kind: Drag['kind'], it?: Item, el?: HTMLElement) => {
    if (e.button !== 0 && e.pointerType === 'mouse') return
    const d: Drag = { kind, it, pointerId: e.pointerId, x0: e.clientX, y0: e.clientY, cell0: cellAt(e.clientX, e.clientY), started: false, touch: e.pointerType !== 'mouse', el }
    if (kind === 'move' && (!it || !canDrag(it))) d.cancelled = true
    if (d.touch && !d.cancelled) d.timer = window.setTimeout(() => { if (drag.current === d) { d.started = true; touchDrag.active = true; navigator.vibrate?.(8); upd(d, d.x0, d.y0) } }, 380)
    drag.current = d
    const upd = (dd: Drag, x: number, y: number) => {
      const c = cellAt(x, y)
      if (dd.kind === 'move' && dd.it) {
        const delta = c - dd.cell0
        setGhost({ id: dd.it.id, start: dd.it.start + delta * DAY, end: dd.it.end + delta * DAY })
      } else {
        const a = Math.min(c, dd.cell0), b = Math.max(c, dd.cell0)
        setGhost({ start: dayOfCell(a), end: dayOfCell(b + 1) })
      }
    }
    const move = (ev: PointerEvent) => {
      if (ev.pointerId !== d.pointerId || drag.current !== d) return
      const dist = Math.hypot(ev.clientX - d.x0, ev.clientY - d.y0)
      if (!d.started) {
        if (d.touch) { if (dist > 8) { clearTimeout(d.timer); d.cancelled = true } return }
        if (d.cancelled || dist < 5) return
        d.started = true
      }
      if (!d.cancelled) upd(d, ev.clientX, ev.clientY)
    }
    const end = (ev: PointerEvent, cancelled: boolean) => {
      if (ev.pointerId !== d.pointerId) return
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      window.removeEventListener('pointercancel', cancel)
      clearTimeout(d.timer)
      touchDrag.active = false
      if (drag.current !== d) return
      drag.current = null
      const g = ghostRef.current
      setGhost(null)
      if (cancelled) return
      if (!d.started) {
        if (Math.hypot(ev.clientX - d.x0, ev.clientY - d.y0) > 8) return
        if (d.it) { props.onOpen(d.it, rectFromEl(d.el!)); return }
        const day = dayOfCell(d.cell0)
        props.onCreate(day, addDays(day, 1), true, anchorOfCells(d.cell0, d.cell0))
        return
      }
      if (!g) return
      if (d.kind === 'move' && d.it) { if (g.start !== d.it.start) props.onMove(d.it, g.start, g.end, d.it.allDay) }
      else {
        const a = Math.round((g.start - gridStart) / DAY), b = Math.round((g.end - gridStart) / DAY) - 1
        props.onCreate(g.start, g.end, true, anchorOfCells(a, b))
      }
    }
    const up = (ev: PointerEvent) => end(ev, false)
    const cancel = (ev: PointerEvent) => {
      if (ev.pointerId === d.pointerId && d.touch && d.started && drag.current === d) {
        // scrolling took the pointer after the long press: follow touch events instead
        window.removeEventListener('pointermove', move)
        window.removeEventListener('pointerup', up)
        window.removeEventListener('pointercancel', cancel)
        const tmove = (te: TouchEvent) => { const t = te.touches[0]; if (!t) return; if (te.cancelable) te.preventDefault(); upd(d, t.clientX, t.clientY) }
        const tend = (te: TouchEvent) => {
          window.removeEventListener('touchmove', tmove); window.removeEventListener('touchend', tend); window.removeEventListener('touchcancel', tend)
          const t = te.changedTouches[0]
          end({ pointerId: d.pointerId, clientX: t?.clientX ?? d.x0, clientY: t?.clientY ?? d.y0 } as PointerEvent, te.type !== 'touchend')
        }
        window.addEventListener('touchmove', tmove, { passive: false })
        window.addEventListener('touchend', tend)
        window.addEventListener('touchcancel', tend)
        return
      }
      end(ev, true)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
    window.addEventListener('pointercancel', cancel)
  }

  useEffect(() => {
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape' && drag.current) { e.stopPropagation(); clearTimeout(drag.current.timer); drag.current = null; setGhost(null) } }
    const tm = (e: TouchEvent) => { if (drag.current?.started) e.preventDefault() }
    window.addEventListener('keydown', key, true)
    const el = gridRef.current
    el?.addEventListener('touchmove', tm, { passive: false })
    return () => { window.removeEventListener('keydown', key, true); el?.removeEventListener('touchmove', tm) }
  }, [])

  const onDown = (e: React.PointerEvent) => {
    const t = e.target as HTMLElement
    if (t.closest('.task-check') || t.closest('.mv-more') || t.closest('.mv-num')) return
    const el = t.closest<HTMLElement>('[data-id]')
    if (el) { const it = items.find(x => x.id === el.dataset.id); if (it) begin(e, 'move', it, el); return }
    begin(e, 'create')
  }
  const keyOpen = (it: Item) => (e: React.KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); props.onOpen(it, rectFromEl(e.currentTarget as HTMLElement)) } }

  const ghostCreate = ghost && !ghost.id ? ghost : draft && draft.allDay ? draft : null

  return (
    <div className={`mv ${settings.show_week_numbers ? 'wk' : ''}`}>
      <div className="mv-head">
        {settings.show_week_numbers && <span className="mv-wn-head" />}
        {Array.from({ length: 7 }, (_, i) => { const wd = (settings.week_start + i) % 7; return <span key={i} className={`mv-wd ${wd === 0 || wd === 6 ? 'weekend' : ''}`}>{WD_SHORT[wd]}</span> })}
      </div>
      <div className="mv-grid" ref={gridRef} onPointerDown={onDown}>
        {weeks.map((ws, w) => {
          const bars = perWeek[w]
          const limit = bars.some(b => b.row >= maxBars) ? maxBars - 1 : maxBars
          const hidden = new Array(7).fill(0)
          for (const b of bars) if (b.row >= limit) for (let c = b.c0; c < b.c1; c++) hidden[c]++
          return (
            <div className="mv-week" key={ws}>
              {settings.show_week_numbers && <span className="mv-wn">{isoWeek(addDays(ws, 3))}</span>}
              <div className="mv-cells">
                {Array.from({ length: 7 }, (_, c) => {
                  const d = addDays(ws, c)
                  const dt = new Date(d)
                  const out = dt.getUTCMonth() !== month
                  const hol = holidays.get(dayKey(d))
                  const inCreate = ghostCreate && d >= startOfDay(ghostCreate.start) && d < ghostCreate.end
                  return (
                    <div key={d} className={`mv-cell ${out ? 'out' : ''} ${d === today ? 'today' : ''} ${d < today ? 'past' : ''} ${dt.getUTCDay() === 0 || dt.getUTCDay() === 6 ? 'weekend' : ''} ${inCreate ? 'selecting' : ''}`}>
                      <div className="mv-cell-head">
                        <button type="button" className="mv-num" onClick={() => props.goDay(d, 'day')} aria-label={fmtDayLong(d)}>{dt.getUTCDate() === 1 ? `${MONTHS_SHORT[dt.getUTCMonth()]} 1` : dt.getUTCDate()}</button>
                        {hol && <span className="mv-hol" title={hol.map(h => h.title + (h.subtitle ? ' — ' + h.subtitle : '')).join('\n')}>{hol[0].title}</span>}
                      </div>
                    </div>
                  )
                })}
                <div className="mv-bars">
                  {bars.filter(b => b.row < limit).map(b => {
                    const it = b.it
                    const bar = it.allDay || b.c1 - b.c0 > 1 || (it.end - it.start) >= DAY
                    return (
                      <div key={it.id} data-id={it.id} role="button" tabIndex={0} aria-label={itemAria(it, it.allDay ? 'all day' : fmtTime(it.start, h12))}
                        className={`mv-item ${bar ? 'bar' : 'line'} ${itemClasses(it)} ${b.clipL ? 'clip-l' : ''} ${b.clipR ? 'clip-r' : ''} ${selectedId === it.id ? 'selected' : ''} ${highlightId === it.id ? 'flash' : ''} ${ghost?.id === it.id ? 'dragging' : ''} ${canDrag(it) ? 'draggable' : ''}`}
                        style={{ ...colorStyle(it.color), top: HEAD + b.row * BAR, left: `calc(${(b.c0 / 7) * 100}% + 3px)`, width: `calc(${((b.c1 - b.c0) / 7) * 100}% - 6px)` }}
                        onKeyDown={keyOpen(it)}>
                        {it.ov ? <ItemGlyph it={it} onToggle={props.onToggleTask} /> : !bar && <span className="mv-dot" />}
                        {!bar && !it.allDay && <span className="mv-time">{fmtTime(it.start, h12, true)}</span>}
                        <span className="mv-title">{it.title}</span>
                      </div>
                    )
                  })}
                  {hidden.map((k, c) => k > 0 && (
                    <button key={'m' + c} type="button" className="mv-more" style={{ top: HEAD + limit * BAR, left: `calc(${(c / 7) * 100}% + 3px)`, width: `calc(${100 / 7}% - 6px)` }}
                      onClick={e => setMore({ day: addDays(ws, c), anchor: rectFromEl(e.currentTarget) })}>+{k}<span className="more-w"> more</span></button>
                  ))}
                </div>
              </div>
            </div>
          )
        })}
      </div>
      {more && (
        <Popover anchor={more.anchor} onClose={() => setMore(null)} prefer="below" width={280} label={fmtDayLong(more.day)}>
          <DayList day={more.day} items={itemsOnDay(items, more.day)} h12={h12} onOpen={(it, a) => { setMore(null); props.onOpen(it, a) }} onToggle={props.onToggleTask} goDay={() => { setMore(null); props.goDay(more.day, 'day') }} />
        </Popover>
      )}
    </div>
  )
}

export function DayList({ day, items, h12, onOpen, onToggle, goDay }: { day: Wall; items: Item[]; h12: boolean; onOpen: (it: Item, a: Anchor) => void; onToggle: (it: Item) => void; goDay: () => void }) {
  const d = new Date(day)
  return (
    <div className="daylist">
      <button type="button" className="daylist-head" onClick={goDay}>
        <span className="dl-wd">{WD_SHORT[d.getUTCDay()]}</span><span className="dl-num">{d.getUTCDate()}</span>
        <Icon name="chevron-right" size={14} />
      </button>
      <div className="daylist-items">
        {items.map(it => (
          <button key={it.id} type="button" className={`dl-item ${itemClasses(it)} ${it.allDay ? 'bar' : 'line'}`} style={colorStyle(it.color)} onClick={e => onOpen(it, rectFromEl(e.currentTarget))}>
            {it.ov ? <ItemGlyph it={it} onToggle={onToggle} /> : !it.allDay && <span className="mv-dot" />}
            {!it.allDay && <span className="mv-time">{fmtTime(it.start, h12, true)}</span>}
            <span className="mv-title">{it.title}</span>
          </button>
        ))}
      </div>
    </div>
  )
}
