import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, statSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { test } from "node:test";

const repository = resolve(import.meta.dirname, "..");
const forbiddenExtensions = new Set([".py", ".pyi", ".pyc", ".sh", ".bash", ".cjs", ".js", ".mjs", ".ps1", ".rb"]);
// Existing native-history/capacity and optional local-embedding entry points.
const operationalAdapters = new Set([
  "scripts/archive_capacity.py", "scripts/archive_capacity_test.py",
  "scripts/cpa-embedding-server.py", "scripts/cpa_embedding_server_test.py",
  "scripts/repair_native_forks.py", "scripts/repair_native_forks_test.py",
  "scripts/sync_native_history.py", "scripts/sync_native_history_test.py",
]);

function files(): string[] {
  // Include tracked and reviewable untracked sources with Git's ignore rules.
  const paths = execFileSync("git", ["ls-files", "--cached", "--others", "--exclude-standard", "-z"], {
    cwd: repository, encoding: "utf8",
  }).split("\0").filter(Boolean);
  return [...new Set(paths)].map((path) => join(repository, path)).filter((path) => {
    try { return statSync(path).isFile(); } catch { return false; }
  });
}

test("release tooling stays TypeScript and existing operational adapters remain explicit", () => {
  const paths = files();
  const forbidden = paths
    .map((path) => relative(repository, path))
    .filter((path) => !operationalAdapters.has(path))
    .filter((path) => [...forbiddenExtensions].some((extension) => path.endsWith(extension)));
  assert.deepEqual(forbidden, []);
  assert.equal(paths.some((path) => relative(repository, path).startsWith(`.forgejo/`)), false);
  const text = paths
    .filter(
      (path) =>
        !path.endsWith("pnpm-lock.yaml") &&
        relative(repository, path) !== "tests/repository_contract.test.ts",
    )
    .map((path) => readFileSync(path, "utf8"))
    .join("\n");
  const tooling = paths
    .filter((path) => relative(repository, path).startsWith(".github/workflows/") || (relative(repository, path).startsWith("scripts/") && path.endsWith(".ts")))
    .map((path) => readFileSync(path, "utf8")).join("\n");
  assert.doesNotMatch(tooling, /#!.*\b(?:python|bash|sh)\b/);
  assert.doesNotMatch(tooling, /\b(?:python3?|PYTHONPATH)\b/);
  assert.doesNotMatch(text, /harbor\.k3s\.onetwo\.website|\.forgejo\/workflows/);
});

test("GitHub CI and v0.8.12 release form one immutable GHCR-only contract", () => {
  const ci = readFileSync(join(repository, ".github/workflows/ci.yml"), "utf8");
  const release = readFileSync(join(repository, ".github/workflows/release.yml"), "utf8");
  const dockerfile = readFileSync(join(repository, "Dockerfile"), "utf8");
  assert.match(ci, /push:\n\s+branches: \[main\]/);
  assert.doesNotMatch(ci, /tags:/);
  assert.match(ci, /pnpm typecheck/);
  assert.match(ci, /pnpm test:scripts/);
  assert.match(ci, /go test -count=1 -race \.\/\.\.\./);
  assert.match(release, /tags: \["v0\.8\.12"\]/);
  assert.doesNotMatch(release, /workflow_dispatch|harbor|forgejo/i);
  assert.doesNotMatch(release, /tags:\s*[^\n]*(?::latest|:main|:v0\.8\.12)/i);
  assert.match(release, /tags: \$\{\{ env\.IMAGE \}\}:\$\{\{ github\.sha \}\}/);
  assert.match(release, /steps\.build\.outputs\.digest/);
  assert.match(release, /release-manifest\.json/);
  assert.match(release, /cpa-session-identity-migrate-linux-amd64/);
  assert.match(release, /cpa-session-archive-backup-linux-amd64/);
  assert.match(release, /verify_buildkit_attestations\.ts/);
  assert.match(dockerfile, /ARG GO_IMAGE=[^\n]+@sha256:[0-9a-f]{64}/);
  assert.match(dockerfile, /ARG GO_IMAGE=golang:1\.26\.7-bookworm@sha256:/);
  assert.match(dockerfile, /ARG RUNTIME_IMAGE=[^\n]+@sha256:[0-9a-f]{64}/);
  assert.doesNotMatch(dockerfile, /^RUN go test /m);
  const uses = [...`${ci}\n${release}`.matchAll(/^\s*- uses: ([^\s]+)(?:\s+#.*)?$/gm)].map(
    (match) => match[1],
  );
  assert.ok(uses.length > 0);
  for (const reference of uses) {
    assert.match(reference, /@[0-9a-f]{40}$/, `action is not pinned: ${reference}`);
  }
});
