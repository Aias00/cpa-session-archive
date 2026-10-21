# Working agreements

- **Development-only memory work (user instruction, 2026-10-21):** Continue
  implementation and isolated verification, but do not configure client plugins,
  install updated artifacts, or restart live services until the user explicitly
  requests integration/rollout again. The local Codex `cpa_archive` entry was
  removed at the user's request. Preserve existing capture services and all raw,
  projection, outbox and reviewed Git data. This temporarily supersedes the
  automatic deployment workflow below for new memory changes.

- Work in the user's fork: `Aias00/cpa-session-archive`.
- The standing deployment target is the user's **Mac mini M4**, reachable as
  `ssh mac-mini` or `ssh mac-mini-m4` (user `aias`).
  Verify the hardware before deployment: this target reports Apple M4 / Mac16,10.
  The separate `mac-mini-m1` SSH alias is the legacy Apple M1 / Macmini9,1 host;
  do not substitute it for the M4 target. After requested code updates pass verification, deploy them
  there and verify the running service. The user authorized this workflow on
  2026-10-17; routine deployments do not need another permission handoff.
- Before replacing a deployed artifact, create a verified SQLite online backup
  and preserve the existing binary, launchd configuration and install manifest.
  Prepare and test artifacts before restarting services. Retain a rollback path.
  When the producer outbox exists, include a verified SQLite online backup of it
  and preserve the active outbox files through updates and rollback. Older plugins
  cannot replay pending entries; never delete them as deployment cleanup.
- On the M4, use the existing installation under
  `/Users/aias/Library/Application Support/CLIProxyAPI/session-archive` and the
  collector LaunchAgent `com.linonetwo.cpa-session-collector`.
- Keep CPA account/routing settings and raw conversation data intact. Restart
  CPA only when a plugin or CPA change requires it; collector-only updates do
  not require a CPA restart.
- Keep recall and ingestion listeners on loopback. Maintain workstation SSH
  access to the recall page at `http://127.0.0.1:18319/` when recall is enabled.
- Never commit or print read tokens, management keys or raw private transcripts.
  Record deployment provenance and verification results without their contents.
- New documentation and code comments use English. Explain results to the user
  in Chinese. Do not claim deployment until live checks have passed.

- The M4 Go SDK is `/Volumes/data/tools/cpa-go-sdk/bin/go` (Go 1.26.3).
  Set `GOROOT` to its parent SDK directory for native builds. See
  `docs/mac-mini-deployment.md` for access ports, verification and rollback.

- Use Codex for model/client acceptance tests. Do not invoke Claude for testing
  unless the user explicitly changes that instruction.
- Curated reusable knowledge uses the existing reviewed Git codex-kb; project
  facts use registered project Git contexts. Do not create a second memory
  authority in SQLite or auto-push private knowledge. Preserve unrelated dirty
  Git work; model tools may draft but never confirm/activate their own proposals.

- Backup retention: the user requested on 2026-10-20 that superseded deployment
  backups be deleted after use. Keep the newest verified complete rollback pair
  and required migration baselines. Before retiring an older database pair,
  confirm the newer raw backup contains its record IDs and CAS hashes, no file
  is open, and preserve binaries/configuration plus an aggregate removal record.
  This does not authorize deleting active data, source histories, or Git knowledge.
