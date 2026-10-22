import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { Occurrence, ViewName } from '../lib/types'
import { VIEW_KEY, VIEW_LABEL } from './CommandPalette'
import { Icon } from './icons'
import { QuickAdd } from './QuickAdd'
import { Select } from './Select'
import { Avatar, MenuButton, Segmented } from './ui'
import { AskButton } from './AskButton'

const DESKTOP_VIEWS: ViewName[] = ['day', '3day', 'week', 'month', 'year', 'agenda', 'people']

export function TopBar({ title, view, onView, onToday, onPrev, onNext, onPalette, onMenu, mobile, compact, onCreated, loading, sidebarOpen }: {
  title: string
  view: ViewName
  onView: (v: ViewName) => void
  onToday: () => void
  onPrev: () => void
  onNext: () => void
  onPalette: () => void
  onMenu: () => void
  mobile: boolean
  compact?: boolean
  onCreated: (ev: Occurrence) => void
  loading: boolean
  sidebarOpen: boolean
}) {
  const { user, themeMode, setThemeMode, status, refresh } = useApp()
  const navigate = useNavigate()
  const signOut = async () => { await api.post(`${V1}/auth/logout`).catch(() => {}); await refresh(); navigate('/login') }
  const avatarMenu = () => [
    { heading: user?.name || user?.email || '' },
    { label: 'Profile', icon: 'user', onSelect: () => navigate('/settings/profile') },
    { label: 'Settings', icon: 'settings', onSelect: () => navigate('/settings'), hint: '' },
    { label: 'Keyboard shortcuts', icon: 'keyboard', onSelect: () => window.dispatchEvent(new CustomEvent('pcal:shortcuts')), hint: '?' },
    'sep' as const,
    { heading: 'Appearance' },
    { label: 'System', checked: themeMode === 'system', onSelect: () => setThemeMode('system') },
    { label: 'Light', checked: themeMode === 'light', onSelect: () => setThemeMode('light') },
    { label: 'Dark', checked: themeMode === 'dark', onSelect: () => setThemeMode('dark') },
    ...(status?.auth_mode === 'local' ? ['sep' as const, { label: 'Sign out', icon: 'log-out', onSelect: signOut }] : []),
  ]
  return (
    <header className={`topbar ${loading ? 'is-loading' : ''}`}>
      <button type="button" className="btn ghost icon-only" onClick={onMenu} aria-label={sidebarOpen ? 'Hide the sidebar' : 'Show the sidebar'} title="Menu"><Icon name={mobile || compact ? 'menu' : 'sidebar'} size={18} /></button>
      {!mobile && <button type="button" className="btn today-btn" onClick={onToday} title="Today (t)">Today</button>}
      <div className="nav-arrows">
        <button type="button" className="btn ghost icon-only" onClick={onPrev} aria-label="Previous" title="Previous (k)"><Icon name="chevron-left" size={18} /></button>
        <button type="button" className="btn ghost icon-only" onClick={onNext} aria-label="Next" title="Next (j)"><Icon name="chevron-right" size={18} /></button>
      </div>
      <h1 className="range-title" aria-live="polite">{title}</h1>
      <div className="topbar-load" aria-hidden="true" />
      <span className="spacer" />
      {!mobile && <QuickAdd onCreated={onCreated} />}
      {mobile ? (
        <>
          <button type="button" className="btn ghost icon-only" onClick={onToday} aria-label="Today"><Icon name="today" size={18} /></button>
          <div className="view-select"><Select value={view} onChange={onView} options={DESKTOP_VIEWS.filter(v => v !== 'year').map(v => ({ value: v, label: VIEW_LABEL[v] }))} size="sm" label="View" width={160} /></div>
        </>
      ) : compact ? (
        <div className="view-select"><Select value={view} onChange={onView} options={DESKTOP_VIEWS.map(v => ({ value: v, label: VIEW_LABEL[v], hint: VIEW_KEY[v].toUpperCase() }))} size="sm" label="View" width={180} /></div>
      ) : (
        <Segmented value={view} onChange={onView} className="view-switch" label="View" options={DESKTOP_VIEWS.map(v => ({ value: v, label: VIEW_LABEL[v], title: `${VIEW_LABEL[v]} (${VIEW_KEY[v]})` }))} />
      )}
      <button type="button" className="btn ghost icon-only" onClick={onPalette} aria-label="Search" title="Search (⌘K or /)"><Icon name="search" size={18} /></button>
      {mobile && <AskButton />}
      <MenuButton label="Account" className="avatar-btn" align="right" width={220} items={avatarMenu}>
        <Avatar name={user?.name} email={user?.email} size={30} />
      </MenuButton>
    </header>
  )
}
