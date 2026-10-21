import { useState } from 'react'
import type { Scope } from '../lib/types'
import { Modal } from './ui'

/** "Change this event / This and following / All events" for a recurring event. */
export function ScopeDialog({ title, verb = 'Change', onPick, onCancel, allowThis = true, allowFollowing = true }: { title?: string; verb?: 'Change' | 'Delete' | 'Move'; onPick: (s: Scope) => void; onCancel: () => void; allowThis?: boolean; allowFollowing?: boolean }) {
  const [v, setV] = useState<Scope>(allowThis ? 'this' : 'all')
  const opts: { value: Scope; label: string; ok: boolean }[] = [
    { value: 'this', label: 'This event', ok: allowThis },
    { value: 'following', label: 'This and following events', ok: allowFollowing },
    { value: 'all', label: 'All events', ok: true },
  ]
  return (
    <Modal title={title || `${verb} recurring event`} onClose={onCancel} size="sm" footer={<>
      <button className="btn" onClick={onCancel}>Cancel</button>
      <button className={`btn ${verb === 'Delete' ? 'danger' : 'primary'}`} onClick={() => onPick(v)} autoFocus>{verb === 'Delete' ? 'Delete' : 'OK'}</button>
    </>}>
      <div className="scope-opts" role="radiogroup">
        {opts.filter(o => o.ok).map(o => (
          <label key={o.value} className={`scope-opt ${v === o.value ? 'on' : ''}`}>
            <input type="radio" name="scope" checked={v === o.value} onChange={() => setV(o.value)} />
            <span>{o.label}</span>
          </label>
        ))}
      </div>
    </Modal>
  )
}
