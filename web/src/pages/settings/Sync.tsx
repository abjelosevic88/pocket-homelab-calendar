import { useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { Feeds } from '../../lib/types'
import { Icon } from '../../components/icons'
import { Confirm, CopyField, errMsg, Field, Loading, useAsync, useToast } from '../../components/ui'
import { Section } from './common'

export function SyncTab({ goTokens, goProfile }: { goTokens: () => void; goProfile: () => void }) {
  const { status, calendars } = useApp()
  const { toast } = useToast()
  const feeds = useAsync(() => api.get<Feeds>(`${V1}/feeds`), [])
  const [rotate, setRotate] = useState(false)
  const f = feeds.data
  if (feeds.error) return <div className="callout danger"><Icon name="alert" size={15} /><span>{feeds.error}</span></div>
  if (!f) return <Loading />
  let host = f.caldav_url
  try { host = new URL(f.caldav_url).host } catch { /* keep the URL */ }
  const withFeeds = calendars.filter(c => c.feed_url)

  return (
    <div className="stack loose">
      <Section title="CalDAV" sub="Two-way sync with the calendar apps on your phone and computer. Changes show up on both sides within moments.">
        <div className="stack">
          <Field label="Server address" group><CopyField value={f.caldav_url} label="CalDAV server address" /></Field>
          <Field label="User name" group help={<>Your CalDAV name — <button type="button" className="link-btn" onClick={goProfile}>change it in Profile</button>.</>}><CopyField value={f.caldav_user} label="CalDAV user name" /></Field>
          <div className="callout info">
            <Icon name="key" size={15} />
            <span><b>Password:</b> create an API token and use it as the password — <button type="button" className="link-btn" onClick={goTokens}>open Tokens</button>.{status?.auth_mode === 'local' ? ' Your account password works too.' : ''} A separate token per device lets you revoke one without touching the others.</span>
          </div>
          <div className="set-howto">
            <details>
              <summary><Icon name="smartphone" size={15} />iPhone and iPad</summary>
              <ol>
                <li>Open <b>Settings → Calendar → Accounts → Add Account → Other</b>.</li>
                <li>Tap <b>Add CalDAV Account</b>.</li>
                <li>Server: <code>{host}</code>, user name: <code>{f.caldav_user}</code>, password: your token.</li>
                <li>Tap <b>Next</b> — your calendars appear in the Calendar app.</li>
              </ol>
            </details>
            <details>
              <summary><Icon name="monitor" size={15} />macOS</summary>
              <ol>
                <li>Open <b>System Settings → Internet Accounts → Add Account → Add Other Account → CalDAV account</b>.</li>
                <li>Account type <b>Manual</b> (or Advanced), user name <code>{f.caldav_user}</code>, your token as password.</li>
                <li>Server address: <code>{f.caldav_url}</code>.</li>
              </ol>
            </details>
            <details>
              <summary><Icon name="smartphone" size={15} />Android (DAVx⁵)</summary>
              <ol>
                <li>Install <b>DAVx⁵</b> from the Play Store or F-Droid.</li>
                <li>Add an account → <b>Login with URL and user name</b>.</li>
                <li>Base URL <code>{f.caldav_url}</code>, user name <code>{f.caldav_user}</code>, your token as password.</li>
                <li>Pick the calendars to sync; they appear in any calendar app.</li>
              </ol>
            </details>
            <details>
              <summary><Icon name="mail" size={15} />Thunderbird</summary>
              <ol>
                <li>In the calendar, choose <b>New Calendar → On the network</b>.</li>
                <li>User name <code>{f.caldav_user}</code>, location <code>{f.caldav_url}</code>.</li>
                <li>Thunderbird finds all your calendars; tick the ones you want.</li>
              </ol>
            </details>
          </div>
        </div>
      </Section>

      <Section title="All my calendars as a feed" sub="A read-only subscription link for apps that can’t do CalDAV (Outlook, Google Calendar “From URL”, smart displays)."
        actions={<button className="btn sm" onClick={() => setRotate(true)}><Icon name="refresh" size={14} />New link</button>}>
        <div className="stack">
          <div className="callout warn"><Icon name="lock" size={15} /><span>Anyone with this link can read your calendars. Only paste it into apps you trust; make a new link if it leaks.</span></div>
          <Field label="Subscribe (webcal)" group><CopyField value={f.all_webcal} secret label="webcal feed" /></Field>
          <Field label="Plain address (https)" group><CopyField value={f.all_url} secret label="https feed" /></Field>
          <div className="row"><a className="btn sm" href={f.all_webcal}><Icon name="calendar-plus" size={14} />Open in my calendar app</a></div>
        </div>
      </Section>

      {withFeeds.length > 0 && (
        <Section title="One calendar as a feed" sub="Each calendar you own or manage has its own secret link (Share → feed in the calendar menu to make a new one).">
          <div className="stack tight">
            {withFeeds.map(c => (
              <div key={c.id} className="set-feed-row">
                <span className="row set-feed-name"><span className="dot" style={{ background: c.color }} /><span className="ellipsis">{c.name}</span></span>
                <div className="grow"><CopyField value={c.feed_url!} secret label={`${c.name} feed`} /></div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {rotate && <Confirm title="Make a new feed link?" confirmLabel="Make new link" danger={false}
        message="The current link stops working right away. Apps subscribed to it need the new one."
        onCancel={() => setRotate(false)}
        onConfirm={async () => { setRotate(false); try { feeds.setData(await api.post<Feeds>(`${V1}/feeds/rotate`)); toast('New link made') } catch (e) { toast(errMsg(e), 'err') } }} />}
    </div>
  )
}
