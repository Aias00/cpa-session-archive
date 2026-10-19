# Magpie-informed native history improvements

Approved scope: improve the existing Codex, Claude Code and Kiro adapters before adding clients not used in this installation.

- [x] Respect absolute CODEX_HOME / CLAUDE_CONFIG_DIR and explicit CLI roots. Follow explicitly configured root links, never arbitrary nested links.
- [x] Recognize Codex segmented rollouts and zstd history. Use the installed zstd executable, validate the complete frame before committing, retain decoded JSONL bytes, preserve existing IDs across compression and moves. Divergent histories remain explicit reconciliation failures.
- [x] Cache unchanged-file proofs using device/inode/size/mtime/ctime, settled change stamps, a 24-hour full verification interval and an explicit verify-all override. Unsupported filesystems always reverify. Checkpoint and digest remain authoritative.
- [x] Enrich normalized message kinds, tool identity, recorded model/time and native parent metadata without changing normalizer-v2 text, pointers or ranges.
- [x] Add a compatible, separately versioned message-metadata projection. Backfill it in bounded idle batches without rebuilding shared text or FTS. Rank dialogue above tools and exclude context from search, while direct citations still work.
- [ ] Test synthetic format/race/replay/migration cases, full/race/vet/build, then build natively and deploy to verified M4 with online backups. Run the real sync and Codex-only acceptance; preserve existing Git knowledge and credentials.

Magpie is a read-only reference (MIT). Do not transplant its UI truncation limits into archival ingestion or import the whole module. Additional Agent formats remain a later iteration; the source/store/recall boundaries stay unchanged.
