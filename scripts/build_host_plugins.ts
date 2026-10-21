import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = fileURLToPath(new URL("..", import.meta.url));
const plugin = path.join(root, "integrations/deepseek-harness");
const result = spawnSync(process.execPath, [path.join(root, "node_modules/typescript/bin/tsc"),
  "--target", "ES2022", "--module", "NodeNext", "--moduleResolution", "NodeNext", "--strict", "--skipLibCheck", "--types", "node",
  "--declaration", "--rootDir", path.join(plugin, "src"), "--outDir", path.join(plugin, "lib"), path.join(plugin, "src/index.ts"),
], { cwd: root, stdio: "inherit" });
process.exitCode = result.status ?? 1;
