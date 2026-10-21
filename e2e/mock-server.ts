import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { BulkJob, BulkSelection } from "../frontend/src/lib/types.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const port = Number(process.env.CPA_E2E_PORT || 4173);
if (!Number.isInteger(port) || port < 1024 || port > 65535) {
  throw new Error("CPA_E2E_PORT must be an integer from 1024 to 65535");
}
const origin = `http://127.0.0.1:${port}`;

const pagePath = path.join(__dirname, "..", "internal", "archive", "web", "index.html");
const sessionID = "mock-session-long-content";
const turnID = "mock-turn-long-content";
const requestID = "mock-request-long-content";
const escapedSummary =
  "calibre 路径 &amp;#34;C:\\Books&amp;#34; 应显示为正常引号";
const escapedEnvironment =
  "&amp;lt;environment_context&amp;gt;mock cwd&amp;lt;/environment_context&amp;gt;";
const longUser =
  "You are an expert at upholding safety and compliance standards for Codex ambient suggestions. " +
  "用户要求：详情页必须能够读取完整命令，列表可以只显示摘要。" +
  "LONG-USER-BODY ".repeat(700) +
  "END-OF-COMPLETE-USER-COMMAND";
const longSystem = (
  "系统指令必须按需加载，并允许查看全文。LONG-SYSTEM-CONTEXT ".repeat(500)
).slice(0, 17704) + "END-OF-SYSTEM-INSTRUCTIONS";
const longAssistant =
  "我会先说明处理结果，再展示必要的中间过程。" +
  "LONG-ASSISTANT-BODY ".repeat(280) +
  "END-OF-COMPLETE-ASSISTANT-ANSWER";
const longToolOutput =
  "命令执行结果：\n" +
  "TOOL-OUTPUT-LINE\n".repeat(7000) +
  "END-OF-LONG-TOOL-OUTPUT";
let turnTextPolls = 0;
const originalRequest = {
  model: "gpt-5.6-sol",
  instructions: longSystem,
  input: [
    {
      role: "user",
      content: [{ type: "input_text", text: longUser }],
    },
  ],
  tools: [
    {
      type: "function",
      name: "shell_command",
      description: "Run a command with deliberately long mock data.",
    },
  ],
};
const originalResponse = {
  output: [
    {
      type: "function_call",
      call_id: "call-long-output",
      name: "shell_command",
      arguments: JSON.stringify({
        command: "printf a deliberately long command",
      }),
    },
    {
      type: "function_call_output",
      call_id: "call-long-output",
      output: longToolOutput,
    },
    {
      type: "message",
      role: "assistant",
      content: [{ type: "output_text", text: longAssistant }],
    },
  ],
};
const encode = (value: unknown) =>
  Buffer.from(JSON.stringify(value), "utf8").toString("base64");
const requestRecord = {
  request_id: requestID,
  session_id: sessionID,
  key_id: "sha256:mock-key-hash",
  summary: escapedEnvironment,
  response_preview: longAssistant.slice(0, 160) + "…",
  requested_model: "gpt-5.6-sol",
  model: "gpt-5.6-sol",
  outcome: "succeeded",
  status_code: 200,
  started_at: "2026-08-03T12:34:56+08:00",
  completed_at: "2026-08-03T12:35:56+08:00",
  original_request: encode(originalRequest),
  upstream_request: "",
  response: encode(originalResponse),
  facets: {
    "project.name": ["mock-long-project"],
    client: ["codex"],
    "request.kind": ["turn"],
    "tool.name": ["shell_command"],
    "key.id": ["sha256:mock-key-hash"],
  },
};
const timeline = {
  request_id: requestID,
  session_id: sessionID,
  key_id: "sha256:mock-key-hash",
  summary: requestRecord.summary,
  requested_model: "gpt-5.6-sol",
  model: "gpt-5.6-sol",
  outcome: "succeeded",
  status_code: 200,
  started_at: requestRecord.started_at,
  completed_at: requestRecord.completed_at,
  kind: "turn",
  input_items: 1,
  history_items: 42,
  system_chars: longSystem.length,
  tool_definitions: 1,
  entries: [
    {
      role: "user",
      text: "<environment_context>mock cwd</environment_context>",
    },
    { role: "assistant", text: longAssistant, label: "analysis" },
    {
      role: "tool_call",
      label: "shell_command",
      call_id: "call-long-output",
    },
    {
      role: "tool_result",
      label: "shell_command",
      call_id: "call-long-output",
    },
  ],
};
const timelinePreview = {
  ...timeline,
  entries: [],
};
const sessionSummary = {
  session_id: sessionID,
  summary: escapedSummary,
  project: "mock-long-project",
  key_id: "sha256:mock-key-hash",
  model: "gpt-5.6-sol",
  requests: 23,
  last_at: "2026-08-03T12:35:56+08:00",
  thread_sources: ["user"],
  kinds: ["turn"],
};
let sessionMetadata = {
  session_id: sessionID,
  title: "",
  note: "",
  tags: [] as string[],
  starred: false,
  archived: false,
  revision: 0,
  updated_at: "",
};


let manuallyEdited = false;
const bulkJobs = new Map<string, { job: BulkJob; before: typeof sessionMetadata; afterRevision?: number }>();
function sessionMatches(query: URLSearchParams) {
  const archived = query.get("archived") || "exclude";
  const text = (query.get("q") || "").toLowerCase();
  const tag = query.get("tag");
  const view = query.get("view") || "all";
  const age = (Date.now() - new Date(sessionSummary.last_at).getTime()) / 86400000;
  const relevant = view === "all" || view === "main" || (view === "recent" && age <= 7) || (view === "inactive" && age >= Number(query.get("inactive_days") || 30));
  const exactFacets: Record<string, string> = { "project.name": sessionSummary.project, client: "codex", "tool.name": "shell_command", "key.id": "sha256:mock-key-hash" };
  return relevant && (archived === "all" || sessionMetadata.archived === (archived === "only")) &&
    (query.get("starred") !== "true" || sessionMetadata.starred) && (!tag || sessionMetadata.tags.includes(tag)) &&
    [...query.entries()].every(([name, value]) => ["q", "tag", "archived", "starred", "view", "inactive_days", "limit", "offset"].includes(name) || exactFacets[name] === value) &&
    (!text || [sessionSummary.session_id, sessionSummary.summary, sessionSummary.project, sessionMetadata.title, sessionMetadata.note, ...sessionMetadata.tags].some((value) => value.toLowerCase().includes(text)));
}
function selectionQuery(selection: BulkSelection) {
  const query = new URLSearchParams({ archived: selection.archived, view: selection.view, inactive_days: String(selection.inactive_days || 30) });
  if (selection.q) query.set("q", selection.q);
  if (selection.tag) query.set("tag", selection.tag);
  if (selection.starred) query.set("starred", "true");
  Object.entries(selection.filters || {}).forEach(([name, value]) => query.set(name, value));
  return query;
}
function patchedMetadata(value: typeof sessionMetadata, job: BulkJob) {
  return { ...value, tags: [...new Set([...value.tags.filter((tag) => !job.action.remove_tags?.includes(tag)), ...(job.action.add_tags || [])])],
    ...(job.action.archived !== undefined ? { archived: job.action.archived } : {}), ...(job.action.starred !== undefined ? { starred: job.action.starred } : {}) };
}

function json(response: http.ServerResponse, value: unknown, status = 200) {
  response.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
  });
  response.end(JSON.stringify(value));
}

const server = http.createServer((request, response) => {
  const url = new URL(request.url ?? "/", origin);
  if (url.pathname === "/healthz") return json(response, { ok: true });
  if (url.pathname === "/" || url.pathname === "/management.html") {
    response.writeHead(200, {
      "content-type": "text/html; charset=utf-8",
      "cache-control": "no-store",
    });
    const page = fs.readFileSync(pagePath, "utf8");
    return response.end(process.env.CPA_PREVIEW === "1"
      ? page.replace("<head>", '<head><meta name="cpa-preview" content="synthetic">')
      : page);
  }
  if (url.pathname === "/v0/management/api-key-aliases") {
    return json(response, {
      items: [
        { apiKeyHash: "sha256:mock-key-hash", alias: "长文本测试 Key" },
      ],
    });
  }
  const prefix = "/v0/management/plugins/cpa-session-archive";
  if (url.pathname === prefix + "/stats") {
    return json(response, {
      compressed_bytes: 987654321,
      sessions: 1,
      records: 23,
    });
  }
  if (url.pathname === prefix + "/facets") {
    return json(response, [
      {
        name: "project.name",
        value: "mock-long-project",
        sessions: 1,
        records: 23,
      },
      { name: "client", value: "codex", sessions: 1, records: 23 },
      {
        name: "tool.name",
        value: "shell_command",
        sessions: 1,
        records: 23,
      },
      {
        name: "key.id",
        value: "sha256:mock-key-hash",
        sessions: 1,
        records: 23,
      },
    ]);
  }
  if (url.pathname === prefix + "/sessions") {
    return json(response, [sessionSummary]);
  }
  if (url.pathname === prefix + "/session-browser") {
    const limit = Number(url.searchParams.get("limit") || 100);
    const offset = Number(url.searchParams.get("offset") || 0);
    const matched = sessionMatches(url.searchParams);
    const sessions = matched ? [{ ...sessionSummary, management: sessionMetadata }] : [];
    return json(response, {
      sessions: sessions.slice(offset, offset + limit),
      total: sessions.length,
      limit,
      offset,
    });
  }
  if (url.pathname === prefix + "/session-bulk" && request.method === "GET") {
    const id = url.searchParams.get("id");
    if (!id) return json(response, { jobs: [...bulkJobs.values()].map((entry) => entry.job).reverse().slice(0, 20) });
    const entry = bulkJobs.get(id);
    return entry ? json(response, entry.job) : json(response, { error: "job not found" }, 404);
  }
  if (["/session-bulk-preview", "/session-bulk-apply", "/session-bulk-undo"].some((suffix) => url.pathname === prefix + suffix) && request.method === "POST") {
    let body = "";
    request.setEncoding("utf8"); request.on("data", (chunk: string) => { body += chunk; });
    request.on("end", () => {
      try {
        const input = JSON.parse(body);
        if (url.pathname.endsWith("-preview")) {
          const selection = { limit: 0, offset: 0, q: "", tag: "", starred: false, ...input.selection } as BulkSelection;
          const total = Number(sessionMatches(selectionQuery(selection)));
          const protectedCount = Number(!!total && input.action.archived === true && ((!input.action.include_starred && sessionMetadata.starred) || (!input.action.include_edited && manuallyEdited)));
          const id = `mock-bulk-${bulkJobs.size + 1}`;
          const job: BulkJob = { id, status: "preview", selection, action: input.action, total, pending: total - protectedCount, protected: protectedCount, applied: 0, conflicts: 0, unchanged: 0, undone: 0, undo_conflicts: 0, cancelled: 0, created_at: new Date().toISOString(), updated_at: new Date().toISOString() };
          if (job.pending && JSON.stringify(patchedMetadata(sessionMetadata, job)) === JSON.stringify(sessionMetadata)) { job.pending = 0; job.unchanged = 1; }
          bulkJobs.set(id, { job, before: structuredClone(sessionMetadata) });
          return json(response, job);
        }
        const entry = bulkJobs.get(input.id);
        if (!entry) return json(response, { error: "job not found" }, 404);
        const { job } = entry;
        if (url.pathname.endsWith("-apply")) {
          if (job.pending) {
            if (sessionMetadata.revision !== entry.before.revision) job.conflicts++;
            else { sessionMetadata = { ...patchedMetadata(sessionMetadata, job), revision: sessionMetadata.revision + 1, updated_at: new Date().toISOString() }; job.applied++; entry.afterRevision = sessionMetadata.revision; }
            job.pending = 0;
          }
          job.status = "complete";
        } else {
          job.cancelled += job.pending; job.pending = 0;
          if (job.applied && !job.undone && !job.undo_conflicts) {
            if (sessionMetadata.revision !== entry.afterRevision) job.undo_conflicts++;
            else { sessionMetadata = { ...entry.before, revision: sessionMetadata.revision + 1, updated_at: new Date().toISOString() }; job.undone++; }
          }
          job.status = "undone";
        }
        job.updated_at = new Date().toISOString();
        return json(response, job);
      } catch { return json(response, { error: "invalid JSON" }, 400); }
    });
    return;
  }
  if (url.pathname === prefix + "/session-metadata") {
    if (request.method === "GET") return json(response, sessionMetadata);
    if (request.method === "PUT") {
      let body = "";
      request.setEncoding("utf8");
      request.on("data", (chunk: string) => { body += chunk; });
      request.on("end", () => {
        try {
          const update = JSON.parse(body);
          if (update.expected_revision !== sessionMetadata.revision) {
            return json(response, { error: "session metadata revision conflict" }, 409);
          }
          manuallyEdited = true;
          sessionMetadata = {
            session_id: sessionID,
            title: update.title,
            note: update.note,
            tags: update.tags,
            starred: update.starred,
            archived: update.archived,
            revision: sessionMetadata.revision + 1,
            updated_at: "2026-10-21T12:00:00Z",
          };
          return json(response, sessionMetadata);
        } catch {
          return json(response, { error: "invalid JSON" }, 400);
        }
      });
      return;
    }
  }
  if (url.pathname === prefix + "/turn-text") {
    turnTextPolls++;
    if (turnTextPolls === 1) {
      return json(response, { status: "building" });
    }
    return json(response, { status: "ok", text: longUser });
  }
  if (url.pathname === prefix + "/turns" && url.searchParams.has("turn_id")) {
    return json(response, {
      turn: {
        turn_id: turnID,
        session_id: sessionID,
        user_text: longUser.slice(0, 160) + "…",
        final_text: longAssistant,
        requests: 23,
        first_at: "2026-08-03T12:34:56+08:00",
        last_at: "2026-08-03T12:35:56+08:00",
        key_id: "sha256:mock-key-hash",
        model: "gpt-5.6-sol",
        outcome: "succeeded",
        status_code: 200,
        tool_names: ["shell_command"],
      },
      records: [timelinePreview],
      total: 41,
      limit: 10,
      offset: Number(url.searchParams.get("offset") || 0),
    });
  }
  if (url.pathname === prefix + "/turns") {
    return json(response, {
      turns: [
        {
          turn_id: turnID,
          session_id: sessionID,
          user_text: escapedEnvironment,
          final_text: longAssistant.slice(0, 160) + "…",
          requests: 23,
          first_at: "2026-08-03T12:34:56+08:00",
          last_at: "2026-08-03T12:35:56+08:00",
          key_id: "sha256:mock-key-hash",
          model: "gpt-5.6-sol",
          outcome: "succeeded",
          status_code: 200,
          tool_names: ["shell_command"],
        },
      ],
      total: 45,
      limit: 20,
      offset: Number(url.searchParams.get("offset") || 0),
    });
  }
  if (url.pathname === prefix + "/request-view") {
    return json(response, timeline);
  }
  if (url.pathname === prefix + "/requests") {
    return json(response, requestRecord);
  }
  if (url.pathname === prefix + "/request-context") {
    return json(response, [requestRecord]);
  }
  if (url.pathname === prefix + "/export") {
    const scope = url.searchParams.get("scope") || "session";
    const format = url.searchParams.get("format") || "archive";
    const filename =
      scope === "all"
        ? "cpa-session-archive.all." + format + ".jsonl"
        : sessionID + "." + format + ".jsonl";
    return json(response, {
      url: "/archive-api/v1/exports/mock-ticket",
      filename,
      content_type: "application/x-ndjson",
    });
  }
  if (url.pathname === "/archive-api/v1/exports/mock-ticket") {
    response.writeHead(200, {
      "content-type": "application/x-ndjson",
      "content-disposition":
        'attachment; filename="' + sessionID + '.archive.jsonl"',
    });
    return response.end(
      JSON.stringify({
        session_id: sessionID,
        request: originalRequest,
        response: originalResponse,
      }) + "\n",
    );
  }
  response.writeHead(404, { "content-type": "text/plain" });
  response.end("not found: " + url.pathname);
});

server.listen(port, "127.0.0.1");
