import { useEffect, useRef, useState } from 'react'
import { Archive, Check, LoaderCircle, RotateCcw, Save, Star, Tag, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Checkbox } from '@/components/ui/checkbox'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import type { ApiClient, Locale, ManagedSession, SessionManagement } from '@/lib/types'

interface SessionEditorProps {
  api: ApiClient
  locale: Locale
  session: ManagedSession
  onSaved: (management: SessionManagement) => void
  onDirtyChange?: (dirty: boolean) => void
}

type Draft = Pick<SessionManagement, 'title' | 'note' | 'starred' | 'archived'> & { tags: string }
const draftOf = (value: SessionManagement): Draft => ({ title: value.title || '', note: value.note || '', tags: (value.tags || []).join('\n'), starred: !!value.starred, archived: !!value.archived })
const statusOf = (error: unknown): number | undefined => error && typeof error === 'object' && 'status' in error ? Number(error.status) : undefined

export function SessionEditor({ api, locale, session, onSaved, onDirtyChange }: SessionEditorProps) {
  const zh = locale === 'zh'
  const [metadata, setMetadata] = useState<SessionManagement | null>(null)
  const [draft, setDraft] = useState<Draft>({ title: '', note: '', tags: '', starred: false, archived: false })
  const [busy, setBusy] = useState(true)
  const [needsReload, setNeedsReload] = useState(false)
  const [status, setStatus] = useState<'saved' | 'conflict' | 'failed' | 'load-failed' | ''>('')
  const [errorStatus, setErrorStatus] = useState<number>()
  const sequence = useRef(0)
  const dirty = !!metadata && JSON.stringify(draft) !== JSON.stringify(draftOf(metadata))
  const draftTags = [...new Set(draft.tags.split(/[,，\n]/).map((tag) => tag.trim()).filter(Boolean))]
  const validation = Array.from(draft.title).length > 200
    ? (zh ? '标题最多 200 字。' : 'Titles can contain up to 200 characters.')
    : Array.from(draft.note).length > 8000
      ? (zh ? '备注最多 8,000 字。' : 'Notes can contain up to 8,000 characters.')
      : draftTags.length > 24 || draftTags.some((tag) => Array.from(tag).length > 48)
        ? (zh ? '最多 24 个标签，每个标签最多 48 字。' : 'Use up to 24 tags, each with up to 48 characters.')
        : ''
  const savedCallback = useRef(onSaved)
  savedCallback.current = onSaved
  const dirtyCallback = useRef(onDirtyChange)
  dirtyCallback.current = onDirtyChange

  useEffect(() => { dirtyCallback.current?.(dirty) }, [dirty])
  useEffect(() => {
    if (!dirty) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = '' }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  async function load() {
    const current = ++sequence.current
    setBusy(true)
    setStatus('')
    try {
      const value = await api<SessionManagement>(`/session-metadata?session_id=${encodeURIComponent(session.session_id)}`)
      if (current !== sequence.current) return
      setMetadata(value)
      setDraft(draftOf(value))
      setNeedsReload(false)
      savedCallback.current(value)
    } catch (error) {
      if (current !== sequence.current) return
      setNeedsReload(true)
      setStatus('load-failed')
      setErrorStatus(statusOf(error))
    } finally {
      if (current === sequence.current) setBusy(false)
    }
  }

  useEffect(() => {
    setMetadata(null)
    setDraft({ title: '', note: '', tags: '', starred: false, archived: false })
    void load()
    return () => { sequence.current++; dirtyCallback.current?.(false) }
    // A fresh selection reads its current revision before enabling edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session.session_id, api])

  async function save() {
    if (!metadata || busy || needsReload || validation) return
    const current = ++sequence.current
    setBusy(true)
    setStatus('')
    const tags = draftTags
    try {
      const value = await api<SessionManagement>(`/session-metadata?session_id=${encodeURIComponent(session.session_id)}`, { method: 'PUT', body: JSON.stringify({ ...draft, tags, expected_revision: metadata.revision }) })
      if (current !== sequence.current) return
      setMetadata(value)
      setDraft(draftOf(value))
      setStatus('saved')
      savedCallback.current(value)
    } catch (error) {
      if (current !== sequence.current) return
      setNeedsReload(true)
      setStatus(statusOf(error) === 409 ? 'conflict' : 'failed')
      setErrorStatus(statusOf(error))
    } finally {
      if (current === sequence.current) setBusy(false)
    }
  }

  const messages = {
    saved: zh ? '更改已保存。' : 'Changes saved.',
    conflict: zh ? '会话信息已被其他操作更新。你的修改仍保留在此，请明确重新加载后再编辑。' : 'This session was updated elsewhere. Your edits are retained here. Reload explicitly before editing again.',
    failed: zh ? `保存结果未确认（${errorStatus || 'network'}）。你的修改已保留，请重新加载核对最新信息。` : `Save could not be confirmed (${errorStatus || 'network'}). Your edits are retained. Reload to inspect the current version.`,
    'load-failed': zh ? `会话信息读取失败（${errorStatus || 'network'}），请重新加载。` : `Unable to load session details (${errorStatus || 'network'}). Please reload.`,
    '': '',
  }

  return <section className="session-editor flex h-auto min-h-0 flex-col lg:h-full" aria-label={zh ? '会话信息' : 'Session details'}>
    <div className="flex items-center justify-between px-5 py-4"><h3 className="text-sm font-semibold">{zh ? '会话信息' : 'Session details'}</h3><span className="text-[10px] text-muted-foreground">{metadata ? `rev ${metadata.revision}` : '—'}</span></div>
    <Separator />
    <div className="min-h-0 flex-1 overflow-visible p-5 lg:overflow-y-auto">
      {!metadata && busy ? <div className="space-y-5"><Skeleton className="h-9 w-full" /><Skeleton className="h-20 w-full" /><Skeleton className="h-36 w-full" /></div> : <fieldset id="sessionMetadataFields" disabled={busy || !metadata} className="space-y-5 disabled:opacity-60">
        <div className="space-y-2"><label htmlFor="sessionTitleInput" className="text-xs font-medium">{zh ? '自定义标题' : 'Custom title'}</label><Input id="sessionTitleInput" value={draft.title} onChange={(event) => setDraft({ ...draft, title: event.target.value })} placeholder={zh ? '留空以使用原始摘要' : 'Use original summary'} className="h-9 text-xs" /><p className="text-[10px] leading-4 text-muted-foreground">{zh ? '仅用于归档展示，最多 200 字。' : 'Archive display only. Up to 200 characters.'}</p></div>
        <div className="space-y-2"><label htmlFor="sessionTagsInput" className="flex items-center gap-1.5 text-xs font-medium"><Tag className="size-3" />{zh ? '标签' : 'Tags'}</label><Textarea id="sessionTagsInput" value={draft.tags} onChange={(event) => setDraft({ ...draft, tags: event.target.value })} placeholder={zh ? '架构, 待跟进' : 'architecture, follow-up'} rows={3} className="min-h-20 text-xs" /><p className="text-[10px] leading-4 text-muted-foreground">{zh ? '逗号或换行分隔；最多 24 个标签。' : 'Separate with commas or new lines. Up to 24 tags.'}</p></div>
        <div className="space-y-2"><label htmlFor="sessionNoteInput" className="text-xs font-medium">{zh ? '备注' : 'Notes'}</label><Textarea id="sessionNoteInput" value={draft.note} onChange={(event) => setDraft({ ...draft, note: event.target.value })} placeholder={zh ? '记录结论、下一步，或值得回看的地方…' : 'Capture conclusions, next steps, or something worth revisiting…'} rows={7} className="min-h-36 text-xs leading-6" /><p className="text-[10px] text-muted-foreground">{zh ? '最多 8,000 字。' : 'Up to 8,000 characters.'}</p></div>
        <Separator />
        <label className="flex cursor-pointer items-center gap-2.5 text-xs"><Checkbox id="sessionStarredInput" checked={draft.starred} onCheckedChange={(checked) => setDraft({ ...draft, starred: checked === true })} /><Star className="size-3.5 text-amber-500" />{zh ? '加入收藏' : 'Add to favorites'}</label>
        <label className="flex cursor-pointer items-center gap-2.5 text-xs"><Checkbox id="sessionArchivedInput" checked={draft.archived} onCheckedChange={(checked) => setDraft({ ...draft, archived: checked === true })} /><Archive className="size-3.5 text-muted-foreground" />{zh ? '归档会话' : 'Archive session'}</label>
        <p className="text-[10px] leading-5 text-muted-foreground">{zh ? '归档会从默认列表隐藏会话，原始记录会保留。' : 'Archiving hides this session from the default library and keeps its source records.'}</p>
      </fieldset>}
      <div id="sessionMetadataStatus" role={status && status !== 'saved' ? 'alert' : 'status'} className={`mt-4 text-xs leading-5 ${status === 'saved' ? 'text-emerald-700' : 'text-destructive'}`}>{status === 'saved' && <Check className="mr-1 inline size-3" />}{messages[status]}</div>
      {validation && <p role="alert" className="mt-2 text-xs leading-5 text-destructive">{validation}</p>}
      {needsReload && <Button id="sessionMetadataReload" variant="outline" size="sm" className="mt-3 w-full gap-2 text-xs" disabled={busy} onClick={() => { if (!dirty || window.confirm(zh ? '重新加载会替换当前未保存的修改，是否继续？' : 'Reloading replaces your unsaved edits. Continue?')) void load() }}><RotateCcw className="size-3" />{zh ? '重新加载会话信息' : 'Reload session details'}</Button>}
      <div className="mt-6 rounded-lg bg-muted/50 p-3"><p className="text-[10px] font-medium text-muted-foreground">SESSION ID</p><p className="mt-1 break-all font-mono text-[10px] leading-5 text-muted-foreground">{session.session_id}</p>{session.project && <><p className="mt-3 text-[10px] font-medium text-muted-foreground">{zh ? '来源项目' : 'SOURCE PROJECT'}</p><p className="mt-1 break-all text-[11px] text-muted-foreground">{session.project}</p></>}</div>
    </div>
    <div className="flex gap-2 border-t bg-background p-4"><Button id="sessionMetadataSave" className="flex-1 gap-2 text-xs" disabled={!metadata || busy || needsReload || !dirty || !!validation} onClick={() => void save()}>{busy ? <LoaderCircle className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}{zh ? '保存更改' : 'Save changes'}</Button><Button id="sessionMetadataCancel" variant="outline" size="icon" title={zh ? '取消修改' : 'Discard changes'} aria-label={zh ? '取消修改' : 'Discard changes'} disabled={!metadata || busy || needsReload || !dirty} onClick={() => { if (metadata) { setDraft(draftOf(metadata)); setStatus('') } }}><X className="size-4" /></Button></div>
  </section>
}
