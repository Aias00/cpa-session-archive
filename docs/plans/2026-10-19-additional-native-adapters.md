# Additional native client adapters

Scope: Qoder, WorkBuddy, CodeBuddy, OpenCode and ZCode. Reuse retained native
archive envelopes, staging/checkpointing, CAS, import and MCP boundaries. No
external dependency or second curated-memory authority.

- [x] Add Qoder's Claude-compatible JSONL and Buddy SDK JSONL normalization.
- [x] Discover only client projects/history roots, preserve parent/child identity.
- [x] Read OpenCode legacy/v2 and ZCode SQLite through a read-only snapshot.
      Prefer v2 for migrated sessions, retain exact source row JSON as strings,
      capture message revisions as immutable source versions, never copy secrets
      tables or mutate source databases. Track rows only after archive commit.
- [x] Add fixtures for tool pairing, timestamps, scope ambiguity, replay,
      database updates, migration overlap and malformed source handling.
- [x] Run full/race/vet/build and native M4 checks. Make a verified online backup
      with sufficient capacity before collector replacement; keep rollback assets.
- [x] Run actual workstation sync, M4 import, source/citation coverage checks
      and Codex-only acceptance, then publish branch/docs.

Database rows may be updated in place. Changed message snapshots remain explicit
immutable historical versions; unchanged revisions do not create extra records.
Do not infer event identity from equal text across different source/client IDs.
Other discovered clients (VS Code/Copilot/Devin) are outside this increment.


Completed on 2026-10-20: `bda3826` deployed on verified M4. Actual initial
import committed 3,758 sources with no conflicts/missing/misscoped projections.
Search/source checks passed for all five additional clients and an old v2
citation remained valid. Real Codex status, five-client search and source-read
acceptance passed. The unchanged corpus replay added zero records. A follow-up
51-source delta completed through the standard scheduled-job path. See the
[deployment record](../mac-mini-deployment.md#additional-native-adapters-2026-10-20).
