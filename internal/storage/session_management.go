package storage

import (
	"context"

	"cpa-session-archive/internal/archive"
)

// SessionManager is an optional management overlay, separate from archive evidence.
type SessionManager interface {
	BrowseSessions(context.Context, archive.SessionBrowseOptions) (archive.ManagedSessionPage, error)
	SessionManagement(context.Context, string) (archive.SessionManagement, error)
	UpdateSessionManagement(context.Context, string, int64, archive.SessionManagementUpdate) (archive.SessionManagement, error)
}

var _ SessionManager = (*SQLite)(nil)

// SessionBulkManager optionally supports persistent frozen-selection jobs.
type SessionBulkManager interface {
	PlanSessionBulk(context.Context, archive.SessionBulkPlanRequest) (archive.SessionBulkJob, error)
	SessionBulk(context.Context, string) (archive.SessionBulkJob, error)
	ListSessionBulk(context.Context) ([]archive.SessionBulkJob, error)
	ApplySessionBulk(context.Context, string, int) (archive.SessionBulkJob, error)
	UndoSessionBulk(context.Context, string, int) (archive.SessionBulkJob, error)
}

var _ SessionBulkManager = (*SQLite)(nil)
