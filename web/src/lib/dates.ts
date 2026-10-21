// Date math on "wall time": a number of milliseconds whose UTC fields are the
// clock on the wall in the person's time zone. The server already answers in
// that zone, so parsing just reads the digits — no browser-zone surprises, and
// a day is always 24 hours on the grid (like every calendar app).

export type Wall = number
export const MIN = 60_000
export const HOUR = 60 * MIN
export const DAY = 24 * HOUR

const pad = (n: number) => String(n).padStart(2, '0')

/** "2026-10-21", "2026-10-21T09:15", "2026-10-21T09:15:00+02:00" → wall time. */
export function parseWall(s: string): Wall {
  const y = +s.slice(0, 4), m = +s.slice(5, 7), d = +s.slice(8, 10)
  const h = s.length > 10 ? +s.slice(11, 13) : 0
  const mi = s.length > 10 ? +s.slice(14, 16) : 0
  return Date.UTC(y, m - 1, d, h || 0, mi || 0)
}

export function dayKey(w: Wall): string {
  const d = new Date(w)
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`
}
export function monthKey(w: Wall): string { return dayKey(w).slice(0, 7) }
export function localString(w: Wall): string {
  const d = new Date(w)
  return `${dayKey(w)}T${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`
}
export function hhmm(w: Wall): string { const d = new Date(w); return `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}` }

export const startOfDay = (w: Wall) => Math.floor(w / DAY) * DAY
export const addDays = (w: Wall, n: number) => w + n * DAY
export const minutesOfDay = (w: Wall) => Math.round((w - startOfDay(w)) / MIN)
export const weekday = (w: Wall) => new Date(w).getUTCDay()
export const sameDay = (a: Wall, b: Wall) => startOfDay(a) === startOfDay(b)
export const daysBetween = (a: Wall, b: Wall) => Math.round((startOfDay(b) - startOfDay(a)) / DAY)

export function addMonths(w: Wall, n: number): Wall {
  const d = new Date(w)
  const y = d.getUTCFullYear(), m = d.getUTCMonth() + n
  const last = new Date(Date.UTC(y, m + 1, 0)).getUTCDate()
  return Date.UTC(y, m, Math.min(d.getUTCDate(), last), d.getUTCHours(), d.getUTCMinutes())
}
export function startOfMonth(w: Wall): Wall { const d = new Date(w); return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1) }
export function startOfYear(w: Wall): Wall { return Date.UTC(new Date(w).getUTCFullYear(), 0, 1) }
export function startOfWeek(w: Wall, weekStart: number): Wall {
  const d = startOfDay(w)
  const diff = (weekday(d) - weekStart + 7) % 7
  return addDays(d, -diff)
}
export function daysInMonth(w: Wall): number { const d = new Date(w); return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 0)).getUTCDate() }

/** ISO 8601 week number. */
export function isoWeek(w: Wall): number {
  const d = new Date(startOfDay(w))
  const day = (d.getUTCDay() + 6) % 7
  d.setUTCDate(d.getUTCDate() - day + 3)
  const firstThursday = new Date(Date.UTC(d.getUTCFullYear(), 0, 4))
  return 1 + Math.round(((d.getTime() - firstThursday.getTime()) / DAY - 3 + ((firstThursday.getUTCDay() + 6) % 7)) / 7)
}

// ---------- zones ----------

const partsCache = new Map<string, Intl.DateTimeFormat>()
function zoneFmt(tz: string) {
  let f = partsCache.get(tz)
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', { timeZone: tz, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })
    partsCache.set(tz, f)
  }
  return f
}

/** The wall time of a real instant in a zone. */
export function instantToWall(ms: number, tz: string): Wall {
  try {
    const p: Record<string, number> = {}
    for (const x of zoneFmt(tz).formatToParts(new Date(ms))) if (x.type !== 'literal') p[x.type] = +x.value
    return Date.UTC(p.year, p.month - 1, p.day, p.hour === 24 ? 0 : p.hour, p.minute, p.second)
  } catch {
    const d = new Date(ms)
    return Date.UTC(d.getFullYear(), d.getMonth(), d.getDate(), d.getHours(), d.getMinutes(), d.getSeconds())
  }
}

export function nowWall(tz: string): Wall { return instantToWall(Date.now(), tz) }
export function todayWall(tz: string): Wall { return startOfDay(nowWall(tz)) }

/** Offset of a zone from UTC (minutes) at an instant. */
export function zoneOffset(tz: string, ms: number): number {
  return Math.round((instantToWall(ms, tz) - Math.floor(ms / 1000) * 1000) / MIN)
}

/** Wall time in a zone → the real instant. */
export function wallToInstant(w: Wall, tz: string): number {
  let guess = w - zoneOffset(tz, w) * MIN
  const off2 = zoneOffset(tz, guess)
  guess = w - off2 * MIN
  return guess
}

/** Wall time → RFC 3339 with the zone's offset ("2026-10-21T09:15:00+02:00"). */
export function wallToRFC3339(w: Wall, tz: string): string {
  const off = zoneOffset(tz, wallToInstant(w, tz))
  const sign = off >= 0 ? '+' : '-'
  const a = Math.abs(off)
  return `${localString(w)}:00${sign}${pad(Math.floor(a / 60))}:${pad(a % 60)}`
}

export function browserZone(): string {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC' } catch { return 'UTC' }
}

let zoneList: string[] | null = null
export function allZones(): string[] {
  if (zoneList) return zoneList
  try {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const z = (Intl as any).supportedValuesOf?.('timeZone') as string[] | undefined
    zoneList = z && z.length ? z : FALLBACK_ZONES
  } catch { zoneList = FALLBACK_ZONES }
  if (!zoneList.includes('UTC')) zoneList = ['UTC', ...zoneList]
  return zoneList
}
const FALLBACK_ZONES = ['UTC', 'Europe/London', 'Europe/Berlin', 'Europe/Sarajevo', 'Europe/Belgrade', 'Europe/Zagreb', 'Europe/Paris', 'Europe/Vienna', 'Europe/Istanbul', 'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles', 'Asia/Tokyo', 'Asia/Dubai', 'Asia/Kolkata', 'Asia/Singapore', 'Australia/Sydney']

export function zoneLabel(tz: string): string {
  try {
    const now = Date.now()
    const off = zoneOffset(tz, now)
    const sign = off >= 0 ? '+' : '−'
    const a = Math.abs(off)
    const gmt = `GMT${sign}${Math.floor(a / 60)}${a % 60 ? ':' + pad(a % 60) : ''}`
    return `${tz.replace(/_/g, ' ')} (${gmt})`
  } catch { return tz }
}

// ---------- formatting ----------

const fmtCache = new Map<string, Intl.DateTimeFormat>()
function f(opts: Intl.DateTimeFormatOptions) {
  const k = JSON.stringify(opts)
  let x = fmtCache.get(k)
  if (!x) { x = new Intl.DateTimeFormat('en-US', { ...opts, timeZone: 'UTC' }); fmtCache.set(k, x) }
  return x
}
export const fmt = (w: Wall, opts: Intl.DateTimeFormatOptions) => f(opts).format(new Date(w))

export const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']
export const MONTHS_SHORT = MONTHS.map(m => m.slice(0, 3))
export const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
export const WD_SHORT = WEEKDAYS.map(d => d.slice(0, 3))
export const WD_MIN = WEEKDAYS.map(d => d.slice(0, 2))

/** 09:15 or 9:15am — compact enough for event blocks. */
export function fmtTime(w: Wall, h12: boolean, compact = false): string {
  const d = new Date(w)
  const h = d.getUTCHours(), m = d.getUTCMinutes()
  if (!h12) return `${pad(h)}:${pad(m)}`
  const hh = h % 12 || 12
  const ap = h < 12 ? 'am' : 'pm'
  if (compact && m === 0) return `${hh}${ap}`
  return `${hh}:${pad(m)}${compact ? '' : ' '}${ap}`
}
export function fmtHour(h: number, h12: boolean): string {
  if (!h12) return `${pad(h)}:00`
  if (h === 0) return '12 AM'
  if (h === 12) return '12 PM'
  return h < 12 ? `${h} AM` : `${h - 12} PM`
}
export function fmtRange(a: Wall, b: Wall, h12: boolean): string {
  return `${fmtTime(a, h12)} – ${fmtTime(b, h12)}`
}
export function fmtMinutes(min: number): string {
  if (min < 60) return `${min} min`
  const h = Math.floor(min / 60), m = min % 60
  return m ? `${h} h ${m} min` : `${h} h`
}
/** "Tue, Oct 21" */
export function fmtDayShort(w: Wall): string { return `${WD_SHORT[weekday(w)]}, ${MONTHS_SHORT[new Date(w).getUTCMonth()]} ${new Date(w).getUTCDate()}` }
/** "Tuesday, October 21, 2026" */
export function fmtDayLong(w: Wall, withYear = true): string {
  const d = new Date(w)
  return `${WEEKDAYS[d.getUTCDay()]}, ${MONTHS[d.getUTCMonth()]} ${d.getUTCDate()}${withYear ? ', ' + d.getUTCFullYear() : ''}`
}
export function fmtDateMedium(w: Wall, withYear?: boolean): string {
  const d = new Date(w)
  return `${MONTHS_SHORT[d.getUTCMonth()]} ${d.getUTCDate()}${withYear ? ', ' + d.getUTCFullYear() : ''}`
}

/** Title for a visible range: "October 2026", "Oct 19 – 25, 2026", "Oct 30 – Nov 5, 2026". */
export function rangeTitle(from: Wall, toExcl: Wall): string {
  const to = addDays(toExcl, -1)
  const a = new Date(from), b = new Date(to)
  if (sameDay(from, to)) return fmtDayLong(from)
  if (a.getUTCFullYear() !== b.getUTCFullYear()) return `${fmtDateMedium(from, true)} – ${fmtDateMedium(to, true)}`
  if (a.getUTCMonth() !== b.getUTCMonth()) return `${fmtDateMedium(from)} – ${fmtDateMedium(to)}, ${b.getUTCFullYear()}`
  return `${MONTHS_SHORT[a.getUTCMonth()]} ${a.getUTCDate()} – ${b.getUTCDate()}, ${b.getUTCFullYear()}`
}

/** "Tue, Oct 21 · 09:15 – 09:30" or an all-day range. */
export function whenText(startS: string, endS: string, allDay: boolean, h12: boolean, today: Wall): string {
  const s = parseWall(startS), e = parseWall(endS)
  const yearOf = (w: Wall) => new Date(w).getUTCFullYear()
  const showYear = yearOf(s) !== yearOf(today)
  const day = (w: Wall) => fmtDayShort(w) + (showYear ? ', ' + yearOf(w) : '')
  if (allDay) {
    const last = addDays(e, -1)
    if (last <= s) return `${day(s)} · All day`
    return `${day(s)} – ${day(last)}`
  }
  if (sameDay(s, e) || (minutesOfDay(e) === 0 && daysBetween(s, e) === 1)) return `${day(s)} · ${fmtRange(s, e, h12)}`
  return `${day(s)}, ${fmtTime(s, h12)} – ${day(e)}, ${fmtTime(e, h12)}`
}

export function relativeDay(w: Wall, today: Wall): string {
  const n = daysBetween(today, w)
  if (n === 0) return 'Today'
  if (n === 1) return 'Tomorrow'
  if (n === -1) return 'Yesterday'
  return ''
}

// ---------- alarms ----------

/** "-PT10M" → minutes before the start (positive = before). null for absolute. */
export function alarmMinutes(trigger: string): number | null {
  const m = /^([+-])?P(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$/.exec(trigger.trim())
  if (!m) return null
  const mins = (+(m[2] || 0)) * 7 * 1440 + (+(m[3] || 0)) * 1440 + (+(m[4] || 0)) * 60 + (+(m[5] || 0)) + Math.round(+(m[6] || 0) / 60)
  return m[1] === '+' ? -mins : mins
}
export function minutesToTrigger(min: number): string {
  if (min === 0) return 'PT0M'
  const sign = min > 0 ? '-' : ''
  let a = Math.abs(min)
  if (a % (7 * 1440) === 0) return `${sign}P${a / (7 * 1440)}W`
  if (a % 1440 === 0) return `${sign}P${a / 1440}D`
  const d = Math.floor(a / 1440); a -= d * 1440
  const h = Math.floor(a / 60), m = a % 60
  return `${sign}P${d ? d + 'D' : ''}T${h ? h + 'H' : ''}${m ? m + 'M' : ''}`
}
export function alarmLabel(trigger: string, allDay = false, h12 = false): string {
  const m = alarmMinutes(trigger)
  if (m === null) {
    const w = parseWall(trigger)
    return isNaN(w) ? trigger : `At ${fmtDateMedium(w)} ${fmtTime(w, h12)}`
  }
  if (allDay && m > 0 && m % 60 === 0 && m < 7 * 1440) {
    // -PT15H on an all-day event = 09:00 the day before
    const daysBefore = Math.ceil(m / 1440)
    const at = daysBefore * 1440 - m
    const when = daysBefore === 1 ? 'The day before' : `${daysBefore} days before`
    return `${when} at ${fmtTime(Date.UTC(2000, 0, 1) + at * MIN, h12)}`
  }
  if (allDay && m === 0) return 'On the day at 00:00'
  if (m === 0) return 'At the start'
  if (m < 0) return `${durationWords(-m)} after`
  return `${durationWords(m)} before`
}
export function durationWords(min: number): string {
  if (min % (7 * 1440) === 0) { const n = min / (7 * 1440); return `${n} week${n > 1 ? 's' : ''}` }
  if (min % 1440 === 0) { const n = min / 1440; return `${n} day${n > 1 ? 's' : ''}` }
  if (min % 60 === 0) { const n = min / 60; return `${n} hour${n > 1 ? 's' : ''}` }
  if (min > 60) return `${Math.floor(min / 60)} h ${min % 60} min`
  return `${min} min`
}
