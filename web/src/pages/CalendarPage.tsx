// The calendar screen: sidebar, top bar, the active view, and every
// interaction that changes events (with optimistic updates and undo).
import { type TouchEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Navigate, useLocation, useNavigate, useParams } from 'react-router-dom'
import { api, V1, qs } from '../lib/api'
import { useApp } from '../lib/app-context'
import { prefetchRange, useRange } from '../lib/data'
import { type Wall, MIN, addDays, dayKey, nowWall, parseWall, startOfDay, wallToRFC3339 } from '../lib/dates'
import { type Item, type Override, buildItems } from '../lib/model'
import { isView, parseDateParam, rangeFor, step, urlFor } from '../lib/nav'
import type { Attendee, EventBody, EventDetail, Occurrence, PeopleEntry, Scope, ViewName } from '../lib/types'
import { CommandPalette, type PaletteCommand, VIEW_KEY, VIEW_LABEL } from '../components/CommandPalette'
import { type EditorDraft, EventEditor } from '../components/EventEditor'
import { EventPopover, OverlayPopover } from '../components/EventPopover'
import { Icon } from '../components/icons'
import { QuickAdd } from '../components/QuickAdd'
import { QuickCreate } from '../components/QuickCreate'
import { ScopeDialog } from '../components/ScopeDialog'
import { ShortcutsSheet } from '../components/ShortcutsSheet'
import { Sidebar } from '../components/Sidebar'
import { TopBar } from '../components/TopBar'
import { type Anchor, Segmented, copyText, download, errMsg, modalOpen, useIsMobile, useMediaQuery, useToast } from '../components/ui'
import { AgendaView } from '../views/AgendaView'
import { MonthView } from '../views/MonthView'
import { PeopleView } from '../views/PeopleView'
import { TimeGrid } from '../views/TimeGrid'
import type { Draft } from '../views/types'
import { YearView } from '../views/YearView'

type Pending = { kind: 'move'; it: Item; start: Wall; end: Wall; allDay: boolean } | { kind: 'delete'; it: Item }
type Undo = { label: string; run: () => Promise<void> }

const SIDEBAR_KEY = 'pcal-sidebar'
const centerAnchor = (): Anchor => ({ x: window.innerWidth / 2 - 10, y: window.innerHeight / 3, w: 20, h: 20 })

export default function CalendarPage() {
  const params = useParams()
  const loc = useLocation()
  const navigate = useNavigate()
  const app = useApp()
  const { settings, tz, tzParam, calendars, version, bump, h12 } = app
  const { toast } = useToast()
  const mobile = useIsMobile()
  const narrow = useMediaQuery('(max-width: 1099px)')
  const drawer = mobile || narrow

  // ----- time -----
  const [now, setNow] = useState(() => nowWall(tz))
  useEffect(() => {
    setNow(nowWall(tz))
    let t: number
    const tick = () => { setNow(nowWall(tz)); t = window.setTimeout(tick, 60000 - (Date.now() % 60000) + 50) }
    t = window.setTimeout(tick, 60000 - (Date.now() % 60000) + 50)
    return () => clearTimeout(t)
  }, [tz])
  const today = startOfDay(now)

  // ----- view + date from the URL -----
  const eventParam = params.id
  const rawView = params.view
  const defaultView: ViewName = (() => {
    const v = settings?.default_view || 'week'
    if (mobile && (v === 'week' || v === 'year')) return '3day'
    return v
  })()
  const view: ViewName = isView(rawView) ? rawView : defaultView
  const date = parseDateParam(params.date) ?? today
  const search = new URLSearchParams(loc.search)
  const peopleSpan: 'day' | 'week' = (search.get('span') as 'day' | 'week') || (mobile ? 'day' : 'week')
  const withIds = (search.get('with') || '').split(',').map(Number).filter(Boolean)
  const [agendaDays, setAgendaDays] = useState(60)
  useEffect(() => { setAgendaDays(60) }, [date, view])

  const range = useMemo(() => rangeFor(view, date, settings, { agendaDays, peopleSpan }), [view, date, settings, agendaDays, peopleSpan])
  const go = useCallback((v: ViewName, d: Wall, replace = false, keep = '') => navigate(urlFor(v, d, today, keep), { replace }), [navigate, today])
  const goView = (v: ViewName) => go(v, date)
  const prev = () => go(view, step(view, date, -1, settings, { peopleSpan }), false, view === 'people' ? loc.search : '')
  const next = () => go(view, step(view, date, 1, settings, { peopleSpan }), false, view === 'people' ? loc.search : '')
  const goToday = () => go(view, today, false, view === 'people' ? loc.search : '')

  // ----- data -----
  const fetchOverlays = view !== 'year' && view !== 'people'
  const { data, loading, error } = useRange(dayKey(range.from), dayKey(range.to), tzParam, version, fetchOverlays)
  const lastData = useRef(data)
  if (data) lastData.current = data
  const shown = data || (view === 'agenda' ? lastData.current : null)
  useEffect(() => {
    if (!data || view === 'agenda' || view === 'people') return
    const p = rangeFor(view, step(view, date, -1, settings, { peopleSpan }), settings, { agendaDays, peopleSpan })
    const n = rangeFor(view, step(view, date, 1, settings, { peopleSpan }), settings, { agendaDays, peopleSpan })
    const t = setTimeout(() => { prefetchRange(dayKey(n.from), dayKey(n.to), tzParam, fetchOverlays); prefetchRange(dayKey(p.from), dayKey(p.to), tzParam, fetchOverlays) }, 400)
    return () => clearTimeout(t)
  }, [data]) // eslint-disable-line react-hooks/exhaustive-deps

  // optimistic state, dropped whenever fresh data arrives
  const [overrides, setOverrides] = useState<Record<string, Override>>({})
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const [extra, setExtra] = useState<Occurrence[]>([])
  const [doneFlip, setDoneFlip] = useState<Record<string, boolean>>({})
  useEffect(() => { setOverrides({}); setHidden(new Set()); setExtra([]); setDoneFlip({}) }, [data])

  const { items, holidays } = useMemo(() => {
    const ovs = (shown?.overlays || []).map(o => (o.id in doneFlip ? { ...o, done: doneFlip[o.id] } : o))
    return buildItems(shown?.events || [], ovs, calendars, settings, overrides, hidden, extra)
  }, [shown, calendars, settings, overrides, hidden, extra, doneFlip])

  const dots = useMemo(() => {
    const s = new Set<string>()
    for (const it of items) if (!it.ov) { let d = startOfDay(it.start); let g = 0; do { s.add(dayKey(d)); d = addDays(d, 1) } while (d < it.end && ++g < 40) }
    return s
  }, [items])

  // ----- UI state -----
  const [sidebarOpen, setSidebarOpen] = useState(() => !drawer && localStorage.getItem(SIDEBAR_KEY) !== '0')
  useEffect(() => { setSidebarOpen(!drawer && localStorage.getItem(SIDEBAR_KEY) !== '0') }, [drawer])
  const drawerOpenedAt = useRef(0)
  const toggleSidebar = () => setSidebarOpen(o => { drawerOpenedAt.current = performance.now(); const n = !o; if (!drawer) localStorage.setItem(SIDEBAR_KEY, n ? '1' : '0'); return n })
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [pop, setPop] = useState<{ item: Item; anchor: Anchor } | null>(null)
  const [qc, setQc] = useState<{ start: Wall; end: Wall; allDay: boolean; anchor: Anchor; attendees?: Attendee[] } | null>(null)
  const [editor, setEditor] = useState<EditorDraft | null>(null)
  const [pending, setPending] = useState<Pending | null>(null)
  const [palette, setPalette] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const [mobileAdd, setMobileAdd] = useState(false)
  const [highlightId, setHighlightId] = useState<string | null>(null)
  const undo = useRef<Undo | null>(null)
  useEffect(() => { if (!highlightId) return; const t = setTimeout(() => setHighlightId(null), 2600); return () => clearTimeout(t) }, [highlightId])
  useEffect(() => {
    const h = () => setShortcuts(true)
    window.addEventListener('pcal:shortcuts', h)
    return () => window.removeEventListener('pcal:shortcuts', h)
  }, [])

  // keep the open popover in sync with refreshed data
  useEffect(() => {
    if (!pop) return
    const fresh = items.find(i => i.id === pop.item.id)
    if (fresh && fresh !== pop.item && fresh.ev !== pop.item.ev) setPop(p => p && { ...p, item: fresh })
  }, [items]) // eslint-disable-line react-hooks/exhaustive-deps

  // ----- deep link /event/:id -----
  const deepLinked = useRef<string | null>(null)
  useEffect(() => {
    if (!eventParam || deepLinked.current === eventParam) return
    deepLinked.current = eventParam
    api.get<EventDetail>(`${V1}/events/${eventParam}${qs({ tz: tzParam })}`).then(r => {
      const ev = r.event
      const start = parseWall(ev.start)
      navigate(urlFor(mobile ? '3day' : 'week', startOfDay(start), today), { replace: true })
      const it: Item = { id: ev.id, title: ev.title, start, end: parseWall(ev.end), allDay: ev.all_day, color: ev.color || r.calendar?.color || settings?.invites_color || '#64748b', ev }
      setTimeout(() => {
        const el = document.querySelector<HTMLElement>(`[data-id="${CSS.escape(ev.id)}"]`)
        const a = el ? el.getBoundingClientRect() : null
        setSelectedId(ev.id); setHighlightId(ev.id)
        setPop({ item: it, anchor: a ? { x: a.left, y: a.top, w: a.width, h: a.height } : centerAnchor() })
      }, 500)
    }).catch(e => { toast(errMsg(e), 'err'); navigate('/', { replace: true }) })
  }, [eventParam]) // eslint-disable-line react-hooks/exhaustive-deps

  // ----- mutations -----
  const flash = (id: string) => { setHighlightId(id); setSelectedId(id) }
  const afterWrite = () => { bump() }

  const createEvent = async (body: EventBody): Promise<boolean> => {
    try {
      const ev = await api.post<Occurrence>(`${V1}/events${qs({ tz: tzParam })}`, body)
      setExtra(x => [...x, ev])
      flash(ev.id)
      afterWrite()
      toast(<>Created <b>{ev.title}</b></>, 'ok', { label: 'Undo', run: () => remove({ id: ev.id, title: ev.title, start: parseWall(ev.start), end: parseWall(ev.end), allDay: ev.all_day, color: '', ev }, 'all', true) })
      return true
    } catch (e) { toast(errMsg(e), 'err'); return false }
  }

  const putEvent = async (ev: Occurrence, body: EventBody, scope: Scope | null): Promise<Occurrence> => {
    const q = qs({ scope: ev.recurring && scope ? scope : undefined, tz: tzParam })
    return api.put<Occurrence>(`${V1}/events/${ev.id}${q}`, body)
  }

  const timesBody = (it: Item, start: Wall, end: Wall, allDay: boolean): EventBody =>
    allDay ? { all_day: true, start: dayKey(start), end: dayKey(end) } : { all_day: false, start: wallToRFC3339(start, tz), end: wallToRFC3339(end, tz) }

  const move = (it: Item, start: Wall, end: Wall, allDay: boolean) => {
    setPop(null); setQc(null)
    if (it.ov) { moveTask(it, start, end, allDay); return }
    if (!it.ev?.can_edit) return
    if (it.ev.recurring) { setOverrides(o => ({ ...o, [it.id]: { start, end, allDay } })); setPending({ kind: 'move', it, start, end, allDay }); return }
    doMove(it, start, end, allDay, null)
  }
  const doMove = async (it: Item, start: Wall, end: Wall, allDay: boolean, scope: Scope | null) => {
    const ev = it.ev!
    setOverrides(o => ({ ...o, [it.id]: { start, end, allDay } }))
    try {
      const saved = await putEvent(ev, timesBody(it, start, end, allDay), scope)
      afterWrite()
      flash(saved.id)
      if (scope !== 'following') {
        const back = timesBody(it, it.start, it.end, it.allDay)
        const u: Undo = { label: 'move', run: async () => { await putEvent(saved, back, scope); afterWrite(); toast('Move undone') } }
        undo.current = u
        toast(<>Moved <b>{it.title}</b></>, 'ok', { label: 'Undo', run: () => { undo.current = null; u.run().catch(e => toast(errMsg(e), 'err')) } })
      } else toast('Moved this and the following events')
    } catch (e) {
      setOverrides(o => { const n = { ...o }; delete n[it.id]; return n })
      toast(errMsg(e), 'err')
    }
  }
  const moveTask = async (it: Item, start: Wall, end: Wall, allDay: boolean) => {
    const ov = it.ov!
    if (!ov.actions?.includes('reschedule')) return
    setOverrides(o => ({ ...o, [it.id]: { start, end, allDay } }))
    try {
      await api.put(`${V1}/overlays/tasks/${encodeURIComponent(ov.ref)}`, { kind: ov.kind === 'due' ? 'due' : 'task', all_day: allDay, start: allDay ? dayKey(start) : wallToRFC3339(start, tz), end: allDay ? undefined : wallToRFC3339(end, tz) })
      afterWrite()
      const back = { kind: ov.kind === 'due' ? 'due' : 'task', all_day: it.allDay, start: it.allDay ? dayKey(it.start) : wallToRFC3339(it.start, tz), end: it.allDay ? undefined : wallToRFC3339(it.end, tz) }
      const u: Undo = { label: 'move', run: async () => { await api.put(`${V1}/overlays/tasks/${encodeURIComponent(ov.ref)}`, back); afterWrite() } }
      undo.current = u
      toast(<>Moved task <b>{it.title}</b></>, 'ok', { label: 'Undo', run: () => { undo.current = null; u.run().catch(e => toast(errMsg(e), 'err')) } })
    } catch (e) {
      setOverrides(o => { const n = { ...o }; delete n[it.id]; return n })
      toast(errMsg(e), 'err')
    }
  }

  const askDelete = (it: Item) => {
    if (!it.ev?.can_edit) return
    setPop(null)
    if (it.ev.recurring) setPending({ kind: 'delete', it })
    else remove(it, null)
  }
  const remove = async (it: Item, scope: Scope | null, quiet = false) => {
    const ev = it.ev!
    setHidden(h => new Set(h).add(it.id))
    if (scope === 'all' || scope === 'following') setHidden(h => { const n = new Set(h); for (const x of items) if (x.ev?.event_id === ev.event_id && (scope === 'all' || x.start >= it.start)) n.add(x.id); return n })
    try {
      const id = scope === 'all' ? String(ev.event_id) : ev.id
      const r = await api.del<{ ok: boolean; restore_id: number }>(`${V1}/events/${id}${qs({ scope: ev.recurring && scope ? scope : undefined })}`)
      afterWrite()
      setSelectedId(null)
      if (quiet) { toast('Undone'); return }
      if (r.restore_id) {
        const u: Undo = { label: 'delete', run: async () => { const back = await api.post<Occurrence>(`${V1}/deleted/${r.restore_id}/restore`); afterWrite(); flash(back.id); toast('Restored') } }
        undo.current = u
        toast(<>Deleted <b>{it.title}</b></>, 'ok', { label: 'Undo', run: () => { undo.current = null; u.run().catch(e => toast(errMsg(e), 'err')) } })
      } else toast(scope === 'this' ? 'This occurrence was deleted' : 'Deleted')
    } catch (e) {
      setHidden(h => { const n = new Set(h); n.delete(it.id); return n })
      toast(errMsg(e), 'err')
    }
  }

  const saveFromEditor = async (body: EventBody, scope: Scope | null, ev?: Occurrence): Promise<boolean> => {
    if (!ev) return createEvent(body)
    try {
      const saved = await putEvent(ev, body, scope)
      afterWrite(); flash(saved.id)
      toast('Saved')
      return true
    } catch (e) { toast(errMsg(e), 'err'); return false }
  }

  const rsvp = async (it: Item, status: 'accepted' | 'tentative' | 'declined', scope: Scope) => {
    const ev = it.ev!
    try {
      await api.post(`${V1}/events/${ev.id}/rsvp`, { status, key: ev.key || undefined, scope: ev.recurring ? scope : undefined })
      afterWrite(); app.reloadInvitations()
      toast(status === 'accepted' ? 'You’re going' : status === 'declined' ? 'Declined' : 'Answered maybe')
      if (status === 'declined' && !settings?.show_declined) setPop(null)
    } catch (e) { toast(errMsg(e), 'err') }
  }

  const duplicate = async (it: Item) => {
    try {
      setPop(null)
      const copy = await api.post<Occurrence>(`${V1}/events/${it.ev!.event_id}/duplicate${qs({ tz: tzParam })}`, {})
      afterWrite(); flash(copy.id)
      setEditor({ ev: copy, start: parseWall(copy.start), end: parseWall(copy.end), allDay: copy.all_day })
      toast('Copied — edit the copy')
    } catch (e) { toast(errMsg(e), 'err') }
  }

  const toggleTask = async (it: Item) => {
    const ov = it.ov
    if (!ov?.actions?.includes('complete')) return
    const done = !ov.done
    setDoneFlip(f => ({ ...f, [ov.id]: done }))
    try {
      await api.post(`${V1}/overlays/tasks/${encodeURIComponent(ov.ref)}/complete`, { done })
      afterWrite()
    } catch (e) {
      setDoneFlip(f => { const n = { ...f }; delete n[ov.id]; return n })
      toast(errMsg(e), 'err')
    }
  }

  // ----- opening things -----
  const openItem = (it: Item, anchor: Anchor) => { setQc(null); setSelectedId(it.id); setPop({ item: it, anchor }) }
  const openCreate = (start: Wall, end: Wall, allDay: boolean, anchor: Anchor, attendees?: Attendee[]) => {
    setPop(null); setSelectedId(null)
    if (calendars.every(c => !c.writable)) { toast('You have no calendar you can add events to', 'err'); return }
    setQc({ start, end, allDay, anchor, attendees })
  }
  const newEvent = (base?: Wall) => {
    setPop(null); setQc(null)
    const d = base ?? (date === today || view === 'month' || view === 'year' ? today : date)
    let s: Wall
    if (d === today) { const m = Math.ceil((now - today) / MIN / 30) * 30; s = today + Math.min(m, 23 * 60) * MIN }
    else s = d + (settings?.work_start ?? 9) * 60 * MIN
    setEditor({ start: s, end: s + (settings?.default_duration_min || 60) * MIN, allDay: false })
  }
  const openOccurrence = (ev: Occurrence) => {
    const start = parseWall(ev.start)
    const target = view === 'year' || view === 'people' ? (mobile ? '3day' : 'week') : view
    go(target, startOfDay(start))
    setTimeout(() => {
      const el = document.querySelector<HTMLElement>(`[data-id="${CSS.escape(ev.id)}"]`)
      const r = el?.getBoundingClientRect()
      const cal = calendars.find(c => c.id === ev.calendar_id)
      const it: Item = { id: ev.id, title: ev.title, start, end: parseWall(ev.end), allDay: ev.all_day, color: ev.color || cal?.color || settings?.invites_color || '#64748b', ev }
      setSelectedId(ev.id); setHighlightId(ev.id)
      setPop({ item: it, anchor: r ? { x: r.left, y: r.top, w: r.width, h: r.height } : centerAnchor() })
    }, 450)
  }
  const onCreatedQuick = (ev: Occurrence) => {
    setExtra(x => [...x, ev])
    const s = startOfDay(parseWall(ev.start))
    if (s < range.from || s >= range.to) go(view === 'year' || view === 'people' ? 'week' : view, s)
    flash(ev.id)
    afterWrite()
  }

  // ----- keyboard -----
  const selectedItem = selectedId ? items.find(i => i.id === selectedId) || (pop?.item.id === selectedId ? pop.item : null) : null
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      const typing = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)
      const mod = e.metaKey || e.ctrlKey
      if (mod && e.key.toLowerCase() === 'k') { e.preventDefault(); if (!modalOpen()) setPalette(true); return }
      if (typing || modalOpen()) return
      if (mod && e.key.toLowerCase() === 'z') {
        if (undo.current) { e.preventDefault(); const u = undo.current; undo.current = null; u.run().catch(err => toast(errMsg(err), 'err')) }
        return
      }
      if (mod || e.altKey) return
      const k = e.key
      const views: Record<string, ViewName> = { d: 'day', x: '3day', w: 'week', m: 'month', y: 'year', a: 'agenda', p: 'people' }
      if (k === 't') goToday()
      else if (k === 'j' || k === 'ArrowRight' || k === 'n') next()
      else if (k === 'k' || k === 'ArrowLeft') prev()
      else if (views[k]) goView(views[k])
      else if (k === 'c') { e.preventDefault(); newEvent() }
      else if (k === 'q') { e.preventDefault(); if (mobile) setMobileAdd(true); else document.querySelector<HTMLInputElement>('.quickadd input')?.focus() }
      else if (k === '/') { e.preventDefault(); setPalette(true) }
      else if (k === '?') setShortcuts(true)
      else if ((k === 'Delete' || k === 'Backspace') && selectedItem?.ev?.can_edit) { e.preventDefault(); askDelete(selectedItem) }
      else if (k === 'e' && selectedItem?.ev?.can_edit) { setPop(null); setEditor({ ev: selectedItem.ev, start: selectedItem.start, end: selectedItem.end, allDay: selectedItem.allDay }) }
      else if (k === 'Escape') { setPop(null); setQc(null); setSelectedId(null) }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })

  // ----- swipe on phones -----
  const swipe = useRef<{ x: number; y: number; t: number } | null>(null)
  const onTouchStart = (e: TouchEvent) => { const p = e.touches[0]; swipe.current = { x: p.clientX, y: p.clientY, t: Date.now() } }
  const onTouchEnd = (e: TouchEvent) => {
    const s = swipe.current; swipe.current = null
    if (!s || !mobile || view === 'agenda') return
    const p = e.changedTouches[0]
    const dx = p.clientX - s.x, dy = p.clientY - s.y
    if (Math.abs(dx) > 70 && Math.abs(dx) > Math.abs(dy) * 1.6 && Date.now() - s.t < 600) (dx < 0 ? next : prev)()
  }

  // ----- palette commands -----
  const commands: PaletteCommand[] = [
    { id: 'new', label: 'New event', icon: 'plus', hint: 'C', run: () => newEvent() },
    { id: 'today', label: 'Go to today', icon: 'today', hint: 'T', run: goToday },
    ...(['day', '3day', 'week', 'month', 'year', 'agenda', 'people'] as ViewName[]).map(v => ({ id: 'v' + v, label: `${VIEW_LABEL[v]} view`, icon: v === 'people' ? 'users' : v === 'agenda' ? 'list' : v === 'year' ? 'grid' : 'calendar', hint: VIEW_KEY[v].toUpperCase(), keywords: 'view switch', run: () => goView(v) })),
    { id: 'settings', label: 'Settings', icon: 'settings', run: () => navigate('/settings') },
    { id: 'apps', label: 'Settings: From your apps', icon: 'layers', keywords: 'overlays tasks travel', run: () => navigate('/settings/apps') },
    { id: 'sync', label: 'Settings: Sync with phone (CalDAV)', icon: 'smartphone', keywords: 'caldav ios android feed', run: () => navigate('/settings/sync') },
    { id: 'accounts', label: 'Settings: Add an account', icon: 'globe', keywords: 'icloud google nextcloud caldav', run: () => navigate('/settings/accounts') },
    { id: 'theme', label: app.theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode', icon: app.theme === 'dark' ? 'sun' : 'moon', keywords: 'theme appearance', run: () => app.setThemeMode(app.theme === 'dark' ? 'light' : 'dark') },
    { id: 'shortcuts', label: 'Keyboard shortcuts', icon: 'keyboard', hint: '?', run: () => setShortcuts(true) },
  ]

  if (rawView && !isView(rawView)) return <Navigate to="/" replace />
  if (!settings) return null

  const draft: Draft | null = qc ? { start: qc.start, end: qc.end, allDay: qc.allDay } : null
  const viewProps = {
    items, holidays, loading, today, now, selectedId, highlightId, draft,
    onOpen: openItem, onCreate: (s: Wall, e: Wall, ad: boolean, a: Anchor) => openCreate(s, e, ad, a), onMove: move, onToggleTask: toggleTask,
    goDay: (d: Wall) => go('day', d),
  }
  const showSidebar = sidebarOpen

  return (
    <div className={`app ${showSidebar ? 'with-sidebar' : 'no-sidebar'} ${mobile ? 'is-mobile' : ''} ${drawer ? 'drawer-mode' : ''}`}>
      {drawer && showSidebar && <div className="drawer-backdrop" onClick={e => { if (e.timeStamp - drawerOpenedAt.current > 400) setSidebarOpen(false) }} />}
      <div className={`sidebar-wrap ${showSidebar ? 'open' : ''}`}>
        <Sidebar date={view === 'year' && new Date(today).getUTCFullYear() === new Date(date).getUTCFullYear() ? today : date} selected={view === 'month' || view === 'year' ? null : date} today={today} range={view === 'month' || view === 'year' || view === 'agenda' ? null : [range.from, range.to]}
          onPick={d => go(view === 'year' ? 'day' : view, d)} onCreate={() => { if (drawer) setSidebarOpen(false); newEvent() }} dots={dots}
          onOpenInvite={openOccurrence} onNavigate={() => drawer && setSidebarOpen(false)} sourceErrors={shown?.errors} />
      </div>
      <main className="main">
        <TopBar title={mobile ? range.short : range.title} view={view} onView={goView} onToday={goToday} onPrev={prev} onNext={next}
          onPalette={() => setPalette(true)} onMenu={toggleSidebar} mobile={mobile} compact={narrow} onCreated={onCreatedQuick} loading={loading} sidebarOpen={showSidebar} />
        {error && !shown && <div className="callout danger m"><Icon name="alert" size={15} />Couldn’t load events: {error} <button className="link-btn" onClick={bump}>Retry</button></div>}
        {view === 'people' && (
          <div className="subbar">
            <Segmented size="sm" value={peopleSpan} onChange={s => navigate(urlFor('people', date, today, `?${new URLSearchParams({ ...(withIds.length ? { with: withIds.join(',') } : {}), span: s })}`))} options={[{ value: 'day', label: 'Day' }, { value: 'week', label: 'Week' }]} label="Span" />
          </div>
        )}
        <div className={`view view-${view} ${loading && !shown ? 'is-loading' : ''}`} onTouchStart={onTouchStart} onTouchEnd={onTouchEnd}>
          {(view === 'day' || view === '3day' || view === 'week') && (
            <TimeGrid key={view} {...viewProps} days={range.days} settings={settings} h12={h12} tz={tz} />
          )}
          {view === 'month' && <MonthView {...viewProps} gridStart={range.from} month={new Date(date).getUTCMonth()} settings={settings} h12={h12} />}
          {view === 'year' && <YearView year={range.from} items={items} today={today} weekStart={settings.week_start} loading={loading} goDay={d => go('day', d)} goMonth={m => go('month', m)} />}
          {view === 'agenda' && <AgendaView {...viewProps} from={range.from} days={agendaDays} h12={h12} calendars={calendars} onMore={() => setAgendaDays(n => Math.min(n + 60, 390))} />}
          {view === 'people' && (
            <PeopleView days={range.days} today={today} now={now} selected={withIds}
              onSelected={ids => navigate(urlFor('people', date, today, `?${new URLSearchParams({ ...(ids.length ? { with: ids.join(',') } : {}), ...(search.get('span') ? { span: peopleSpan } : {}) })}`), { replace: true })}
              onCreate={(s, e, ppl: PeopleEntry[], a) => openCreate(s, e, false, a, ppl.map(p => ({ email: p.email, name: p.name })))} />
          )}
        </div>
        {mobile && <button type="button" className="fab" aria-label="New event" onClick={() => newEvent()}><Icon name="plus" size={24} strokeWidth={2.2} /></button>}
        {mobile && <button type="button" className="fab-2" aria-label="Quick add" onClick={() => setMobileAdd(true)}><Icon name="sparkles" size={18} /></button>}
      </main>

      {pop && pop.item.ev && (
        <EventPopover item={pop.item} anchor={pop.anchor} onClose={() => setPop(null)} actions={{
          onEdit: () => { const it = pop.item; setPop(null); setEditor({ ev: it.ev, start: it.start, end: it.end, allDay: it.allDay }) },
          onDuplicate: () => duplicate(pop.item),
          onDelete: () => askDelete(pop.item),
          onRsvp: (s, sc) => rsvp(pop.item, s, sc),
          onDownload: () => download(`${V1}/events/${pop.item.ev!.event_id}/ics`),
          onCopyLink: async () => toast(await copyText(`${location.origin}/event/${pop.item.ev!.id}`) ? 'Link copied' : 'Could not copy'),
        }} />
      )}
      {pop && pop.item.ov && <OverlayPopover item={pop.item} anchor={pop.anchor} onClose={() => setPop(null)} onToggle={() => toggleTask(pop.item)} />}
      {qc && (
        <QuickCreate anchor={qc.anchor} start={qc.start} end={qc.end} allDay={qc.allDay} attendees={qc.attendees} onClose={() => setQc(null)} onCreate={createEvent}
          onMore={d => { setQc(null); setEditor({ title: d.title, start: d.start, end: d.end, allDay: d.allDay, calendarId: d.calendarId, attendees: d.attendees }) }} />
      )}
      {editor && <EventEditor draft={editor} onClose={() => setEditor(null)} onSave={saveFromEditor} />}
      {pending?.kind === 'move' && (
        <ScopeDialog verb="Move" onCancel={() => { setOverrides(o => { const n = { ...o }; delete n[pending.it.id]; return n }); setPending(null) }}
          onPick={s => { const p = pending; setPending(null); doMove(p.it, p.start, p.end, p.allDay, s) }} />
      )}
      {pending?.kind === 'delete' && <ScopeDialog verb="Delete" onCancel={() => setPending(null)} onPick={s => { const p = pending; setPending(null); remove(p.it, s) }} />}
      {palette && <CommandPalette onClose={() => setPalette(false)} commands={commands} goDate={(d, v) => go(v || (view === 'year' || view === 'agenda' ? 'day' : view), d)} openEvent={openOccurrence} />}
      {shortcuts && <ShortcutsSheet onClose={() => setShortcuts(false)} />}
      {mobileAdd && (
        <div className="mobile-qa" role="dialog" aria-label="Quick add">
          <div className="mobile-qa-box">
            <QuickAdd autoFocus onCreated={onCreatedQuick} onDone={() => setMobileAdd(false)} compact />
            <button type="button" className="btn ghost sm" onClick={() => setMobileAdd(false)}>Cancel</button>
          </div>
          <div className="mobile-qa-back" onClick={() => setMobileAdd(false)} />
        </div>
      )}
    </div>
  )
}

