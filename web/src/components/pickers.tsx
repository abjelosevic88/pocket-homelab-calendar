import React, { useMemo, useRef, useState } from 'react'
import { useApp } from '../lib/app-context'
import { type Wall, addDays, allZones, dayKey, fmtMinutes, fmtTime, MIN, parseWall, todayWall, WD_SHORT, MONTHS_SHORT, zoneLabel } from '../lib/dates'
import type { Calendar } from '../lib/types'
import { Icon } from './icons'
import { MiniMonth } from './MiniMonth'
import { type Option, OptionList, Select } from './Select'
import { type Anchor, Popover, rectOf } from './ui'

export const PALETTE = ['#3b82f6', '#6366f1', '#8b5cf6', '#d946ef', '#ec4899', '#ef4444', '#f97316', '#f59e0b', '#eab308', '#84cc16', '#10b981', '#14b8a6', '#06b6d4', '#64748b']

// ---------- date ----------

export function DatePicker({ value, onChange, label, min, className = '' }: { value: string; onChange: (v: string) => void; label?: string; min?: string; className?: string }) {
  const { settings, tz } = useApp()
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  const w = value ? parseWall(value) : todayWall(tz)
  const [month, setMonth] = useState(w)
  const d = new Date(w)
  const text = value ? `${WD_SHORT[d.getUTCDay()]}, ${MONTHS_SHORT[d.getUTCMonth()]} ${d.getUTCDate()}, ${d.getUTCFullYear()}` : 'Pick a date'
  return (
    <>
      <button type="button" className={`picker-btn ${className}`} aria-label={label} onClick={e => { setMonth(w); setAnchor(anchor ? null : rectOf(e.currentTarget)) }}>
        <span className="tnum">{text}</span>
      </button>
      {anchor && (
        <Popover anchor={anchor} prefer="below" width={268} onClose={() => setAnchor(null)} className="date-pop" label="Choose a date">
          <MiniMonth month={month} onMonth={setMonth} selected={w} today={todayWall(tz)} weekStart={settings?.week_start ?? 1}
            onPick={x => { if (min && dayKey(x) < min) return; onChange(dayKey(x)); setAnchor(null) }} />
          <div className="date-pop-foot">
            <button type="button" className="btn ghost sm" onClick={() => { onChange(dayKey(todayWall(tz))); setAnchor(null) }}>Today</button>
            <button type="button" className="btn ghost sm" onClick={() => { onChange(dayKey(addDays(todayWall(tz), 1))); setAnchor(null) }}>Tomorrow</button>
          </div>
        </Popover>
      )}
    </>
  )
}

// ---------- time ----------

/** "9", "930", "9:30", "9.30", "2pm", "14h" → minutes after midnight. */
export function parseTimeInput(s: string): number | null {
  const t = s.trim().toLowerCase().replace(/\s+/g, '')
  const m = /^(\d{1,2})(?:[:.h]?(\d{2}))?(am|pm|a|p)?$/.exec(t)
  if (!m) return null
  let h = +m[1]
  const mi = +(m[2] || 0)
  if (mi > 59) return null
  if (m[3]) { if (h > 12 || h === 0) return null; if (m[3].startsWith('p') && h < 12) h += 12; if (m[3].startsWith('a') && h === 12) h = 0 }
  if (h > 24 || (h === 24 && mi > 0)) return null
  return h * 60 + mi
}

const base = Date.UTC(2000, 0, 1)

/** A time field you can type into, with a 15-minute list (and durations for an end time). */
export function TimePicker({ value, onChange, from, label, className = '' }: { value: number; onChange: (min: number) => void; from?: number; label?: string; className?: string }) {
  const { h12 } = useApp()
  const [text, setText] = useState<string | null>(null)
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  const ref = useRef<HTMLInputElement>(null)
  const shown = fmtTime(base + value * MIN, h12)
  const options = useMemo<Option<number>[]>(() => {
    const out: Option<number>[] = []
    const startAt = from !== undefined ? from : 0
    for (let m = startAt; m < startAt + 24 * 60; m += 15) {
      if (from !== undefined && m <= from) continue
      if (from === undefined && m >= 24 * 60) break
      const mm = m % (24 * 60)
      out.push({ value: m, label: fmtTime(base + mm * MIN, h12), hint: from !== undefined ? fmtMinutes(m - from) : undefined })
      if (from !== undefined && m - from >= 24 * 60) break
    }
    return out
  }, [from, h12])
  const commit = () => {
    if (text === null) return
    const v = parseTimeInput(text)
    if (v !== null) onChange(v)
    setText(null)
  }
  return (
    <>
      <input ref={ref} className={`time-input tnum ${className}`} aria-label={label} value={text ?? shown}
        onFocus={e => { setText(shown); setAnchor(rectOf(e.currentTarget)); requestAnimationFrame(() => e.target.select()) }}
        onChange={e => setText(e.target.value)}
        onBlur={() => { commit(); setTimeout(() => setAnchor(null), 120) }}
        onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); commit(); setAnchor(null); ref.current?.blur() } if (e.key === 'Escape') { setText(null); setAnchor(null) } }} />
      {anchor && <OptionList anchor={anchor} options={options} value={options.reduce((best, o) => Math.abs(o.value - value) < Math.abs(best - value) ? o.value : best, options[0]?.value ?? 0)} search={false} width={from !== undefined ? 170 : 120}
        onPick={v => { onChange(v); setText(null); setAnchor(null); ref.current?.blur() }} onClose={() => setAnchor(null)} />}
    </>
  )
}

// ---------- time zones ----------

export function TimezoneSelect({ value, onChange, allowEmpty, emptyLabel = 'Use the browser’s zone', className = '' }: { value: string; onChange: (v: string) => void; allowEmpty?: boolean; emptyLabel?: string; className?: string }) {
  const options = useMemo<Option<string>[]>(() => {
    const zones = allZones()
    const list: Option<string>[] = zones.map(z => ({ value: z, label: z.replace(/_/g, ' '), keywords: z.split('/').pop() || '', hint: zoneLabel(z).replace(/^.*\(/, '').replace(')', '') }))
    if (value && !zones.includes(value)) list.unshift({ value, label: value })
    if (allowEmpty) list.unshift({ value: '', label: emptyLabel })
    return list
  }, [value, allowEmpty, emptyLabel])
  return <Select value={value} options={options} onChange={onChange} searchable width={320} className={className} label="Time zone" />
}

// ---------- calendars ----------

export function calendarOptions(cals: Calendar[], writableOnly = true): Option<number>[] {
  return cals.filter(c => !writableOnly || c.writable).map(c => ({
    value: c.id, label: c.name, color: c.color,
    group: c.role === 'owner' ? 'My calendars' : c.account_id ? 'Other calendars' : 'Shared with me',
    description: c.role !== 'owner' ? c.owner?.name || c.owner?.email : undefined,
  }))
}

export function CalendarSelect({ value, onChange, className = '' }: { value: number; onChange: (id: number) => void; className?: string }) {
  const { calendars } = useApp()
  return <Select value={value} options={calendarOptions(calendars)} onChange={onChange} className={className} label="Calendar" placeholder="Choose a calendar" />
}

// ---------- colours ----------

export function ColorPalette({ value, onChange, allowNone, noneLabel = 'Calendar colour', custom = true }: { value: string; onChange: (c: string) => void; allowNone?: boolean; noneLabel?: string; custom?: boolean }) {
  const lower = (value || '').toLowerCase()
  const isCustom = !!value && !PALETTE.includes(lower)
  return (
    <div className="palette" role="radiogroup" aria-label="Colour">
      {allowNone && <button type="button" role="radio" aria-checked={!value} className={`swatch none ${!value ? 'active' : ''}`} title={noneLabel} aria-label={noneLabel} onClick={() => onChange('')}><Icon name="x" size={12} /></button>}
      {PALETTE.map(c => <button key={c} type="button" role="radio" aria-checked={lower === c} aria-label={c} className={`swatch ${lower === c ? 'active' : ''}`} style={{ background: c }} onClick={() => onChange(c)} />)}
      {custom && (
        <label className={`swatch custom ${isCustom ? 'active' : ''}`} title="Custom colour" style={isCustom ? { background: value } : undefined}>
          {!isCustom && <Icon name="plus" size={12} />}
          <input type="color" value={value || '#3b82f6'} onChange={e => onChange(e.target.value)} aria-label="Custom colour" />
        </label>
      )}
    </div>
  )
}

// ---------- countries ----------

const COUNTRY_CODES = 'AD AL AM AR AT AU AX BA BB BE BG BJ BO BR BS BW BY BZ CA CD CG CH CL CN CO CR CU CY CZ DE DK DO EC EE EG ES FI FO FR GA GB GD GE GG GH GI GL GM GR GT GY HK HN HR HT HU ID IE IM IS IT JE JM JP KE KR KZ LI LS LT LU LV MA MC MD ME MG MK MN MS MT MX MZ NA NE NG NI NL NO NZ PA PE PG PH PL PR PT PY RO RS RU SE SG SI SJ SK SM SR SV TN TR UA US UY VA VE VN ZA ZW'.split(' ')

export function CountrySelect({ value, onChange, emptyLabel }: { value: string; onChange: (v: string) => void; emptyLabel: string }) {
  const options = useMemo<Option<string>[]>(() => {
    let names: Intl.DisplayNames | null = null
    try { names = new Intl.DisplayNames(['en'], { type: 'region' }) } catch { names = null }
    const list = COUNTRY_CODES.map(c => ({ value: c, label: names?.of(c) || c, keywords: c, hint: flag(c) + ' ' + c }))
    list.sort((a, b) => a.label.localeCompare(b.label))
    return [{ value: '', label: emptyLabel }, ...list]
  }, [emptyLabel])
  return <Select value={value} options={options} onChange={onChange} searchable width={300} label="Country" />
}

export function flag(code: string): string {
  if (code.length !== 2) return ''
  return String.fromCodePoint(...[...code.toUpperCase()].map(c => 0x1f1e6 + c.charCodeAt(0) - 65))
}

// ---------- small helpers ----------

export function hourOptions(h12: boolean, from = 0, to = 23): Option<number>[] {
  const out: Option<number>[] = []
  for (let h = from; h <= to; h++) out.push({ value: h, label: fmtTime(base + h * 60 * MIN, h12) })
  return out
}

export function Chips<T extends string | number>({ options, value, onChange, multi }: { options: { value: T; label: React.ReactNode }[]; value: T[]; onChange: (v: T[]) => void; multi?: boolean }) {
  return (
    <div className="chips">
      {options.map(o => {
        const on = value.includes(o.value)
        return <button key={String(o.value)} type="button" aria-pressed={on} className={`chip ${on ? 'active' : ''}`}
          onClick={() => onChange(multi ? (on ? value.filter(v => v !== o.value) : [...value, o.value]) : [o.value])}>{o.label}</button>
      })}
    </div>
  )
}

export const wallMinutes = (w: Wall) => Math.round((w - Math.floor(w / 86400000) * 86400000) / MIN)
