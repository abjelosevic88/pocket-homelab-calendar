import React, { useState } from 'react'
import { ApiError, api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { Account } from '../../lib/types'
import { Icon } from '../../components/icons'
import { type Option, Select } from '../../components/Select'
import { Confirm, CoreHint, Empty, errMsg, Field, Loading, MenuButton, Modal, Segmented, Spinner, Switch, useAsync, useToast } from '../../components/ui'
import { ago, Section, StatusDot } from './common'

type Provider = 'icloud' | 'google' | 'nextcloud' | 'fastmail' | 'caldav' | 'ics'

const PROVIDERS: { id: Provider; name: string; desc: string; icon: string; color: string }[] = [
  { id: 'icloud', name: 'iCloud', desc: 'Apple calendars, with an app-specific password', icon: 'smartphone', color: '#0a84ff' },
  { id: 'google', name: 'Google', desc: 'Sign in with Google — no password stored', icon: 'globe', color: '#ea4335' },
  { id: 'nextcloud', name: 'Nextcloud', desc: 'Through Pocket Core, or with an app password', icon: 'server', color: '#0082c9' },
  { id: 'fastmail', name: 'Fastmail', desc: 'CalDAV with a Fastmail app password', icon: 'mail', color: '#2f6ae1' },
  { id: 'caldav', name: 'Other CalDAV', desc: 'Any CalDAV server: address, user, password', icon: 'calendar', color: '#64748b' },
  { id: 'ics', name: 'Subscribe to a URL', desc: 'A read-only ICS or webcal feed', icon: 'rss', color: '#f59e0b' },
]
const KIND_ICON: Record<string, string> = { caldav: 'calendar', google: 'globe', ics: 'rss', nextcloud: 'server' }
const KIND_NAME: Record<string, string> = { caldav: 'CalDAV', google: 'Google', ics: 'Subscription', nextcloud: 'Nextcloud' }
const SYNC: Option<number>[] = [5, 15, 30, 60, 360, 1440].map(m => ({ value: m, label: m < 60 ? `Every ${m} minutes` : m === 60 ? 'Every hour' : m === 360 ? 'Every 6 hours' : 'Once a day' }))

interface AccountBody { kind: string; name?: string; url?: string; username?: string; password?: string; via?: 'direct' | 'core' }
interface TestResult { ok: boolean; error?: string; name?: string; calendars?: { name: string; color: string; href: string }[] }

export function AccountsTab() {
  const { reloadCalendars, bump } = useApp()
  const { toast } = useToast()
  const list = useAsync(() => api.get<Account[]>(`${V1}/accounts`), [])
  const [adding, setAdding] = useState(false)
  const [edit, setEdit] = useState<Account | null>(null)
  const [del, setDel] = useState<Account | null>(null)
  const [syncing, setSyncing] = useState<number | null>(null)
  const missing = list.error && /not found|404/i.test(list.error)
  const params = new URLSearchParams(window.location.search)
  const oauthErr = params.get('error')

  const run = async (fn: () => Promise<unknown>, ok?: string) => {
    try { await fn(); if (ok) toast(ok); list.reload(); await reloadCalendars(); bump() } catch (e) { toast(errMsg(e), 'err') }
  }

  return (
    <div className="stack loose">
      {oauthErr && <div className="callout danger"><Icon name="alert" size={15} /><span>{oauthErr}</span></div>}
      <Section title="Other calendars" sub="Calendars from other services are mirrored here and kept in sync. Changes you make here go back to the service when it allows writing."
        actions={<button className="btn primary sm" onClick={() => setAdding(true)}><Icon name="plus" size={14} />Add account</button>}>
        {missing ? (
          <div className="callout warn"><Icon name="info" size={15} /><span>Calendar accounts aren’t available on this server yet. Update Pocket Calendar to connect iCloud, Google, Nextcloud and other CalDAV calendars.</span></div>
        ) : list.loading && !list.data ? <Loading /> : list.error ? (
          <div className="callout danger"><Icon name="alert" size={15} /><span>{list.error}</span></div>
        ) : !list.data?.length ? (
          <Empty title="No accounts yet" icon="globe">Add iCloud, Google, Nextcloud or any CalDAV account to see those calendars next to yours.</Empty>
        ) : (
          <div className="acct-list">
            {list.data.map(a => {
              const status: 'ok' | 'error' | 'off' = !a.enabled ? 'off' : a.last_error ? 'error' : 'ok'
              return (
                <div key={a.id} className="acct-row">
                  <span className="acct-ico"><Icon name={KIND_ICON[a.kind] || 'calendar'} size={17} /></span>
                  <div className="grow">
                    <div className="acct-name"><span className="ellipsis">{a.name || a.url}</span><span className="badge">{KIND_NAME[a.kind] || a.kind}{a.via === 'core' ? ' · via Core' : ''}</span></div>
                    <div className="acct-meta ellipsis">{[a.username, a.via === 'core' ? '' : a.url].filter(Boolean).join(' · ')}</div>
                    <div className="acct-meta">
                      <StatusDot status={status} title={a.last_error || undefined} />
                      {!a.enabled ? 'Paused' : syncing === a.id ? 'Syncing…' : `Synced ${ago(a.last_sync_at)}`} · {a.calendars} calendar{a.calendars === 1 ? '' : 's'}
                    </div>
                    {a.last_error && <div className="acct-error">{a.last_error}</div>}
                  </div>
                  <Switch size="sm" label={`Sync ${a.name}`} checked={a.enabled} onChange={v => run(() => api.put(`${V1}/accounts/${a.id}`, { enabled: v }))} />
                  <button className="btn ghost sm" disabled={syncing === a.id || !a.enabled} onClick={async () => { setSyncing(a.id); await run(() => api.post(`${V1}/accounts/${a.id}/sync`), 'Sync started'); setSyncing(null) }}>
                    {syncing === a.id ? <Spinner /> : <Icon name="refresh" size={14} />}Sync now
                  </button>
                  <MenuButton label={`More for ${a.name}`} align="right" items={[
                    { label: 'Edit…', icon: 'edit', onSelect: () => setEdit(a) },
                    'sep',
                    { label: 'Remove account…', icon: 'trash', danger: true, onSelect: () => setDel(a) },
                  ]}><Icon name="more" size={16} /></MenuButton>
                </div>
              )
            })}
          </div>
        )}
      </Section>
      {adding && <AddAccount onClose={() => setAdding(false)} onAdded={() => { setAdding(false); list.reload(); reloadCalendars(); bump() }} />}
      {edit && <EditAccount acct={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); list.reload(); reloadCalendars(); bump() }} />}
      {del && <Confirm title={`Remove ${del.name || 'this account'}?`} confirmLabel="Remove"
        message={<>Its {del.calendars} mirrored calendar{del.calendars === 1 ? '' : 's'} disappear from Pocket Calendar. Nothing is deleted at {KIND_NAME[del.kind] || 'the provider'}.</>}
        onCancel={() => setDel(null)} onConfirm={() => { const a = del; setDel(null); run(() => api.del(`${V1}/accounts/${a.id}`), 'Account removed') }} />}
    </div>
  )
}

function AddAccount({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const { sr } = useApp()
  const { toast } = useToast()
  const [prov, setProv] = useState<Provider | null>(null)
  const [f, setF] = useState({ name: '', url: '', username: '', password: '' })
  // Through Pocket Core needs a Core this box is registered with; without one
  // the choice stays visible but greyed out and Direct is what is in effect
  const coreOn = !!(sr?.core.configured && sr.core.registered)
  const [pickedVia, setVia] = useState<'core' | 'direct'>(coreOn ? 'core' : 'direct')
  const via = coreOn ? pickedVia : 'direct'
  const [test, setTest] = useState<{ busy: boolean; result?: TestResult; error?: string }>({ busy: false })
  const [busy, setBusy] = useState(false)
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) => { setF(x => ({ ...x, [k]: e.target.value })); setTest({ busy: false }) }

  const pick = (p: Provider) => {
    setProv(p)
    setTest({ busy: false })
    setF({ name: '', url: p === 'icloud' ? 'https://caldav.icloud.com' : p === 'fastmail' ? 'https://caldav.fastmail.com/dav/' : '', username: '', password: '' })
  }
  const body = (): AccountBody => {
    const b: AccountBody = { kind: prov === 'nextcloud' ? 'nextcloud' : prov === 'ics' ? 'ics' : 'caldav' }
    if (f.name.trim()) b.name = f.name.trim()
    if (prov === 'nextcloud') { b.via = via; if (via === 'core') return b }
    b.url = f.url.trim().replace(/^webcal:\/\//i, 'https://')
    if (prov !== 'ics') { b.username = f.username.trim(); b.password = f.password }
    else if (f.username.trim()) { b.username = f.username.trim(); b.password = f.password }
    if (prov === 'icloud' && !b.name) b.name = 'iCloud'
    if (prov === 'fastmail' && !b.name) b.name = 'Fastmail'
    return b
  }
  const valid = () => {
    if (!prov || prov === 'google') return false
    if (prov === 'nextcloud' && via === 'core') return coreOn
    if (!f.url.trim()) return false
    if (prov !== 'ics' && (!f.username.trim() || !f.password)) return false
    return true
  }
  const explain = (e: unknown) => e instanceof ApiError && e.status === 404 ? 'Calendar accounts aren’t available on this server yet.' : errMsg(e)
  const runTest = async () => {
    setTest({ busy: true })
    try { const r = await api.post<TestResult>(`${V1}/accounts/test`, body()); setTest(r.ok === false ? { busy: false, error: r.error || 'Could not connect' } : { busy: false, result: r }) } catch (e) { setTest({ busy: false, error: explain(e) }) }
  }
  const add = async () => {
    setBusy(true)
    try { await api.post(`${V1}/accounts`, body()); toast('Account added — syncing now'); onAdded() } catch (e) { toast(explain(e), 'err') } finally { setBusy(false) }
  }
  const p = PROVIDERS.find(x => x.id === prov)

  return (
    <Modal size="lg" onClose={onClose} title={p ? <span className="row"><button type="button" className="btn ghost icon-only sm" aria-label="Back to providers" onClick={() => setProv(null)}><Icon name="chevron-left" size={16} /></button>Add {p.name}</span> : 'Add a calendar account'}
      footer={prov && prov !== 'google' ? <>
        <button className="btn" onClick={runTest} disabled={!valid() || test.busy}>{test.busy ? <Spinner /> : <Icon name="activity" size={14} />}Test connection</button>
        <span className="spacer" />
        <button className="btn" onClick={onClose}>Cancel</button>
        <button className="btn primary" onClick={add} disabled={!valid() || busy}>{busy ? 'Adding…' : 'Add account'}</button>
      </> : undefined}>
      {!prov ? (
        <div className="acct-providers">
          {PROVIDERS.map(x => (
            <button key={x.id} type="button" className="acct-provider" onClick={() => pick(x.id)}>
              <span className="acct-provider-ico" style={{ background: x.color }}><Icon name={x.icon} size={18} /></span>
              <span className="acct-provider-name">{x.name}</span>
              <span className="acct-provider-desc">{x.desc}</span>
            </button>
          ))}
        </div>
      ) : (
        <div className="stack">
          {prov === 'google' && (
            <div className="stack">
              <p className="muted">You’ll be sent to Google to allow Pocket Calendar to read and change your Google calendars, then brought back here.</p>
              {sr?.google_configured
                ? <a className="btn primary lg" href={`${V1}/accounts/google/start`}><Icon name="globe" size={16} />Continue with Google</a>
                : <>
                  <button className="btn primary lg" disabled><Icon name="globe" size={16} />Continue with Google</button>
                  <div className="callout warn"><Icon name="info" size={15} /><span>Google sign-in isn’t set up on this box. Ask the admin to set <code>GOOGLE_CLIENT_ID</code> and <code>GOOGLE_CLIENT_SECRET</code> (a Google Cloud OAuth client). Google calendars can only be connected this way.</span></div>
                </>}
            </div>
          )}
          {prov === 'nextcloud' && (
            <div>
              <Segmented value={via} onChange={v => { setVia(v); setTest({ busy: false }) }} options={[{ value: 'core', label: 'Through Pocket Core', disabled: !coreOn }, { value: 'direct', label: 'Direct' }]} label="How to connect" />
              {!coreOn && <CoreHint />}
            </div>
          )}
          {prov === 'nextcloud' && via === 'core' && (
            <div className="callout info"><Icon name="info" size={15} /><span>Your Nextcloud calendars are reached through Pocket Core as you — no address or password needed.</span></div>
          )}
          {prov !== 'google' && !(prov === 'nextcloud' && via === 'core') && (<>
            <Field label={prov === 'ics' ? 'Feed address' : 'Server address'} help={prov === 'ics' ? 'An https:// or webcal:// link to an .ics file. It is read-only here.' : prov === 'nextcloud' ? 'Your Nextcloud address, e.g. https://cloud.example.com' : prov === 'caldav' ? 'The CalDAV URL; most servers find the calendars from the base address.' : undefined}>
              <input value={f.url} onChange={set('url')} inputMode="url" spellCheck={false} autoComplete="off" placeholder={prov === 'ics' ? 'webcal://example.com/calendar.ics' : 'https://'} autoFocus={prov !== 'icloud' && prov !== 'fastmail'} />
            </Field>
            {prov !== 'ics' && <div className="two-col">
              <Field label={prov === 'icloud' ? 'Apple ID (email)' : prov === 'fastmail' ? 'Fastmail email' : 'User name'}><input value={f.username} onChange={set('username')} autoComplete="off" spellCheck={false} autoFocus={prov === 'icloud' || prov === 'fastmail'} /></Field>
              <Field label={prov === 'icloud' ? 'App-specific password' : prov === 'caldav' ? 'Password' : 'App password'}><input type="password" value={f.password} onChange={set('password')} autoComplete="new-password" /></Field>
            </div>}
            {prov === 'icloud' && <p className="help small muted" style={{ margin: 0 }}>Create one at <a href="https://appleid.apple.com" target="_blank" rel="noreferrer">appleid.apple.com</a> → Sign-In and Security → App-Specific Passwords. Your normal Apple password won’t work.</p>}
            {prov === 'fastmail' && <p className="help small muted" style={{ margin: 0 }}>In Fastmail: Settings → Privacy &amp; Security → Manage app passwords → New app password, with access to “Calendars (CalDAV)”.</p>}
            {prov === 'nextcloud' && <p className="help small muted" style={{ margin: 0 }}>In Nextcloud: Personal settings → Security → Create new app password.</p>}
          </>)}
          {prov !== 'google' && <Field label="Name (optional)"><input value={f.name} onChange={set('name')} placeholder={p?.name} /></Field>}
          {test.error && <div className="callout danger"><Icon name="alert" size={15} /><span>{test.error}</span></div>}
          {test.result && (
            <div className="callout success">
              <Icon name="check-circle" size={15} />
              <div className="grow">
                <b>Connected.</b> {test.result.calendars?.length ? `Found ${test.result.calendars.length} calendar${test.result.calendars.length === 1 ? '' : 's'}:` : 'No calendars found yet.'}
                {!!test.result.calendars?.length && <ul className="acct-found">{test.result.calendars.map(c => <li key={c.href}><span className="dot" style={{ background: c.color || 'var(--faint)' }} />{c.name || c.href}</li>)}</ul>}
              </div>
            </div>
          )}
        </div>
      )}
    </Modal>
  )
}

function EditAccount({ acct, onClose, onSaved }: { acct: Account; onClose: () => void; onSaved: () => void }) {
  const { sr } = useApp()
  const { toast } = useToast()
  const [f, setF] = useState({ name: acct.name, url: acct.url, username: acct.username, password: '', sync_minutes: acct.sync_minutes || 30 })
  const [busy, setBusy] = useState(false)
  const core = acct.via === 'core'
  const save = async () => {
    setBusy(true)
    try {
      const body: Record<string, unknown> = { name: f.name.trim(), sync_minutes: f.sync_minutes }
      if (!core && acct.kind !== 'google') { body.url = f.url.trim(); body.username = f.username.trim(); if (f.password) body.password = f.password }
      await api.put(`${V1}/accounts/${acct.id}`, body)
      toast('Account saved')
      onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const sync = SYNC.some(o => o.value === f.sync_minutes) ? SYNC : [...SYNC, { value: f.sync_minutes, label: `Every ${f.sync_minutes} minutes` }]
  return (
    <Modal title={`Edit ${acct.name || 'account'}`} onClose={onClose} footer={<>
      <button className="btn" onClick={onClose}>Cancel</button>
      <button className="btn primary" onClick={save} disabled={busy}>Save</button>
    </>}>
      <div className="stack">
        <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} /></Field>
        {core && !(sr?.core.configured && sr.core.registered) && <div><span className="muted small">This account is connected through Pocket Core, so it can’t sync right now.</span><CoreHint /></div>}
        {!core && acct.kind !== 'google' && <>
          <Field label="Address"><input value={f.url} onChange={e => setF({ ...f, url: e.target.value })} inputMode="url" spellCheck={false} /></Field>
          {acct.kind !== 'ics' && <div className="two-col">
            <Field label="User name"><input value={f.username} onChange={e => setF({ ...f, username: e.target.value })} autoComplete="off" /></Field>
            <Field label="Password" help="Leave empty to keep the saved one."><input type="password" value={f.password} onChange={e => setF({ ...f, password: e.target.value })} placeholder={acct.has_secret ? '•••• saved' : ''} autoComplete="new-password" /></Field>
          </div>}
        </>}
        <Field label="Sync" group><Select value={f.sync_minutes} options={sync} onChange={v => setF({ ...f, sync_minutes: v })} label="Sync interval" /></Field>
      </div>
    </Modal>
  )
}
