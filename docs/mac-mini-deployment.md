# Mac mini M4 deployment

The user authorized the Mac mini M4 as the standing deployment target on
2026-10-17. After code verification, deploy and check the live service without
another routine permission handoff.

## Verify the target first

Use `ssh mac-mini-m4`, user `aias`. Before changing services, verify:

```sh
ssh mac-mini-m4 'sysctl -n machdep.cpu.brand_string; sysctl -n hw.model'
```

The target reports **Apple M4 / Mac16,10**. The separate `mac-mini` alias reports
**Apple M1 / Macmini9,1** and is a legacy host. Do not substitute it for the M4.
The workstation SSH configuration holds the actual address and deployment-key
selection. Its existing public key is authorized on the M4.

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
- Native Go SDK: `/Users/aias/Work/tools/cpa-go-sdk/bin/go`, version 1.26.3.
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
   `/Users/aias/Work/build/cpa-session-archive/<commit>` on the M4.
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
MCP, semantic retrieval and automatic memory extraction are not deployed.

## Legacy archive merge

The legacy M1 archive was subsequently merged into M4 with the standalone
`cpa-session-import` tool (commit `99c51be`). It added 1,413 missing requests,
kept existing M4 rows intact, and completed memory-index catch-up. See the
[migration record](archive-migration.md) for snapshot cutoff, backups and checks.
The collector did not need a restart or executable update for this operation.
