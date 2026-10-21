import { useEffect } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { Icon } from '../components/icons'
import { Tabs, useIsMobile } from '../components/ui'
import { GeneralTab } from './settings/General'
import { CalendarsTab } from './settings/Calendars'
import { NotificationsTab } from './settings/Notifications'
import { AppsTab } from './settings/Apps'
import { AccountsTab } from './settings/Accounts'
import { SyncTab } from './settings/Sync'
import { ProfileTab } from './settings/Profile'
import { PeopleTab } from './settings/People'
import { TokensTab } from './settings/Tokens'
import { AboutTab } from './settings/About'

type TabId = 'general' | 'calendars' | 'notifications' | 'apps' | 'accounts' | 'sync' | 'profile' | 'people' | 'tokens' | 'about'

const TABS: { id: TabId; label: string; icon: string; sub: string; admin?: boolean }[] = [
  { id: 'general', label: 'General', icon: 'settings', sub: 'Time zone, week, working hours and how the calendar looks.' },
  { id: 'calendars', label: 'Calendars', icon: 'calendar', sub: 'Your calendars, the ones shared with you, and recently deleted events.' },
  { id: 'notifications', label: 'Notifications', icon: 'bell', sub: 'Where reminders, invitations and the morning agenda are sent.' },
  { id: 'apps', label: 'From your apps', icon: 'layers', sub: 'Tasks, trips, bills and more from your other Pocket apps, on the same grid.' },
  { id: 'accounts', label: 'Accounts', icon: 'globe', sub: 'Bring in calendars from iCloud, Google, Nextcloud and other CalDAV servers.' },
  { id: 'sync', label: 'Sync & feeds', icon: 'smartphone', sub: 'Use this calendar on your phone, laptop or anywhere that reads calendar feeds.' },
  { id: 'profile', label: 'Profile', icon: 'user', sub: 'Your name, email, CalDAV user name and password.' },
  { id: 'people', label: 'People', icon: 'users', sub: 'Everyone who has an account on this box.', admin: true },
  { id: 'tokens', label: 'Tokens', icon: 'key', sub: 'Personal tokens for the API and for CalDAV apps.' },
  { id: 'about', label: 'About', icon: 'info', sub: 'Version, numbers and the Pocket Core link.' },
]

export default function Settings() {
  const { tab: raw } = useParams()
  const navigate = useNavigate()
  const { user } = useApp()
  const mobile = useIsMobile()
  const isAdmin = user?.role === 'admin'
  const tabs = TABS.filter(t => !t.admin || isAdmin)
  const tab: TabId = (tabs.find(t => t.id === raw)?.id) || 'general'
  const current = tabs.find(t => t.id === tab)!
  const go = (id: TabId) => navigate(`/settings/${id}`, { replace: true })

  useEffect(() => { document.querySelector('.set-content')?.scrollTo({ top: 0 }) }, [tab])

  return (
    <div className="set-page">
      <header className="set-header">
        <button type="button" className="btn ghost icon-only" aria-label="Back to the calendar" title="Back to the calendar" onClick={() => navigate('/')}><Icon name="chevron-left" size={18} /></button>
        <img src="/favicon.svg" alt="" width={24} height={24} className="set-logo" />
        <h1>Settings</h1>
      </header>
      {mobile && <div className="set-tabs-mobile"><Tabs tabs={tabs.map(t => ({ id: t.id, label: t.label, icon: t.icon }))} value={tab} onChange={go} /></div>}
      <div className="set-body">
        {!mobile && (
          <nav className="set-nav" aria-label="Settings sections">
            {tabs.map(t => (
              <button key={t.id} type="button" className={`set-nav-item ${t.id === tab ? 'active' : ''}`} aria-current={t.id === tab ? 'page' : undefined} onClick={() => go(t.id)}>
                <Icon name={t.icon} size={16} />{t.label}
              </button>
            ))}
          </nav>
        )}
        <main className="set-content">
          <div className="set-inner">
            <div className="set-title">
              <h2>{current.label}</h2>
              <p>{current.sub}</p>
            </div>
            {tab === 'general' && <GeneralTab />}
            {tab === 'calendars' && <CalendarsTab />}
            {tab === 'notifications' && <NotificationsTab />}
            {tab === 'apps' && <AppsTab />}
            {tab === 'accounts' && <AccountsTab />}
            {tab === 'sync' && <SyncTab goTokens={() => go('tokens')} goProfile={() => go('profile')} />}
            {tab === 'profile' && <ProfileTab />}
            {tab === 'people' && <PeopleTab />}
            {tab === 'tokens' && <TokensTab />}
            {tab === 'about' && <AboutTab />}
          </div>
        </main>
      </div>
    </div>
  )
}
