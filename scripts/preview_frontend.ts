import { spawn, spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));
const port = Number(process.env.CPA_PREVIEW_PORT || 18818);
if (!Number.isInteger(port) || port < 1024 || port > 65535) {
  throw new Error("CPA_PREVIEW_PORT must be a port between 1024 and 65535");
}
for (const args of [
  ["node_modules/typescript/bin/tsc", "-p", "frontend/tsconfig.json"],
  ["node_modules/vite/bin/vite.js", "build", "--config", "frontend/vite.config.ts"],
]) {
  const result = spawnSync(process.execPath, args, { cwd: root, stdio: "inherit" });
  if (result.status !== 0) process.exit(result.status || 1);
}

const preview = spawn(process.execPath, ["--import", "tsx", path.join(root, "e2e/mock-server.ts")], {
  cwd: root,
  env: { ...process.env, CPA_E2E_PORT: String(port), CPA_PREVIEW: "1" },
  stdio: "inherit",
});
console.log(`Synthetic session workspace: http://127.0.0.1:${port}`);
console.log("Uses synthetic fixtures only. No production services or credentials are accessed.");
preview.on("error", () => { console.error("Could not start the synthetic preview server."); process.exitCode = 1; });
preview.on("exit", (code, signal) => { process.exitCode = signal ? 0 : code || 0; });
for (const signal of ["SIGINT", "SIGTERM"] as const) {
  process.on(signal, () => { preview.kill(signal); });
}
