// Natural-language quick add with a live preview of what was understood.
import React, { useEffect, useRef, useState } from 'react'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { alarmLabel, todayWall, whenText } from '../lib/dates'
import type { Occurrence, QuickParsed } from '../lib/types'
import { Icon } from './icons'
import { errMsg, useDebounce, useToast } from './ui'

export function QuickAdd({ onCreated, autoFocus, onDone, compact }: { onCreated: (ev: Occurrence) => void; autoFocus?: boolean; onDone?: () => void; compact?: boolean }) {
  const { tzParam, calendars, h12, tz } = useApp()
  const { toast } = useToast()
  const [text, setText] = useState('')
  const [focus, setFocus] = useState(false)
  const [parsed, setParsed] = useState<QuickParsed | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const q = useDebounce(text, 220)
  const ref = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!q.trim()) { setParsed(null); setErr(''); return }
    let live = true
    api.post<{ parsed: QuickParsed }>(`${V1}/events/quick`, { text: q, preview: true, tz: tzParam || undefined })
      .then(r => { if (live) { setParsed(r.parsed); setErr('') } })
      .catch(e => live && setErr(errMsg(e)))
    return () => { live = false }
  }, [q, tzParam])

  const submit = async () => {
    if (!text.trim() || busy) return
    setBusy(true)
    try {
      const r = await api.post<{ event?: Occurrence; parsed: QuickParsed }>(`${V1}/events/quick`, { text, tz: tzParam || undefined })
      setText(''); setParsed(null)
      if (r.event) onCreated(r.event)
      toast(<>Added <b>{r.parsed.title || 'event'}</b></>)
      onDone?.()
      ref.current?.blur()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const cal = parsed ? calendars.find(c => c.id === parsed.calendar_id) : undefined
  const open = focus && text.trim().length > 0
  const today = todayWall(tz)
  return (
    <div className={`quickadd ${open ? 'open' : ''} ${compact ? 'compact' : ''}`}>
      <div className="qa-field">
        <Icon name="sparkles" size={15} className="qa-ico" />
        <input ref={ref} value={text} autoFocus={autoFocus} placeholder="Quick add — “Lunch with Sani fri 13:00 #family”" aria-label="Quick add an event"
          onFocus={() => setFocus(true)} onBlur={() => setTimeout(() => setFocus(false), 150)} onChange={e => setText(e.target.value)}
          onKeyDown={(e: React.KeyboardEvent) => {
            if (e.key === 'Enter') { e.preventDefault(); submit() }
            if (e.key === 'Escape') { e.stopPropagation(); setText(''); ref.current?.blur(); onDone?.() }
          }} />
        {busy && <span className="spinner" />}
      </div>
      {open && (
        <div className="qa-preview" onMouseDown={e => e.preventDefault()}>
          {err && <div className="qa-err">{err}</div>}
          {parsed && <>
            <div className="qa-title">{parsed.title || <span className="muted">(No title)</span>}</div>
            <div className="qa-chips">
              <span className="qa-chip"><Icon name="clock" size={12} />{whenText(parsed.start, parsed.end, parsed.all_day, h12, today)}</span>
              {parsed.repeat_text && <span className="qa-chip"><Icon name="repeat" size={12} />{parsed.repeat_text}</span>}
              {parsed.location && <span className="qa-chip"><Icon name="pin" size={12} />{parsed.location}</span>}
              <span className="qa-chip"><span className="dot" style={{ background: cal?.color }} />{parsed.calendar}</span>
              {(parsed.alarms || []).map(a => <span key={a} className="qa-chip"><Icon name="bell" size={12} />{alarmLabel(a, parsed.all_day, h12)}</span>)}
              {parsed.attendees?.map(a => <span key={a.email} className="qa-chip"><Icon name="user" size={12} />{a.name || a.email}</span>)}
              {parsed.private && <span className="qa-chip"><Icon name="lock" size={12} />Private</span>}
            </div>
            <div className="qa-foot"><span><kbd>↵</kbd> add</span><span><kbd>esc</kbd> clear</span></div>
          </>}
          {!parsed && !err && <div className="qa-hint muted small">Reading…</div>}
        </div>
      )}
    </div>
  )
}
