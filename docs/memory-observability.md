# Coverage and ingestion diagnostics

## Coverage is not a loss percentage

`partial_sources` remains backward compatible: it includes intentional exclusions,
compaction, unsupported content, malformed or missing payloads, truncation and
upstream failures. It must not be presented as a percentage of lost conversations.

Owner-wide recall status now includes a versioned `coverage` summary. Each reason
has a stable code, category and affected-source count. A source contributes once
to each applicable reason, so reason counts are not additive. Sources with only
intentional exclusions/compaction are counted separately from sources requiring
investigation. Partial sources without a recorded explanation are counted as
unclassified, not silently treated as complete.

| Category | Meaning |
| --- | --- |
| `intentional` | Known reasoning, media, tool declarations or internal requests intentionally excluded from text indexing |
| `compacted` | Compacted context retained without replaying replacement snapshots |
| `missing` | Required retained payload or completion message unavailable |
| `invalid` | Payload parsing/shape failure |
| `truncated` | Retained payload cut off |
| `unsupported` | Known unsupported protocol or content |
| `upstream_failure` / `incomplete` | Failed upstream interaction or incomplete stream |
| `ambiguous` / `unknown` | Existing diagnostics do not establish the cause |

Legacy “opaque or unsupported” diagnostics stay ambiguous. The summary describes
diagnostics saved at the source's last projection. Fresh source reads and new
projections distinguish known reasoning/media from unknown types. The existing
resumable metadata worker refreshes ambiguous historical diagnostics in bounded
batches when change ingestion is caught up; `message_metadata_done` reports
completion. Coverage-only refreshes update source diagnostics and a per-source
policy marker, without rewriting existing message metadata or FTS text. Sources
with already definitive reasons need no raw payload reread. No historical
reclassification is added to the request path. Checked source responses also include
`coverage_issues`; legacy human-readable `issues` remain available.

`coverage_groups` is a small transactional aggregate over stored source diagnostics.
Its initial migration reads source metadata, not archive payloads or FTS text.
Source insert/delete/update triggers maintain it, including writes by compatible
older collectors. Missing maintenance triggers cause aggregates to be recalibrated
on startup, covering older projection rebuilds. Projection v3 and normalizer-v2
message text, pointers and citation ranges remain unchanged.

Corpus counts, metadata state and coverage are read in one WAL transaction, so a
concurrent commit cannot mix counts from one projection snapshot with reasons
from another. The raw archive watermark is sampled after releasing that read
transaction. This does not establish an atomic cross-database snapshot.

## Collector delivery boundary

Collector diagnostics are available at the existing authenticated recall status
boundary and at `GET /v1/ingest-status` on the ingestion listener. The latter
contains operational counters only and has no authentication; keep the ingestion
listener on loopback and do not expose it remotely. Read clients should use the
authenticated recall API or MCP bridge.

The optional `ingestion` object identifies its boundary as
`collector_http_ingest` and its acknowledgement as `in_memory_queue`:

| Field | Meaning |
| --- | --- |
| `epoch`, `since` | This metrics lifetime; counters restart with the collector |
| `received_requests`, `invalid_requests` | Ingest endpoint requests and rejected method/body validation |
| `enqueued_deliveries`, `queue_rejected` | Valid delivery attempts admitted to or refused by the queue |
| `committed_deliveries` | Entries in batches whose storage write returned success |
| `discarded_deliveries` | Entries abandoned by the writer after a non-retryable error; not proof of a backend's unknown commit outcome |
| `write_errors`, `retries` | Failed write attempts and retryable failures scheduled for another attempt |
| `truncated_deliveries` | Enqueued entries marked truncated |
| `queue_depth`, `buffered`, `write_in_flight` | Approximate pending-work gauges; queue observation is not an atomic storage snapshot |
| `last_commit_at` | Last observed successful batch write |

Counters describe delivery attempts, not unique archive records. Idempotent
replays can increase accepted/committed counts without increasing corpus size.
Native history imports bypass this HTTP boundary. Counters are process-local
diagnostics, not a durable loss ledger and not an acknowledgement upgrade.
HTTP 202 still precedes archive commit. A crash can lose admitted work, and a
queue rejection may later succeed through sender retries.

`producer_observed=false` means no valid producer report has been observed in
this collector epoch. With a compatible plugin, `producer` contains the latest
process-local capture snapshot and its collector `received_at` timestamp. Only
the most recently reporting producer is retained; this is not an all-producer
aggregate. Reports travel in the bounded `X-CPA-Archive-Capture` header and are
best-effort loopback telemetry, not authenticated delivery receipts. There is no
heartbeat: a report may become stale when no records are delivered. The snapshot
precedes the current HTTP acknowledgement, so `http_accepted` can lag the current
delivery. Zero collector discards must not be reported as end-to-end zero loss.

Producer counters distinguish count-limit, byte-limit, oversize and invalid
record drops, permanent HTTP rejections, retries, and shutdown discards. Their
epoch resets with the plugin process. New explicit model-catalog callbacks are
skipped rather than archived as empty conversations; historical records are not
reclassified or deleted by this filter.

Both delivery queues bound retained record weight, including dequeued entries
waiting for a write or retry. Plugin `queue_max_bytes` defaults to 268435456;
collector `ARCHIVE_QUEUE_MAX_BYTES` defaults to 536870912. These are estimated
payload/metadata budgets, not total process RSS limits. Active request capture,
HTTP serialization and database caches have separate allocations. Lowering the
plugin limit does not evict already admitted entries; new admissions wait for
the retained work to drain below the limit.

Collector JSON decoding concurrency is bounded by `ARCHIVE_INGEST_CONCURRENCY`
(default 2, range 1–64). Busy decoding and temporary queue pressure return 503;
a single record exceeding the queue budget returns 413. Status exposes
`busy_rejected`, `byte_rejected`, `oversize_rejected`, `queue_bytes` and
`queue_max_bytes`. The plugin retries temporary delivery failures and treats
400/413/422 as permanent rejections. None of these limits upgrades the volatile
202 acknowledgement. The separately enabled [producer outbox](durable-delivery.md)
requests `commit-v1`, checks `committed-v1` and the exact body digest, and retains
pending bytes until receipt verification. Its snapshot, capacity waits, read/write
errors, quarantines and verified commits are nested in the latest producer report.
The collector `acknowledgement` field still describes the default legacy mode;
legacy and opt-in deliveries share admission/commit counters. A current report
precedes its own receipt, so it can still count that delivery as pending.

## Read and authorization architecture

The collector owns counters; the projection engine does not depend on collector
implementation or HTTP queues. `memory.WithIngestionStatus` decorates the existing
read capability for status/search/recap, and credential scoping wraps that
decorator. Project/session credentials omit both global coverage and ingestion
objects, including through HTTP and MCP. Per-source coverage still accompanies
authorized source reads.

The Web page explains these boundaries and handles servers without the new
optional fields. A newly started `cpa-memory` binary preserves these fields over
MCP; older bridge processes may ignore additional backend fields until restarted.
No client configuration or reviewed-Git authority changes are required.

## Verification

Regression tests cover conservative classification, duplicate-reason counting,
known opaque exclusions without message/citation changes, metadata-only migration,
updates/deletions/rollback, lost-trigger recovery and consistent status snapshots.
Collector tests synchronize before commit to distinguish admission from durable
success and exercise refusal, truncation, retry, discard and epoch reset.
Owner/project/session tests cover all three status-bearing read paths and HTTP
omission. Full Go race/vet checks and a Chromium smoke test cover owner/scoped
rendering, authentication errors and legacy responses.

Follow the M4 runbook for verified backups, isolated preflight, native artifact
replacement and live capture/MCP checks. Collector/bridge updates do not require
a CPA restart. Deployment results belong in the deployment runbook and private
manifest; this document defines behavior rather than asserting a rollout.
