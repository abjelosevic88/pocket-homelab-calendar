// Building and reading the RRULEs the repeat picker offers.
import { type Wall, WEEKDAYS, MONTHS, weekday, dayKey, parseWall, fmtDateMedium } from './dates'

export const BYDAY = ['SU', 'MO', 'TU', 'WE', 'TH', 'FR', 'SA']
const ORD = ['', 'first', 'second', 'third', 'fourth', 'fifth']

export interface Rule {
  freq: 'DAILY' | 'WEEKLY' | 'MONTHLY' | 'YEARLY'
  interval: number
  byday: string[]        // ["MO","WE"] or ["2TU"] for monthly
  bymonthday?: number
  until?: string         // YYYY-MM-DD
  count?: number
}

export function parseRule(s: string): Rule | null {
  if (!s) return null
  const p: Record<string, string> = {}
  for (const part of s.replace(/^RRULE:/, '').split(';')) { const [k, v] = part.split('='); if (k) p[k.toUpperCase()] = v || '' }
  const freq = p.FREQ as Rule['freq']
  if (!['DAILY', 'WEEKLY', 'MONTHLY', 'YEARLY'].includes(freq)) return null
  const r: Rule = { freq, interval: +(p.INTERVAL || 1) || 1, byday: p.BYDAY ? p.BYDAY.split(',') : [] }
  if (p.BYMONTHDAY) r.bymonthday = +p.BYMONTHDAY
  if (p.COUNT) r.count = +p.COUNT
  if (p.UNTIL) r.until = `${p.UNTIL.slice(0, 4)}-${p.UNTIL.slice(4, 6)}-${p.UNTIL.slice(6, 8)}`
  return r
}

export function buildRule(r: Rule): string {
  const parts = [`FREQ=${r.freq}`]
  if (r.interval > 1) parts.push(`INTERVAL=${r.interval}`)
  const order = (d: string) => (BYDAY.indexOf(d.slice(-2)) + 6) % 7
  if (r.byday.length) parts.push(`BYDAY=${[...r.byday].sort((a, b) => order(a) - order(b)).join(',')}`)
  if (r.bymonthday) parts.push(`BYMONTHDAY=${r.bymonthday}`)
  if (r.count) parts.push(`COUNT=${r.count}`)
  else if (r.until) parts.push(`UNTIL=${r.until.replace(/-/g, '')}T235959Z`)
  return parts.join(';')
}

/** Which "nth weekday" a date is (1..5), and whether it is the last one. */
export function nthOfMonth(w: Wall): { n: number; last: boolean } {
  const d = new Date(w).getUTCDate()
  const dim = new Date(Date.UTC(new Date(w).getUTCFullYear(), new Date(w).getUTCMonth() + 1, 0)).getUTCDate()
  return { n: Math.ceil(d / 7), last: d + 7 > dim }
}

export interface Preset { id: string; label: string; rrule: string }

export function presets(start: Wall): Preset[] {
  const wd = weekday(start)
  const { n, last } = nthOfMonth(start)
  const d = new Date(start)
  const out: Preset[] = [
    { id: 'none', label: 'Does not repeat', rrule: '' },
    { id: 'daily', label: 'Every day', rrule: 'FREQ=DAILY' },
    { id: 'weekdays', label: 'Every weekday (Mon – Fri)', rrule: 'FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR' },
    { id: 'weekly', label: `Weekly on ${WEEKDAYS[wd]}`, rrule: `FREQ=WEEKLY;BYDAY=${BYDAY[wd]}` },
    { id: 'biweekly', label: `Every 2 weeks on ${WEEKDAYS[wd]}`, rrule: `FREQ=WEEKLY;INTERVAL=2;BYDAY=${BYDAY[wd]}` },
    { id: 'monthly-day', label: `Monthly on day ${d.getUTCDate()}`, rrule: `FREQ=MONTHLY;BYMONTHDAY=${d.getUTCDate()}` },
  ]
  if (n <= 4) out.push({ id: 'monthly-nth', label: `Monthly on the ${ORD[n]} ${WEEKDAYS[wd]}`, rrule: `FREQ=MONTHLY;BYDAY=${n}${BYDAY[wd]}` })
  if (last) out.push({ id: 'monthly-last', label: `Monthly on the last ${WEEKDAYS[wd]}`, rrule: `FREQ=MONTHLY;BYDAY=-1${BYDAY[wd]}` })
  out.push({ id: 'yearly', label: `Annually on ${MONTHS[d.getUTCMonth()]} ${d.getUTCDate()}`, rrule: 'FREQ=YEARLY' })
  return out
}

const UNIT: Record<Rule['freq'], [string, string]> = { DAILY: ['day', 'days'], WEEKLY: ['week', 'weeks'], MONTHLY: ['month', 'months'], YEARLY: ['year', 'years'] }

/** A readable sentence for any rule the picker can build. */
export function describeRule(s: string, start: Wall): string {
  const r = parseRule(s)
  if (!r) return s ? 'Custom' : 'Does not repeat'
  const match = presets(start).find(p => p.rrule === s)
  if (match) return match.label
  const [one, many] = UNIT[r.freq]
  let txt = r.interval > 1 ? `Every ${r.interval} ${many}` : `Every ${one}`
  if (r.freq === 'WEEKLY' && r.byday.length) {
    const days = BYDAY.map((b, i) => r.byday.includes(b) ? WEEKDAYS[i].slice(0, 3) : '').filter(Boolean)
    txt += ` on ${days.join(', ')}`
  }
  if (r.freq === 'MONTHLY') {
    if (r.bymonthday) txt += ` on day ${r.bymonthday}`
    else if (r.byday.length) {
      const m = /^(-?\d)?([A-Z]{2})$/.exec(r.byday[0])
      if (m) { const n = +(m[1] || 1); txt += ` on the ${n === -1 ? 'last' : ORD[n]} ${WEEKDAYS[BYDAY.indexOf(m[2])]}` }
    }
  }
  if (r.count) txt += `, ${r.count} times`
  else if (r.until) txt += `, until ${fmtDateMedium(parseWall(r.until), true)}`
  return txt
}

export function defaultRule(start: Wall): Rule {
  return { freq: 'WEEKLY', interval: 1, byday: [BYDAY[weekday(start)]] }
}

export function untilDefault(start: Wall): string { return dayKey(start + 90 * 86400000) }
