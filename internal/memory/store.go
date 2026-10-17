package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

// Engine owns the built-in projection. Only Sync writes derived source content.
// Source validation also runs at read time, so projection lag cannot expose a
// deleted or reassigned source revision.
type Engine struct {
	db     *sql.DB
	feed   storage.MemoryFeed
	syncMu sync.Mutex
}

var _ Reader = (*Engine)(nil)

func Open(path string, feed storage.MemoryFeed) (*Engine, error) {
	if feed == nil || path == "" || path == ":memory:" {
		return nil, fmt.Errorf("memory requires a file path and source feed")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*Engine, error) { db.Close(); return nil, err }
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fail(err)
	}
	if version > NormalizerVersion {
		return fail(fmt.Errorf("unsupported memory schema version %d", version))
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS state(id INTEGER PRIMARY KEY CHECK(id=1),backfill_after_id INTEGER NOT NULL DEFAULT 0,backfill_done INTEGER NOT NULL DEFAULT 0,checkpoint INTEGER NOT NULL DEFAULT 0,failures INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '');
 INSERT OR IGNORE INTO state(id) VALUES(1);
 CREATE TABLE IF NOT EXISTS sources(request_id TEXT PRIMARY KEY,revision TEXT NOT NULL,sequence INTEGER NOT NULL,project_id TEXT NOT NULL,client TEXT NOT NULL,session_id TEXT NOT NULL,event_at TEXT NOT NULL,event_ns INTEGER NOT NULL,partial INTEGER NOT NULL,issues_text TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS chunks(id INTEGER PRIMARY KEY,request_id TEXT NOT NULL REFERENCES sources(request_id) ON DELETE CASCADE,side TEXT NOT NULL,pointer TEXT NOT NULL,role TEXT NOT NULL,start INTEGER NOT NULL,end INTEGER NOT NULL,text TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(request_id);
 CREATE INDEX IF NOT EXISTS idx_memory_scope_time ON sources(project_id,event_ns);
 CREATE INDEX IF NOT EXISTS idx_memory_session ON sources(session_id,event_ns);
 CREATE VIRTUAL TABLE IF NOT EXISTS chunk_search USING fts4(tokens,tokenize=unicode61);
 CREATE TRIGGER IF NOT EXISTS memory_chunk_delete AFTER DELETE ON chunks BEGIN DELETE FROM chunk_search WHERE docid=OLD.id; END;`)
	if err != nil {
		return fail(err)
	}
	if version < NormalizerVersion {
		tx, err := db.Begin()
		if err != nil {
			return fail(err)
		}
		// This database currently contains only rebuildable projections.
		// Reset the checkpoint with the corpus so new normalizer semantics
		// cannot silently reinterpret an older indexed citation.
		if _, err = tx.Exec(fmt.Sprintf(`DELETE FROM sources; DELETE FROM chunk_search; DELETE FROM state; INSERT INTO state(id) VALUES(1); PRAGMA user_version=%d;`, NormalizerVersion)); err != nil {
			tx.Rollback()
			return fail(err)
		}
		if err = tx.Commit(); err != nil {
			return fail(err)
		}
	}
	return &Engine{db: db, feed: feed}, nil
}
func (e *Engine) Close() error { return e.db.Close() }

func (e *Engine) Status(ctx context.Context) (Status, error) {
	var s Status
	err := e.db.QueryRowContext(ctx, `SELECT backfill_after_id,backfill_done,checkpoint,failures,last_error,(SELECT COUNT(*) FROM sources),(SELECT COUNT(*) FROM chunks),(SELECT COUNT(*) FROM sources WHERE partial=1) FROM state WHERE id=1`).Scan(&s.BackfillAfterID, &s.BackfillDone, &s.Checkpoint, &s.Failures, &s.LastError, &s.Sources, &s.Chunks, &s.PartialSources)
	if err != nil {
		return s, err
	}
	s.Watermark, err = e.feed.MemoryWatermark(ctx)
	s.Lag = max(0, s.Watermark-s.Checkpoint)
	return s, err
}

// Sync commits a bounded batch of work. Projection and checkpoint share one
// transaction; a crash before commit replays, and a crash after commit resumes.
func (e *Engine) Sync(ctx context.Context, limit int) (n int, err error) {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	defer func() {
		if err != nil && !errors.Is(err, context.Canceled) {
			_, _ = e.db.ExecContext(ctx, `UPDATE state SET failures=failures+1,last_error='source projection failed; retry pending' WHERE id=1`)
		}
	}()
	if limit < 1 || limit > 256 {
		limit = 64
	}
	s, err := e.Status(ctx)
	if err != nil {
		return 0, err
	}
	if !s.BackfillDone {
		page, err := e.feed.MemoryBackfill(ctx, s.BackfillAfterID, limit)
		if err != nil {
			return 0, err
		}
		for _, id := range page.IDs {
			if err = e.project(ctx, id, 0); err != nil {
				return n, err
			}
			n++
		}
		// Do not skip the page if any projection fails. Reprocessing earlier items
		// is safe, including after a process exits halfway through the page.
		_, err = e.db.ExecContext(ctx, `UPDATE state SET backfill_after_id=?,backfill_done=?,last_error='' WHERE id=1`, page.AfterID, page.Done)
		if err != nil {
			return n, err
		}
		if !page.Done || n > 0 {
			return n, nil
		}
	}
	changes, err := e.feed.MemoryChanges(ctx, s.Checkpoint, limit)
	if err != nil {
		return n, err
	}
	for _, change := range changes {
		if err = e.project(ctx, change.RequestID, change.Sequence); err != nil {
			return n, err
		}
		n++
	}
	if len(changes) == 0 && s.Watermark > s.Checkpoint {
		// A legacy archive can start its clock above zero without historical
		// events. Only advance across an empty gap to the watermark captured
		// BEFORE reading changes, never across a newly committed event.
		_, err = e.db.ExecContext(ctx, `UPDATE state SET checkpoint=?,last_error='' WHERE id=1`, s.Watermark)
	}
	return n, err
}

func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		n, err := e.Sync(ctx, 64)
		if ctx.Err() != nil {
			return
		}
		if err == nil && n > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *Engine) project(ctx context.Context, id string, checkpoint int64) error {
	source, err := e.feed.MemorySource(ctx, id)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return err
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !missing {
		var indexedRevision string
		readErr := tx.QueryRowContext(ctx, `SELECT revision FROM sources WHERE request_id=?`, id).Scan(&indexedRevision)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		if readErr == nil && indexedRevision == source.Revision {
			if _, err = tx.ExecContext(ctx, `UPDATE sources SET sequence=? WHERE request_id=?`, source.Sequence, id); err != nil {
				return err
			}
			if checkpoint > 0 {
				if _, err = tx.ExecContext(ctx, `UPDATE state SET checkpoint=?,last_error='' WHERE id=1`, checkpoint); err != nil {
					return err
				}
			}
			return tx.Commit()
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sources WHERE request_id=?`, id); err != nil {
		return err
	}
	if !missing {
		doc := Normalize(source.Record)
		if isInternal(source.Record) {
			doc = Document{Partial: true, Issues: []string{"internal memory request excluded"}}
		}
		project, client := sourceScope(source.Record)
		stamp := source.Record.StartedAt.UTC()
		if _, err = tx.ExecContext(ctx, `INSERT INTO sources(request_id,revision,sequence,project_id,client,session_id,event_at,event_ns,partial,issues_text) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, source.Revision, source.Sequence, project, client, source.Record.SessionID, stamp.Format(time.RFC3339Nano), stamp.UnixNano(), doc.Partial, strings.Join(doc.Issues, "\n")); err != nil {
			return err
		}
		for _, message := range doc.Messages {
			text := []rune(message.Text)
			for start := 0; start < len(text); {
				end := min(start+1600, len(text))
				part := string(text[start:end])
				result, err := tx.ExecContext(ctx, `INSERT INTO chunks(request_id,side,pointer,role,start,end,text) VALUES(?,?,?,?,?,?,?)`, id, message.Side, message.Pointer, message.Role, start, end, part)
				if err != nil {
					return err
				}
				rowID, err := result.LastInsertId()
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, `INSERT INTO chunk_search(docid,tokens) VALUES(?,?)`, rowID, indexTokens(part)); err != nil {
					return err
				}
				if end == len(text) {
					break
				}
				start = end - 256
			}
		}
	}
	if checkpoint > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE state SET checkpoint=?,last_error='' WHERE id=1`, checkpoint); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func sourceScope(r archive.Record) (project, client string) {
	first := func(keys ...string) string {
		for _, key := range keys {
			if v := r.Facets[key]; len(v) > 0 && strings.TrimSpace(v[0]) != "" {
				return strings.TrimSpace(v[0])
			}
		}
		return ""
	}
	project = first("project.id")
	if project == "" {
		if remote := first("git.remote"); remote != "" {
			project = "git:" + remote
		}
	}
	client = first("client", "client.name")
	return
}
func isInternal(r archive.Record) bool {
	for _, kind := range r.Facets["request.kind"] {
		if kind == "memory" || kind == "memory-extraction" {
			return true
		}
	}
	return false
}
