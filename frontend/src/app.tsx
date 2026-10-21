import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowLeft, ArrowUpRight, Check, ChevronRight, Download, FolderGit2, Layers3, LoaderCircle, Menu, Moon, PanelRightClose, PanelRightOpen, RefreshCw, ShieldCheck, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { WorkspaceSidebar } from "@/components/workspace-sidebar";
import { SessionLibrary } from "@/components/session-library";
import { SessionEditor } from "@/components/session-editor";
import { SessionViewer } from "@/components/session-viewer";
import { createApi, hostConnection, validateConnection, type Connection } from "@/lib/api";
import { displayText } from "@/lib/utils";
import { emptyManagement, type ArchiveStats, type FacetCount, type LibraryView, type Locale, type ManagedSession, type ProjectFilter, type SessionManagement, type SessionSummary } from "@/lib/types";

interface Route { sessionID?: string; turnID?: string; requestID?: string; toolID?: string; rawPart?: string }
function readRoute(): Route {
  const parts = location.hash.replace(/^#\/?/, "").split("/").map((value) => {
    try { return decodeURIComponent(value); } catch { return ""; }
  });
  if (parts[0] !== "sessions" || !parts[1]) return {};
  const route: Route = { sessionID: parts[1] };
  let index = 2;
  if (parts[index] === "turns") { route.turnID = parts[index + 1]; index += 2; }
  if (parts[index] === "requests") { route.requestID = parts[index + 1]; index += 2; }
  if (parts[index] === "tools") route.toolID = parts[index + 1];
  if (parts[index] === "raw") route.rawPart = parts[index + 1];
  return route;
}
function language(): Locale {
  try {
    if (parent !== window && parent.document.documentElement.lang)
      return /^zh/i.test(parent.document.documentElement.lang) ? "zh" : "en";
  } catch { /* A cross-origin host cannot provide a language hint. */ }
  return /^zh/i.test(document.documentElement.lang || navigator.language) ? "zh" : "en";
}
function bytes(value: number) {
  if (!value) return "0 B";
  const unit = Math.min(4, Math.floor(Math.log(value) / Math.log(1024)));
  return `${(value / 1024 ** unit).toFixed(unit ? 1 : 0)} ${["B", "KiB", "MiB", "GiB", "TiB"][unit]}`;
}
const preview = document.querySelector('meta[name="cpa-preview"]')?.getAttribute("content") === "synthetic";

export function App() {
  const [locale, setLocale] = useState<Locale>(language);
  const zh = locale === "zh";
  const [connection, setConnection] = useState<Connection | null>(() => hostConnection() || (preview ? { base: location.origin, key: "mock-management-key" } : null));
  const api = useMemo(() => connection ? createApi(connection) : null, [connection]);
  const [route, setRoute] = useState<Route>(readRoute);
  const routeRef = useRef(route);
  routeRef.current = route;
  const acceptedHash = useRef(location.hash || "#/sessions");
  const dirty = useRef(false);
  const [theme, setTheme] = useState(() => {
    try { return localStorage.getItem("cpa-workspace-theme") === "dark" ? "dark" : "light"; } catch { return "light"; }
  });
  const [view, setView] = useState<LibraryView>("all");
  const [project, setProject] = useState<ProjectFilter>();
  const [mobileNav, setMobileNav] = useState(false);
  const [showEditor, setShowEditor] = useState(true);
  const [facets, setFacets] = useState<FacetCount[]>([]);
  const [stats, setStats] = useState<ArchiveStats>();
  const [aliases, setAliases] = useState<Record<string, string>>(() => Object.create(null) as Record<string, string>);
  const [cache, setCache] = useState<Record<string, ManagedSession>>({});
  const [refreshKey, setRefreshKey] = useState(0);
  const [loadKey, setLoadKey] = useState(0);
  const [connectionEpoch, setConnectionEpoch] = useState(0);
  const [metadataEpoch, setMetadataEpoch] = useState(0);
  const [loadError, setLoadError] = useState("");
  const [format, setFormat] = useState("archive");
  const [exportBusy, setExportBusy] = useState(false);
  const [exportError, setExportError] = useState("");
  const [downloads, setDownloads] = useState<Record<string, { url: string; filename: string }>>({});
  const iframe = useRef<HTMLIFrameElement>(null);

  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    try { localStorage.setItem("cpa-workspace-theme", theme); } catch { /* Theme still works without storage. */ }
  }, [theme]);
  useEffect(() => {
    const observer = new MutationObserver(() => setLocale(language()));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["lang"] });
    try { if (parent !== window) observer.observe(parent.document.documentElement, { attributes: true, attributeFilter: ["lang"] }); } catch { /* Cross-origin host. */ }
    return () => observer.disconnect();
  }, []);
  useEffect(() => { document.title = zh ? "会话工作台 · Session Archive" : "Session Workspace · Session Archive"; }, [zh]);
  useEffect(() => {
    const change = () => {
      const next = readRoute();
      if (dirty.current && next.sessionID !== routeRef.current.sessionID && !window.confirm(zh ? "有尚未保存的修改。放弃修改并离开？" : "Discard unsaved session changes and leave?")) {
        history.replaceState(null, "", acceptedHash.current);
        return;
      }
      acceptedHash.current = location.hash;
      setRoute(next);
    };
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, [zh]);
  useEffect(() => {
    if (!api) return;
    const abort = new AbortController();
    setLoadError("");
    void Promise.all([
      api<ArchiveStats>("/stats", { signal: abort.signal }),
      api<FacetCount[]>("/facets", { signal: abort.signal }),
      api<{ items?: { apiKeyHash: string; alias: string }[] }>("/v0/management/api-key-aliases", { signal: abort.signal }).catch(() => ({})),
      api<{ identities?: { principal_id: string; alias?: string; name?: string }[] }>("/v0/management/plugins/cpa-key-policy/identities", { signal: abort.signal }).catch(() => ({})),
    ]).then(([nextStats, nextFacets, keyData, principalData]) => {
      if (abort.signal.aborted) return;
      setStats(nextStats);
      setFacets(nextFacets);
      const nextAliases: Record<string, string> = Object.create(null) as Record<string, string>;
      if ("items" in keyData) keyData.items?.forEach((item) => { nextAliases[item.apiKeyHash.replace(/^sha256:/, "").toLowerCase()] = item.alias; });
      if ("identities" in principalData) principalData.identities?.forEach((item) => { if (item.alias || item.name) nextAliases[item.principal_id] = item.alias || item.name || ""; });
      setAliases(nextAliases);
    }).catch((error: unknown) => {
      if (!abort.signal.aborted) setLoadError(error && typeof error === "object" && "status" in error ? String(error.status) : "network");
    });
    return () => abort.abort();
  }, [api, loadKey]);

  const navigate = useCallback((path: string) => { location.hash = path.startsWith("#") ? path : `#${path}`; }, []);
  const identityName = useCallback((item: SessionSummary) => item.principal_alias || aliases[item.principal_id || ""] || aliases[(item.key_id || "").replace(/^sha256:/, "").toLowerCase()] || item.key_id?.slice(0, 14) || "—", [aliases]);
  const projects = useMemo(() => facets.filter((facet) => ["project.id", "project.name", "project.path", "workspace.id", "workspace.name", "workspace.path", "git.repository", "git.remote"].includes(facet.name)), [facets]);
  const selected = useMemo<ManagedSession | undefined>(() => route.sessionID ? Object.hasOwn(cache, route.sessionID) ? cache[route.sessionID] : { session_id: route.sessionID, requests: 0, last_at: "", management: emptyManagement(route.sessionID) } : undefined, [route.sessionID, cache]);
  const saveMetadata = useCallback((value: SessionManagement) => {
    setCache((current) => ({ ...current, [value.session_id]: { ...(Object.hasOwn(current, value.session_id) ? current[value.session_id] : { session_id: value.session_id, requests: 0, last_at: "" }), management: value } }));
    setRefreshKey((value) => value + 1);
  }, []);
  const bulkChanged = useCallback(() => {
    setCache((current) => dirty.current && routeRef.current.sessionID && Object.hasOwn(current, routeRef.current.sessionID) ? { [routeRef.current.sessionID]: current[routeRef.current.sessionID] } : {});
    setRefreshKey((value) => value + 1);
    setMetadataEpoch((value) => value + 1);
  }, []);
  function leaveDetail() {
    if (dirty.current && !window.confirm(zh ? "放弃尚未保存的修改？" : "Discard unsaved session changes?")) return false;
    dirty.current = false;
    navigate("/sessions");
    setMobileNav(false);
    return true;
  }
  function chooseProject(value?: ProjectFilter) { if (leaveDetail()) setProject(value); }
  function chooseView(value: LibraryView) { if (leaveDetail()) setView(value); }
  function changeConnection(value: Connection | null) {
    if (dirty.current && !window.confirm(zh ? "放弃尚未保存的修改并重新连接？" : "Discard unsaved changes and reconnect?")) return;
    dirty.current = false;
    setConnectionEpoch((current) => current + 1);
    setConnection(value);
    setCache({});
    setStats(undefined);
    setFacets([]);
    setAliases(Object.create(null) as Record<string, string>);
    setDownloads({});
    setExportError("");
    setLoadError("");
    setProject(undefined);
    setView("all");
    if (!value) navigate("/sessions");
  }
  async function download() {
    if (!api || !connection || exportBusy) return;
    setExportBusy(true);
    setExportError("");
    try {
      const query = new URLSearchParams({ scope: route.sessionID ? "session" : "all", format });
      if (route.sessionID) query.set("id", route.sessionID);
      const ticket = await api<{ url: string; filename: string }>(`/export?${query}`);
      const url = new URL(ticket.url, connection.base);
      if (url.origin !== new URL(connection.base).origin || !url.pathname.startsWith("/archive-api/v1/exports/")) throw new Error("invalid export ticket");
      const key = route.sessionID || "all";
      setDownloads((current) => ({ ...current, [key]: { url: url.href, filename: ticket.filename } }));
      if (iframe.current) iframe.current.src = url.href;
    } catch (error: unknown) {
      setExportError(error && typeof error === "object" && "status" in error ? String(error.status) : "network");
    } finally { setExportBusy(false); }
  }

  if (!connection || !api) return <Connect locale={locale} onConnect={changeConnection} />;
  const sidebar = <WorkspaceSidebar locale={locale} projects={projects} project={project} view={view} onProjectChange={chooseProject} onViewChange={chooseView} />;
  const downloadKey = route.sessionID || "all";
  const downloadResult = Object.hasOwn(downloads, downloadKey) ? downloads[downloadKey] : undefined;
  const title = selected?.management.title || displayText(selected?.summary || selected?.project || (zh ? "会话详情" : "Session details"));
  const overview = !route.turnID && !route.requestID;
  return <TooltipProvider delayDuration={200}><div className="workspace-shell grid h-dvh min-h-0 w-full grid-cols-1 overflow-hidden md:grid-cols-[232px_minmax(0,1fr)]">
    <div className="hidden min-h-0 md:block">{sidebar}</div>
    <Dialog open={mobileNav} onOpenChange={setMobileNav}><DialogContent className="left-0 top-0 flex h-dvh w-[280px] max-w-[85vw] translate-x-0 translate-y-0 flex-col gap-0 rounded-none border-0 p-0 sm:max-w-[280px]" aria-describedby="navigation-description"><DialogTitle className="sr-only">{zh ? "工作区导航" : "Workspace navigation"}</DialogTitle><DialogDescription id="navigation-description" className="sr-only">{zh ? "按项目、收藏与归档浏览会话。" : "Browse sessions by project, favorite or archive state."}</DialogDescription>{sidebar}</DialogContent></Dialog>
    <main className="workspace-main flex min-h-0 min-w-0 flex-col">
      <header className="flex h-16 shrink-0 items-center justify-between gap-2 border-b bg-background/85 px-4 sm:px-6">
        <div className="flex min-w-0 items-center gap-2 text-xs"><Button variant="ghost" size="icon" className="md:hidden" aria-label={zh ? "打开导航" : "Open navigation"} onClick={() => setMobileNav(true)}><Menu /></Button><span className="hidden size-6 items-center justify-center rounded-md border text-muted-foreground sm:flex"><Layers3 className="size-3.5" /></span><span className="truncate font-medium">{zh ? "会话工作台" : "Session workspace"}</span><ChevronRight className="hidden size-3 text-muted-foreground sm:block" /><span className="hidden truncate text-muted-foreground sm:block">{route.sessionID ? (zh ? "历史详情" : "Conversation") : (zh ? "会话库" : "Library")}</span>{preview && <Badge variant="outline" className="hidden text-[10px] lg:inline-flex">{zh ? "合成数据预览" : "Synthetic preview"}</Badge>}</div>
        <div className="flex shrink-0 items-center gap-1.5">
          <select id={route.sessionID ? "exportFormat" : "allExportFormat"} aria-label={zh ? "导出格式" : "Export format"} value={format} onChange={(event) => setFormat(event.target.value)} className="h-8 w-[82px] rounded-md border bg-background px-2 text-[11px]"><option value="archive">JSONL</option><option value="sft">SFT</option></select>
          <Button variant="outline" size="sm" onClick={() => void download()} disabled={exportBusy} className="h-8 gap-1.5 px-2 text-[11px]">{exportBusy ? <LoaderCircle className="size-3.5 animate-spin" /> : <Download className="size-3.5" />}<span>{route.sessionID ? (zh ? "下载当前 Session" : "Download this session") : (zh ? "下载全库" : "Download all")}</span></Button>
          <div className="mx-1 h-4 border-l" />
          <Tooltip><TooltipTrigger asChild><Button variant="ghost" size="icon" className="size-8" aria-label={zh ? "切换主题" : "Toggle theme"} onClick={() => setTheme((value) => value === "light" ? "dark" : "light")}>{theme === "light" ? <Moon className="size-4" /> : <Sun className="size-4" />}</Button></TooltipTrigger><TooltipContent>{zh ? "切换主题" : "Toggle theme"}</TooltipContent></Tooltip>
        </div>
      </header>
      {loadError && <div role="alert" className="flex flex-wrap items-center justify-between gap-2 border-b bg-destructive/5 px-5 py-3 text-xs"><span>{zh ? "连接失败，请检查 CPA 管理地址与凭据。" : "Could not connect. Check your CPA origin and credential."} ({loadError})</span><div className="flex gap-2"><Button size="sm" variant="outline" onClick={() => setLoadKey((value) => value + 1)}><RefreshCw className="size-3" />{zh ? "重试" : "Retry"}</Button><Button size="sm" variant="ghost" onClick={() => changeConnection(null)}>{zh ? "重新连接" : "Reconnect"}</Button></div></div>}
      {(downloadResult || exportError) && <div id={route.sessionID ? "downloadNotice" : "allDownloadNotice"} className="border-b bg-accent/30 px-5 py-2.5 text-xs" aria-live="polite">{exportError ? <span className="text-destructive">{zh ? "导出失败，请重试。" : "Export failed. Please retry."} ({exportError})</span> : <span className="flex min-w-0 items-center gap-2"><Check className="size-3.5 shrink-0 text-primary" /><span className="hidden text-muted-foreground sm:inline">{zh ? "下载已就绪，链接 30 分钟内可用：" : "Download ready. Link available for 30 minutes:"}</span><a href={downloadResult!.url} className="truncate font-medium text-primary underline underline-offset-4">{downloadResult!.filename}</a><ArrowUpRight className="size-3 shrink-0" /></span>}</div>}
      <div className={route.sessionID ? "hidden" : "flex min-h-0 flex-1 flex-col"} aria-hidden={!!route.sessionID}>
        {stats && <div className="flex shrink-0 items-center gap-5 border-b bg-muted/20 px-5 py-2 text-[10px] text-muted-foreground sm:px-8"><span><span className="mr-1.5 inline-block size-1.5 rounded-full bg-emerald-500" />{zh ? "已连接" : "Connected"}</span><span>{stats.sessions.toLocaleString()} {zh ? "个会话" : "sessions"}</span><span className="hidden sm:inline">{stats.records.toLocaleString()} {zh ? "条请求" : "requests"}</span><span className="ml-auto tabular-nums">{bytes(stats.compressed_bytes)}</span></div>}
        <SessionLibrary key={connectionEpoch} api={api} locale={locale} project={project} view={view} facets={facets} onSelect={(session) => { setCache((current) => ({ ...current, [session.session_id]: session })); navigate(`/sessions/${encodeURIComponent(session.session_id)}`); }} refreshKey={refreshKey} identityName={identityName} onBulkChanged={bulkChanged} />
      </div>
      {selected && <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex shrink-0 items-center gap-3 border-b px-4 py-3 sm:px-6"><Button variant="ghost" size="sm" className="h-8 gap-1.5 px-2 text-xs" onClick={leaveDetail}><ArrowLeft className="size-3.5" />{zh ? "返回会话列表" : "Back to sessions"}</Button><span className="h-4 border-l" /><div className="min-w-0 flex-1"><h1 id="detailTitle" className="truncate text-sm font-semibold" title={title}>{title}</h1><p className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">{selected.session_id}</p></div>{overview && <Button variant="ghost" size="icon" aria-label={zh ? "切换会话信息" : "Toggle session information"} className="size-8" onClick={() => setShowEditor((value) => !value)}>{showEditor ? <PanelRightClose className="size-4" /> : <PanelRightOpen className="size-4" />}</Button>}</div>
        <div className="workspace-body flex min-h-0 flex-1 flex-col overflow-auto lg:flex-row lg:overflow-hidden">
          <div className="min-h-0 min-w-0 shrink-0 p-4 sm:p-6 lg:flex-1 lg:overflow-auto"><SessionViewer api={api} locale={locale} {...route} sessionID={selected.session_id} onNavigate={navigate} identityName={identityName} /></div>
          <div className={overview && showEditor ? "shrink-0 border-t bg-muted/10 lg:w-[300px] lg:overflow-auto lg:border-l lg:border-t-0" : "hidden"}><SessionEditor refreshKey={metadataEpoch} api={api} locale={locale} session={selected} onSaved={saveMetadata} onDirtyChange={(value) => { dirty.current = value; }} /></div>
        </div>
      </div>}
    </main>
    <iframe ref={iframe} title="Archive export stream" className="hidden" />
  </div></TooltipProvider>;
}

function Connect({ locale, onConnect }: { locale: Locale; onConnect: (connection: Connection) => void }) {
  const zh = locale === "zh";
  const [base, setBase] = useState(location.origin);
  const [key, setKey] = useState("");
  const [error, setError] = useState(false);
  return <main className="flex min-h-dvh items-center justify-center bg-muted/35 p-6"><Card className="w-full max-w-[420px] shadow-xl shadow-foreground/5"><CardHeader className="space-y-3 p-7 pb-5"><div className="mb-3 flex size-11 items-center justify-center rounded-xl bg-primary text-primary-foreground"><Layers3 className="size-6" /></div><CardTitle className="text-xl tracking-tight">{zh ? "连接会话工作台" : "Connect your workspace"}</CardTitle><CardDescription className="text-xs leading-6">{zh ? "连接已有的 CPA 管理服务，浏览和整理你的项目会话。" : "Connect to your existing CPA management service to browse and organize project conversations."}</CardDescription></CardHeader><CardContent className="p-7 pt-0"><form className="space-y-5" onSubmit={(event) => { event.preventDefault(); try { onConnect(validateConnection({ base, key })); } catch { setError(true); } }}><div className="space-y-2"><Label htmlFor="base" className="text-xs">{zh ? "CPA 管理地址" : "CPA management origin"}</Label><Input id="base" type="url" value={base} onChange={(event) => setBase(event.target.value)} required autoComplete="url" placeholder="http://127.0.0.1:8317" /></div><div className="space-y-2"><Label htmlFor="key" className="text-xs">{zh ? "管理凭据" : "Management credential"}</Label><Input id="key" type="password" value={key} onChange={(event) => setKey(event.target.value)} required autoComplete="off" /></div>{error && <p role="alert" className="text-xs text-destructive">{zh ? "请输入有效的 HTTPS 或本机 HTTP 地址及管理凭据。" : "Enter a valid HTTPS or loopback HTTP origin and credential."}</p>}<Button className="w-full" type="submit">{zh ? "连接工作台" : "Connect workspace"}<ChevronRight className="ml-auto size-4" /></Button><p className="flex items-start gap-2 text-[10px] leading-5 text-muted-foreground"><ShieldCheck className="mt-0.5 size-3.5 shrink-0" />{zh ? "手动输入的凭据仅保存在当前页面内存中。" : "Manually entered credentials stay in this page's memory."}</p></form></CardContent></Card></main>;
}
