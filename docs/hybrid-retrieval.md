# Experimental local hybrid retrieval

Hybrid retrieval is an explicit experimental mode. Default `literal` and
`diverse` retrieval continue without an embedding model. The collector, raw
archive, normalizer-v2 citations and reviewed Git knowledge remain independent.

The first real-model experiment improved recall but regressed empty-answer
precision. It does not justify enabling semantic backfill on the production
archive or selecting hybrid as the default. The local model/provider is
available for isolated evaluation; production enablement requires separate
quality and capacity evidence.

## Implementation

`mode=hybrid` obtains two independent candidate streams:

1. Existing diverse lexical selection, with actual literal containment checked
   before fusion. The index still stores deduplicated lexical token sets; its
   ranking must not be described as conventional frequency-based BM25.
2. A local purpose-specific query embedding compared with stored window vectors
   for eligible texts in the requested project/session/client/time range.

RRF combines stream ranks, then content/message rounds spread repeated history.
Every displayed hit belongs to one retained occurrence. Semantic-only excerpts
cover the matched window and retain original Unicode rune offsets. Current
source revisions are checked before results are served; reading a citation
reconstructs the same original normalized text.

Scope, role and message-kind eligibility apply before semantic top-k. System,
developer, context and image content do not become semantic candidates. Shared
vectors do not merge source ownership or grant cross-project access.

The first implementation scans scoped vectors; it is not an ANN index. Its
semantic budget is 100 unique texts, followed by up to 1,001 occurrence
candidates per stream and the existing bounded result selection. A long message
can occupy multiple semantic text slots; content diversity after top-k cannot
recover a text excluded by that earlier budget. Candidate limits set
`truncated=true`. Pool wait, embedding, SQL and live source checks share a
five-second query budget; exhaustion returns HTTP 422, never an empty success.

An absent, rejected, changed or malformed embedding provider returns an explicit
HTTP 503. The client does not silently turn hybrid into lexical-only retrieval.

## Derived storage and recovery

Optional tables live in the independent projection:

- `semantic_profiles`: provider/policy profile, discovery cursor and aggregate
  progress/error counters.
- `semantic_jobs`: one job per profile and eligible unique text, with content
  hash, attempts and delayed retry state.
- `semantic_vectors`: normalized float32 vectors for the text's windows.

Document windows contain at most 384 Unicode runes with a 96-rune overlap. They
cover nonblank text in the parent chunk; pure whitespace windows are skipped.
A whitespace-only chunk completes with an explicit zero-window count. The
provider rejects input exceeding its
512-token limit instead of silently truncating it; rejected windows remain
observable as incomplete work.

The profile fingerprint binds the provider's model/runtime/preprocessing
fingerprint, dimensions and our window/normalization policy. Equal dimensions
do not permit different models to share vector comparisons. Source IDs and
citations remain independent of this profile.

Source projection admits jobs in the same transaction as new occurrences and
their raw-feed checkpoint. Initial discovery covers pre-existing unique texts.
The semantic worker has its own progress and retries; model completion never
advances the raw source checkpoint. Model calls run outside the source mutex and
SQLite transactions. A complete text's vectors and ready state commit together.
Late results must still match both text ID and content hash, protecting against
SQLite row-ID reuse. Foreign keys and eligibility cleanup remove orphaned
derived state. Configured startup resets discovery for a bounded rescan, covering
new/reused IDs that an older binary or a disabled semantic worker did not admit.
Previously ready embeddings are reused when the profile and content still match.

Pending/retry work does not block later jobs. Repeated failures enter delayed
retry with a five-minute interval after eight attempts, so provider recovery
does not require manually reviving terminal jobs. Source transport errors and
explicit source absence retain their existing distinct meanings.

Global semantic counters are omitted for scoped credentials. A hybrid result's
`retrieval.semantic_complete` describes indexed eligible text in that query's
scope; false also sets `truncated`. It does not mean the collector has captured
every conversation, the raw feed is current, or the returned matches answer the
question. Inspect raw lag and evidence as well. Recap propagates this coverage
metadata and remains cited excerpts, not an inferred task-completion report.

## Explicit configuration

See [the pinned local provider](local-embedding.md) for installation, model
checksums, input policy and the dedicated private credential. It never downloads
models at request time and uses no external inference service.

To enable a disposable memory service, set these additional variables:

```text
MEMORY_EMBEDDING_ENDPOINT=http://127.0.0.1:8320
MEMORY_EMBEDDING_TOKEN_FILE=/absolute/private/embedding.token
MEMORY_EMBEDDING_FINGERPRINT=<exact pinned provider fingerprint>
MEMORY_EMBEDDING_DIMENSIONS=384
MEMORY_SEMANTIC_MINIMUM=0.8
```

The endpoint must be a literal loopback HTTP origin. Redirects and environment
proxies are disabled. The embedding credential must differ from archive-feed,
owner-read and scoped-read credentials. Incomplete embedding configuration is
rejected. Omitting all embedding settings leaves the feature disabled.

Enabling starts discovery/backfill over eligible unique text in that projection;
it is not a bounded one-project import switch. Measure capacity in a disposable
projection before configuring the production one. Old profile data is derived
state; this increment does not automatically prune old model profiles.

## Evaluation and acceptance

Use the unchanged frozen corpus and explicit model configuration:

```sh
go run ./cmd/cpa-memory-eval \
  --corpus internal/recalltest/testdata/baseline-v1.json \
  --mode hybrid \
  --embedding-endpoint http://127.0.0.1:8320 \
  --embedding-token-file /absolute/private/embedding.token \
  --embedding-fingerprint '<exact pinned provider fingerprint>' \
  --embedding-dimensions 384 \
  --semantic-minimum 0.8 \
  --require-empty-pass \
  --output /absolute/private/new-hybrid-report.json
```

Set the Go SDK environment described in the deployment runbook when necessary.
The evaluator creates disposable archives/projections, fully indexes the chosen
corpus and preserves all gold labels and the input hash. Model metadata, calls,
text/window counts and elapsed time are aggregate output; tokens, endpoints and
corpus text are excluded. Deterministic fake embeddings in tests verify
mechanics only.

Submitted text/window counts include retries. Model elapsed time sums call
durations and is not a distinct end-to-end latency or compute-cost measure.

`--require-empty-pass` adds an explicit no-evidence quality gate. Its failure
still emits the aggregate report, then exits 3. Existing citation, scope and
forbidden-source gates remain independent. Neither a successful command without
that flag nor improved recall alone certifies precision.

The initial real-model experiment used pinned quantized multilingual E5-small,
384 dimensions and a minimum cosine of 0.8 chosen before running the frozen
corpus. The dataset hash stayed
`3ba68a39c5489d213775c9ae1141002700a16d31b7285d565e67ba6f50c914bf`.
Its exploratory result was 53/56 supported questions found, paraphrases 5/8,
empty cases 4/6, zero citation/scope/forbidden failures and zero duplicate
excerpt slots. These are isolated synthetic results, not production quality or
an SLO. The report is retained outside the repository under the current hybrid
experiment's reports directory; final artifact measurements are recorded
separately after correctness fixes.

The final native artifacts built from `f1a62bf` reproduced these results with
three query repetitions. The [aggregate hybrid report](evals/hybrid-baseline-v1.json)
contains no queries or source IDs:

| Mode | Supported questions found | Paraphrases found | Empty cases passed | MRR@5 |
| --- | --- | --- | --- | --- |
| Literal | 36/56 (64.3%) | 0/8 | 6/6 | 57.1% |
| Diverse | 48/56 (85.7%) | 0/8 | 6/6 | 67.9% |
| Experimental hybrid | 53/56 (94.6%) | 5/8 | 4/6 | 75.0% |

All three had zero citation/scope/forbidden failures. Non-paraphrase categories
retain diverse mode's hit/MRR scores. The hybrid negative-case gate correctly
exited 3, so the recall gain does not qualify it for production enablement.
Fixture-only hybrid p50/p95 were approximately 3.24/4.74 ms; these tiny isolated
databases do not establish full-archive performance.

Separate real-process preflight with the same artifacts/local model passed
semantic-only WAL paraphrase retrieval, original citation roundtrips,
cross-project pre-filtering, shared embedding deduplication, model outage with
literal availability, restart/new-text discovery, source outage recovery, and
immediate deletion/session-move invalidation while projection writes were
blocked. Those checks establish mechanics and failure behavior, not the missing
precision or owner-corpus quality evidence.

Do not tune thresholds or rewrite frozen labels merely to remove these misses.
Use independently labeled calibration and held-out owner questions. The
[private corpus workflow](private-recall-corpus.md) prepares that work without
inventing questions or gold evidence.

The [synthetic calibration workflow](semantic-calibration.md) adds a fixed
parameter grid, separate subjects and an unevaluated held-out split. It requires
positive recall, every negative case, safety and complete retrieval together.
It supplements the regression fixture; model-authored labels do not replace
owner review or authorize production backfill.

The first separate calibration selected no threshold: at 0.78 it found 24/24
positives but passed only 2/24 negatives; at 0.92 it passed 24/24 negatives but
found only 3/24 positives. All ten predeclared trials were ineligible. The
held-out split remains unevaluated and production semantic indexing remains
disabled; see the linked calibration report for every trial and its limits.
