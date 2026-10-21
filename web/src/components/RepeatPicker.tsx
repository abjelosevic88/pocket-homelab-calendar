import { useMemo, useState } from 'react'
import { type Wall, WD_MIN } from '../lib/dates'
import { BYDAY, type Rule, buildRule, defaultRule, describeRule, nthOfMonth, parseRule, presets, untilDefault } from '../lib/rrule'
import { DatePicker } from './pickers'
import { type Option, Select } from './Select'
import { Field, Modal, Segmented } from './ui'

export function RepeatPicker({ value, start, onChange, weekStart }: { value: string; start: Wall; onChange: (r: string) => void; weekStart: number }) {
  const [custom, setCustom] = useState(false)
  const list = presets(start)
  const options = useMemo<Option<string>[]>(() => {
    const o: Option<string>[] = list.map(p => ({ value: p.rrule, label: p.label }))
    if (value && !list.some(p => p.rrule === value)) o.push({ value, label: describeRule(value, start) })
    o.push({ value: '__custom', label: 'Custom…' })
    return o
  }, [value, start]) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <>
      <Select value={value} options={options} onChange={v => v === '__custom' ? setCustom(true) : onChange(v)} label="Repeat" className="inline ghost" width={280} />
      {custom && <CustomRepeat start={start} initial={value} weekStart={weekStart} onClose={() => setCustom(false)} onDone={r => { onChange(r); setCustom(false) }} />}
    </>
  )
}

function CustomRepeat({ start, initial, onClose, onDone, weekStart }: { start: Wall; initial: string; onClose: () => void; onDone: (r: string) => void; weekStart: number }) {
  const [rule, setRule] = useState<Rule>(() => parseRule(initial) || defaultRule(start))
  const [ends, setEnds] = useState<'never' | 'on' | 'after'>(rule.count ? 'after' : rule.until ? 'on' : 'never')
  const set = (p: Partial<Rule>) => setRule(r => ({ ...r, ...p }))
  const wd = new Date(start).getUTCDay()
  const { n, last } = nthOfMonth(start)
  const monthlyMode = rule.byday.length ? 'nth' : 'day'
  const out = (): string => {
    const r: Rule = { ...rule, until: ends === 'on' ? rule.until || untilDefault(start) : undefined, count: ends === 'after' ? rule.count || 10 : undefined }
    if (r.freq !== 'WEEKLY' && r.freq !== 'MONTHLY') r.byday = []
    if (r.freq !== 'MONTHLY') r.bymonthday = undefined
    if (r.freq === 'WEEKLY' && !r.byday.length) r.byday = [BYDAY[wd]]
    return buildRule(r)
  }
  return (
    <Modal title="Custom repeat" onClose={onClose} size="sm" footer={<>
      <button className="btn" onClick={onClose}>Cancel</button>
      <button className="btn primary" onClick={() => onDone(out())}>Done</button>
    </>}>
      <div className="stack">
        <div className="row">
          <span>Every</span>
          <input type="number" min={1} max={99} value={rule.interval} onChange={e => set({ interval: Math.max(1, +e.target.value || 1) })} style={{ width: 72 }} aria-label="Interval" />
          <div style={{ width: 150 }}>
            <Select value={rule.freq} onChange={f => set({ freq: f, byday: f === 'WEEKLY' ? [BYDAY[wd]] : [], bymonthday: f === 'MONTHLY' ? new Date(start).getUTCDate() : undefined })}
              options={[{ value: 'DAILY', label: rule.interval > 1 ? 'days' : 'day' }, { value: 'WEEKLY', label: rule.interval > 1 ? 'weeks' : 'week' }, { value: 'MONTHLY', label: rule.interval > 1 ? 'months' : 'month' }, { value: 'YEARLY', label: rule.interval > 1 ? 'years' : 'year' }]} label="Frequency" />
          </div>
        </div>
        {rule.freq === 'WEEKLY' && (
          <Field label="On" group>
            <div className="weekday-picker">
              {Array.from({ length: 7 }, (_, i) => (weekStart + i) % 7).map(d => {
                const on = rule.byday.includes(BYDAY[d])
                return <button key={d} type="button" aria-pressed={on} className={`wd-toggle ${on ? 'on' : ''}`} onClick={() => set({ byday: on ? rule.byday.filter(x => x !== BYDAY[d]) : [...rule.byday, BYDAY[d]] })}>{WD_MIN[d]}</button>
              })}
            </div>
          </Field>
        )}
        {rule.freq === 'MONTHLY' && (
          <Select value={monthlyMode} onChange={m => m === 'day' ? set({ byday: [], bymonthday: new Date(start).getUTCDate() }) : set({ bymonthday: undefined, byday: [`${last && n > 4 ? -1 : n}${BYDAY[wd]}`] })}
            options={[{ value: 'day', label: `On day ${new Date(start).getUTCDate()}` }, { value: 'nth', label: describeRule(`FREQ=MONTHLY;BYDAY=${last && n > 4 ? -1 : n}${BYDAY[wd]}`, start).replace(/^Monthly on|^Every month on/, 'On') }]} label="Monthly on" />
        )}
        <Field label="Ends" group>
          <Segmented value={ends} onChange={setEnds} options={[{ value: 'never', label: 'Never' }, { value: 'on', label: 'On a date' }, { value: 'after', label: 'After' }]} />
        </Field>
        {ends === 'on' && <DatePicker value={rule.until || untilDefault(start)} onChange={v => set({ until: v })} label="End date" />}
        {ends === 'after' && <div className="row"><input type="number" min={1} max={999} value={rule.count || 10} onChange={e => set({ count: Math.max(1, +e.target.value || 1) })} style={{ width: 90 }} aria-label="Occurrences" /><span className="muted">times</span></div>}
        <p className="repeat-summary"><b>{describeRule(out(), start)}</b></p>
      </div>
    </Modal>
  )
}
