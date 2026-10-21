import assert from "node:assert/strict";
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync, rmSync, chmodSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "vite";

const root = fileURLToPath(new URL("..", import.meta.url));
const magpie = process.env.MAGPIE_SOURCE;
const harness = process.env.DSH_SOURCE;
const go = process.env.GO_BINARY || "go";
if (!magpie || !harness) throw new Error("Set MAGPIE_SOURCE and DSH_SOURCE to the host source checkouts");
const work = mkdtempSync(path.join(tmpdir(), "cpa-host-acceptance-"));
chmodSync(work, 0o700);
let collector: ChildProcess | undefined;
function run(command: string, args: string[], cwd = root, env = process.env): void {
  const result = spawnSync(command, args, { cwd, env, stdio: "inherit" });
  if (result.status !== 0) throw new Error(`Development verification failed: ${path.basename(command)}`);
}
try {
  // Host checkouts are read-only inputs. All workspace sums and binaries are temporary.
  const version = /^go (.+)$/m.exec(readFileSync(path.join(magpie, "go.mod"), "utf8"))?.[1];
  if (!version) throw new Error("Cannot read Magpie toolchain requirement");
  const goWork = path.join(work, "go.work");
  writeFileSync(goWork, `go ${version}\n\nuse (\n${JSON.stringify(root)}\n${JSON.stringify(path.join(root, "integrations/magpie"))}\n${JSON.stringify(path.resolve(magpie))}\n)\nreplace github.com/yetone/magpie v0.0.0 => ${JSON.stringify(path.resolve(magpie))}\n`);
  run(go, ["test", "-race", "-tags", "nogui", "./..."], path.join(root, "integrations/magpie"), { ...process.env, GOWORK: goWork });
  const originalAll = path.resolve(magpie, "plugins/all/all.go");
  const overlaidAll = path.join(work, "all.go");
  writeFileSync(overlaidAll, readFileSync(originalAll, "utf8") + '\nimport _ "github.com/yetone/magpie/plugins/sessionarchive"\n');
  const overlay = path.join(work, "overlay.json");
  writeFileSync(overlay, JSON.stringify({ Replace: { [originalAll]: overlaidAll } }));
  run(go, ["build", "-tags", "nogui", "-overlay", overlay, "-o", path.join(work, "magpie"), "."], magpie, { ...process.env, GOWORK: goWork });
  const capture = path.join(work, "cpa-session-capture");
  const collectorBinary = path.join(work, "cpa-session-collector");
  run(go, ["build", "-o", capture, "./cmd/cpa-session-capture"]);
  run(go, ["build", "-o", collectorBinary, "./cmd/cpa-session-collector"]);
  const server = createServer();
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  assert.ok(address && typeof address !== "string");
  const endpoint = `http://127.0.0.1:${address.port}`;
  await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  collector = spawn(collectorBinary, [], { env: { PATH: process.env.PATH, ARCHIVE_DB: path.join(work, "archive.sqlite"), LISTEN_ADDR: `127.0.0.1:${address.port}` }, stdio: "ignore" });
  collector.on("error", () => {});
  let ready = false;
  for (let i = 0; i < 100; i++) {
    if (collector.exitCode !== null || collector.signalCode !== null) throw new Error("Isolated collector exited");
    try { ready = (await fetch(endpoint + "/readyz", { signal: AbortSignal.timeout(500) })).ok; } catch { /* Startup is bounded below. */ }
    if (ready) break;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  assert.ok(ready, "Isolated collector did not become ready");
  // Bundle actual Cordis source, rather than mocking ctx.on/waterfall/disposal.
  const fixture = path.join(work, "acceptance.ts");
  writeFileSync(fixture, `
import assert from "node:assert/strict";
import { Context } from "@deepseek-ai/cordis";
import { apply, ProcessSink } from "archive-plugin";
import * as plugin from "archive-plugin";
const ctx = new Context();
const processSink = new ProcessSink(${JSON.stringify({ binary: capture, endpoint, spoolDir: path.join(work, "outbox") })});
const records = [];
let closed = false;
const sink = {submit(message) { records.push(message); processSink.submit(message); }, close() { closed = true; return processSink.close(); }};
const fiber = ctx.plugin({apply(context) { apply(context, {}, sink); }});
await Promise.resolve();
const request = Object.freeze({provider:"synthetic",model:"fixture",sessionId:"fixture-session",messages:[{role:"user",content:[{type:"text",text:"synthetic retained question"}]}]});
const chunks = [{type:"text-delta",index:0,text:"synthetic answer"},{type:"block-end",index:0,block:{type:"text",text:"synthetic answer"}},{type:"finish",reason:{kind:"stop"}}];
const seen = [];
for await (const chunk of ctx.waterfall({}, "llm/stream", request, async function*(){yield* chunks;})) seen.push(chunk);
assert.equal(records.length,1);
chunks.forEach((chunk,index)=>assert.strictEqual(seen[index],chunk));
const id=records[0].record.request_id;
let archived;
for(let i=0;i<100;i++) {
 const response = await fetch(${JSON.stringify(endpoint)}+"/v1/requests/"+encodeURIComponent(id),{signal:AbortSignal.timeout(1000)});
 if(response.ok){archived=await response.json();break;}
 await new Promise(resolve=>setTimeout(resolve,50));
}
assert.ok(archived,"No committed archive record");
assert.equal(archived.session_id,"deepseek-harness:fixture-session");
assert.equal(archived.outcome,"succeeded");
assert.equal(JSON.parse(Buffer.from(archived.original_request,"base64").toString()).messages[0].content[0].text,"synthetic retained question");
assert.equal(JSON.parse(Buffer.from(archived.response,"base64").toString()).choices[0].message.content[0].text,"synthetic answer");
await fiber.dispose();
assert.equal(closed,true);
const after=[];
for await(const chunk of ctx.waterfall({},"llm/stream",request,async function*(){yield* chunks;}))after.push(chunk);
assert.equal(records.length,1,"Disposed plugin still observes calls");
assert.equal(after.length,chunks.length);
// Load the exported plugin itself, including its llm injection and default sink.
const removeLlm = ctx.provide("llm", {});
const direct = ctx.plugin(plugin, ${JSON.stringify({ binary: capture, endpoint, spoolDir: path.join(work, "direct-outbox") })});
await direct;
for await (const chunk of ctx.waterfall({}, "llm/stream", {...request,sessionId:"direct-session"}, async function*(){yield* chunks;})) assert.ok(chunks.includes(chunk));
let directRecords = [];
for(let i=0;i<100;i++) {
 const response = await fetch(${JSON.stringify(endpoint)}+"/v1/sessions/deepseek-harness:direct-session",{signal:AbortSignal.timeout(1000)});
 if(response.ok){directRecords=await response.json();if(directRecords.length)break;}
 await new Promise(resolve=>setTimeout(resolve,50));
}
assert.equal(directRecords.length,1,"Exported plugin did not load through Cordis injection");
await direct.dispose();
removeLlm();
assert.equal(ctx.events.dispatch("waterfall",[{},"llm/stream",request,async function*(){}]).length,0,"Unloaded plugin retained stream listeners");
console.log("Actual Cordis dispatch -> capture child -> collector -> SQLite passed; disposal preserved the stream.");
`);
  await build({ configFile: false, logLevel: "error", resolve: { alias: {
    "@deepseek-ai/cordis": path.resolve(harness, "vendor/cordis/src/index.ts"),
    "@deepseek-ai/cosmokit": path.resolve(harness, "vendor/cosmokit/src/index.ts"),
    "archive-plugin": path.join(root, "integrations/deepseek-harness/src/index.ts"),
  } }, build: { ssr: fixture, outDir: path.join(work, "bundle"), target: "esnext", minify: false,
    rollupOptions: { output: { entryFileNames: "acceptance.mjs" } } }, ssr: { noExternal: true } });
  run(process.execPath, [path.join(work, "bundle/acceptance.mjs")]);
  console.log("Host plugin acceptance passed using synthetic evidence only.");
} finally {
  if (collector && collector.exitCode === null && collector.signalCode === null) {
    const exited = new Promise<void>(resolve => collector!.once("exit", () => resolve()));
    collector.kill("SIGTERM");
    await exited;
  }
  rmSync(work, { recursive: true, force: true });
}
