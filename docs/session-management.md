# Session management: Web and CLI

The session workspace and paired collector/plugin/CLI were deployed to the
Mac mini M4 on 2026-10-21 after the user's explicit request. Native, isolated
integration and live read-only acceptance checks passed; see the
[deployment record](mac-mini-deployment.md). This scope does not enable client
integration, automatic memory extraction, semantic retrieval or human memory
approval. Existing management credentials remain in use.

The next [automatic organization and bulk-job slice](session-automation.md) is
in development. Its computed views and frozen all-matching apply/undo controls
are not part of that deployed P0 release.

## Available workflow

The existing archive page now has a session library with literal label search,
exact tag filtering, favorites, archive visibility, and ordinary pagination.
Existing project/client/model facets combine with those controls using AND.
Favorites appear first, followed by most recent activity and session ID as a
deterministic tie-breaker. Search covers session ID, project label, the source
summary, custom title, note and tags. This is not full conversation or semantic
search; the separate recall engine remains responsible for those capabilities.

The page uses the [React/shadcn workspace](frontend-workspace.md),
with responsive navigation and a synthetic preview. The Web and CLI keep the same
backend contract described below.

Opening a session exposes its custom title, tags, note, favorite and archived
state. Saving an empty title restores the source summary as the displayed title.
Tags are separated by commas or newlines in the editor; commas are not part of
a tag. Title limits are 200 Unicode code points, notes 8,000, and tags 24 items
of up to 48 code points each. Tags are trimmed, deduplicated and sorted.

An archived session is hidden from the default library. Select archived sessions
or all sessions to restore it. This action does not delete archive records,
change the native client thread, retire reviewed knowledge, or exclude the
history from the independent recall engine. Existing session links and exports
continue to work. Favorites and archive state describe library organization,
not whether a task is running or complete.

Saving requires the metadata revision that the user inspected. Concurrent edits
return a conflict; the form retains local text and requires an explicit reload.
A failed or interrupted write is not silently retried. Reload metadata before
deciding whether to submit another write. Navigation in this browser does not
natively resume a conversation in a different AI client.

## Shared API and storage boundary

Both interfaces use CPA's authenticated management API:

| Management route | Method | Result |
| --- | --- | --- |
| `/v0/management/plugins/cpa-session-archive/session-browser` | GET | `{sessions,total,limit,offset}` |
| `/v0/management/plugins/cpa-session-archive/session-metadata?session_id=...` | GET | Current metadata, including revision |
| Same metadata route | PUT | Full metadata replacement with `expected_revision` |

The corresponding collector-internal routes are `/v1/session-browser` and
`/v1/session-metadata`. They retain the collector's existing private network
boundary, not independent end-user authentication. Keep the collector private;
the CLI uses the authenticated CPA origin, not a direct collector port or the
separate recall API at port 18319. A recall token cannot authorize this manager.

List parameters are `q`, `tag`, `archived=exclude|only|all`, `starred=true|false`,
`limit=1..1000`, `offset=0..1000000`, and existing exact facet name/value pairs.
`starred=true` selects favorites; false applies no favorite restriction. Results
have the existing summary fields plus a nested `management` object. Source
summaries remain independently visible and are never rewritten by title edits.

The metadata object has `session_id`, `title`, `note`, `tags`, `starred`,
`archived`, `revision` and `updated_at`. A known session with no edits starts at
revision zero. PUT requires `expected_revision` plus the replacement title,
note, tags and flags. Missing fields use their empty/false defaults, so direct
API consumers must merge untouched fields themselves. The CLI does this before
submitting an edit. Unknown sessions return 404; stale revisions return 409.

The optional `storage.SessionManager` contract keeps backends without this
capability compatible; new handlers return 501 for unsupported backends. SQLite
stores user metadata and normalized tags in additive tables separate from
rebuildable session projections. New capture and projection rebuilds cannot
overwrite user edits. Mutations do not touch raw records, CAS payloads, source
revisions, processing sequences, snapshot contents or reviewed Git knowledge.

These tables are now part of the archive database backup. Existing raw JSONL
and SFT exports do not include user organization metadata. A separate metadata
backup/export/import workflow remains future work; moving only a JSONL file
does not transfer favorites, notes or tags.

Library pages are live views. Each response counts and selects within one short
read transaction, but ingestion and edits can move rows between page requests.
They do not pin the WAL or provide the migration snapshot protocol's replay
guarantees. The old `/v1/sessions` array response and stable export snapshot
protocol remain compatible.

## CLI examples

Build a development executable without installing it:

```sh
GOROOT=/Volumes/data/tools/cpa-go-sdk \
  /Volumes/data/tools/cpa-go-sdk/bin/go build \
  -o /absolute/development/cpa-session-manage ./cmd/cpa-session-manage
```

The examples below use the installed M4 CLI and local CPA origin. For a
development executable or a different host, replace those two values. Use a
private regular credential file (0600) containing the existing management
credential; replace the placeholder path below with that file. Nothing
in these examples provisions or retrieves a credential. The CLI permits HTTPS
origins or literal-loopback HTTP, disables proxies and redirects, and never puts
the credential in a command-line flag value or diagnostic output.

```sh
manager="/Users/aias/Library/Application Support/CLIProxyAPI/session-archive/bin/cpa-session-manage"
cpa_origin=http://127.0.0.1:8317
credential_file=/absolute/private/management.token

"$manager" list --endpoint "$cpa_origin" --token-file "$credential_file" \
  --filter project.name=my-project --filter client=codex \
  --q 'storage' --limit 20 --offset 0

"$manager" show --endpoint "$cpa_origin" --token-file "$credential_file" \
  --session-id 'EXACT_SESSION_ID'

# Use the revision returned by show, not a guessed value.
"$manager" edit --endpoint "$cpa_origin" --token-file "$credential_file" \
  --session-id 'EXACT_SESSION_ID' --expected-revision 0 \
  --title 'Storage investigation' --tag architecture --tag followup \
  --note 'Next: verify the migration against a synthetic fixture.' --starred true
```

All successful commands return JSON for scripting. `edit` changes only explicitly
provided options, first reading and checking the inspected revision; its final
PUT repeats that check atomically. It does not retry a conflict or network error.
Repeated `--tag` options replace the entire tag set; `--clear-tags` empties it.
`--title ''` and `--note ''` clear those fields. To archive or restore use
`edit --archived true` or `edit --archived false` with the current revision.
To browse archived sessions use `list --archived only`; use `list --starred true`
for favorites. Use `list --help`, `show --help` or `edit --help` for flags.

## Verification

Live rollout checks on 2026-10-21 verified the exact embedded page, browser
login-screen rendering, anonymous management rejection, collector readiness,
session browsing and session-scoped turn reads. Raw history advanced, the
outbox and quarantine were empty, existing citations stayed intact, and the
independent memory service and protected configurations were preserved. No live
authenticated login or write was tested: the existing management credential was
neither retrieved nor reset. Positive authenticated operations were verified
with synthetic credentials in the isolated real-CPA fixture below.

The later React/shadcn acceptance described in
[frontend-workspace.md](frontend-workspace.md#verified-development-acceptance-2026-10-21)
supersedes the initial browser counts below: all 15 scenarios passed with the
pinned Playwright 1.62.1 browser, alongside 21 script/normalization/repository
checks, TypeScript, deterministic frontend build, repository-wide Go race tests
and `go vet`.

The release was also checked against the actual installed CPA executable in an
isolated fixture using synthetic credentials and records. Plugin/collector
streaming and nonstreaming capture, termination recovery, before/after-commit
replay, exact embedded HTML, management authentication, native CLI edits and
revision conflicts passed, with unchanged raw exports. A production-sized
online-backup clone passed startup, readiness and session API checks. These
checks did not replace live artifacts or use live management credentials.

The following records describe the earlier API/CLI increment before the React
workspace and release-fixture verification above.

Isolated synthetic tests cover metadata persistence across capture and rebuild,
concurrent revision conflicts, exact Unicode and SQL wildcard literal matching,
facet conjunctions, pagination beyond 100 sessions, and unchanged source hashes
and exports. HTTP tests cover method/body/query bounds and conflict propagation.
CLI tests cover authenticated routing, safe credential handling, unchanged-field
merging, refusal of redirects, and no automatic retry. Browser tests exercise
save/reload, filtering and pagination, archive/restore, explicit conflict
reconciliation, and inert rendering/navigation for hostile titles and IDs.

Development validation on 2026-10-21 passed repository-wide Go race tests and
`go vet`, strict TypeScript checks, and all 11 browser scenarios. Browser checks
used an isolated existing Playwright 1.64.1 cache; the repository pins 1.62.1, so
this is not a claim that the exact pinned browser CI gate ran locally. No
dependency was installed and no real CPA management service was exercised.

Native collector, CLI and C-shared plugin builds also passed. An isolated native
collector plus the native CLI completed a synthetic list/show/edit/filter,
stale-revision rejection, archive and restore flow through a local test gateway.
The raw JSONL export SHA-256 was identical before and after. That gateway was a
fixture, not CPA; this does not claim live CPA integration or deployment.

For the remaining product stages, see the
[session management plan](plans/2026-10-21-session-management.md).
