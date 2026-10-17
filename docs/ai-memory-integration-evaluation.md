# ai-memory integration evaluation

Date: 2026-10-16

Status: evaluated proposal; no integration, installation or data migration has
been implemented. This document records the comparison and the recommended
direction for the Aias00 fork, not a promise of current product capabilities.

## Goal and reviewed versions

Turn conversations from multiple AI clients into searchable project history and
reusable long-term memory, while retaining the original archive for inspection,
export and reprocessing.

- This fork: `4e01221d31c44767450033d213578dd1756c3558`, the collector/storage
  separation based on upstream Session Archive v0.8.12.
- [ai-memory](https://github.com/akitaonrails/ai-memory): reviewed main commit
  [`584ab792a2ac506445ca27629f040311ad8dc0d2`](https://github.com/akitaonrails/ai-memory/tree/584ab792a2ac506445ca27629f040311ad8dc0d2).
- The release API was checked for v3.1.0, including a macOS aarch64 artifact.
  Main-branch behavior was not assumed to be identical to that release.
- Evidence is source and documentation inspection. No ai-memory build, runtime,
  Chinese retrieval evaluation or CPA integration test was performed.

## Decision

For the alternative engine/product assessment, see the
[Hy-Memory and MemHub comparison](hy-memory-integration-evaluation.md).

Keep Session Archive as the authoritative protocol archive. Evaluate ai-memory
as a separate knowledge and retrieval service, accessed through its public
HTTP/MCP interfaces. Do not replace `internal/storage.Backend` with ai-memory or
share either application's SQLite database.

This is a product-layer integration rather than a database-driver replacement.
Our backend contract includes raw records, request and turn views, archive/SFT
exports, consistent snapshots, statistics and maintenance. ai-memory's public
contract instead exposes observations, wiki pages, retrieval and handoffs.

## Capability comparison

| Area | Session Archive today | ai-memory reviewed implementation |
| --- | --- | --- |
| Primary purpose | Capture and inspect model request/response history | Compile agent activity into reusable cross-session knowledge |
| Durable content | SQLite metadata and gzip-compressed, content-addressed payloads | Git-backed Markdown wiki plus SQLite operational data and indexes |
| Input | CPA request, response, stream and completion hooks | Client lifecycle hooks, external event batches and explicit memory writes |
| Retrieval | Session/turn browsing and metadata facet filters | FTS5, entities, link neighbors and vector ranking |
| Long-term memory | No implemented fact extraction or automatic recall layer | Versioned knowledge pages, consolidation, profiles and handoffs |
| Cross-client use | Capture clients whose requests pass through CPA | Client hooks and MCP retrieval, with harness-specific capabilities |
| Provider use | Captures existing calls; no summary model required | Rule-based summaries without a cloud LLM; richer consolidation can use a provider |
| Integration contract | Go storage interfaces and existing collector APIs | Public HTTP/MCP; not a CPA native plugin or archive backend |

Neither archive payloads nor ai-memory observations are guaranteed to be exact,
complete native transcripts. Our archive normalizes some stream output, drops
selected opaque fields and applies body-size limits; ai-memory capture is also
sanitized and bounded.

## Recommended topology

```text
CPA plugin -> Session Archive collector -> committed archive records
                                              |
                                      durable processing queue
                                              |
                                  normalization / memory adapter
                                              |
                                   ai-memory HTTP or MCP APIs
                                              |
                               knowledge pages + retrieval indexes
                                              |
                                      AI clients via MCP
```

Responsibilities:

1. Session Archive retains request/response evidence and source identifiers.
2. A background adapter extracts new messages rather than repeatedly importing
   the full history clients resend. It resolves project scope, sanitizes content,
   preserves provenance and records delivery state.
3. ai-memory owns its wiki, index, versioning, consolidation and retrieval.
4. Clients use MCP to retrieve relevant knowledge. Automatic prompt injection is
   a separate feature to evaluate, not a prerequisite for initial integration.

Prefer committed archive records as the adapter's input. A durable outbox written
in the archive transaction, or a verified resumable committed-event reader, must
prevent a crash between archive commit and delivery from losing the handoff.
Do not add an independent network write inside `PutBatch` and call it atomic.
This only protects records that reached the collector; it does not repair the
existing plugin-side in-memory queue's overflow or crash losses.

## Public integration paths

- Historical import: transform selected sessions into the importer's generic
  external-conversation envelope. It accepts system/user/assistant messages, not
  arbitrary raw CPA protocol JSON or complete tool traces. A transformation and
  source-ID ledger are required.
- Live events: use `/hook/batch` only when the adapter can truthfully map the
  supported event vocabulary and native session identity. A CPA request ending
  is not evidence of native `SessionEnd`; lifecycle completion needs client
  hooks, an explicit finish operation, or a clearly labeled alternative policy.
- Knowledge pages: use MCP `memory_write_page` with explicit project scope,
  source references and conditional writes (`create_only` / `expected_page_id`).
- Recall: use `memory_query` and return source links to the retained archive.
- Delivery: reference the companion relay's persistent queue and per-session
  ordering. Stable event identity, not prompt content hashes, identifies retries.
  Ordinary ai-memory ingest keys expire after 30 days; they do not provide
  permanent deduplication or exactly-once effects. Keep our own import ledger.

Choose one observation capture owner for each execution if native ai-memory
hooks and CPA-derived ingestion coexist. MCP recall can remain enabled without
duplicating native capture. A passive proxy cannot assume it controls the client
process environment or its lifecycle.

## Storage alternatives

| Option | Assessment for the current personal Mac mini deployment |
| --- | --- |
| Keep SQLite and add local search/memory projections | Lowest operational overhead; requires implementing the memory layer ourselves |
| Keep SQLite archive and integrate ai-memory | Recommended integration experiment; reuses its knowledge workflow through public APIs |
| PostgreSQL/pgvector for a new memory layer | Possible later if centralized multi-process writes justify another database |
| Replace the entire archive with ai-memory | Poor fit: its public model does not preserve our complete backend contract |
| Write directly to ai-memory SQLite or wiki files | Rejected: bypasses its authorization, sanitization, mutation and indexing boundaries |

## Useful designs to borrow

- Human-readable, versioned memory pages, separate from protocol archives.
- Incremental consolidation outside the model request path.
- Retrieval combining lexical, entity, link and vector candidates.
- Explicit project scope, provenance, conditional updates and supersession.
- Persistent event delivery with retry identity and observable outcomes.
- Memory lifecycle controls rather than permanently treating every utterance as
  a project fact or user preference.

## Limits and validation requirements

- **Recovery:** wiki pages can rebuild page indexes, but sessions, observations
  and handoffs cannot all be reconstructed from Markdown. Back up operational
  SQLite data as well. Git history retention is not irreversible deletion.
- **Chinese retrieval:** reviewed FTS uses `unicode61`; it is not a dedicated
  Chinese segmenter. Test real Chinese queries before selecting the tokenizer or
  embedding model.
- **Embedding defaults:** reviewed main tries local MiniLM embeddings when no
  provider is configured, with fallback if unavailable. The first model download
  and the selected release's behavior need verification. Cloud embeddings and
  consolidation can send conversation-derived content to another provider.
- **Vector scale:** reviewed retrieval scores matching project page vectors by
  exact scan. It is not an ANN/HNSW index. Evaluate page counts and latency before
  importing a large corpus; do not embed every repeated protocol request.
- **Identity:** project scope must distinguish repositories and worktrees across
  devices. Requests lacking trustworthy scope remain unassigned rather than
  contaminating another project.
- **Deletion:** define how source deletion affects delivered observations,
  generated pages, links and embeddings. Source lookup must not resurrect data
  that a retention or deletion rule made unavailable.
- **Trust:** recalled text is historical evidence, not authority to execute tools
  or override current user/project instructions.
- **Licensing:** reviewed ai-memory is MIT; this fork is Apache-2.0. Preserve
  applicable notices if copying implementation rather than integrating by API.

## Proposed experiment and acceptance

1. Run ai-memory separately with a disposable corpus and pinned version; do not
   change production CPA configuration.
2. Import a small, sanitized sample from more than one client and project. Verify
   message ordering, repeated-history suppression and traceable source IDs.
3. Compare lexical and semantic recall on Chinese and code/error queries. Answers
   must cite the original session; an absent result must not invent a memory.
4. Test offline delivery, process restart and repeated import. Confirm no silent
   loss or duplicate knowledge from an acknowledged/replayed source event.
5. Verify version conflicts, scope isolation, deletion and backup restoration.
6. Connect two actual clients via MCP and verify useful cross-client recall.

Only after this evidence should we choose between ongoing service integration
and implementing selected designs inside our fork. The current storage
separation supplies a boundary, not an implemented event publisher or memory
adapter.

## Pinned references

All ai-memory references below use the reviewed commit, not a moving branch.

- [Architecture](https://github.com/akitaonrails/ai-memory/blob/584ab792a2ac506445ca27629f040311ad8dc0d2/docs/ARCHITECTURE.md)
- [Programmatic memory](https://github.com/akitaonrails/ai-memory/blob/584ab792a2ac506445ca27629f040311ad8dc0d2/docs/programmatic-memory.md)
- [External lifecycle capture](https://github.com/akitaonrails/ai-memory/blob/584ab792a2ac506445ca27629f040311ad8dc0d2/docs/external-lifecycle.md)
- [Companion integration policy](https://github.com/akitaonrails/ai-memory/blob/584ab792a2ac506445ca27629f040311ad8dc0d2/docs/companion-crates.md)
- [Importer](https://github.com/akitaonrails/ai-memory/blob/584ab792a2ac506445ca27629f040311ad8dc0d2/companions/ai-memory-importer/README.md)
- [Relay](https://github.com/akitaonrails/ai-memory/blob/584ab792a2ac506445ca27629f040311ad8dc0d2/companions/ai-memory-relay/README.md)
- [Local embeddings](https://github.com/akitaonrails/ai-memory/blob/584ab792a2ac506445ca27629f040311ad8dc0d2/docs/local-embeddings.md)
- [Lifecycle and recovery](https://github.com/akitaonrails/ai-memory/blob/584ab792a2ac506445ca27629f040311ad8dc0d2/docs/lifecycle-ops.md)
- [Vector retrieval source](https://github.com/akitaonrails/ai-memory/blob/584ab792a2ac506445ca27629f040311ad8dc0d2/crates/ai-memory-store/src/reader.rs#L5218)
- [Current collector storage contract](../internal/storage/storage.go)
