// Day / 3-day / week time grid: an all-day lane on top, 24 scrollable hours
// below, overlap layout, a live "now" line, and pointer-driven create, move
// and resize (15-minute snapping, long-press on touch, Escape cancels).
import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { type Wall, DAY, MIN, addDays, dayKey, fmtHour, fmtTime, isoWeek, minutesOfDay, startOfDay, WD_SHORT, zoneOffset } from '../lib/dates'
import { type Item, type Placed, isLaneItem, itemClasses, layoutBars, layoutDay } from '../lib/model'
import type { Settings } from '../lib/types'
import { Icon } from '../components/icons'
import type { Anchor } from '../components/ui'
import { ItemGlyph, colorStyle, itemAria } from './EventChip'
import { type ViewProps, canDrag, touchDrag } from './types'

const SNAP = 15
const snap = (m: number) => Math.round(m / SNAP) * SNAP
const floorSnap = (m: number) => Math.floor(m / SNAP) * SNAP
const LANE_ROW = 24
const LANE_MAX = 3

type Drag = {
  kind: 'move' | 'resize' | 'create' | 'lane-move' | 'lane-create'
  it?: Item
  pointerId: number
  x0: number
  y0: number
  day0: number
  min0: number
  started: boolean
  touch: boolean
  timer?: number
  el?: HTMLElement
  cancelled?: boolean
}
type Ghost = { id?: string; start: Wall; end: Wall; kind: Drag['kind'] }

export function TimeGrid(props: ViewProps & { days: Wall[]; settings: Settings; h12: boolean; tz: string }) {
  const { days, items, holidays, today, now, settings, h12, selectedId, highlightId, draft } = props
  const n = days.length
  const compact = settings.density === 'compact'
  const hourH = compact ? 40 : 52
  const ppm = hourH / 60
  const minVisual = Math.ceil((compact ? 18 : 22) / ppm)
  const scrollRef = useRef<HTMLDivElement>(null)
  const colsRef = useRef<HTMLDivElement>(null)
  const laneRef = useRef<HTMLDivElement>(null)
  const drag = useRef<Drag | null>(null)
  const [ghost, setGhostState] = useState<Ghost | null>(null)
  const ghostRef = useRef<Ghost | null>(null)
  const setGhost = (g: Ghost | null) => { ghostRef.current = g; setGhostState(g) }
  const [laneOpen, setLaneOpen] = useState(false)
  const suppressClick = useRef(false)
  const [colW, setColW] = useState(160)
  useLayoutEffect(() => {
    const el = colsRef.current
    if (!el) return
    const ro = new ResizeObserver(() => setColW(el.getBoundingClientRect().width / Math.max(1, n)))
    ro.observe(el)
    return () => ro.disconnect()
  }, [n])

  const first = days[0], last = addDays(days[n - 1], 1)
  const colOf = useMemo(() => {
    const idx = new Map(days.map((d, i) => [d, i]))
    return (d: Wall) => {
      const s = startOfDay(d)
      if (idx.has(s)) return idx.get(s)!
      // hidden weekend days: snap to the next visible column
      for (let i = 0; i < n; i++) if (days[i] > s) return i
      return n - 1
    }
  }, [days, n])

  const laneItems = useMemo(() => items.filter(isLaneItem), [items])
  const timed = useMemo(() => items.filter(it => !isLaneItem(it)), [items])

  // ----- all-day lane layout -----
  const bars = useMemo(() => layoutBars(laneItems, first, Math.round((last - first) / DAY), d => colOf(d))
    .map(b => ({ ...b, c0: b.c0, c1: Math.max(b.c0 + 1, b.c1) })), [laneItems, first, last, colOf])
  const fixedBars = useMemo(() => {
    // weekends hidden: recompute column spans from visible columns
    if (Math.round((last - first) / DAY) === n) return bars
    return bars.map(b => {
      const s = Math.max(startOfDay(b.it.start), first)
      const e = Math.min(b.it.allDay ? b.it.end : addDays(startOfDay(b.it.end - 1), 1), last)
      const c0 = colOf(s)
      let c1 = c0
      for (let i = c0; i < n; i++) if (days[i] < e) c1 = i + 1
      return { ...b, c0, c1: Math.max(c1, c0 + 1) }
    })
  }, [bars, first, last, n, days, colOf])
  const rowsUsed = fixedBars.reduce((m, b) => Math.max(m, b.row + 1), 0)
  const overflow = rowsUsed > LANE_MAX
  const visibleRows = laneOpen || !overflow ? rowsUsed : LANE_MAX - 1
  const hiddenPerCol = useMemo(() => {
    const c = new Array(n).fill(0)
    if (laneOpen || !overflow) return c
    for (const b of fixedBars) if (b.row >= visibleRows) for (let i = b.c0; i < b.c1; i++) c[i]++
    return c
  }, [fixedBars, laneOpen, overflow, visibleRows, n])
  const laneH = Math.max(1, visibleRows + (overflow && !laneOpen ? 1 : 0)) * LANE_ROW + 6

  // ----- per-column placement -----
  const placed = useMemo(() => days.map(d => layoutDay(timed.filter(it => !(ghost && ghost.id === it.id && ghost.kind !== 'create')), d, minVisual, colW < 420)), [days, timed, minVisual, ghost, colW])
  const origin = ghost?.id ? timed.find(t => t.id === ghost.id) : undefined

  // ----- scroll to working hours once -----
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const start = settings.work_start ?? 8
    const nowMin = minutesOfDay(now)
    const showsToday = days.includes(today)
    let target = Math.max(0, start * 60 - 30)
    if (showsToday && (nowMin < target || nowMin > (settings.work_end ?? 18) * 60)) target = Math.max(0, nowMin - 120)
    el.scrollTop = target * ppm
  }, [hourH]) // eslint-disable-line react-hooks/exhaustive-deps

  const scrolledTo = useRef<string | null>(null)
  useEffect(() => {
    if (!highlightId || scrolledTo.current === highlightId) return
    const el = scrollRef.current?.querySelector<HTMLElement>(`[data-id="${CSS.escape(highlightId)}"]`)
    if (!el) return
    scrolledTo.current = highlightId
    const box = scrollRef.current!.getBoundingClientRect(), r = el.getBoundingClientRect()
    if (r.top < box.top + 120 || r.bottom > box.bottom) el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }, [highlightId, items])

  // ----- geometry -----
  const colAt = (x: number) => {
    const r = colsRef.current!.getBoundingClientRect()
    return Math.max(0, Math.min(n - 1, Math.floor((x - r.left) / (r.width / n))))
  }
  const minAt = (y: number) => {
    const r = colsRef.current!.getBoundingClientRect()
    return Math.max(0, Math.min(1440, (y - r.top) / ppm))
  }
  const laneColAt = (x: number) => {
    const r = laneRef.current!.getBoundingClientRect()
    return Math.max(0, Math.min(n - 1, Math.floor((x - r.left) / (r.width / n))))
  }
  const anchorFor = (start: Wall, end: Wall, lane = false): Anchor => {
    if (lane) {
      const r = laneRef.current!.getBoundingClientRect()
      const w = r.width / n
      const c0 = colOf(start), c1 = colOf(addDays(end, -1))
      return { x: r.left + c0 * w, y: r.top, w: (c1 - c0 + 1) * w, h: LANE_ROW }
    }
    const r = colsRef.current!.getBoundingClientRect()
    const w = r.width / n
    const c = colOf(start)
    const top = r.top + minutesOfDay(start) * ppm
    return { x: r.left + c * w, y: Math.max(top, 60), w, h: Math.max(20, (end - start) / MIN * ppm) }
  }

  // ----- the pointer machine -----
  const update = (d: Drag, x: number, y: number) => {
    if (d.kind === 'lane-move' || d.kind === 'lane-create') {
      const c = laneColAt(x)
      if (d.kind === 'lane-move' && d.it) {
        const delta = c - d.day0
        const s = addDays(d.it.start, delta), e = addDays(d.it.end, delta)
        setGhost({ id: d.it.id, start: s, end: e, kind: d.kind })
      } else {
        const a = Math.min(c, d.day0), b = Math.max(c, d.day0)
        setGhost({ start: days[a], end: addDays(days[b], 1), kind: d.kind })
      }
      return
    }
    const c = colAt(x)
    const m = minAt(y)
    if (d.kind === 'move' && d.it) {
      const dur = d.it.end - d.it.start
      const offsetMin = (d.it.start - startOfDay(d.it.start)) / MIN + (m - d.min0)
      let sm = snap(offsetMin)
      sm = Math.max(0, Math.min(1440 - SNAP, sm))
      const s = days[c] + sm * MIN
      setGhost({ id: d.it.id, start: s, end: s + dur, kind: 'move' })
    } else if (d.kind === 'resize' && d.it) {
      let e = days[c] + snap(m) * MIN
      if (e < d.it.start + SNAP * MIN) e = d.it.start + SNAP * MIN
      setGhost({ id: d.it.id, start: d.it.start, end: e, kind: 'resize' })
    } else if (d.kind === 'create') {
      const a = floorSnap(d.min0), b = floorSnap(m)
      const s = Math.min(a, b), e = Math.max(a, b) + SNAP
      setGhost({ start: days[d.day0] + s * MIN, end: days[d.day0] + Math.min(1440, e) * MIN, kind: 'create' })
    }
  }

  const autoScroll = (y: number) => {
    const el = scrollRef.current
    if (!el) return
    const r = el.getBoundingClientRect()
    if (y < r.top + 90) el.scrollTop -= 10
    else if (y > r.bottom - 40) el.scrollTop += 10
  }

  const begin = (e: React.PointerEvent, kind: Drag['kind'], it?: Item, el?: HTMLElement) => {
    if (e.button !== 0 && e.pointerType === 'mouse') return
    const touch = e.pointerType !== 'mouse'
    const lane = kind.startsWith('lane')
    const d: Drag = {
      kind, it, pointerId: e.pointerId, x0: e.clientX, y0: e.clientY,
      day0: lane ? laneColAt(e.clientX) : colAt(e.clientX), min0: lane ? 0 : minAt(e.clientY),
      started: false, touch, el: el || (e.currentTarget as HTMLElement),
    }
    // move/resize of something we can't change: click only
    if ((kind === 'move' || kind === 'resize' || kind === 'lane-move') && (!it || !canDrag(it))) d.cancelled = true
    if (touch && !d.cancelled) {
      d.timer = window.setTimeout(() => {
        if (drag.current !== d) return
        d.started = true
        touchDrag.active = true
        navigator.vibrate?.(8)
        update(d, d.x0, d.y0)
      }, 380)
    }
    drag.current = d
    const move = (ev: PointerEvent) => {
      if (ev.pointerId !== d.pointerId || drag.current !== d) return
      const dist = Math.hypot(ev.clientX - d.x0, ev.clientY - d.y0)
      if (!d.started) {
        if (d.touch) { if (dist > 8) { clearTimeout(d.timer); d.cancelled = true } return }
        if (d.cancelled || dist < 4) return
        d.started = true
      }
      if (d.cancelled) return
      autoScroll(ev.clientY)
      update(d, ev.clientX, ev.clientY)
    }
    const up = (ev: PointerEvent) => {
      if (ev.pointerId !== d.pointerId) return
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      window.removeEventListener('pointercancel', cancel)
      clearTimeout(d.timer)
      touchDrag.active = false
      if (drag.current !== d) return
      drag.current = null
      const dist = Math.hypot(ev.clientX - d.x0, ev.clientY - d.y0)
      if (!d.started) {
        setGhost(null)
        if (dist > 8) return
        // a click / tap
        suppressClick.current = true
        setTimeout(() => { suppressClick.current = false }, 50)
        if (d.it) { props.onOpen(d.it, rectFromEl(d.el!)); return }
        if (kind === 'lane-create') { const day = days[d.day0]; props.onCreate(day, addDays(day, 1), true, anchorFor(day, addDays(day, 1), true)); return }
        if (kind === 'create') {
          const s = days[d.day0] + Math.min(1440 - 30, Math.floor(d.min0 / 30) * 30) * MIN
          const dur = (settings.default_duration_min || 60) * MIN
          props.onCreate(s, Math.min(s + dur, days[d.day0] + DAY), false, anchorFor(s, s + dur))
        }
        return
      }
      const g = ghostRef.current
      setGhost(null)
      if (g) commit(d, g)
    }
    const cancel = (ev: PointerEvent) => {
      if (ev.pointerId !== d.pointerId) return
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      window.removeEventListener('pointercancel', cancel)
      clearTimeout(d.timer)
      // the browser took the touch for scrolling after the long press: keep
      // following the finger with touch events (their moves are prevented)
      if (d.touch && d.started && drag.current === d) { followTouch(d); return }
      touchDrag.active = false
      if (drag.current === d) drag.current = null
      setGhost(null)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
    window.addEventListener('pointercancel', cancel)
  }

  const followTouch = (d: Drag) => {
    const tmove = (te: TouchEvent) => { const t = te.touches[0]; if (!t) return; if (te.cancelable) te.preventDefault(); autoScroll(t.clientY); update(d, t.clientX, t.clientY) }
    const tend = (te: TouchEvent) => {
      window.removeEventListener('touchmove', tmove)
      window.removeEventListener('touchend', tend)
      window.removeEventListener('touchcancel', tend)
      touchDrag.active = false
      if (drag.current !== d) return
      drag.current = null
      const g = ghostRef.current
      setGhost(null)
      if (g && te.type === 'touchend') commit(d, g)
    }
    window.addEventListener('touchmove', tmove, { passive: false })
    window.addEventListener('touchend', tend)
    window.addEventListener('touchcancel', tend)
  }

  const commit = (d: Drag, g: Ghost) => {
    if (d.kind === 'create') { props.onCreate(g.start, g.end, false, anchorFor(g.start, g.end)); return }
    if (d.kind === 'lane-create') { props.onCreate(g.start, g.end, true, anchorFor(g.start, g.end, true)); return }
    if (!d.it) return
    if (g.start === d.it.start && g.end === d.it.end) return
    props.onMove(d.it, g.start, g.end, d.kind === 'lane-move' ? d.it.allDay : false)
  }

  // Escape cancels a drag; a started touch drag must not scroll the page.
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && drag.current) { e.stopPropagation(); clearTimeout(drag.current.timer); drag.current = null; setGhost(null) }
    }
    const tm = (e: TouchEvent) => { if (drag.current?.started) e.preventDefault() }
    window.addEventListener('keydown', key, true)
    const el = scrollRef.current
    el?.addEventListener('touchmove', tm, { passive: false })
    return () => { window.removeEventListener('keydown', key, true); el?.removeEventListener('touchmove', tm) }
  }, [])

  const onGridDown = (e: React.PointerEvent) => {
    const t = e.target as HTMLElement
    if (t.closest('.task-check')) return
    const block = t.closest<HTMLElement>('[data-id]')
    if (block) {
      const it = timed.find(x => x.id === block.dataset.id)
      if (!it) return
      e.stopPropagation()
      begin(e, t.closest('.resize-handle') ? 'resize' : 'move', it, block)
      return
    }
    begin(e, 'create')
  }
  const onLaneDown = (e: React.PointerEvent) => {
    const t = e.target as HTMLElement
    if (t.closest('.task-check') || t.closest('.lane-more')) return
    const block = t.closest<HTMLElement>('[data-id]')
    if (block) {
      const it = laneItems.find(x => x.id === block.dataset.id)
      if (!it) return
      begin(e, 'lane-move', it, block)
      return
    }
    begin(e, 'lane-create')
  }
  const keyOpen = (it: Item) => (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); props.onOpen(it, rectFromEl(e.currentTarget as HTMLElement)) }
  }

  // ----- render helpers -----
  const workDays = new Set(settings.work_days || [1, 2, 3, 4, 5])
  const ws = settings.work_start ?? 8, we = settings.work_end ?? 18
  const nowMin = minutesOfDay(now)
  const offset = zoneOffset(props.tz, Date.now())
  const gmt = `GMT${offset >= 0 ? '+' : '−'}${Math.floor(Math.abs(offset) / 60)}${Math.abs(offset) % 60 ? ':' + String(Math.abs(offset) % 60).padStart(2, '0') : ''}`

  const block = (p: Placed, key: string, extraCls = '') => {
    const it = p.it
    const h = Math.max(p.height * ppm, minVisual * ppm) - 2
    const short = h < 34
    const time = fmtTime(it.start, h12, true)
    const timeRange = `${fmtTime(it.start, h12, true)} – ${fmtTime(it.end, h12, true)}`
    const draggable = canDrag(it)
    const isTask = it.ov && (it.ov.kind === 'task' || it.ov.kind === 'due' || it.ov.kind === 'done')
    const narrow = p.width * colW < 70
    return (
      <div key={key} data-id={it.id} role="button" tabIndex={0} aria-label={itemAria(it, timeRange)}
        className={`ev-block ${itemClasses(it)} ${short ? 'short' : ''} ${selectedId === it.id ? 'selected' : ''} ${highlightId === it.id ? 'flash' : ''} ${p.contStart ? 'cont-start' : ''} ${p.contEnd ? 'cont-end' : ''} ${draggable ? 'draggable' : ''} ${isTask ? 'task-block' : ''} ${narrow ? 'narrow' : ''} ${extraCls}`}
        style={{ ...colorStyle(it.color), zIndex: p.col ? 1 + p.col : undefined, top: p.top * ppm + 1, height: h, left: `calc(${p.left * 100}% + 1px)`, width: `calc(${p.width * 100}% - ${p.left + p.width >= 0.999 ? 6 : 2}px)` }}
        onKeyDown={keyOpen(it)}>
        <div className="ev-inner">
          {short ? (
            <div className="ev-line"><ItemGlyph it={it} onToggle={props.onToggleTask} /><span className="ev-title">{it.title}</span><span className="ev-time">{time}</span></div>
          ) : (<>
            <div className="ev-line"><ItemGlyph it={it} onToggle={props.onToggleTask} /><span className="ev-title wrap">{it.title}</span></div>
            <div className="ev-time">{timeRange}{it.ev?.recurring && <Icon name="repeat" size={10} className="ev-rep" />}</div>
            {h > 62 && it.ev?.location && <div className="ev-loc">{it.ev.location}</div>}
          </>)}
        </div>
        {draggable && !p.contEnd && <div className="resize-handle" aria-hidden="true" />}
      </div>
    )
  }

  const ghostBlock = (g: Ghost, c: number) => {
    const d0 = days[c]
    if (startOfDay(g.start) !== d0) return null
    const it = g.id ? timed.find(t => t.id === g.id) : undefined
    const sm = (g.start - d0) / MIN, em = Math.min(1440, (g.end - d0) / MIN)
    if (it) return block({ it: { ...it, start: g.start, end: g.end }, top: sm, height: em - sm, left: 0, width: 1, startMin: sm, endMin: em, contStart: false, contEnd: g.end > d0 + DAY }, 'ghost', 'dragging')
    return (
      <div key="ghost" className="ev-block draft" style={{ top: sm * ppm + 1, height: Math.max(18, (em - sm) * ppm - 2), left: 1, width: 'calc(100% - 6px)' }}>
        <div className="ev-inner"><div className="ev-title">(No title)</div><div className="ev-time">{fmtTime(g.start, h12, true)} – {fmtTime(g.end, h12, true)}</div></div>
      </div>
    )
  }
  const draftG: Ghost | null = draft && !draft.allDay ? { start: draft.start, end: draft.end, kind: 'create' } : null
  const laneGhost = ghost && (ghost.kind === 'lane-move' || ghost.kind === 'lane-create') ? ghost : null
  const laneDraft = draft && draft.allDay ? draft : null

  return (
    <div className={`tg ${compact ? 'compact' : ''}`} style={{ ['--hour' as string]: `${hourH}px`, ['--cols' as string]: n }}>
      <div className="tg-scroll" ref={scrollRef}>
        <div className="tg-top">
          <div className="tg-head">
            <div className="tg-corner">{settings.show_week_numbers && <span className="wk-badge" title="Week number">W{isoWeek(days[0])}</span>}</div>
            {days.map(d => {
              const hol = holidays.get(dayKey(d))
              const wd = new Date(d).getUTCDay()
              return (
                <div key={d} className={`tg-dayhead ${d === today ? 'today' : ''} ${d < today ? 'past' : ''} ${wd === 0 || wd === 6 ? 'weekend' : ''}`}>
                  <button type="button" className="dh-btn" onClick={() => props.goDay(d, 'day')} aria-label={`Open ${dayKey(d)}`}>
                    <span className="dh-wd">{WD_SHORT[wd]}</span>
                    <span className="dh-num">{new Date(d).getUTCDate()}</span>
                  </button>
                  {hol && <div className="dh-hol" title={hol.map(h => `${h.title}${h.subtitle ? ' — ' + h.subtitle : ''}`).join('\n')}>{hol[0].title}{hol.length > 1 ? ` +${hol.length - 1}` : ''}</div>}
                </div>
              )
            })}
          </div>
          <div className="tg-allday">
            <div className="tg-corner lane-corner">
              <span className="lane-label">{gmt}</span>
              {overflow && <button type="button" className="btn ghost icon-only xs" aria-label={laneOpen ? 'Collapse all-day events' : 'Show all all-day events'} onClick={() => setLaneOpen(o => !o)}><Icon name={laneOpen ? 'chevron-up' : 'chevron-down'} size={14} /></button>}
            </div>
            <div className="tg-lane" ref={laneRef} style={{ height: laneH }} onPointerDown={onLaneDown}>
              {days.map((d, i) => <div key={d} className={`lane-col ${d === today ? 'today' : ''}`} style={{ left: `${(i / n) * 100}%`, width: `${100 / n}%` }} />)}
              {fixedBars.filter(b => b.row < visibleRows && !(laneGhost?.id === b.it.id)).map(b => laneBar(b.it, b.row, b.c0, b.c1, b.clipL, b.clipR))}
              {!laneOpen && overflow && hiddenPerCol.map((k, i) => k > 0 && (
                <button type="button" key={'more' + i} className="lane-more" style={{ top: visibleRows * LANE_ROW + 3, left: `calc(${(i / n) * 100}% + 2px)`, width: `calc(${100 / n}% - 6px)` }} onClick={() => setLaneOpen(true)}>+{k}<span className="more-w"> more</span></button>
              ))}
              {laneGhost && (() => {
                const c0 = colOf(Math.max(laneGhost.start, first)), c1 = colOf(addDays(Math.min(laneGhost.end, last), -1)) + 1
                const it = laneGhost.id ? laneItems.find(x => x.id === laneGhost.id) : undefined
                return it ? laneBar({ ...it, start: laneGhost.start, end: laneGhost.end }, fixedBars.find(b => b.it.id === it.id)?.row ?? 0, c0, c1, false, false, 'dragging')
                  : <div className="lane-bar draft" style={{ top: 3, left: `calc(${(c0 / n) * 100}% + 2px)`, width: `calc(${((c1 - c0) / n) * 100}% - 6px)` }}>(No title)</div>
              })()}
              {laneDraft && !laneGhost && (() => {
                const c0 = colOf(Math.max(laneDraft.start, first)), c1 = colOf(addDays(Math.min(laneDraft.end, last), -1)) + 1
                return <div className="lane-bar draft" style={{ top: visibleRows * LANE_ROW + 3, left: `calc(${(c0 / n) * 100}% + 2px)`, width: `calc(${((c1 - c0) / n) * 100}% - 6px)` }}>(No title)</div>
              })()}
            </div>
          </div>
        </div>
        <div className="tg-body" style={{ height: 24 * hourH }}>
          <div className="tg-gutter" aria-hidden="true">
            {Array.from({ length: 24 }, (_, h) => h > 0 && <span key={h} className="hour-label" style={{ top: h * hourH }}>{fmtHour(h, h12)}</span>)}
            {days.includes(today) && <span className="now-label" style={{ top: nowMin * ppm }}>{fmtTime(now, h12, true)}</span>}
          </div>
          <div className="tg-cols" ref={colsRef} onPointerDown={onGridDown}>
            {days.map((d, i) => {
              const wd = new Date(d).getUTCDay()
              const working = workDays.has(wd)
              return (
                <div key={d} className={`tg-col ${d === today ? 'today' : ''} ${working ? '' : 'offday'}`}>
                  {working ? <>
                    {ws > 0 && <div className="offhours" style={{ top: 0, height: ws * hourH }} />}
                    {we < 24 && <div className="offhours" style={{ top: we * hourH, height: (24 - we) * hourH }} />}
                  </> : <div className="offhours" style={{ top: 0, height: 24 * hourH }} />}
                  {placed[i].map(p => block(p, p.it.id, origin && p.it.id === origin.id ? 'drag-origin' : ''))}
                  {ghost && ghost.kind !== 'lane-move' && ghost.kind !== 'lane-create' && ghostBlock(ghost, i)}
                  {!ghost && draftG && ghostBlock(draftG, i)}
                  {d === today && <div className="now-line" style={{ top: nowMin * ppm }}><span className="now-dot" /></div>}
                </div>
              )
            })}
          </div>
        </div>
      </div>
    </div>
  )

  function laneBar(it: Item, row: number, c0: number, c1: number, clipL: boolean, clipR: boolean, extra = '') {
    return (
      <div key={it.id + (extra || '')} data-id={it.id} role="button" tabIndex={0} aria-label={itemAria(it, 'all day')}
        className={`lane-bar ${itemClasses(it)} ${it.allDay ? '' : 'timed'} ${clipL ? 'clip-l' : ''} ${clipR ? 'clip-r' : ''} ${selectedId === it.id ? 'selected' : ''} ${highlightId === it.id ? 'flash' : ''} ${canDrag(it) ? 'draggable' : ''} ${extra}`}
        style={{ ...colorStyle(it.color), top: row * LANE_ROW + 3, left: `calc(${(c0 / n) * 100}% + 2px)`, width: `calc(${((c1 - c0) / n) * 100}% - 6px)` }}
        onKeyDown={keyOpen(it)}>
        {clipL && <Icon name="chevron-left" size={11} className="clip-ico" />}
        <ItemGlyph it={it} onToggle={props.onToggleTask} />
        {!it.allDay && <span className="bar-time">{fmtTime(it.start, h12, true)}</span>}
        <span className="bar-title">{it.title}</span>
        {clipR && <Icon name="chevron-right" size={11} className="clip-ico r" />}
      </div>
    )
  }
}

export function rectFromEl(el: HTMLElement): Anchor {
  const r = el.getBoundingClientRect()
  return { x: r.left, y: r.top, w: r.width, h: r.height }
}
