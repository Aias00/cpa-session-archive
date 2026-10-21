import assert from "node:assert/strict";
import { test } from "node:test";
import { decodePayload, extractTools, recordPayloads, toolEntryKind } from "../../frontend/src/lib/conversation.js";

test("Responses tool calls pair across input and output records by exact call ID", () => {
  const entries = extractTools([
    { output: [
      { type: "function_call", call_id: "12", name: "shell", arguments: '{"command":"pwd"}' },
      { type: "custom_tool_call", call_id: "call-b", name: "patch", input: "fixture patch" },
    ] },
    { input: [
      { type: "custom_tool_call_output", call_id: "call-b", output: "patched" },
      { type: "function_call_output", call_id: "12", output: "fixture project" },
    ] },
  ]);
  assert.deepEqual(entries, [
    { callID: "12", name: "shell", arguments: '{"command":"pwd"}', output: "fixture project" },
    { callID: "call-b", name: "patch", arguments: "fixture patch", output: "patched" },
  ]);
});

test("Codex native envelopes expose retained JSON strings without inventing HTTP messages", () => {
  const native = {
    version: 1,
    client: "codex",
    lines: [
      { offset: 0, raw: JSON.stringify({ type: "response_item", payload: { type: "function_call", call_id: "call-native", name: "shell", arguments: '{"command":"pwd"}' } }) + "\n" },
      { offset: 200, raw: JSON.stringify({ type: "response_item", payload: { type: "function_call_output", call_id: "call-native", output: "native fixture" } }) + "\n" },
    ],
  };
  const original = JSON.stringify(native);
  assert.deepEqual(extractTools([native]), [
    { callID: "call-native", name: "shell", arguments: '{"command":"pwd"}', output: "native fixture" },
  ]);
  assert.equal(JSON.stringify(native), original);
});

test("Claude-compatible native content pairs tool_use and tool_result blocks", () => {
  const native = {
    version: 1,
    client: "claude-code",
    lines: [{ raw: JSON.stringify({ message: { content: [
      { type: "tool_use", id: "call-block", name: "read_file", input: { path: "fixture.txt" } },
      { type: "tool_result", tool_use_id: "call-block", content: [{ type: "text", text: "fixture contents" }] },
    ] } }) }],
  };
  const [entry] = extractTools([native]);
  assert.equal(entry.callID, "call-block");
  assert.equal(entry.name, "read_file");
  assert.deepEqual(JSON.parse(entry.arguments), { path: "fixture.txt" });
  assert.equal(entry.output, "fixture contents");
});

test("OpenCode database envelopes decode retained data_raw tool parts", () => {
  const native = {
    version: 1,
    client: "opencode",
    lines: [{ raw: JSON.stringify({
      version: 1,
      table: "message",
      data_raw: '{"role":"assistant"}',
      parts: [{ id: "part-fixture", data_raw: JSON.stringify({ type: "tool", callID: "call-db", tool: "shell", state: { input: { command: "pwd" }, output: "database fixture" } }) }],
    }) }],
  };
  const [entry] = extractTools([native]);
  assert.equal(entry.callID, "call-db");
  assert.equal(entry.name, "shell");
  assert.deepEqual(JSON.parse(entry.arguments), { command: "pwd" });
  assert.equal(entry.output, "database fixture");
});

test("Chat Completions tool calls pair with their later tool-role results", () => {
  const [entry] = extractTools([{ messages: [
    { role: "assistant", tool_calls: [{ id: "call-chat", type: "function", function: { name: "lookup", arguments: '{"id":7}' } }] },
    { role: "tool", tool_call_id: "call-chat", content: "lookup result" },
  ] }]);
  assert.deepEqual(entry, { callID: "call-chat", name: "lookup", arguments: '{"id":7}', output: "lookup result" });
});

test("tool definitions and malformed auxiliary native content are not rendered as executed calls", () => {
  assert.deepEqual(extractTools([{
    version: 1,
    client: "codex",
    lines: [null, 7, { raw: "malformed retained fixture" }],
    tools: [{ type: "function_call", id: "definition", name: "unused", arguments: "not executed" }],
    tool_calls: [null, false, {}],
  }]), []);
});

test("UTF-8 base64 payloads preserve Unicode and literal markup for source inspection", () => {
  const text = '{"message":"你好 🐈 <environment_context>&amp;</environment_context>"}';
  const encoded = Buffer.from(text, "utf8").toString("base64");
  assert.equal(decodePayload(encoded), text);
  assert.deepEqual(recordPayloads({ request_id: "fixture-request", session_id: "fixture-session", original_request: encoded, response: { output: [] } }), [JSON.parse(text), { output: [] }]);
  assert.equal(decodePayload(""), "");
});

test("native kinds and proxy roles use the same tool-link classification", () => {
  assert.equal(toolEntryKind({ role: "assistant", kind: "tool_use" }), "call");
  assert.equal(toolEntryKind({ role: "tool", kind: "tool_result" }), "result");
  assert.equal(toolEntryKind({ role: "tool_call" }), "call");
  assert.equal(toolEntryKind({ role: "tool_result" }), "result");
  assert.equal(toolEntryKind({ role: "assistant", kind: "text" }), null);
});
