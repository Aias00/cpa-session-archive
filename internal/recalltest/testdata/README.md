# Frozen synthetic retrieval baseline

`baseline-v1.json` is a hand-labeled, deterministic retrieval corpus created
before changing retrieval ranking. Every conversation, identifier, path,
configuration value, and decision in this fixture is synthetic. No production
archive, private transcript, account configuration, or read token was used.

The fixture contains 134 source records, 62 questions, and four raw-store
mutations. All questions request the top five hits. Its SHA-256 is
`3ba68a39c5489d213775c9ae1141002700a16d31b7285d565e67ba6f50c914bf`.

| Category | Questions | What the labels measure |
| --- | ---: | --- |
| `exact_error` | 8 | An error and its recorded remedy, including punctuation-heavy errors. |
| `path` | 8 | A file's actual implementation decision among later questions and another project's matching path. |
| `chinese` | 8 | Chinese queries about recorded operational decisions and invariants. |
| `paraphrase` | 8 | Different wording, including cross-language queries, for a recorded fact. |
| `old_decision` | 6 | Older accepted decisions among five newer matching reminders per topic. |
| `correction` | 6 | The corrected current value rather than an earlier proposal or mistake. |
| `branch` | 6 | Correct evidence from two distinct sessions with incompatible experimental decisions. |
| `missing` | 6 | No evidence, another project's evidence, or stale deleted/replaced evidence. |
| `repetition` | 6 | Older useful decisions behind 30 identical newer context reminders. |

## Labels and evidence

`relevant_ids` names sources that contain an answer to the query, rather than
every source that mentions its words. Each positive source also has an `evidence`
label with short exact `contains` anchors. All anchors in a label occur within
one message of the named source. A returned hit counts as relevant only when its
source ID and returned snippet match that evidence label. This prevents a source
from receiving credit merely because an unrelated message, question, or keyword
mention from the same source was returned.

Paraphrase labels deliberately preserve expected failures of literal retrieval.
Some also require retrieving a Chinese answer for an English question or the
reverse. Do not rewrite them into exact-key queries to improve a score.

Correction cases keep the superseded history. Their labels prefer the later
explicit correction; an older proposal is not forbidden historical evidence.
The evaluator can therefore measure current-answer ranking without pretending
that a correction deletes history. Similarly, the newer reminders in
`old_decision` and `repetition` are non-answers, even though they contain the query
words. Only the older accepted decision has supporting evidence anchors.

`forbidden_ids` identifies evidence that cannot be valid in the specified scope
or current raw-store state. It is intentionally narrower than all non-relevant
IDs. Cases with `expect_empty: true` have no relevant sources and no evidence
labels. A nonempty result on those questions is a false positive even when the
source is not explicitly listed in `forbidden_ids`.

The category called `branch` uses ordinary distinct `session_id` values. It does
not assert that native branch metadata or a branch graph exists. Matching text
from the other session is explicitly forbidden for each session-scoped query.
Project and session filters are part of the frozen queries, not inferred from
prose during scoring.

## Mutations and stale projections

Index all initial records first. Then apply every item in `changes` in order.
All four changes have `sync_projection: false`: two delete a raw source and two
replace a raw source under the same ID with different content and a new
revision. Run the cases before synchronizing these changes into the projection.
The old projection rows must therefore be rejected by raw-source revalidation.

The two replacement records are authoritative replacements, not additional
messages. Their former identifiers and hints have no current evidence. The
fixture does not demand that the unindexed replacement content be retrievable.
No positive label refers to a deleted or replaced source.

## Interpretation and versioning

This is a reproducible synthetic baseline, not the planned private owner corpus
of at least 50 real questions. Its numbers cannot establish owner relevance,
production latency, semantic gains, or acceptable operational thresholds.
Report those limits alongside aggregate recall, ranking, excerpt redundancy,
stale-hit, and scope checks. Keep category scores visible: exact matching improvements must
not conceal poor paraphrase retrieval.

The corpus intentionally includes easy invariants and harder ranking failures.
Passing invariants alone does not mean the relevance task is complete. Conversely,
an honest paraphrase miss is evidence about the current lexical mode, not grounds
to weaken its labels. Any later semantic improvement needs evaluation on an
independent corpus to reduce overfitting to these known examples.

Keep this file and its evidence labels frozen when tuning a candidate ranker.
If a label or source must change, review the reason, create a new named fixture
version, preserve this baseline, and rerun both sides of the comparison. Never
derive the relevance labels from the ranker's own results.
