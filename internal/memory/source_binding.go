package memory

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"

	"cpa-session-archive/internal/storage"
)

var (
	ErrSourceBinding         = errors.New("archive source identity is unbound or incompatible; verify the source and rebuild the projection when required")
	ErrArchiveProjectionPath = errors.New("memory projection path contains an archive database")
)

type openOptions struct {
	sourceAdoption bool
}

type OpenOption func(*openOptions)

// WithSourceAdoption authorizes binding an existing unbound projection to the
// configured remote archive. Operators must verify that they belong together;
// the worker preserves existing checkpoints rather than silently rebuilding.
func WithSourceAdoption() OpenOption {
	return func(options *openOptions) { options.sourceAdoption = true }
}

func openSourceBinding(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS source_binding(id INTEGER PRIMARY KEY CHECK(id=1),archive_id TEXT NOT NULL,last_watermark INTEGER NOT NULL DEFAULT 0 CHECK(last_watermark>=0))`); err != nil {
		return err
	}
	var hasWatermark bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('source_binding') WHERE name='last_watermark')`).Scan(&hasWatermark); err != nil {
		return err
	}
	if !hasWatermark {
		_, err := db.Exec(`ALTER TABLE source_binding ADD COLUMN last_watermark INTEGER NOT NULL DEFAULT 0 CHECK(last_watermark>=0)`)
		return err
	}
	return nil
}

func rejectArchiveSchema(db *sql.DB) error {
	var archive bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name IN ('records','blobs','archive_ingest_clock'))`).Scan(&archive); err != nil {
		return errors.New("memory database schema is unreadable")
	}
	if archive {
		return ErrArchiveProjectionPath
	}
	return nil
}

// Inspect an existing file read-only before chmod, WAL setup or derived schema
// migration. An accidental archive path must preserve its contents and metadata.
func rejectArchivePath(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("memory database path is unavailable")
	}
	location := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", location.String()+"?mode=ro&_query_only=on")
	if err != nil {
		return errors.New("memory database schema is unreadable")
	}
	defer db.Close()
	return rejectArchiveSchema(db)
}

// Only Sync may establish a binding. Readers check both the stored binding and
// current feed identity, and never serve an unbound or rolled-back source.
func (e *Engine) sourceIdentity(ctx context.Context, allowBind bool) (*storage.FeedIdentity, error) {
	feed, ok := e.feed.(storage.IdentityFeed)
	if !ok {
		return nil, nil
	}
	var archiveID string
	var checkpoint, lastWatermark int64
	var populated bool
	if err := e.readDB.QueryRowContext(ctx, `SELECT COALESCE(b.archive_id,''),s.checkpoint,COALESCE(b.last_watermark,0),s.checkpoint<>0 OR s.backfill_after_id<>0 OR s.backfill_done<>0 OR EXISTS(SELECT 1 FROM sources) FROM state s LEFT JOIN source_binding b ON b.id=s.id WHERE s.id=1`).Scan(&archiveID, &checkpoint, &lastWatermark, &populated); err != nil {
		return nil, err
	}
	if archiveID == "" && (!allowBind || (populated && !e.allowSourceAdoption)) {
		return nil, ErrSourceBinding
	}
	identity, err := feed.MemoryIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if identity.ArchiveID == "" || len(identity.ArchiveID) > 256 || !identity.HistoryComplete || identity.Watermark < max(checkpoint, lastWatermark) || identity.Watermark < 0 || (archiveID != "" && archiveID != identity.ArchiveID) {
		return nil, ErrSourceBinding
	}
	if allowBind && (archiveID == "" || identity.Watermark > lastWatermark) {
		if _, err := e.db.ExecContext(ctx, `INSERT INTO source_binding(id,archive_id,last_watermark) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET last_watermark=excluded.last_watermark WHERE source_binding.archive_id=excluded.archive_id AND source_binding.last_watermark<excluded.last_watermark`, identity.ArchiveID, identity.Watermark); err != nil {
			return nil, err
		}
		if err := e.readDB.QueryRowContext(ctx, `SELECT archive_id FROM source_binding WHERE id=1`).Scan(&archiveID); err != nil {
			return nil, err
		}
		if archiveID != identity.ArchiveID {
			return nil, ErrSourceBinding
		}
	}
	return &identity, nil
}

func (e *Engine) sourceRevision(ctx context.Context, id string) (string, error) {
	if feed, ok := e.feed.(storage.RevisionFeed); ok {
		return feed.MemoryRevision(ctx, id)
	}
	source, err := e.feed.MemorySource(ctx, id)
	return source.Revision, err
}
