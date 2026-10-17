package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"cpa-session-archive/internal/archive"
)

// SQLite adapts the existing archive store without changing its schema or exports.
type SQLite struct {
	*archive.Store
}

var _ Backend = (*SQLite)(nil)
var _ Snapshot = (*archive.StableSessionSnapshot)(nil)

func NewSQLite(store *archive.Store) *SQLite { return &SQLite{Store: store} }

func (s *SQLite) Ping(ctx context.Context) error { return s.DB.PingContext(ctx) }
func (s *SQLite) Close() error                   { return s.DB.Close() }

func (s *SQLite) SessionExists(ctx context.Context, sessionID string) (bool, error) {
	var exists int
	err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM records WHERE session_id=? LIMIT 1`, sessionID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *SQLite) BeginSnapshot(lowerBound time.Time, afterFence *int64) (Snapshot, error) {
	snapshot, err := s.BeginStableSessionSnapshot(lowerBound, afterFence)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// RetryableWrite retains the archive writer's SQLite busy/locked retry policy.
func (s *SQLite) RetryableWrite(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database is busy")
}
