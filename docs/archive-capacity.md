# Archive capacity and backup protection

Run the dependency-free inventory on the host containing the installation:

```sh
python3 scripts/archive_capacity.py --root '/absolute/session-archive'
```

The tool reads file metadata and small configuration/verification documents. It
never opens conversation payloads, modifies databases, deletes backups, checkpoints
WAL or runs VACUUM. Reports include paths/names and operational counts; retain them
privately. No database dependency or collector restart is required.

Exit codes: 0 means capacity is sufficient, 1 means warning, 2 means critically
low free space, and 3 means inventory failed. The default minimum reserve is
10 GiB (`--reserve-gib`). A conservative new backup pair needs the current raw
and memory database sizes plus their WAL files and that reserve. This is a
preflight estimate, not a reservation: concurrent growth and output allocation
can still exhaust disk. `can_create_backup_pair=false` must stop a routine full
backup deployment until space is available. Existing backup commands do not yet
invoke this preflight automatically; run it before those commands.

Each backup directory shows logical and allocated bytes, whether a complete pair
has a recorded integrity result, and reasons to protect it:

- The two newest complete pairs with recorded successful verification.
- Every directory referenced by the current manifest or rollback pointer files.
- M1-to-M4 migration baselines.
- Unverified pairs, partial inventories, linked entries or unreadable references.

Recorded integrity is not a fresh database check. A nonempty WAL disqualifies a
pair from this recorded-complete classification. Unknown verification formats fail
closed. Missing/invalid manifest or active database files prevents an affirmative
backup preflight. The report does not follow directory symlinks. Hardlinked files
are counted once per inventory bucket; APFS clones/snapshots and compression mean
allocated bytes are not guaranteed reclaimed bytes.

`cleanup_review_candidates` only identifies unprotected complete backup pairs for
operator review. It never authorizes deletion or proves that a backup has no
unique history. Prefer copying historical baselines to independently verified
external storage; keep rollback binaries/configuration and at least two verified
complete pairs until a reviewed retention policy changes that requirement.

## M4 scheduled checks

The standalone tool is installed as `bin/archive-capacity.py` under the existing
installation. LaunchAgent `com.router-for-me.cpa-archive-capacity` runs at login
and every six hours, without KeepAlive/retry loops. Standard output is a private
append-only `logs/capacity.log`; errors go to `logs/capacity-error.log`. The process
exit code provides a local operational warning; this increment sends no external
notification and does not expose a new HTTP endpoint. Run the tool manually for
an immediate report. The latest release report is also saved privately under
`reports/capacity-*.json`.

The guard is an operational tool. Reviewed Git Markdown/YAML remains the authority
for reusable knowledge; capacity reports are disposable operational metadata.
