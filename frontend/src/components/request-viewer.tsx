import { useEffect, useState } from "react";
import { ArrowLeft, ArrowUpRight, FileJson2, Layers, Terminal } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { MessageBubble, PayloadViewer, ViewerError, ViewerLoading } from "@/components/content-viewer";
import { contentText, decodePayload, displayText, extractTools, kindLabel, localTime, parsePayload, prettyPayload, recordPayloads, routePath, toolEntryKind, type ArchiveRecord, type RequestView, type ToolEntry } from "@/lib/conversation";
import type { ApiClient, Locale } from "@/lib/types";

export interface RequestViewerProps {
  api: ApiClient;
  locale: Locale;
  sessionID: string;
  turnID?: string;
  requestID: string;
  toolID?: string;
  rawPart?: string;
  onNavigate: (path: string) => void;
}

export function RequestViewer({ api, locale, sessionID, turnID, requestID, toolID, rawPart, onNavigate }: RequestViewerProps) {
  const english = String(locale).startsWith("en");
  const [view, setView] = useState<RequestView | null>(null);
  const [record, setRecord] = useState<ArchiveRecord | null>(null);
  const [tool, setTool] = useState<ToolEntry | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [retry, setRetry] = useState(0);
  const diagnostic = rawPart !== undefined || toolID !== undefined;
  const base = routePath(sessionID, turnID, requestID);

  useEffect(() => {
    let active = true;
    setView(null); setRecord(null); setTool(null); setError(""); setLoading(true);
    const load = async () => {
      try {
        if (!diagnostic) {
          const result = await api<RequestView>(`/request-view?id=${encodeURIComponent(requestID)}`);
          if (result.session_id && result.session_id !== sessionID) throw new Error(english ? "This request does not belong to this session." : "该请求不属于当前会话。");
          if (active) setView(result);
        } else {
          const result = await api<ArchiveRecord>(`/requests?id=${encodeURIComponent(requestID)}`);
          if (result.session_id && result.session_id !== sessionID) throw new Error(english ? "This request does not belong to this session." : "该请求不属于当前会话。");
          if (!active) return;
          setRecord(result);
          if (toolID !== undefined) {
            const entries = extractTools(recordPayloads(result));
            let entry = entries.find((item) => item.callID === toolID) ?? (/^\d+$/.test(toolID) ? entries[Number(toolID)] : undefined);
            if (!entry?.output) {
              const records = await api<ArchiveRecord[]>(`/request-context?id=${encodeURIComponent(requestID)}&limit=16`);
              const context = extractTools(records.filter((item) => !item.session_id || item.session_id === sessionID).flatMap(recordPayloads));
              const match = context.find((item) => item.callID === (entry?.callID || toolID));
              if (match) entry = { ...match, name: match.name || entry?.name || "", arguments: match.arguments || entry?.arguments || "" };
            }
            if (active) setTool(entry ?? null);
          }
        }
      } catch (caught) {
        if (active) setError(caught instanceof Error ? caught.message : String(caught));
      } finally { if (active) setLoading(false); }
    };
    void load();
    return () => { active = false; };
  }, [api, sessionID, requestID, diagnostic, toolID, rawPart, english, retry]);

  const backPath = diagnostic ? base : routePath(sessionID, turnID);
  const backLabel = diagnostic ? (english ? "Back to this request" : "返回本次请求") : turnID ? (english ? "Back to this turn" : "返回本轮对话") : (english ? "Back to session" : "返回当前会话");
  const rawLabels: Record<string, string> = {
    request: english ? "Original request" : "原始请求",
    upstream: english ? "Upstream request" : "上游请求",
    response: english ? "Original response" : "原始响应",
    instructions: english ? "System instructions" : "系统指令",
  };
  let rawText = "";
  if (record && rawPart) {
    rawText = decodePayload(rawPart === "request" || rawPart === "instructions" ? record.original_request : rawPart === "upstream" ? record.upstream_request : record.response);
    if (rawPart === "instructions") {
      const object = parsePayload(rawText) as Record<string, unknown> | null;
      rawText = typeof object?.instructions === "string" ? object.instructions : "";
      if (!rawText && object) {
        const messages = object.messages ?? object.input;
        if (Array.isArray(messages)) rawText = messages.filter((entry: Record<string, unknown>) => entry.role === "system" || entry.role === "developer").map((entry: Record<string, unknown>) => contentText(entry.content)).join("\n\n");
      }
    } else rawText = prettyPayload(rawText);
  }

  return <section className="min-w-0 space-y-5" aria-label={english ? "Request details" : "请求详情"}>
    <Button variant="ghost" size="sm" className="-ml-2 text-muted-foreground" onClick={() => onNavigate(backPath)}><ArrowLeft className="mr-2 size-4" />{backLabel}</Button>
    {loading ? <ViewerLoading english={english} /> : error ? <ViewerError english={english} error={error} onRetry={() => setRetry(retry + 1)} /> : diagnostic ? <>
      <div><div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground"><FileJson2 className="size-3.5" />{english ? "SOURCE INSPECTOR" : "原始记录查看器"}</div><h2 id="subDetailTitle" className="text-xl font-semibold tracking-tight">{rawPart ? rawLabels[rawPart] ?? rawLabels.request : tool?.name || (english ? "Tool call" : "工具调用")}</h2><p className="mt-2 break-all font-mono text-xs text-muted-foreground">{tool?.callID || requestID}</p></div>
      <div id="subDetail">
        {rawPart ? <PayloadViewer key={`${requestID}:${rawPart}`} text={rawText} english={english} /> : tool ? <div className="grid min-w-0 gap-5 xl:grid-cols-2"><Card className="tool-card min-w-0"><CardHeader><CardTitle className="text-sm">{english ? "Tool input" : "工具输入"}</CardTitle></CardHeader><CardContent><PayloadViewer text={prettyPayload(tool.arguments)} english={english} /></CardContent></Card><Card className="tool-card min-w-0"><CardHeader><CardTitle className="text-sm">{english ? "Tool output" : "工具输出"}</CardTitle></CardHeader><CardContent>{tool.output ? <PayloadViewer text={prettyPayload(tool.output)} english={english} /> : <p className="text-sm text-muted-foreground">{english ? "No tool output was captured." : "尚未捕获到工具结果。"}</p>}</CardContent></Card></div> : <p className="rounded-xl border border-dashed p-8 text-center text-sm text-muted-foreground">{english ? "No matching tool call was found in this request or its context." : "当前请求及上下文中没有找到对应的工具调用。"}</p>}
      </div>
    </> : view && <>
      <div><div className="mb-3 flex flex-wrap items-center gap-2"><Badge variant="secondary">{kindLabel(view.kind, english)}</Badge><span className="font-mono text-xs text-muted-foreground">{view.requested_model || view.model}</span><span className="ml-auto text-xs text-muted-foreground">{localTime(view.started_at)}</span></div><h2 id="requestTitle" className="break-words text-xl leading-8 font-semibold tracking-tight">{displayText(view.summary || (english ? "Request timeline" : "请求过程"))}</h2><div className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">{view.outcome && <span>{view.outcome}</span>}{view.status_code ? <Badge variant={view.status_code >= 400 ? "destructive" : "outline"}>{view.status_code}</Badge> : null}</div></div>
      <div id="requestDetail" className="space-y-5">
        {view.error && <ViewerError error={view.error} english={english} />}
        {!!(view.history_items || view.system_chars || view.tool_definitions) && <div className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-xl border border-border/70 bg-muted/35 p-3 text-xs text-muted-foreground">{!!view.history_items && <span className="inline-flex items-center gap-1.5"><Layers className="size-3.5" />{english ? `${view.history_items} history items folded` : `已折叠 ${view.history_items} 条历史上下文`}</span>}{!!view.system_chars && <Button size="sm" variant="ghost" className="h-7 text-xs" onClick={() => onNavigate(`${base}/raw/instructions`)}>{english ? `Expand system instructions · ${view.system_chars} characters` : `展开系统指令 · ${view.system_chars} 字符`}<ArrowUpRight className="ml-1 size-3" /></Button>}{!!view.tool_definitions && <span>{english ? `${view.tool_definitions} tool definitions` : `${view.tool_definitions} 个工具定义`}</span>}</div>}
        <div className="space-y-3">{(view.entries ?? []).map((entry, index) => <div key={index} className="space-y-2"><MessageBubble role={toolEntryKind(entry) ? "tool" : entry.role} text={entry.text || ""} label={entry.label || entry.call_id} english={english} />{toolEntryKind(entry) && entry.call_id && <Button variant="outline" size="sm" onClick={() => onNavigate(`${base}/tools/${encodeURIComponent(entry.call_id!)}`)}><Terminal className="mr-1.5 size-3.5" />{displayText(entry.label || entry.call_id)} · {english ? "View complete call" : "查看完整调用"}<ArrowUpRight className="ml-1.5 size-3.5" /></Button>}{entry.truncated && <p className="text-xs text-muted-foreground">{english ? "This preview is truncated. Open the source inspector for the original payload." : "此处为截断预览，可在原始记录中查看完整数据。"}</p>}</div>)}{!(view.entries?.length) && <p className="rounded-xl border border-dashed p-8 text-center text-sm text-muted-foreground">{view.kind === "retry" ? (english ? "A retry with no new conversation content." : "这是一次重试，没有新增对话内容。") : (english ? "No readable text was captured." : "没有可直接阅读的文本内容。")}</p>}</div>
        {!!view.facets && <details className="rounded-lg border px-4 py-3 text-xs"><summary className="cursor-pointer text-muted-foreground">{english ? "Technical metadata" : "技术元数据"}</summary><dl className="mt-3 grid grid-cols-[minmax(100px,auto)_1fr] gap-x-5 gap-y-2">{Object.entries(view.facets).map(([name, values]) => <div key={name} className="contents"><dt className="break-all font-mono text-muted-foreground">{name}</dt><dd className="break-words">{values.join(", ")}</dd></div>)}</dl></details>}
        <Separator /><div><h3 className="mb-1 text-sm font-medium">{english ? "Original request / response (diagnostics)" : "原始请求/响应（排障）"}</h3><p className="mb-4 text-xs leading-6 text-muted-foreground">{english ? "Payloads are loaded only when opened. Original data may contain complete context." : "按需加载原始数据，用于排查请求、响应和上下文问题。"}</p><div className="grid gap-3 sm:grid-cols-3">{["request", "upstream", "response"].map((part) => <Card key={part} className="bg-muted/20"><CardContent className="p-4"><div className="mb-3 flex items-center gap-2 text-sm font-medium"><FileJson2 className="size-4 text-muted-foreground" />{rawLabels[part]}</div><Button size="sm" variant="outline" onClick={() => onNavigate(`${base}/raw/${part}`)}>{english ? "View and copy" : "查看并复制"}<ArrowUpRight className="ml-2 size-3.5" /></Button></CardContent></Card>)}</div></div>
      </div>
    </>}
  </section>;
}
