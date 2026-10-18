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
