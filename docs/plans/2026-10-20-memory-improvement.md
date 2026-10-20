# Memory improvement roadmap

Date: 2026-10-20
Status: foundation deployed; remaining increments proposed.

Implementation update: the architecture/performance foundation and two stability
fixes have since been implemented and deployed. See the
[architecture contract](../memory-architecture.md) and
[verified rollout](../mac-mini-deployment.md#memory-performance-and-stability-rollout-2026-10-20).
The remaining quality, ranking, continuation and extraction increments below
remain planned. The baseline section records the state before these changes.

## Outcome and authority

Make a fresh client find a project's reviewed decisions, recover the last
evidence-backed task state, and propose useful corrections with little manual
search. Keep archive.sqlite as retained evidence, memory.sqlite as rebuildable
retrieval data, and existing reviewed Git repositories as curated authority.
Do not introduce a second authoritative memory database.

## Observed baseline

Local collector status during inspection: 139,596 sources, 17,118,702 chunk
occurrences, 3,227,857 unique chunks, 129,048 partial sources, backfill and message
metadata complete, lag 1, failures 0. These are one live snapshot, not an SLO.
Database files were approximately 18 GiB raw and 15 GiB derived, excluding WAL.
One status request took about seven seconds end to end; repeat controlled
measurements before attributing this to database counts or contention.

Existing capabilities include revision-checked citations, Chinese-aware FTS4,
native history adapters, read-only MCP, and optional Git-backed proposals,
operator approval, correction and archive. Optional Git configuration was not
audited in this inspection; implementation does not establish client adoption.

Search currently requires all whitespace-separated literal terms in one chunk,
caps candidates at 1,000, orders text before summaries before tools and then by
request time, and suppresses overlaps within a message rather than repeated
context across requests. Recap is a maximum 8 KiB excerpt packet, not structured
task state. Search also calls Status, whose implementation performs corpus counts.
The projection has one database connection shared by its operations.

Partial classification combines intentional omissions and unsupported content.
The most common exact issue combination was opaque/unsupported Codex response
items (79,056 sources). This does not establish how much useful text is missing.
Plugin queue overflow currently has an empty default branch, and collector 202
acknowledges volatile queue admission rather than a committed archive record.

## Increment 1: quality and performance baseline

1. Add stable coverage reason codes separating intentional exclusions, unknown
   types, parse failures, missing payloads and actual truncation. Preserve old
   citation semantics; parser changes require explicit normalizer version handling.
2. Count received, enqueued, dropped, committed and indexed records at their
   actual boundaries. Show process epoch, queue depth, lag and last success.
   Counters across restarts must not imply a durable reconciliation ledger.
3. Measure search/source/status p50 and p95 under concurrent capture at production
   scale using metadata-only reports. Separate candidate query, source validation,
   counts and connection wait time. Optimize status with measured, bounded cached
   aggregates, without caching source visibility decisions.
4. Create a private owner-controlled evaluation corpus with 50 questions covering
   exact errors, paths, Chinese, paraphrases, old decisions, corrections, concurrent
   branches and missing evidence. Commit synthetic fixtures and aggregate results
   only. Label expected sources before tuning.

Gate: every sampled partial item has an actionable category; no cross-scope or
stale citation is returned; publish baseline recall@5, latency, disk and ingest
loss visibility. Initial performance target is search p95 under one second on
the actual corpus, subject to measurement and explicit revision.

## Increment 2: useful retrieval

Add optional kind/role/tool filters and ranking within authorized candidates.
Keep default literal compatibility; offer an explicit broader retrieval mode
with phrase/identifier signals and relevance ranking. Evaluate FTS4-compatible
BM25-style ranking before changing FTS engines. Distinct query intents can favor
tool results for diagnosis and user decisions for project continuity.

Group repeated equal text for display within scope while retaining all source
occurrences. Select a representative citation with explicit first/last seen and
expandable occurrences; never merge event identity by text. Ensure candidate
selection itself has diversity so 1,000 repeated snapshots do not hide older
useful evidence. Project alias bindings require explicit owner-reviewed identity
mapping and scope invalidation; a path basename is never a project identity.

Gate: improved frozen-corpus recall@5 without loss on exact errors/paths;
unchanged citation and scope tests; p95 measured with capture enabled.

## Increment 3: project continuation

Add an explicit project/branch/session selector and a bounded continuation packet
combining reviewed knowledge, cited recent evidence, failed attempts, unresolved
questions and suggested next actions. Distinguish user goals, model claims and
observed tool outcomes. Unknown facts stay unknown. A model saying tests passed
is not a verified test result. Any generated summary is marked generated, carries
its input watermark, and is invalidated or refreshed when sources change.

Start with deterministic assembly and on-demand optional summarization. Preserve
the existing recap API or add a versioned tool rather than silently redefining it.
Respect an explicit byte/token budget, reserve space for citations, and report
omitted sections. Provide copy and MCP access. Do not rewrite native history or
inject into every proxy request.

Gate: a fresh Codex session finds the last verified state and continues a selected
task without repeating its explanation. Cover failed checks, forks, stale source
reads and absent evidence. Use Codex for acceptance; do not invoke Claude.

## Increment 4: practical reviewed knowledge

Improve the existing proposal workflow with source comparison, applicability,
expiry/review reminders, correction diffs and explicit conflict links. Drafts
stay separate from active memory. Review remains an operator-only capability.
A workstation-local review UI/CLI may operate on existing Git roots; do not add
a collector approval endpoint with no access to canonical workstation Git.

Add optional on-demand draft extraction first. A later worker must use durable
jobs keyed by source revisions, extraction version and scope, validate citations,
exclude internal requests, and bound concurrency, tokens and daily calls. An
uncertain provider outcome is reconciled rather than retried without limits.
Extraction output is a proposal, not proof. No automatic approval or Git push.

Gate: idempotent proposals, correction without premature activation, explicit
conflict handling, dirty-work preservation, evidence invalidation and budget tests.

## Increment 5: semantic recall and durable transport

Add embeddings only if labeled paraphrase misses justify them. Index unique
eligible text and reviewed memory, track model/version/dimensions, and intersect
with current authorization before exposing results. Fuse lexical/vector ranks;
retain literal fallback and source revalidation. Require a documented gain on
the same evaluation set and bounded incremental storage and model cost.

Durable transport is an independent reliability slice: negotiate commit ACK,
use idempotent record identities and bounded background spool/replay, and test
crashes before/after commit. Never claim zero loss before asynchronous spool
commit. Promote this slice earlier if measured drops or operational requirements
justify it. Do not couple embeddings to transport changes.

## Delivery and validation

Deliver separate reviewable changes: coverage/metrics; status performance;
ranking/diversity; continuation packet; review usability; extraction; optional
vectors; optional durable delivery. Keep the plugin thin and background work
outside model response processing. No new graph/vector service is presently
justified by the evidence.

Implementation requires relevant unit/integration tests, corpus measurements,
Go build/test checks and real Codex acceptance where applicable. Before a live
replacement, follow AGENTS.md and the M4 runbook: verify hardware, create and
verify online backups, preserve the newest complete rollback pair, stage tested
artifacts and check live capture/recall. Collector-only changes do not require a
CPA restart. This planning document does not perform or claim deployment.
