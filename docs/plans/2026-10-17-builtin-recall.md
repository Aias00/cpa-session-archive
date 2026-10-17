# Built-in recall implementation plan

Status: first retrieval increment implemented and locally verified. Implements the first retrieval increment of
[the product plan](2026-10-17-project-memory-product.md), authorized by the user.

## Outcome and boundary

Provide local, source-grounded search without models or new dependencies. Keep
archive capture and exports compatible. Do not deploy to the Mac mini or modify
client configuration in this increment. MCP, automatic extraction and semantic
retrieval remain separate increments.

## Implementation and verification

- [x] Add an optional storage change-feed capability using the existing
  transactionally written `archive_ingest_events`; read retained records and
  payloads in one consistent snapshot. Test updates, deletions and rollback.
- [x] Normalize retained Chat Completions, Responses and Claude Messages into
  sourced text without UI truncation. Test tools, repeated messages, long text,
  SSE and partial/unsupported input.
- [x] Add a separate SQLite projection and resumable backfill/live checkpoints.
  Commit each projection and its checkpoint together. Retry failures without
  advancing the checkpoint. Test crash/reopen replay and concurrent updates.
- [x] Add literal, case-insensitive lexical search with project/client/session/
  time filters and immutable revision/range citations. Check current source on
  every result/read to suppress deleted, moved or updated evidence immediately.
  Test Chinese, literal paths/errors, scope changes and deletion before replay.
- [x] Expose search, source reads, deterministic excerpt recaps and index status
  through a dedicated loopback-only, token-authenticated HTTP listener. A small
  separate Web page uses that API; never expose the collector's ingest port.
- [x] Run targeted tests, full tests/race/vet and native builds. Exercise a real
  disposable collector and memory API with ingestion, restart and deletion.

## Deliberate adjustments

The existing archive already has a durable ordered change log; use it as the
outbox rather than add a second queue. Backfill records by row ID, then replay
the complete change log. This is at-least-once processing, not durable collector
HTTP acknowledgement. A single worker owns both checkpoints.

Use bundled SQLite FTS4 (available in ordinary mattn/go-sqlite3 builds) with
pre-tokenized Unicode words and CJK unigrams/bigrams, followed by literal term
verification. This avoids requiring all users to build with an FTS5 tag. Search
does not accept raw FTS syntax. Queries have bounded result/candidate limits and
report when candidate limits truncate recall.

Project identity initially uses an explicit `project.id` or exact `git.remote`
facet. A bare project name/path is not treated as a cross-device identity;
unassigned sources stay unassigned. No inferred personal preference or generated
claim is promoted to durable memory. Repeated snapshots remain attributable to
their requests; no text-hash-only merging of separate user actions is permitted.

Read credentials are dedicated to this API and represent owner access to all
archived projects on this machine. Per-client project-scoped credentials and
MCP are not claimed in this increment. Index files contain private plaintext
and require the same backup/protection as the archive.
