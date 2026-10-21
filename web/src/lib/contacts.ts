// Pocket Contacts through the calendar's own API (it asks Contacts as the
// person; when the Birthdays source is off it simply answers nobody).
import { useEffect, useState } from 'react'
import { api, qs, V1 } from './api'
import { useApp } from './app-context'
import type { ContactRef, ContactSuggestion } from './types'

/** Is Pocket Contacts switched on (the "birthdays" source)? */
export function useContactsOn(): boolean {
  const { sources } = useApp()
  const s = sources.find(x => x.id === 'birthdays')
  return !!s && s.mode !== 'off' && s.status !== 'error'
}

/** Guests from Contacts for what is typed (2+ letters, debounced). */
export function useContactSuggestions(q: string, on: boolean): ContactSuggestion[] {
  const [found, setFound] = useState<ContactSuggestion[]>([])
  useEffect(() => {
    const n = q.trim()
    if (!on || n.length < 2) { setFound([]); return }
    let live = true
    const t = setTimeout(() => {
      api.get<ContactSuggestion[]>(`${V1}/contacts/suggest${qs({ q: n, limit: 8 })}`)
        .then(r => { if (live) setFound(r || []) })
        .catch(() => { if (live) setFound([]) })
    }, 220)
    return () => { live = false; clearTimeout(t) }
  }, [q, on])
  return found
}

const known = new Map<string, { at: number; ref: ContactRef | null }>()
const FRESH = 5 * 60_000

/** The contacts these emails belong to; asked after the caller rendered, so it never waits on Contacts. */
export function useContactRefs(emails: (string | undefined)[], on: boolean): Record<string, ContactRef> {
  const list = [...new Set(emails.filter((e): e is string => !!e).map(e => e.toLowerCase()))].sort()
  const key = list.join(',')
  const pick = () => Object.fromEntries(list.flatMap(e => { const k = known.get(e); return k?.ref ? [[e, k.ref]] : [] }))
  const [refs, setRefs] = useState<Record<string, ContactRef>>(pick)
  useEffect(() => {
    setRefs(pick())
    if (!on || !list.length) return
    const missing = list.filter(e => { const k = known.get(e); return !k || Date.now() - k.at > FRESH })
    if (!missing.length) return
    let live = true
    api.get<Record<string, ContactRef>>(`${V1}/contacts/lookup${qs({ email: missing.slice(0, 30).join(',') })}`)
      .then(r => {
        for (const e of missing.slice(0, 30)) known.set(e, { at: Date.now(), ref: r?.[e] || null })
        if (live) setRefs(pick())
      })
      .catch(() => {})
    return () => { live = false }
  }, [key, on]) // eslint-disable-line react-hooks/exhaustive-deps
  return refs
}
