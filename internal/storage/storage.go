// Package storage defines the persistence boundary consumed by the collector.
package storage

import (
	"context"
	"io"
	"time"

	"cpa-session-archive/internal/archive"
)

// Writer accepts archive batches and classifies backend-specific write failures.
// Successful writes commit the batch; HTTP queue acceptance happens separately.
type Writer interface {
	PutBatch([]archive.Record) error
	RetryableWrite(error) bool
}

// Repository serves archive queries, identity mappings, derived text and exports.
type Repository interface {
	SessionExists(context.Context, string) (bool, error)
	SessionsFiltered(context.Context, int, map[string]string) ([]archive.SessionSummary, error)
	SessionMetadataRange(context.Context, string, int, int, string, map[string]string) (archive.SessionPage, error)
	SessionRange(context.Context, string, int, int, int) (archive.SessionPage, error)
	Session(context.Context, string) ([]archive.Record, error)
	Request(context.Context, string) (archive.Record, error)
	RequestContext(context.Context, string, int, int) ([]archive.Record, error)
	RequestTimeline(context.Context, string) (archive.RequestTimelineView, error)
	SessionTurnDetail(context.Context, string, string, int, int) (archive.TurnDetailPage, error)
	SessionTurnPage(context.Context, string, int, int, string) (archive.TurnPage, error)
	CachedTurnText(context.Context, string, string) (string, bool, error)
	BuildTurnText(context.Context, string, string) (string, error)
	SaveTurnText(context.Context, string, string, string) error
	CredentialPrincipals(context.Context) ([]archive.CredentialPrincipal, error)
	ApplyCredentialPrincipals(context.Context, []archive.CredentialPrincipal) error
	Facets(context.Context) ([]archive.FacetCount, error)
	Stats(context.Context) (archive.Stats, error)
	ExportSessionJSONL(context.Context, string, io.Writer) error
	ExportArchiveJSONL(context.Context, string, io.Writer) error
	ExportTrainingJSONL(context.Context, string, io.Writer) error
}

// Snapshot preserves the session set and export contents for one read lifetime.
type Snapshot interface {
	IngestFence() int64
	TombstoneSafeAfterIngestFence() int64
	SessionCount() int
	RequestCount() int
	DeletedSessionCount() int
	SessionSetSHA256() string
	Page(string, string, int) ([]archive.StableSessionSummary, *archive.StableSessionSummary, error)
	Summary(string) (archive.StableSessionSummary, bool)
	ExportSessionJSONL(context.Context, string, string, io.Writer) error
	Close() error
}

type SnapshotStore interface {
	BeginSnapshot(time.Time, *int64) (Snapshot, error)
	StableSnapshotContract(context.Context) (int, int64, error)
	PendingSessionExportDigests() (int, int)
	RefreshSessionExportDigests(context.Context, int) (int, error)
	RefreshQueuedSessionExportDigests(context.Context, int) (int, error)
}

// Backend is the complete collector contract. Database setup and migrations are
// owned by the concrete implementation at application startup.
type Backend interface {
	Writer
	Repository
	SnapshotStore
	Ping(context.Context) error
	GC() (int64, error)
	Close() error
}
