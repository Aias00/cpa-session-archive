package storage

import (
	"context"
	"cpa-session-archive/internal/archive"
)

// MemoryFeed is optional; other backends can implement committed source changes
// without exposing their connection, SQL dialect or database file path.
type MemoryFeed interface {
	MemorySource(context.Context, string) (archive.SourceSnapshot, error)
	MemoryChanges(context.Context, int64, int) ([]archive.SourceChange, error)
	MemoryBackfill(context.Context, int64, int) (archive.SourcePage, error)
	MemoryWatermark(context.Context) (int64, error)
}

var _ MemoryFeed = (*SQLite)(nil)
