# Reviewed Git memory

The optional Codex MCP knowledge adapter uses existing Git authorities rather
than creating another authoritative database. M4 continues to retain conversation
evidence and serve revision-checked source excerpts. The workstation bridge
reads/writes the owner's existing canonical knowledge repositories.

The first lifecycle supports sourced candidates, human confirmation, corrections
and archiving. It performs no automatic extraction, preference activation,
semantic contradiction detection or remote publication. This increment was
validated with Codex only; Claude configuration and accounts were not changed.

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

## Codex MCP configuration

Extend the existing `cpa-memory` command arguments:

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
`drafts=true` to inspect candidates separately. Confirmed reads omit drafts,
archived/expired entries and CPA-derived entries with unavailable evidence.

With explicit `--allow-proposals`, MCP also exposes `memory_propose`:

- Required: stable `operation_id` (8-128 letters/digits/underscore/dot/hyphen),
  `kind` (`decision`, `fact`, `experience`, `preference`), title, statement and
  one to eight exact archive citations. Select explicit global or a registered
  project scope. Statements are limited to 4,000 Unicode characters.
- New candidates get a deterministic ID from scope and operation ID. Retrying
  the same proposal returns the same revision/commit without duplicate writes.
- Draft edits pass `id` and its current `expected_revision`.
- Correcting confirmed knowledge passes `id` and the active `base_revision`.
  The old conclusion remains active until the correction is separately reviewed.
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

## Git safety and recovery

Only managed files are staged/committed, using `git commit --only`; unrelated
staged and unstaged user edits are retained. Already-dirty managed files are
rejected rather than folded into an approval. An existing uncommitted project
context must first be reviewed/committed through the project's normal workflow.
The adapter never force-cleans it. Normal Git identity/signing/hooks apply.
It does not push anything to GitHub.

Concurrent adapter writes use a repository lock. Commit failure restores only
the adapter's owned files/index paths. A process killed between file replacement
and commit may leave uncommitted managed work; it is not active knowledge and
subsequent writes fail closed. Inspect that owned change and the temporary
Git-path `cpa-memory.lock` before recovery; do not reset unrelated work. Avoid
simultaneous manual edits to managed files during a review operation.

The bridge binary is installed on both workstation and M4. The production Codex
entry points at the workstation's existing `codex-kb`, not a newly invented M4
knowledge authority. Other devices can use the same reviewed Git repository
through their explicit sync/review workflow. M4 archive service/schema/credentials
are unchanged by this increment and require no collector/CPA restart.

## Validation

Regression tests exercise draft/confirm/correct/archive, idempotency, scopes,
source invalidation, expected revisions, project-context preservation, unrelated
staged changes and failed Git commits. Official MCP client tests verify that
review/approval is not advertised. Real Codex acceptance uses a disposable Git
repository and actual M4 evidence across four fresh runs: proposal without
activation; correction without premature activation; corrected recall; and no
recall after archiving. No Claude test is used.
