import { useEffect, useMemo, useRef, useState } from 'react'
import { Archive, ArrowDownUp, ArrowRight, ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight, Filter, FolderGit2, MessageSquareText, RefreshCw, Search, SlidersHorizontal, Star, Tag, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { Skeleton } from '@/components/ui/skeleton'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import type { ApiClient, BulkSelection, FacetCount, LibraryView, Locale, ManagedSession, ManagedSessionPage, ProjectFilter, SessionSummary } from '@/lib/types'
import { displayText } from '@/lib/utils'
import { SessionBulkManager } from './session-bulk-manager'

interface SessionLibraryProps {
  api: ApiClient
  locale: Locale
  project?: ProjectFilter
  view: LibraryView
  facets?: FacetCount[]
  onSelect: (session: ManagedSession) => void
  selectedId?: string
  refreshKey?: number
  identityName?: (session: SessionSummary) => string
  onBulkChanged: () => void
}

interface Filters {
  query: string
  tag: string
  starred: boolean
  archived: 'exclude' | 'only' | 'all' | ''
  facets: Record<string, string>
}
const blankFilters = (): Filters => ({ query: '', tag: '', starred: false, archived: '', facets: {} })
const selectClass = 'h-9 w-full min-w-0 rounded-md border border-input bg-background px-3 text-xs shadow-xs outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50'

export function SessionLibrary({ api, locale, project, view, facets = [], onSelect, selectedId, refreshKey, identityName, onBulkChanged }: SessionLibraryProps) {
  const zh = locale === 'zh'
  const [draft, setDraft] = useState<Filters>(blankFilters)
  const [filters, setFilters] = useState<Filters>(blankFilters)
  const [offset, setOffset] = useState(0)
  const [limit, setLimit] = useState(100)
  const [page, setPage] = useState<ManagedSessionPage | null>(null)
  const [busy, setBusy] = useState(true)
  const [error, setError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)
  const [filterOpen, setFilterOpen] = useState(false)
  const [facetSearch, setFacetSearch] = useState('')
  const [inactiveDays, setInactiveDays] = useState(30)
  const sequence = useRef(0)
  const baseArchive = view === 'archived' ? 'only' : 'exclude'
  const archive = filters.archived || baseArchive
  const [previousScope, setPreviousScope] = useState({ view, name: project?.name, value: project?.value })
  if (previousScope.view !== view || previousScope.name !== project?.name || previousScope.value !== project?.value) {
    setPreviousScope({ view, name: project?.name, value: project?.value })
    setOffset(0)
    if (previousScope.view !== view) {
      setFilters((value) => ({ ...value, archived: '' }))
      setDraft((value) => ({ ...value, archived: '' }))
    }
  }
  const selection = useMemo<BulkSelection>(() => ({
    archived: archive,
    view: view === 'starred' || view === 'archived' ? 'all' : view,
    inactive_days: inactiveDays,
    ...(filters.query.trim() ? { q: filters.query.trim() } : {}),
    ...(filters.tag.trim() ? { tag: filters.tag.trim() } : {}),
    ...(filters.starred || view === 'starred' ? { starred: true } : {}),
    filters: { ...Object.fromEntries(Object.entries(filters.facets).filter(([, value]) => value)), ...(project ? { [project.name]: project.value } : {}) },
  }), [archive, filters, project, view, inactiveDays])
  const query = useMemo(() => {
    const params = new URLSearchParams({ limit: String(limit), offset: String(offset), archived: selection.archived, view: selection.view, inactive_days: String(selection.inactive_days) })
    Object.entries(selection.filters).forEach(([name, value]) => params.set(name, value))
    if (selection.q) params.set('q', selection.q)
    if (selection.tag) params.set('tag', selection.tag)
    if (selection.starred) params.set('starred', 'true')
    return params.toString()
  }, [limit, offset, selection])

  useEffect(() => {
    const current = ++sequence.current
    setBusy(true)
    setError('')
    void api<ManagedSessionPage>(`/session-browser?${query}`).then((result) => {
      if (current !== sequence.current) return
      if (result.total > 0 && !result.sessions?.length && offset > 0) {
        setOffset(Math.floor((result.total - 1) / result.limit) * result.limit)
        return
      }
      setPage(result)
    }).catch((cause: unknown) => {
      if (current !== sequence.current) return
      const status = cause && typeof cause === 'object' && 'status' in cause ? String(cause.status) : 'network'
      setError(status)
    }).finally(() => { if (current === sequence.current) setBusy(false) })
    return () => { sequence.current++ }
  }, [api, query, refreshKey, reloadKey, offset])

  const groups = useMemo(() => {
    const result = new Map<string, FacetCount[]>()
    for (const facet of facets) result.set(facet.name, [...(result.get(facet.name) || []), facet])
    return [...result.entries()].sort(([a], [b]) => a.localeCompare(b))
  }, [facets])
  const activeFacets = Object.entries(filters.facets).filter(([name, value]) => value && name !== project?.name)
  const activeCount = activeFacets.length + Number(!!project) + Number(!!filters.query.trim()) + Number(!!filters.tag.trim()) + Number(filters.starred || view === 'starred') + Number(archive !== 'exclude')
  const rows = page?.sessions || []
  const total = page?.total || 0
  const currentPage = Math.floor((page?.offset || 0) / (page?.limit || limit)) + 1
  const pages = Math.max(1, Math.ceil(total / (page?.limit || limit)))
  const titles: Record<LibraryView, string> = { all: zh ? '全部会话' : 'All sessions', starred: zh ? '我的收藏' : 'Favorites', archived: zh ? '已归档' : 'Archived sessions', recent: zh ? '最近 7 天' : 'Last 7 days', inactive: zh ? '长期未活跃' : 'Inactive sessions', main: zh ? '主会话' : 'Main sessions', subagent: zh ? '子代理会话' : 'Subagent sessions', system: zh ? '系统任务' : 'System tasks', prewarm: zh ? '预热会话' : 'Prewarm sessions', unassigned: zh ? '待归类' : 'Unresolved project' }
  const title = titles[view]
  const formatTime = (value: string) => {
    const date = new Date(value)
    return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(zh ? 'zh-CN' : 'en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(date)
  }
  function apply() { setFilters({ ...draft, facets: { ...draft.facets } }); setOffset(0); setFilterOpen(false) }
  function reset() { setDraft(blankFilters()); setFilters(blankFilters()); setOffset(0); setFacetSearch('') }
  function removeFacet(name: string) {
    setFilters((value) => ({ ...value, facets: { ...value.facets, [name]: '' } }))
    setDraft((value) => ({ ...value, facets: { ...value.facets, [name]: '' } }))
    setOffset(0)
  }

  return <section className="session-library flex h-full min-h-0 flex-col" aria-label={zh ? '会话库' : 'Session library'}>
    <div className="border-b px-5 pb-5 pt-6 sm:px-8">
      <div className="mb-5 flex items-start justify-between gap-3"><div><div className="mb-2 flex items-center gap-1.5 text-[11px] text-muted-foreground"><FolderGit2 className="size-3" /><span className="max-w-[320px] truncate" title={project?.value}>{project ? `${project.name}: ${project.value}` : (zh ? '工作区' : 'Workspace')}</span><span className="px-1 text-border">/</span><span>{zh ? '会话' : 'Sessions'}</span></div><div className="flex items-center gap-3"><h1 className="text-2xl font-semibold tracking-tight">{title}</h1>{page && <Badge variant="secondary" className="rounded-md px-1.5 text-[10px] font-medium tabular-nums">{total.toLocaleString()}</Badge>}</div><p className="mt-2 text-xs leading-5 text-muted-foreground">{view === 'unassigned' ? (zh ? '项目来源缺失或存在冲突，需要进一步确认归属。' : 'Project identity is missing or conflicting and needs review.') : view === 'inactive' ? (zh ? '按最近活动时间分组；未活跃不代表任务已完成。' : 'Grouped by last activity. Inactivity does not mean the task is complete.') : (zh ? '回看每一次对话，把有价值的探索留在手边。' : 'Revisit your conversations. Keep valuable work within reach.')}</p></div><Button variant="outline" size="sm" disabled={busy} onClick={() => setReloadKey((value) => value + 1)} className="mt-1 gap-2 bg-background text-xs"><RefreshCw className={`size-3.5 ${busy ? 'animate-spin' : ''}`} /><span className="hidden sm:inline">{zh ? '刷新' : 'Refresh'}</span></Button></div>
      {view === 'inactive' && <div className="mb-4 flex items-center gap-2 text-xs"><label htmlFor="inactiveDays">{zh ? '至少未活跃天数' : 'Days without activity'}</label><select id="inactiveDays" className="h-8 rounded-md border bg-background px-2 text-xs" value={inactiveDays} onChange={(event) => { setInactiveDays(Number(event.target.value)); setOffset(0) }}>{[7, 30, 60, 90, 180, 365].map((days) => <option key={days} value={days}>{days} {zh ? '天' : 'days'}</option>)}</select></div>}
      <form onSubmit={(event) => { event.preventDefault(); apply() }} className="flex flex-wrap gap-2">
        <div className="relative min-w-40 flex-1"><Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" /><Input id="sessionSearch" aria-label={zh ? '搜索会话' : 'Search sessions'} value={draft.query} onChange={(event) => setDraft({ ...draft, query: event.target.value })} placeholder={zh ? '搜索标题、摘要、备注或会话 ID…' : 'Search titles, summaries, notes or session IDs…'} className="h-10 bg-background pl-9 text-xs" /></div>
        <Button type="submit" className="h-10 px-4 text-xs">{zh ? '搜索' : 'Search'}</Button>
        <Dialog open={filterOpen} onOpenChange={setFilterOpen}><DialogTrigger asChild><Button type="button" variant="outline" className="h-10 gap-2 text-xs"><SlidersHorizontal className="size-3.5" />{zh ? '筛选' : 'Filters'}<span id="activeCount" className="rounded bg-muted px-1.5 text-[10px] tabular-nums">{activeCount}</span></Button></DialogTrigger><DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl"><DialogHeader><DialogTitle>{zh ? '筛选会话' : 'Filter sessions'}</DialogTitle><DialogDescription>{zh ? '搜索标签与精确分面共同生效。正文检索可使用独立的记忆搜索。' : 'Combine label search with exact facets. Full conversation retrieval is available through memory search.'}</DialogDescription></DialogHeader>
          <div id="filterSidebar" className="space-y-5 pt-2">
            <div className="grid gap-4 sm:grid-cols-2"><div className="space-y-2"><label htmlFor="sessionTagFilter" className="text-xs font-medium">{zh ? '精确标签' : 'Exact tag'}</label><Input id="sessionTagFilter" value={draft.tag} onChange={(event) => setDraft({ ...draft, tag: event.target.value })} placeholder={zh ? '完整标签名称' : 'Full tag name'} className="h-9 text-xs" /></div><div className="space-y-2"><label htmlFor="sessionArchiveFilter" className="text-xs font-medium">{zh ? '归档状态' : 'Archive visibility'}</label><select id="sessionArchiveFilter" className={selectClass} value={draft.archived || baseArchive} onChange={(event) => setDraft({ ...draft, archived: event.target.value as Filters['archived'] })}><option value="exclude">{zh ? '未归档' : 'Not archived'}</option><option value="only">{zh ? '仅已归档' : 'Archived only'}</option><option value="all">{zh ? '包含已归档' : 'Include archived'}</option></select></div></div>
            <label className="flex items-center gap-2 text-xs"><Checkbox id="sessionStarredFilter" checked={draft.starred || view === 'starred'} disabled={view === 'starred'} onCheckedChange={(checked) => setDraft({ ...draft, starred: checked === true })} /><Star className="size-3.5 text-amber-500" />{zh ? '仅收藏' : 'Favorites only'}</label>
            <div className="border-t pt-4"><label htmlFor="facetSearch" className="mb-2 block text-xs font-medium">{zh ? '项目、客户端与其他分面' : 'Projects, clients and other facets'}</label><Input id="facetSearch" value={facetSearch} onChange={(event) => setFacetSearch(event.target.value)} placeholder={zh ? '查找分面名称…' : 'Find a facet…'} className="mb-4 h-9 text-xs" /><div className="grid gap-4 sm:grid-cols-2">{groups.filter(([name]) => name.toLowerCase().includes(facetSearch.trim().toLowerCase())).map(([name, values]) => <div key={name} className="min-w-0 space-y-2"><label htmlFor={`facet-${name}`} className="block truncate font-mono text-[10px] text-muted-foreground" title={name}>{name}</label><select id={`facet-${name}`} data-facet={name} value={name === project?.name ? project.value : (draft.facets[name] || '')} disabled={name === project?.name} onChange={(event) => setDraft({ ...draft, facets: { ...draft.facets, [name]: event.target.value } })} className={selectClass}><option value="">{zh ? '全部' : 'All values'}</option>{values.map((item) => <option key={item.value} value={item.value}>{item.value} · {item.sessions}</option>)}</select></div>)}</div>{!groups.length && <p className="text-xs text-muted-foreground">{zh ? '暂时没有可用分面。' : 'No facets available yet.'}</p>}</div>
            <div className="flex items-center justify-between gap-3 border-t pt-4"><Button type="button" variant="ghost" size="sm" onClick={reset} className="text-xs">{zh ? '重置筛选' : 'Reset filters'}</Button><Button type="button" size="sm" onClick={apply} className="gap-2 text-xs"><Filter className="size-3" />{zh ? '应用筛选' : 'Apply filters'}</Button></div>
          </div>
        </DialogContent></Dialog>
      </form>
      {activeCount > 0 && <div id="activeFilters" className="mt-3 flex flex-wrap items-center gap-1.5">{project && <Badge variant="secondary" className="max-w-64 gap-1 text-[10px]"><FolderGit2 className="size-2.5 shrink-0" /><span className="truncate">{project.value}</span></Badge>}{filters.query.trim() && <Badge variant="secondary" className="max-w-64 gap-1 text-[10px]"><Search className="size-2.5 shrink-0" /><span className="truncate">{filters.query}</span></Badge>}{filters.tag.trim() && <Badge variant="secondary" className="max-w-64 gap-1 text-[10px]"><Tag className="size-2.5 shrink-0" /><span className="truncate">{filters.tag}</span></Badge>}{(filters.starred || view === 'starred') && <Badge variant="secondary" className="gap-1 text-[10px]"><Star className="size-2.5" />{zh ? '收藏' : 'Favorites'}</Badge>}{archive !== 'exclude' && <Badge variant="secondary" className="gap-1 text-[10px]"><Archive className="size-2.5" />{archive === 'only' ? (zh ? '已归档' : 'Archived') : (zh ? '含归档' : 'Including archived')}</Badge>}{activeFacets.map(([name, value]) => <Badge key={name} variant="secondary" className="max-w-72 gap-1 text-[10px]"><span className="truncate">{name}: {value}</span><button type="button" onClick={() => removeFacet(name)} aria-label={`${zh ? '移除筛选' : 'Remove filter'} ${name}`} className="rounded-sm focus-visible:outline focus-visible:outline-2"><X className="size-3" /></button></Badge>)}</div>}
    </div>
    <div className="flex items-center justify-between border-b bg-muted/20 px-5 py-3 text-[11px] text-muted-foreground sm:px-8"><span id="resultCount">{zh ? `${total.toLocaleString()} 个会话` : `${total.toLocaleString()} sessions`}</span><div className="flex items-center gap-3"><SessionBulkManager api={api} locale={locale} selection={selection} total={total} disabled={busy || !!error} onChanged={onBulkChanged} /><span className="hidden items-center gap-1.5 sm:flex"><ArrowDownUp className="size-3" />{zh ? '收藏优先 · 最近活动' : 'Favorites first · Recent activity'}</span></div></div>
    <div className="min-h-0 flex-1 overflow-auto">
      {error ? <div id="sessionListStatus" role="alert" className="mx-5 mt-5 rounded-xl border border-destructive/20 bg-destructive/5 p-5 text-sm"><p className="text-destructive">{zh ? `会话列表读取失败（${error}）。` : `Unable to load sessions (${error}).`}</p><Button variant="outline" size="sm" onClick={() => setReloadKey((value) => value + 1)} className="mt-3 gap-2 text-xs"><RefreshCw className="size-3" />{zh ? '重试' : 'Retry'}</Button></div> : busy && !page ? <div className="space-y-0 px-5 sm:px-8">{Array.from({ length: 8 }, (_, index) => <div key={index} className="flex gap-4 border-b py-5"><Skeleton className="size-9 rounded-xl" /><div className="flex-1 space-y-2"><Skeleton className="h-3 w-2/3" /><Skeleton className="h-2.5 w-1/3" /></div><Skeleton className="h-3 w-16" /></div>)}</div> : !rows.length ? <div className="flex min-h-80 flex-col items-center justify-center px-6 text-center"><div className="mb-5 flex size-14 items-center justify-center rounded-2xl border bg-muted/50"><MessageSquareText className="size-6 text-muted-foreground" /></div><h2 className="text-sm font-medium">{zh ? '这里还没有会话' : 'No sessions here yet'}</h2><p className="mt-2 max-w-sm text-xs leading-6 text-muted-foreground">{activeCount ? (zh ? '没有符合当前筛选的会话，试试调整搜索条件。' : 'No sessions match these filters. Try adjusting your search.') : (zh ? '采集到的 AI 会话会出现在这里。' : 'Captured AI conversations will appear here.')}</p>{activeCount > 0 && <Button variant="outline" size="sm" onClick={reset} className="mt-4 text-xs">{zh ? '清除搜索与筛选' : 'Clear search and filters'}</Button>}</div> : <table className={`w-full table-fixed text-left transition-opacity ${busy ? 'opacity-50' : ''}`} aria-busy={busy}><thead className="sticky top-0 z-10 bg-background/95 text-[10px] font-medium text-muted-foreground backdrop-blur"><tr className="border-b"><th scope="col" className="w-auto px-5 py-3 font-medium sm:px-8">{zh ? '会话' : 'Conversation'}</th><th scope="col" className="hidden w-36 px-3 py-3 font-medium xl:table-cell">{zh ? '来源 / 模型' : 'Source / model'}</th><th scope="col" className="hidden w-20 px-3 py-3 text-right font-medium md:table-cell">{zh ? '请求' : 'Requests'}</th><th scope="col" className="w-32 px-5 py-3 text-right font-medium sm:w-40 sm:px-8">{zh ? '最近活动' : 'Last activity'}</th></tr></thead><tbody id="rows">{rows.map((row) => {
        const metadata = row.management
        const displayTitle = metadata?.title || displayText(row.summary || row.project || (zh ? '未命名会话' : 'Untitled session'))
        const identity = identityName?.(row) || row.principal_alias || row.principal_id || row.key_id
        return <tr key={row.session_id} tabIndex={0} role="link" aria-label={displayTitle} aria-current={selectedId === row.session_id ? 'true' : undefined} data-session-id={row.session_id} onClick={() => onSelect(row)} onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); onSelect(row) } }} className={`group cursor-pointer border-b border-border/70 outline-none transition-colors hover:bg-muted/45 focus-visible:bg-accent focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring ${selectedId === row.session_id ? 'bg-accent/70' : ''}`}><td className="min-w-0 px-5 py-4 sm:px-8"><div className="flex items-start gap-3"><div className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-xl border border-border/70 bg-muted/30 text-muted-foreground">{metadata?.starred ? <Star className="size-4 fill-amber-400/25 text-amber-600" /> : metadata?.archived ? <Archive className="size-4" /> : <MessageSquareText className="size-4" />}</div><div className="min-w-0 flex-1"><div className="conversation-title truncate text-[13px] font-medium leading-5" title={displayTitle}>{displayTitle}</div><div className="conversation-meta mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-muted-foreground"><span className="max-w-48 truncate" title={row.project}>{row.project || (zh ? '待归类项目' : 'Unresolved project')}</span>{identity && <><span className="text-border">·</span><span className="key-pill max-w-40 truncate" title={identity}>{identity}</span></>}<span className="hidden max-w-52 truncate font-mono text-[9px] 2xl:inline" title={row.session_id}>{row.session_id}</span></div>{!!metadata?.tags?.length && <div className="session-tags mt-2 flex flex-wrap gap-1">{metadata.tags.map((tag) => <Badge key={tag} variant="outline" className="max-w-40 rounded px-1.5 py-0 text-[9px] font-normal text-muted-foreground"><span className="truncate">{tag}</span></Badge>)}</div>}</div></div></td><td className="hidden px-3 py-4 xl:table-cell"><p className="truncate text-[10px] text-muted-foreground" title={row.model}>{row.model || '—'}</p><div className="mt-1 flex flex-wrap gap-1">{row.thread_sources?.includes('system') && <Badge variant="secondary" className="text-[9px]">{zh ? '系统任务' : 'System task'}</Badge>}{row.kinds?.length === 1 && row.kinds[0] === 'prewarm' && <Badge variant="secondary" className="text-[9px]">{zh ? '预热' : 'Prewarm'}</Badge>}</div></td><td className="hidden px-3 py-4 text-right text-xs tabular-nums text-muted-foreground md:table-cell">{row.requests.toLocaleString()}</td><td className="px-5 py-4 text-right sm:px-8"><div className="text-[10px] tabular-nums text-muted-foreground" title={row.last_at}>{formatTime(row.last_at)}</div><ArrowRight className="ml-auto mt-2 size-3 text-muted-foreground/50 transition-transform group-hover:translate-x-0.5" /></td></tr>
      })}</tbody></table>}
    </div>
    <div className="flex flex-wrap items-center justify-between gap-3 border-t bg-background px-5 py-3 sm:px-8"><div className="flex items-center gap-2 text-[10px] text-muted-foreground"><label htmlFor="limit">{zh ? '每页' : 'Per page'}</label><select id="limit" className="h-7 rounded-md border bg-background px-1.5 text-[10px]" value={limit} onChange={(event) => { setLimit(Number(event.target.value)); setOffset(0) }}><option value="25">25</option><option value="50">50</option><option value="100">100</option><option value="200">200</option><option value="1000">1000</option></select><span id="sessionListInfo" className="ml-1 tabular-nums">{zh ? `第 ${currentPage} / ${pages} 页 · 共 ${total} 条` : `Page ${currentPage} / ${pages} · ${total} total`}</span></div><div className="flex items-center gap-1"><Button id="sessionListFirst" variant="ghost" size="icon" className="size-7" disabled={busy || currentPage <= 1} aria-label={zh ? '第一页' : 'First page'} onClick={() => setOffset(0)}><ChevronsLeft className="size-3.5" /></Button><Button id="sessionListPrevious" variant="ghost" size="icon" className="size-7" disabled={busy || currentPage <= 1} aria-label={zh ? '上一页' : 'Previous page'} onClick={() => setOffset(Math.max(0, offset - limit))}><ChevronLeft className="size-3.5" /></Button><span className="min-w-7 text-center text-[10px] tabular-nums">{currentPage}</span><Button id="sessionListNext" variant="ghost" size="icon" className="size-7" disabled={busy || currentPage >= pages} aria-label={zh ? '下一页' : 'Next page'} onClick={() => setOffset(offset + limit)}><ChevronRight className="size-3.5" /></Button><Button id="sessionListLast" variant="ghost" size="icon" className="size-7" disabled={busy || currentPage >= pages} aria-label={zh ? '最后一页' : 'Last page'} onClick={() => setOffset((pages - 1) * limit)}><ChevronsRight className="size-3.5" /></Button></div></div>
  </section>
}
