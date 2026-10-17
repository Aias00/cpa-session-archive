package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type SourceSnapshot struct {
	Record   Record
	Revision string
	Sequence int64
}
type SourceChange struct {
	Sequence  int64
	RequestID string
}
type SourcePage struct {
	IDs     []string
	AfterID int64
	Done    bool
}

// MemorySource reads metadata, retained payloads and revision in one database
// snapshot. A source revision never silently refers to a newer request body.
func (s *Store) MemorySource(ctx context.Context, id string) (SourceSnapshot, error) {
	var out SourceSnapshot
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out.Record, err = s.requestWithQuery(ctx, tx, id)
	if err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM archive_ingest_events WHERE request_id=?`, id).Scan(&out.Sequence)
	if err != nil {
		return out, err
	}
	record := out.Record
	// The alias is joined from a separate user-maintained table; changing its
	// display name does not change retained evidence or its index scope.
	record.PrincipalAlias = ""
	raw, err := json.Marshal(record)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(raw)
	out.Revision = hex.EncodeToString(digest[:])
	if err := tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}
func (s *Store) MemoryChanges(ctx context.Context, after int64, limit int) ([]SourceChange, error) {
	if limit < 1 || limit > 256 {
		limit = 64
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT sequence,request_id FROM archive_ingest_events WHERE sequence>? ORDER BY sequence LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceChange{}
	for rows.Next() {
		var change SourceChange
		if err := rows.Scan(&change.Sequence, &change.RequestID); err != nil {
			return nil, err
		}
		out = append(out, change)
	}
	return out, rows.Err()
}
func (s *Store) MemoryBackfill(ctx context.Context, afterID int64, limit int) (SourcePage, error) {
	if limit < 1 || limit > 256 {
		limit = 64
	}
	out := SourcePage{IDs: []string{}, AfterID: afterID}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,request_id FROM records WHERE id>? ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&out.AfterID, &id); err != nil {
			return out, err
		}
		out.IDs = append(out.IDs, id)
	}
	out.Done = len(out.IDs) < limit
	return out, rows.Err()
}
func (s *Store) MemoryWatermark(ctx context.Context) (int64, error) {
	var seq int64
	err := s.DB.QueryRowContext(ctx, `SELECT sequence FROM archive_ingest_clock WHERE id=1`).Scan(&seq)
	return seq, err
}
