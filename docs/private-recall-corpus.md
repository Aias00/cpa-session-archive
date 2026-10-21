# Prepare an owner-labeled private retrieval corpus

The committed synthetic corpus is a reproducible baseline. It is not the owner
corpus of at least 50 real questions required by the architecture plan. Neither
the preparation tool nor a model can certify that questions came from the owner
or that a label correctly answers them. That review remains a human task.

`cpa-memory-corpus` prepares a deliberately unfinished draft and validates a
completed file. It does not read the live archive, export transcripts, connect to
the collector, call an embedding/LLM provider, run retrieval, or modify the frozen
synthetic corpus. Its output contains only aggregate counts and the input hash.
It never prints dataset names, questions, case/source IDs, scopes, anchors, or
private filenames.

## Create the unfinished draft

Choose a directory outside the repository that only the owner can read. For
example, create `/absolute/private/recall-labels` with directory mode 0700. Then
run from the repository root:

```sh
PATH=/Volumes/data/tools/cpa-go-sdk/bin:$PATH \
GOROOT=/Volumes/data/tools/cpa-go-sdk \
go run ./cmd/cpa-memory-corpus init \
  --output /absolute/private/recall-labels/owner-v1-draft.json \
  --questions 50
```

The command exclusively creates a new file with mode 0600. Existing files and
symlinks are refused. It creates no parent directories. It accepts 50–1000 draft
slots; the evaluator's existing dataset limits also apply to completed files.
Preserve these permissions when saving through an editor.

The draft contains an empty `records` array and numbered case slots with blank
queries, scopes, categories, and evidence. Its description lists the approved
category vocabulary. No slot is an invented question, positive label, or negative
claim. The unfinished draft is intentionally rejected by validation and by the
evaluator. Draft creation does not complete the owner-corpus requirement.

## Supply real questions and supporting records

Write questions from actual owner tasks before looking at a candidate ranker's
answers. Use the question's natural Chinese or English wording, including
paraphrases, rather than rewriting it into a known file's keywords. Keep the
current query limits: at most 256 Unicode characters and 16 whitespace-delimited
words. Every case needs an explicit `query.project_id`; add session, client, or
time filters only when the real question requires them.

Use the existing authenticated archive browser or a separately authorized export
to inspect evidence. This preparation command does not perform that export.
Copy only the records and message text needed for the evaluation into `records`,
alongside enough realistic non-answer context to test candidate selection. Keep
tool outputs, corrections, and older decisions when they matter. Do not fill
the corpus with generated transcripts. Avoid copying credentials. If identifiers
are pseudonymized, apply the mapping consistently and keep that mapping private.

Each record uses the existing evaluator shape:

```text
id: a stable dataset source ID
project_id, session_id: the source's scope
client: the source client (defaults to codex when omitted)
event_at: the original observation time in RFC3339 form
messages: [{role: user|assistant|tool, content: exact copied message text}, ...]
```

This fixture format copies message text into an isolated archive. It does not
preserve production CAS objects, native response sides, or the live archive's
source revision. Optional evidence pointers refer to the fixture's
`original_request` side and `/messages/<index>/content`, not automatically to a
production citation. Keep a private mapping to original sources if needed for
human review. Live citation/authorization and ingestion acceptance still require
their independent integration checks.

For every answerable case:

1. Choose an approved category and fill its actual scoped question.
2. Put every gold source ID in `relevant_ids`.
3. Add an `evidence` label for each positive source:
   `{record_id: <source ID>, contains: [<short exact supporting phrase>, ...]}`.
   All phrases in one label must occur in one eligible message. Prefer the
   accepted value, fix, decision, or result over a mere mention of the question.
4. Add `forbidden_ids` only for sources that cannot be valid in that scope or
   source state. A historical non-answer is not automatically forbidden.
5. Keep `expect_empty` false. For a real no-evidence question, set it true and
   remove all positive labels and evidence. Do not invent negatives because a
   ranker failed to find an answer.

An empty expectation describes the frozen corpus and its declared scope. A
selected subset cannot prove that the entire production archive has no answer.

Use the fixed vocabulary and review category counts:

| Category | Intended coverage |
| --- | --- |
| `exact_error` | Literal errors, failure output, and their supported remedies |
| `path` | File paths, code symbols, and implementation decisions |
| `chinese` | Natural Chinese queries over Chinese/code evidence |
| `paraphrase` | Different wording for an actual supported fact |
| `old_decision` | Older accepted decisions among later unfinished discussion |
| `correction` | Corrections and superseded values, with history retained |
| `branch` | Distinct sessions with different decisions; no invented native branch metadata |
| `missing` | Real absence or deliberately invalidated fixture evidence |
| `repetition` | Repeated conversation context competing with useful evidence |

Categories alone do not prove coverage. Human review should also include failed
approaches, tool results, long messages, multiple relevant sources, cross-project
negatives, and source changes/deletions. Keep later evaluation questions separate
from a tuning set where possible. Do not let the candidate ranker's output write
or approve its own gold labels.

## Validate and freeze

After completing the private file, run:

```sh
PATH=/Volumes/data/tools/cpa-go-sdk/bin:$PATH \
GOROOT=/Volumes/data/tools/cpa-go-sdk \
go run ./cmd/cpa-memory-corpus validate \
  --corpus /absolute/private/recall-labels/owner-v1-completed.json
```

Validation uses the existing bounded `recalltest.Load` schema and evidence
checks, then requires at least 50 distinct scoped questions. Case IDs, whitespace,
case changes, equivalent time-zone spellings, and retrieval mode changes cannot
pad the count. Distinct exact project/session/client/time scopes can represent
different questions. The validator checks scope presence and current query
length/time-range limits; it does not execute a ranker or verify real-world
truth. Missing categories are reported as coverage gaps, not filled automatically.

A successful aggregate has `structural_validation_passed: true`, but always has
`owner_authorship_verified: false` and `evaluation_performed: false`. It proves
structural readiness only. The owner must separately review authenticity,
relevance, sufficient distractors, completeness of gold sources, and category
coverage before treating P0 as complete.

Freeze the completed file after that review. Record the tool's SHA-256, retain
the exact bytes privately, and compare every candidate against those same bytes.
Do not commit private questions, transcript records, mappings, or case reports.
A changed question, source, or label needs a new private corpus version and both
comparison runs repeated; do not edit a corpus to conceal failures.

Run the existing evaluator with the frozen private file, new output paths, and
the same scope/evidence grading used for the synthetic baseline:

```sh
PATH=/Volumes/data/tools/cpa-go-sdk/bin:$PATH \
GOROOT=/Volumes/data/tools/cpa-go-sdk \
go run ./cmd/cpa-memory-eval \
  --corpus /absolute/private/recall-labels/owner-v1-completed.json \
  --mode literal \
  --output /absolute/private/recall-labels/owner-v1-literal-report.json
```

Repeat with `--mode diverse` and a different new report path. Semantic/hybrid
evaluation additionally needs an explicitly selected embedder/model profile and
measured call, latency, memory/disk, and cost boundaries; deterministic test
embeddings do not prove real paraphrase quality. See
[the evaluator contract](recall-evaluation.md) and
[the framework assessment](memory-framework-comparison.md) for the broader gates.
