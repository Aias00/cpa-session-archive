package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
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
const ProjectionVersion = 3

type Engine struct {
	db     *sql.DB
	readDB *sql.DB
	feed   storage.MemoryFeed
	syncMu sync.Mutex
}

var _ Reader = (*Engine)(nil)
var _ Projection = (*Engine)(nil)

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
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	location := url.URL{Scheme: "file", Path: abs}
	db, err := sql.Open("sqlite3", location.String()+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*Engine, error) { db.Close(); return nil, err }
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fail(err)
	}
	if version > ProjectionVersion {
		return fail(fmt.Errorf("unsupported memory schema version %d", version))
	}
	schema := `CREATE TABLE IF NOT EXISTS state(id INTEGER PRIMARY KEY CHECK(id=1),backfill_after_id INTEGER NOT NULL DEFAULT 0,backfill_done INTEGER NOT NULL DEFAULT 0,checkpoint INTEGER NOT NULL DEFAULT 0,failures INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '');
 INSERT OR IGNORE INTO state(id) VALUES(1);
 CREATE TABLE IF NOT EXISTS sources(request_id TEXT PRIMARY KEY,revision TEXT NOT NULL,sequence INTEGER NOT NULL,project_id TEXT NOT NULL,client TEXT NOT NULL,session_id TEXT NOT NULL,event_at TEXT NOT NULL,event_ns INTEGER NOT NULL,partial INTEGER NOT NULL,issues_text TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS chunk_texts(id INTEGER PRIMARY KEY,content_hash TEXT NOT NULL UNIQUE,text TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS chunks(id INTEGER PRIMARY KEY,request_id TEXT NOT NULL REFERENCES sources(request_id) ON DELETE CASCADE,side TEXT NOT NULL,pointer TEXT NOT NULL,role TEXT NOT NULL,start INTEGER NOT NULL,end INTEGER NOT NULL,text_id INTEGER NOT NULL REFERENCES chunk_texts(id));
 CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(request_id);
 CREATE INDEX IF NOT EXISTS idx_chunks_text ON chunks(text_id);
 CREATE INDEX IF NOT EXISTS idx_memory_scope_time ON sources(project_id,event_ns);
 CREATE INDEX IF NOT EXISTS idx_memory_session ON sources(session_id,event_ns);
 CREATE INDEX IF NOT EXISTS idx_memory_event ON sources(event_ns DESC,request_id);
 CREATE VIRTUAL TABLE IF NOT EXISTS chunk_search USING fts4(tokens,tokenize=unicode61);
 CREATE TRIGGER IF NOT EXISTS memory_chunk_delete AFTER DELETE ON chunks BEGIN DELETE FROM chunk_texts WHERE id=OLD.text_id AND NOT EXISTS(SELECT 1 FROM chunks WHERE text_id=OLD.text_id); END;
 CREATE TRIGGER IF NOT EXISTS memory_text_delete AFTER DELETE ON chunk_texts BEGIN DELETE FROM chunk_search WHERE docid=OLD.id; END;`
	if version < ProjectionVersion {
		tx, err := db.Begin()
		if err != nil {
			return fail(err)
		}
		// Only derived data lives here. Drop the old layout without running
		// millions of per-row delete triggers, then restart its checkpoints.
		if _, err = tx.Exec(`DROP TRIGGER IF EXISTS memory_chunk_delete; DROP TRIGGER IF EXISTS memory_text_delete; DROP TABLE IF EXISTS chunks; DROP TABLE IF EXISTS sources; DROP TABLE IF EXISTS chunk_texts; DROP TABLE IF EXISTS chunk_search; DROP TABLE IF EXISTS state; DROP TABLE IF EXISTS corpus_stats; DROP TABLE IF EXISTS coverage_groups;`); err != nil {
			tx.Rollback()
			return fail(err)
		}
		if _, err = tx.Exec(schema); err != nil {
			tx.Rollback()
			return fail(err)
		}
		if _, err = tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, ProjectionVersion)); err != nil {
			tx.Rollback()
			return fail(err)
		}
		if err = tx.Commit(); err != nil {
			return fail(err)
		}
	} else if _, err = db.Exec(schema); err != nil {
		return fail(err)
	}
	if err := openMessageMetadata(db); err != nil {
		return fail(err)
	}
	if err := openCorpusStats(db); err != nil {
		return fail(err)
	}
	if err := openCoverageStats(db); err != nil {
		return fail(err)
	}
	// WAL readers use a bounded, read-only pool so a projection transaction does
	// not monopolize the connection required by interactive searches.
	readDB, err := sql.Open("sqlite3", location.String()+"?mode=ro&_query_only=on&_busy_timeout=5000")
	if err != nil {
		return fail(err)
	}
	readDB.SetMaxOpenConns(4)
	readDB.SetMaxIdleConns(4)
	if err := readDB.Ping(); err != nil {
		readDB.Close()
		return fail(err)
	}
	return &Engine{db: db, readDB: readDB, feed: feed}, nil
}
func (e *Engine) Close() error { return errors.Join(e.readDB.Close(), e.db.Close()) }

func (e *Engine) Status(ctx context.Context) (Status, error) {
	var s Status
	tx, err := e.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return s, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT backfill_after_id,backfill_done,checkpoint,failures,last_error,cs.sources,cs.chunks,cs.partial_sources,cs.unique_chunks FROM state JOIN corpus_stats cs ON cs.id=state.id WHERE state.id=1`).Scan(&s.BackfillAfterID, &s.BackfillDone, &s.Checkpoint, &s.Failures, &s.LastError, &s.Sources, &s.Chunks, &s.PartialSources, &s.UniqueChunks)
	if err != nil {
		return s, err
	}
	if err := tx.QueryRowContext(ctx, "select done from message_meta_state where id=1").Scan(&s.MessageMetadataDone); err != nil {
		return s, err
	}
	s.DeduplicatedChunks = s.Chunks - s.UniqueChunks
	s.Coverage, err = coverageStatus(ctx, tx, s.PartialSources)
	if err != nil {
		return s, err
	}
	if err := tx.Commit(); err != nil {
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
	var s Status
	err = e.db.QueryRowContext(ctx, `SELECT backfill_after_id,backfill_done,checkpoint FROM state WHERE id=1`).Scan(&s.BackfillAfterID, &s.BackfillDone, &s.Checkpoint)
	if err == nil {
		s.Watermark, err = e.feed.MemoryWatermark(ctx)
	}
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
	if n == 0 && err == nil {
		return e.syncMessageMetadata(ctx, limit)
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
		doc := projectionDocument(source.Record)
		project, client := sourceScope(source.Record)
		stamp := source.Record.StartedAt.UTC()
		if _, err = tx.ExecContext(ctx, `INSERT INTO sources(request_id,revision,sequence,project_id,client,session_id,event_at,event_ns,partial,issues_text) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, source.Revision, source.Sequence, project, client, source.Record.SessionID, stamp.Format(time.RFC3339Nano), stamp.UnixNano(), doc.Partial, strings.Join(doc.Issues, "\n")); err != nil {
			return err
		}
		if err = putMessageMetadata(ctx, tx, id, source.Revision, doc.Messages); err != nil {
			return err
		}
		for _, message := range doc.Messages {
			text := []rune(message.Text)
			for start := 0; start < len(text); {
				end := min(start+1600, len(text))
				part := string(text[start:end])
				textID, err := putChunkText(ctx, tx, part)
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, `INSERT INTO chunks(request_id,side,pointer,role,start,end,text_id) VALUES(?,?,?,?,?,?,?)`, id, message.Side, message.Pointer, message.Role, start, end, textID); err != nil {
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

func projectionDocument(r archive.Record) Document {
	if isInternal(r) {
		return Document{Partial: true, Issues: []string{"internal memory request excluded"}}
	}
	return Normalize(r)
}

// Physical sharing does not merge occurrence identity or scope.
func putChunkText(ctx context.Context, tx *sql.Tx, text string) (int64, error) {
	digest := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(digest[:])
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO chunk_texts(content_hash,text) VALUES(?,?)`, hash, text)
	if err != nil {
		return 0, err
	}
	added, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	var id int64
	if added > 0 {
		id, err = result.LastInsertId()
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO chunk_search(docid,tokens) VALUES(?,?)`, id, indexTokens(text))
	} else {
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT id,text FROM chunk_texts WHERE content_hash=?`, hash).Scan(&id, &existing)
		if err == nil && existing != text {
			return 0, fmt.Errorf("chunk digest collision")
		}
	}
	return id, err
}
