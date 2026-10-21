import { Archive, ArrowUpRight, Boxes, FolderGit2, Layers3, MessageSquareText, Star } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import type { FacetCount, LibraryView, Locale, ProjectFilter } from '@/lib/types'


interface WorkspaceSidebarProps {
  locale: Locale
  projects: FacetCount[]
  project?: ProjectFilter
  view: LibraryView
  onProjectChange: (project?: ProjectFilter) => void
  onViewChange: (view: LibraryView) => void
}

export function WorkspaceSidebar({ locale, projects, project, view, onProjectChange, onViewChange }: WorkspaceSidebarProps) {
  const zh = locale === 'zh'
  const nav = [
    { id: 'all' as const, icon: MessageSquareText, label: zh ? '全部会话' : 'All sessions' },
    { id: 'starred' as const, icon: Star, label: zh ? '我的收藏' : 'Favorites' },
    { id: 'archived' as const, icon: Archive, label: zh ? '已归档' : 'Archived' },
  ]
  const projectLabel = (value: string) => value
  return (
    <aside className="workspace-sidebar flex min-h-0 flex-col border-r border-border/80 bg-sidebar" aria-label={zh ? '工作区导航' : 'Workspace navigation'}>
      <div className="flex h-[76px] items-center gap-3 px-5">
        <div className="flex size-9 items-center justify-center rounded-xl bg-foreground text-background shadow-sm"><Layers3 className="size-5" /></div>
        <div><p className="text-sm font-semibold tracking-tight">Session Archive</p><p className="mt-0.5 text-[11px] text-muted-foreground">{zh ? '你的 AI 工作记录' : 'Your AI workspace history'}</p></div>
      </div>
      <div className="px-3 pb-5">
        <p className="px-3 pb-2 pt-3 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">{zh ? '会话库' : 'Library'}</p>
        <nav className="space-y-1">
          {nav.map(({ id, icon: Icon, label }) => <Button key={id} variant="ghost" onClick={() => onViewChange(id)} aria-current={view === id ? 'page' : undefined} className={`h-10 w-full justify-start gap-3 px-3 text-[13px] ${view === id ? 'bg-background font-semibold text-foreground shadow-sm ring-1 ring-border/70' : 'font-normal text-muted-foreground hover:text-foreground'}`}><Icon className="size-4" />{label}{view === id && <span className="ml-auto size-1.5 rounded-full bg-foreground" />}</Button>)}
        </nav>
      </div>
      <div className="px-6 pb-5"><label htmlFor="automaticView" className="mb-2 block text-[10px] font-semibold text-muted-foreground">{zh ? '自动整理视图' : 'Automatic views'}</label><select id="automaticView" value={['starred', 'archived'].includes(view) ? '' : view} onChange={(event) => onViewChange(event.target.value as LibraryView)} className="h-9 w-full rounded-md border bg-background px-2 text-xs"><option value="" disabled>{zh ? '选择自动视图' : 'Choose a view'}</option><option value="all">{zh ? '全部会话' : 'All sessions'}</option><option value="recent">{zh ? '最近 7 天' : 'Last 7 days'}</option><option value="inactive">{zh ? '长期未活跃' : 'Inactive sessions'}</option><option value="main">{zh ? '主会话' : 'Main sessions'}</option><option value="subagent">{zh ? '子代理会话' : 'Subagent sessions'}</option><option value="system">{zh ? '系统任务' : 'System tasks'}</option><option value="prewarm">{zh ? '预热会话' : 'Prewarm sessions'}</option><option value="unassigned">{zh ? '待归类（未知或冲突项目）' : 'Unresolved project'}</option></select></div>
      <Separator />
      <div className="flex items-center justify-between px-6 pb-3 pt-5"><p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">{zh ? '项目' : 'Projects'}</p><span className="text-[10px] tabular-nums text-muted-foreground">{projects.length}</span></div>
      <ScrollArea className="min-h-0 flex-1 px-3">
        <nav className="space-y-0.5 pb-4" aria-label={zh ? '项目筛选' : 'Project filters'}>
          <Button variant="ghost" onClick={() => onProjectChange(undefined)} aria-current={!project ? 'true' : undefined} className={`h-9 w-full justify-start gap-3 px-3 text-xs ${!project ? 'bg-accent font-medium' : 'font-normal text-muted-foreground'}`}><Boxes className="size-4 shrink-0" />{zh ? '所有项目' : 'All projects'}</Button>
          {projects.map((item) => {
            const active = project?.name === item.name && project.value === item.value
            return <Button key={`${item.name}\0${item.value}`} variant="ghost" title={`${item.name}: ${item.value}`} onClick={() => onProjectChange({ name: item.name, value: item.value })} aria-current={active ? 'true' : undefined} className={`h-12 w-full justify-start gap-3 px-3 text-xs ${active ? 'bg-accent font-medium' : 'font-normal text-muted-foreground'}`}><FolderGit2 className="size-4 shrink-0" /><span className="min-w-0 text-left"><span className="block truncate">{projectLabel(item.value)}</span><span className="block truncate text-[9px] font-normal text-muted-foreground">{item.name}</span></span><span className="ml-auto shrink-0 text-[10px] tabular-nums text-muted-foreground">{item.sessions}</span></Button>
          })}
          {!projects.length && <p className="px-3 py-4 text-xs leading-5 text-muted-foreground">{zh ? '采集到项目后会显示在这里。' : 'Captured projects will appear here.'}</p>}
        </nav>
      </ScrollArea>
      <div className="m-3 rounded-xl border border-border/80 bg-background/60 p-3.5"><div className="flex items-center gap-2 text-xs font-medium"><span className="size-1.5 rounded-full bg-emerald-500" />{zh ? '历史记录，井然有序' : 'A home for your history'}</div><p className="mt-2 text-[11px] leading-5 text-muted-foreground">{zh ? '整理会话、回看过程，让每次探索都能接续。' : 'Organize sessions and revisit the work behind every answer.'}</p><a className="mt-3 inline-flex items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground" href="https://github.com/Aias00/cpa-session-archive" target="_blank" rel="noreferrer">{zh ? '项目文档' : 'Documentation'}<ArrowUpRight className="size-3" /></a></div>
    </aside>
  )
}
