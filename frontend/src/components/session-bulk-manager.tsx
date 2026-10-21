import { useEffect, useRef, useState } from 'react'
import { Layers3, LoaderCircle, Pause, RefreshCw, RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { ApiClient, BulkAction, BulkJob, BulkSelection, Locale } from '@/lib/types'

interface Props {
  api: ApiClient
  locale: Locale
  selection: BulkSelection
  total: number
  disabled?: boolean
  onChanged: () => void
}
const selectClass = 'h-9 w-full rounded-md border bg-background px-2 text-xs'
const tagsOf = (value: string) => [...new Set(value.split(/[,，\n]/).map((tag) => tag.trim()).filter(Boolean))]
const signature = (selection: BulkSelection) => JSON.stringify({ q: selection.q || '', tag: selection.tag || '', starred: !!selection.starred, archived: selection.archived || 'exclude', view: selection.view || 'all', inactive_days: selection.inactive_days || 30, filters: Object.entries(selection.filters || {}).sort(([a], [b]) => a.localeCompare(b)) })
const statusOf = (error: unknown) => error && typeof error === 'object' && 'status' in error ? String(error.status) : 'network'

export function SessionBulkManager({ api, locale, selection, total, disabled, onChanged }: Props) {
  const zh = locale === 'zh'
  const [open, setOpen] = useState(false)
  const [archive, setArchive] = useState('keep')
  const [star, setStar] = useState('keep')
  const [add, setAdd] = useState('')
  const [remove, setRemove] = useState('')
  const [includeStarred, setIncludeStarred] = useState(false)
  const [includeEdited, setIncludeEdited] = useState(false)
  const [job, setJob] = useState<BulkJob>()
  const [jobs, setJobs] = useState<BulkJob[]>([])
  const [busy, setBusy] = useState<'preview' | 'load' | 'apply' | 'undo' | ''>('')
  const [error, setError] = useState('')
  const [jobsError, setJobsError] = useState('')
  const [mustRefresh, setMustRefresh] = useState(false)
  const [acknowledgeScope, setAcknowledgeScope] = useState(false)
  const stop = useRef(false)
  const generation = useRef(0)
  const changeCallback = useRef(onChanged)
  changeCallback.current = onChanged
  const sourceSignature = signature(selection)
  const staleScope = !!job && signature(job.selection) !== sourceSignature
  const processing = busy === 'apply' || busy === 'undo'
  const added = tagsOf(add)
  const removed = tagsOf(remove)
  const hasAction = archive !== 'keep' || star !== 'keep' || !!added.length || !!removed.length
  const invalidTags = [...added, ...removed].some((tag) => Array.from(tag).length > 48) || added.length > 24 || removed.length > 24 || added.some((tag) => removed.includes(tag))

  useEffect(() => { setAcknowledgeScope(false) }, [sourceSignature, job?.id])
  useEffect(() => () => { stop.current = true; generation.current++ }, [api])
  useEffect(() => {
    if (!open) return
    const abort = new AbortController()
    setJobsError('')
    void api<{ jobs: BulkJob[] }>('/session-bulk', { signal: abort.signal }).then((result) => {
      if (!abort.signal.aborted) setJobs(result.jobs || [])
    }).catch((cause: unknown) => { if (!abort.signal.aborted) setJobsError(statusOf(cause)) })
    return () => abort.abort()
  }, [api, open])

  function remember(value: BulkJob) {
    setJob(value)
    setJobs((current) => [value, ...current.filter((item) => item.id !== value.id)].slice(0, 20))
  }
  async function preview() {
    if (!hasAction || invalidTags || busy || disabled) return
    const current = ++generation.current
    setBusy('preview'); setError(''); setMustRefresh(false)
    const action: BulkAction = {
      ...(archive !== 'keep' ? { archived: archive === 'true' } : {}),
      ...(star !== 'keep' ? { starred: star === 'true' } : {}),
      ...(added.length ? { add_tags: added } : {}),
      ...(removed.length ? { remove_tags: removed } : {}),
      ...(archive === 'true' ? { include_starred: includeStarred, include_edited: includeEdited } : {}),
    }
    try {
      const result = await api<BulkJob>('/session-bulk-preview', { method: 'POST', body: JSON.stringify({ selection, action }) })
      if (current === generation.current) remember(result)
    } catch (cause) { if (current === generation.current) setError(statusOf(cause)) }
    finally { if (current === generation.current) setBusy('') }
  }
  async function refresh(id: string) {
    if (busy) return
    const current = ++generation.current
    setBusy('load'); setError('')
    try {
      const result = await api<BulkJob>(`/session-bulk?id=${encodeURIComponent(id)}`)
      if (current !== generation.current) return
      remember(result); setMustRefresh(false)
      changeCallback.current()
    } catch (cause) {
      if (current === generation.current) { setError(statusOf(cause)); setMustRefresh(true) }
    } finally { if (current === generation.current) setBusy('') }
  }
  async function run(mode: 'apply' | 'undo') {
    if (!job || busy || mustRefresh || (staleScope && !acknowledgeScope)) return
    const current = ++generation.current
    stop.current = false
    setBusy(mode); setError('')
    let latest = job
    try {
      do {
        const result = await api<BulkJob>(`/session-bulk-${mode}`, { method: 'POST', body: JSON.stringify({ id: latest.id, limit: 100 }) })
        if (current !== generation.current) return
        const noProgress = result.pending === latest.pending && result.applied === latest.applied && result.undone === latest.undone && result.undo_conflicts === latest.undo_conflicts && result.cancelled === latest.cancelled && result.status === latest.status
        latest = result
        remember(result)
        changeCallback.current()
        if ((mode === 'apply' && (result.status === 'complete' || result.pending === 0)) || (mode === 'undo' && result.status === 'undone')) break
        if (noProgress) { setMustRefresh(true); setError('no-progress'); break }
        // Yield between confirmed chunks so pause can prevent the next request.
        await new Promise<void>((resolve) => setTimeout(resolve, 30))
      } while (!stop.current)
    } catch (cause) {
      if (current === generation.current) {
        setError(statusOf(cause)); setMustRefresh(true)
        changeCallback.current()
      }
    } finally { if (current === generation.current) setBusy('') }
  }
  const counts = job ? [
    [zh ? '范围总数' : 'Total', job.total], [zh ? '待处理' : 'Pending', job.pending], [zh ? '已应用' : 'Applied', job.applied],
    [zh ? '受保护' : 'Protected', job.protected], [zh ? '无变化' : 'Unchanged', job.unchanged], [zh ? '版本冲突' : 'Conflicts', job.conflicts],
    [zh ? '已撤销' : 'Undone', job.undone], [zh ? '撤销冲突' : 'Undo conflicts', job.undo_conflicts], [zh ? '已取消' : 'Cancelled', job.cancelled],
  ] as const : []
  const statuses: Record<BulkJob['status'], string> = { preview: zh ? '预览，尚未执行' : 'Preview; not applied', running: zh ? '执行中／可继续' : 'Running / resumable', complete: zh ? '处理完成' : 'Complete', undoing: zh ? '撤销中／可继续' : 'Undoing / resumable', undone: zh ? '撤销结束' : 'Undo finished' }
  function scopeText(value: BulkSelection) {
    return [value.view, value.view === 'inactive' ? `${value.inactive_days} ${zh ? '天' : 'days'}` : '', `archived=${value.archived}`, value.starred ? (zh ? '仅收藏' : 'favorites only') : '', value.q ? `q=${value.q}` : '', value.tag ? `tag=${value.tag}` : '', ...Object.entries(value.filters || {}).map(([name, item]) => `${name}=${item}`)].filter(Boolean).join(' · ')
  }
  function actionText(value: BulkAction) {
    return [value.archived !== undefined ? (value.archived ? (zh ? '归档' : 'Archive') : (zh ? '恢复归档' : 'Restore')) : '', value.starred !== undefined ? (value.starred ? (zh ? '收藏' : 'Favorite') : (zh ? '取消收藏' : 'Unfavorite')) : '', value.add_tags?.length ? `+ ${value.add_tags.join(', ')}` : '', value.remove_tags?.length ? `− ${value.remove_tags.join(', ')}` : ''].filter(Boolean).join(' · ')
  }
  return <Dialog open={open} onOpenChange={(value) => { if (!value) stop.current = true; setOpen(value) }}>
    <DialogTrigger asChild><Button id="sessionBulkOpen" type="button" variant="outline" size="sm" className="gap-2 text-xs"><Layers3 className="size-3.5" />{zh ? '批量整理' : 'Bulk organize'}</Button></DialogTrigger>
    <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl" aria-describedby="bulk-description">
      <DialogHeader><DialogTitle>{zh ? '批量整理全部匹配会话' : 'Organize all matching sessions'}</DialogTitle><DialogDescription id="bulk-description">{zh ? '按当前已应用的筛选匹配全部页面。先冻结预览，再明确执行；标题和备注不会被覆盖。' : 'Match every page using the applied filters. Freeze a preview before applying. Titles and notes are preserved.'}</DialogDescription></DialogHeader>
      {!job ? <>
        <div className="rounded-lg border bg-muted/30 p-3 text-xs"><p className="font-medium">{zh ? `当前全部匹配：${total.toLocaleString()} 个会话（不限当前页）` : `All ${total.toLocaleString()} matches (across all pages)`}</p><p className="mt-2 break-all text-[10px] leading-5 text-muted-foreground">{scopeText(selection)}</p></div>
        <fieldset disabled={!!busy} className="space-y-4 disabled:opacity-60">
          <div className="grid gap-4 sm:grid-cols-2"><div className="space-y-2"><Label htmlFor="bulkArchive">{zh ? '归档操作' : 'Archive action'}</Label><select id="bulkArchive" className={selectClass} value={archive} onChange={(event) => setArchive(event.target.value)}><option value="keep">{zh ? '保持原状态' : 'Keep current state'}</option><option value="true">{zh ? '归档' : 'Archive'}</option><option value="false">{zh ? '恢复归档' : 'Restore archived'}</option></select></div><div className="space-y-2"><Label htmlFor="bulkStar">{zh ? '收藏操作' : 'Favorite action'}</Label><select id="bulkStar" className={selectClass} value={star} onChange={(event) => setStar(event.target.value)}><option value="keep">{zh ? '保持原状态' : 'Keep current state'}</option><option value="true">{zh ? '加入收藏' : 'Favorite'}</option><option value="false">{zh ? '取消收藏' : 'Unfavorite'}</option></select></div></div>
          <div className="grid gap-4 sm:grid-cols-2"><div className="space-y-2"><Label htmlFor="bulkAddTags">{zh ? '添加标签' : 'Add tags'}</Label><Input id="bulkAddTags" value={add} onChange={(event) => setAdd(event.target.value)} placeholder={zh ? '逗号分隔，保留现有标签' : 'Comma separated; retain existing tags'} className="text-xs" /></div><div className="space-y-2"><Label htmlFor="bulkRemoveTags">{zh ? '移除标签' : 'Remove tags'}</Label><Input id="bulkRemoveTags" value={remove} onChange={(event) => setRemove(event.target.value)} placeholder={zh ? '精确标签名称，逗号分隔' : 'Exact tag names, comma separated'} className="text-xs" /></div></div>
          {archive === 'true' && <div className="space-y-3 rounded-lg border p-3"><p className="text-xs leading-5 text-muted-foreground">{zh ? '归档默认跳过收藏及人工修改过的会话。只有勾选后才纳入。' : 'Archive protects favorites and manually edited sessions unless explicitly included.'}</p><label className="flex items-center gap-2 text-xs"><Checkbox id="bulkIncludeStarred" checked={includeStarred} onCheckedChange={(value) => setIncludeStarred(value === true)} />{zh ? '包含收藏会话' : 'Include favorites'}</label><label className="flex items-center gap-2 text-xs"><Checkbox id="bulkIncludeEdited" checked={includeEdited} onCheckedChange={(value) => setIncludeEdited(value === true)} />{zh ? '包含人工修改过的会话' : 'Include manually edited sessions'}</label></div>}
        </fieldset>
        {invalidTags && <p role="alert" className="text-xs text-destructive">{zh ? '每项最多 24 个标签、每个最多 48 字；同一标签不能同时添加和移除。' : 'Use up to 24 tags per action, each up to 48 characters. Do not add and remove the same tag.'}</p>}
        <Button id="sessionBulkPreview" disabled={!!busy || !hasAction || invalidTags || disabled || total === 0} onClick={() => void preview()}>{busy === 'preview' && <LoaderCircle className="size-3.5 animate-spin" />}{zh ? '生成预览（不修改会话）' : 'Create preview (no session changes)'}</Button>
      </> : <div className="space-y-4">
        <div className="rounded-lg border bg-muted/30 p-3"><div className="flex flex-wrap items-center justify-between gap-2"><p id="sessionBulkStatus" className="text-sm font-medium">{statuses[job.status]}</p><Button id="sessionBulkRefresh" size="sm" variant="outline" disabled={!!busy} onClick={() => void refresh(job.id)} className="gap-1.5 text-xs"><RefreshCw className="size-3" />{zh ? '刷新任务状态' : 'Refresh job status'}</Button></div><p className="mt-2 break-all text-[10px] text-muted-foreground">{job.id}</p><p id="sessionBulkFrozenScope" className="mt-2 break-all text-xs leading-5">{scopeText(job.selection)}</p><p className="mt-2 break-all text-xs font-medium">{actionText(job.action)}</p>{job.action.archived && <p className="mt-2 text-[10px] text-muted-foreground">{zh ? `包含收藏：${job.action.include_starred ? '是' : '否'}；包含人工修改：${job.action.include_edited ? '是' : '否'}` : `Include favorites: ${!!job.action.include_starred}; include manually edited: ${!!job.action.include_edited}`}</p>}</div>
        {staleScope && <div role="status" className="space-y-2 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-xs leading-5"><p>{zh ? '当前列表筛选已改变。此任务仍使用上方冻结的原范围。' : 'The library filters changed. This job retains its original frozen scope above.'}</p><label className="flex items-start gap-2"><Checkbox id="bulkAcknowledgeScope" checked={acknowledgeScope} disabled={!!busy} onCheckedChange={(value) => setAcknowledgeScope(value === true)} />{zh ? '确认继续处理此任务的原范围' : 'Continue with this job’s original scope'}</label></div>}
        <dl id="sessionBulkCounts" className="grid grid-cols-3 gap-2">{counts.map(([label, value]) => <div key={label} className="rounded-lg border p-2.5"><dt className="text-[10px] text-muted-foreground">{label}</dt><dd className="mt-1 text-lg font-semibold tabular-nums">{(value || 0).toLocaleString()}</dd></div>)}</dl>
        {!!job.items?.length && <div className="max-h-36 overflow-auto rounded-lg border p-3 text-[10px]"><p className="mb-2 font-medium">{zh ? '项目状态样本（最多 20 条）' : 'Item status sample (up to 20)'}</p>{job.items.map((item, index) => <p key={`${item.session_id}\0${index}`} className="mb-1 break-all font-mono">{item.session_id}: {item.status}{item.reason ? ` · ${item.reason}` : ''}</p>)}</div>}
        <p className="text-[11px] leading-5 text-muted-foreground">{zh ? '每次处理最多 100 条，可在批次间暂停。撤销会取消尚未处理的项，并只恢复执行后未被再次修改的会话；冲突项保留。' : 'Process up to 100 items per chunk and pause between chunks. Undo cancels pending items and restores applied items only if they have not changed again.'}</p>
        <div className="flex flex-wrap gap-2">{processing ? <Button id="sessionBulkPause" variant="outline" onClick={() => { stop.current = true }}><Pause className="size-3.5" />{zh ? '本批完成后暂停' : 'Pause after this chunk'}</Button> : <>
          {(job.status === 'preview' || job.status === 'running') && job.pending > 0 && <Button id="sessionBulkApply" disabled={!!busy || mustRefresh || (staleScope && !acknowledgeScope)} onClick={() => void run('apply')}>{zh ? (job.status === 'preview' ? '确认执行此预览' : '继续执行此任务') : (job.status === 'preview' ? 'Apply this preview' : 'Resume this job')}</Button>}
          {job.status !== 'undone' && <Button id="sessionBulkUndo" variant="outline" disabled={!!busy || mustRefresh || (staleScope && !acknowledgeScope)} onClick={() => void run('undo')}><RotateCcw className="size-3.5" />{zh ? (job.status === 'undoing' ? '继续撤销' : '撤销此批次／取消待处理') : (job.status === 'undoing' ? 'Resume undo' : 'Undo / cancel pending')}</Button>}
          <Button id="sessionBulkNew" variant="ghost" disabled={!!busy} onClick={() => { setJob(undefined); setError(''); setMustRefresh(false) }}>{zh ? '返回创建新预览' : 'Create another preview'}</Button>
        </>}</div>
      </div>}
      {error && <div id="sessionBulkError" role="alert" className="rounded-lg border border-destructive/30 p-3 text-xs leading-5 text-destructive">{mustRefresh ? (zh ? `处理结果未确认（${error}）。已停止后续请求。请先刷新任务状态，再明确继续或撤销。` : `Result unconfirmed (${error}). Further requests stopped. Refresh the job status before explicitly resuming or undoing.`) : (zh ? `请求失败（${error}），请核对后重试。` : `Request failed (${error}). Review and retry.`)}</div>}
      <div className="border-t pt-4"><p className="mb-2 text-xs font-medium">{zh ? '最近任务（刷新页面后可恢复）' : 'Recent jobs (recover after reload)'}</p>{jobsError && <p role="alert" className="mb-2 text-xs text-destructive">{zh ? `任务列表读取失败（${jobsError}）。关闭后重新打开可重试。` : `Job list unavailable (${jobsError}). Close and reopen to retry.`}</p>}<div className="max-h-36 space-y-1 overflow-auto">{jobs.map((item) => <Button key={item.id} variant="ghost" className="h-auto w-full min-w-0 justify-start py-2 text-left" disabled={!!busy} onClick={() => void refresh(item.id)}><span className="min-w-0"><span className="block truncate text-xs">{statuses[item.status]} · {item.total.toLocaleString()} · {actionText(item.action)}</span><span className="block truncate text-[10px] font-normal text-muted-foreground">{item.id} · {item.created_at}</span></span></Button>)}{!jobs.length && !jobsError && <p className="text-xs text-muted-foreground">{zh ? '尚无批量任务。' : 'No bulk jobs yet.'}</p>}</div></div>
    </DialogContent>
  </Dialog>
}
