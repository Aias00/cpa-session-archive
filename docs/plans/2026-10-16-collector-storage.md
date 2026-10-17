# Collector and storage separation

## Scope

Decouple the collector's HTTP handlers, ingestion queue and snapshot lifecycle from
the SQLite implementation. Preserve the existing SQLite schema, archive formats,
HTTP responses, retry policy and default configuration. Do not deploy or migrate
production data, add a second backend, or introduce dependencies in this change.

## Implementation

1. Lock existing ingest validation, queue acceptance and export-ticket behavior
   with collector regression tests before changing the implementation.
2. Define the storage operations consumed by the collector, including a snapshot
   interface. Keep SQLite construction and startup repairs in the assembly path.
3. Replace direct database access with health and session-existence operations;
   let the SQLite implementation classify retryable write errors. Pass the export
   workspace explicitly rather than deriving it from a backend database field.
4. Prove ingestion and query handlers work with an injected non-SQLite test
   backend, and retain real SQLite snapshot/export regressions.
5. Document the boundary and run Go tests, race tests, vet, formatting checks and
   native collector/plugin builds.

## Acceptance

- Runtime collector handlers do not access SQL connections, database paths or
  SQLite error strings, or require concrete SQLite snapshots.
- A replacement backend can be supplied without changing request collection or
  the HTTP response contract.
- Existing database files and all existing tests remain compatible.
- Queue acceptance remains asynchronous and is not represented as durable commit.
