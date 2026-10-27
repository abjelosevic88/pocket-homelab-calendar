// Capture the real demo UI for the README. Setup: docs/screenshots/README.md.
// Node 22+; Chrome's DevTools protocol, with no npm dependencies.
//
// The script adds a little more demo data than DEMO_DATA does (three more
// people, invitations, overlaps, a free/busy-only calendar) and serves a tiny
// stand-in for Pocket Tasks / Travel / Garage / Invoicing / Review on
// 127.0.0.1:18199, so the "From your apps" overlays have something to show.
// It only talks to the demo instance it is pointed at.
import fs from 'node:fs/promises'
import http from 'node:http'

const base = process.env.BASE || 'http://127.0.0.1:18191'
const chrome = process.env.CHROME || 'http://127.0.0.1:19228'
const font = process.env.FONT || '' // optional .woff2 (e.g. Inter) for consistent captures
const admin = { email: process.env.ADMIN_EMAIL || 'alex@example.com', password: process.env.ADMIN_PASSWORD || 'demo-password' }
const out = new URL('../docs/screenshots/', import.meta.url)
const MOCK_PORT = 18199

// ---------- dates relative to this week (the demo data is too) ----------
const pad = n => String(n).padStart(2, '0')
const ymd = d => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
const now = new Date()
const monday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - ((now.getDay() + 6) % 7))
const day = n => ymd(new Date(monday.getFullYear(), monday.getMonth(), monday.getDate() + n))
const today = ymd(now)

// ---------- a stand-in for the other Pocket apps ----------
const tasks = [
  { id: 101, title: 'Renew passport', start: day(1), list: { name: 'Home', color: '#2563eb' } },
  { id: 102, title: 'Send quarterly VAT report', due: day(4), list: { name: 'Work', color: '#7c3aed' }, priority: 3 },
  { id: 103, title: 'Call the plumber', start: day(2) + 'T11:30:00', estimate_min: 30, list: { name: 'Home', color: '#2563eb' } },
  { id: 104, title: 'Water the plants', start: day(2), list: { name: 'Home', color: '#2563eb' } },
  { id: 105, title: 'Book dentist for Sani', start: day(0), completed: true, list: { name: 'Family', color: '#10b981' } },
  { id: 106, title: 'Pay the electricity bill', due: day(3), list: { name: 'Home', color: '#2563eb' } },
]
const flipped = new Set()
const mock = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x')
  const json = (code, v) => { res.writeHead(code, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(v)) }
  let body = ''
  req.on('data', c => { body += c })
  req.on('end', () => {
    let m
    if (u.pathname === '/api/v1/calendar') {
      const from = u.searchParams.get('from'), to = u.searchParams.get('to'), days = {}
      const add = (d, k, t) => { if (d >= from && d < to) (days[d] ||= { start: [], due: [], done: [] })[k].push(t) }
      for (const t of tasks) {
        const done = !!t.completed !== flipped.has(t.id)
        const tj = { id: t.id, title: t.title, start: t.start || '', due: t.due || '', estimate_min: t.estimate_min || 0, priority: t.priority || 0, list: t.list, status: done ? 'done' : 'open', overdue: false }
        if (done) add((t.start || t.due).slice(0, 10), 'done', tj)
        else { if (t.start) add(t.start.slice(0, 10), 'start', tj); if (t.due) add(t.due.slice(0, 10), 'due', tj) }
      }
      return json(200, { days })
    }
    if ((m = /^\/api\/v1\/tasks\/(\d+)\/complete$/.exec(u.pathname))) { const id = +m[1]; flipped.has(id) ? flipped.delete(id) : flipped.add(id); return json(200, { ok: true }) }
    if ((m = /^\/api\/v1\/tasks\/(\d+)$/.exec(u.pathname))) { const t = tasks.find(x => x.id === +m[1]); Object.assign(t || {}, JSON.parse(body || '{}')); return json(200, t || {}) }
    if (u.pathname === '/api/v1/trips') return json(200, [{ id: 7, name: 'Vienna weekend', destination: 'Vienna, Austria', start_date: day(10), end_date: day(13), status: 'planned', color: '#0ea5e9' }])
    if (u.pathname === '/api/v1/vehicles') return json(200, [{ id: 3, name: 'Škoda Octavia', summary: { next_reminder: { id: 31, title: 'Winter tyres', due_date: day(3), urgency: 'soon', status: 'open' } } }])
    if (u.pathname === '/api/v1/invoices') return json(200, { items: [{ id: 55, number: '2026-041', client_name: 'Athena Studio', status: 'sent', due_date: day(4), currency: 'EUR', total: 2400, balance: 2400 }] })
    if (u.pathname === '/api/v1/reviews') return json(200, [{ id: 9, title: 'Onboarding copy v3', status: 'open', due_date: day(8), decided_at: null }])
    if (u.pathname === '/api/v1/habits') return json(200, [])
    if (u.pathname === '/api/v1/birthdays') {
      const from = u.searchParams.get('from'), until = ymd(new Date(new Date(from + 'T12:00:00').getTime() + (+u.searchParams.get('days') || 60) * 86400000))
      const marko = { id: 12, fn: 'Marko Petrović', emails: ['marko@example.com'], has_photo: false, photo_url: '' }
      return json(200, [{ contact: marko, kind: 'birthday', date: day(2), original: '1988-' + day(2).slice(5), age: +day(2).slice(0, 4) - 1988, days: 0 }].filter(b => b.date >= from && b.date < until))
    }
    if (u.pathname === '/api/v1/suggest') return json(200, [{ id: 12, name: 'Marko Petrović', email: 'marko@example.com', phone: '', photo_url: '' }])
    if (u.pathname === '/api/v1/lookup') return json(200, { contacts: [] })
    json(404, { error: 'not found' })
  })
})
await new Promise(r => mock.listen(MOCK_PORT, '127.0.0.1', r))

// ---------- API helpers (session cookies per person) ----------
const sessions = {}
async function login(who) {
  if (sessions[who.email]) return sessions[who.email]
  const r = await fetch(`${base}/api/v1/auth/login`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(who) })
  if (!r.ok) throw new Error(`Sign-in as ${who.email} failed (${r.status}). Start the demo with AUTH_MODE=local, ADMIN_EMAIL and ADMIN_PASSWORD — see docs/screenshots/README.md.`)
  return (sessions[who.email] = r.headers.get('set-cookie').split(';')[0])
}
async function call(method, path, body, who = admin) {
  const r = await fetch(`${base}/api/v1${path}`, { method, headers: { 'Content-Type': 'application/json', 'X-Pocket-Source': 'web', Cookie: await login(who) }, body: body ? JSON.stringify(body) : undefined })
  const t = await r.text()
  if (!r.ok) throw new Error(`${method} ${path}: ${r.status} ${t}`)
  return t ? JSON.parse(t) : null
}

// ---------- extra demo data (once per database) ----------
const sani = { email: 'sani@example.com', password: 'demo-password' }, maya = { email: 'maya@example.com', password: 'demo-password' }
const users = await call('GET', '/users')
if (!users.some(u => u.email === sani.email)) {
  for (const [email, name] of [[sani.email, 'Sani Hadžić'], [maya.email, 'Maya Kovač'], ['emir@example.com', 'Emir Begić']]) await call('POST', '/users', { email, name, password: 'demo-password', role: 'member' })
  const tz = 'Europe/Sarajevo'
  const ev = (who, b) => call('POST', '/events', { tz, ...b }, who)
  await call('PUT', '/settings', { freebusy_shared: true }, sani)
  await call('PUT', '/settings', { freebusy_shared: true }, maya)
  await ev(sani, { title: 'Pilates', start: `${day(1)}T08:00`, end: `${day(1)}T09:00` })
  await ev(sani, { title: 'Work', start: `${day(2)}T09:00`, end: `${day(2)}T13:00` })
  await ev(sani, { title: 'School pickup', start: `${day(2)}T15:30`, end: `${day(2)}T16:15`, rrule: 'FREQ=WEEKLY;BYDAY=MO,WE,FR' })
  await ev(sani, { title: 'Coffee with Alex', start: `${day(3)}T10:00`, end: `${day(3)}T10:45`, location: 'Café Tito', attendees: [{ email: admin.email }] })
  await ev(sani, { title: 'Parents’ evening', start: `${day(4)}T17:30`, end: `${day(4)}T18:30`, attendees: [{ email: admin.email }, { email: maya.email }] })
  const mcal = (await call('GET', '/calendars', null, maya))[0]
  await call('POST', `/calendars/${mcal.id}/shares`, { email: admin.email, role: 'freebusy' }, maya)
  await ev(maya, { title: 'Doctor', start: `${day(1)}T11:00`, end: `${day(1)}T12:00` })
  await ev(maya, { title: 'Workshop', start: `${day(3)}T13:00`, end: `${day(3)}T16:00` })
  const cals = await call('GET', '/calendars')
  const cal = n => cals.find(c => c.name === n)?.id
  await ev(admin, { calendar_id: cal('Work'), title: 'Review PR #482', start: `${day(1)}T14:00`, end: `${day(1)}T14:45` })
  await ev(admin, { calendar_id: cal('Personal'), title: 'Call with the bank', start: `${day(1)}T14:30`, end: `${day(1)}T15:00` })
  await ev(admin, { calendar_id: cal('Personal'), title: 'Lunch with Emir', start: `${day(2)}T12:30`, end: `${day(2)}T13:30`, location: 'Kod Keke, Sarajevo', attendees: [{ email: 'emir@example.com', name: 'Emir Begić' }] })
  await ev(admin, { calendar_id: cal('Work'), title: 'Sprint retro', start: `${day(4)}T15:00`, end: `${day(4)}T16:00`, conference: 'https://meet.jit.si/pocket-retro', attendees: [{ email: sani.email, name: 'Sani Hadžić' }, { email: maya.email, name: 'Maya Kovač' }], description: 'What went well, what to change.\nNotes: https://docs.example.com/retro' })
  await ev(admin, { calendar_id: cal('Family'), title: 'Swimming lessons', start: `${day(5)}T10:00`, end: `${day(5)}T11:00`, rrule: 'FREQ=WEEKLY;BYDAY=SA' })
  await ev(admin, { calendar_id: cal('Work'), title: 'WebDev Days', start: day(15), end: day(17), all_day: true, location: 'Ljubljana' })
}
for (const s of ['tasks', 'travel', 'garage', 'invoicing', 'review', 'birthdays']) await call('PUT', `/overlays/sources/${s}`, { mode: 'direct', url: `http://127.0.0.1:${MOCK_PORT}`, token: 'demo' })
const people = await call('GET', '/people')
const withIds = people.filter(p => p.email === sani.email || p.email === maya.email).map(p => p.id).join(',')
const retro = (await call('GET', `/events?from=${day(0)}&to=${day(7)}`)).events.find(e => e.title === 'Sprint retro')
const cookie = (await login(admin)).split('=')

// ---------- Chrome ----------
const version = await (await fetch(`${chrome}/json/version`)).json()
const ws = new WebSocket(version.webSocketDebuggerUrl)
await new Promise((resolve, reject) => { ws.onopen = resolve; ws.onerror = reject })
let id = 0
const pending = new Map()
ws.onmessage = event => { const message = JSON.parse(event.data); pending.get(message.id)?.(message) }
const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
  const requestId = ++id
  const timer = setTimeout(() => { pending.delete(requestId); reject(new Error(`Timeout: ${method}`)) }, 20000)
  pending.set(requestId, message => {
    clearTimeout(timer)
    pending.delete(requestId)
    if (message.error) reject(new Error(`${method}: ${JSON.stringify(message.error)}`))
    else resolve(message.result)
  })
  ws.send(JSON.stringify({ id: requestId, method, params, sessionId }))
})
let fontCSS = ''
if (font) fontCSS = `@font-face{font-family:Inter;font-weight:100 900;src:url(data:font/woff2;base64,${(await fs.readFile(font)).toString('base64')}) format('woff2')}`

let targetId
try {
  ;({ targetId } = await send('Target.createTarget', { url: 'about:blank' }))
  const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
  const s = (method, params) => send(method, params, sessionId)
  await s('Page.enable')
  await s('Runtime.enable')
  await s('Network.enable')
  await s('Emulation.setTimezoneOverride', { timezoneId: 'Europe/Sarajevo' })
  await s('Network.setCookie', { name: cookie[0], value: cookie.slice(1).join('='), url: base })
  const evaluate = async expression => {
    const result = await s('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true })
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails))
    return result.result.value
  }
  const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))
  const waitFor = async expression => {
    const deadline = Date.now() + 15000
    while (Date.now() < deadline) {
      if (await evaluate(`Boolean(${expression})`)) return
      await sleep(100)
    }
    throw new Error(`UI did not become ready: ${expression}`)
  }
  // a real click: pointer down/up at an element's centre
  const click = async (selector, dx = 0, dy = 0) => {
    const r = await evaluate(`(() => { const el = ${selector}; if (!el) return null; const b = el.getBoundingClientRect(); return { x: b.left + b.width / 2, y: b.top + Math.min(b.height / 2, 12) } })()`)
    if (!r) throw new Error(`Nothing to click: ${selector}`)
    for (const type of ['mouseMoved', 'mousePressed', 'mouseReleased']) await s('Input.dispatchMouseEvent', { type, x: r.x + dx, y: r.y + dy, button: 'left', buttons: type === 'mousePressed' ? 1 : 0, clickCount: 1 })
  }
  const byTitle = t => `[...document.querySelectorAll('.ev-block')].find(e => e.querySelector('.ev-title')?.textContent === ${JSON.stringify(t)})`
  await fs.mkdir(out, { recursive: true })
  let setup
  async function shot(name, path, { mobile = false, dark = false, ready, before } = {}) {
    await s('Emulation.setDeviceMetricsOverride', { width: mobile ? 390 : 1440, height: mobile ? 844 : 900, deviceScaleFactor: mobile ? 2 : 1, mobile })
    await s('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: dark ? 'dark' : 'light' }] })
    if (setup) await s('Page.removeScriptToEvaluateOnNewDocument', { identifier: setup })
    ;({ identifier: setup } = await s('Page.addScriptToEvaluateOnNewDocument', {
      source: `localStorage.setItem('pcal-theme', '${dark ? 'dark' : 'light'}'); localStorage.removeItem('pcal-sidebar');` +
        (fontCSS ? `document.addEventListener('DOMContentLoaded', () => { const st = document.createElement('style'); st.textContent = ${JSON.stringify(fontCSS)}; document.head.appendChild(st) })` : ''),
    }))
    await s('Page.navigate', { url: base + path })
    await waitFor("(document.querySelector('.topbar') || document.querySelector('.set-page, .set-shell, h1')) && !document.querySelector('.boot')")
    if (ready) await waitFor(ready)
    await sleep(500)
    if (before) await before()
    await evaluate('document.fonts.ready.then(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))')
    await sleep(250)
    const { data } = await s('Page.captureScreenshot', { format: 'png' })
    await fs.writeFile(new URL(`${name}.png`, out), Buffer.from(data, 'base64'))
    console.log(`Captured ${name}.png`)
  }
  const weekReady = "document.querySelector('.ev-block') && document.querySelector('.lane-bar.ov-task')"
  await shot('week', `/week/${today}`, { ready: weekReady })
  await shot('week-dark', `/week/${today}`, { dark: true, ready: weekReady })
  await shot('event', `/week/${today}`, { ready: weekReady, before: async () => { await click(byTitle('Sprint retro')); await waitFor("document.querySelector('.evpop-title')") } })
  await shot('invitation', `/week/${today}`, { ready: weekReady, before: async () => { await click(byTitle('Coffee with Alex')); await waitFor("document.querySelector('.rsvp-box')") } })
  await shot('editor', `/week/${today}`, {
    ready: weekReady,
    before: async () => {
      await click(byTitle('Sprint retro'))
      await waitFor("document.querySelector('.evpop-bar [aria-label=Edit]')")
      await evaluate("document.querySelector('.evpop-bar [aria-label=Edit]').click()")
      await waitFor("document.querySelector('.editor-title')")
    },
  })
  await shot('quick-add', `/week/${today}`, {
    ready: weekReady,
    before: async () => {
      await evaluate("document.querySelector('.topbar .quickadd input').focus()")
      await s('Input.insertText', { text: 'Dinner with Sani fri 19:30 at Kod Keke #family remind 1h' })
      await waitFor("document.querySelectorAll('.qa-chip').length >= 4")
    },
  })
  await shot('month', `/month/${today.slice(0, 7)}`, { ready: "document.querySelector('.mv-item')" })
  await shot('year', `/year/${today.slice(0, 4)}`, { ready: "document.querySelector('.yv-day.h1, .yv-day.h2, .yv-day.h3, .yv-day.h4')" })
  await shot('agenda', '/agenda', { ready: "document.querySelector('.ag-item')" })
  await shot('people', `/people/${today}?with=${withIds}`, { ready: "document.querySelector('.pv-busy')" })
  await shot('settings', '/settings/general', { ready: "document.querySelector('.setting-row')" })
  await shot('settings-apps', '/settings/apps', { ready: "document.body.innerText.includes('Pocket Tasks')" })
  await shot('mobile-week', `/3day/${today}`, { mobile: true, ready: "document.querySelector('.ev-block')" })
  await shot('mobile-agenda', '/agenda', { mobile: true, ready: "document.querySelector('.ag-item')" })
  if (retro) await shot('mobile-event', `/event/${retro.id}`, { mobile: true, ready: "document.querySelector('.sheet .evpop-title')" })
  await shot('mobile-dark', `/month/${today.slice(0, 7)}`, { mobile: true, dark: true, ready: "document.querySelector('.mv-item')" })
} finally {
  if (targetId) await send('Target.closeTarget', { targetId }).catch(() => {})
  ws.close()
  mock.close()
}
