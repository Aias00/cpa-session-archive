# Synthetic semantic calibration and held-out sets

Two new version-1 fixtures separate threshold calibration from a subsequent
fixed-choice evaluation. They preserve the existing evaluator schema, top-five
evidence grading, and the unchanged `baseline-v1.json`.

| Fixture | Sources | Positive questions | Empty-expected questions | SHA-256 |
| --- | ---: | ---: | ---: | --- |
| [semantic-calibration-v1.json](../internal/recalltest/testdata/semantic-calibration-v1.json) | 30 | 24 | 24 | `e203f926d0617b6b6366cae2cf3a98d8accb3d7ec30737e463ad1105421ca4b3` |
| [semantic-held-out-v1.json](../internal/recalltest/testdata/semantic-held-out-v1.json) | 20 | 16 | 16 | `e03a0b32ec87b0ac4ce62e1354c78cc7593adfbfdf07b6714399b44e6eda5237` |

Both sets are model-authored synthetic data. They contain no owner questions,
private transcripts, exported production records, or owner-reviewed labels.
The developer had already read the original frozen baseline and observed its
results before creating these sets. Their new subjects and wording reduce direct
example reuse; they do not make this a human-authored independent blind test.
The development process also authored both new splits. Separating their use in
time is a procedural check, not proof that their authors were independent.

## Coverage and labels

Calibration subjects cover chess tie breaking, basil cultivation, audio
resampling, ferry accessibility, pastry, book humidity, wool dyeing, beehive
ventilation, irrigation sensors, violin pegs, wort cooling, and aluminum milling.
Held-out subjects cover porcelain cooling, bridge bearings, polarization in
photography, sail reefing, geographic tiles, grapheme cursor movement, gemstone
optics, and chromatographic integration. Neither split reuses the baseline's
archive, memory, deployment, or request-delivery questions.

Each subject has two differently worded positives and two negatives. English,
Chinese, cross-language, and mixed code questions are present. Calibration has
24 English, 18 Chinese, and 6 mixed code questions; held-out has 16 English,
10 Chinese, and 6 mixed code questions. These counts include both polarities.
The `note` fields declare `split`, `polarity`, `language`, and `feature` slices
without introducing new production schema fields.

Every positive binds an exact supporting phrase to the second message of its
source with an explicit side and pointer. The first message is a user prompt,
which cannot earn credit for the answer. Code subjects use tool-role evidence;
their nearby display or telemetry code is separate non-answer context.

Eighteen calibration negatives and twelve held-out negatives ask for a missing
detail in a topic that is represented in the corpus: a temperature, dimension,
rate, material ratio, brand, or version that the retained evidence does not give.
This tests weak association rather than relying only on unrelated topics.
They remain empty-expected even if a similar passage receives a high model score.

The remaining six calibration and four held-out negatives have a true requested
fact only in another project, session, or client. Those sources are explicitly
forbidden for the query's scope. Related in-scope non-answers are not marked
forbidden; returning them still fails the empty expectation. An empty label
describes this fixture and its exact filters, not an absence claim about a live
archive.

## Evaluation order

1. Validate both files structurally and record the exact hashes above before
   running candidate retrieval. The structural tests read both sets but do not
   execute any ranker or embedding provider on either set.
2. Prespecify the embedding profile, decision rule, candidate parameter grid,
   and acceptance requirements. Use only the calibration file to select among
   that grid. Preserve the unchanged frozen baseline as a diagnostic comparison,
   not another source of tuning questions.
3. Persist the calibration result, including `none` when no candidate meets
   the requirements. Record its corpus hash, model/index identity, parameters,
   and quality/safety metrics before opening the held-out questions or results
   in the selecting workflow. A rejected calibration does not authorize
   enabling semantic retrieval.
4. Only when a candidate was selected, evaluate the persisted choice on
   held-out once. If selection is `none`, leave held-out unevaluated. Do not select another
   threshold, rewrite labels, remove failed cases, or repeatedly inspect
   held-out to tune a later attempt. Report positive recall, negative empty-pass
   rate, scope/citation/forbidden failures, and semantic coverage separately.
5. Keep subsequent work separate. A revised candidate needs another newly
   authored held-out version for a further independent evaluation. Keep these
   files and prior failures available for reproducible comparison.

The selection policy for this iteration requires at least 0.8 positive macro
recall, all negative cases empty, complete semantic coverage, zero safety
failures, and zero truncated cases. The last condition is deliberately strict:
the current result flag also marks ordinary top-five pagination when further
hits exist. Such a result is rejected even when the additional hits would be
normal pagination rather than a query-budget failure. Do not interpret this
gate as proof that every rejected result exhausted a candidate budget.
A fixed grid can have no qualifying candidate. That outcome is useful evidence
about the model or abstention rule; it must remain visible.

The predeclared grid for this iteration is `0.78, 0.80, 0.82, 0.84, 0.86,
0.88, 0.90, 0.92, 0.94, 0.96`. The selector evaluates every value and chooses
the lowest value passing every gate, rather than optimizing a held-out score.
The provider is the pinned multilingual E5 Small profile from
[local embedding](local-embedding.md), with its existing window policy unchanged.
The minimum positive macro recall is 0.8. These choices were recorded before
running the new calibration set.

`empty_pass_rate` is the fraction of empty-expected negative questions returning
zero hits with complete retrieval. It is not precision: the positive questions have their own
evidence-constrained source recall, and the evaluator does not exhaustively label
all returned passages for a conventional precision denominator.

## Calibration command

```sh
PATH=/Volumes/data/tools/cpa-go-sdk/bin:$PATH \
GOROOT=/Volumes/data/tools/cpa-go-sdk \
go run ./cmd/cpa-memory-calibrate \
  --corpus internal/recalltest/testdata/semantic-calibration-v1.json \
  --output /absolute/private/new-calibration.json \
  --embedding-endpoint http://127.0.0.1:8320 \
  --embedding-token-file /absolute/private/embedding.token \
  --embedding-fingerprint '<exact pinned provider fingerprint>' \
  --embedding-dimensions 384
```

The command requires explicit model configuration and a new 0600 aggregate
output path. It runs each candidate in newly created disposable databases;
costs therefore include repeated indexing. It never opens production databases.
The report records every candidate's quality, category, cost and completeness
metrics, the input hash and model/index fingerprint. No queries, case/source IDs,
labels, tokens or provider endpoints appear in its aggregate output.

`selected_minimum: null` and exit 3 mean no candidate qualified. Exit 1 denotes
execution/input/report failure; exit 2 denotes invalid arguments. A later model
failure or identity change invalidates the entire run, including an earlier
eligible candidate. Raising the semantic threshold does not filter the separate
lexical stream, so some false positives may persist at every tested value.
The observed original baseline is explicitly refused as calibration input.

A selected threshold is calibration evidence, not an activation policy. Persist
the report before a single held-out evaluation at that exact minimum, using
`cpa-memory-eval --mode hybrid --semantic-minimum <selected> --require-empty-pass
--require-complete --min-recall-at-5 0.8` with the same embedding configuration.
Do not run this step when selection is null. Production also needs owner labels
and capacity evidence beyond these synthetic fixtures.

## Lexical comparison

The ordinary evaluator already accepts either fixture. For a lexical comparator
on calibration, use a new private output path:

```sh
PATH=/Volumes/data/tools/cpa-go-sdk/bin:$PATH \
GOROOT=/Volumes/data/tools/cpa-go-sdk \
go run ./cmd/cpa-memory-eval \
  --corpus internal/recalltest/testdata/semantic-calibration-v1.json \
  --mode literal \
  --output /absolute/private/new-calibration-literal.json
```

An embedding run additionally requires the explicit provider options documented
in [hybrid retrieval](hybrid-retrieval.md). Use fresh aggregate and case-report
paths for every configuration. Keep `--require-empty-pass` and the safety
checks enabled when judging acceptance; an aggregate recall number alone can
hide false positives. No retrieval results for these new sets were inspected
while authoring and structurally freezing version 1.

## Measured calibration, 2026-10-20

Native tools built from exact source `9e4f39b` ran the predeclared grid once on
the local Apple M4 with the pinned E5 Small provider. The
[aggregate report](evals/semantic-calibration-v1.json) preserves all ten trials.
The index profile remained
`878738c18327a5af1399e132e99b9e4d05d6ce469bf6b7b21e66ed686977efb3`.

| Minimum cosine | Positive questions found | Negative questions passed | Eligible |
| --- | ---: | ---: | --- |
| 0.78 | 24/24 | 2/24 | No |
| 0.80 | 18/24 | 5/24 | No |
| 0.82 | 15/24 | 6/24 | No |
| 0.84 | 14/24 | 14/24 | No |
| 0.86 | 14/24 | 17/24 | No |
| 0.88 | 12/24 | 19/24 | No |
| 0.90 | 7/24 | 22/24 | No |
| 0.92 | 3/24 | 24/24 | No |
| 0.94 | 1/24 | 24/24 | No |
| 0.96 | 0/24 | 24/24 | No |

Every trial had complete semantic coverage, zero truncated cases, and zero
citation, scope or forbidden-source failures. No candidate combined at least
0.8 positive macro recall with all negatives passing. The command correctly
exited 3 and persisted `selected_minimum: null`. No parameter was promoted and
the held-out set remained unevaluated. The grid result does not characterize
every possible continuous threshold, other models, or production questions.

Literal and diverse comparison runs on this deliberately paraphrased calibration
set each found 0/24 positives and passed 24/24 negatives, with no safety failures.
These are distinct questions from the observed baseline; the scores do not imply
a regression in existing literal/diverse behavior. They demonstrate why merely
returning nothing is an insufficient acceptance criterion.

The full ten-trial command took approximately 4.14 seconds in this small fixture
experiment. Each trial reindexed 30 short synthetic records. This elapsed time
is not a production query latency, capacity measurement or SLO. The temporary
embedding process was stopped after evaluation; production embeddings remain
disabled and no owner transcripts were processed by this experiment.

Next retrieval work needs a separately evaluated relevance/answerability policy
or a different candidate model; another threshold adjustment is not established
as sufficient by these results. Evidence must still be returned with current
source citations, and refusal must remain distinct from transport failure.
For broad project-orientation questions, build cited context from the registered
project's reviewed Git documents and scoped observations. A typed project brief
should cover purpose, architecture, open work and installation without treating
an unreviewed model summary as a new knowledge authority.

## Limits

Questions within a subject share evidence, so the balanced counts are not
independent statistical samples or a production false-positive estimate. The
sets use short messages, modest candidate populations, one positive source per
question, and deliberately selected facts. They do not test raw-store mutations,
long windows, production load, comprehensive relevance judgments, or the
authenticity of private labels. Existing integration and stress tests cover
different boundaries. The text/scope disjointness test detects literal reuse;
it cannot prove generalization or semantic independence.

These sets do not complete the requirement for at least 50 real owner-labeled
questions. Follow [private corpus preparation](private-recall-corpus.md) for that
work. Model-generated labels and calibration output cannot approve themselves
as owner evidence or authorize a live rollout.
