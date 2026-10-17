# Mac mini M4 deployment

The user authorized this machine as the standing deployment target on
2026-10-17. After code verification, build natively, preserve rollback artifacts,
deploy and check the live service without another routine permission handoff.

## Installation and access

- SSH host: `mac-mini` (Darwin arm64, user GUI domain 501).
- Go: `/opt/homebrew/bin/go`; non-interactive SSH does not put it on PATH.
- Installation: `/Users/aias/Library/Application Support/CLIProxyAPI/session-archive`.
- Collector label: `com.linonetwo.cpa-session-collector`.
- Collector: `bin/cpa-session-collector`, listening on `127.0.0.1:8318`.
- Raw archive: `data/archive.sqlite`; recall projection: `data/memory.sqlite`.
- Recall: `127.0.0.1:8319`, enabled with `MEMORY_ENABLED=true`.
- Read token: `private/memory-read.token`; the launchd environment contains only
  its path, never its value.
- Workstation page: <http://127.0.0.1:18319/>.
- Workstation token file:
  `/Users/aias/Library/Application Support/CLIProxyAPI Access/memory-read-token.txt`.
- Workstation tunnel label: `com.router-for-me.cliproxyapi-recall-access`.

Recall and management use separate tokens and tunnels. The original archive
page remains available through port 18317. CPA stays at its existing installed
version; collector-only updates do not restart CPA or change its routing.

## Release procedure

1. Run targeted/full Go tests, race checks, vet and native builds on the
   workstation. Commit the source in the user's fork.
2. Transfer a `git archive` of the exact source commit into
   `/Users/aias/Work/build/cpa-session-archive/<commit>` on the Mac mini.
3. Run Go tests and vet, then build the collector on the target. Prefer the
   existing verified Go cache (`GOPROXY=off`, `GOSUMDB=off`) when dependencies
   have not changed.
4. Use the installed SQLite online backup command to create a verified archive
   snapshot. Preserve the current binary, LaunchAgent, manifest and install notes
   under a private timestamped `backups/before-...` directory.
5. Preflight against a separate copy of that snapshot, with isolated loopback
   ports. Complete historical indexing and inspect counts/coverage without
   printing private source text. Never run preflight against the backup itself.
6. Stop preflight, checkpoint its projection WAL, and stage the verified derived
   projection. Do not replace the original archive with the historical snapshot.
7. Stop only the collector, atomically replace its executable/configuration and
   install the projection if needed. Restart the LaunchAgent. The plugin/CPA
   remain running; keep this interruption short because their queues are finite.
8. Verify collector readiness, unauthenticated denial, authenticated recall,
   source citations, live index catch-up, archive growth and browser access
   through the workstation tunnel. Record source/binary hashes and live results
   in the private install manifest. Do not print tokens or transcripts.

## Rollback

`builtin-recall-rollback-dir.txt` in the installation identifies the preserved
release. Stop the collector, restore the previous binary and its LaunchAgent,
then bootstrap that agent again. Keep the current raw archive intact; never
restore an older archive merely to roll back executable code. Derived recall
files can remain unused by an older collector. Keep failed-release artifacts
for diagnosis and compare their hashes with the manifest.

## Current rollout

2026-10-17: deployment in progress. Native Go tests and vet passed on the Mac
mini, and a verified online archive backup was taken. Production still runs the
previous collector while the Codex custom-tool normalization update is prepared.
