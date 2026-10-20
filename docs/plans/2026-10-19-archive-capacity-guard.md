# Archive capacity guard

Scope: inventory and operational protection before further memory indexing work.
Keep active raw/projection databases intact. No automatic deletion, vacuum, model
installation or collector restart. The user explicitly authorized removal of
three old v2 index files after both retained backup pairs passed fresh checks.

- [x] Add a dependency-free, read-only capacity report for the installation.
- [x] Account for database/WAL, backup disk allocation, symlinks and read failures.
- [x] Protect current manifest/pointer references, migration baselines and the two
      newest complete backup pairs with recorded successful integrity checks.
- [x] Report low disk space and room for another conservative online backup pair.
- [x] Test fixtures for retention classification and filesystem boundaries.
- [x] Install the standalone tool on verified M4 and record the actual report.
- [x] Separately verify retained backup pairs before presenting a concrete cleanup
      proposal. Cleanup requires explicit user authorization for exact files.

The report consumes recorded verification metadata; it never claims a fresh
SQLite integrity check. Reported allocated bytes are not a guarantee of reclaimed
APFS space because clones, compression and snapshots can share physical blocks.


Deployment evidence: the standalone script was tested on workstation and native
M4 Python (14 tests), deployed from `d1ea91f`, and scheduled every six hours.
Both retained raw/memory backup pairs passed fresh `PRAGMA quick_check`.
Only `memory-v2.sqlite`, `memory-v2.sqlite-wal`, and `memory-v2.sqlite-shm` in
`before-mcp-dedup-20261018T072639Z` were deleted following explicit user approval.
Logical size and observed free-space recovery were approximately 12.01 GiB.
Service binaries, active databases and remaining backups were retained.
