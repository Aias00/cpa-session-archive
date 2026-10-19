# Native client history synchronization

Native history supplements proxy capture. It does not execute recorded commands,
restore a native client thread, or automatically promote transcripts into curated
Git knowledge. Original files remain untouched.

## Sources and adapters

| Client | Default source | Indexed content |
| --- | --- | --- |
| Codex | `~/.codex/sessions`, `~/.codex/archived_sessions` | Response messages, function/custom tool calls and results; event-message fallback when response items are absent; compaction summary without replaying replacement snapshots |
| Claude Code | `~/.claude/projects` | User/assistant content and tool use/results, including separate subagent branches |
| Kiro CLI | `~/.kiro/sessions/cli` | Prompt, AssistantMessage, ToolResults, and compaction summary without replaying snapshots |

Kiro JSON metadata and legacy `.history` files are also retained. Auxiliary text,
opaque reasoning, encrypted blocks, multimodal content, unknown events and
malformed complete lines remain recoverable as originals but do not become
searchable text unless supported. Such sources are marked partial. Local deleted
or never-persisted content cannot be recovered. The empty legacy Kiro SQLite
conversation tables on the investigated workstation are not an importer source.

`internal/nativehistory` contains the format adapters; `internal/historysync`
contains filesystem discovery and checkpointed staging. `native-history/v1`
segments preserve exact original line strings and absolute byte offsets inside
an immutable envelope. The archive stores new envelopes as compressed raw CAS
blobs, avoiding HTTP payload cleanup and one compressor allocation per line.
Segments normally contain at most 64 lines / 1 MiB; a single record up to 64 MiB
is retained whole. Larger records fail that file explicitly. Complete final JSON
without a newline is accepted; an incomplete active tail waits for the next pass.

## Identity and replay

Source IDs namespace device, client, native filename, byte range and original
content digest. Session IDs separately namespace native session and branch.
Moving a Codex file from active to archived history preserves source identity.
Codex fork filenames are authoritative when an inherited parent session header
contains a different UUID. The header remains in the original payload and the
parent relationship is retained in metadata. Subagents remain distinct. Repeated text is not evidence of event identity; no
text/time-based merging with CPA traffic occurs. The existing projection shares
identical text storage without merging source occurrences.

A private `native_sync_files` table in the staging archive records the committed
byte offset, SHA256 of that entire prefix, and parsing context. Changed files verify
the full committed prefix before appending. Unchanged files can reuse a settled
device/inode/size/mtime/ctime proof for up to 24 hours; missing change stamps,
changed metadata, clock rollback and `--verify-all` force a full check. A changed/truncated JSONL file is
reported for reconciliation and its existing archive remains intact. Kiro
auxiliary files are mutable snapshots: changed versions are retained separately.
Archive commit precedes checkpoint advancement; deterministic source IDs make a
crash between them replay safely. Source deletions do not delete archived data.

Project scope prefers the recorded Git origin when the history has one consistent
workspace. Files spanning multiple working directories or recorded Git origins
receive a device/session-specific ambiguous project ID. Owner-wide and session
recall remain available; project-scoped credentials do not inherit those mixed
histories. This conservative classification can exclude same-repository worktrees
from project recall until finer event-level attribution is available. When missing, the importer reads
the currently accessible working tree's origin, or falls back to a device-scoped
local path. This fallback is enrichment, not proof of historical repository
identity. Per-event timestamps remain in originals; search/time filtering uses
the first available timestamp in each segment, with file/session time as fallback.
The segment is an archival boundary, not an assertion that it is one native turn.

## One-shot staging

Build `./cmd/cpa-history-sync` with the existing Go toolchain, then run:

```sh
cpa-history-sync --device workstation-id --archive /private/history/staging.sqlite \
  --report /private/history/first-report.json
```

Use the same stable device ID on later passes. `--home` overrides the source home
for fixtures or another retained profile. The staging archive, lock and reports
are private; do not place them in Git. Reports/logs contain aggregate counts and
private diagnostic paths, never conversation text or read credentials. A nonzero
file-failure count makes the command fail while preserving completed files.

## Transfer and ongoing synchronization

`scripts/sync_native_history.py --config /private/history/config.json` orchestrates:

1. An exclusive job lock and local staging, including when the M4 is offline.
2. An actual M4 hardware check through the configured SSH alias. The initial run
   creates a consistent SQLite online snapshot; later runs export only the source
   event range after the acknowledged watermark, including its reachable CAS
   dependencies. Each transfer archive is verified with quick_check.
3. Delta rsync to a private M4 staging directory, followed by the existing additive
   `cpa-session-import` into the live archive.
4. Zero-conflict/full-import checks, collector readiness and authenticated recall
   status. The transfer checkpoint advances only after successful verification.

Configuration is a private 0600 JSON file containing `ssh_host` (`mac-mini-m4` or
`mac-mini`), `device`, `state_dir`, and the absolute `binary` path. The orchestration
uses Python 3 standard library, SSH and rsync. No model client is invoked.
A failed upload/import retries from the durable local watermark even when the
next local pass has no new records. The job does not change CPA routes or tokens.

A workstation LaunchAgent may run the verified orchestration periodically after
login. It catches up on the next run when the M4 is offline or the workstation was
asleep. Archive commit and recall indexing are separate: a successful transfer
can report a nonzero projection lag until the collector catches up. Check the
private `last-success.json`, `last-failure.json`, and timestamped reports. As the
staging archive grows, full-prefix verification costs local
I/O even when few new records are present. After the first transfer, small
independent delta archives avoid rewriting the full staging database on every
run; zero-change transfers skip export and network upload.

Before the first live merge or collector replacement, follow the verified online
backup and rollback procedure in [M4 deployment](mac-mini-deployment.md). The
original histories, staging archive, and previous native versions are retained;
this feature does not propagate destructive retention decisions.


## Attribution repair for pre-fix imports

`scripts/repair_native_forks.py --archive /private/archive.sqlite --device workstation-id
--report /private/new-repair-report.json` repairs only native Codex records for the
specified device whose filename UUID differs from the inherited header. Back up
the archive first. Run after imports finish, on both staging and destination when
repairing an existing installation. It changes session metadata, facets and derived
browser projections; original CAS references and bytes remain unchanged. Ingest
events invalidate stale source revisions and drive recall catch-up. Staging parsing
checkpoints are updated when present. Repeating the repair makes zero changes. `--scope-manifest` optionally applies an
owner-audited JSON manifest with `device`, `ambiguous_codex_sessions` (filename
UUIDs), and `ambiguous_claude_files` (native basenames). It marks mixed-workspace
sources with an ambiguous project ID and updates staging parsing contexts. It
never changes original payloads or activates curated knowledge.


## Magpie-informed compatibility and retrieval

The reader now accepts absolute `CODEX_HOME` / `CLAUDE_CONFIG_DIR`, or explicit
`--codex-home` / `--claude-home`. The periodic job may pin the same homes using
`codex_home` / `claude_home` in its private configuration; launchd does not inherit
interactive shell variables automatically. Relative configured homes fail rather
than depend on a working directory. Only explicitly configured root directory
symlinks are resolved; arbitrary nested symlinks are still excluded.

Codex rollout filenames may include `_<segment>` and `.jsonl.zst`. The original
logical filename, without `.zst`, keys the checkpoint, so plain-to-compressed
moves preserve occurrence identity. If a plain twin exists it is preferred while
compression is in progress. A compressed-only file requires the installed
`zstd` executable (PATH, `/opt/homebrew/bin`, or `/usr/local/bin`). It is decoded
to a private temporary spool and its complete frame must pass before any source
commit. Offset/hash refer to decoded original bytes. Divergent same-name files
fail explicitly for reconciliation; they are not guessed into one thread.
Missing zstd or a corrupt/incomplete frame leaves checkpoints untouched.

A settled filesystem signature can skip unchanged files. The two-second settle
window and ctime reduce same-tick rewrite errors; cached proofs expire after
24 hours and are stored separately from authoritative SHA256 checkpoints. A
changed file still verifies its entire committed prefix. Filesystems without
suitable stamps always perform the full check. Use `--verify-all` for an explicit
integrity pass. This optimization is intended for ordinary local filesystems,
not a defense against a privileged attacker controlling filesystem metadata.

Messages expose additive `kind`, `tool_name`, `tool_call_id`, `model`, and
`message_at` fields. Model and time come only from available recorded events;
when a segment has no relevant model context the field stays empty. Existing
normalizer-v2 text, pointer and rune ranges remain unchanged. Search ranks user
and assistant text above summaries and tools, and excludes context/image parts;
a direct valid older context citation can still be read. Native timelines expose
the same metadata. Codex's structured subagent source and Claude subagent
`.meta.json` sidecars enrich new segments without rewriting old evidence.

The collector adds independently versioned `message_meta*` projection tables to
`memory.sqlite`. It backfills existing sources in bounded idle batches and
persists its checkpoint across restarts, leaving shared text and FTS unchanged.
`message_metadata_done` reports completion. The archive change worker still runs
first; changed/deleted sources invalidate metadata through the same lifecycle.
Until backfill finishes, older sources may have fallback classification. The
SQLite text projection schema remains v3 and citation normalizer remains v2.

These compatibility rules reference the MIT-licensed Magpie session readers;
see `third_party/README.md` and `third_party/magpie-LICENSE`. Magpie's UI excerpt
limits and whole application dependencies were not imported. Additional clients
(OpenCode, Cursor, Pi/OMP, Qoder, Kimi) remain future adapter work.
