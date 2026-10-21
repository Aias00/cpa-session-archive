# Offline memory extraction and incremental tasks

This is development-only batch functionality. It does not configure a client,
install an artifact, start a background worker, or approve knowledge. Existing
collector, archive, projection, outbox and reviewed Git data stay separate.

The new `cpa-memory-extract` command connects three stages:

1. Read exact project/session evidence and generate attributed candidate reports.
2. Compare a bounded snapshot of reviewed project knowledge and suggest new,
   correcting, replacing, conflicting or duplicate claims.
3. Durably schedule changed sources, reserve budgets and recover interrupted work
   without automatically repeating ambiguous model calls.

## Evidence and attribution

Inputs come from an explicit archive feed or an offline `Bundle`. Every source
snapshot contains an `archive.Record`, its exact archive revision and ingestion
sequence. The offline bundle format is:

```text
{"archive_id":"<stable archive identity>","watermark":123,
 "sources":[{"Record":<archive.Record>,"Revision":"<sha256>","Sequence":123}]}
```

The revision uses the same canonical Go record serialization as
`archive.MemorySource`, excluding the user-maintained principal display alias.
Arbitrary IDs, bodies or revisions are not repaired into valid citations.
`Bundle` is an immutable set of current snapshots, not a complete historical
change log or proof that deleted conversations never existed. Reordered bundle
arrays do not change its stable backfill order. Ordinary raw JSONL exports need
conversion to the source-snapshot contract; they are not accepted as bundles.

Selection requires an exact project ID and optionally an exact session within
that project. Project names and directory names are not identities. Source
roles and message kinds come from the versioned normalizer, independently of
the extraction model. System/developer context, summaries, tool invocations,
and requests marked `memory` or `memory-extraction` are excluded. Tool results
remain separate from an assistant's report about a tool.

Each accepted candidate retains its literal supporting quotes and normalizer-v2
citations, with Unicode rune offsets. The source adapter reconstructs input
windows before dispatch and output validation. It rejects substituted text,
speaker roles, scope, versions or window identities. Nonblank excerpts preserve
their original offsets even when adjacent blank-only spans are skipped.

| Attribution | Allowed direct supporting evidence |
| --- | --- |
| `user_requirement` | User text |
| `user_report` | User text, retained as a report |
| `assistant_suggestion` | Assistant text, retained as a suggestion |
| `assistant_report` | Assistant text, retained as a report |
| `execution_result` | Retained tool-result text |

The proposal statement preserves this attribution as a prefix. Neither a model
summary nor an assistant saying "completed" establishes actual execution.
Validation proves exact grounding and actor boundaries; it does not mechanically
prove semantic entailment, whether quoted material is true, or whether a user
accepted a suggestion. Every accepted candidate still requires human review.

## Knowledge comparison and review

With `--project-root`, the existing Git knowledge adapter provides the committed
project lifecycle view. Eligible exact ID/revision pairs are sent to the model.
The comparison is bounded by source/result/input limits. Omitted knowledge is
disclosed through `bounded_knowledge_comparison`; absence from that input does
not prove a claim is new. Persisted lifecycle, read-time validity and review dates
remain visible in the comparison, so a review-due claim is not silently relabeled
as currently eligible. No global knowledge is activated by extraction.

The allowed suggestions are `new`, `corrects`, `supersedes`, `conflicts_with` and
`duplicate`. Relationship targets must be eligible entries in that exact scope
and bind the supplied revision. Exact statement duplicates can be suppressed
mechanically. Similar wording alone does not retire or merge an entry.

`run` generates reports in the private task ledger. It does not write Git.
`stage --job-id` separately revalidates source evidence and calls only the
existing `Knowledge.Propose` capability. Staging creates Git drafts, never active
knowledge. Repeating staging reconciles the same candidate ID/operation and
does not create another model call or Git commit. A changed candidate requires
operator inspection rather than silent overwrite. Git and the task ledger are
not one atomic transaction; durable model output plus proposal idempotency
provides recovery between them.

Approval remains the separate human operator workflow in
[reviewed Git memory](curated-git-memory.md). It rechecks candidate and target
revisions again. The [validity and relationship rules](memory-validity.md),
including preservation of retired IDs and unresolved conflict exclusion, still
apply. The model-facing extraction interface has no approval capability.

## Explicit CLI batches

Use a dedicated absolute task DB path under a private `0700` directory. The DB,
its operational lock and private output files use `0600`. Existing archive,
projection, outbox, unrelated SQLite DBs, unsafe links and broad permissions are
rejected before database initialization. Do not point `--state` at a business DB.

The following is a reference invocation, not an instruction to enable the client
integration during development:

```text
go run ./cmd/cpa-memory-extract scan
  --state /absolute/private/extraction/jobs.sqlite
  --bundle /absolute/private/source-snapshots.json
  --project-id my-project --session-id selected-session
  --project-root /absolute/canonical/project
  --provider-endpoint http://127.0.0.1:8317/v1
  --model explicit-compatible-model
  --model-token-file /absolute/private/model.token
  --scan-pages 1
```

`scan` makes no model calls. It still binds jobs to the explicit provider/model
profile so work for different configurations cannot be mixed. Use `run` with the
same options and `--max-jobs 1` to generate a bounded batch. A new `--output` path
writes the private candidate reports for inspection. Default stdout contains
only job IDs, counts, coverage and operational metadata. Existing output files
are never overwritten.

Instead of `--bundle`, an operator can explicitly supply `--archive-endpoint`
and `--archive-token-file`. The existing feed client requires a literal-loopback
HTTP origin and a dedicated archive-feed credential. The feed currently pages
global source IDs, then the extractor filters exact project/session scope before
model input. This is not server-side payload filtering. A project recall token
does not automatically grant archive-feed access.

Control and inspection commands require only the task DB:

```text
go run ./cmd/cpa-memory-extract status --state /absolute/private/extraction/jobs.sqlite
go run ./cmd/cpa-memory-extract list --state /absolute/private/extraction/jobs.sqlite
go run ./cmd/cpa-memory-extract pause --state /absolute/private/extraction/jobs.sqlite
go run ./cmd/cpa-memory-extract resume --state /absolute/private/extraction/jobs.sqlite
go run ./cmd/cpa-memory-extract show --state /absolute/private/extraction/jobs.sqlite
  --job-id exact-job-id --output /absolute/private/new-job-report.json
```

`show` requires an output file because full jobs contain retained quotations.
`list` is bounded to 100 job summaries; `status` reports aggregate state/budget
counts. Neither command prints credentials or full evidence by default.

To stage reviewed candidates, use the same source selection and canonical root:

```text
go run ./cmd/cpa-memory-extract stage
  --state /absolute/private/extraction/jobs.sqlite
  --bundle /absolute/private/source-snapshots.json
  --project-id my-project --session-id selected-session
  --project-root /absolute/canonical/project
  --job-id exact-job-id
```

No model configuration is needed for staging saved output. An unavailable or
changed source blocks staging. A saved report recovered after its source became
unavailable remains historical ledger evidence and is not returned as a fresh
generated report. It is still available through explicit private inspection.

## Incremental discovery and deduplication

The durable cursor binds archive identity, extraction/provider configuration,
project/session selection, backfill position and ingestion sequence. Source
versions, exact evidence, comparison knowledge and the provider fingerprint are
included in each job key. Model, prompt or comparison changes cannot silently
reuse a report from another input. Identical retries reuse the same job.

Discovery records all selected inputs and its next cursor in one SQLite
transaction with compare-and-swap. A failed registration cannot advance the
cursor past unregistered work. Pending jobs and generated results are separate
from discovery progress; reaching a source watermark does not mean every job
ran or that a human approved anything. Worker queries filter project/session and
provider before their result limit, so unrelated queued work cannot starve them.

Initial backfill uses the archive's row cursor; later processing follows the
committed change feed. A scan fence bounds events, not historical source
versions. A source already newer than that fence is deferred, with partial
coverage retained. Transport/auth failures never become deletion or successful
empty evidence. Identity replacement and watermark rollback are rejected.
`caught_up` means that the declared event fence was enumerated; `complete` also
requires the bounded source operations to have no recorded coverage gaps.

The CLI starts no scheduler. Repeated explicit batches resume persisted work.
To deliberately reprocess an entire scope, use a new isolated task ledger or a
changed provider profile; do not edit a cursor or delete pending work as cleanup.
Retired/changed inputs remain recorded as failed tasks. A full rescan is needed
to re-establish coverage after a previously recorded permanent source gap.

## Budget and failure policy

The default CLI has fixed conservative limits. The package accepts explicit
limits when creating a ledger, within its per-call and attempt hard caps;
persisted settings cannot silently change when
another handle or a later process opens it.

| Limit | Default |
| --- | --- |
| In-flight model calls per ledger | 1 |
| Input reservation per call | 8,000 |
| Output reservation per call | 1,000 |
| Call reservations per UTC day | 20 |
| Daily input reservations | 100,000 |
| Daily output reservations | 10,000 |
| Attempts per job | 3 |

The first exhausted limit stops dispatch. Reserving the full 1,000 output units
allows at most ten attempts under the daily output limit. Input reservations
use actual serialized request bytes plus overhead, including JSON escaping;
they are conservative bounds rather than tokenizer counts. Default source
windows are at most 2,600 JSON bytes, and the complete provider request is checked
again before dispatch. Measured successful usage is recorded separately.
Unknown usage is not a zero-cost assumption and never refunds the reservation.
No monetary estimate is invented without actual prices.

Reservation, attempt fencing, daily budgets and pause checks are transactional.
Each possible provider attempt consumes a fresh reservation, including a safe
retry. The single worker uses an OS file lock, not an elapsed lease, to establish
that another compliant local worker cannot still be authorizing calls. Opening a
control handle does not recover a live worker. After process exit, an undispatched
reservation may be resumed conservatively; a dispatched call becomes uncertain.

Only definite retryable provider rejections can enter capped exponential backoff.
Timeouts, disconnects, malformed/incomplete successful responses and unknown
outcomes remain `uncertain` and stop unattended dispatch for that ledger.
An operator can inspect the private job, then explicitly choose to abandon or
retry its exact attempt:

```text
go run ./cmd/cpa-memory-extract resolve
  --state /absolute/private/extraction/jobs.sqlite
  --job-id exact-job-id --expected-attempt inspected-attempt
  --resolution abandon --confirm-uncertain
```

`retry` instead of `abandon` acknowledges possible duplicate processing/billing.
It does not refund the old reservation or bypass subsequent attempt/day limits.
Pause stops new dispatch; already generated results remain inspectable and can
be staged. Internal extraction calls carry CPA's existing structured request
tag so their input/output do not recursively become new archive candidates.
See [the provider contract](memory-extraction-provider.md) for protocol details.

## Verification and practical limits

Isolated tests cover normal and native histories, Unicode citation readback,
source/role tampering, invalid attribution promotion, typed relation suggestions,
source changes, staged replay, human approval, model-output recovery, scope-first
queue selection, concurrent reservations, pause/restart, safe retries and
uncertain-call reconciliation. They also check path/DB preservation, source
coverage limits, wire escaping and private CLI output. Twenty public labeled
fixtures exercise deterministic attribution/grounding validation independently
of model quality.

`TestRealProviderPublicCorpus` is a separate opt-in gate. It uses twenty public
synthetic labels, reserves at most five model calls, and writes its isolated
ledger/results to an explicit private directory. It requires explicit
`CPA_EXTRACT_ACCEPTANCE_ENDPOINT`, `CPA_EXTRACT_ACCEPTANCE_MODEL`,
`CPA_EXTRACT_ACCEPTANCE_TOKEN_FILE` and `CPA_EXTRACT_ACCEPTANCE_DIR` environment
settings. `TestRealProviderPublicRelations` uses a separate public fixture to
check all five suggestion actions in one additional bounded call. Both can share
one isolated ledger, reserving at most six calls in total. Ordinary tests skip
them. The attribution/key-preservation check is not
owner-question acceptance, semantic entailment certification, or real-model
relationship-classification accuracy. Failed trials must remain recorded and
must not be presented as production acceptance.

The implementation does not schedule background jobs, resolve disputes, infer
completed deployments, approve global preferences, migrate a vector database,
or change client configuration. Actual model-route compatibility and answer
quality remain explicit gates before any rollout.

## Verified development acceptance, 2026-10-21

The configured `gpt-6.1-sol` route passed the v3 protocol with twenty public
synthetic attribution labels: 20 accepted, 20 matched, zero rejected. Four cases
each covered user requirements, user reports, assistant suggestions, assistant
reports and tool observations. The actual assistant-report summaries retained
their lack of tool verification; failed tool checks remained failures. Every
supporting quote passed exact scoped source readback.

A separate real-model fixture produced supported `new`, `corrects`, `supersedes`,
`conflicts_with` and `duplicate` suggestions, preserving the supplied target
revisions. It generated no Git proposals or approvals. Together the successful
gates reserved six calls, 29,541 input units and 6,000 output units; the provider
reported 7,822 input and 2,212 output tokens. These are operational reservations
and successful-call usage, not monetary prices or a private-corpus quality score.

Earlier isolated trials exposed two explicitly handled CPA compatibility fields.
Their four call reservations and uncertain ledgers were retained. The complete
development verification reserved ten calls and 10,000 output units across
those trials and the successful gate; none was refunded or silently replayed by
the worker. Successful v3 evidence is private under
`/Volumes/data/build/cpa-session-archive/extraction-public-acceptance-v3-ghm0jcdv`.
Temporary model-credential copies were removed after each invocation. The
committed [aggregate acceptance record](evals/memory-extraction-v1.json) contains
no credentials or conversation text.

Full-repository Go race tests, vet, native command build and the native status
smoke passed. Final focused race tests also covered review-state preservation.
The tests use disposable repositories/databases and public synthetic sources.
No client configuration, live reviewed Git knowledge or running service was
changed by this development increment. No binary was installed and no push was
performed.
