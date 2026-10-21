# Memory architecture and storage boundaries

## Responsibilities

The system has three independent authorities:

| Layer | Owner | Contract |
| --- | --- | --- |
| Retained evidence | Collector archive backend | Record/CAS transaction, source revisions, ordered change feed, stable snapshots |
| Derived retrieval | Collector memory projection | Scoped search, revision-checked excerpts, bounded recap, resumable indexing |
| Reviewed knowledge | Existing canonical Git repositories | Explicit proposals, operator approval, correction and archive |

`internal/storage.Backend` is the archive persistence boundary. Its optional
`MemoryFeed` change/source capability feeds indexing. A new archive backend must
implement snapshot and change ordering, not just CRUD. SQLite remains the only
bundled archive implementation.

`internal/memory.Reader` composes `SearchReader`, `SourceReader` and `StatusReader`.
HTTP and MCP accept this read capability; they do not own SQL connections or
indexing. `Projection` adds collector-owned Run/Close lifecycle. The composition
root still selects SQLite explicitly with `memory.Open`; no plugin registry,
backend configuration matrix or speculative generic SQL repository is needed.
An alternative engine can implement Projection once an evaluated use case exists.

`internal/memory.Knowledge` is separate from the projection. It supports reading
and proposing, never operator review. Git commits remain authoritative; SQLite
does not acquire confirmation state. A derived index may be discarded only
without discarding authoritative evidence or Git state.

## SQLite concurrency and status

The projection owns one write connection and a pool of at most four read-only
connections. Only the collector worker mutates content. WAL allows reads during
projection transactions; a bounded pool limits page cache and concurrent query
pressure. Candidate rows are closed before archive source validation. Source
revision and scope validation remain live reads, never a cached permission result.

Candidate selection materializes at most 1,001 occurrence IDs and sort keys
before loading text. This avoids reading large shared payloads for every matching
occurrence that will be discarded by the sort. Match rules, ordering, the 1,000
candidate cap, truncation reporting and source citation semantics are preserved.

Broad lexical queries first probe at most 1,024 matching unique text IDs. When
that probe is full, a recent-source path considers the newest 4,096 sources and
only ordinary-text rank candidates. The additive `idx_memory_event` index makes
source selection ordered. A count guard in the same SQL statement accepts this
path only if it produces the complete 1,001-candidate prefix; otherwise search
runs the original full query. Sources outside that recent window necessarily
sort after this complete rank-zero prefix. Summary/tool ranks, sparse results,
older matches and scope filters retain the full-query fallback. The probe is a
planning hint, never a permission or freshness cache. Returned sources still
undergo live revision validation. No projection or normalizer version changes.

Corpus counts are stored in the additive `corpus_stats` table. Triggers maintain
sources, partial sources, chunk occurrences and unique text in the same
transaction as inserts, deletions, cascades and partial-state updates. Counts
are exact committed aggregates, not approximate cache values. Status reads a
single aggregate row instead of scanning the corpus on every search.

The small `coverage_groups` extension groups stored diagnostics with transactional
source triggers. Counts, metadata state and coverage use one read snapshot.
Both aggregate extensions recalibrate when their maintenance triggers are missing
after a legacy rebuild. Collector-owned delivery counters attach through a read
decorator outside the engine; scoped readers omit global diagnostic objects.
See [coverage and ingestion diagnostics](memory-observability.md) for exact
counting, ambiguity, epoch and durability semantics.

Opening an existing v3 projection initializes these counts once transactionally.
It does not rebuild FTS, change normalizer v2 citations, or change projection
version 3. A projection rebuild also resets aggregates. A normal older v3
collector continues to update aggregates through triggers; do not assume an
older collector's own schema rebuild preserves this extension. Reopen with the
new collector to initialize aggregates after restoring an older projection.

The initial aggregate scan is proportional to corpus size and must be measured
on an isolated online snapshot before deployment. Additional trigger updates
increase write work; test indexing throughput as well as query latency.

## Stability invariants

- Projection/checkpoint changes commit together; retries are idempotent.
- Equal text may share storage but retains independent source occurrences.
- Uncommitted index changes are invisible to readers.
- Source invalidation and project/session authorization apply to all retrieval
  implementations, including any future vector candidate path.
- Startup repair work lists are derived hints: missing/deleted records are
  skipped, including deletion after enumeration. They must not block readiness
  or recreate deleted sources.
- Historical output is evidence, not instructions. Generated summaries and
  proposals cannot activate reviewed memory.
- Legacy in-memory plugin/collector acceptance and HTTP 202 are not durable.
  An enabled producer outbox retains committed local entries until a matching
  archive commit receipt, including process restart and uncertain responses.
  Pre-spool capture and SQLite NORMAL archive power-loss behavior remain separate
  gaps; no storage abstraction claims to recover traffic lost before persistence.
  See [delivery boundaries](durable-delivery.md).

Delivery admission uses a small generic count/weight queue. Reservations survive
dequeue, storage retry and HTTP retry until the consumer releases the item.
Archive-specific retained-weight estimation belongs to the archive package, not
the queue or storage backend. Collector decoding has a separate concurrency
bound. These controls bound pending payload retention without pretending to bound
active capture or total RSS. Process-local producer observations remain delivery
diagnostics attached outside the projection engine.

Online archive backups pin a read transaction across backup steps, preventing
concurrent commits from repeatedly restarting the copy. WAL writers remain
available; the pinned snapshot temporarily retains WAL history until copying
finishes. The configured backup deadline bounds the copy lifetime. Integrity
verification runs on the completed copy after the source snapshot is released.

## Choosing another store

First measure end-to-end search, source reads, status, indexing throughput,
connection wait, backup/restore and physical table/index sizes. Use the same
private labeled query set for every candidate, with synthetic committed fixtures.

An independent search engine is justified by measured relevance or latency
improvement after query/index optimization. PostgreSQL is justified by actual
multi-writer/multi-user requirements or a demonstrated SQLite limit. Object
storage requires a measured attachment/cold-data benefit and a consistent
metadata/blob backup protocol. Vector retrieval requires a labeled paraphrase
gain and bounded storage/model cost. None is enabled by this change.

## Verification and rollout

Regression tests compare aggregates with actual tables after replacement,
shared-text deletion, rollback, migration and projection rebuild. A held write
transaction must not block search or expose uncommitted state, and the read pool
must reject writes. Existing source/scope/citation tests remain required.

Run full Go tests, race checks, vet and native builds. Preflight production-sized
snapshots separately, preserve a verified raw/projection rollback pair and old
artifacts, and follow the M4 deployment runbook. Collector-only updates leave CPA
running. Record actual live latency, index lag and new archive commits after
replacement; passing unit tests alone does not establish deployment success.
