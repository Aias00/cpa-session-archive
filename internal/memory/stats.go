package memory

import "database/sql"

// openCorpusStats installs transactional aggregates independently of the text
// projection version. Existing corpora are scanned once, not on every search.
// Triggers also maintain counts when an older collector opens this projection.
func openCorpusStats(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var triggers int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND tbl_name IN ('sources','chunks','chunk_texts') AND name IN ('corpus_sources_insert','corpus_sources_delete','corpus_sources_update','corpus_chunks_insert','corpus_chunks_delete','corpus_text_insert','corpus_text_delete')`).Scan(&triggers); err != nil {
		return err
	}
	if triggers != 7 {
		// A legacy projection rebuild can remove owner-table triggers while
		// retaining this extension table. Reinitialize instead of serving stale counts.
		if _, err := tx.Exec(`DROP TABLE IF EXISTS corpus_stats`); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS corpus_stats(
 id INTEGER PRIMARY KEY CHECK(id=1),
 sources INTEGER NOT NULL, chunks INTEGER NOT NULL,
 partial_sources INTEGER NOT NULL, unique_chunks INTEGER NOT NULL);
 INSERT INTO corpus_stats SELECT 1,
 (SELECT COUNT(*) FROM sources), (SELECT COUNT(*) FROM chunks),
 (SELECT COUNT(*) FROM sources WHERE partial=1),
 (SELECT COUNT(*) FROM chunk_texts)
 WHERE NOT EXISTS(SELECT 1 FROM corpus_stats WHERE id=1);
 CREATE TRIGGER IF NOT EXISTS corpus_sources_insert AFTER INSERT ON sources BEGIN
 UPDATE corpus_stats SET sources=sources+1,partial_sources=partial_sources+NEW.partial WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS corpus_sources_delete AFTER DELETE ON sources BEGIN
 UPDATE corpus_stats SET sources=sources-1,partial_sources=partial_sources-OLD.partial WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS corpus_sources_update AFTER UPDATE OF partial ON sources BEGIN
 UPDATE corpus_stats SET partial_sources=partial_sources+NEW.partial-OLD.partial WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS corpus_chunks_insert AFTER INSERT ON chunks BEGIN
 UPDATE corpus_stats SET chunks=chunks+1 WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS corpus_chunks_delete AFTER DELETE ON chunks BEGIN
 UPDATE corpus_stats SET chunks=chunks-1 WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS corpus_text_insert AFTER INSERT ON chunk_texts BEGIN
 UPDATE corpus_stats SET unique_chunks=unique_chunks+1 WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS corpus_text_delete AFTER DELETE ON chunk_texts BEGIN
 UPDATE corpus_stats SET unique_chunks=unique_chunks-1 WHERE id=1; END;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}
