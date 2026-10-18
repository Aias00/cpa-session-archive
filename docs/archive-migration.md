# Incremental SQLite archive migration

`cpa-session-import` merges an **immutable online-backup snapshot** into an
existing archive without replacing it. Its destination can be the running
collector's SQLite WAL database. It does not call `/ingest`, whose HTTP 202 only
acknowledges an in-memory queue entry.

```sh
go build -o /private/workspace/cpa-session-import ./cmd/cpa-session-import
/private/workspace/cpa-session-import \
  -source /private/workspace/source-snapshot.sqlite \
  -target /private/workspace/archive.sqlite \
  -report /private/workspace/new-import-report.json
```

Take and verify online backups of both databases first. Preserve source
snapshot/checksum and reports privately. Restrict archive directories to the
owner. Transfer snapshots only through authenticated encrypted connections.
Use a new report path per attempt; the tool refuses to overwrite an old report.

## Contract

- Source and destination must use this archive schema. The source is opened
  read-only; source/target identity and incompatible record columns fail before
  mutation. Use a snapshot, not another machine's changing live database.
- Request IDs identify duplicates. Existing target rows always win, including
  their payloads, timestamps and identity. A private conflict-ID list records
  differences in raw record columns without printing conversation bodies.
- Only missing records are inserted. Record IDs are allocated by the target;
  source integer IDs and source ingest sequence numbers are never reused.
- Reachable CAS manifests and nested blobs are expanded/validated and their
  SHA-256 checked before import. Compressed source bytes and inline legacy
  payloads are retained exactly, including upstream bodies already archived.
  Unreferenced source blobs are not copied.
- A bounded batch reserves the SQLite writer before checking target IDs, then
  atomically copies records, CAS dependencies, record/session facets, compact
  turn projections and per-record normalization/preview markers. The standard
  archive insert trigger emits target-local change events in that transaction.
- Session browser projections use the existing archive indexing logic; the
  collector-owned memory worker consumes imported events normally. Do not copy
  memory.sqlite, source processing cursors or source session summary counters.
- Target credentials, identity mappings, repair policy and service configuration
  are not replaced. Source snapshots retain any conflicting metadata for review.
- Successfully committed batches remain after a later failure. Rerunning with
  a new report path safely resumes without adding records or ingest events for
  existing IDs. Recheck `remaining=0`, raw integrity, projections and index lag.

This is a point-in-time migration, not a continuously running replication
service. Requests arriving at the old host after the source snapshot require a
fresh snapshot and another run. Keep both source and destination archives intact
until verification is complete. Do not restore the target's old database just to
undo a tool/binary update; that would discard concurrent live requests.

## Validation

Regression tests cover distinct target/source IDs, source/target conflicts,
unchanged target records, nested CAS and upstream payload retention, session and
turn projections, committed change events, repeated imports, batch rollback and
resume, cancellation, source/target alias rejection, escaped paths and corrupt
CAS rejection. Run:

```sh
go test ./internal/archive -run TestImportSnapshot -count=1
go test -race ./...
go vet ./...
```

## Verified M1 to M4 migration

Source: legacy `mac-mini` (Apple M1). Destination: `mac-mini-m4` (Apple M4).
The final immutable source snapshot was taken at **2026-10-18 02:47:42 UTC**
(2026-10-17 in America/Los_Angeles), containing 7,509 requests. Its SHA-256 was
checked on both machines before the live merge.

The tool was built on the M4 from fork commit
`99c51be6b0f859f6abd927176ebcffe162d04be5`, with native full/race tests and vet
passing. A disposable target-backup copy imported the initial delta and a repeat
run inserted nothing before production use.

The live import inserted **1,413 requests** and **2,963 reachable CAS blobs**,
skipped 6,096 shared IDs, and found no conflicting raw record columns. The merge
took approximately 12 seconds without restarting CPA or the collector. A repeat
live import inserted zero rows/blobs and reported zero remaining source IDs.

Final verification passed:

- All 7,509 source request IDs exist on M4; all copied record columns match.
- Every request from the pre-import M4 backup remains, with zero column changes.
- Raw SQLite quick_check, turn/record ID associations, indexed-session membership
  and session summary counters match the merged raw archive.
- All imported IDs are present in the memory projection. A migrated source
  citation resolves to exactly the indexed excerpt.
- At verification M4 held 7,542 requests / 3,564 sessions and 2,826,203 chunks,
  with checkpoint and watermark 13,638, zero lag and zero processing failures.
  Counts include concurrent live M4 traffic and continue changing.

Private reports and the transferred snapshots are retained on M4 under
`session-archive/migration/`. Source and destination online backups remain in
`session-archive/backups/`, with paths recorded in the private reports. The
installed `bin/cpa-session-import` and its checksum/provenance are recorded in
`install-manifest.json`; the collector executable itself did not change.

This covers the stated source snapshot. The M1 service/archive was retained;
clients still sending requests to M1 after the snapshot are not continuously
replicated by this one-time operation.
