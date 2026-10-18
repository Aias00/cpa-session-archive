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
Subagents remain distinct. Repeated text is not evidence of event identity; no
text/time-based merging with CPA traffic occurs. The existing projection shares
identical text storage without merging source occurrences.

A private `native_sync_files` table in the staging archive records the committed
byte offset, SHA256 of that entire prefix, and parsing context. Each pass verifies
the full committed prefix before appending. A changed/truncated JSONL file is
reported for reconciliation and its existing archive remains intact. Kiro
auxiliary files are mutable snapshots: changed versions are retained separately.
Archive commit precedes checkpoint advancement; deterministic source IDs make a
crash between them replay safely. Source deletions do not delete archived data.

Project scope prefers the recorded Git origin. When missing, the importer reads
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

1. An exclusive job lock and actual M4 hardware check through the configured SSH alias.
2. Local staging and a consistent SQLite online snapshot, verified with quick_check.
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
staging archive grows, full-prefix verification and online snapshots cost local
I/O even when few new records are present; zero-change transfers skip snapshot
creation and network upload.

Before the first live merge or collector replacement, follow the verified online
backup and rollback procedure in [M4 deployment](mac-mini-deployment.md). The
original histories, staging archive, and previous native versions are retained;
this feature does not propagate destructive retention decisions.
