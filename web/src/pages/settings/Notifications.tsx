import { useEffect, useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { Settings } from '../../lib/types'
import { Icon } from '../../components/icons'
import { parseTimeInput, TimePicker } from '../../components/pickers'
import { errMsg, Field, Loading, Segmented, SettingRow, Spinner, Switch, useToast } from '../../components/ui'
import { Section, useSaveSettings } from './common'

const KINDS: { value: Settings['notify_kind']; label: string; help: string; placeholder: string; token: string }[] = [
  { value: 'apprise', label: 'Apprise', help: 'An Apprise API route — it fans out to ntfy, Pushover, Telegram, email and 100+ others.', placeholder: 'https://apprise.example.com/notify/calendar', token: 'Usually not needed' },
  { value: 'ntfy', label: 'ntfy', help: 'An ntfy topic URL. Install the ntfy app and subscribe to the same topic.', placeholder: 'https://ntfy.sh/my-calendar-topic', token: 'Access token for a protected topic (optional)' },
  { value: 'webhook', label: 'Webhook', help: 'A JSON POST with title, body and link to any URL you choose.', placeholder: 'https://example.com/hooks/calendar', token: 'Sent as a Bearer token (optional)' },
]

export function NotificationsTab() {
  const { sr, settings, calendars, patchCalendar } = useApp()
  const save = useSaveSettings()
  const { toast } = useToast()
  const [url, setUrl] = useState(settings?.notify_url || '')
  const [token, setToken] = useState('')
  const [testing, setTesting] = useState(false)
  useEffect(() => { setUrl(settings?.notify_url || '') }, [settings?.notify_url])
  if (!sr || !settings) return <Loading />
  const s = settings
  const kind = KINDS.find(k => k.value === s.notify_kind) || KINDS[0]
  const digestOn = !!s.digest_time
  const digestMin = digestOn ? (parseTimeInput(s.digest_time) ?? 450) : 450
  const hhmm = (m: number) => `${String(Math.floor(m / 60) % 24).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`
  const dirty = url !== s.notify_url || token !== ''

  const saveRoute = async () => {
    const patch: Partial<Settings> & { notify_token?: string } = { notify_url: url.trim() }
    if (token) patch.notify_token = token
    if (await save(patch, 'Notification address saved')) setToken('')
  }
  const test = async () => {
    setTesting(true)
    try { await api.post(`${V1}/notify/test`); toast('Test sent — check your phone') } catch (e) { toast(errMsg(e), 'err') } finally { setTesting(false) }
  }

  return (
    <div className="stack loose">
      <Section title="Where to send" sub="Reminders are pushed from the server, so they arrive even when no app is open.">
        {sr.notify_box_route && <div className="callout info mb"><Icon name="info" size={15} /><span>This box already has a notification route set by the admin. It is used for you until you set your own address here.</span></div>}
        <div className="stack">
          <Segmented label="Service" value={s.notify_kind} onChange={v => save({ notify_kind: v })} options={KINDS.map(k => ({ value: k.value, label: k.label }))} />
          <p className="muted small" style={{ margin: 0 }}>{kind.help}</p>
          <Field label="Address"><input value={url} onChange={e => setUrl(e.target.value)} placeholder={kind.placeholder} inputMode="url" autoComplete="off" spellCheck={false} /></Field>
          <Field label="Token" help={kind.token}><input type="password" value={token} onChange={e => setToken(e.target.value)} placeholder={sr.notify_has_token ? '•••• saved — type to replace' : 'Optional'} autoComplete="new-password" /></Field>
          <div className="row">
            <button className="btn primary" disabled={!dirty} onClick={saveRoute}>Save</button>
            <button className="btn" disabled={testing || (!s.notify_url && !sr.notify_box_route)} onClick={test}>{testing ? <Spinner /> : <Icon name="bell" size={14} />}Send a test</button>
            {sr.notify_has_token && <button className="btn ghost sm" onClick={() => save({ notify_token: '' }, 'Token removed')}>Remove token</button>}
          </div>
        </div>
      </Section>

      <Section title="What to send">
        <SettingRow title="Event reminders" help="The reminders set on each event (for calendars with reminders on below).">
          <Switch label="Event reminders" checked={s.notify_alarms} onChange={v => save({ notify_alarms: v })} />
        </SettingRow>
        <SettingRow title="Invitations and answers" help="When someone invites you, and when people answer your invitations.">
          <Switch label="Invitations and answers" checked={s.notify_invites} onChange={v => save({ notify_invites: v })} />
        </SettingRow>
        <SettingRow title="Morning agenda" help="A short list of the day’s events, every morning.">
          {digestOn && <TimePicker value={digestMin} onChange={m => save({ digest_time: hhmm(m) })} label="Agenda time" className="set-time" />}
          <Switch label="Morning agenda" checked={digestOn} onChange={v => save({ digest_time: v ? hhmm(digestMin) : '' })} />
        </SettingRow>
      </Section>

      <Section title="Reminders per calendar" sub="Turn reminders off for calendars you only glance at.">
        {calendars.map(c => (
          <SettingRow key={c.id} title={<span className="row"><span className="dot" style={{ background: c.color }} />{c.name}</span>} help={c.role !== 'owner' ? `Shared by ${c.owner?.name || c.owner?.email}` : undefined}>
            <Switch label={`Reminders for ${c.name}`} checked={c.notify} onChange={v => patchCalendar(c.id, { notify: v }).catch(e => toast(errMsg(e), 'err'))} />
          </SettingRow>
        ))}
      </Section>
    </div>
  )
}
