import { spawnSync } from "node:child_process";
import { readFileSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));
const output = mkdtempSync(path.join(tmpdir(), "cpa-ui-build-"));
try {
  const result = spawnSync(process.execPath, [path.join(root, "node_modules/vite/bin/vite.js"), "build", "--config", "frontend/vite.config.ts"], {
    cwd: root, env: { ...process.env, CPA_UI_OUT_DIR: output }, stdio: "inherit",
  });
  if (result.status !== 0) process.exitCode = result.status || 1;
  else if (!readFileSync(path.join(output, "index.html")).equals(readFileSync(path.join(root, "internal/archive/web/index.html")))) {
    console.error("Embedded UI is stale. Run pnpm build:frontend and include the generated index.html.");
    process.exitCode = 1;
  }
} finally {
  rmSync(output, { recursive: true, force: true });
}
