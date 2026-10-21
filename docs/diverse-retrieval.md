# Optional diverse literal retrieval

Search and recap accept `mode=literal|diverse`. Omitted mode and explicit
`literal` retain the existing occurrence ordering and recent-prefix path.
The Web page defaults to literal and requests five results in diverse mode;
MCP exposes the same explicit option with its existing five-result default.
Diverse requires nonempty searchable text, including for recap. Punctuation-only
search and empty scoped recap remain available in literal mode.

Diverse still requires every whitespace-separated literal term in one chunk.
It does not add paraphrase matching, embeddings, frequency ranking, inferred
decisions or reviewed knowledge. Projection v3, normalizer v2 and source citation
formats are unchanged; there is no migration or new persistent retrieval state.

## Candidate and display diversity

Scope, client, time and eligible-role/kind predicates apply before candidate
selection. Candidate rounds are the maximum of two one-based positions:

- Occurrence position for an exact shared chunk, role, effective kind and tool
  name, ordered by request event time descending, request ID and chunk ID.
- Window position within one request/side/message pointer, ordered by chunk ID.

Order by round, ordinary text before summary before tool evidence, then request
event time descending and stable IDs. This lets older independent text enter the
first round ahead of repeated snapshots and additional windows of long messages.
It does not reinterpret request time as original message time or verified state.
At most 1,001 IDs/sort keys are materialized before loading text; the existing
1,000-candidate budget and truncation sentinel remain.

Literal verification and current source-revision checks run before suppressing
accepted messages or excerpts. A deleted, changed or reassigned newest occurrence
can fall back to another occurrence in the bounded candidate prefix. This is not
an exhaustive fallback across all occurrences; 1,000 stale or token-only false
candidates can still exhaust the budget. Truncation requests narrower filters.

Identical displayed excerpts with the same role, kind and tool name use one
representative citation. Distinct roles/kinds/tools remain distinct. This is
display suppression: all archived occurrences remain independent and retained.
There are no complete occurrence counts, first/last-seen claims or merged events.
Every returned citation continues to identify one message range in one current
retained source. Query rows close before live archive checks.

## Cost and failure boundaries

The two window sorts can process more than 1,000 matching occurrences. A lexical
probe of at most 1,024 unique text IDs enables a recent-source attempt for broad
queries. It considers the newest 1,024 complete sources within the exact
project/session/client/time filters and selects only first-round ordinary text,
up to `max(16, 2*limit+1)` rows. Every excluded eligible source sorts later than
this ordinary-text first-round prefix. All ordering/window decisions share one
SQL snapshot; no occurrence is excluded within a selected source before the
windows run.

Accept that prefix only when literal, live revision and display checks provide
the requested number of hits, reporting conservative truncation. A shorter prefix
never proves exhaustion. Otherwise query the full 1,001-row prefix and return
only its validated results. The recent attempt has its own one-second SQL/pool
budget: expiry falls back to the full query within the total budget. This keeps
an expensive recent scan from consuming most of a rare query's time. Later-round
recent candidates cannot use this proof, because older independent text can
precede newer repeated content.

A five-second diverse search budget covers read-pool wait, SQL, source validation
and status. Client cancellation/deadlines remain authoritative. Exceeding the
internal budget returns `ErrQueryBudget` / HTTP 422 with a request to narrow
scope, time range or literal terms; it never returns a successful empty result.
The default literal path has no new timeout. The projection read pool remains
bounded to four connections and writes remain collector-owned.

## Evaluation

The same frozen 62-question synthetic corpus/hash yields:

| Metric | Literal | Diverse |
| --- | --- | --- |
| Supported answerable cases | 36/56 | 48/56 |
| Recall/hit at five | 64.3% | 85.7% |
| MRR at five | 57.1% | 67.9% |
| Identical excerpt slots | 48/110 | 0/74 |
| Empty-expected cases passed | 6/6 | 6/6 |
| Forbidden/scope/citation failures | 0 | 0 |

Old-decision and repetition categories improve from 0/6 to 6/6 each. Exact
errors, paths, Chinese literals, corrections and branch scores stay unchanged.
Paraphrases remain 0/8. The full categorized aggregate is
[diverse-baseline-v1.json](evals/diverse-baseline-v1.json).

Four read-only production engine samples while online backup/verification ran
measured diverse searches at 42–444 ms for a common literal and a three-term
literal, each with owner-wide and one project filter; all 20 citations resolved.
Earlier full-sort attempts exceeded a 10-second test deadline, motivating the
bounded first-round prefix and FTS-first full fallback. These are isolated
samples, not p95, cold-cache or concurrent-ingestion service guarantees. Literal
samples ranged from 252 ms to 7.93 seconds; existing broad/scoped latency remains
an open task. Live installed-binary checks are recorded separately at rollout.


Compare both modes against the same frozen input hash with
[cpa-memory-eval](recall-evaluation.md). The committed synthetic baseline is not
an owner-labeled real-query acceptance corpus. Keep separate stress fixtures for
more than 1,000 repeated occurrences, long-message windows, stale representative
fallback and role/scope distinctions. Actual broad-query latency and budget
failures must accompany quality scores; fixture latency is not a production SLO.
