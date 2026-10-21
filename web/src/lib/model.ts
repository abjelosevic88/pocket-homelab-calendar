// Turning API occurrences and overlay items into things the views lay out.
import { type Wall, DAY, MIN, addDays, parseWall, startOfDay } from './dates'
import type { Calendar, Occurrence, OverlayItem, PartStat, Settings } from './types'

/** Where new events go: the person's chosen default, else their own default calendar, else any writable one. */
export function defaultCalendar(cals: Calendar[], s: Settings | null): Calendar | undefined {
  return cals.find(c => c.id === s?.default_calendar_id && c.writable) || cals.find(c => c.is_default && c.role === 'owner' && c.writable) || cals.find(c => c.writable)
}

/** Is this the calendar new events go to? */
export const isDefaultCal = (c: Calendar, cals: Calendar[], s: Settings | null) => defaultCalendar(cals, s)?.id === c.id

/** Make a calendar the default: own ones carry the flag, shared ones go into settings. */
export function makeDefaultPatch(c: Calendar): 'flag' | 'setting' { return c.role === 'owner' ? 'flag' : 'setting' }

export interface Item {
  id: string
  title: string
  start: Wall
  end: Wall          // exclusive; for all-day items the day after the last day
  allDay: boolean
  color: string
  ev?: Occurrence
  ov?: OverlayItem
}

export interface Override { start: Wall; end: Wall; allDay: boolean }

/** The viewer's own answer to an event (invitation or attendee row). */
export function myStatus(ev: Occurrence): PartStat | '' {
  if (ev.invite) return ev.invite.status
  const me = ev.attendees?.find(a => a.me)
  return me?.status || ''
}

export function itemClasses(it: Item): string {
  const c: string[] = []
  if (it.ov) {
    c.push('ov', `ov-${it.ov.kind}`)
    if (it.ov.done) c.push('done')
    if (it.ov.overdue) c.push('overdue')
    return c.join(' ')
  }
  const ev = it.ev!
  if (ev.masked) c.push('masked')
  const st = myStatus(ev)
  if (ev.invite && st === 'NEEDS-ACTION') c.push('pending')
  if (st === 'DECLINED') c.push('declined')
  if (st === 'TENTATIVE' || ev.status === 'tentative') c.push('tentative')
  if (ev.status === 'cancelled') c.push('cancelled')
  if (!ev.busy) c.push('free')
  return c.join(' ')
}

export function buildItems(events: Occurrence[], overlays: OverlayItem[], calendars: Calendar[], settings: Settings | null, overrides: Record<string, Override>, hidden: Set<string>, extra: Occurrence[] = []): { items: Item[]; holidays: Map<string, OverlayItem[]> } {
  const calColor = new Map(calendars.map(c => [c.id, c.color]))
  const visibleCal = new Map(calendars.map(c => [c.id, c.visible]))
  const items: Item[] = []
  const holidays = new Map<string, OverlayItem[]>()
  const seen = new Set<string>()
  for (const ev of [...events, ...extra]) {
    if (hidden.has(ev.id) || seen.has(ev.id)) continue
    seen.add(ev.id)
    if (!ev.invite && visibleCal.get(ev.calendar_id) === false) continue
    if (myStatus(ev) === 'DECLINED' && !settings?.show_declined) continue
    const color = ev.color || (ev.invite ? settings?.invites_color : calColor.get(ev.calendar_id)) || '#64748b'
    const o = overrides[ev.id]
    items.push({ id: ev.id, title: ev.title || '(No title)', start: o ? o.start : parseWall(ev.start), end: o ? o.end : parseWall(ev.end), allDay: o ? o.allDay : ev.all_day, color, ev })
  }
  for (const ov of overlays) {
    if (ov.kind === 'holiday') {
      let d = parseWall(ov.start)
      const end = parseWall(ov.end)
      do {
        const k = new Date(d).toISOString().slice(0, 10)
        const list = holidays.get(k) || []
        list.push(ov)
        holidays.set(k, list)
        d = addDays(d, 1)
      } while (d < end)
      continue
    }
    if (hidden.has(ov.id)) continue
    const o = overrides[ov.id]
    let s = o ? o.start : parseWall(ov.start), e = o ? o.end : parseWall(ov.end || ov.start)
    const allDay = o ? o.allDay : ov.all_day
    if (allDay && e <= s) e = addDays(s, 1)
    if (!allDay && e <= s) e = s + 30 * MIN
    items.push({ id: ov.id, title: ov.title.replace(/^(\u2708|\u{1F527})\uFE0F?\s*/u, ''), start: s, end: e, allDay, color: ov.color, ov })
  }
  items.sort((a, b) => a.start - b.start || (b.end - b.start) - (a.end - a.start) || a.title.localeCompare(b.title))
  return { items, holidays }
}

/** Items that sit in the all-day lane (all-day ones and timed ones spanning 24h+). */
export function isLaneItem(it: Item): boolean {
  return it.allDay || it.end - it.start >= DAY
}

// ---------- time grid overlap layout ----------

export interface Placed { it: Item; top: number; height: number; left: number; width: number; startMin: number; endMin: number; contStart: boolean; contEnd: boolean; col?: number }

/**
 * Lay out one day's timed items: overlapping ones sit side by side in
 * columns, each widened to the right as far as nothing is in the way.
 * Positions are fractions (0..1) of the column width; top/height in minutes.
 */
export function layoutDay(items: Item[], day: Wall, minVisualMin: number, cascade = true): Placed[] {
  const d0 = startOfDay(day), d1 = d0 + DAY
  const evs = items
    .filter(it => it.start < d1 && it.end > d0)
    .map(it => {
      const s = Math.max(it.start, d0), e = Math.min(it.end, d1)
      const startMin = (s - d0) / MIN, endMin = Math.max((e - d0) / MIN, startMin + 1)
      return { it, startMin, endMin, vEnd: Math.max(endMin, startMin + minVisualMin), contStart: it.start < d0, contEnd: it.end > d1 }
    })
    .sort((a, b) => a.startMin - b.startMin || b.endMin - a.endMin)
  const out: Placed[] = []
  let cluster: typeof evs = []
  let clusterEnd = -1
  const flush = () => {
    if (!cluster.length) return
    const cols: (typeof evs)[] = []
    const colOf = new Map<(typeof evs)[number], number>()
    for (const e of cluster) {
      let placed = false
      for (let c = 0; c < cols.length; c++) {
        const last = cols[c][cols[c].length - 1]
        if (last.vEnd <= e.startMin) { cols[c].push(e); colOf.set(e, c); placed = true; break }
      }
      if (!placed) { cols.push([e]); colOf.set(e, cols.length - 1) }
    }
    const n = cols.length
    for (const e of cluster) {
      const c = colOf.get(e)!
      let span = 1
      for (let k = c + 1; k < n; k++) {
        if (cols[k].some(o => o.startMin < e.vEnd && o.vEnd > e.startMin)) break
        span++
      }
      let width = span / n
      // like Google: earlier columns reach under the later ones so titles stay readable
      if (cascade && c + span < n) width = Math.min(1 - c / n, width * 1.7)
      out.push({ it: e.it, top: e.startMin, height: e.endMin - e.startMin, left: c / n, width, startMin: e.startMin, endMin: e.endMin, contStart: e.contStart, contEnd: e.contEnd, col: c })
    }
    cluster = []
    clusterEnd = -1
  }
  for (const e of evs) {
    if (cluster.length && e.startMin >= clusterEnd) flush()
    cluster.push(e)
    clusterEnd = Math.max(clusterEnd, e.vEnd)
  }
  flush()
  return out
}

// ---------- bars across day columns (all-day lane, month rows) ----------

export interface Bar { it: Item; row: number; c0: number; c1: number; clipL: boolean; clipR: boolean }

/** Assign rows to items spanning day columns [first, first + n). */
export function layoutBars(items: Item[], first: Wall, n: number, colOfDay?: (d: Wall) => number): Bar[] {
  const last = addDays(first, n)
  const rows: boolean[][] = []
  const out: Bar[] = []
  const col = colOfDay || ((d: Wall) => Math.round((startOfDay(d) - first) / DAY))
  const sorted = items.filter(it => it.start < last && it.end > first)
    .sort((a, b) => {
      const da = startOfDay(a.start), db = startOfDay(b.start)
      if (da !== db) return da - db
      // multi-day and all-day first (longest first), then by time of day
      const la = spanDays(a), lb = spanDays(b)
      if (la !== lb) return lb - la
      if (a.allDay !== b.allDay) return a.allDay ? -1 : 1
      return a.start - b.start || Number(!!a.ov) - Number(!!b.ov)
    })
  for (const it of sorted) {
    const s = Math.max(startOfDay(it.start), first)
    const endDay = it.allDay ? it.end : (startOfDay(it.end) === it.end ? it.end : addDays(startOfDay(it.end), 1))
    const e = Math.min(endDay, last)
    let c0 = col(s), c1 = col(addDays(e, -1)) + 1
    if (c1 <= c0) c1 = c0 + 1
    if (c0 < 0) c0 = 0
    if (c1 > n) c1 = n
    let r = 0
    for (; ; r++) {
      const row = rows[r] || (rows[r] = [])
      let free = true
      for (let c = c0; c < c1; c++) if (row[c]) { free = false; break }
      if (free) { for (let c = c0; c < c1; c++) row[c] = true; break }
    }
    out.push({ it, row: r, c0, c1, clipL: it.start < first, clipR: endDay > last })
  }
  return out
}

function spanDays(it: Item): number {
  const end = it.allDay ? it.end : addDays(startOfDay(it.end - 1), 1)
  return Math.max(1, Math.round((end - startOfDay(it.start)) / DAY))
}

/** Items that touch a given day (for month cells and the "+N more" list). */
export function itemsOnDay(items: Item[], day: Wall): Item[] {
  const d1 = addDays(day, 1)
  return items.filter(it => it.start < d1 && (it.end > day || (it.end === it.start && it.start >= day)))
}
