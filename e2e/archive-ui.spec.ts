import { statSync, readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";

type SessionMetadata = {
  session_id: string;
  title: string;
  note: string;
  tags: string[];
  starred: boolean;
  archived: boolean;
  revision: number;
  updated_at: string;
};

type BrowserSession = {
  session_id: string;
  summary: string;
  project: string;
  key_id: string;
  model: string;
  requests: number;
  last_at: string;
  thread_sources: string[];
  kinds: string[];
  management: SessionMetadata;
};

function managedSession(
  sessionID = "managed-session",
  metadata: Partial<SessionMetadata> = {},
): BrowserSession {
  return {
    session_id: sessionID,
    summary: "Original captured conversation",
    project: "mock-long-project",
    key_id: "sha256:mock-key-hash",
    model: "gpt-5.6-sol",
    requests: 23,
    last_at: "2026-10-21T10:00:00Z",
    thread_sources: ["user"],
    kinds: ["turn"],
    management: {
      session_id: sessionID,
      title: "Project planning",
      note: "",
      tags: [],
      starred: false,
      archived: false,
      revision: 0,
      updated_at: "",
      ...metadata,
    },
  };
}

async function installSessionManager(
  page: Page,
  initialSessions = [managedSession()],
) {
  const sessions = structuredClone(initialSessions);
  const browserQueries: URLSearchParams[] = [];
  const writes: Record<string, unknown>[] = [];
  let conflictNextWrite = false;
  await page.route(
    /\/cpa-session-archive\/(session-browser|session-metadata)(?:\?|$)/,
    async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const reply = (value: unknown, status = 200) => route.fulfill({
        status,
        contentType: "application/json; charset=utf-8",
        body: JSON.stringify(value),
      });
      if (url.pathname.endsWith("/session-browser")) {
        const query = url.searchParams;
        browserQueries.push(new URLSearchParams(query));
        const limit = Number(query.get("limit") || 100);
        const offset = Number(query.get("offset") || 0);
        const archived = query.get("archived") || "exclude";
        const q = (query.get("q") || "").toLowerCase();
        const matches = sessions.filter((session) => {
          const metadata = session.management;
          return (
            (archived === "all" || metadata.archived === (archived === "only")) &&
            (query.get("starred") !== "true" || metadata.starred) &&
            (!query.get("tag") || metadata.tags.includes(query.get("tag")!)) &&
            (!query.get("project.name") || query.get("project.name") === session.project) &&
            (!q || [session.session_id, session.summary, session.project, metadata.title, metadata.note,
              ...metadata.tags].some((value) => value.toLowerCase().includes(q)))
          );
        });
        return reply({
          sessions: matches.slice(offset, offset + limit),
          total: matches.length,
          limit,
          offset,
        });
      }
      const session = sessions.find((item) => item.session_id === url.searchParams.get("session_id"));
      if (!session) return reply({ error: "session not found" }, 404);
      if (request.method() === "GET") return reply(session.management);
      const update = request.postDataJSON() as Record<string, unknown>;
      writes.push(update);
      if (conflictNextWrite) {
        conflictNextWrite = false;
        session.management.title = "Changed in another window";
        session.management.revision++;
      }
      if (update.expected_revision !== session.management.revision) {
        return reply({ error: "session metadata revision conflict" }, 409);
      }
      session.management = {
        session_id: session.session_id,
        title: update.title as string,
        note: update.note as string,
        tags: update.tags as string[],
        starred: update.starred as boolean,
        archived: update.archived as boolean,
        revision: session.management.revision + 1,
        updated_at: "2026-10-21T12:00:00Z",
      };
      return reply(session.management);
    },
  );
  await page.reload();
  await expect(page.locator("#rows .conversation-title").first()).toContainText(sessions[0].management.title);
  return {
    sessions,
    writes,
    browserQueries,
    conflictNextWrite: () => { conflictNextWrite = true; },
  };
}

async function openFilters(page: Page) {
  await page.getByRole("button", { name: /^筛选(?:\s*\d+)?$/ }).click();
  await expect(page.getByRole("dialog", { name: "筛选会话", exact: true })).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem(
      "cli-proxy-auth",
      JSON.stringify({
        state: {
          apiBase: location.origin,
          managementKey: "mock-management-key",
        },
      }),
    );
  });
  await page.goto("/#/sessions");
  await expect(page.locator(".key-pill").first()).toHaveText("长文本测试 Key");
});

test("long previews remain lightweight while turn details expose full content", async ({
  page,
}) => {
  await expect(page.getByRole("navigation", { name: "项目筛选" })).toBeVisible();
  await expect(page.locator("#rows .conversation-meta")).toContainText(
    "mock-long-project",
  );
  await page.screenshot({ path: test.info().outputPath("desktop-session-library.png") });

  await page.locator("#rows .conversation-title").first().click();
  await expect(page.locator(".turn-card")).toBeVisible();
  await expect(page.locator("#sessionTitleInput")).toBeEnabled();
  await page.screenshot({ path: test.info().outputPath("desktop-session-details.png") });
  await expect(page.locator(".turn-card")).not.toContainText(
    "END-OF-COMPLETE-USER-COMMAND",
  );

  await page.locator(".turn-card").click();
  await expect(page).toHaveURL(/\/turns\/mock-turn-long-content$/);
  const primary = page.locator("#turnPrimary");
  await expect(primary).not.toContainText("END-OF-COMPLETE-USER-COMMAND");
  await primary
    .getByRole("button", { name: "展开完整内容" })
    .first()
    .click();
  await expect(primary).toContainText("END-OF-COMPLETE-USER-COMMAND");
  await primary
    .locator(".turn-final")
    .getByRole("button", { name: "展开完整内容" })
    .click();
  await expect(primary).toContainText("END-OF-COMPLETE-ASSISTANT-ANSWER");

  await page
    .locator("#turnPagination")
    .getByRole("button", { name: "下一页" })
    .click();
  await expect(page.locator("#turnPageInfo")).toContainText("第 2 / 5 页");
  await page
    .locator("#turnPagination")
    .getByRole("button", { name: "上一页" })
    .click();
  await expect(page.locator("#turnPageInfo")).toContainText("第 1 / 5 页");

  await page.locator(".process-step").first().click();
  await expect(page.getByText("shell_command · 查看完整调用")).toBeVisible();
  await page.getByText("shell_command · 查看完整调用").click();
  await expect(page).toHaveURL(/\/tools\/call-long-output$/);
  await expect(page.locator("#subDetail")).not.toContainText(
    "END-OF-LONG-TOOL-OUTPUT",
  );
  await page
    .locator("#subDetail .tool-card")
    .last()
    .getByRole("button", { name: "展开完整内容" })
    .click();
  await expect(page.locator("#subDetail")).toContainText(
    "END-OF-LONG-TOOL-OUTPUT",
  );
});

test("system instructions and diagnostics are routed and loaded on demand", async ({
  page,
}) => {
  await page.locator("#rows .conversation-title").first().click();
  await page.locator(".turn-card").click();
  await page.locator(".process-step").first().click();
  await page.getByRole("button", { name: "原始请求/响应（排障）" }).click();
  await expect(page).toHaveURL(/\/requests\/mock-request-long-content$/);

  await page
    .getByRole("button", { name: /展开系统指令.*17730 字符/ })
    .click();
  await expect(page).toHaveURL(/\/raw\/instructions$/);
  await expect(page.locator("#subDetail")).toContainText(
    "END-OF-SYSTEM-INSTRUCTIONS",
  );
  await page.getByRole("button", { name: /返回本次请求/ }).click();
  await expect(page).toHaveURL(/\/requests\/mock-request-long-content$/);
  await page.getByRole("button", { name: "查看并复制" }).first().click();
  await expect(page).toHaveURL(/\/raw\/request$/);
  await expect(page.locator("#subDetail")).toContainText(
    "END-OF-COMPLETE-USER-COMMAND",
  );
});

test("session export exposes a reusable server-streamed JSONL download", async ({
  page,
}) => {
  await page.locator("#rows .conversation-title").first().click();
  const sessionDownload = page.waitForEvent("download");
  await page.getByRole("button", { name: "下载当前 Session" }).click();
  const downloaded = await sessionDownload;
  expect(downloaded.suggestedFilename()).toBe(
    "mock-session-long-content.archive.jsonl",
  );
  const downloadedPath = await downloaded.path();
  expect(downloadedPath).toBeTruthy();
  expect(statSync(downloadedPath).size).toBeGreaterThan(0);
  expect(readFileSync(downloadedPath, "utf8")).toContain(
    "END-OF-LONG-TOOL-OUTPUT",
  );
  const link = page.locator("#downloadNotice a");
  await expect(link).toHaveAttribute(
    "href",
    new URL("/archive-api/v1/exports/mock-ticket", page.url()).href,
  );
  await expect(link).toHaveText("mock-session-long-content.archive.jsonl");

  await page.getByRole("button", { name: /返回会话列表/ }).click();
  await page.locator("#allExportFormat").selectOption("sft");
  const allDownload = page.waitForEvent("download");
  await page.getByRole("button", { name: "下载全库" }).click();
  expect((await allDownload).suggestedFilename()).toBe(
    "mock-session-long-content.archive.jsonl",
  );
  await expect(page.locator("#allDownloadNotice a")).toHaveText(
    "cpa-session-archive.all.sft.jsonl",
  );
});

test("escaped environment context is readable and process content loads lazily", async ({
  page,
}) => {
  const detailRequests: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (/\/(request-view|requests)$/.test(url.pathname)) {
      detailRequests.push(url.pathname);
    }
  });
  await expect(page.locator("#rows .conversation-title").first()).toContainText(
    'calibre 路径 "C:\\Books" 应显示为正常引号',
  );
  await page.locator("#rows .conversation-title").first().click();
  await expect(page.locator(".turn-card").first()).toContainText(
    "<environment_context>mock cwd</environment_context>",
  );
  await page.locator(".turn-card").click();
  const step = page.locator(".process-step").first();
  await expect(step).toContainText("展开后加载完整过程正文");
  expect(detailRequests).toEqual([]);
  const loadProcess = page.waitForRequest((request) =>
    new URL(request.url()).pathname.endsWith("/request-view"),
  );
  await step.click();
  await loadProcess;
  await expect(step).not.toContainText("没有可直接阅读的文本内容");
  expect(detailRequests.some((url) => url.endsWith("/requests"))).toBe(false);
  await step.getByRole("button", { name: "原始请求/响应（排障）" }).click();
  await expect(page.locator("#requestTitle")).toContainText(
    "<environment_context>mock cwd</environment_context>",
  );
  await expect(page.locator("#requestDetail")).toContainText(
    "<environment_context>mock cwd</environment_context>",
  );
});

test("native archive events open the exact tool call and preserve its complete output", async ({ page }) => {
  const sessionID = "mock-session-long-content";
  const requestID = "mock-request-long-content";
  const snapshot = {
    version: 1,
    client: "codex",
    lines: [
      { type: "function_call", call_id: "other", name: "decoy_tool", arguments: "false" },
      { type: "function_call_output", call_id: "other", output: "DO-NOT-SELECT-DECOY" },
      { type: "function_call", call_id: "007", name: "native_shell", arguments: '{"command":"echo native"}' },
      { type: "function_call_output", call_id: "007", output: "NATIVE-TOOL-OUTPUT-EXACT-ID" },
    ].map((payload) => ({ raw: JSON.stringify({ type: "response_item", payload }) })),
  };
  await page.route("**/cpa-session-archive/request-view?**", (route) => route.fulfill({
    json: {
      request_id: requestID,
      session_id: sessionID,
      summary: "Native tool call",
      kind: "turn",
      entries: [
        { role: "assistant", kind: "tool_use", call_id: "007", label: "native_shell" },
        { role: "tool", kind: "tool_result", call_id: "007", label: "native_shell" },
      ],
    },
  }));
  await page.route("**/cpa-session-archive/requests?**", (route) => route.fulfill({
    json: {
      request_id: requestID,
      session_id: sessionID,
      original_request: Buffer.from(JSON.stringify(snapshot)).toString("base64"),
    },
  }));
  await page.locator("#rows .conversation-title").first().click();
  await page.locator(".turn-card").first().click();
  await page.locator(".process-step").first().click();
  await page.getByRole("button", { name: "native_shell · 查看完整调用" }).click();
  await expect(page).toHaveURL(/\/tools\/007$/);
  await expect(page.locator("#subDetail")).toContainText("echo native");
  await expect(page.locator("#subDetail")).toContainText("NATIVE-TOOL-OUTPUT-EXACT-ID");
  await expect(page.locator("#subDetail")).not.toContainText("DO-NOT-SELECT-DECOY");
});

test("facets, i18n, deep links, and back navigation remain interactive", async ({
  page,
}) => {
  let appliedProjectRequests = 0;
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname.endsWith("/session-browser") && url.searchParams.has("project.name")) appliedProjectRequests++;
  });
  await openFilters(page);
  await page.locator("#facetSearch").fill("project");
  await expect(page.locator('[data-facet="project.name"]')).toBeVisible();
  await expect(page.locator('[data-facet="client"]')).toHaveCount(0);
  await page.locator("#facetSearch").fill("");
  await page.locator('[data-facet="project.name"]').selectOption(
    "mock-long-project",
  );
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "筛选会话" })).toHaveCount(0);
  await expect(page.locator("#activeCount")).toHaveText("0");
  expect(appliedProjectRequests).toBe(0);
  await openFilters(page);
  await expect(page.locator('[data-facet="project.name"]')).toHaveValue("mock-long-project");
  const filteredRequest = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return url.pathname.endsWith("/session-browser") && url.searchParams.get("project.name") === "mock-long-project";
  });
  await page.getByRole("button", { name: "应用筛选" }).click();
  await filteredRequest;
  await expect(page.getByRole("dialog", { name: "筛选会话" })).toHaveCount(0);
  await expect(page.locator("#activeCount")).toHaveText("1");
  await expect(page.locator("#rows .conversation-title")).toHaveCount(1);
  await openFilters(page);
  await page.getByRole("button", { name: "重置筛选", exact: true }).click();
  await expect(page.locator("#activeCount")).toHaveText("0");
  await page.getByRole("button", { name: "应用筛选" }).click();

  await page.evaluate(() => {
    document.documentElement.lang = "en";
  });
  await expect(page.getByRole("heading", { name: "All sessions", exact: true })).toBeVisible();
  await page.evaluate(() => {
    document.documentElement.lang = "zh-CN";
  });
  await expect(page.getByRole("heading", { name: "全部会话", exact: true })).toBeVisible();

  await page.goto(
    "/#/sessions/mock-session-long-content/turns/mock-turn-long-content",
  );
  await expect(page.getByRole("button", { name: /返回当前会话/ })).toBeVisible();
  await page.getByRole("button", { name: /返回当前会话/ }).click();
  await expect(page).toHaveURL(/\/sessions\/mock-session-long-content$/);
  await page.getByRole("button", { name: /返回会话列表/ }).click();
  await expect(page).toHaveURL(/#\/sessions$/);
  await expect(page.getByRole("region", { name: "会话库", exact: true })).toBeVisible();
});

test("session pagination shows loading feedback and submits the requested page and order", async ({
  page,
}) => {
  let delayed = false;
  const queries: URLSearchParams[] = [];
  await page.route("**/cpa-session-archive/turns?**", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (!query.has("turn_id")) queries.push(query);
    if (!delayed) {
      delayed = true;
      await new Promise((resolve) => setTimeout(resolve, 600));
    }
    await route.continue();
  });
  await page.locator("#rows .conversation-title").first().click();
  await expect(page.getByRole("status").filter({ hasText: "正在加载会话" })).toBeVisible();
  await expect(page.locator(".turn-card")).toBeVisible();
  await expect(page.locator("#pageInfo")).toContainText("第 1 / 3 页");

  await page.getByRole("button", { name: "末页" }).click();
  await expect(page.locator("#pageInfo")).toContainText("第 3 / 3 页");
  expect(queries.at(-1)?.get("offset")).toBe("40");
  await page.getByRole("button", { name: "首页" }).click();
  await expect(page.locator("#pageInfo")).toContainText("第 1 / 3 页");
  await page.locator("#pageJump").fill("2");
  await page.locator("#pageJump").press("Enter");
  await expect(page.locator("#pageInfo")).toContainText("第 2 / 3 页");
  expect(queries.at(-1)?.get("offset")).toBe("20");
  await page.locator("#sessionOrder").selectOption("asc");
  await expect(page.locator("#pageInfo")).toContainText("第 1 / 3 页");
  expect(queries.at(-1)?.get("offset")).toBe("0");
  expect(queries.at(-1)?.get("order")).toBe("asc");
});

test("session organization saves title, tags, note and favorite across reloads", async ({ page }) => {
  const fixture = await installSessionManager(page);
  await page.locator("#rows .conversation-title").first().click();
  await expect(page.locator("#sessionTitleInput")).toHaveValue("Project planning");
  await page.locator("#sessionTitleInput").fill("Memory service architecture");
  await page.locator("#sessionTagsInput").fill("architecture, follow-up");
  await page.locator("#sessionNoteInput").fill("Review the collector boundary.\nKeep the original evidence.");
  await page.locator("#sessionStarredInput").check();
  await page.locator("#sessionMetadataSave").click();
  await expect(page.locator("#sessionMetadataStatus")).toContainText("更改已保存");
  expect(fixture.writes).toHaveLength(1);
  expect(fixture.writes[0]).toEqual({
    expected_revision: 0,
    title: "Memory service architecture",
    note: "Review the collector boundary.\nKeep the original evidence.",
    tags: ["architecture", "follow-up"],
    starred: true,
    archived: false,
  });
  await page.reload();
  await expect(page.locator("#sessionTitleInput")).toHaveValue("Memory service architecture");
  await expect(page.locator("#sessionNoteInput")).toHaveValue("Review the collector boundary.\nKeep the original evidence.");
  await expect(page.locator("#sessionTagsInput")).toHaveValue(/architecture.*follow-up/s);
  await expect(page.locator("#sessionStarredInput")).toBeChecked();
  await page.getByRole("button", { name: /返回会话列表/ }).click();
  await expect(page.locator("#rows .conversation-title").first()).toContainText("Memory service architecture");
  await openFilters(page);
  await page.locator("#sessionTagFilter").fill("architecture");
  await page.locator("#sessionStarredFilter").check();
  await page.getByRole("button", { name: "应用筛选" }).click();
  await expect(page.locator("#rows .conversation-title")).toHaveText(["Memory service architecture"]);
});

test("archiving hides a session until archived view is selected and restoration returns it", async ({ page }) => {
  const fixture = await installSessionManager(page);
  await page.locator("#rows .conversation-title").first().click();
  await expect(page.locator("#sessionArchivedInput")).not.toBeChecked();
  await page.locator("#sessionArchivedInput").check();
  await page.locator("#sessionMetadataSave").click();
  await expect(page.locator("#sessionMetadataStatus")).toContainText("更改已保存");
  await page.getByRole("button", { name: /返回会话列表/ }).click();
  await expect(page.locator("#rows .conversation-title")).toHaveCount(0);
  await openFilters(page);
  await page.locator("#sessionArchiveFilter").selectOption("only");
  await page.getByRole("button", { name: "应用筛选" }).click();
  await expect(page.locator("#rows .conversation-title")).toHaveText(["Project planning"]);
  await page.locator("#rows .conversation-title").first().click();
  await expect(page.locator("#sessionArchivedInput")).toBeChecked();
  await page.locator("#sessionArchivedInput").uncheck();
  await page.locator("#sessionMetadataSave").click();
  await expect(page.locator("#sessionMetadataStatus")).toContainText("更改已保存");
  await page.getByRole("button", { name: /返回会话列表/ }).click();
  await expect(page.locator("#rows .conversation-title")).toHaveCount(0);
  await openFilters(page);
  await page.getByRole("button", { name: "重置筛选", exact: true }).click();
  await expect(page.locator("#sessionArchiveFilter")).toHaveValue("exclude");
  await page.getByRole("button", { name: "应用筛选" }).click();
  await expect(page.locator("#rows .conversation-title")).toHaveText(["Project planning"]);
  expect(fixture.writes.map((write) => write.expected_revision)).toEqual([0, 1]);
  expect(fixture.sessions[0].management.archived).toBe(false);
});

test("session search paginates the full result set and resets offset when filters change", async ({ page }) => {
  const sessions = Array.from({ length: 205 }, (_, i) => managedSession(
    "search-session-" + (i + 1),
    {
      title: "Searchable design " + String(i + 1).padStart(3, "0"),
      tags: i === 204 ? ["final"] : ["design"],
      starred: i === 204,
    },
  ));
  const fixture = await installSessionManager(page, sessions);
  await page.locator("#sessionSearch").fill("Searchable design");
  await page.locator("#sessionSearch").press("Enter");
  await expect(page.locator("#rows .conversation-title")).toHaveCount(100);
  await expect(page.locator("#sessionListInfo")).toContainText("205");
  await page.locator("#sessionListNext").click();
  await expect(page.locator("#rows .conversation-title").first()).toHaveText("Searchable design 101");
  expect(fixture.browserQueries.at(-1)?.get("q")).toBe("Searchable design");
  expect(fixture.browserQueries.at(-1)?.get("offset")).toBe("100");
  await page.locator("#sessionListLast").click();
  await expect(page.locator("#rows .conversation-title")).toHaveCount(5);
  await expect(page.locator("#sessionListNext")).toBeDisabled();
  await openFilters(page);
  await page.locator("#sessionTagFilter").fill("final");
  await page.locator("#sessionStarredFilter").check();
  await page.getByRole("button", { name: "应用筛选" }).click();
  await expect(page.locator("#rows .conversation-title")).toHaveText(["Searchable design 205"]);
  expect(fixture.browserQueries.at(-1)?.get("offset")).toBe("0");
  expect(fixture.browserQueries.at(-1)?.get("tag")).toBe("final");
  expect(fixture.browserQueries.at(-1)?.get("starred")).toBe("true");
  await openFilters(page);
  await page.getByRole("button", { name: "重置筛选", exact: true }).click();
  await expect(page.locator("#sessionSearch")).toHaveValue("");
  await expect(page.locator("#sessionTagFilter")).toHaveValue("");
  await expect(page.locator("#sessionStarredFilter")).not.toBeChecked();
  await page.getByRole("button", { name: "应用筛选" }).click();
  await expect(page.locator("#rows .conversation-title").first()).toHaveText("Searchable design 001");
});

test("concurrent metadata updates preserve unsaved input and require an explicit reload", async ({ page }) => {
  const fixture = await installSessionManager(page);
  await page.locator("#rows .conversation-title").first().click();
  await expect(page.locator("#sessionTitleInput")).toHaveValue("Project planning");
  await page.locator("#sessionTitleInput").fill("My unsaved title");
  fixture.conflictNextWrite();
  await page.locator("#sessionMetadataSave").click();
  await expect(page.locator("#sessionMetadataStatus")).toContainText("你的修改仍保留");
  await expect(page.locator("#sessionTitleInput")).toHaveValue("My unsaved title");
  await expect(page.locator("#sessionMetadataSave")).toBeDisabled();
  await expect(page.locator("#sessionMetadataReload")).toBeVisible();
  expect(fixture.writes).toHaveLength(1);
  const reloadPrompt = page.waitForEvent("dialog");
  page.once("dialog", (dialog) => void dialog.accept());
  await page.locator("#sessionMetadataReload").click();
  expect((await reloadPrompt).message()).toContain("未保存的修改");
  await expect(page.locator("#sessionTitleInput")).toHaveValue("Changed in another window");
  await expect(page.locator("#sessionMetadataSave")).toBeDisabled();
  expect(fixture.writes).toHaveLength(1);
  await page.locator("#sessionTitleInput").fill("Reviewed replacement");
  await expect(page.locator("#sessionMetadataSave")).toBeEnabled();
  await page.locator("#sessionMetadataSave").click();
  await expect(page.locator("#sessionMetadataStatus")).toContainText("更改已保存");
  expect(fixture.writes).toHaveLength(2);
  expect(fixture.writes[1].expected_revision).toBe(1);
});

test("quoted IDs and HTML-like titles stay inert during keyboard navigation", async ({ page }) => {
  const sessionID = "quoted');window.__sessionInjection=1;//<&\"/会话";
  const title = "<img src=x onerror=window.__sessionInjection=2> 'quoted' title";
  await installSessionManager(page, [managedSession(sessionID, { title })]);
  await expect(page.locator("#rows .conversation-title")).toHaveText(title);
  await expect(page.locator("#rows img")).toHaveCount(0);
  await page.getByRole("link", { name: title, exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("#sessionTitleInput")).toHaveValue(title);
  await expect(page.locator("#detailTitle")).toHaveText(title);
  expect(new URL(page.url()).hash).toBe("#/sessions/" + encodeURIComponent(sessionID));
  expect(await page.evaluate(() => (window as unknown as Record<string, unknown>).__sessionInjection)).toBeUndefined();
  await page.reload();
  await expect(page.locator("#sessionTitleInput")).toHaveValue(title);
  await expect(page.locator("#detailTitle img")).toHaveCount(0);
});

test("prototype-named session IDs remain exact across deep links, reloads and edits", async ({ page }) => {
  const sessionID = "__proto__";
  const fixture = await installSessionManager(page, [managedSession(sessionID, { title: "Prototype-named session" })]);
  await page.goto(`/#/sessions/${sessionID}`);
  await expect(page.locator("#sessionTitleInput")).toHaveValue("Prototype-named session");
  await expect(page.locator("#detailTitle")).toHaveText("Prototype-named session");
  await page.reload();
  await expect(page.locator("#sessionTitleInput")).toHaveValue("Prototype-named session");
  await page.locator("#sessionTitleInput").fill("Reviewed prototype-named session");
  await page.locator("#sessionMetadataSave").click();
  await expect(page.locator("#sessionMetadataStatus")).toContainText("更改已保存");
  expect(fixture.sessions[0].session_id).toBe(sessionID);
  expect(fixture.writes).toHaveLength(1);
  expect(fixture.writes[0].expected_revision).toBe(0);
  expect(new URL(page.url()).hash).toBe(`#/sessions/${sessionID}`);
  await page.getByRole("button", { name: /返回会话列表/ }).click();
  await expect(page.locator("#rows .conversation-title")).toHaveText("Reviewed prototype-named session");
});

test("theme switching changes the rendered palette and survives reload", async ({ page }) => {
  const root = page.locator("html");
  const initiallyDark = await root.evaluate((element) => element.classList.contains("dark"));
  const background = await page.locator("body").evaluate((element) => getComputedStyle(element).backgroundColor);
  await page.getByRole("button", { name: "切换主题", exact: true }).click();
  await expect.poll(() => root.evaluate((element) => element.classList.contains("dark"))).toBe(!initiallyDark);
  await expect.poll(() => page.locator("body").evaluate((element) => getComputedStyle(element).backgroundColor)).not.toBe(background);
  const selectedPalette = await root.evaluate((element) => getComputedStyle(element).getPropertyValue("--background").trim());
  expect(selectedPalette).not.toBe("");
  await page.reload();
  await expect(page.locator("#rows .conversation-title")).toHaveCount(1);
  await expect.poll(() => root.evaluate((element) => element.classList.contains("dark"))).toBe(!initiallyDark);
  await expect.poll(() => root.evaluate((element) => getComputedStyle(element).getPropertyValue("--background").trim())).toBe(selectedPalette);
});

test("mobile navigation reaches full conversation content without horizontal overflow", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: test.info().outputPath("mobile-session-library.png") });
  await page.getByRole("button", { name: "打开导航", exact: true }).click();
  const navigation = page.getByRole("complementary", { name: "工作区导航" });
  await expect(navigation).toBeVisible();
  await navigation.getByRole("button", { name: /mock-long-project/ }).click();
  await page.locator("#rows .conversation-title").first().click();
  await page.locator(".turn-card").first().click();
  await expect(page).toHaveURL(/\/turns\/mock-turn-long-content$/);
  await page.locator("#turnPrimary").getByRole("button", { name: "展开完整内容" }).first().click();
  await expect(page.locator("#turnPrimary")).toContainText("END-OF-COMPLETE-USER-COMMAND");
  const dimensions = await page.evaluate(() => ({
    viewport: document.documentElement.clientWidth,
    document: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
  }));
  expect(dimensions.document).toBeLessThanOrEqual(dimensions.viewport + 1);
  expect(dimensions.body).toBeLessThanOrEqual(dimensions.viewport + 1);
  await page.screenshot({ path: test.info().outputPath("mobile-turn-detail.png") });
  await page.getByRole("button", { name: /返回当前会话/ }).click();
  await expect(page).toHaveURL(/\/sessions\/mock-session-long-content$/);
});
