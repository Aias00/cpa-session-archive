export interface TurnSummary {
  turn_id: string;
  session_id: string;
  user_text?: string;
  final_text?: string;
  requests: number;
  first_at?: string;
  last_at?: string;
  principal_id?: string;
  principal_alias?: string;
  key_id?: string;
  model?: string;
  outcome?: string;
  status_code?: number;
  tool_names?: string[];
  has_compaction?: boolean;
}

export interface TimelineEntry {
  role: string;
  text?: string;
  label?: string;
  call_id?: string;
  kind?: string;
  truncated?: boolean;
}

export interface RequestView {
  request_id: string;
  session_id: string;
  summary?: string;
  requested_model?: string;
  model?: string;
  principal_alias?: string;
  principal_id?: string;
  key_id?: string;
  outcome?: string;
  status_code?: number;
  started_at?: string;
  kind?: string;
  history_items?: number;
  system_chars?: number;
  tool_definitions?: number;
  error?: string;
  entries?: TimelineEntry[];
  facets?: Record<string, string[]>;
}

export interface ArchiveRecord extends RequestView {
  original_request?: string | Record<string, unknown>;
  upstream_request?: string | Record<string, unknown>;
  response?: string | Record<string, unknown>;
}

export interface ToolEntry {
  callID: string;
  name: string;
  arguments: string;
  output: string;
}

export function toolEntryKind(entry: TimelineEntry): "call" | "result" | null {
  if (entry.role === "tool_call" || entry.kind === "tool_use") return "call";
  if (entry.role === "tool_result" || entry.role === "tool" || entry.kind === "tool_result") return "result";
  return null;
}

export function displayText(value: unknown): string {
  let text = String(value ?? "");
  for (let pass = 0; pass < 4; pass++) {
    const textarea = document.createElement("textarea");
    textarea.innerHTML = text;
    const decoded = textarea.value;
    if (decoded === text) break;
    text = decoded;
  }
  return text;
}

export function localTime(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function decodePayload(value: unknown): string {
  if (value == null) return "";
  if (typeof value !== "string") return JSON.stringify(value, null, 2);
  if (/^\s*[\[{]/.test(value)) return value;
  try {
    return new TextDecoder().decode(Uint8Array.from(atob(value), (char) => char.charCodeAt(0)));
  } catch {
    return value;
  }
}

export function parsePayload(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return null;
  }
}

export function prettyPayload(value: string): string {
  const parsed = parsePayload(value);
  return parsed == null ? value : JSON.stringify(parsed, null, 2);
}

export function contentText(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.map(contentText).filter(Boolean).join("\n");
  if (typeof value !== "object") return String(value);
  const object = value as Record<string, unknown>;
  if (/image|audio|video/.test(String(object.type ?? ""))) return `[${String(object.type)} attachment]`;
  const nested = object.text ?? object.output_text ?? object.input_text ?? object.output ?? object.content ?? object.summary;
  return nested == null ? JSON.stringify(object, null, 2) : contentText(nested);
}

export function extractTools(values: unknown[]): ToolEntry[] {
  const entries: ToolEntry[] = [];
  const byID = new Map<string, ToolEntry>();
  const objectValue = (value: unknown): Record<string, unknown> => value != null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
  const get = (id: string, name: string) => {
    const key = id || `anonymous:${name}:${entries.length}`;
    const existing = byID.get(key);
    if (existing) {
      if (name && !existing.name) existing.name = name;
      return existing;
    }
    const entry = { callID: id, name, arguments: "", output: "" };
    byID.set(key, entry);
    entries.push(entry);
    return entry;
  };
  const walk = (value: unknown) => {
    if (!value || typeof value !== "object") return;
    if (Array.isArray(value)) return value.forEach(walk);
    const object = value as Record<string, unknown>;
    const type = String(object.type ?? "").toLowerCase();
    const id = String(object.call_id ?? object.tool_call_id ?? object.callId ?? object.tool_use_id ?? object.id ?? "");
    const name = String(object.name ?? "");
    if (object.version === 1 && typeof object.client === "string" && Array.isArray(object.lines)) {
      object.lines.forEach((value: unknown) => {
        const line = objectValue(value);
        if (typeof line.raw === "string") walk(parsePayload(line.raw));
      });
    }
    if (object.version === 1 && typeof object.table === "string" && typeof object.data_raw === "string") {
      walk(parsePayload(object.data_raw));
      if (Array.isArray(object.parts)) object.parts.forEach((value: unknown) => {
        const part = objectValue(value);
        if (typeof part.data_raw === "string") walk(parsePayload(part.data_raw));
      });
    }
    if (["function_call", "custom_tool_call", "local_shell_call", "tool_use"].includes(type)) {
      get(id, name).arguments = contentText(object.arguments ?? object.input ?? object.content);
    }
    if (["function_call_output", "custom_tool_call_output", "function_call_result", "tool_result"].includes(type)) {
      get(id, name).output = contentText(object.output ?? object.content);
    }
    if (type === "tooluse" || type === "toolresult") {
      const data = objectValue(object.data);
      const entry = get(String(data.toolUseId ?? ""), String(data.name ?? ""));
      if (type === "tooluse") entry.arguments = contentText(data.input);
      else entry.output = contentText(data.content);
    }
    if (type === "tool" && object.state && typeof object.state === "object") {
      const state = object.state as Record<string, unknown>;
      const entry = get(String(object.callID ?? object.callId ?? object.id ?? ""), String(object.tool ?? object.name ?? ""));
      entry.arguments = contentText(state.input);
      entry.output = contentText(state.output ?? state.content);
    }
    if (Array.isArray(object.tool_calls)) {
      object.tool_calls.forEach((value: unknown) => {
        const raw = objectValue(value);
        const fn = objectValue(raw.function);
        get(String(raw.id ?? ""), String(fn.name ?? raw.name ?? "")).arguments = contentText(fn.arguments ?? raw.arguments);
      });
    }
    if (String(object.role ?? "").toLowerCase() === "tool") {
      get(id, name).output = contentText(object.content ?? object.output);
    }
    Object.entries(object).forEach(([key, child]) => {
      if (key !== "tools" && key !== "tool_calls") walk(child);
    });
  };
  values.forEach(walk);
  return entries.filter((entry) => entry.arguments || entry.output);
}

export function recordPayloads(record: ArchiveRecord): unknown[] {
  return [parsePayload(decodePayload(record.original_request)), parsePayload(decodePayload(record.response))];
}

export function routePath(sessionID: string, turnID?: string, requestID?: string): string {
  let path = `#/sessions/${encodeURIComponent(sessionID)}`;
  if (turnID) path += `/turns/${encodeURIComponent(turnID)}`;
  if (requestID) path += `/requests/${encodeURIComponent(requestID)}`;
  return path;
}

export function kindLabel(kind: string | undefined, english: boolean): string {
  const labels: Record<string, [string, string]> = {
    turn: ["对话", "Conversation"], retry: ["重试", "Retry"], compaction: ["上下文压缩", "Compaction"],
    prewarm: ["预热", "Prewarm"], history_snapshot: ["历史快照", "History snapshot"],
    context_rebuild: ["上下文重建", "Context rebuild"], tool_continuation: ["工具接续", "Tool continuation"],
  };
  return (labels[kind ?? "turn"] ?? [kind ?? "对话", kind ?? "Conversation"])[english ? 1 : 0];
}
