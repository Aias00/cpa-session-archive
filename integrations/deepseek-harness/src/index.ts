import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { randomUUID } from "node:crypto";
import { isAbsolute } from "node:path";

export const name = "session-archive";
export const inject = ["llm"];

// This is the read-only subset of the provider-neutral llm/stream contract.
// Actual Cordis dispatch is exercised separately against the host source.
interface Block { type: string; text?: string; id?: string; name?: string; arguments?: string; [key: string]: unknown }
interface Message { role: string; content: readonly Block[]; source?: { callId?: string }; [key: string]: unknown }
export interface Request {
  provider: string; model: string; messages: readonly Message[];
  system?: string; tools?: readonly unknown[]; sessionId?: string; purpose?: string;
  temperature?: number; maxTokens?: number; stop?: readonly string[]; toolHistory?: unknown;
}
export interface Chunk {
  type: string; index?: number; text?: string; block?: Block;
  reason?: { kind: string; failure?: { message?: string; status?: number } };
  [key: string]: unknown;
}
export interface HarnessContext {
  on(event: "llm/stream", listener: (request: Request, next: () => AsyncIterable<Chunk>) => AsyncIterable<Chunk>): unknown;
  effect(setup: () => (() => void | Promise<void>)): unknown;
}
export interface Config {
  binary: string;
  endpoint?: string;
  spoolDir: string;
  maxBodyBytes?: number;
  maxPendingBytes?: number;
}
export interface RecordMessage { version: 1; record: Record<string, unknown> }
export interface Sink { submit(message: RecordMessage): void; close(): void | Promise<void> }

function protocolMessage(message: Message): Record<string, unknown> {
  const content = message.content.filter(block => block.type !== "tool-call");
  const tool_calls = message.content.filter(block => block.type === "tool-call").map(block => ({
    id: block.id, type: "function", function: { name: block.name, arguments: block.arguments },
  }));
  return { role: message.role, content, ...(tool_calls.length ? { tool_calls } : {}),
    ...(message.role === "tool" ? { tool_call_id: message.source?.callId } : {}) };
}

function protocolTools(tools: readonly unknown[] | undefined): unknown {
  return tools?.map(tool => {
    if (!tool || typeof tool !== "object" || !("name" in tool)) return tool;
    const { name, description, parameters } = tool as Record<string, unknown>;
    return { type: "function", function: { name, description, parameters } };
  });
}

function response(chunks: Chunk[]): Record<string, unknown> {
  const blocks = new Map<number, Block>();
  const text = new Map<number, string>();
  for (const chunk of chunks) {
    const index = chunk.index ?? 0;
    if (chunk.type === "block-end" && chunk.block) blocks.set(index, chunk.block);
    if (chunk.type === "text-delta") text.set(index, (text.get(index) ?? "") + (chunk.text ?? ""));
  }
  for (const [index, value] of text) if (!blocks.has(index)) blocks.set(index, { type: "text", text: value });
  const content = [...blocks].sort(([a], [b]) => a - b).map(([, block]) => block);
  return { choices: [{ message: protocolMessage({ role: "assistant", content }) }], harness_stream: chunks };
}

// Capture failures cannot change the chunks, thrown errors or consumer cancellation.
export async function* observe(
  request: Request, next: () => AsyncIterable<Chunk>, sink: Sink, maxBodyBytes: number,
): AsyncIterable<Chunk> {
  const started = new Date().toISOString();
  const id = `deepseek-harness:${randomUUID()}`;
  let original: string | undefined;
  let bytes = 0, truncated = false, outcome = "cancelled", status = 0;
  let error: string | undefined;
  let finished = false;
  const chunks: Chunk[] = [];
  try {
    // AbortSignal and adapter/service objects are deliberately outside this envelope.
    const { provider, model, messages, system, tools, sessionId, purpose, temperature, maxTokens, stop, toolHistory } = request;
    const retained = { provider, model, messages, system, tools, sessionId, purpose, temperature, maxTokens, stop, toolHistory };
    const encoded = JSON.stringify({ messages: messages.map(protocolMessage), system, tools: protocolTools(tools), model, harness_request: retained });
    if (Buffer.byteLength(encoded) <= maxBodyBytes) original = encoded;
    else truncated = true;
  } catch { truncated = true; }
  try {
    for await (const chunk of next()) {
      try {
        const encoded = JSON.stringify(chunk);
        const size = Buffer.byteLength(encoded);
        if (bytes + size <= maxBodyBytes / 3) { chunks.push(JSON.parse(encoded) as Chunk); bytes += size; }
        else truncated = true;
        if (chunk.type === "finish") {
          finished = true;
          const kind = chunk.reason?.kind;
          outcome = kind === "error" ? "failed" : kind === "aborted" ? "cancelled" : "succeeded";
          status = chunk.reason?.failure?.status ?? 0;
          error = chunk.reason?.failure?.message;
        }
      } catch { truncated = true; }
      yield chunk;
    }
    if (!finished) outcome = "incomplete";
  } catch (failure) {
    outcome = "failed";
    // The original exception is rethrown; private exception text is not logged.
    throw failure;
  } finally {
    try {
      const reply = JSON.stringify(response(chunks));
      const retainedResponse = Buffer.byteLength(reply) <= maxBodyBytes ? reply : undefined;
      sink.submit({ version: 1, record: {
        request_id: id, session_id: request.sessionId ? `deepseek-harness:${request.sessionId}` : `request:${id}`,
        client: "deepseek-harness", source_format: "deepseek-harness-stream", model: request.model,
        requested_model: request.model, request_kind: request.purpose ?? "", stream: true,
        started_at: started, completed_at: new Date().toISOString(), outcome, status_code: status, error,
        original_request: original ? Buffer.from(original).toString("base64") : undefined,
        response: retainedResponse ? Buffer.from(retainedResponse).toString("base64") : undefined,
        truncated: truncated || !retainedResponse,
        facets: { "capture.host": ["deepseek-harness"], "capture.layer": ["provider-neutral"], "provider.target": [request.provider] },
      } });
    } catch { /* Archival observation never replaces the model outcome. */ }
  }
}

export class ProcessSink implements Sink {
  private child: ChildProcessWithoutNullStreams;
  private available = true;
  private closed = false;
  private stopped: Promise<void>;
  dropped = 0;
  constructor(config: Config) {
    if (!isAbsolute(config.binary) || !isAbsolute(config.spoolDir)) throw new Error("archive binary and spoolDir must be absolute paths");
    const endpoint = new URL(config.endpoint ?? "http://127.0.0.1:8318");
    if (endpoint.protocol !== "http:" || !["127.0.0.1", "[::1]"].includes(endpoint.hostname) || endpoint.username || endpoint.password || endpoint.pathname !== "/" || endpoint.search || endpoint.hash)
      throw new Error("archive endpoint must be a literal loopback HTTP origin");
    const max = config.maxBodyBytes ?? 64 * 1024 * 1024;
    if (!Number.isSafeInteger(max) || max < 1024 || max > 64 * 1024 * 1024) throw new Error("invalid archive body limit");
    this.maxPendingBytes = config.maxPendingBytes ?? 256 * 1024 * 1024;
    if (!Number.isSafeInteger(this.maxPendingBytes) || this.maxPendingBytes < 1024) throw new Error("invalid archive pending limit");
    this.child = spawn(config.binary, ["--endpoint", endpoint.origin, "--spool-dir", config.spoolDir, "--max-body-bytes", String(max)], { stdio: ["pipe", "pipe", "pipe"] });
    this.stopped = new Promise(resolve => this.child.once("close", () => resolve()));
    this.child.on("error", () => { this.available = false; });
    this.child.on("exit", () => { this.available = false; });
    this.child.stdin.on("error", () => { this.available = false; });
    // Drain bounded status and sanitized diagnostics without logging private records.
    this.child.stdout.resume(); this.child.stderr.resume();
  }
  private maxPendingBytes: number;
  submit(message: RecordMessage): void {
    if (this.closed || !this.available) { this.dropped++; return; }
    const line = JSON.stringify(message) + "\n";
    if (this.child.stdin.writableLength + Buffer.byteLength(line) > this.maxPendingBytes) { this.dropped++; return; }
    this.child.stdin.write(line);
  }
  close(): Promise<void> {
    if (this.closed) return this.stopped;
    this.closed = true;
    this.child.stdin.end();
    const timer = setTimeout(() => { this.child.kill("SIGTERM"); }, 6000);
    timer.unref();
    void this.stopped.then(() => clearTimeout(timer));
    return this.stopped;
  }
}

export function apply(ctx: HarnessContext, config: Config, sink: Sink = new ProcessSink(config)): void {
  ctx.effect(() => () => sink.close());
  ctx.on("llm/stream", (request, next) => observe(request, next, sink, config.maxBodyBytes ?? 64 * 1024 * 1024));
}
