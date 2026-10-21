package storage

import (
	"context"
	"errors"

	"cpa-session-archive/internal/archive"
)

// ErrSourceNotFound means the authoritative feed explicitly reports absence.
// Transport, authorization, and protocol failures must never use this sentinel.
var ErrSourceNotFound = errors.New("archive source not found")

type FeedIdentity struct {
	ArchiveID       string `json:"archive_id"`
	Watermark       int64  `json:"watermark"`
	HistoryComplete bool   `json:"history_complete"`
}

// IdentityFeed is optional; legacy local feeds keep their existing behavior.
type IdentityFeed interface {
	MemoryIdentity(context.Context) (FeedIdentity, error)
}

// RevisionFeed avoids transferring retained payloads during citation checks.
type RevisionFeed interface {
	MemoryRevision(context.Context, string) (string, error)
}

// MemoryFeed is optional; other backends can implement committed source changes
// without exposing their connection, SQL dialect or database file path.
type MemoryFeed interface {
	MemorySource(context.Context, string) (archive.SourceSnapshot, error)
	MemoryChanges(context.Context, int64, int) ([]archive.SourceChange, error)
	MemoryBackfill(context.Context, int64, int) (archive.SourcePage, error)
	MemoryWatermark(context.Context) (int64, error)
}

var _ MemoryFeed = (*SQLite)(nil)
