# Automatic session organization and bulk jobs

This slice is in development. The currently deployed P0 session workspace is
recorded in [deployment history](mac-mini-deployment.md); its deployment does
not install the controls described here. No model enrichment, background rule
scheduler, automatic deletion or new client integration is enabled by this work.

## Computed views

The session browser can filter existing summary/facet evidence without assigning
a title or tag to every session:

| `view` | Meaning |
| --- | --- |
| `all` | No additional automatic-view restriction |
| `recent` | Activity in the last seven days |
| `inactive` | No activity for `inactive_days`, default 30 |
| `main` | Explicitly captured main/user source evidence |
| `subagent` | Explicitly captured subagent evidence |
| `system` | System-only source evidence |
| `prewarm` | Prewarm-only request-kind evidence |
| `unassigned` | Missing trustworthy project scope or captured scope conflicts |

`inactive_days` accepts integers from 1 through 3650. These restrictions combine
with project, client and other exact facets, literal label search, tags,
favorites and archive visibility. Activity is measured against server UTC at
query time. Inactivity does not mean that a task has completed or that its
evidence is invalid. Activity views also consult retained completion times so
that an existing request updated before preview cannot hide behind an older
cached summary time. A session without source evidence is not assumed to be a
main conversation. A fork/parent conversation link alone is not proof that a
session is a subagent.

Project identity and selection retain their exact facet name and value. Project
directory basenames are display labels, not merge keys. An ambiguous native
project ID or an identity-conflict marker remains unresolved even when a
project label is available. These views do not alter raw data or user metadata.

## Preview, execute and recover

A bulk preview takes the entire matching selection, independently of the
browser's page size and offset. It stores exact session IDs, inspected metadata
versions, source-change sequences and before values in management tables. A
preview is bounded to
100,000 matches; larger selections are rejected rather than truncated. It does
not keep a long-running SQLite read snapshot or materialize conversation bodies.
Newly arriving sessions are not appended to an existing job.

Supported patches are archive/restore, favorite/unfavorite and tag additions or
removals. Titles, notes and unrelated tags remain unchanged. Archiving protects
favorites and manually edited sessions by default; including either group is
an explicit preview option. Bulk writes retain their own provenance so that a
previous bulk tag change alone does not become a manual-edit protection.

Applying a job is separate from previewing it. Each request processes at most
100 pending items and records its outcome atomically with metadata changes.
Sessions edited since preview are reported as conflicts. For archiving, a
session with newer captured activity is also skipped. The authoritative event
sequence catches updates to an existing request, moves and deletions even if
the session summary's count or time stays unchanged. Replaying a completed
step cannot apply the same item twice. Status remains available after the
browser, command or collector restarts.

The Web interface can run sequential steps and pause between them. The CLI
executes one bounded step per command by default; `--until-done` explicitly
continues after confirmed responses. An uncertain network failure is not
automatically retried: refresh the durable job status, inspect it, then explicitly
resume. A job remains recoverable even if archiving removes every matching row
from the current library view.

Undo cancels pending work and restores successfully applied items in bounded
steps. It only restores an item while its metadata revision still matches the
revision written by that job. Later edits become undo conflicts. A successful
undo writes a new revision; it never moves the revision counter backward.
Undo that would archive a session also checks for newer source changes. Undo
cannot reverse raw collection because bulk operations never change it.

## Shared management API

The authenticated CPA prefix is
`/v0/management/plugins/cpa-session-archive`. The collector mirrors these paths
under `/v1` within its existing private listener boundary.

| Route | Method | Result |
| --- | --- | --- |
| `/session-browser?view=inactive&inactive_days=30` | GET | Existing bounded browser page |
| `/session-bulk-preview` | POST | A frozen job; no session metadata is changed |
| `/session-bulk` | GET | Latest 20 jobs as `{jobs:[...]}` |
| `/session-bulk?id=JOB_ID` | GET | One job with bounded item samples |
| `/session-bulk-apply` | POST | Apply one bounded step |
| `/session-bulk-undo` | POST | Undo one bounded step and cancel pending work |

Preview request example:

```json
{
  "selection": {
    "view": "inactive",
    "inactive_days": 30,
    "archived": "exclude",
    "filters": {"project.id": "EXACT_PROJECT_ID"}
  },
  "action": {"archived": true}
}
```

Apply/undo requests use `{"id":"JOB_ID","limit":100}`. Jobs expose their
frozen selection/action, total and pending counts, applied/protected/unchanged/
conflict counts, undo outcomes and creation/update times. Bounded item samples
contain session IDs, status and reason, not the saved note text or raw bodies.
The optional `storage.SessionBulkManager` capability allows other storage
backends to keep basic session management without claiming bulk support.

## CLI examples

Use a development build containing this slice and the matching isolated
collector/plugin. The live installed P0 CLI does not yet have these commands.
Keep the existing management credential in a private regular file (0600), as
described in [session management](session-management.md).

```sh
manager=/absolute/development/cpa-session-manage
cpa_origin=http://127.0.0.1:YOUR_ISOLATED_CPA_PORT
credential_file=/absolute/private/management.token

"$manager" list --endpoint "$cpa_origin" --token-file "$credential_file" \
  --view inactive --inactive-days 30 --filter project.id=EXACT_PROJECT_ID

"$manager" bulk-preview --endpoint "$cpa_origin" --token-file "$credential_file" \
  --view inactive --inactive-days 30 --filter project.id=EXACT_PROJECT_ID \
  --archive true

# Inspect the preview's counts and protections before applying its returned ID.
"$manager" bulk-show --endpoint "$cpa_origin" --token-file "$credential_file" \
  --job-id JOB_ID

# Each request performs at most 100 items. Inspect status before resuming.
"$manager" bulk-apply --endpoint "$cpa_origin" --token-file "$credential_file" \
  --job-id JOB_ID --limit 100 --until-done

"$manager" bulk-undo --endpoint "$cpa_origin" --token-file "$credential_file" \
  --job-id JOB_ID --limit 100 --until-done
```

Use `--add-tag TAG` and `--remove-tag TAG` for tag patches, `--star true|false`
for favorites, and `--archive false` for restore. Archiving options
`--include-starred` and `--include-edited` opt out of the respective protections.
Omit `--job-id` from `bulk-show` to recover recent jobs. All successful commands
return JSON. They do not automatically resend a metadata mutation.

## Development verification, 2026-10-21

Repository-wide Go race tests and `go vet`, TypeScript checks, deterministic
frontend rebuild comparison and all 21 script/parser/repository checks passed.
The original 15 browser scenarios and five new bulk/view scenarios passed with
the pinned Playwright 1.62.1 browser. Browser checks used synthetic mutable
fixtures, including 1,105 sessions, across-page processing, protection opt-ins,
preview/edit conflicts, undo conflicts, pause/reload recovery, uncertain network
results, changed scopes and job recovery from an empty library.

Storage tests verify a frozen selection larger than the page limit, restart
recovery, atomic rollback under an injected SQLite error, concurrent apply,
partial undo/cancellation, tag bounds, exact hostile IDs and unchanged raw
exports/feed sequences. Activity regressions cover new requests, updates to
an existing request before/after preview, moves and deletes. Old metadata gains manual-edit protection
through an additive migration. Older development previews without an activity
baseline conservatively conflict when an operation would archive a session.

A native M4 synthetic scale sample with 15,000 indexed sessions and 150,000
source events took about 40 ms to freeze a preview and 7 ms to apply 100 items.
These observations are not a latency guarantee for arbitrary filters or payloads.

Native collector, CLI and C-shared plugin builds also passed. An isolated
instance of the installed CPA executable, synthetic credentials and those
artifacts verified actual POST route/header forwarding, the exact embedded
page and anonymous rejection. Its 250-session job protected two sessions,
skipped two activity/edit conflicts, applied 246 updates across a collector
restart and undid 245 while preserving a later manual edit as an undo conflict.
CLI `--until-done`, recent-job recovery, exclusion of newly arriving sessions
and an unchanged raw export hash passed. No model calls or production data were
used. Private aggregate evidence and development artifacts are under
`/Volumes/data/build/cpa-session-archive/session-scale-20261021-57jupxsw`.

## Remaining scope

Saved organization rules, scheduled evaluation, job-history retention and
metadata portability remain later work. Optional model suggestions should be
incremental, independently versioned and subordinate to user edits. Automatic
organization does not approve knowledge, infer task completion or change the
independent memory engine's retrieval scope.
