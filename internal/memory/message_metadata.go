package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This additive projection is versioned independently from shared text schema
// v3. Older collectors may ignore it; neither FTS nor normalizer-v2 citations
// need rebuilding when message classification is introduced.
func openMessageMetadata(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS message_meta(request_id TEXT NOT NULL REFERENCES sources(request_id) ON DELETE CASCADE,side TEXT NOT NULL,pointer TEXT NOT NULL,kind TEXT NOT NULL,tool_name TEXT NOT NULL,tool_call_id TEXT NOT NULL,model TEXT NOT NULL,message_at TEXT NOT NULL,PRIMARY KEY(request_id,side,pointer));
 CREATE TABLE IF NOT EXISTS message_meta_sources(request_id TEXT PRIMARY KEY REFERENCES sources(request_id) ON DELETE CASCADE,revision TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS message_meta_state(id INTEGER PRIMARY KEY CHECK(id=1),version INTEGER NOT NULL,after_id INTEGER NOT NULL DEFAULT 0,done INTEGER NOT NULL DEFAULT 0);
 INSERT OR IGNORE INTO message_meta_state(id,version) VALUES(1,1);`)
	if err != nil {
		return err
	}
	var version int
	if err = db.QueryRow("select version from message_meta_state where id=1").Scan(&version); err != nil {
		return err
	}
	if version != 1 {
		return fmt.Errorf("unsupported message metadata version %d", version)
	}
	_, err = db.Exec(`UPDATE message_meta_state SET after_id=0,done=0 WHERE id=1 AND done=1 AND EXISTS(SELECT 1 FROM sources s LEFT JOIN message_meta_sources m ON m.request_id=s.request_id WHERE m.request_id IS NULL OR m.revision<>s.revision)`)
	return err
}
func messageKind(m Message) string {
	if m.Kind != "" {
		return m.Kind
	}
	switch m.Role {
	case "system", "developer":
		return "context"
	case "tool":
		return "tool_result"
	case "summary":
		return "summary"
	}
	return "text"
}
func putMessageMetadata(ctx context.Context, tx *sql.Tx, id, revision string, messages []Message) error {
	if _, err := tx.ExecContext(ctx, "delete from message_meta where request_id=?", id); err != nil {
		return err
	}
	statement, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO message_meta(request_id,side,pointer,kind,tool_name,tool_call_id,model,message_at) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, m := range messages {
		if _, err := statement.ExecContext(ctx, id, m.Side, m.Pointer, messageKind(m), m.ToolName, m.ToolCallID, m.Model, m.MessageAt); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "insert into message_meta_sources(request_id,revision) values(?,?) on conflict(request_id) do update set revision=excluded.revision", id, revision)
	return err
}
func (e *Engine) syncMessageMetadata(ctx context.Context, limit int) (int, error) {
	var after int64
	var done bool
	if err := e.db.QueryRowContext(ctx, "select after_id,done from message_meta_state where id=1").Scan(&after, &done); err != nil {
		return 0, err
	}
	if done {
		return 0, nil
	}
	rows, err := e.db.QueryContext(ctx, `SELECT s.rowid,s.request_id,s.revision,COALESCE(m.revision,'') FROM sources s LEFT JOIN message_meta_sources m ON m.request_id=s.request_id WHERE s.rowid>? ORDER BY s.rowid LIMIT ?`, after, limit)
	if err != nil {
		return 0, err
	}
	type item struct {
		row                int64
		id, revision, meta string
	}
	var page []item
	for rows.Next() {
		var v item
		if err := rows.Scan(&v.row, &v.id, &v.revision, &v.meta); err != nil {
			rows.Close()
			return 0, err
		}
		page = append(page, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, v := range page {
		if v.meta == v.revision {
			if _, err := e.db.ExecContext(ctx, "update message_meta_state set after_id=? where id=1", v.row); err != nil {
				return 0, err
			}
			continue
		}
		var doc Document
		current := v.revision
		if v.meta != v.revision {
			source, err := e.feed.MemorySource(ctx, v.id)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}
			if errors.Is(err, sql.ErrNoRows) {
				current = ""
			} else {
				current = source.Revision
				if !isInternal(source.Record) {
					doc = Normalize(source.Record)
				}
			}
		}
		tx, err := e.db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		if v.meta != v.revision && current == v.revision {
			err = putMessageMetadata(ctx, tx, v.id, v.revision, doc.Messages)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, "update message_meta_state set after_id=? where id=1", v.row)
		}
		if err != nil {
			tx.Rollback()
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
	}
	if len(page) < limit {
		_, err = e.db.ExecContext(ctx, "update message_meta_state set done=1 where id=1")
	}
	return len(page), err
}
