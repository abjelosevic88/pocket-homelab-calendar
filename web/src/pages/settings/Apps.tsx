import { useEffect, useState } from 'react'
import { api, V1 } from '../../lib/api'
import { useApp } from '../../lib/app-context'
import type { OverlaySource } from '../../lib/types'
import { Icon } from '../../components/icons'
import { ColorPalette } from '../../components/pickers'
import { CoreHint, errMsg, Field, Loading, Segmented, useToast } from '../../components/ui'
import { StatusDot } from './common'

const ICON: Record<string, string> = { tasks: 'check-circle', travel: 'plane', garage: 'wrench', invoicing: 'receipt', habits: 'habit', collaborator: 'review', projects: 'flag', birthdays: 'cake', health: 'heart', holidays: 'star' }

export function AppsTab() {
  const { sr, sources, reloadSources } = useApp()
  useEffect(() => { reloadSources() }, [reloadSources])
  const core = sr?.core
  const coreOn = !!(core?.configured && core.registered)
  return (
    <div className="stack loose">
      <div className="callout info">
        <Icon name="layers" size={16} />
        <div>
          {coreOn
            ? <><b>Pocket Core is the default and needs no setup.</b> Each app is read as you through a Pocket Core app link, so you only see your own tasks, trips and bills.
              {' '}Use <i>Direct</i> only for an app that is not connected to Core: paste its address and a token from that app.</>
            : <>Without Pocket Core, <i>Pocket Core</i> is greyed out below: use <i>Direct</i> for an app, with its address and a token from that app.</>}
          <div className="small mt-s">
            Pocket Core: {!core?.configured ? <span className="danger-text">not configured on this box</span> : core.registered ? <span className="ok-text">connected</span> : <span className="danger-text">not reachable{core.error ? ` — ${core.error}` : ''}</span>}
          </div>
          {sr && !coreOn && <CoreHint />}
        </div>
      </div>
      {!sources.length ? <Loading /> : (
        <div className="set-apps">
          {sources.map(s => <SourceCard key={s.id} src={s} coreOn={coreOn} onChanged={reloadSources} />)}
        </div>
      )}
    </div>
  )
}

function SourceCard({ src, coreOn, onChanged }: { src: OverlaySource; coreOn: boolean; onChanged: () => Promise<void> }) {
  const { bump, setSources } = useApp()
  const { toast } = useToast()
  const [url, setUrl] = useState(src.url || '')
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => { setUrl(src.url || '') }, [src.url])

  const put = async (body: Partial<{ mode: string; color: string; url: string; token: string }>, ok?: string) => {
    setBusy(true)
    setSources(list => list.map(x => x.id === src.id ? { ...x, ...(body.mode ? { mode: body.mode as OverlaySource['mode'] } : {}), ...(body.color !== undefined ? { color: body.color || x.default_color } : {}) } : x))
    try {
      await api.put(`${V1}/overlays/sources/${src.id}`, body)
      if (ok) toast(ok)
      setToken('')
    } catch (e) { toast(errMsg(e), 'err') }
    finally { setBusy(false); await onChanged(); bump() }
  }
  // a Pocket app read through Core does nothing without a Core: show it as off
  // (the saved choice is kept and comes back once Core is connected)
  const mode = src.pocket && !coreOn && src.mode === 'core' ? 'off' : src.mode
  const status: 'ok' | 'error' | 'off' = mode === 'off' ? 'off' : src.status
  const modes = src.pocket
    ? [{ value: 'core' as const, label: 'Pocket Core', disabled: !coreOn }, { value: 'direct' as const, label: 'Direct' }, { value: 'off' as const, label: 'Off' }]
    : [{ value: 'core' as const, label: 'On' }, { value: 'off' as const, label: 'Off' }]
  const isDefaultColor = src.color.toLowerCase() === src.default_color.toLowerCase()

  return (
    <section className={`set-app ${mode === 'off' ? 'off' : ''}`}>
      <div className="set-app-head">
        <span className="set-app-ico" style={{ background: src.color }}><Icon name={ICON[src.id] || 'layers'} size={16} /></span>
        <div className="grow">
          <div className="set-app-name">{src.name} <span className="muted small">· {src.app}</span></div>
          <div className="muted small">{src.description}</div>
        </div>
        <span className="set-app-status" title={src.detail || status}>
          <StatusDot status={status} title={status === 'error' ? src.detail : status === 'ok' ? 'Working' : 'Off'} />
          <span className="small">{status === 'ok' ? 'Working' : status === 'off' ? 'Off' : 'Problem'}</span>
        </span>
      </div>
      {status === 'error' && src.detail && <div className="callout danger small"><Icon name="alert" size={14} /><span>{src.detail}</span></div>}
      <div className="set-app-controls">
        <Segmented size="sm" label={`${src.name} source`} value={mode} onChange={m => put({ mode: m })} options={modes} />
        <ColorPalette value={isDefaultColor ? '' : src.color} allowNone noneLabel="Default colour" custom onChange={c => put({ color: c })} />
      </div>
      {src.mode === 'direct' && (
        <div className="set-app-direct">
          <div className="two-col">
            <Field label={`${src.app} address`}><input value={url} onChange={e => setUrl(e.target.value)} placeholder={`https://${src.id}.example.com`} inputMode="url" spellCheck={false} /></Field>
            <Field label="Token" help={`An API token created in ${src.app}.`}><input type="password" value={token} onChange={e => setToken(e.target.value)} placeholder={src.has_token ? '•••• saved — type to replace' : 'Paste a token'} autoComplete="new-password" /></Field>
          </div>
          <div className="row"><button className="btn primary sm" disabled={busy || !url.trim()} onClick={() => put({ mode: 'direct', url: url.trim(), ...(token ? { token } : {}) }, 'Saved')}>Save connection</button></div>
        </div>
      )}
    </section>
  )
}
