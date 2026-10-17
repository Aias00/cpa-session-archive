# Project recall and continuity implementation plan

Date: 2026-10-17

Status: partially implemented. The [built-in retrieval increment](../builtin-memory.md)
provides local search, revision-checked source excerpts and a Web recap packet.
Remaining release gates, MCP and durable curated memory are not implemented;
no Mac mini deployment has been performed.
Baseline: fork commit `35cfaa281505c4008abae4613c64cedc05717b26`.

## Product outcome

Make the archive useful during everyday work, not merely a collection of model
requests. A user should be able to switch supported clients and answer:

1. What did we decide about this project, and where was it said?
2. What approaches failed, under which conditions, and what was actually tested?
3. Where did the last task stop, and what should the next client do first?

The first useful release delivers scoped history search and an evidence-backed
handoff through the Web UI and MCP. Durable project memory follows. Semantic
search improves recall later; it is not a prerequisite for basic usefulness.
Until the user changes the priority, ship retrieval before automatic injection.

### Explicit boundaries

- Personal Mac mini service, with multiple clients/devices and multiple projects.
- Initially process only data that actually reached CPA and was retained. Do not
  claim that all client history or a complete native session has been captured.
- Preserve the raw archive and current archive/export behavior. Use additive,
  versioned migrations and a new derived memory database.
- This plan does not authorize implementation, dependency installation, model
  calls, client configuration changes, production migration or deployment.

## Current evidence and gaps

| Evidence | Consequence for implementation |
| --- | --- |
| [Storage contract](../../internal/storage/storage.go) separates collector behavior from the SQLite adapter | Reuse it; do not restart a database abstraction refactor |
| [Plugin completion](../../internal/archive/plugin.go) enqueues a finished record non-blockingly and currently drops on a full queue | Expose loss/partial state; downstream processing cannot recover missing upstream records |
| [Collector](../../cmd/cpa-session-collector/main.go) currently returns 202 before its writer commits | Queue acceptance is not a durability guarantee |
| [SQLite batch write](../../internal/archive/store.go) already uses a transaction | Add processing work in that same transaction, not an independent network dual-write |
| [Timeline reconstruction](../../internal/archive/timeline.go) identifies repeated prefixes, retries and tool continuation | Reuse tested parsing concepts, but not the UI view as a complete data feed |
| Timeline output can retain only the final 24 entries and truncate text at 40,000 bytes | Normalize from retained payloads through a dedicated loss-aware path |
| [CAS normalization](../../internal/archive/cas.go) removes selected opaque fields and folds successful Responses SSE | Preserve original retained evidence and explicitly represent unavailable detail |
| Current archive queries primarily use facets, not a searchable normalized message corpus | Add messages/chunks and Chinese-aware lexical retrieval |
| [Management resources](../../internal/archive/management.go) expose a static page and forward data reads through management routes | Keep secrets/data out of static resources; fix download integration separately |
| ai-memory and Hy-Memory evaluations are source-level comparisons only | Do not make an untested external engine the first release's critical dependency |

Mac mini still runs the previously installed v0.8.12 build. The collector/storage
refactor exists on the development branch and needs its deferred independent
review completed before a release. Do not describe it as deployed.

## Architecture decision

Use the existing Go collector and SQLite archive. Add a background processing
worker and a separate `memory.sqlite` for normalized content, search indexes,
handoff snapshots and curated memory. Keep the plugin thin.

```text
AI clients -> CPA -> archive plugin -> collector
                                       |
                           archive.sqlite transaction
                           [record/CAS + processing job]
                                       |
                         restart-safe background worker
                                       |
                         memory.sqlite derived projection
                         [messages / search / handoff / memory]
                                       |
                         authenticated read/query boundary
                                  /            \
                              Web UI       stdio MCP bridge
```

One collector-owned worker writes the memory projection initially. Do not let
the plugin, browser, MCP bridge or an external engine write these databases.
Database paths and job lifecycle stay outside the CPA request execution path.

### Alternatives considered

| Option | Assessment |
| --- | --- |
| Go + SQLite projections and a small recall product | Selected for the first releases: reuses deployment, minimizes dependencies, and makes search useful before model spending |
| ai-memory sidecar | Retained as a future integration candidate for knowledge pages and cross-client recall; introduces another runtime and mapping/duplicate-capture boundaries |
| Hy Memory pro / MemHub | Retained as a candidate for extraction or a separate product trial; useful fact/experience/governance design, but adds model and multi-store dependencies |
| PostgreSQL/pgvector or a graph service now | Deferred: current missing capability is data normalization and retrieval, not a demonstrated database capacity failure |

This is a small project-memory implementation, not a reimplementation of all
ai-memory or MemHub. Borrow their bounded processing, provenance, versioning and
retrieval principles. Reassess external engine integration only against measured
gaps, using the same evaluation corpus.

## Data and identity contracts

| Object | Minimum fields and invariant |
| --- | --- |
| Project binding | Canonical project ID, repository identity, device/path aliases, owner and mapping revision; a directory basename alone is not identity |
| Source binding | Client/device namespace, raw session/thread ID, request ID, native turn/message/tool IDs when supplied, detection method and association confidence |
| Message | Project, session/branch, role, normalized content, source references, event time, completeness and normalizer version |
| Search chunk | Stable message/range reference, lexical text, content revision, scope and visibility generation |
| Handoff | Goal, progress, verified results, failed attempts, unresolved questions, next actions, source citations and source watermark |
| Durable memory | Kind, statement, project/global scope, sources, draft/active/superseded/deleted state, validity, revision and optional extraction metadata |
| Processing job | Durable source change identity, source/scope revision, purpose/version, lease, attempts, next retry, last failure and completion marker |

Keep decisions, facts, experiences and preferences distinct. Model-generated
summaries are labeled as generated; model confidence is not proof. Global
preferences require an explicit user action. Task progress may update
automatically, but must not silently promote an unverified claim to a project
rule. Keep canonical repository instructions authoritative.

Stable IDs use the source namespace and supplied event IDs. Payload hashes detect
unchanged revisions, not whether two user actions are the same event. Do not merge
equal-text repeated instructions, concurrent branches, or two devices solely by
matching a prompt hash. Unknown project/session associations remain visible and
unassigned until a reliable mapping exists.

A citation identifies the request plus retained payload revision/reference and
message/range, not only the mutable request ID. If a source revision is no longer
retained, show it as unavailable; never substitute the latest body and claim it
is the original evidence. Reprocessing validates the current content and scope
revision before publishing a job's result.

## Release slices and gates

### Slice 1: source-grounded search, the first useful release

Deliverables:

- Finish review of the storage refactor; preserve existing tests and exports.
- Add a transactionally recorded processing queue and resumable historical
  backfill. Jobs also cover record updates and later source invalidations.
- Normalize retained Chat Completions, Responses and Claude Messages into
  messages and tool records. Mark unsupported protocols and opaque history as
  partial rather than pretending they were parsed.
- Build searchable content from actual new messages. Repeated request context,
  retries and incomplete stream tails must not manufacture duplicate messages.
- Enable FTS5 in the actual Go builds, not just system SQLite. Start with
  Unicode word indexing plus a Chinese short-query strategy validated by tests;
  character grams or trigram lookup must have exact-match verification and a
  bounded fallback for one/two-character terms.
- Add a Web search box with project/client/time filters, snippets, completion
  status and links to the retained source. Show index lag, failures and partial
  coverage. Repair the known Web export-route mismatch without exposing the
  collector's unauthenticated port to other machines.

Likely owners: new `internal/memory/{model,normalize,store,index,jobs}.go`,
new migrations beside that store, a narrow committed-change/job capability at
`internal/storage`, the SQLite archive transaction, collector worker wiring,
`internal/archive/management.go` and its existing Web resource.

Acceptance:

- An interrupted backfill resumes and converges with live ingestion without a
  skipped upsert or duplicate normalized message. Source ordering follows a
  committed sequence/revision, not request ID or wall clock alone.
- A process killed after archive commit and before memory projection does not
  lose its pending work; after projection but before job ACK it can safely replay.
- Replaying the same request snapshot 10 times adds no extra occurrences; two
  distinct identical user messages remain two messages.
- On a labeled 30-query corpus, at least 24 queries retrieve the correct source
  within the top 5; at least 9 of 10 exact error/path queries do so. These are
  initial acceptance targets, not claimed current measurements.
- All search results have resolvable source references. Missing or partial
  evidence is disclosed. Search works without any LLM/embedding configuration.

### Slice 2: cross-client recall and a reliable handoff

Deliverables:

- Provide a read-only stdio MCP bridge: `archive_search`, `archive_read_source`,
  `project_recap` and `project_memory`. Tool names are proposed API names.
- The bridge calls collector-owned authenticated APIs. Give it a dedicated
  read credential and permitted project scope; do not distribute the full CPA
  management key to every AI client. Raw archive access remains separately
  authorized. Keep data endpoints loopback-only by default, using the existing
  SSH/Tailscale access pattern when needed.
- Produce a project/task handoff with the goal, latest attributable work,
  observed checks, unresolved issues and next actions. First provide a
  deterministic source excerpt packet; add an optional structured summary only
  after evidence and cost controls exist.
- Users select the task/branch when several are active. Idle time can trigger a
  checkpoint but is never treated as proof of native session completion.
- Support copying a bounded context block and reading it via MCP. Preserve the
  native agent transcript, tool protocol and compaction state.

Cross-client continuity here means a new client can understand and continue the
task using cited context. It does not claim that Claude can natively resume a
Codex thread, or that an account switch can transfer opaque provider state.

Likely owners: new `cmd/cpa-memory`, `internal/memory/{query,handoff,auth}.go`,
collector query routes and Web handoff view. Confirm the MCP transport/library
choice before adding a dependency; do not hand-roll an unverified protocol stack.

Acceptance:

- Complete a task fragment in one real client, then use a second real client to
  find its decision and source, identify the last verified state and continue
  without repeating the full explanation. Test Codex and Claude Code first.
- Token/project scope mismatches return no data, including through source links.
- A handoff uses at most 8 KiB of UTF-8 content by default and includes citations;
  optional token-aware limits are recorded explicitly, not inferred from bytes.
- A missing result returns an explicit no-evidence response. A failed check is
  never presented as a passing result. An outdated handoff shows its watermark.

Release boundary: ship Slices 1 and 2 before building advanced cognition. At this
point the plugin is useful even if no model-generated long-term memory is added.

### Slice 3: durable decisions and reusable project experience

Deliverables:

- Let users save a sourced decision, fact, experience or preference from a search
  hit or handoff. Editing creates a revision; correction supersedes a prior
  statement; archive records are not rewritten to match the new interpretation.
- Add optional background extraction into typed draft candidates. Preserve the
  actual speaker, tool outcome and scope. Ordinary task instructions and quoted
  documents do not automatically become permanent user preferences.
- Drafts may improve a labeled recap, but only active memory is returned as
  settled project knowledge. Global activation is an explicit user operation.
- Add an engine-neutral extraction interface. The first implementation uses a
  configured OpenAI-compatible model through CPA; avoid introducing a mandatory
  Python service just to generate a small schema. Hy Memory pro can later be
  evaluated as another implementation if its experience extraction is better.
- Tag internal extraction requests and exclude them from recursive archive-to-
  memory processing. Store source citations and extraction status separately.

Proposed starting limits, configurable and validated before enabling automatic
model calls: one model job in flight, at most 8,000 input/1,000 output tokens per
job, at most 20 jobs/day, and shared daily reservations of 100,000 input/10,000
output tokens. The first exhausted limit stops new work. Cached unchanged input
does not start another model job. A pause switch stops new calls; an uncertain
post-call outcome is not blindly retried. Report token usage and account quota
consumption; show monetary estimates only when an actual price is known.

Acceptance:

- Every active/generated memory cites live, in-scope evidence; invalid source IDs
  or malformed extraction JSON are rejected, not silently repaired into facts.
- Conflicting claims are shown as conflicts or superseded versions, with the
  decision trace retained. A user correction changes subsequent recall.
- Duplicate job execution does not create duplicate memory versions or unbounded
  provider calls. Budget reservation is atomic under concurrent workers.
- On a labeled 20-item extraction corpus, at least 18 draft items are accurately
  attributed and supported. Any unsupported item stays inactive; all 20 source
  references resolve. Publish error examples rather than only an aggregate score.

### Slice 4: semantic retrieval, only when it earns its cost

Deliverables:

- Use missed questions from the lexical evaluation, not an arbitrary vector
  database migration, as the reason to add embeddings.
- Embed deduplicated messages and active memories selectively, never every
  repeated protocol snapshot. Track provider/model/version/dimensions and chunk
  version. An engine change schedules a rebuild rather than mixing vectors.
- Add lexical/vector rank fusion behind a feature flag. Keep literal error codes
  and identifiers searchable. Select the vector implementation after a small
  macOS arm64 compatibility/performance test; no PostgreSQL/graph service is
  required by this plan.
- Keep ai-memory and Hy-Memory as bounded alternative experiments if they reduce
  implementation or improve recall. They must pass this same corpus, scope,
  deletion and recovery contract before becoming a product dependency.

Acceptance:

- On the same frozen 30-query corpus, improve top-5 recall by at least 3 queries
  without reducing the exact-query score. If already near the ceiling, require
  a documented paraphrase-corpus gain instead. Otherwise retain lexical default.
- Benchmark 10,000 indexed chunks on the Mac mini. Initial target: search p95
  below one second, with capture enabled, measured over at least 100 queries.
  Report actual memory/disk and model download size. Targets are provisional.
- Turning embedding off preserves lexical search and existing active memory.
  Model/provider failures do not block proxy requests or corrupt archive writes.

## Cross-cutting reliability and lifecycle

### Accepted data versus observed traffic

Add counters for received, durably committed, queued, dropped, partial, retried,
dead-lettered and indexed records. The dashboard reports gaps explicitly.

Before promising restart-safe plugin delivery, add a negotiated durable collector
ACK: the background sender may discard a queued record only after its archive
transaction is committed. The legacy 202 path is labeled queue-only. Any new
plugin spool must remain bounded and off the response critical path; a worker
crash before the spool commit remains a visible gap. No design here promises
zero loss under arbitrary crashes without a synchronous durability cost.

Roll out loss visibility with Slice 1. Implement durable ACK/spooling as a
separate gated reliability increment; do not delay useful search while claiming
that the existing plugin queue has become durable.

### Deletion, scope changes and rebuild

- Authoritative tombstones and scope revisions live with the archive/control
  state. Search/MCP/source reads recheck current visibility, not only an index.
- Deletion immediately prevents reads. Background cleanup removes normalized
  messages, snippets, summaries, vectors and derived candidates. A stale worker
  cannot publish them again; shared derived memory loses invalid citations and
  becomes inactive if no valid evidence remains.
- Project reassignment invalidates old-scope results before rebuilding the new
  scope. Backups have a separately documented retention policy; logical deletion
  is not a claim of physical erasure from backups.
- `memory.sqlite` is rebuilt from retained evidence, but user edits, confirmations,
  corrections and tombstones are authoritative curation data. Back them up or
  record an append-only curation journal; do not call the entire DB disposable.

### Processing limits and observability

Start with one worker, paged reads and bounded per-job input. Oversized payloads
and unsupported media get a visible partial state. Avoid loading the whole
archive or arbitrarily large tool results into memory or a model prompt.

Expose processing lag, oldest pending age, failures, retry count, candidate
status, search coverage and per-job model usage. Logs contain IDs and errors,
not full prompts, secrets or raw access keys. Memory output is quoted historical
data and never overrides the current user or repository instructions.

## Migration, release and rollback

1. Complete review and baseline checks on the existing storage branch. Use a
   versioned branch/release for each accepted slice; do not combine all slices
   into one unreviewable change.
2. Make an online archive backup and verify restore before additive schema work.
   Keep data and binaries separate. Register schema versions and migration tests.
3. Build and test a Mac mini candidate against a copy/disposable corpus. The
   live service is not an integration-test fixture.
4. Enable normalization/search with model work off. Backfill newest sessions
   first with a recorded fence and idempotent overlap reconciliation, then older
   sessions. Show how much history is actually searchable.
5. Validate Web and MCP with two clients, then replace the production collector
   using a tested rollback procedure. Verify old-binary reopen compatibility;
   never assume an additive migration is automatically safe for old code.
6. Enable sourced memory and later model/embedding features separately. On
   failure, pause workers and recall features while leaving archive capture
   running. Rebuild indexes without restoring old OAuth credentials or deleting
   the current archive.

## Verification and definition of done

- Unit: protocol parsers, history/branch identity, CJK lookup, source references,
  curation state, ranking, budget reservations and extraction-loop exclusion.
- Integration: commit/outbox atomicity, killed-worker replay, upserts during
  backfill, source/scope invalidation, snapshot exports, backup/restore and old
  database migration. Use controllable clocks and deterministic gates.
- Runtime: Go test/race/vet/build gates, macOS arm64 plugin load, collector
  HTTP capture, authenticated Web, MCP handshake and actual two-client recall.
- Product corpus: at least 50 sanitized sessions spanning two clients and two
  projects, including a concurrent branch, retry, compaction, interrupted stream,
  repeated history, correction and missing-source case. Label the 30 queries and
  expected citations before tuning. Never commit real private conversations.
- Regression budget: measure the same proxy workload before/after; initial
  target is under 10% p95 added latency with processing enabled. If exceeded,
  reduce processing pressure or separate the worker before release.

The first release is done when the user can search retained project history,
open the cited source, and use another client to resume the selected task, with
partial coverage and failures visible. The complete product is done only when
curated memory can be corrected/deleted, recovery is verified and optional
semantic/model features meet the same provenance and scope contracts.

## References

- [Collector/storage refactor plan](2026-10-16-collector-storage.md)
- [ai-memory evaluation](../ai-memory-integration-evaluation.md)
- [Hy-Memory / MemHub evaluation](../hy-memory-integration-evaluation.md)
- [Storage interfaces](../../internal/storage/storage.go)
- [Archive record model](../../internal/archive/types.go)
- [Protocol timeline parsing](../../internal/archive/timeline.go)
- [Current collector and writer](../../cmd/cpa-session-collector/main.go)
