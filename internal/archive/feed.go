package archive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// EnsureFeedIdentity assigns a stable archive identity in the raw database.
// Online backups retain this row; a different archive receives another ID.
func (s *Store) EnsureFeedIdentity(ctx context.Context) (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	// Reserve the writer before reading schema or data. A deferred transaction
	// can otherwise fail its read-to-write upgrade under concurrent initialization.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return "", err
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS archive_feed_identity(
 id INTEGER PRIMARY KEY CHECK(id=1), archive_id TEXT NOT NULL CHECK(length(archive_id)>0))`); err != nil {
		return "", err
	}
	if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO archive_feed_identity(id,archive_id) VALUES(1,?)`, hex.EncodeToString(random[:])); err != nil {
		return "", err
	}
	var id string
	if err := conn.QueryRowContext(ctx, `SELECT archive_id FROM archive_feed_identity WHERE id=1`).Scan(&id); err != nil {
		return "", err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return "", err
	}
	return id, nil
}
