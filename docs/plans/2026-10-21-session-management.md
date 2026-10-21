# Session workspace product plan

Status: stage P0 and the React/shadcn workspace were deployed to the Mac mini M4
on 2026-10-21 after the user's explicit request. Isolated integration and live
read-only acceptance passed; the [deployment record](../mac-mini-deployment.md)
records the checks and live-authentication limitation. Subsequent stages remain
planned. Both Web and CLI are first-class interfaces, as requested by the user
on 2026-10-21. This rollout does not authorize deploying new memory features or
configuring client plugins.

The user subsequently prioritized managing large session collections without
editing each session. Automatic views and previewed bulk operations are now the
next development slice, ahead of broader discovery and model enrichment. These
new controls are not included in the P0 deployment above.
Implementation and isolated acceptance for this slice are documented in
[automatic session organization](../session-automation.md).

## Product outcome

Manage the work represented by conversations across projects and clients:
find a session, understand its source and capture coverage, organize it, inspect
what happened, prepare a supported continuation, and review reusable knowledge.
The Web and CLI must operate on the same identities, data and revision checks.

The archive remains the source of retained conversation evidence. Session
organization is a separate user-maintained layer. Recall/search projections are
rebuildable. Reviewed Git remains the authority for durable project knowledge.
These boundaries do not require a storage replacement.

## Delivery stages

| Stage | User outcome | Web and CLI scope | Acceptance |
| --- | --- | --- | --- |
| P0: session library | Organize and find sessions | Title, tags, note, favorites, archive/restore, exact facets, label search and pagination | Changes persist, both interfaces agree, concurrent writes conflict, raw evidence is unchanged |
| P0.1: automatic organization | Manage thousands of sessions by scope and exceptions | Computed activity/source/unresolved-project views; frozen all-matching previews; protected archive/restore, favorites and tag patches; resumable bounded steps and revision-safe undo | Pagination never truncates selection, new arrivals do not expand it, new edits/activity are protected, interrupted jobs resume, and raw evidence is unchanged |
| P1: discovery and navigation | Find an event inside a session | Unified entry to literal/semantic recall with project/client/time scope; cited hit to exact retained turn; date range and sorting; saved filters | Search scope is preserved through navigation, missing evidence is explicit, exact-match queries retain quality |
| P2: task continuity | Understand where work stopped | User-managed task state; goal, verified result, unresolved items and next actions; versioned handoff packet and copy/export | Reports and execution evidence remain distinct, stale handoffs are labeled, native resume only uses supported client adapters |
| P3: session-to-knowledge review | Turn selected history into reviewed memory | Select exact sessions, preview extraction, compare existing entries, inspect quotations, stage drafts, explicit human review | Budgeted jobs resume safely, models cannot approve, source/target revisions are revalidated |
| P4: operations and portability | Maintain and move the workspace | Capture/sync coverage, queue/error/retry visibility; metadata export/import and backup/restore; reusable scheduled organization rules and job-retention controls | Round trips preserve IDs and metadata; archives, outboxes and knowledge survive recovery; no unreviewed destructive defaults |

Each stage requires both an API contract and CLI equivalents for its meaningful
operations. UI availability must not imply an operation is installed or enabled
on live services. Preserve existing capture while developing these stages.

## P0 architecture and boundaries

Reuse the embedded management UI and CPA's authenticated management API instead
of starting another frontend or distributing a management key to AI clients.
The optional `storage.SessionManager` interface supports additive user metadata
and a bounded live session list. A new browsing route avoids changing the legacy
session array or using WAL-pinning migration snapshots for normal browsing.

Metadata is not a claim about task progress. Archived means hidden from the
default library, not deleted or excluded from memory recall. Favorites are not
project priorities. User notes never become approved memory automatically.
Session identity stays tied to its original source; equal wording or directory
basenames must not merge conversations. User organization requires its own
portable export before raw JSONL migration can claim to transfer the workspace.

## Next slice

Implement P0.1 from existing summary and facet evidence. Use exact project
identities, repositories and captured scope rather than merging directory
basenames. Unknown or conflicting project evidence remains visible as unresolved.
Recent/inactive are activity views, not inferred task completion. Main/subagent
classification requires captured evidence; a parent conversation link alone
does not prove a subagent relationship.

Bulk operations use the entire matching selection, not the loaded page. Preview
freezes IDs and inspected metadata revisions in independent management tables.
Archiving protects favorites and manually edited sessions by default, and skips
sessions with newer activity or edits after preview. Explicit flag/tag patches
preserve untouched user fields. Apply and undo use bounded durable steps; undo
never overwrites a later edit or restores an old revision number. Web and CLI
share the same jobs and recovery status. Models, automatic deletion and new
memory authority are not prerequisites for this slice.

After this slice, start P1 by joining existing evidence search to the session browser: preserve
project/session scope, return a retained source citation, and navigate to the
correct turn with an explicit unavailable state when the evidence cannot be
resolved. Add time controls and ordering to the shared API and CLI alongside
that flow. Reuse the existing recall contracts and quality fixtures; do not
introduce a second vector store or model-based summary as a prerequisite.
