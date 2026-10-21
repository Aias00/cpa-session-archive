# Frozen recall evaluation

## Run the evaluator

`cpa-memory-eval` creates disposable private archive/projection databases from an
explicit JSON dataset. It does not connect to production listeners, read native
histories, invoke a model, change reviewed Git knowledge, or open live databases.
It deletes its databases after completion. It can run the committed synthetic
corpus or a separate owner-controlled private dataset of the same shape.

From the repository root on the M4:

```sh
GOROOT=/Volumes/data/tools/cpa-go-sdk /Volumes/data/tools/cpa-go-sdk/bin/go run \
  ./cmd/cpa-memory-eval \
  --corpus internal/recalltest/testdata/baseline-v1.json \
  --output /absolute/private/new-aggregate.json \
  --case-report /absolute/private/new-case-metrics.json \
  --repetitions 3
```

Report paths must be new: the command refuses existing files and symlinks.
Reports use mode 0600. Standard output contains only aggregates, never dataset
names, case/source IDs, project/session IDs, query text, labels or snippets.
The optional per-case report includes case IDs and numeric metrics; retain it
privately when labels describe private work. Do not commit private datasets or
case reports. Only synthetic fixtures and reviewed aggregate results belong in
this repository. A file hash identifies the exact input bytes; mutation of a
loaded dataset is rejected before execution.

The optional `--mode literal` or `--mode diverse` overrides retrieval mode for
every case on a local query copy. With no override, each fixture query's `mode`
is preserved; an omitted query mode means literal. The aggregate
`retrieval_mode` is `literal`, `diverse`, or `mixed`, according to the effective
case modes. `mixed` describes a report and is not an accepted override. Unknown
mode values are rejected. Comparing the same corpus with these overrides keeps
its file hash, records, questions, relevance labels, and evidence anchors intact;
write each run to a different new report path. Mode selection alone is not
evidence of a relevance or semantic improvement.

The default deadline is two minutes. `--repetitions` is bounded to 1–100; only the
first pass contributes quality counts, while all passes contribute isolated
latency samples. These are small-fixture search times, not production latency,
cold-cache measurements, concurrent-capture throughput or a service SLO.

## Gold labels and scoring

The version-1 shape is documented by the
[frozen synthetic fixture](../internal/recalltest/testdata/README.md).
Every answerable case must name its relevant source IDs and exact supporting
phrases within one eligible message. Optional side/pointer labels constrain that
message. The loader rejects missing or unreachable support, deleted positives,
contradictory labels and positives outside the declared project/session/client.
A matching source ID alone does not score: the returned cited snippet must
contain the support phrases and its current source read must reproduce it.

The fixed top-five metrics are:

- `hit_at_5`: answerable cases with at least one supported source / answerable cases.
- `recall_at_5`: mean of each answerable case's distinct supported source count
  divided by that case's relevant source count. This is macro recall; empty cases
  do not change its denominator. It measures evidence-constrained source labels,
  not automatically inferred independent semantic evidence groups.
- `mrr_at_5`: mean reciprocal rank of the first supported hit; a miss contributes 0.
- `empty_pass_rate`: empty-expected cases returning no hits / empty-expected cases.
- `duplicate_excerpt_rate`: returned slots after the first identical excerpt in
  each query / returned slots. Exact excerpt equality is display redundancy; it
  does not merge event identity or prove equal full messages.
- `forbidden_hits`, `scope_violations`, `citation_failures`: separately counted
  hard failures. Every returned excerpt is read through the fixed scoped reader
  and checked against its current citation/text. HTTP/MCP credential enforcement
  still needs their independent integration tests.

Default quality misses and an empty-expected false positive remain visible in
metrics; they do not alone make a baseline command fail. Scope, citation or
explicit forbidden-source violations return exit code 3. The optional
`--min-recall-at-5 0.8` adds a finite 0–1 macro-recall gate; the default -1 disables
that quality threshold. Exit 1 denotes input/execution/report failure, and exit 2
invalid command arguments. Category scores must accompany overall scores so exact
matching cannot conceal paraphrase or old-decision misses.

## Baseline v1

The frozen dataset contains 134 synthetic sources, 62 questions and four unsynced
post-index mutations. Fifty-six questions have evidence-backed positives; six
expect no evidence. The raw archive deliberately stays ahead of the projection
after these mutations, testing stale-source rejection rather than assuming lag 0.
The baseline report is [literal-baseline-v1.json](evals/literal-baseline-v1.json).

Default literal retrieval found 36/56 answerable questions: recall/hit@5 64.3%,
MRR@5 57.1%. All six empty cases passed and no forbidden, scope or citation failures
occurred. Exact errors, paths, Chinese literals, corrections and session-scoped
branch cases passed; paraphrase, old-decision and repeated-context cases missed.
Identical excerpts occupied 48 of 110 returned slots (43.6%). Those are synthetic
observations, not estimates of owner relevance or archive completeness.

This version does not cover a private owner corpus of 50 real questions. Its
sources are short single assistant messages, with only one positive source per
answerable case and at most 30 duplicate reminders. Separate grading tests cover
multi-positive macro denominators and wrong-message/source-only false credit.
Production candidate pressure above 1,000 occurrences, tool-role ordering,
long-message windows, real paraphrases and actual project continuation still need
additional datasets and acceptance. Do not change these frozen labels to fit a
ranker. Version corrections and compare both sides on the same input hash.

The measured next retrieval slice is explicit result diversity/candidate diversity
for repeated context, followed by an evaluated broader query mode. Keep default
literal behavior stable. Existing lexical indexing deduplicates tokens, so any
FTS4 matchinfo-based score must not be described as conventional frequency-based
BM25 without a separately evaluated frequency-preserving index. Embeddings remain
conditional on a labeled gain and bounded storage/model cost.

## Diverse v1 comparison

The same frozen input hash with `--mode diverse` finds 48/56 supported questions
(85.7% recall/hit@5), MRR@5 67.9%, and zero duplicate excerpt slots among 74 hits.
Both modes pass all six empty cases with zero citation/scope/forbidden failures.
Old-decision and repeated-context categories improve from 0/6 to 6/6 each;
paraphrases remain 0/8. Exact-category scores remain unchanged. The aggregate is
[diverse-baseline-v1.json](evals/diverse-baseline-v1.json), with selection and
measured cost boundaries in [diverse-retrieval.md](diverse-retrieval.md).
Separate Go stress tests now cover more than 1,000 repeated occurrences and long
message windows, stale representatives, refill and role/tool/scope attribution.
They supplement the frozen corpus; the owner-labeled real corpus remains pending.
