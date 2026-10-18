package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ImportReport is safe to persist privately; it contains identifiers, not bodies.
type ImportReport struct {
	SourceRecords int      `json:"source_records"`
	Imported      int      `json:"imported"`
	Existing      int      `json:"existing"`
	Conflicts     int      `json:"conflicts"`
	ConflictIDs   []string `json:"conflict_request_ids,omitempty"`
	CopiedBlobs   int64    `json:"copied_blobs"`
	Remaining     int      `json:"remaining"`
}

// ImportSnapshot merges an immutable SQLite snapshot into a running archive.
// Existing target IDs win. Each bounded batch commits records, CAS dependencies,
// browser projections and ingest events atomically. Rerunning safely resumes.
func (s *Store) ImportSnapshot(ctx context.Context, path string, batchSize int) (report ImportReport, err error) {
	return s.importSnapshot(ctx, path, batchSize, nil)
}

// ImportSnapshotRange selects source IDs changed in (after, through]. Use this
// to export immutable native segments from an exclusively locked staging store;
// it selects current retained records, not historical versions of mutable IDs.
func (s *Store) ImportSnapshotRange(ctx context.Context, path string, batchSize int, after, through int64) (ImportReport, error) {
	if after < 0 || through < after {
		return ImportReport{}, fmt.Errorf("invalid source event range")
	}
	return s.importSnapshot(ctx, path, batchSize, &[2]int64{after, through})
}

func (s *Store) importSnapshot(ctx context.Context, path string, batchSize int, eventRange *[2]int64) (report ImportReport, err error) {
	if batchSize < 1 || batchSize > 64 {
		batchSize = 8
	}
	info, err := os.Stat(path)
	if err != nil {
		return report, err
	}
	if own, e := os.Stat(s.DBPath); e == nil && os.SameFile(info, own) {
		return report, fmt.Errorf("source and target are the same archive")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return report, err
	}
	uri := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}).String()
	sourceDB, err := sql.Open("sqlite3", uri)
	if err != nil {
		return report, err
	}
	defer sourceDB.Close()
	source := &Store{DB: sourceDB, DBPath: path, StoreUpstream: true}
	filter := "1=1"
	var filterArgs []any
	if eventRange != nil {
		watermark, err := source.MemoryWatermark(ctx)
		if err != nil {
			return report, err
		}
		if eventRange[1] > watermark {
			return report, fmt.Errorf("source event range exceeds committed watermark")
		}
		filter = `EXISTS(SELECT 1 FROM incoming.archive_ingest_events e WHERE e.request_id=s.request_id AND e.sequence>? AND e.sequence<=?)`
		filterArgs = []any{eventRange[0], eventRange[1]}
	}
	if eventRange == nil {
		var check string
		if err = sourceDB.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&check); err != nil {
			return report, err
		}
		if check != "ok" {
			return report, fmt.Errorf("source SQLite integrity check failed")
		}
	}
	connection, err := s.DB.Conn(ctx)
	if err != nil {
		return report, err
	}
	defer connection.Close()
	if _, err = connection.ExecContext(ctx, `ATTACH DATABASE ? AS incoming`, uri); err != nil {
		return report, err
	}
	defer func() { _, _ = connection.ExecContext(context.Background(), `DETACH DATABASE incoming`) }()
	// Only identifiers from SQLite's schema are interpolated; values use bindings.
	rows, err := connection.QueryContext(ctx, `PRAGMA main.table_info(records)`)
	if err != nil {
		return report, err
	}
	columns := []string{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return report, err
		}
		if name != "id" {
			columns = append(columns, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	// Preparing this statement also rejects incompatible source schemas before any write.
	columnSQL := strings.Join(columns, ",")
	insertSQL := `INSERT INTO records(` + columnSQL + `) SELECT ` + columnSQL + ` FROM incoming.records WHERE request_id=?`
	statement, err := connection.PrepareContext(ctx, insertSQL)
	if err != nil {
		return report, err
	}
	statement.Close()
	if err = connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM incoming.records s WHERE `+filter, filterArgs...).Scan(&report.SourceRecords); err != nil {
		return report, err
	}
	rows, err = connection.QueryContext(ctx, `SELECT s.request_id FROM incoming.records s WHERE `+filter+` AND NOT EXISTS(SELECT 1 FROM main.records d WHERE d.request_id=s.request_id) ORDER BY s.id`, filterArgs...)
	if err != nil {
		return report, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return report, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	report.Existing = report.SourceRecords - len(ids)
	different := []string{}
	for _, column := range columns {
		different = append(different, "s."+column+" IS NOT d."+column)
	}
	rows, err = connection.QueryContext(ctx, `SELECT s.request_id FROM incoming.records s JOIN main.records d USING(request_id) WHERE (`+strings.Join(different, " OR ")+`) AND `+filter, filterArgs...)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return report, err
		}
		report.ConflictIDs = append(report.ConflictIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	report.Conflicts = len(report.ConflictIDs)
	type prepared struct {
		record Record
		hashes []string
	}
	for offset := 0; offset < len(ids); offset += batchSize {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		batch := []prepared{}
		for _, id := range ids[offset:min(offset+batchSize, len(ids))] {
			record, e := source.Request(ctx, id)
			if e != nil {
				return report, fmt.Errorf("read source record: %w", e)
			}
			hashes, e := source.importPayloadHashes(ctx, id)
			if e != nil {
				return report, fmt.Errorf("validate source payload: %w", e)
			}
			batch = append(batch, prepared{record: record, hashes: hashes})
		}
		tx, e := connection.BeginTx(ctx, nil)
		if e != nil {
			return report, e
		}
		committed := false
		added := 0
		var copied int64
		e = func() error {
			defer func() {
				if !committed {
					_ = tx.Rollback()
				}
			}()
			// Reserve the WAL writer before checking IDs, preventing read-upgrade races
			// and making the no-overwrite decision atomic with live ingestion.
			if _, e := tx.ExecContext(ctx, `UPDATE archive_ingest_clock SET sequence=sequence WHERE id=1`); e != nil {
				return e
			}
			for _, item := range batch {
				var exists bool
				if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM records WHERE request_id=?)`, item.record.RequestID).Scan(&exists); e != nil {
					return e
				}
				if exists {
					continue
				}
				for _, hash := range item.hashes {
					result, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blobs(hash,media_type,raw_size,codec,data) SELECT hash,media_type,raw_size,codec,data FROM incoming.blobs WHERE hash=?`, hash)
					if e != nil {
						return e
					}
					n, e := result.RowsAffected()
					if e != nil {
						return e
					}
					copied += n
				}
				id := item.record.RequestID
				if _, e := tx.ExecContext(ctx, insertSQL, id); e != nil {
					return e
				}
				// Copy source facets exactly; they can include identity repairs absent from
				// an older materialized facets_json value.
				if _, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO record_facets(request_id,name,value) SELECT request_id,name,value FROM incoming.record_facets WHERE request_id=?`, id); e != nil {
					return e
				}
				if _, e := tx.ExecContext(ctx, `INSERT INTO turn_records(id,request_id,session_id,key_id,summary,response_preview,requested_model,model,outcome,status_code,started_at,completed_at,facets_json,principal_id,credential_hash,turn_id,tool_names_json,request_kinds_json)
     SELECT d.id,d.request_id,d.session_id,d.key_id,d.summary,d.response_preview,d.requested_model,d.model,d.outcome,d.status_code,d.started_at,d.completed_at,d.facets_json,d.principal_id,d.credential_hash,s.turn_id,s.tool_names_json,s.request_kinds_json FROM records d LEFT JOIN incoming.turn_records s ON s.request_id=d.request_id WHERE d.request_id=?`, id); e != nil {
					return e
				}
				if e := indexSessionRecord(tx, item.record); e != nil {
					return e
				}
				if _, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_facets(session_id,name,value) SELECT ?,name,value FROM incoming.record_facets WHERE request_id=?`, item.record.SessionID, id); e != nil {
					return e
				}
				// Preserve per-record completion markers without replacing target rules or mappings.
				for _, table := range []string{"normalized_response_requests"} {
					if _, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO `+table+`(request_id) SELECT request_id FROM incoming.`+table+` WHERE request_id=?`, id); e != nil {
						return e
					}
				}
				if _, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO previewed_requests(request_id,version) SELECT request_id,version FROM incoming.previewed_requests WHERE request_id=?`, id); e != nil {
					return e
				}
				added++
			}
			if e := tx.Commit(); e != nil {
				return e
			}
			committed = true
			return nil
		}()
		if e != nil {
			return report, e
		}
		report.Imported += added
		report.Existing += len(batch) - added
		report.CopiedBlobs += copied
	}
	err = connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM incoming.records s WHERE `+filter+` AND NOT EXISTS(SELECT 1 FROM main.records d WHERE d.request_id=s.request_id)`, filterArgs...).Scan(&report.Remaining)
	return report, err
}

// Validate and collect only reachable CAS dependencies, including manifest roots.
// Import keeps compressed bytes exactly as retained, instead of re-normalizing.
func (s *Store) importPayloadHashes(ctx context.Context, id string) ([]string, error) {
	var roots [3]string
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(original_ref,''),COALESCE(upstream_ref,''),COALESCE(response_ref,'') FROM records WHERE request_id=?`, id).Scan(&roots[0], &roots[1], &roots[2])
	if err != nil {
		return nil, err
	}
	hashes := []string{}
	seen := map[string]bool{}
	load := func(hash string) ([]byte, error) {
		raw, e := loadBlobWithQuery(ctx, s.DB, hash)
		if e != nil {
			return nil, e
		}
		digest := sha256.Sum256(raw)
		if hash != "sha256:"+hex.EncodeToString(digest[:]) {
			return nil, fmt.Errorf("CAS digest mismatch")
		}
		if !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash)
		}
		return raw, nil
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		manifest, e := load(root)
		if e != nil {
			return nil, e
		}
		if _, e = ExpandPayload(manifest, load); e != nil {
			return nil, e
		}
	}
	return hashes, nil
}
