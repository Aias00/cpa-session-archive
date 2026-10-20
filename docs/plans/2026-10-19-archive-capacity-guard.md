# Archive capacity guard

Scope: inventory and operational protection before further memory indexing work.
Keep active raw/projection databases and all current backups intact during this
increment. No automatic deletion, vacuum, model installation or collector restart.

- [ ] Add a dependency-free, read-only capacity report for the installation.
- [ ] Account for database/WAL, backup disk allocation, symlinks and read failures.
- [ ] Protect current manifest/pointer references, migration baselines and the two
      newest complete backup pairs with recorded successful integrity checks.
- [ ] Report low disk space and room for another conservative online backup pair.
- [ ] Test fixtures for retention classification and filesystem boundaries.
- [ ] Install the standalone tool on verified M4 and record the actual report.
- [ ] Separately verify retained backup pairs before presenting a concrete cleanup
      proposal. Cleanup requires explicit user authorization for exact files.

The report consumes recorded verification metadata; it never claims a fresh
SQLite integrity check. Reported allocated bytes are not a guarantee of reclaimed
APFS space because clones, compression and snapshots can share physical blocks.
