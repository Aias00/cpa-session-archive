# Reviewed Git memory

The optional Codex MCP knowledge adapter uses existing Git authorities rather
than creating another authoritative database. M4 continues to retain conversation
evidence and serve revision-checked source excerpts. A configured workstation
bridge can read and propose changes in the owner's existing canonical knowledge
repositories.

The lifecycle supports sourced candidates, human confirmation, corrections,
archiving, and explicit replacement/conflict relationships. Read-time validity
keeps review-due, unsupported, retired, and disputed entries out of current
recall. It performs no automatic extraction, preference activation, semantic
contradiction detection or remote publication. See
[memory validity](memory-validity.md) for the states and relationship workflow.

## Authority layout

- Reusable engineering knowledge: the existing `codex-kb` Git repository.
  Candidates use `inbox/captured/cpa-memory-*.md`; confirmed entries use the
  existing Markdown/YAML entry format under `entries/`.
- Project-only facts, decisions, experiences and preferences: a registered
  project's `.codex/ai-context.yaml`, under `reviewed_memories`. Drafts live in
  `.codex/memory-candidates/`. Other existing context fields are preserved.
- No arbitrary repository path is accepted as a tool argument. The owner
  configures exact project-ID-to-Git-root bindings in a JSON file.
- Active recall reads committed Git HEAD only. Git retains earlier versions;
  uncommitted files and pending corrections do not become confirmed knowledge.
- Existing canonical entries without CPA citations retain their reviewed external
  evidence. CPA-derived entries revalidate all retained citations on confirmation
  and active recall. Changed/deleted/cross-project evidence is not substituted.
- SQLite archive indexes remain derived retrieval data. The small curated corpus
  is queried directly from committed Git documents, so confirmation takes effect
  without maintaining a second authoritative memory database. The existing KB's
  separate derived index can be rebuilt by its own maintenance command.

No raw transcript, credential or customer payload is copied into global knowledge.
A candidate contains the proposed conclusion and opaque source citations. Global
scope is an explicit choice for reusable knowledge; private project-only facts
belong in their project's registered Git context.

## Optional bridge configuration reference

These examples describe the optional interface. The local Codex `cpa_archive`
entry is currently removed while development continues. The new validity and
relationship changes are development-only and have not been deployed.

Optional `cpa-memory` command arguments:

```text
--knowledge-root /absolute/path/to/codex-kb
--projects-file /private/path/project-memory-roots.json
--allow-proposals
```

Example owner-configured roots file:

```json
{
  "git:https://github.com/owner/project.git": "/absolute/path/to/project"
}
```

Each root must be a Git repository root with a committed HEAD. Normal source
project IDs must match the selected binding; unknown source identity is not
silently inferred from directory names. Existing read-token/project/session
restrictions still apply. Project-scoped bridges cannot access global knowledge
or other project authorities. Session-only credentials do not grant global or
project-wide memory access.

With a canonical root configured, `project_memory` is added to MCP. It accepts
`project_id` or explicit `global=true`, optional `text` and `limit`, and
`view=current` (the default) or `view=all`. Current reads return eligible verified
entries; they omit drafts, review-due entries, archived/superseded/disputed
entries, invalid metadata, and CPA-derived entries with unavailable evidence.
The all view retains canonical lifecycle records with their returned `validity`
states and reasons. It still reads committed HEAD; it does not search every old
Git version. Use `drafts=true` to inspect the separate candidate area;
`drafts=true` with `view=all` is rejected.

Canonical `states` counts apply to the authorized readable records before
`text`, `limit`, and current-eligibility filtering. They can therefore include
records absent from the returned entries; they are not keyword-match or
pagination counts. Draft queries count their separate candidate area.

`review_after` schedules a human recheck. It is not a fact TTL, deletion time,
or automatic proof that a statement became false. An overdue entry can remain
persisted as verified while its read-time validity is `review_due`.

With explicit `--allow-proposals`, MCP also exposes `memory_propose`:

- Required: stable `operation_id` (8-128 letters/digits/underscore/dot/hyphen),
  `kind` (`decision`, `fact`, `experience`, `preference`), title, statement and
  one to eight exact archive citations. Select explicit global or a registered
  project scope. Statements are limited to 4,000 Unicode characters.
- New candidates get a deterministic ID from scope and operation ID. Retrying
  the same proposal returns the same revision/commit without duplicate writes.
- Draft edits pass `id` and its current `expected_revision`.
- Same-ID correction or re-review of a plain `verified` entry passes `id` and
  the exact `base_revision`. It is allowed only when the canonical node has no
  confirmed `relations` or `superseded_by`. All non-verified canonical nodes,
  including archived entries, remain inactive under this path. The proposal
  leaves the canonical entry unchanged until separate review.
- Explicit `supersedes`, `corrects`, or `conflicts_with` relationships bind a
  different target entry's exact revision in the same authority and scope.
  Only an operator approval can apply their lifecycle effects; proposal creation
  leaves the target unchanged. The `relations` array accepts at most eight
  `{kind, target_id, target_revision}` objects. Conflict relationships cannot be
  mixed with replacement/correction relationships in one proposal.
- A node with confirmed relations, `superseded_by`, or `disputed`/`superseded`
  status cannot use same-ID `base_revision` rewriting to remove that lineage.
  Continue the chain with a new ID plus `corrects`/`supersedes` against an
  eligible target's exact revision. Relationship targets must be `verified` or
  `disputed` with no successor reference; archived and superseded targets cannot
  be reactivated. A new ID may also be an independent sourced proposal without
  relations, with its own citations and separate human approval.
- Different revisions fail closed. Equal statements are not automatically merged
  or treated as proof that a claim is true.

Review is never exposed as an MCP tool. Giving the model draft capability does
not give it a review/confirmation endpoint.

## Human confirmation, correction and archive

After inspecting content, scope and cited evidence, an operator uses the
separate command (same endpoint/token/root flags as the bridge):

```sh
cpa-memory review \
  --endpoint http://127.0.0.1:18319 \
  --token-file /private/path/codex-mcp-read.token \
  --allow-all-projects \
  --knowledge-root /absolute/path/to/codex-kb \
  --global --id memory-EXACT_ID \
  --expected-revision EXACT_DRAFT_REVISION \
  --action approve \
  --note 'Reviewed evidence, applicability and sensitivity' \
  --confirm-reviewed
```

Use `--project-id` and `--projects-file` for project context. To archive a
confirmed entry, pass its active revision and `--action archive`. Corrections
use the same proposal/review lifecycle and preserve earlier Git history. The
CLI's explicit review attestation is an operator action, not evidence that an
LLM verified a conclusion. Synthetic acceptance fixtures must remain outside
the user's canonical repositories.

For an explicit replacement or correction relationship, approval retires the
target as `superseded` with `superseded_by` and verifies the new entry in one Git
commit. An explicit conflict approval marks both entries `disputed`; neither is
returned by default current recall. The operator must later correct, replace,
or archive the affected entries. The adapter does not choose a winner from their
wording. See [the relationship contract](memory-validity.md#explicit-typed-relationships)
before reviewing either transition.

The all view lists retained canonical records, including those retired under a
separate ID. It does not resurrect each earlier body of a same-ID correction;
those remain in Git history. Drafts stay separate from both canonical views.

## Git safety and recovery

Only managed files are staged/committed, using `git commit --only`; unrelated
staged and unstaged user edits are retained. Already-dirty managed files are
rejected rather than folded into an approval. An existing uncommitted project
context must first be reviewed/committed through the project's normal workflow.
The adapter never force-cleans it. Normal Git identity/signing/hooks apply.
It does not push anything to GitHub.

Concurrent adapter writes use a repository lock. This lock does not coordinate
arbitrary external Git writers or manual editors. Operators must serialize
repository maintenance with adapter operations, including managed-file, HEAD,
and index changes; existing dirty-file checks cannot exclude a concurrent writer.
Commit failure restores only the adapter's owned files/index paths. A process
killed between file replacement and commit may leave uncommitted managed work;
it is not active knowledge and
subsequent writes fail closed. Inspect that owned change and the temporary
Git-path `cpa-memory.lock` before recovery; do not reset unrelated work. Avoid
simultaneous manual edits to managed files during a review operation.

The bridge can use the existing `codex-kb` from either workstation or M4 through
an explicitly configured path. Other devices can use that same reviewed Git
authority through their normal sync/review workflow. These optional capabilities
do not create another M4 knowledge authority or require enabling the integration
during development.

## Validation

Regression tests exercise draft/confirm/correct/archive, idempotency, scopes,
source invalidation, expected revisions, project-context preservation, unrelated
staged changes and failed Git commits. Official MCP client tests verify that
review/approval is not advertised. Earlier Codex lifecycle acceptance used a
disposable Git repository and actual M4 evidence across four fresh runs:
proposal without activation; correction without premature activation; corrected
recall; and no recall after archiving. No Claude test is used.

Validity and explicit-relationship development uses isolated synthetic
repositories. It does not read private knowledge to infer conflicts or approve
new entries. The public fixture mechanics do not replace owner-labeled recall
evaluation or establish factual correctness.
