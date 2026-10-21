import { useEffect, useState } from 'react'
import { useApp } from '../../lib/app-context'
import { browserZone, WEEKDAYS } from '../../lib/dates'
import type { ViewName } from '../../lib/types'
import { AlarmsEditor } from '../../components/AlarmsEditor'
import { Icon } from '../../components/icons'
import { Chips, ColorPalette, CountrySelect, hourOptions, TimezoneSelect } from '../../components/pickers'
import { type Option, Select } from '../../components/Select'
import { Loading, Segmented, SettingRow, Switch } from '../../components/ui'
import { Section, useSaveSettings } from './common'

const VIEWS: Option<ViewName>[] = [
  { value: 'day', label: 'Day' }, { value: '3day', label: '3 days' }, { value: 'week', label: 'Week' }, { value: 'month', label: 'Month' },
  { value: 'year', label: 'Year' }, { value: 'agenda', label: 'Agenda' }, { value: 'people', label: 'People' },
]
const DURATIONS: Option<number>[] = [15, 30, 45, 60, 90, 120].map(m => ({ value: m, label: m < 60 ? `${m} minutes` : m === 60 ? '1 hour' : m === 90 ? '1½ hours' : '2 hours' }))

export function GeneralTab() {
  const { sr, settings, user, calendars, h12, themeMode, setThemeMode } = useApp()
  const save = useSaveSettings()
  const [title, setTitle] = useState(settings?.title || '')
  useEffect(() => { setTitle(settings?.title || '') }, [settings?.title])
  if (!sr || !settings) return <Loading />
  const s = settings
  const ws = s.week_start
  const dayOrder = Array.from({ length: 7 }, (_, i) => (ws + i) % 7)
  const writable = calendars.filter(c => c.writable)
  const calOpts: Option<number>[] = [{ value: 0, label: 'Your default calendar', description: 'The one marked as default in the calendar list' },
    ...writable.map(c => ({ value: c.id, label: c.name, color: c.color, description: c.role !== 'owner' ? `Shared by ${c.owner?.name || c.owner?.email}` : undefined }))]
  const endOpts = hourOptions(h12, 1, 24).map(o => o.value === 24 ? { ...o, label: h12 ? '12 AM (midnight)' : '24:00' } : o)

  return (
    <div className="stack loose">
      <Section title="Appearance">
        <SettingRow title="Theme" help="System follows your device’s light or dark setting.">
          <Segmented label="Theme" value={themeMode} onChange={setThemeMode} options={[
            { value: 'system', label: <span className="row"><Icon name="monitor" size={14} />System</span> },
            { value: 'light', label: <span className="row"><Icon name="sun" size={14} />Light</span> },
            { value: 'dark', label: <span className="row"><Icon name="moon" size={14} />Dark</span> },
          ]} />
        </SettingRow>
        <SettingRow title="Density" help="Compact fits more hours on the screen.">
          <Segmented label="Density" value={s.density} onChange={v => save({ density: v })} options={[{ value: 'comfortable', label: 'Comfortable' }, { value: 'compact', label: 'Compact' }]} />
        </SettingRow>
        <SettingRow title="Default view" help="What opens when you start the app (phones start in 3 days or day view).">
          <Select value={s.default_view} options={VIEWS} onChange={v => save({ default_view: v })} label="Default view" />
        </SettingRow>
        <SettingRow title="Show weekends"><Switch label="Show weekends" checked={s.show_weekends} onChange={v => save({ show_weekends: v })} /></SettingRow>
        <SettingRow title="Show week numbers" help="ISO week numbers next to weeks and in the mini calendar."><Switch label="Show week numbers" checked={s.show_week_numbers} onChange={v => save({ show_week_numbers: v })} /></SettingRow>
        <SettingRow title="Show declined events" help="Events you said no to stay on the calendar, struck through."><Switch label="Show declined events" checked={s.show_declined} onChange={v => save({ show_declined: v })} /></SettingRow>
        <SettingRow title="Invitations colour" help="Events you are invited to from someone else’s calendar.">
          <ColorPalette value={s.invites_color} onChange={c => c && save({ invites_color: c })} />
        </SettingRow>
      </Section>

      <Section title="Date & time">
        <SettingRow title="Time zone" help={<>Times are shown in this zone. Right now: <b>{s.timezone || browserZone()}</b>{sr.effective_tz && sr.effective_tz !== (s.timezone || browserZone()) ? <> (reminders and feeds use {sr.effective_tz} until you choose one)</> : null}.</>}>
          <TimezoneSelect value={s.timezone} onChange={v => save({ timezone: v })} allowEmpty emptyLabel={`Browser time zone (${browserZone()})`} />
        </SettingRow>
        <SettingRow title="Week starts on">
          <Segmented label="Week starts on" value={s.week_start} onChange={v => save({ week_start: v })} options={[{ value: 1, label: 'Monday' }, { value: 0, label: 'Sunday' }, { value: 6, label: 'Saturday' }]} />
        </SettingRow>
        <SettingRow title="Time format">
          <Segmented label="Time format" value={s.time_format} onChange={v => save({ time_format: v })} options={[{ value: '24h', label: '13:00' }, { value: '12h', label: '1:00 PM' }]} />
        </SettingRow>
        <SettingRow title="Public holidays" help="Shown as small labels in the day headers.">
          <CountrySelect value={s.holiday_country} onChange={v => save({ holiday_country: v })} emptyLabel={`Box default (${sr.box_holidays || 'none'})`} />
        </SettingRow>
      </Section>

      <Section title="Working hours" sub="Hours outside are shaded, and the day view opens at the start of your day.">
        <SettingRow title="Hours">
          <Select value={s.work_start} options={hourOptions(h12, 0, 23)} onChange={v => save({ work_start: v })} label="Working day starts" className="inline" />
          <span className="muted">to</span>
          <Select value={s.work_end} options={endOpts} onChange={v => save({ work_end: v })} label="Working day ends" className="inline" />
        </SettingRow>
        <SettingRow title="Days" stack>
          <Chips multi value={s.work_days || []} onChange={v => save({ work_days: [...v].sort() })} options={dayOrder.map(d => ({ value: d, label: WEEKDAYS[d].slice(0, 3) }))} />
        </SettingRow>
      </Section>

      <Section title="New events">
        <SettingRow title="Default calendar" help="Where new events go unless you pick another.">
          <Select value={s.default_calendar_id || 0} options={calOpts} onChange={v => save({ default_calendar_id: v })} label="Default calendar" />
        </SettingRow>
        <SettingRow title="Default length">
          <Select value={s.default_duration_min} options={DURATIONS.some(d => d.value === s.default_duration_min) ? DURATIONS : [...DURATIONS, { value: s.default_duration_min, label: `${s.default_duration_min} minutes` }]} onChange={v => save({ default_duration_min: v })} label="Default length" />
        </SettingRow>
        <SettingRow title="Reminders for timed events" help="Added to every new event unless the calendar has its own." stack>
          <AlarmsEditor value={s.default_alarms || []} onChange={v => save({ default_alarms: v })} />
        </SettingRow>
        <SettingRow title="Reminders for all-day events" stack>
          <AlarmsEditor allDay value={s.all_day_alarms || []} onChange={v => save({ all_day_alarms: v })} />
        </SettingRow>
      </Section>

      <Section title="Sharing on this box">
        <SettingRow title="Show my free/busy to everyone on this box" help="People here can see when you are busy — only grey blocks, never titles, places or notes — so they can find a time that suits you. Calendars you share explicitly are not affected.">
          <Switch label="Show my free/busy to everyone on this box" checked={s.freebusy_shared} onChange={v => save({ freebusy_shared: v })} />
        </SettingRow>
      </Section>

      {user?.role === 'admin' && (
        <Section title="This box" sub="Only admins see this.">
          <SettingRow title="App title" help="Shown in the browser tab and on the sign-in page.">
            <input value={title} onChange={e => setTitle(e.target.value)} onBlur={() => { if (title.trim() && title !== s.title) save({ title: title.trim() }, 'Title saved') }}
              onKeyDown={e => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }} style={{ width: 240 }} aria-label="App title" />
          </SettingRow>
        </Section>
      )}
    </div>
  )
}
