# Mac mini M4 deployment

## Calibration tooling and rejected selection, 2026-10-20

Two new native diagnostic commands, `cpa-memory-calibrate` and
`cpa-memory-eval`, were built from exact source `9e4f39b` and added to the
existing M4 installation. Both paths were previously absent; exclusive creation
prevented replacement. No existing artifact or configuration was replaced and
no service restarted. The previous complete raw/projection/outbox rollback pair
was preserved. Tool checksums and private installation evidence are in
`/Volumes/data/build/cpa-session-archive/calibration-20261020-9e4f39b/reports`;
the installation also has a separate aggregate tool-installation record.

The [new synthetic calibration](semantic-calibration.md) evaluated ten
predeclared cosine thresholds on 24 supported and 24 empty-expected questions.
No threshold met both the 0.8 positive recall requirement and all negative
expectations. The report retained `selected_minimum: null` and exit 3; the
separate 32-question held-out set was not evaluated. All trials passed citation,
scope, forbidden-source and completeness gates. This is a failed exploratory
quality selection, not production acceptance or a storage migration trigger.

Full Go race/vet and native builds passed. The evaluator now refuses to count
truncated or semantically incomplete empty results as passing negatives, with
an additional explicit `--require-complete` gate. Provider failure/profile
drift tests confirm that an earlier eligible trial cannot survive a later
execution failure.

Post-install read-only checks observed 144,334 sources, lag 0 and failures 0.
CPA, collector and memory listener PIDs and existing artifact/configuration
hashes were unchanged. Production still has zero semantic tables and an explicit
hybrid request returns 503. The temporary local embedding provider was stopped
and port 8320 was verified unbound. Four actual owner questions were saved in
private intake with project scope/evidence unconfirmed; none was counted as a
completed label or copied into the repository.

## Experimental hybrid readiness, 2026-10-20

Memory server, MCP bridge and the new private-corpus preparation command were
built from exact source `f1a62bf` on Apple M4 / Mac16,10 and installed. Only the
memory LaunchAgent restarted. CPA PID 3300 and collector PID 25410 were unchanged;
the collector remains `9bc6205` and the durable plugin remains `572f0a1`.
Account/routing configuration, credentials, raw/projection/outbox files and
reviewed Git knowledge were preserved.

Production semantic retrieval is **disabled**. No embedding environment settings
were added, no semantic tables were created in the live projection, and no
production conversation content was embedded. Explicit hybrid requests return
503; default literal/diverse retrieval remains available. The temporary local
embedding process used for isolated testing was stopped; its pinned model and
isolated dependencies remain available for later calibration.

The [hybrid evaluation](hybrid-retrieval.md) used the unchanged frozen corpus:
recall/hit@5 increased from diverse 48/56 to 53/56, paraphrases from 0/8 to 5/8,
and MRR from 67.9% to 75.0%. Empty cases regressed from 6/6 to 4/6. Citation,
scope and forbidden-source checks passed, but the new `--require-empty-pass`
gate correctly exited 3. This is an experimental result, not a production
quality acceptance. No thresholds or frozen labels were changed to erase the
misses.

Full Go race/vet, native builds, formatting and six Python protocol tests passed.
Separate real-process checks passed actual E5 paraphrase retrieval, source
citations, scope-before-top-k, shared embedding deduplication, 14 exact commit
receipts, model outage with literal availability, restart/incremental discovery,
source outage recovery, and deletion/session-move invalidation before a blocked
projection could catch up. Additional regressions cover stale vector writes,
row-ID reuse, model drift, whitespace-only windows, retry recovery and explicit
coverage/truncation.

Live acceptance verified three citations from the pre-update projection,
Chromium default/diverse/source behavior and explicit disabled-hybrid errors.
A fresh real Codex client called hybrid (expected error), diverse and source
read, and verified matching text. Its default `gpt-6.1-sol` encountered upstream
capacity errors twice before any tools ran; a one-time CLI `--model gpt-6-sol`
override completed acceptance. Persistent client settings were not changed.
A broad pre-update diverse query hit the existing five-second budget during
backup verification; the later live browser query returned five results. This
does not establish production p95 or eliminate cold-query budget limits.

The final recorded status had 144,018 indexed sources, lag 0 and failures 0;
actual producer outbox entries/bytes/quarantine were all zero. Projection v3 and
normalizer v2 remain compatible. A private 50-slot owner-corpus draft was created
at `session-archive/private/recall-eval/owner-v1-draft.json`; it is deliberately
unfinished and contains no invented questions or gold labels.

Release artifacts, exact source, private logs and aggregate verification reports
are under `/Volumes/data/build/cpa-session-archive/hybrid-20261020`. The newest
verified raw/projection/outbox rollback set has checkpoint/raw fence 302748;
old binaries, LaunchAgents and install manifest are retained. The current and
hybrid rollback markers point there. The preceding decoupled database pair and
its empty outbox backup were retired only after zero missing source IDs/CAS
hashes and no open files were verified; about 33.6 GiB of logical backup files
was removed, while old
artifacts/configuration/reports were preserved. Raw source histories and active
replay files were not removed.

## Independent memory service rollout, 2026-10-20

Collector and memory-server source: `9bc6205` (process separation `0e8b1eb` plus
strict feed-control response validation). Both were built from the exact source
archive on Apple M4 / Mac16,10. The existing MCP bridge remains `5349321` and
continues to use the unchanged recall API. CPA/plugin/account/routing and client
configuration were preserved.

Collection and recall now have separate LaunchAgents:

| Service | Listener | Owns |
| --- | --- | --- |
| `com.linonetwo.cpa-session-collector` | 127.0.0.1:8318 | Raw archive, durable commit boundary and authenticated feed |
| `com.linonetwo.cpa-memory` | 127.0.0.1:8319 | Projection, index worker, recall credentials and HTTP |

The collector has no `MEMORY_*` environment settings and its Go dependency graph
has no memory/recall packages. The memory service opens only the projection,
validated by live file descriptors; it obtains authoritative sources through the
new dedicated private feed token. Existing owner/scoped read credentials cannot
read the full-source feed. See [protocol and recovery](decoupled-memory.md).

The existing projection was explicitly adopted once against the intended archive.
Its persistent identity and monotonic watermarks were verified, migration
authorization was removed, and another memory-only restart succeeded. Collector
and CPA PIDs stayed unchanged across that restart. The projection remains v3,
normalizer v2. Three citations sampled from the verified pre-migration projection
still returned byte-identical normalized text through the new service.

Full Go race/vet checks and native collector/server/MCP builds passed. Frozen
literal and diverse evaluation scores/categories remain exactly unchanged.
Separate process preflight verified offline memory startup, collection under
broken legacy memory settings, ten exact commit receipts, a commit while memory
was killed, checkpoint-preserving source outage/recovery, unchanged archive ID
across collector restart, explicit deletion/410, MCP owner/scoped search/recap,
Chromium and graceful memory shutdown. Safety tests additionally cover wrong
archive/history/rollback, explicit legacy adoption, ambiguous protocol controls,
bounded body/concurrency, cancellation, raw-path preservation and duplicate
projection owners, including hard links.

Live acceptance verified the old installed bridge, Chromium and a fresh real
Codex invocation through CPA. Codex used diverse `archive_search`, read the
returned citation and checked its text. All 21 deployment citation roundtrips
passed. Recorded literal samples were 314–561 ms; diverse samples 166–172 ms.
These are samples, not production p95 or cold-cache guarantees. A transient
upstream model-capacity error was followed by a successful Codex retry.

During cutover, the verification script retained its own read-only inspection
connection and mistook it for an old projection owner. That delayed the intended
memory restart; collection continued. The connection was closed, the normal
memory LaunchAgent restored, and a further independent restart/acceptance passed.
No raw/projection/outbox data was restored or replaced during recovery.

The final recorded corpus had 142,635 sources, lag 0 and failures 0. The new
collector epoch had 189 committed deliveries, zero queue refusals and zero
discards. Actual producer outbox backlog/bytes/quarantine were zero at deployment;
its live files were preserved. Verified online pre-update raw/projection/outbox
copies are retained under
`/Volumes/data/build/cpa-session-archive/decoupled-20261020/rollback` with old
artifacts/configuration/manifest. Projection checkpoint 300172 is covered by raw
fence 300172; the raw backup contains 141,846 records. The install manifest,
`decoupled-memory-rollback-dir.txt` and `current-rollback-dir.txt` identify the
current rollback set. Rollback must stop the new memory service before restoring
the prior collector's embedded-memory configuration; preserve active data and
producer replay files. Prior superseded database copies are retired only after
record-ID/CAS inclusion and open-file checks, with their artifacts retained.

## Diverse literal retrieval rollout, 2026-10-20

Collector and MCP bridge source: `5349321`, built from the committed source
archive on Apple M4 / Mac16,10. The evaluator was built from the same archive for
artifact-level comparison and remains outside production `bin`. Only the
collector restarted. CPA PID, plugin `572f0a1`, account/routing settings, read
credentials and client configuration were preserved. Default literal retrieval,
projection v3 and normalizer v2 remain compatible.

The explicit [diverse mode](diverse-retrieval.md) spreads candidate selection
across repeated exact content and long-message windows, then suppresses identical
displayed excerpts within role/kind/tool identity. Each result retains a current
single-source citation. Search/MCP/recap and the page expose the option; the page
requests five results in diverse mode. A one-second recent-prefix attempt falls
back within a five-second total diverse budget, with explicit HTTP 422 recovery
advice instead of a false empty success. Full fallback starts from FTS matches;
large-project-first scanning had caused avoidable latency.

Full Go race/vet checks and native collector/bridge/evaluator builds passed.
Separate tests cover 1,100 repeated occurrences, 1,100 long-message windows,
deleted/replaced/moved representatives, refill past literal/stale leaders,
authorized source/time/client filters, fast/full ordering equivalence,
role/tool/pointer attribution, cancellation and the real connection-wait budget.
The frozen synthetic corpus remains byte-identical: recall/hit@5 improves from
36/56 (64.3%) to 48/56 (85.7%), MRR from 57.1% to 67.9%, and identical excerpt
slots from 48/110 to 0/74. Empty cases and citation/scope/forbidden gates all pass;
paraphrases remain 0/8. These are synthetic results, not owner relevance scores.

Exact release artifacts passed an isolated real-collector/commit-receipt test,
MCP owner/scoped checks, literal/diverse search/source/recap roundtrips and
Chromium interaction. Live installed-binary samples returned three diverse hits
at 162.58–163.56 ms; default literal had one 3.47-second sample and warm samples
at 279–283 ms. All 18 deployment citation reads matched. Live Chromium verified
five diverse hits and source access without browser token storage. A fresh real
Codex invocation through CPA used `archive_search` in diverse mode and then
`archive_read_source`, checked the text match and returned the fixed success
marker. Its per-invocation MCP configuration did not change user configuration.
This verifies retrieval integration, not full project-continuation acceptance.

After catch-up, the checked corpus had 141,600 sources, lag 0 and failures 0.
Status samples were 0.81–5.4 ms. Collector and producer sampled queue/drop/error
counters were zero. The active outbox retained the same file identity throughout
the update; its actual backlog, bytes and quarantine were zero. The producer's
latest process snapshot lagged actual acknowledgement by one, as expected.
These samples do not establish cold-cache or concurrent-capture p95 guarantees.

Before replacement, online raw/projection/outbox copies passed integrity and
hash verification. Projection checkpoint 299105 was covered by raw fence 299106;
the raw copy contained 141,330 records. Private release/rollback evidence is in
`/Volumes/data/build/cpa-session-archive/diversity-20261020`. The install manifest,
`diverse-retrieval-rollback-dir.txt` and `current-rollback-dir.txt` identify the
newest complete set. Active archive/projection/outbox files were preserved.
The predecessor `durable-572f0a1` database pair was retired only after zero
missing record IDs/CAS hashes and no open files were confirmed; its old
binaries/configuration, removal report and required baselines remain.

## Coverage and delivery diagnostics rollout, 2026-10-20

Collector and MCP bridge source: `7a88d15`. Both were built from the committed
source archive and installed on Apple M4 / Mac16,10. Only the collector was
restarted; CPA remained PID 637, and accounts/routing/client configuration were
preserved. Existing bridge processes can continue with their previous schema;
fresh bridge processes expose the new optional diagnostic fields.

The [observability contract](memory-observability.md) defines conservative
coverage categories, resumable historical metadata refresh, collector HTTP
delivery counters, epoch boundaries and credential scoping. Normalizer v2 and
projection v3 citations/text remain compatible. The per-source coverage policy
marker is additive and does not change message-metadata schema version 1.

Production-sized preflight used 140,096 sources and 17,276,810 chunk occurrences.
Coverage refresh completed in approximately seven minutes without rewriting FTS
or existing message metadata. Search result/citation hashes matched the previous
binary. The final snapshot classified 107,342 sources as having only expected
omissions and 22,205 as needing investigation; this is not a loss estimate.
Source reason counts can overlap.

The contention test held the archive WAL writer while posting 4,300 synthetic
deliveries: 4,160 were admitted, 140 were refused by the full queue, one storage
retry was observed, and no admitted delivery was discarded. Every admitted
delivery committed and indexed after releasing the lock; deletion then restored
the original corpus and coverage counts. Invalid input and truncation counters
were also checked. All fixtures used isolated databases and ports.

Full Go race/vet/build checks passed. A real new/old/new collector sequence
verified old-writer compatibility and subsequent coverage refresh. Chromium
checked owner/scoped presentation, legacy API responses and authentication
errors; live Chromium checked the deployed page without displaying transcripts
or credentials. Live MCP preserved the new owner fields and omitted them for a
project-scoped bridge. Search/source roundtrips and stale-citation rejection
passed, and further real records committed after the convergence check.

Live historical refresh completed with zero change-feed lag and zero projection
failures. At the recorded check, all 18 admitted deliveries in the new epoch had
committed, with no invalid requests, queue refusals, write failures or discards.
Status p95 measured 3.67 ms and the example warm search median was 144.91 ms.
At that rollout, counters covered the collector HTTP boundary only; plugin-side
drops were unobserved and HTTP 202 was a volatile queue acknowledgement. Later
producer diagnostics and opt-in commit delivery are recorded below.

A new verified online raw/projection pair, old collector/bridge, LaunchAgent and
manifest were preserved before replacement. The archive fence followed the
projection snapshot fence; external copies were hash-verified. The installed
manifest and `memory-observability-rollback-dir.txt` identify the newest private
external-volume rollback pair. Its predecessor's database pair was retired only
after zero missing record IDs/CAS hashes and no open files were confirmed; old
binaries/configuration and migration baselines remain. Generated preflight
databases were removed. Final capacity checks had no warnings.

Follow-up diagnosis found that the dominant missing-payload bucket consists of
non-streaming successful OpenAI records with no model identity. The host's
model-list lifecycle has that shape. Verify explicit control-operation tagging
with synthetic lifecycle events before changing collection or relabeling old
records; do not infer conversation loss from these counts alone.

## Memory performance and stability rollout, 2026-10-20

Collector and online backup tool source: `59d60bd` in the user's fork. Built
from the committed source archive on Apple M4 / Mac16,10 using the isolated
Go SDK at its current `/Volumes/data/tools/cpa-go-sdk` location. The collector
was replaced and restarted; CPA remained PID 637. Accounts, routing, listener
addresses and client configuration were preserved.

The [memory architecture](memory-architecture.md) now documents archive,
retrieval and reviewed-Git boundaries. This rollout adds transactional corpus
counts, a bounded read-only WAL pool, candidate-ID materialization before text
loading, a pinned online-backup snapshot and deletion-safe historical SSE repair.
Projection version remains 3 and citation normalizer version remains 2.

Controlled preflight used independent production-sized snapshots with 139,806
sources and 17,189,795 chunk occurrences. No live conversation text was printed.

| Check | Installed baseline | Candidate |
| --- | --- | --- |
| Status median / p95, 20 reads | 113.91 / 128.23 ms | 0.38 / 0.70 ms |
| `SQLITE_BUSY` warm median, 3 reads | 266.18 ms | 126.59 ms |
| 100 synthetic records accepted and indexed | 0.54 s | 0.58 s |
| Five search results, including citations | Identical result hash | Identical result hash |

These are bounded smoke measurements, not a labeled search-quality evaluation
or an all-query latency SLO. Ingestion timing includes worker polling. Both
versions converged after synthetic deletion. Preflight exposed orphaned session
work items blocking SSE startup repair; regression tests now cover deletion both
before enumeration and between enumeration and processing. The repaired binary
started against the same affected snapshot successfully.

Full Go tests with race detection, vet and native builds passed. A disposable
new/old/new binary test verified schema reopening and aggregate maintenance by
the older writer. Live health/readiness, Web delivery, unauthorized access
rejection, stale-source rejection and HTTP/MCP search/source roundtrips passed.
Actual newly committed OpenAI/Responses records confirmed capture continued.
The workstation tunnel configuration was not changed; this rollout verified the
M4 loopback endpoints rather than claiming a remote-workstation browser test.

Live status median/p95 measured 0.48/0.60 ms. The example search first took
2.86 seconds, followed by a three-read warm median of 105.90 ms. Cold query
latency remains a measured improvement area; no global subsecond claim is made.
At the live check, backfill and metadata were complete, lag was zero and
projection failures were zero.

Before replacement, a verified online raw/projection pair and previous binaries,
LaunchAgent and manifest were preserved. The installed manifest and
`memory-architecture-rollback-dir.txt` identify the private rollback directory
on the external volume; mount that volume before using it. External copies were
hash-checked against the verified snapshots. Two superseded database pairs were
retired only after checking retained record IDs and CAS hashes and confirming
no open files. Their binaries/configuration and required migration baselines
remain. Generated preflight databases were removed. Final capacity inventory
reported about 80 GiB free, no warnings and sufficient backup headroom.

The user authorized the Mac mini M4 as the standing deployment target on
2026-10-17. After code verification, deploy and check the live service without
another routine permission handoff.

## Verify the target first

Use `ssh mac-mini` or `ssh mac-mini-m4`, user `aias`. Before changing services, verify:

```sh
ssh mac-mini-m4 'sysctl -n machdep.cpu.brand_string; sysctl -n hw.model'
```

The target reports **Apple M4 / Mac16,10**. Both workstation SSH aliases
`mac-mini` and `mac-mini-m4` now select this M4. The legacy Apple M1 / Macmini9,1
host is available as `mac-mini-m1`. The workstation SSH configuration holds the
actual address and deployment-key selection. Its existing public key is
authorized on the M4. This is an SSH alias change: the Tailscale DNS name
`mac-mini.tail73d4c.ts.net` still identifies M1; only the SSH aliases changed.

During this rollout the legacy M1 received a collector update before the host
identity mismatch was discovered. Its hardware provenance was corrected and its
archive retained. The requested M4 was then separately verified and deployed.
Future updates target the M4.

## Installation and access

- User/home and launchd domain: `aias`, `/Users/aias`, `gui/501`.
- Installation: `/Users/aias/Library/Application Support/CLIProxyAPI/session-archive`.
- Collector label: `com.linonetwo.cpa-session-collector`.
- Collector executable: `bin/cpa-session-collector`.
- Ingestion/query listener: `127.0.0.1:8318`; recall: `127.0.0.1:8319`.
- Raw archive: `data/archive.sqlite`; recall projection: `data/memory.sqlite`.
- Read token: `private/memory-read.token`; launchd receives its path only.
- Native Go SDK: `/Volumes/data/tools/cpa-go-sdk/bin/go`, version 1.26.3.
  This isolated user SDK and verified module cache avoid system PATH changes.
- Workstation recall: <http://127.0.0.1:18319/>.
- Workstation M4 archive:
  <http://127.0.0.1:18327/v0/resource/plugins/cpa-session-archive/index.html>.
- Workstation read token:
  `/Users/aias/Library/Application Support/CLIProxyAPI Access/memory-read-token-m4.txt`.
- Workstation tunnel labels: `com.router-for-me.cliproxyapi-recall-access` and
  `com.router-for-me.cliproxyapi-m4-access`.

Recall and management use separate credentials. The pre-existing workstation
management key was verified against the M4. The legacy M1 archive remains on
port 18317 and its read token is retained separately. CPA remote management
remains disabled; data listeners and SSH forwards bind loopback only.

The collector LaunchAgent runs at user login and restarts after an unexpected
exit. It does not start before user login. Logs are under `session-archive/logs`.

## Release procedure

1. Verify the M4 hardware before any service mutation. Run targeted/full tests,
   race checks and vet; commit the source in the user's fork.
2. Transfer a `git archive` of the exact commit into
   `/Volumes/data/build/cpa-session-archive/<release>/source` on the M4.
3. Run native full/race tests and vet, then build on the target. Set `GOROOT` to
   the isolated SDK and invoke its `bin/go`; use the verified module cache with
   `GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`, `CGO_ENABLED=1` when
   dependencies have not changed.
4. Take a verified SQLite online backup of the raw archive. Preserve the old
   binary, manifest, install notes and LaunchAgent in a private timestamped
   `backups/before-...` directory. Do not copy only a live SQLite main file.
5. For upgrades, preflight an isolated copy with production startup/repair
   settings. Let migrations and historical indexing finish before cutover when
   a new projection schema requires a rebuild. Preserve staged/old projections
   separately; never replace the original archive with the historical snapshot.
6. Stage artifacts, stop only the collector when replacement requires it,
   atomically replace the executable/configuration, and restart its LaunchAgent.
   A collector-only change does not require restarting CPA. Keep interruption
   short because the plugin/collector queues are finite and volatile.
7. Verify readiness, authentication, source citations, index catch-up, enabled
   plugin state and browser access through the workstation tunnel. Record
   source/binary hashes and results in the private manifest without printing
   tokens or source text.

For the first M4 install there was no collector LaunchAgent. The existing CPA,
plugin and 6,096-source archive were already present. The archive was backed up
and retained; no M1 database was imported during the M4 rollout. The newly
registered collector built its first projection in the background.

## Rollback

`builtin-recall-rollback-dir.txt` identifies preserved artifacts. Stop the
collector and restore the previous binary/LaunchAgent when one existed. The
first M4 install had no prior collector LaunchAgent: rolling it back means
unloading the new agent and restoring the old files, while preserving the
current raw archive. Never restore an older archive merely to undo executable
or configuration changes. This first projection contains no curated user state
and can be rebuilt from retained evidence.

## Verified rollout

Deployed collector source: `097220c852602ea36baa6361f28aa2b27cceabca`, built
natively on Apple M4 / Mac16,10 with Go 1.26.3 on macOS 27.0. CPA's executable,
account/routing configuration and existing v0.8.12 plugin dylib were retained;
CPA was not restarted.

Passed checks:

- Native full Go suite, race detector and vet on the actual M4.
- Archive backup/SQLite quick_check, native artifact checksum, registered/enabled
  plugin, collector health/readiness, authenticated API and unauthenticated denial.
- Interactive Chromium search, source reads and recap through the M4 tunnel;
  the page stored no token in browser storage.
- Completed 6,096-source backfill and change-log replay: 2,326,204 chunks,
  checkpoint/watermark 12,192, zero lag and zero processing failures at the
  final verification sample. Partial coverage is disclosed by the application.

A startup preview-repair WAL snapshot-upgrade race was found during the legacy
host attempt, deterministically reproduced and fixed before the M4 rollout.
The bounded repair batch now reserves the writer before reading payloads. The
M4's first full startup repair took approximately 63 seconds and completed.

The M4 projection is approximately 8.4 GiB, with approximately 140 GiB free at
verification. Repeated context remains separately attributable across request
snapshots; source-aware deduplication/retention is a subsequent improvement.
At this initial rollout MCP was not deployed; its subsequent update is recorded
below. Semantic retrieval and automatic memory extraction remain unimplemented.

## Legacy archive merge

The legacy M1 archive was subsequently merged into M4 with the standalone
`cpa-session-import` tool (commit `99c51be`). It added 1,413 missing requests,
kept existing M4 rows intact, and completed memory-index catch-up. See the
[migration record](archive-migration.md) for snapshot cutoff, backups and checks.
The collector did not need a restart or executable update for this operation.

## MCP and shared-text rollout (2026-10-18)

Collector and stdio bridge source: `e2ce86c03fc442685082121464f90f61ba115819`.
The official MCP Go SDK v1.8.0 requires Go 1.25+, satisfied by the M4's isolated
Go 1.26.3 SDK. Both native full/race tests and vet passed.

The collector now loads `MEMORY_READ_CREDENTIALS_FILE` from the private credential
registry and serves independent owner-wide read credentials for the owner's
Codex/Claude clients. Fixed project/session credentials are supported and tested
server-side, including source reads. The old Web read credential still works.
The local bridge is installed under `CLIProxyAPI Access/bin/cpa-memory`; the M4
also has `session-archive/bin/cpa-memory`. Both clients have user-level stdio
entries named `cpa_archive`. Tokens are referenced by private file path only.
See [MCP setup](mcp-recall.md) for tools and scopes.

Projection v3 shares exact text/FTS entries while preserving occurrence identity,
source scope, revisions and normalizer-v2 citations. A separate preflight corpus
completed before cutover. The old index and raw backups were preserved under
`backups/before-mcp-dedup-20261018T072639Z`; the collector switched and became ready
in approximately 8 seconds, without restarting CPA.

Active index size at switch: 638,304,256 bytes versus 11,978,981,376 bytes before
switch (94.67% smaller). The old bytes are still retained for rollback. Final
verification recorded 8,401 sources, 3,231,486 occurrence chunks and 30,467 unique
texts, zero lag and zero processing failures. Browser search/source/recap, both
SQLite integrity checks, scoped source denial and old-citation reads passed.

Real Codex and Claude CLI tests each called search and source-read tools and
verified a known harmless historical marker. Claude's existing first-party
model login returned HTTP403 before it could use tools; the successful Claude
CLI test used a temporary M4-compatible model/API override. Its persistent
login/model configuration was not changed. Newly started clients can discover
the installed MCP entry; existing sessions may need to restart.

User configuration backups live in the private workstation access directory
under `backups/before-recall-mcp-20261018T003008`. Runtime artifact hashes,
credential paths, verification counts and rollback paths are recorded in the
private install manifest. No curated or generated long-term memory is claimed.

## Reviewed Git memory bridge (2026-10-18)

The optional bridge was updated to source commit
`fb5526f61310d4412237076b7aedd8cc5b18c788` on workstation and M4. M4 native
full/race Go tests and vet passed. The collector remains at its earlier source
commit; no collector/CPA restart or archive/projection migration was needed.
The private manifest records independent artifact hashes and the bridge's
pre-update backup/verified raw snapshot.

The production Codex stdio entry adds the existing workstation knowledge Git
root and an owner-configured project-root map. It exposes `project_memory` and
opt-in `memory_propose` in addition to the four archive tools. Confirmation and
archive require a separate `cpa-memory review` operator action with the expected
revision and explicit reviewed attestation; there is no MCP approval tool.
Knowledge stays in existing Git Markdown/YAML authorities, not the M4 SQLite
projection. See [the curated memory guide](curated-git-memory.md).

Codex-only acceptance completed draft/confirm/correct/archive in a disposable Git
fixture, and separately read an existing confirmed entry from the actual KB. The
user's staged/unstaged KB work was preserved. Claude was not invoked and its
configuration remained unchanged. Synthetic test conclusions were not added to
the user's canonical repository.


## Native client history rollout (2026-10-18)

The collector's native-format reader is deployed from `48595a9`. The standalone
native history sync CLI and workstation orchestration are from
`a60ec7f518c074104df63d0bc8caa163517f4c78`; the snapshot importer is from `dadb9d6`.
The private manifest tracks these independent artifacts. The curated-memory bridge
and CPA executable/plugin were retained. Only the collector was restarted during
its initial reader upgrade; later CLI and attribution updates needed no restart.
Native full/race Go suites, vet and Python synchronization/repair tests passed on
both workstation and the actual M4.

Initial workstation import covered 5,387 files and 34,812,525,197 original bytes.
The 111,619 native archive segments comprise 108,437 Codex, 3,095 Claude Code and
87 Kiro CLI sources. These are segments, not conversation counts. After native
fork attribution repair, retained session identities were 4,091 Codex, 1,241
Claude Code and 33 Kiro; Kiro includes metadata-only identities. Original local
histories were retained, with unsupported/encrypted content stored but not
claimed as searchable text.

The first 78,208 sources and a 33,411-source remainder merged with zero conflicts;
replaying the remainder before attribution repair added zero records or blobs.
Full source/destination comparison matched all 28 retained metadata/CAS-reference
columns, excluding local row IDs. Verified online backups and previous binaries
are recorded under `backups/before-native-history-*`, `before-native-delta-*`, and
`before-native-attribution-*`. No old CPA rows were replaced.

Fork rollouts can contain inherited parent session headers. The filename UUID is
now authoritative and the parent header remains intact in original payloads.
An audited set of 81 Codex files and 324 Claude histories with multiple workspaces
received ambiguous project IDs, preserving owner/session recall without assigning
mixed content to a registered Git project. Both staging and M4 metadata were
repaired transactionally; original CAS bytes remained unchanged. These scope
flags are conservative and may include worktrees of the same repository.

The workstation LaunchAgent `com.router-for-me.cpa-native-history-sync` runs after
login every 1,800 seconds. Its private installation is under `CLIProxyAPI
Access/history-sync`; configuration, reports and status files stay outside Git.
The first scheduled run collected another 57 segments / 16,500,095 source bytes,
exported only those 57 records, copied 114 blobs, and merged with zero conflicts
or failures. The job completed with exit code 0. It stages locally before trying
M4 connectivity and acknowledges only committed, verified transfers.

Final checks found 111,676 native sources with zero missing or incorrectly scoped
projection rows, authenticated search/source round trips for all three clients,
collector readyz 200 and unauthenticated recall denial. A real read-only Codex run
completed archive_status, archive_search and archive_read_source successfully.
Claude was not invoked for testing. The live projection reported zero failures;
active CPA traffic can produce a transient lag of one while native coverage is
complete. Curated Git knowledge was not populated automatically.

See [native history operation](native-history-sync.md) for replay, incremental
exports, partial coverage, attribution repair and source-preserving behavior.


## Magpie-informed history adapter rollout (2026-10-19)

Collector, native history CLI and MCP bridge were built natively on the verified
Apple M4 from `7480384` after workstation and M4 full/race Go suites, vet, Python
sync tests and builds passed. The workstation sync CLI and bridge use the same
source. Source changes include `c1dc1e4` and `362fa33`; the private manifest tracks
artifact hashes independently. CPA and its routing/account configuration were
retained. Online backups of both databases passed `quick_check`; previous
binaries, LaunchAgent and manifest are retained under
`backups/before-sse-wal-fix-20261019T065429Z`.

An initial cutover failed readiness and rolled back. Investigation found a
pre-existing SQLite deferred read-to-write upgrade race in historical SSE
normalization; the old collector also hit it. A deterministic competing-writer
regression failed before the fix and passed after reserving the WAL writer before
manifest reads. The final collector completed 99,170 pending normalization checks
with no response rewrites, then passed `/readyz`. Queued startup changes were
drained with zero projection failures before the independently versioned message
metadata idle backfill. Deployment readiness alone
does not assert metadata completeness.

A real incremental run scanned 5,435 files and skipped 5,433 unchanged files in
0.491 seconds of local collection. It collected four new segments / 255,030 bytes;
the M4 importer replayed 129 already-committed records and added four, with zero
conflicts or remaining records. These timings measure local collection, not the
complete SSH transfer or a cold integrity verification pass.

Authenticated search/source round trips passed for Codex, Claude Code and Kiro
history. The saved pre-upgrade normalizer-v2 citation returned identical text and
additive kind metadata; unauthenticated recall remained denied. A real ephemeral,
read-only Codex run through the M4 provider called `archive_status`,
`archive_search` and `archive_read_source`, with all four structured acceptance
checks true. Claude was not invoked. Existing curated Git knowledge and client
configuration were retained. Segmented/compressed histories were exercised with
synthetic fixtures because this installation currently has no real compressed
rollouts; other clients remain future adapter work.


A subsequent incremental run scanned the same 5,435 files in 0.541 seconds, skipped
5,426 unchanged files and imported nine new segments with zero conflicts. At that
check, index lag was one and failures were zero. Frequent live traffic can add new
sources between observations; source counts and lag are point-in-time values.


Final verification at `2026-10-19T07:29:57Z` confirmed
`message_metadata_done=true`, zero missing/stale metadata markers, and all 111,893
native archive segments present under the correct client/project/session scope.
The projection held 123,955 total sources, with zero failures and a live lag of
two. Message classification included 880,907 text, 105,193 context, 1,409 summary,
1,728,018 tool-use and 2,495,640 tool-result messages; 4,223,658 messages carried a
tool name or call identifier. These are retained message occurrences, not unique
conversations or distinct facts. The additive backfill completed without changing
the citation normalizer or rebuilding the v3 text schema. Detailed aggregate
verification is retained privately on M4 in `magpie-history-verification.json` and
the artifact manifest.


After metadata completion, all three client search/source round trips passed
again; the observed status had zero lag and zero failures. End-to-end search plus
source-read checks took approximately 4.8 seconds for the selected Codex term,
19.3 seconds for the Claude Code term and 64.8 seconds for the broad Kiro term.
These are different query workloads, not a client speed comparison. Broad recall
queries still need performance work; the subsecond unchanged-file scan does not
imply subsecond retrieval. No semantic/vector retrieval was added in this change.


## Capacity guard rollout (2026-10-19)

The read-only `archive-capacity.py` operational tool was deployed from `d1ea91f`
after all 14 Python tests passed on workstation and native M4 Python. It runs at
login and every six hours through `com.router-for-me.cpa-archive-capacity`;
capacity and error logs remain private. No Go artifact, CPA/collector listener,
Git knowledge authority or database schema changed; services were not restarted.
See [capacity operations](archive-capacity.md) for exit codes and backup preflight.

Both databases in `before-sse-wal-fix-20261019T065429Z` and
`before-magpie-adapters-20261019T042236Z` passed fresh read-only SQLite integrity
checks. With explicit user approval, exactly the old `memory-v2.sqlite`,
`memory-v2.sqlite-wal` and `memory-v2.sqlite-shm` files were removed from
`before-mcp-dedup-20261018T072639Z`. Their combined logical size was 12.01 GiB;
observed available space increased from 1.13 to 13.14 GiB immediately afterward.
The old v2 projection rollback copy is no longer available. Active v3/raw data,
the two complete verified backup pairs and old binaries/configuration remain.
Deletion evidence is retained in that backup's `v2-projection-removal.json`.

The final capacity report observed 11.09 GiB free, approximately 79.48 GiB of
logical backup data, no inventory warnings, and a 42.70 GiB conservative
new-backup requirement including reserve. The scheduled job correctly exits 1
for this space warning. It does not automatically block backup programs; the
operator must run the preflight before another whole-database backup. Further
retention/off-host backup work is still required before routine full backups can
resume on the same volume. Final service checks returned readyz 200,
`message_metadata_done=true`, zero projection failures and live lag four.


## Additional native adapters (2026-10-20)

Reader and history-sync binaries from `bda3826` passed full/race Go tests, vet,
Python tests and builds on workstation and verified M4. The collector was
restarted and became ready after 9,624 historical normalization checks; CPA was
not restarted. An online raw/memory backup pair passed SQLite integrity checks
under `before-additional-native-20261020T083356Z`, with old binaries/configuration
retained for rollback. Following the user's instruction to remove old backups
after use, the raw database from `before-native-attribution-20261018T144747Z` and
the raw/memory databases from `before-magpie-adapters-20261019T042236Z` were
removed with their sidecars. Configuration/binaries and the newer complete
`before-sse-wal-fix` pair were retained; this cleanup recovered about 45 GiB.

An isolated real workstation corpus captured 650 native files/databases and
3,741 segments (1,237,906,627 original JSON bytes) without collection failures.
Client segment counts were Qoder 1,872, WorkBuddy 1,492, CodeBuddy 243,
OpenCode 62 and ZCode 72. Isolated search/source checks passed for all five,
metadata backfill finished and index failures/lag were zero. An unchanged replay
added zero segments in 0.217 seconds: 647 JSONL files were proof-skipped and
134 existing database rows were recognized. Source databases were opened read-only.
These are source segments/message snapshots, not conversation counts.

The formal periodic-job capture also completed without failures (3,758 segments,
including new original-client history), and its immutable delta was sent to M4.
Transfer/import/live indexing and Codex acceptance are recorded separately below
when verified. The deployed workstation binary now discovers the additional roots
without changing existing credentials, model configuration or curated Git memory.


The initial delta import committed all 3,758 records in 20.5 seconds, copying
7,516 CAS blobs with zero conflicts or remaining records. Projection checks
found zero missing records and zero client/project/session scope mismatches.
Five-client Web/API search/source round trips and the saved old citation passed.
A real read-only ephemeral Codex run called status, five searches and a source
read and returned all three acceptance booleans true. The observed live status
had both backfills complete, zero failures and zero lag. No Claude test was run.

The original SSH upload was slow over a DERP relay. An authenticated temporary
transport served only an AES-encrypted snapshot; the decryption key and expected
hashes traveled through SSH. A truncated first response was rejected, then range
requests resumed the ciphertext. Ciphertext/plaintext SHA256 and SQLite integrity
checks passed before import. The temporary tunnel/server and ephemeral keys were
removed. The source event range was reconstructed against the immutable exported
IDs and required exact set equality before acknowledging its transfer watermark.
The archive/import/index checks were required before that acknowledgement.
No private source rows, credentials or transport keys were written to Git.

A follow-up regular sync scanned 6,124 files/databases in 1.279 seconds, skipped
6,115 unchanged files and recognized 134 existing database rows. It collected
eight new local segments and transferred 51 pending segments, all imported with
zero conflicts or remaining records. Live recall then had lag one and zero
failures. The 30-minute workstation LaunchAgent was restored after the cutover.

Following the user's backup-retention instruction, the old `before-sse-wal-fix`
database pair and sidecars were retired after the new verified backup was shown
to include every old archive request ID and CAS hash. Binaries/configuration and
removal evidence were retained; the newest complete rollback pair remains under
`before-additional-native-20261020T083356Z`. Approximately 28.55 GiB was removed,
leaving about 43.14 GiB free at that check. Raw source histories and active
archive/projection databases were retained.

## Capture queue and producer diagnostics rollout (2026-10-20)

Source `10042d2` was built from a clean `git archive` on the Apple M4. Collector,
MCP bridge and native plugin were deployed together. The installed CPA executable
first loaded the release-identical plugin in an isolated configuration with a
synthetic provider: stream/nonstream capture retained inputs and replies, model
catalog callbacks were skipped, and capture telemetry reached the mock collector.
Full Go race tests and vet passed. Synthetic Chromium checks covered owner,
scoped and legacy status rendering.

The private release directory is
`/Volumes/data/build/cpa-session-archive/capture-10042d2`. Its verified rollback
pair has raw/projection checkpoint 297502. The raw copy contains 140,553 records;
the projection passed SQLite quick_check. Old plugin, CPA executable, collector,
bridge, configuration, manifest and LaunchAgents are retained there. The
projection verification on the external volume took about 725 seconds; do not
interpret a slow verification as process termination or restart a live copy.

The first cutover was automatically rolled back because its 30-second readiness
window expired during normal startup repair. The second cutover used a longer
readiness window and succeeded. CPA was stopped before draining the collector;
new artifacts were installed atomically and CPA restarted after collector
readiness. No account/routing settings or active database files were replaced.
Native plugin mapping and all three installed artifact hashes were checked.

Live acceptance verified authenticated status, invalid-token denial, three
revision-checked source excerpts, MCP owner metrics and scoped diagnostic
omission, and actual Chromium rendering. Initial producer observation reported
five admitted records, no count/byte/oversize/invalid drops or permanent HTTP
rejections, and 92 skipped catalog callbacks. These are process-local samples,
not a durable no-loss guarantee. Collector rejection/discard counters were zero
and projection lag/failures were zero at the observation.

Status requests took approximately 1.5–7 ms. A cold broad `sqlite` search took
16.2 seconds; two warm repeats took 1.14 and 1.12 seconds. A `queue_max_bytes`
search took 1.06 seconds. These measurements leave the subsecond search target
unproven and identify cold/broad candidate retrieval as follow-up performance
work. They must not be replaced by the faster earlier query baseline.

After successful live checks, the superseded observability database pair was
retired only after proving that the new raw backup contains every old record ID
and CAS hash and that no retired file was open. Binaries/configuration and
migration baselines were retained. Aggregate reports, backup hashes, rollback
provenance and removal evidence are in the private release directory.

## Broad-query candidate rollout (2026-10-20)

Collector source `9e32762` was built from its exact archived commit and deployed
without restarting CPA or changing the native plugin. The private release root
is `/Volumes/data/build/cpa-session-archive/search-9e32762`. Online backups were
verified before replacement: projection checkpoint 298212, raw fence 298213,
140,776 raw records. The raw backup contains every record ID and CAS hash in the
superseded capture-reliability backup; that older database pair was retired only
after live acceptance and open-file checks. Its binaries/configuration remain.

The additive event-order index occupied 11.4 MiB in the isolated production-sized
projection. Regression tests exercise a complete recent prefix, older-only
matches requiring fallback, summary-only fallback, ordering, scope and citations.
Full race/vet checks passed. Isolated old/new result hashes matched: the broad
`sqlite` query improved from about 1.12–1.14 s to 0.23–0.30 s for three hits.

Live acceptance verified readiness, invalid-token denial, three revision-checked
citations, MCP owner/scoped diagnostics and actual Chromium. Startup indexing
lag reached zero with zero failures. The first cold `sqlite` request took 4.18 s.
Ten subsequent requests had median 239 ms and maximum 404 ms. Ten
`queue_max_bytes` requests had median 150 ms, but their first request took 1.84 s.
This verifies the sampled warm improvement, not a universal subsecond or cold
latency SLO. Capacity, query distribution and concurrent capture still require
continued observation. Aggregate evidence is retained in the private reports.

## Producer outbox activation (2026-10-20)

Source `572f0a1` is enabled on the M4. Collector, native plugin and MCP bridge
were built from its exact archived commit. Full race/vet checks passed. Release
artifacts were tested with the installed CPA executable, a synthetic provider and
an actual isolated collector. Before-commit failure and after-commit receipt loss
both survived forced producer and collector termination: replay retained exact
bytes, produced two final records, and cleared the outbox without duplicates.
Actual Chromium and MCP tests preserved the new outbox fields and scoped omission.

The private release root is
`/Volumes/data/build/cpa-session-archive/durable-572f0a1`. Its verified online
rollback pair has checkpoint/fence 298518 and 140,959 raw records. Projection
quick_check and hashes passed. Old artifacts, CPA executable, LaunchAgents,
configuration and manifest are retained. Only the three outbox configuration
settings changed; semantic comparison verified account/routing preservation.
The compatible collector started first, then CPA restarted with the new plugin.
No active archive or projection database was replaced.

The enabled directory is
`session-archive/private/delivery-outbox` under the existing installation. Its
limits are 2 GiB of logical payload and 10,000 entries, including quarantine.
Directory/file permissions and SQLite integrity passed. Initial live observation
reported eight persisted entries and seven verified commits in the latest sender
snapshot; direct local inspection showed zero remaining entries and zero
quarantine. The snapshot precedes the current receipt. No sampled queue drops,
permanent rejections, missing receipts, outbox read/write errors or collector
discards were observed. Index lag/failures were zero. These are sampled counters,
not a durable reconciliation ledger or proof about unpersisted callbacks.

Authenticated status, invalid-token denial, three source/citation roundtrips,
MCP owner/scoped behavior and actual live Chromium passed. Status took about
0.8–5.5 ms. The first broad search after restart took 4.33 s; cold latency remains
open despite the earlier warm improvement.

On the actual system volume, sixteen synthetic deliveries per size measured
persist medians of approximately 0.11 ms (4 KiB), 0.56 ms (256 KiB), and 9.46 ms
(4 MiB). All drained; these are isolated fsync samples, not a sustained ingest SLO.
System-volume fixture databases were removed after use. SQLite WAL/file overhead
is separate from the logical payload limit.

The outbox uses SQLite FULL; the archive still uses NORMAL. This activation
verifies process-crash replay after local persistence, not machine power-loss
survival or callbacks still waiting to persist. Preserve active outbox files on
rollback, and online-back up that database before future artifact replacements.
An older plugin cannot drain it. The superseded search rollback database pair
is retired only after record-ID/CAS inclusion proof and open-file checks;
aggregate reports and old binaries/configuration remain in their release roots.
