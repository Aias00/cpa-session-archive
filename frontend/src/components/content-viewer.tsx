import { useState } from "react";
import { Check, ChevronDown, Clipboard, AlertCircle, LoaderCircle, Terminal, UserRound, Sparkles } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { displayText } from "@/lib/conversation";

export function ViewerError({ error, english = false, onRetry }: { error: string; english?: boolean; onRetry?: () => void }) {
  return <div role="alert" className="flex items-center gap-3 rounded-xl border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive">
    <AlertCircle className="size-4 shrink-0" /><span className="min-w-0 flex-1 break-words">{error}</span>
    {onRetry && <Button variant="outline" size="sm" onClick={onRetry}>{english ? "Retry" : "重试"}</Button>}
  </div>;
}

export function ViewerLoading({ english = false }: { english?: boolean }) {
  return <div role="status" className="flex items-center justify-center gap-2 p-12 text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />{english ? "Loading conversation…" : "正在加载会话…"}</div>;
}

export function ExpandableText({ text, english = false, limit = 1600, preview = 1200, className = "" }: {
  text: string; english?: boolean; limit?: number; preview?: number; className?: string;
}) {
  const [expanded, setExpanded] = useState(false);
  const large = text.length > limit;
  return <div className={className}>
    <div className="whitespace-pre-wrap break-words text-[13px] leading-7 [overflow-wrap:anywhere]">{displayText(large && !expanded ? `${text.slice(0, preview)}…` : text)}</div>
    {large && <Button className="mt-3 h-8 text-xs" size="sm" variant="outline" onClick={() => setExpanded(!expanded)}><ChevronDown className={`mr-1.5 size-3.5 transition-transform ${expanded ? "rotate-180" : ""}`} />{expanded ? (english ? "Collapse" : "收起内容") : (english ? "Expand full content" : "展开完整内容")}</Button>}
  </div>;
}

export function PayloadViewer({ text, english = false }: { text: string; english?: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const [copyState, setCopyState] = useState<"idle" | "copied" | "error">("idle");
  const limit = 100000;
  const copy = async () => {
    try { await navigator.clipboard.writeText(text); setCopyState("copied"); }
    catch { setCopyState("error"); }
  };
  return <div className="min-w-0 overflow-hidden rounded-xl border border-border/70 bg-muted/30">
    <div className="flex items-center justify-between border-b border-border/60 px-4 py-2">
      <span className="font-mono text-[11px] text-muted-foreground">{new Blob([text]).size.toLocaleString()} {english ? "bytes" : "字节"}</span>
      <Button size="sm" variant="ghost" className="h-7 gap-1.5 text-xs" onClick={copy}>{copyState === "copied" ? <Check className="size-3" /> : <Clipboard className="size-3" />}{copyState === "copied" ? (english ? "Copied" : "已复制") : copyState === "error" ? (english ? "Copy failed" : "复制失败") : (english ? "Copy" : "复制")}</Button>
    </div>
    <pre className="diagnostic-content max-h-[65vh] overflow-auto p-4 font-mono text-xs leading-6 whitespace-pre-wrap break-words [overflow-wrap:anywhere]">{expanded ? text : text.slice(0, limit)}{!expanded && text.length > limit ? "\n…" : ""}</pre>
    {!expanded && text.length > limit && <div className="border-t p-3"><Button size="sm" variant="outline" onClick={() => setExpanded(true)}><ChevronDown className="mr-1.5 size-3" />{english ? "Expand full content" : "展开完整内容"}</Button></div>}
  </div>;
}

export function MessageBubble({ role, text, label, english = false }: { role: string; text: string; label?: string; english?: boolean }) {
  if (!text) return null;
  if (role === "system" || role === "developer") {
    return <Collapsible className="rounded-xl border bg-muted/30">
      <CollapsibleTrigger className="flex w-full items-center justify-between p-4 text-xs text-muted-foreground"><span>{english ? "System context" : "系统上下文"} · {text.length.toLocaleString()} {english ? "characters" : "字符"}</span><ChevronDown className="size-4" /></CollapsibleTrigger>
      <CollapsibleContent className="px-4 pb-4"><ExpandableText text={text} english={english} /></CollapsibleContent>
    </Collapsible>;
  }
  const isTool = role === "tool" || role === "tool_call" || role === "tool_result";
  const isUser = role === "user";
  const roleLabel = isTool ? (english ? "Tool" : "工具") : isUser ? (english ? "You" : "用户") : role === "compaction" ? (english ? "Compaction" : "上下文压缩") : (english ? "Assistant" : "助手");
  const Icon = isTool ? Terminal : isUser ? UserRound : Sparkles;
  return <div className={`message-bubble ${role} flex gap-3 rounded-xl border p-4 ${isUser ? "border-primary/10 bg-primary/[0.035]" : isTool ? "border-border/60 bg-muted/35" : "border-border/60 bg-card"}`}>
    <span className={`flex size-7 shrink-0 items-center justify-center rounded-lg ${isUser ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground"}`}><Icon className="size-3.5" /></span>
    <div className="min-w-0 flex-1"><div className="mb-2 flex items-center gap-2 text-xs font-medium">{roleLabel}{label && <Badge variant="secondary" className="max-w-full truncate font-mono text-[10px]">{displayText(label)}</Badge>}</div><ExpandableText text={text} english={english} limit={4000} preview={1400} /></div>
  </div>;
}
