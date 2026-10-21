# Memory validity and explicit relationships

Reviewed memory stays in the existing canonical Git authorities. This layer
records lifecycle decisions and reports whether a retained entry is eligible
for current recall. It does not infer whether a statement is true, detect
semantic contradictions automatically, or activate a model's proposal.

The changes documented here are development-only and have not been deployed.
The local Codex `cpa_archive` entry is currently removed. This work uses isolated
repositories and does not enable an integration or publish knowledge.

## Lifecycle and read-time validity

An entry's persisted `status` records its lifecycle. Its returned `validity`
contains `state` and `reasons`, derived at read time rather than persisted as a
second knowledge authority. A verified entry can become unavailable
for current recall without changing its persisted lifecycle: its review date
may be due or a retained source revision may no longer be readable.

| Validity state | Meaning | Eligible for default current recall |
| --- | --- | --- |
| `current` | Verified canonical entry passes the applicable metadata, review, and source checks | Yes |
| `review_due` | The recorded review date has passed and a human recheck is needed | No |
| `source_unavailable` | Required retained evidence cannot be validated in the authorized scope | No |
| `invalid_metadata` | The entry's lifecycle or validity metadata cannot be accepted | No |
| `archived` | An operator retired the entry | No |
| `superseded` | An approved replacement or correction retired this canonical entry | No |
| `disputed` | An approved explicit conflict left the claim unresolved | No |
| `draft` | A candidate has not been approved | No |

`current` means eligible for this retrieval policy, not guaranteed correct in
every situation. Callers should preserve validity reasons, provenance, and
scope. An unavailable source is not proof that the recorded statement became
false, and a lifecycle status alone does not independently verify its evidence.
The state describes a parseable entry. Unreadable Git content or malformed
document structure can still fail the query; `all` does not turn those failures
into a successful empty response or promise to repair the underlying files.

`review_after` is a review schedule, not a fact TTL. Reaching it does not delete
the entry, rewrite the statement, prove it expired, or automatically extend its
review date. A human must inspect the claim's evidence and applicability through
the existing review workflow. Entries with external reviewed evidence continue
to use that authority; the adapter does not manufacture CPA citations for them.

## Current, lifecycle, and draft views

`project_memory` uses `view: "current"` by default. This returns eligible current
entries and keeps unavailable, retired, and unresolved claims out of ordinary
recall. `view: "all"` exposes canonical entries with their validity states for
inspection, subject to the same authority, scope, text, and result limits.
It is a lifecycle view of the canonical files at committed HEAD.

`drafts: true` selects the separate candidate area. Combining it with
`view: "all"` is rejected. A draft query does not mix drafts into the canonical
lifecycle view or make them current. Preserve that distinction in the UI and in
any generated answer.

For canonical queries, returned `states` counts cover the authorized readable
records in the selected authority before `text` matching, `limit`, and current
eligibility filter the returned entries. Visibility restrictions still apply.
Consequently these counts can include inactive records and records absent from
the displayed results; they are not counts of keyword matches or returned
entries. A draft query counts its separate candidate set. Neither count proves
truth or enumerates historical Git versions.

Neither `view: "all"` nor a draft query enumerates every historical Git version.
An earlier body overwritten by a same-ID correction remains in Git history; it
does not become a separate canonical result merely because `all` was selected.
An explicit replacement using a different entry ID can retain the old record
as `superseded` in the canonical lifecycle. Git remains the place to inspect
commits and prior file contents that are no longer represented there.

## Explicit typed relationships

A proposal may state one of these directed relationships to another memory:

| Relationship | Proposed meaning | Effect after human approval |
| --- | --- | --- |
| `supersedes` | The proposed entry replaces the target's applicable guidance | Target becomes `superseded`; replacement becomes `verified` |
| `corrects` | The proposed entry corrects the target's recorded claim | Target becomes `superseded`; corrected entry becomes `verified` |
| `conflicts_with` | The proposed claim and target cannot currently be accepted together | Both canonical entries become `disputed` and leave current recall |

The proposal's `relations` array accepts at most eight objects, each with
`kind`, `target_id`, and `target_revision`. `supersedes` and `corrects` may appear
together as replacement relationships. `conflicts_with` cannot be mixed with
either replacement kind in the same proposal. Conflicting kind or revision
bindings for one target are rejected; callers must make one coherent intended
transition rather than rely on array order.

Relationships are explicit proposal metadata. Similar wording, embedding
distance, a model's confidence, or a shared source does not create one
automatically. A model can suggest a relationship and supply evidence; it cannot
approve the relationship or resolve the dispute by itself.

The target must belong to the same canonical authority and exact memory scope.
Global and project memory cannot replace one another, and one project's entry
cannot retire another project's entry. A shared repository path or a matching
target ID does not widen the caller's authorization.

Each relationship binds the target ID and its exact revision before approval.
The operator also supplies the candidate's expected revision. Approval checks
both bindings and current evidence again. If the candidate or target changed,
review must fail rather than silently act on a different revision. Inspect the
new state before making a new intentional proposal or review attempt.

The target revision in an approved relationship records the revision reviewed
at that transition. Approval itself changes the target's lifecycle metadata,
which gives that target a new content revision. These are different observations;
the prior relationship must not be interpreted as a promise that the target's
current revision will always equal its pre-approval revision.

## Preserve relationship identities

A same-ID `base_revision` correction or re-review is allowed only for a
`verified` canonical node with no confirmed `relations` and no `superseded_by`.
All non-verified nodes, including archived entries, remain inactive under this
path. That restriction applies at proposal and approval,
including a pending draft created before the node acquired a relationship.
An exact base revision alone does not authorize erasing confirmed lineage.

Continue a relationship chain with a new entry ID and an explicit `corrects` or
`supersedes` relationship to the eligible target's current revision. Targets must
have `verified` or `disputed` status and no successor reference. A disputed node
can be corrected or replaced this way after human review. Archived and
superseded entries cannot be relationship targets or reactivated, including when
a successor becomes review-due or loses evidence. Follow the retained lineage
to the applicable non-retired node instead of restoring the retired ID.

A new ID can also represent an independent sourced proposal without a
relationship. It needs its own retained source citations and human approval;
it does not rewrite or reactivate an archived or superseded node.

A plain `verified` node with no confirmed relationships or successor reference
still supports same-ID correction or re-review with its exact `base_revision`.
The earlier body remains in Git history. This compatibility path does not let
relationship-bearing or retired nodes shed their identity and become current
again.

## Approval and unresolved conflict

The adapter exposes approval only through the separate operator review path.
The model-facing interface can read and propose; it cannot approve. A successful
approval commits the affected canonical entries and candidate state together in
one Git commit. For replacement or correction, the retired target keeps its
`superseded_by` linkage to the new
entry. The transition does not delete raw evidence or rewrite past commits.

A conflict approval records an unresolved disagreement. It does not pick a
winner or merge the statements. Both sides are excluded from default recall;
the lifecycle view can show them for authorized inspection. A human must use a
subsequent explicit correction, replacement, or archive action to resolve the
affected entries. Archiving one disputed entry does not by itself establish
the other as true or automatically approve it.

One Git commit provides a single committed lifecycle transition. It does not
make unrelated working-tree edits part of that transition or guarantee that
every file write survives a process interruption. Only committed canonical
content is active. After an interruption or rejected commit, inspect managed
files and the repository lock while preserving unrelated work; follow
[Git memory recovery](curated-git-memory.md#git-safety-and-recovery).

The adapter's repository lock coordinates adapter operations that honor that
lock. It does not cover arbitrary external Git writers or manual file edits.
Operators must serialize repository maintenance with proposal and review
operations, including changes to managed files, HEAD, and the index. Dirty-file
checks and one committed transition are not a substitute for that coordination.

## Interpretation and validation

Validity reasons and explicit relationship checks are mechanical policy
results. They do not replace an operator's assessment of factual support,
sensitivity, relevance, or conflicting instructions. The implementation does
not promise semantic contradiction detection, automatic reconciliation,
historical truth reconstruction, or exhaustive Git-version search.

Isolated acceptance covers current versus all views, due reviews,
unavailable sources, malformed metadata, stale target and candidate revisions,
cross-authority and cross-project rejection, replacement/correction lineage,
disputed-pair recall exclusion, blocked same-ID lineage rewrites, non-reactivation
of superseded nodes, exact returned-source citation validation, and failed-commit
preservation of Git state. Context tests preserve conflict/revalidation coverage
signals, and in-memory MCP tests verify view forwarding and the absence of an
approval tool. Full-repository Go race tests and vet passed; the final added
source/scope and lineage regressions also passed focused race runs.
Public synthetic fixtures can verify those mechanics. They do not create owner
labels or complete the real-question evaluation described in
[private corpus preparation](private-recall-corpus.md).
