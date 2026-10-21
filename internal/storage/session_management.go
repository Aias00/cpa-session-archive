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
