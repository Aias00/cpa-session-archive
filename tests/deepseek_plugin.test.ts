import assert from "node:assert/strict";
import test from "node:test";
import { observe, apply, ProcessSink, type Request, type Chunk, type Sink, type RecordMessage, type HarnessContext } from "../integrations/deepseek-harness/src/index.js";

const request: Request = Object.freeze({ provider: "fixture", model: "model", sessionId: "native-session", messages: Object.freeze([{ role: "user", content: Object.freeze([{ type: "text", text: "retained question" }]) }]) });
function fixture() { const records: RecordMessage[] = []; let closed = false; const sink: Sink = { submit: record => records.push(record), close: () => { closed = true; } }; return { records, sink, closed: () => closed }; }
function decode(record: RecordMessage, field: string) { return JSON.parse(Buffer.from(record.record[field] as string, "base64").toString()); }

test("DeepSeek observer preserves chunk identity, source and normalized tool correlation", async () => {
  const value = fixture();
  const chunks: Chunk[] = [
    { type: "text-delta", index: 0, text: "answer" },
    { type: "block-end", index: 0, block: { type: "text", text: "answer" } },
    { type: "block-end", index: 1, block: { type: "tool-call", id: "call", name: "shell", arguments: '{"cmd":"pwd"}' } },
    { type: "finish", reason: { kind: "tool-calls" } },
  ];
  const actual = [];
  for await (const chunk of observe(request, async function* () { yield* chunks; }, value.sink, 1 << 20)) actual.push(chunk);
  assert.equal(actual.length, chunks.length);
  actual.forEach((chunk, index) => assert.strictEqual(chunk, chunks[index]));
  assert.equal(value.records.length, 1);
  const record = value.records[0];
  assert.equal(record.record.session_id, "deepseek-harness:native-session");
  assert.equal(record.record.outcome, "succeeded");
  assert.equal(decode(record, "original_request").messages[0].content[0].text, "retained question");
  const reply = decode(record, "response");
  assert.deepEqual(reply.harness_stream, chunks);
  assert.equal(reply.choices[0].message.tool_calls[0].id, "call");
});

test("DeepSeek observer preserves errors and consumer cancellation when capture fails", async () => {
  const failure = new Error("synthetic downstream error");
  const value = fixture();
  await assert.rejects(async () => { for await (const _ of observe(request, async function* () { throw failure; }, value.sink, 4096)) {} }, error => error === failure);
  assert.equal(value.records[0].record.outcome, "failed");
  const cancelled = fixture();
  let returned = false;
  for await (const _ of observe(request, async function* () { try { yield { type: "text-delta", text: "one" }; yield { type: "text-delta", text: "two" }; } finally { returned = true; } }, cancelled.sink, 4096)) break;
  assert.equal(returned, true);
  assert.equal(cancelled.records[0].record.outcome, "cancelled");
  const broken: Sink = { submit() { throw new Error("capture unavailable"); }, close() {} };
  const output = [];
  for await (const chunk of observe(request, async function* () { yield { type: "finish", reason: { kind: "stop" } }; }, broken, 4096)) output.push(chunk);
  assert.equal(output.length, 1);
});

test("DeepSeek observer bounds retained data without cutting the model stream", async () => {
  const value = fixture();
  let count = 0;
  for await (const _ of observe(request, async function* () { for (let i = 0; i < 100; i++) yield { type: "text-delta", text: "x".repeat(100) }; yield { type: "finish", reason: { kind: "stop" } }; }, value.sink, 1024)) count++;
  assert.equal(count, 101);
  assert.equal(value.records[0].record.truncated, true);
  assert.ok(Buffer.from(value.records[0].record.response as string, "base64").length <= 1024);
});

test("DeepSeek plugin registers observation and disposes its transport", () => {
  const value = fixture();
  const handlers = new Map<string, unknown>();
  const ctx = { on(event: string, listener: unknown) { handlers.set(event, listener); }, effect(setup: () => () => void) { handlers.set("dispose", setup()); } } as HarnessContext;
  apply(ctx, { binary: "/synthetic/capture", spoolDir: "/synthetic/outbox" }, value.sink);
  assert.ok(handlers.has("llm/stream"));
  (handlers.get("dispose") as () => void)();
  assert.equal(value.closed(), true);
});

test("DeepSeek transport rejects unsafe destinations before starting a child", () => {
  assert.throws(() => new ProcessSink({ binary: "capture", spoolDir: "/synthetic/outbox" }));
  for (const endpoint of ["https://127.0.0.1", "http://example.com", "http://127.0.0.1/path", "http://user:password@127.0.0.1"]) {
    assert.throws(() => new ProcessSink({ binary: "/synthetic/capture", spoolDir: "/synthetic/outbox", endpoint }));
  }
});

test("DeepSeek companion startup failure does not change model output or leave cleanup pending", async () => {
  const sink = new ProcessSink({ binary: "/synthetic/nonexistent-capture-binary", spoolDir: "/synthetic/private-outbox" });
  const output = [];
  for await (const chunk of observe(request, async function* () { yield { type: "finish", reason: { kind: "stop" } }; }, sink, 4096)) output.push(chunk);
  assert.equal(output.length, 1);
  await sink.close();
  await sink.close();
});

test("DeepSeek tool schemas and observed failure metadata remain distinct from HTTP assumptions", async () => {
  const value = fixture();
  const tools = [{ name: "shell", description: "Run a synthetic command", parameters: { type: "object" } }];
  for await (const _ of observe({ ...request, tools }, async function* () { yield { type: "finish", reason: { kind: "stop" } }; }, value.sink, 4096)) {}
  assert.deepEqual(decode(value.records[0], "original_request").tools, [{ type: "function", function: tools[0] }]);
  assert.equal(value.records[0].record.status_code, 0);
  const failed = fixture();
  for await (const _ of observe(request, async function* () { yield { type: "finish", reason: { kind: "error", failure: { message: "synthetic limit", status: 429 } } }; }, failed.sink, 4096)) {}
  assert.equal(failed.records[0].record.outcome, "failed");
  assert.equal(failed.records[0].record.status_code, 429);
});
