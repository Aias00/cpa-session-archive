# Durable delivery protocol

## Authority and rollout

The archive remains retained evidence; a producer spool is a temporary delivery
outbox, never another knowledge or retrieval authority. Delivery reliability is
independent of projection freshness and retrieval ranking.

The implementation includes an opt-in collector commit receipt and a tested
outbox storage primitive. Producer integration, end-to-end crash/replay acceptance
and production activation remain pending.
Existing senders still receive HTTP 202 on volatile queue admission. Do not
report durable delivery until the producer actually persists and verifies its
receipts.

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

## Required next increments

1. Persist exact producer payload bytes in a bounded private outbox outside the
   response callback. Count admission, persistence, replay, rejection and overflow
   separately. Records not yet committed to the spool remain crash-vulnerable.
2. Replay after restart; remove only on a matching commit receipt. Keep permanent
   rejections for explicit inspection without blocking later eligible deliveries.
3. Bound logical payload bytes, entry count, memory, retry concurrency and disk
   use; report capacity pressure and startup recovery. Avoid transcript/error body
   logging. Preserve pending outbox data during upgrade and rollback.
4. Test process termination before/after spool commit and archive commit, lost
   receipts, duplicate replay, incompatible collectors, cancellation, disk full,
   permanent rejection and corrupted spool. Run isolated native-host acceptance
   before production activation.
