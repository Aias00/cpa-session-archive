# Reviewed Git memory through Codex

Status: implemented and installed; only Codex was used for acceptance testing.

## Authority and scope

Use the existing canonical `codex-kb` Git repository for reusable knowledge;
never create a second authoritative SQLite memory store. Project-only facts
belong in registered project Git repositories' `.codex/ai-context.yaml`.
M4 remains the archive/evidence authority and the authenticated source service.
The workstation stdio bridge reads/writes the owner's existing Git authorities.
The bridge binary is deployed on M4 too; clients select their local canonical
repository path. No private knowledge is automatically pushed to GitHub.

The existing KB has unrelated uncommitted changes. Preserve them. Commit only
managed candidate/canonical files and reject edits to already-dirty owned files.
Canonical queries read committed Git HEAD, not uncommitted files. No Claude
commands, model calls or configuration changes are part of this increment.

## First lifecycle

- [x] `memory_propose` saves an explicitly scoped, sourced draft; deterministic
  operation IDs make retries idempotent. Editing requires the expected draft
  revision; correcting active knowledge records its base revision.
- [x] `project_memory` reads only confirmed, current in-scope Git entries. Drafts
  are separately requested. Revalidate retained archive citations on reads and
  review; deleted/changed/cross-project evidence cannot support active recall.
- [x] A separate `cpa-memory review` command requires an explicit reviewed
  attestation and expected draft revision. Review is never exposed as MCP.
  Confirmation commits canonical Markdown/YAML; corrections preserve Git history.
  Archiving makes a prior entry unavailable to subsequent active recall.
- [x] Project roots are configured by the owner, never accepted as tool arguments.
  Global promotion is explicit. Preserve existing KB front matter/body contracts.
- [x] Test source validation, scopes, optimistic revisions, duplicate retries,
  confirm/correct/archive, dirty-file safety, and independent staged Git changes.
- [x] Test the actual workflow with Codex only: draft proposal, explicit operator
  confirmation, recall, correction and archive. Use disposable fixtures for
  synthetic claims; leave no test claim in the user's canonical entries.
- [x] Run full/race/vet/build checks, install verified bridge binaries on the
  workstation and M4, back up configuration and record honest remaining limits.

Automatic extraction, inferred preference activation, semantic search and
unattended publication remain separate increments. Saving a citation proves
where evidence was retained, not that a proposed interpretation is correct.

## Verified outcome

Bridge commit `fb5526f61310d4412237076b7aedd8cc5b18c788` was full/race-tested
and vetted on workstation and Apple M4, then installed on both. Collector/CPA
processes, archive schema and memory projection were unchanged. A verified raw
SQLite backup and prior bridge/configuration were preserved before deployment.

Four fresh Codex runs against a disposable Git repository passed: draft save
without activation; confirmed-entry correction as a draft; corrected recall
after operator review; and empty recall after archive. A further Codex check
used the actual installed entry and existing canonical KB, found the confirmed
`canonical-memory-authority` entry, and left the existing Git worktree/index
unchanged. No test claim entered the user's canonical entries. No Claude test
was run and its configuration hash remained unchanged.

The real Codex entry now references the existing workstation codex-kb and an
owner-configured map for cpa-session-archive/CLIProxyAPI project Git roots. Other
project roots must be explicitly registered. The M4 bridge is available for
clients with their own selected canonical Git checkout; this rollout does not
copy or publish a second authoritative knowledge repository on M4.
