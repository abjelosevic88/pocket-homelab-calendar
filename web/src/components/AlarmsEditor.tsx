// Reminders: a list of triggers ("-PT10M") with presets and a custom entry.
import { useState } from 'react'
import { useApp } from '../lib/app-context'
import { alarmLabel, alarmMinutes, minutesToTrigger } from '../lib/dates'
import { Icon } from './icons'
import { type Option, Select } from './Select'
import { Modal } from './ui'

const TIMED = [0, 5, 10, 15, 30, 60, 120, 1440, 2880, 10080]
// all-day: minutes before midnight of the day — 9:00 the day before = 15 h, on the day 09:00 = -9 h
const ALL_DAY = [-540, 900, 2340, 9540]

export function AlarmsEditor({ value, onChange, allDay = false, max = 5 }: { value: string[]; onChange: (v: string[]) => void; allDay?: boolean; max?: number }) {
  const { h12 } = useApp()
  const [custom, setCustom] = useState(false)
  const presets = allDay ? ALL_DAY : TIMED
  const used = new Set(value.map(v => alarmMinutes(v)))
  const options: Option<string>[] = [
    ...presets.filter(m => !used.has(m)).map(m => ({ value: minutesToTrigger(m), label: alarmLabel(minutesToTrigger(m), allDay, h12) })),
    { value: '__custom', label: 'Custom…' },
  ]
  return (
    <div className="alarms">
      {value.map((t, i) => (
        <div key={t + i} className="alarm-row">
          <Icon name="bell" size={14} className="muted" />
          <span className="grow">{alarmLabel(t, allDay, h12)}</span>
          <button type="button" className="btn ghost icon-only xs" aria-label="Remove reminder" onClick={() => onChange(value.filter((_, k) => k !== i))}><Icon name="x" size={13} /></button>
        </div>
      ))}
      {value.length < max && (
        <Select<string> value="" options={options} placeholder="Add a reminder" size="sm" className="inline ghost add-alarm" label="Add a reminder" width={240}
          renderValue={() => <span className="row muted"><Icon name="plus" size={14} />Add a reminder</span>}
          onChange={v => { if (v === '__custom') setCustom(true); else onChange([...value, v]) }} />
      )}
      {custom && <CustomAlarm allDay={allDay} onClose={() => setCustom(false)} onAdd={t => { onChange([...value, t]); setCustom(false) }} />}
    </div>
  )
}

function CustomAlarm({ onAdd, onClose, allDay }: { onAdd: (t: string) => void; onClose: () => void; allDay: boolean }) {
  const [n, setN] = useState(30)
  const [unit, setUnit] = useState<'m' | 'h' | 'd' | 'w'>(allDay ? 'd' : 'm')
  const mult = { m: 1, h: 60, d: 1440, w: 10080 }[unit]
  return (
    <Modal title="Custom reminder" onClose={onClose} size="sm" footer={<>
      <button className="btn" onClick={onClose}>Cancel</button>
      <button className="btn primary" disabled={!(n >= 0)} onClick={() => onAdd(minutesToTrigger(n * mult))}>Add</button>
    </>}>
      <div className="row">
        <input type="number" min={0} max={999} value={n} onChange={e => setN(Math.max(0, +e.target.value))} style={{ width: 90 }} aria-label="How many" autoFocus />
        <Select value={unit} onChange={setUnit} options={[{ value: 'm', label: 'minutes' }, { value: 'h', label: 'hours' }, { value: 'd', label: 'days' }, { value: 'w', label: 'weeks' }]} className="inline" label="Unit" />
        <span className="muted">before</span>
      </div>
    </Modal>
  )
}
