// Small shared bits for rendering items in every view.
import React from 'react'
import { Icon, OVERLAY_ICON } from '../components/icons'
import type { Item } from '../lib/model'

export function colorStyle(color: string): React.CSSProperties {
  return { ['--c' as string]: color }
}

/** The tick box of a task overlay. */
export function TaskCheck({ it, onToggle, size = 13 }: { it: Item; onToggle?: (it: Item) => void; size?: number }) {
  const can = !!it.ov?.actions?.includes('complete')
  return (
    <span role="checkbox" aria-checked={it.ov?.done} aria-label={it.ov?.done ? 'Mark as not done' : 'Mark as done'} tabIndex={can ? 0 : -1}
      className={`task-check ${it.ov?.done ? 'on' : ''} ${can ? '' : 'ro'}`} style={{ width: size, height: size }}
      onPointerDown={e => e.stopPropagation()}
      onClick={e => { e.stopPropagation(); if (can) onToggle?.(it) }}
      onKeyDown={e => { if ((e.key === ' ' || e.key === 'Enter') && can) { e.preventDefault(); e.stopPropagation(); onToggle?.(it) } }}>
      {it.ov?.done && <Icon name="check" size={size - 3} strokeWidth={3} />}
    </span>
  )
}

/** Leading glyph for an item: task checkbox, overlay icon, or nothing. */
export function ItemGlyph({ it, onToggle }: { it: Item; onToggle?: (it: Item) => void }) {
  if (!it.ov) return null
  const k = it.ov.kind
  if (k === 'task' || k === 'done') return <TaskCheck it={it} onToggle={onToggle} />
  if (k === 'due') return <span className="ov-glyph"><TaskCheck it={it} onToggle={onToggle} /><Icon name="flag" size={11} className="due-flag" /></span>
  return <Icon name={OVERLAY_ICON[k] || 'dot'} size={12} className="ov-ico" />
}

export function itemAria(it: Item, when: string): string {
  const what = it.ov ? `${it.ov.kind === 'due' ? 'Deadline' : it.ov.kind === 'task' ? 'Task' : it.ov.kind}: ` : ''
  return `${what}${it.title}, ${when}`
}
