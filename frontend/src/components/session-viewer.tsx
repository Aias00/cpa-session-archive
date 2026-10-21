import { useEffect, useRef, useState } from "react";
import { ArrowLeft, ArrowRight, ArrowUpRight, Bot, ChevronDown, ChevronFirst, ChevronLast, ChevronLeft, ChevronRight, Clock3, Layers, MessageSquare, Terminal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { ExpandableText, MessageBubble, ViewerError, ViewerLoading } from "@/components/content-viewer";
import { RequestViewer } from "@/components/request-viewer";
import { displayText, kindLabel, localTime, routePath, toolEntryKind, type RequestView, type TurnSummary } from "@/lib/conversation";
import type { ApiClient, Locale, SessionSummary } from "@/lib/types";

export interface SessionViewerProps {
  api: ApiClient;
  locale: Locale;
  sessionID: string;
  turnID?: string;
  requestID?: string;
  toolID?: string;
  rawPart?: string;
  onNavigate: (path: string) => void;
  identityName?: (row: SessionSummary) => string;
}

interface TurnPage { turns: TurnSummary[]; total: number; limit: number; offset: number }
interface TurnDetailPage { turn: TurnSummary; records: RequestView[]; total: number; limit: number; offset: number }

function Pagination({ page, total, limit, onPage, english, turn = false }: { page: number; total: number; limit: number; onPage: (page: number) => void; english: boolean; turn?: boolean }) {
  const pages = Math.max(1, Math.ceil(total / limit));
  const [jump, setJump] = useState(String(page));
  useEffect(() => setJump(String(page)), [page]);
  if (!total) return null;
  return <div id={turn ? "turnPagination" : "pagination"} className="flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-card px-4 py-3">
    <span id={turn ? "turnPageInfo" : "pageInfo"} aria-live="polite" className="text-xs text-muted-foreground">{english ? `Page ${page} / ${pages} · ${total} ${turn ? "requests" : "turns"}` : `第 ${page} / ${pages} 页 · 共 ${total} ${turn ? "次请求" : "轮对话"}`}</span>
    <div className="flex items-center gap-1">
      {!turn && <Button size="icon" variant="ghost" className="size-8" title={english ? "First page" : "首页"} aria-label={english ? "First page" : "首页"} disabled={page <= 1} onClick={() => onPage(1)}><ChevronFirst className="size-4" /></Button>}
      <Button size="sm" variant="outline" className="h-8 px-2 text-xs" disabled={page <= 1} onClick={() => onPage(page - 1)}><ChevronLeft className="mr-1 size-3.5" />{english ? "Previous" : "上一页"}</Button>
      {!turn && <form className="mx-1" onSubmit={(event) => { event.preventDefault(); const value = Number(jump); if (Number.isInteger(value)) onPage(Math.min(pages, Math.max(1, value))); }}><Input id="pageJump" className="h-8 w-14 px-1 text-center text-xs" type="number" min={1} max={pages} value={jump} aria-label={english ? "Go to page" : "跳转页码"} onChange={(event) => setJump(event.target.value)} /></form>}
      <Button size="sm" variant="outline" className="h-8 px-2 text-xs" disabled={page >= pages} onClick={() => onPage(page + 1)}>{english ? "Next" : "下一页"}<ChevronRight className="ml-1 size-3.5" /></Button>
      {!turn && <Button size="icon" variant="ghost" className="size-8" title={english ? "Last page" : "末页"} aria-label={english ? "Last page" : "末页"} disabled={page >= pages} onClick={() => onPage(pages)}><ChevronLast className="size-4" /></Button>}
    </div>
  </div>;
}

function LazyUserText({ api, sessionID, turnID, text, english }: { api: ApiClient; sessionID: string; turnID: string; text: string; english: boolean }) {
  const [full, setFull] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const expand = async () => {
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    setLoading(true); setError("");
    try {
      for (let attempt = 0; attempt < 90; attempt++) {
        const result = await api<{ status: string; text?: string }>(`/turn-text?session_id=${encodeURIComponent(sessionID)}&turn_id=${encodeURIComponent(turnID)}`, { signal: request.signal });
        if (request.signal.aborted) return;
        if (result.status === "ok") { setFull(result.text ?? ""); return; }
        if (result.status !== "building" && result.status !== "pending") throw new Error(english ? "The full text is unavailable." : "暂时无法获取完整文本。");
        await new Promise<void>((resolve) => {
          const finish = () => { request.signal.removeEventListener("abort", abort); resolve(); };
          const timer = window.setTimeout(finish, 2000);
          const abort = () => { window.clearTimeout(timer); finish(); };
          request.signal.addEventListener("abort", abort, { once: true });
        });
        if (request.signal.aborted) return;
      }
      throw new Error(english ? "Building full text timed out. Try again." : "完整文本生成超时，请重试。");
    } catch (caught) {
      if (!request.signal.aborted) setError(caught instanceof Error ? caught.message : String(caught));
    } finally { if (!request.signal.aborted) setLoading(false); }
  };
  return <div className="space-y-3"><div className="whitespace-pre-wrap break-words text-[13px] leading-7 [overflow-wrap:anywhere]">{displayText(full ?? text)}</div>{full === null && (text.endsWith("…") || text.length >= 150) && <Button size="sm" variant="outline" className="h-8 text-xs" disabled={loading} onClick={() => void expand()}><ChevronDown className={`mr-1.5 size-3.5 ${loading ? "animate-pulse" : ""}`} />{loading ? (english ? "Building full text…" : "正在加载全文…") : (english ? "Expand full content" : "展开完整内容")}</Button>}{error && <p role="alert" className="text-xs text-destructive">{error}</p>}</div>;
}

function ProcessStep({ api, view, number, english, base, onNavigate }: { api: ApiClient; view: RequestView; number: number; english: boolean; base: string; onNavigate: (path: string) => void }) {
  const [open, setOpen] = useState(false);
  const [full, setFull] = useState<RequestView | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    if (!open || full) return;
    let active = true;
    const controller = new AbortController();
    setLoading(true); setError("");
    api<RequestView>(`/request-view?id=${encodeURIComponent(view.request_id)}`, { signal: controller.signal }).then((result) => {
      if (result.session_id && result.session_id !== view.session_id) throw new Error(english ? "This request does not belong to this session." : "该请求不属于当前会话。");
      if (active) setFull(result);
    }).catch((caught: unknown) => { if (active) setError(caught instanceof Error ? caught.message : String(caught)); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; controller.abort(); };
  }, [open, full, api, view.request_id, view.session_id, retry, english]);
  const current = full ?? view;
  const tools = new Map<string, { id: string; name: string; hasCallID: boolean }>();
  const readable = (current.entries ?? []).filter((entry, index) => {
    if (toolEntryKind(entry)) {
      const id = entry.call_id || `index-${index}`;
      tools.set(id, { id, name: entry.label || (english ? "Tool" : "工具"), hasCallID: !!entry.call_id });
      return false;
    }
    return entry.role !== "user";
  });
  return <Collapsible open={open} onOpenChange={setOpen} className="process-step overflow-hidden rounded-xl border border-border/80 bg-card" data-request-id={view.request_id}>
    <CollapsibleTrigger className="flex w-full items-start gap-3 p-4 text-left transition-colors hover:bg-muted/40">
      <span className="flex size-7 shrink-0 items-center justify-center rounded-lg border bg-muted/40 font-mono text-xs text-muted-foreground">{String(number).padStart(2, "0")}</span>
      <div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-2 text-sm font-medium">{english ? `Step ${number}` : `步骤 ${number}`}<Badge variant="secondary" className="text-[10px] font-normal">{kindLabel(view.kind, english)}</Badge><span className="truncate font-mono text-[11px] font-normal text-muted-foreground">{view.requested_model || view.model}</span></div><div className="mt-1.5 text-[11px] text-muted-foreground">{localTime(view.started_at)}{!full && <span className="ml-2">· {english ? "Expand to load the complete process" : "展开后加载完整过程正文"}</span>}</div></div><ChevronDown className={`mt-1 size-4 shrink-0 text-muted-foreground transition-transform ${open ? "rotate-180" : ""}`} />
    </CollapsibleTrigger>
    <CollapsibleContent><div className="space-y-3 border-t border-border/60 p-4">
      {loading ? <ViewerLoading english={english} /> : error ? <ViewerError error={error} english={english} onRetry={() => setRetry(retry + 1)} /> : <>
        {readable.map((entry, index) => entry.role === "compaction" ? <div key={index} className="flex items-center gap-2 rounded-lg bg-muted/50 p-3 text-xs text-muted-foreground"><Layers className="size-3.5" />{english ? "Context was compacted in this step" : "此步骤发生上下文压缩"}</div> : <MessageBubble key={index} role={entry.role || "assistant"} text={entry.text || ""} label={entry.label} english={english} />)}
        {tools.size > 0 && <div className="flex flex-wrap gap-2">{[...tools.values()].map((tool) => <Button key={tool.id} size="sm" variant="outline" className="h-auto min-h-8 max-w-full py-1.5 text-xs" onClick={() => onNavigate(`${base}/requests/${encodeURIComponent(view.request_id)}${tool.hasCallID ? `/tools/${encodeURIComponent(tool.id)}` : ""}`)}><Terminal className="mr-1.5 size-3.5 shrink-0" /><span className="truncate">{displayText(tool.name)} · {tool.hasCallID ? (english ? "View complete call" : "查看完整调用") : (english ? "View request" : "查看所属请求")}</span><ArrowUpRight className="ml-1.5 size-3 shrink-0" /></Button>)}</div>}
        {!readable.length && !tools.size && <p className="py-4 text-sm text-muted-foreground">{english ? "No readable text was captured for this step." : "没有可直接阅读的文本内容。"}</p>}
      </>}
      <Button variant="ghost" size="sm" className="h-8 text-xs text-muted-foreground" onClick={() => onNavigate(`${base}/requests/${encodeURIComponent(view.request_id)}`)}>{english ? "Original request / response (diagnostics)" : "原始请求/响应（排障）"}<ArrowUpRight className="ml-1.5 size-3" /></Button>
    </div></CollapsibleContent>
  </Collapsible>;
}

function SessionContent(props: SessionViewerProps) {
  const { api, locale, sessionID, turnID, requestID, onNavigate } = props;
  const english = String(locale).startsWith("en");
  const [page, setPage] = useState(1);
  const [order, setOrder] = useState("desc");
  const [data, setData] = useState<TurnPage | TurnDetailPage | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const limit = turnID ? 10 : 20;
  const scope = JSON.stringify([sessionID, turnID ?? ""]);
  useEffect(() => {
    if (requestID) return;
    let active = true;
    const controller = new AbortController();
    setLoading(true); setError("");
    const query = new URLSearchParams({ session_id: sessionID, limit: String(limit), offset: String((page - 1) * limit) });
    if (turnID) query.set("turn_id", turnID); else query.set("order", order);
    api<TurnPage | TurnDetailPage>(`/turns?${query}`, { signal: controller.signal }).then((result) => { if (active) setData(result); }).catch((caught: unknown) => { if (active) setError(caught instanceof Error ? caught.message : String(caught)); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; controller.abort(); };
  }, [api, sessionID, turnID, requestID, page, order, limit, retry]);
  if (requestID) return <RequestViewer {...props} requestID={requestID} />;
  const turnPage = data && "turn" in data ? data : null;
  const sessionPage = data && "turns" in data ? data : null;
  const turn = turnPage?.turn;
  const turnIdentity = turn ? props.identityName?.({ ...turn, last_at: turn.last_at ?? "" }) || turn.principal_alias || turn.key_id : "";
  const changePage = (value: number) => {
    if (value === page) return;
    setLoading(true);
    setPage(value);
    document.querySelector("main")?.scrollTo({ top: 0 });
  };

  return <section className="min-w-0 space-y-5" aria-label={english ? "Session conversation" : "会话内容"}>
    {turnID ? <><Button variant="ghost" size="sm" className="-ml-2 text-muted-foreground" onClick={() => onNavigate(routePath(sessionID))}><ArrowLeft className="mr-2 size-4" />{english ? "Back to session" : "返回当前会话"}</Button>{turn && <><div><div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground"><MessageSquare className="size-3.5" />{english ? "CONVERSATION TURN" : "本轮对话"}<span className="ml-auto font-mono">{turn.model}</span></div><h2 id="turnTitle" className="line-clamp-2 break-words text-xl leading-8 font-semibold tracking-tight">{displayText(turn.user_text || (english ? "Untitled turn" : "未命名对话"))}</h2><p className="mt-2 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground"><Clock3 className="size-3" />{localTime(turn.last_at)}<span className="mx-1">·</span>{turn.requests} {english ? "requests" : "次请求"}{turnIdentity && <><span className="mx-1">·</span><span>{turnIdentity}</span></>}</p></div><div id="turnPrimary" key={scope} className="grid min-w-0 gap-4 xl:grid-cols-2"><Card className="min-w-0"><CardContent className="p-5"><h3 className="mb-4 flex items-center gap-2 text-xs font-semibold text-muted-foreground"><MessageSquare className="size-3.5" />{english ? "User command" : "用户命令"}</h3><LazyUserText api={api} sessionID={sessionID} turnID={turnID} text={turn.user_text || (english ? "Untitled" : "未命名")} english={english} /></CardContent></Card><Card className="turn-final min-w-0 border-primary/15 bg-primary/[0.025]"><CardContent className="p-5"><h3 className="mb-4 flex items-center gap-2 text-xs font-semibold text-primary/80"><Bot className="size-3.5" />{english ? "Final answer" : "最终答复"}</h3>{turn.final_text ? <ExpandableText text={turn.final_text} english={english} /> : <p className="text-sm text-muted-foreground">{english ? "No final answer was captured." : "尚未捕获到最终答复。"}</p>}</CardContent></Card></div></> }</> : <div className="flex items-center justify-between gap-3"><h3 className="flex items-center gap-2 text-sm font-semibold"><MessageSquare className="size-4 text-muted-foreground" />{english ? "Conversation turns" : "对话轮次"}{data && <Badge variant="secondary" className="font-mono text-[10px]">{data.total}</Badge>}</h3><label className="flex items-center gap-2 text-xs text-muted-foreground">{english ? "Order" : "排序"}<select id="sessionOrder" value={order} onChange={(event) => { setLoading(true); setOrder(event.target.value); setPage(1); }} className="h-8 rounded-lg border border-input bg-background px-2 text-xs text-foreground"><option value="desc">{english ? "Newest first" : "最新在前"}</option><option value="asc">{english ? "Oldest first" : "最早在前"}</option></select></label></div>}
    {loading ? <ViewerLoading english={english} /> : error ? <ViewerError error={error} english={english} onRetry={() => setRetry(retry + 1)} /> : <>
      {sessionPage && <div className="space-y-3">{sessionPage.turns.map((item, index) => <button type="button" key={item.turn_id} onClick={() => onNavigate(routePath(sessionID, item.turn_id))} className="turn-card group block w-full overflow-hidden rounded-xl border border-border/80 bg-card text-left transition-all hover:border-primary/35 hover:shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"><div className="grid min-w-0 gap-4 p-5 md:grid-cols-2"><div className="min-w-0"><div className="mb-2.5 flex items-center gap-2 text-[11px] font-medium text-muted-foreground"><MessageSquare className="size-3.5" />{english ? "User command" : "用户命令"}</div><div className="line-clamp-4 whitespace-pre-wrap break-words text-sm leading-6 [overflow-wrap:anywhere]">{displayText(item.user_text || (english ? "Untitled" : "未命名"))}</div></div><div className="turn-final min-w-0 md:border-l md:border-border/60 md:pl-5"><div className="mb-2.5 flex items-center gap-2 text-[11px] font-medium text-primary/75"><Bot className="size-3.5" />{english ? "Final answer" : "最终答复"}</div><div className="line-clamp-4 whitespace-pre-wrap break-words text-sm leading-6 text-muted-foreground [overflow-wrap:anywhere]">{displayText(item.final_text || (english ? "No final answer" : "尚无最终答复"))}</div></div></div><div className="flex flex-wrap items-center gap-2 border-t border-border/60 bg-muted/20 px-5 py-2.5 text-[10px] text-muted-foreground"><span className="font-mono">#{(page - 1) * limit + index + 1}</span><span>{item.requests} {english ? "requests" : "次请求"}</span>{item.model && <span className="font-mono">{item.model}</span>}{item.tool_names?.map((name) => <Badge key={name} variant="outline" className="gap-1 text-[9px] font-normal"><Terminal className="size-2.5" />{name}</Badge>)}<span className="ml-auto">{localTime(item.last_at)}</span><ArrowRight className="size-3.5 transition-transform group-hover:translate-x-0.5" /></div></button>)}</div>}
      {turnPage && <div><div className="mb-3 flex items-center gap-2"><Layers className="size-4 text-muted-foreground" /><h3 className="text-sm font-semibold">{english ? "Execution process" : "执行过程"}</h3><Badge variant="secondary" className="font-mono text-[10px]">{turnPage.total}</Badge></div><div className="space-y-3">{turnPage.records.map((view, index) => <ProcessStep key={`${page}:${view.request_id}`} api={api} view={view} number={(page - 1) * limit + index + 1} english={english} base={routePath(sessionID, turnID)} onNavigate={onNavigate} />)}</div></div>}
      {data?.total === 0 && <div className="rounded-xl border border-dashed p-12 text-center"><MessageSquare className="mx-auto mb-3 size-7 text-muted-foreground/50" /><p className="text-sm text-muted-foreground">{english ? "No conversation turns were captured for this session." : "该会话尚未捕获到可展示的对话轮次。"}</p></div>}
      {data && <Pagination page={page} total={data.total} limit={limit} onPage={changePage} english={english} turn={!!turnID} />}
    </>}
  </section>;
}

export function SessionViewer(props: SessionViewerProps) {
  const { sessionID, turnID, requestID, toolID, rawPart } = props;
  if (requestID) return <RequestViewer key={JSON.stringify([sessionID, turnID, requestID, toolID, rawPart])} {...props} requestID={requestID} />;
  return <SessionContent key={JSON.stringify([sessionID, turnID])} {...props} />;
}
