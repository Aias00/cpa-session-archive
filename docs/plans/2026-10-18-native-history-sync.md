# Native history synchronization implementation plan

**Goal:** Import retained workstation Codex, Claude Code and Kiro CLI histories into the existing M4 archive and recall service, with resumable replay and provenance.

**Approved design:** Source adapters preserve original records and normalize messages/tools for the existing recall projection. Native histories retain separate client/device/session identities; no text-based merging with proxy traffic and no automatic curated-memory promotion.

**Architecture:** A local command builds a private SQLite staging archive using bounded native-history segments. Each segment has an explicit `native-history/v1` format, an immutable original JSON envelope, source byte offsets and a metadata context. The existing snapshot importer transfers committed sources into M4; the collector normalizer recognizes the native format. Durable local checkpoints advance after archive commit. Replay uses deterministic segment IDs. Full prefix hashing detects edited/truncated histories and fails that file explicitly rather than silently losing data. Missing native formats and malformed complete records remain retained and visibly partial.

**Constraints:** No new dependencies; no model calls or Claude tests; private files, no transcript/token output; loopback listeners only; preserve source histories, existing archive, and canonical Git memory. Deploy only after tests and verified online backups, to verified Apple M4 / Mac16,10.

## Tasks

- [x] Add three adapters and synthetic regression fixtures covering text, tools, metadata, Codex event/response duplication, compaction, unsupported/encrypted blocks, and branch IDs. Test first, then implement.
- [x] Add bounded discovery/segment synchronization, durable checkpoints, deterministic replay, active-tail handling, prefix verification and private summary reporting. Test interruption/replay, append, move, rewrite rejection, malformed lines and non-model execution.
- [x] Integrate native normalization with existing search/source citations and archive timeline; test end-to-end isolation and source round trips. Do not change legacy normalizer-v2 citations.
- [ ] Run full tests, race, vet and builds. Document operation/coverage. Commit exact source; build and test natively on M4, back up live SQLite and artifacts, deploy collector without restarting CPA.
- [ ] Execute workstation initial sync, transfer a consistent staging snapshot, merge on M4, verify zero-conflict replay, native client coverage, source citation reads and projection catch-up. Install periodic workstation synchronization only once the one-shot pipeline is verified.

## Interfaces

`internal/nativehistory` owns `Context`, `Envelope`, `Line`, `Message`, `Document`, `Inspect(client string, raw []byte, *Context)`, and `Normalize(Envelope) Document`. Context records native session/parent/branch, cwd, Git remote, model, timestamps and whether Codex response items exist. Envelope stores version, client, context and original line strings with absolute source byte offsets. Adapters cannot execute tools or models.

`internal/historysync` implements the runner. The sync runner accepts explicit roots/device and a private staging archive. Native segment IDs namespace client/device/logical source/byte offset/content digest. Original file rewrites are reported and left for explicit reconciliation; deleted sources are not propagated as destructive archive deletion. The native format is an archival representation, not HTTP traffic and not native-thread restoration.

## Verification / stop condition

All three clients have retained native sources on M4, replay adds zero existing segments, search/read-source works for each searchable client, live service is healthy and caught up with no new failures, and periodic sync status is observable. Reports contain counts/hashes/paths only. Any unreadable, malformed or opaque historical coverage is reported explicitly.
