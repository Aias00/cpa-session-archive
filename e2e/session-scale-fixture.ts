import type { Page, Route } from '@playwright/test'
import type { BulkAction, BulkJob, BulkSelection, ManagedSession, SessionManagement } from '../frontend/src/lib/types.js'

type FixtureSession = ManagedSession & { exactFacets: Record<string, string>; manuallyEdited?: boolean }
type PlanItem = { id: string; before: SessionManagement; afterRevision?: number; status: 'pending' | 'protected' | 'unchanged' | 'applied' | 'conflict' | 'undone' | 'undo-conflict' | 'cancelled' }
type Plan = { job: BulkJob; items: PlanItem[] }

export function scaleSession(index: number, overrides: Partial<FixtureSession> = {}): FixtureSession {
  const id = `scale-session-${index}`
  return {
    session_id: id, summary: `Captured session ${index}`, project: '/work/one/project', model: 'fixture-model', requests: 1,
    last_at: new Date(Date.now() - (index % 2 ? 90 : 1) * 86400000).toISOString(), thread_sources: ['user'], kinds: ['turn'],
    exactFacets: { 'project.path': '/work/one/project', 'project.id': 'project-one', client: 'codex' },
    management: { session_id: id, title: '', note: '', tags: ['existing'], starred: false, archived: false, revision: 0, updated_at: '' },
    ...overrides,
  }
}

export async function installScaleFixture(page: Page, initialSessions: FixtureSession[]) {
  const sessions = structuredClone(initialSessions)
  const plans = new Map<string, Plan>()
  const queries: URLSearchParams[] = []
  const requests: { path: string; body: unknown }[] = []
  let nextID = 0
  let ambiguousNext = false
  let heldChunk: { reached: Promise<void>; mark: () => void; gate: Promise<void>; release: () => void } | undefined
  const changed = (metadata: SessionManagement, action: BulkAction) => {
    const tags = [...new Set([...metadata.tags.filter((tag) => !action.remove_tags?.includes(tag)), ...(action.add_tags || [])])]
    return { ...metadata, tags, ...(action.archived !== undefined ? { archived: action.archived } : {}), ...(action.starred !== undefined ? { starred: action.starred } : {}) }
  }
  const matches = (session: FixtureSession, selection: BulkSelection) => {
    const metadata = session.management
    const age = (Date.now() - new Date(session.last_at).getTime()) / 86400000
    const view = selection.view || 'all'
    const relevant = view === 'all' || (view === 'recent' && age <= 7) || (view === 'inactive' && age >= (selection.inactive_days || 30)) || (view === 'main' && session.thread_sources?.includes('user')) || (view === 'subagent' && session.thread_sources?.includes('subagent')) || (view === 'system' && session.thread_sources?.includes('system')) || (view === 'prewarm' && session.kinds?.every((kind) => kind === 'prewarm')) || (view === 'unassigned' && !session.project)
    return relevant && (selection.archived === 'all' || metadata.archived === (selection.archived === 'only')) && (!selection.starred || metadata.starred) && (!selection.tag || metadata.tags.includes(selection.tag)) && Object.entries(selection.filters || {}).every(([name, value]) => session.exactFacets[name] === value) && (!selection.q || [session.session_id, session.summary || '', session.project || '', metadata.title, metadata.note, ...metadata.tags].some((value) => value.toLowerCase().includes(selection.q!.toLowerCase())))
  }
  const summarize = (plan: Plan) => {
    const count = (status: PlanItem['status']) => plan.items.filter((item) => item.status === status).length
    Object.assign(plan.job, { pending: count('pending'), applied: count('applied') + count('undone') + count('undo-conflict'), protected: count('protected'), unchanged: count('unchanged'), conflicts: count('conflict'), undone: count('undone'), undo_conflicts: count('undo-conflict'), cancelled: count('cancelled'), updated_at: new Date().toISOString(), items: plan.items.filter((item) => ['protected', 'conflict', 'undo-conflict'].includes(item.status)).slice(0, 20).map((item) => ({ session_id: item.id, status: item.status, reason: item.status === 'protected' ? 'favorite or manual edit protected' : 'revision changed' })) })
    return plan.job
  }
  await page.route(/\/cpa-session-archive\/(session-browser|session-metadata|session-bulk(?:-preview|-apply|-undo)?|facets)(?:\?|$)/, async (route: Route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.split('/').pop()!
    const reply = (value: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) })
    if (path === 'facets') {
      const facets = new Map<string, { name: string; value: string; sessions: number }>()
      for (const session of sessions) for (const [name, value] of Object.entries(session.exactFacets)) {
        const key = `${name}\0${value}`
        const entry = facets.get(key) || { name, value, sessions: 0 }
        entry.sessions++; facets.set(key, entry)
      }
      return reply([...facets.values()])
    }
    if (path === 'session-browser') {
      const query = url.searchParams
      queries.push(new URLSearchParams(query))
      const limit = Number(query.get('limit') || 100), offset = Number(query.get('offset') || 0)
      const filters = Object.fromEntries([...query.entries()].filter(([name]) => !['q', 'tag', 'archived', 'starred', 'view', 'inactive_days', 'limit', 'offset'].includes(name)))
      const selection = { q: query.get('q') || '', tag: query.get('tag') || '', archived: query.get('archived') || 'exclude', starred: query.get('starred') === 'true', filters, view: query.get('view') || 'all', inactive_days: Number(query.get('inactive_days') || 30) } as BulkSelection
      const result = sessions.filter((session) => matches(session, selection))
      return reply({ sessions: result.slice(offset, offset + limit), total: result.length, limit, offset })
    }
    if (path === 'session-metadata') {
      const session = sessions.find((item) => item.session_id === url.searchParams.get('session_id'))
      if (!session) return reply({ error: 'not found' }, 404)
      return reply(session.management)
    }
    if (request.method() === 'GET') {
      const id = url.searchParams.get('id')
      if (!id) return reply({ jobs: [...plans.values()].map((plan) => summarize(plan)).reverse().slice(0, 20) })
      const plan = plans.get(id)
      return plan ? reply(summarize(plan)) : reply({ error: 'not found' }, 404)
    }
    const body = request.postDataJSON()
    requests.push({ path, body })
    if (path === 'session-bulk-preview') {
      const { action } = body as { selection: BulkSelection; action: BulkAction }
      // Match Go's stored SessionBrowseOptions shape, including default fields.
      const selection = { limit: 0, offset: 0, q: '', tag: '', starred: false, ...body.selection, filters: Object.keys(body.selection.filters || {}).length ? body.selection.filters : null } as BulkSelection
      const matched = sessions.filter((session) => matches(session, selection))
      const id = `fixture-job-${++nextID}`
      const plan: Plan = {
        job: { id, status: 'preview', selection, action, total: matched.length, pending: 0, applied: 0, conflicts: 0, protected: 0, unchanged: 0, undone: 0, undo_conflicts: 0, cancelled: 0, created_at: new Date().toISOString(), updated_at: '' },
        items: matched.map((session) => {
          const protectedItem = action.archived === true && ((!action.include_starred && session.management.starred) || (!action.include_edited && session.manuallyEdited))
          const before = structuredClone(session.management)
          const same = JSON.stringify(changed(before, action)) === JSON.stringify(before)
          return { id: session.session_id, before, status: protectedItem ? 'protected' : same ? 'unchanged' : 'pending' }
        }),
      }
      plans.set(id, plan)
      return reply(summarize(plan))
    }
    const plan = plans.get(body.id)
    if (!plan) return reply({ error: 'not found' }, 404)
    const hold = heldChunk; heldChunk = undefined
    if (hold) { hold.mark(); await hold.gate }
    if (path === 'session-bulk-apply') {
      plan.job.status = 'running'
      for (const item of plan.items.filter((item) => item.status === 'pending').slice(0, body.limit || 100)) {
        const session = sessions.find((value) => value.session_id === item.id)!
        if (session.management.revision !== item.before.revision) { item.status = 'conflict'; continue }
        session.management = { ...changed(session.management, plan.job.action), revision: session.management.revision + 1, updated_at: new Date().toISOString() }
        item.afterRevision = session.management.revision
        item.status = 'applied'
      }
      if (!plan.items.some((item) => item.status === 'pending')) plan.job.status = 'complete'
    } else {
      plan.job.status = 'undoing'
      for (const item of plan.items) if (item.status === 'pending') item.status = 'cancelled'
      for (const item of plan.items.filter((item) => item.status === 'applied').slice(0, body.limit || 100)) {
        const session = sessions.find((value) => value.session_id === item.id)!
        if (session.management.revision !== item.afterRevision) { item.status = 'undo-conflict'; continue }
        session.management = { ...structuredClone(item.before), revision: session.management.revision + 1, updated_at: new Date().toISOString() }
        item.status = 'undone'
      }
      if (!plan.items.some((item) => item.status === 'applied')) plan.job.status = 'undone'
    }
    const result = summarize(plan)
    if (ambiguousNext) { ambiguousNext = false; return route.abort('failed') }
    return reply(result)
  })
  await page.addInitScript(() => { localStorage.setItem('cli-proxy-auth', JSON.stringify({ state: { apiBase: location.origin, managementKey: 'synthetic-bulk-key' } })) })
  await page.goto('/#/sessions')
  return {
    sessions, plans, queries, requests,
    ambiguousNext: () => { ambiguousNext = true },
    holdNextChunk: () => {
      let mark!: () => void, release!: () => void
      const reached = new Promise<void>((resolve) => { mark = resolve }), gate = new Promise<void>((resolve) => { release = resolve })
      heldChunk = { reached, gate, mark, release }
      return { reached, release }
    },
  }
}
