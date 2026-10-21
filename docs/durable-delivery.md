# Durable delivery protocol

## Authority and rollout

The archive remains retained evidence; a producer spool is a temporary delivery
outbox, never another knowledge or retrieval authority. Delivery reliability is
independent of projection freshness and retrieval ranking.

The implementation includes an opt-in collector commit receipt, bounded outbox
storage and producer integration. Isolated native-host acceptance covers producer
and collector process termination after archive commit with a lost receipt.
Source `572f0a1` is enabled on the M4 with a private 2 GiB / 10,000-entry outbox.
Live capture, matching receipts, empty local backlog, source citations, MCP scope
isolation and Chromium rendering were verified. See the deployment runbook for
backup provenance and the exact observation limits.
Legacy senders still receive HTTP 202 on volatile queue admission. Enabled
producers persist before delivery and require matching commit receipts. This
protects the tested process-crash boundary after persistence, not every capture
callback or machine power failure.

## Commit receipt v1

A producer POSTs the existing JSON record to `/ingest` with
`X-CPA-Archive-Ack: commit-v1`. Unknown protocol values return 400 before
admission. This mode accepts exactly one JSON value, followed only by whitespace.
The response is HTTP 200 only after the admitted batch's `PutBatch` returns
success. It includes:

- `X-CPA-Archive-Ack: committed-v1`
- `X-CPA-Archive-Content-SHA256`: lowercase SHA-256 of the exact request body bytes.

A producer must check status, protocol and body digest before removing its local
outbox entry. HTTP 202 or a plain 200 is insufficient. The receipt is loopback
transport confirmation, not cryptographic authentication of the collector.

Each admitted delivery owns a separate buffered completion notification. Repeated
request IDs do not share a waiter. HTTP cancellation stops waiting but does not
revoke admitted storage work. The writer can complete without a live client. An
uncertain response must retain/replay the identical outbox bytes. Storage failure
returns 503 without a receipt. Temporary storage errors retain the batch for the
existing writer retry policy. Archive record identity remains `request_id`;
replay can increase ingestion events without increasing record count.

Receipts confirm the archive transaction, not indexing. The current archive WAL
uses SQLite `synchronous=NORMAL`; a receipt does not establish survival of sudden
power failure. Process-crash recovery, OS/power durability and producer pre-spool
capture are distinct boundaries. Changing SQLite durability settings requires
separate throughput and failure tests; do not silently redefine that guarantee.

## Outbox storage primitive

`internal/outbox` stores opaque exact payload bytes, keyed by SHA-256 and ordered
by an independent insertion sequence. It has no archive, knowledge or model
dependency. Its SQLite WAL uses `synchronous=FULL`. A private absolute database
path and directory are required; the database file must be regular, private and
not a symlink. Existing queued data is retained when configured limits decrease.

Transactional counters bound logical payload bytes and entry count, including
quarantined permanent rejections. Identical bytes do not consume another entry.
Permanent HTTP 400/413/422 responses may be quarantined without removing their
bytes or blocking later pending deliveries. Digest verification rejects changed
payload bytes before replay. Successful receipt processing deletes only the
corresponding pending entry, with repeated deletion remaining harmless.

The byte budget does not cap filesystem usage: SQLite pages, indexes, free space,
and WAL add overhead. Disk capacity and write failures need separate monitoring.
The outbox cannot strengthen the archive's power-loss contract by itself. A
subprocess test verifies recovery after successful Put followed by immediate
process exit without Close; this is not a simulated machine power failure.

## Producer configuration and execution

Set `spool_dir` to an absolute private directory (0700) to enable the outbox. The
database is `deliveries.sqlite` inside that directory. `spool_max_bytes` defaults
to 2147483648 and `spool_max_entries` to 10000. Omitted `spool_dir` preserves
legacy volatile delivery. Enabling/disabling, moving the directory or changing
outbox limits requires plugin restart; live reconfiguration rejects these changes
before replacing any active settings. Restarting with lower limits preserves
existing entries and refuses new capacity until retained work drains.

Capture still enqueues into the bounded memory queue. One background worker
serializes and persists deliveries, holding each memory reservation until Put
succeeds. A separate replay worker reads the oldest pending entry and sends its
exact bytes with commit negotiation. Missing/wrong receipts, plain 200 and legacy
202 responses retain the entry and retry. A verified receipt removes the pending
entry; permanent rejections are retained in quarantine. Storage errors apply
backpressure rather than deleting pending records. Network retry does not stop
new records from persisting until the logical disk budget fills.

Active requests and admitted records not yet persisted remain vulnerable to
process exit. Shutdown counts unspooled discards but retains already persisted
in-flight deliveries. This asynchronous design keeps fsync off response capture;
it does not promise that every completed callback has already reached disk.

Producer diagnostics include persisted/duplicate entries, outbox capacity waits, write/read
errors, oversize drops, verified commits, quarantines and missing commit receipts.
The cached outbox snapshot reports pending/quarantined entries and logical byte
usage without database access on capture callbacks. Snapshots travel in the
existing latest-producer report, can be stale, and are hidden for scoped readers.
A current delivery's report precedes its receipt and may still count that entry
as pending. Process-local counters reset; retained outbox contents do not.

Deploy the compatible collector before enabling the producer. Preserve pending
outbox files during updates and rollback. An older collector's 202 does not drain
an enabled outbox. An older plugin cannot replay it; keep the database intact
until a compatible plugin is restored. Future backup procedures must include the
outbox through SQLite online backup whenever it exists, rather than copying only
its main file. No automatic deletion, approval, Git write or knowledge activation
is introduced.

## Remaining verification and activation work

Tests also simulate SQLite capacity exhaustion with `max_page_count`, verify
that failed persistence rolls back counters while preserving prior entries, and
verify acknowledgement deletion followed by immediate subprocess exit. Native
before-commit failure leaves zero raw records before restart and exactly two
after replay; after-commit receipt loss leaves one before restart and two after.
Both use forced producer and collector termination. These do not simulate an OS
or hardware power failure.

1. Validate receipt fields through live HTTP/MCP/browser boundaries after a
   backed-up native deployment. Verify fresh captures persist and drain normally.
2. Extend native acceptance to deliberately unpersisted callbacks, corrupted-spool
   operator recovery and OS disk exhaustion. Before/after archive commit and
   post-deletion process tests already pass; SQLite capacity rollback is covered.
   Preserve exact boundary labels rather than claiming power-loss tests.
3. Measure throughput, fsync cost, disk/WAL growth and restart backlog drain with
   representative synthetic payload sizes and bounded concurrency.
4. Preserve the verified rollout procedure for future updates, including online
   outbox backups and active pending-file preservation. The archive's SQLite
   NORMAL power-loss limitation remains a separate durability decision.
