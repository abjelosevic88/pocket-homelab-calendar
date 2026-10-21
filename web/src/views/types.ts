import type { Anchor } from '../components/ui'
import type { Wall } from '../lib/dates'
import type { Item } from '../lib/model'
import type { OverlayItem } from '../lib/types'

export interface Draft { start: Wall; end: Wall; allDay: boolean }

export interface ViewProps {
  items: Item[]
  holidays: Map<string, OverlayItem[]>
  loading: boolean
  today: Wall
  now: Wall
  selectedId: string | null
  highlightId: string | null
  draft: Draft | null
  onOpen: (it: Item, anchor: Anchor) => void
  onCreate: (start: Wall, end: Wall, allDay: boolean, anchor: Anchor) => void
  onMove: (it: Item, start: Wall, end: Wall, allDay: boolean) => void
  onToggleTask: (it: Item) => void
  goDay: (d: Wall, view?: 'day') => void
}

export function canDrag(it: Item): boolean {
  if (it.ev) return it.ev.can_edit && !it.ev.masked
  if (it.ov) return it.ov.source === 'tasks' && !!it.ov.actions?.includes('reschedule') && !it.ov.done
  return false
}

// A long-press drag on touch screens must stop the page from scrolling.
// iOS only delivers cancelable touchmoves to a non-passive listener that
// existed before the touch began, so one is installed once, up front.
export const touchDrag = { active: false }
if (typeof window !== 'undefined') {
  window.addEventListener('touchmove', e => { if (touchDrag.active && e.cancelable) e.preventDefault() }, { passive: false })
  // a long press on an event is the start of a drag, not a context menu
  let lastTouch = 0
  window.addEventListener('touchstart', () => { lastTouch = Date.now() }, { passive: true })
  window.addEventListener('contextmenu', e => { if (touchDrag.active || (Date.now() - lastTouch < 1500 && (e.target as Element)?.closest?.('.ev-block, .lane-bar, .mv-item'))) e.preventDefault() })
}
