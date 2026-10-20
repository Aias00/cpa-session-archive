package historysync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"cpa-session-archive/internal/nativehistory"
)

type databaseSession struct {
	id, parent, cwd, title string
	created                int64
	table                  string
}

// Source connections are read-only and each pass holds one WAL read snapshot.
// The stage marker follows archive commit, so an interrupted row is safely replayed.
func syncDatabase(ctx context.Context, cfg Config, root Root, remotes map[string]string, report *Report) error {
	info, err := os.Lstat(root.Path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("database root must be a regular file")
	}
	absolute, err := filepath.Abs(root.Path)
	if err != nil {
		return err
	}
	uri := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro&_busy_timeout=5000"}).String()
	db, err := sql.Open("sqlite3", uri)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return err
	}
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !tables["session"] && !tables["session_v2"] {
		return fmt.Errorf("unsupported native database schema")
	}
	var sessions []databaseSession
	for _, table := range []string{"session_v2", "session"} {
		if !tables[table] || table == "session_v2" && root.Client != "opencode" {
			continue
		}
		query := "SELECT id,COALESCE(parent_id,''),COALESCE(directory,''),COALESCE(title,''),time_created FROM " + table
		if table == "session" && root.Client == "opencode" && tables["session_v2"] {
			query += " WHERE id NOT IN (SELECT id FROM session_v2)"
		}
		rows, err = tx.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		for rows.Next() {
			var s databaseSession
			s.table = table
			if err = rows.Scan(&s.id, &s.parent, &s.cwd, &s.title, &s.created); err != nil {
				rows.Close()
				return err
			}
			sessions = append(sessions, s)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if _, err = cfg.Store.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS native_db_rows(source_key TEXT PRIMARY KEY,digest TEXT NOT NULL)`); err != nil {
		return err
	}
	for _, session := range sessions {
		c := nativehistory.Context{SessionID: session.id, ParentID: session.parent, CWD: session.cwd, Title: session.title, CreatedAt: time.UnixMilli(session.created).UTC(), AgentRole: "main"}
		if session.parent != "" {
			c.AgentRole = "subagent"
		}
		enrich(ctx, root.Client, root.Path, &c, remotes)
		table := "message"
		if session.table == "session_v2" {
			table = "session_message"
		}
		if !tables[table] {
			return fmt.Errorf("native database is missing its message table")
		}
		query := "SELECT id,data,time_created FROM message WHERE session_id=? ORDER BY time_created,id"
		if table == "session_message" {
			query = "SELECT id,data,time_created,type FROM session_message WHERE session_id=? ORDER BY seq,id"
		}
		rows, err = tx.QueryContext(ctx, query, session.id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var event nativehistory.DatabaseEvent
			var created int64
			event.Version = 1
			event.Context = c
			event.Table = table
			if table == "session_message" {
				err = rows.Scan(&event.RowID, &event.Data, &created, &event.Role)
			} else {
				err = rows.Scan(&event.RowID, &event.Data, &created)
			}
			if err != nil {
				rows.Close()
				return err
			}
			if len(event.Data) > maxLine || !json.Valid([]byte(event.Data)) {
				rows.Close()
				return fmt.Errorf("invalid or oversized database message; source not advanced")
			}
			event.At = time.UnixMilli(created).UTC().Format(time.RFC3339Nano)
			if table == "message" {
				var data map[string]any
				json.Unmarshal([]byte(event.Data), &data)
				event.Role, _ = data["role"].(string)
				parts, errPart := tx.QueryContext(ctx, "SELECT id,data FROM part WHERE message_id=? ORDER BY time_created,id", event.RowID)
				if errPart != nil {
					rows.Close()
					return errPart
				}
				total := len(event.Data)
				for parts.Next() {
					var part nativehistory.DatabasePart
					if err = parts.Scan(&part.ID, &part.Data); err != nil {
						parts.Close()
						rows.Close()
						return err
					}
					total += len(part.Data)
					if total > maxLine || !json.Valid([]byte(part.Data)) {
						parts.Close()
						rows.Close()
						return fmt.Errorf("invalid or oversized database parts; source not advanced")
					}
					event.Parts = append(event.Parts, part)
				}
				err = parts.Err()
				parts.Close()
				if err != nil {
					rows.Close()
					return err
				}
			}
			raw, err := json.Marshal(event)
			if err != nil {
				rows.Close()
				return err
			}
			if len(raw) > maxLine {
				rows.Close()
				return fmt.Errorf("database snapshot exceeds 64 MiB; source not advanced")
			}
			env := nativehistory.Envelope{Version: 1, Client: root.Client, Context: c, Lines: []nativehistory.Line{{Raw: string(raw)}}}
			revisionRaw, _ := json.Marshal(env)
			hash := id(string(revisionRaw))
			key := cfg.Device + "\x00" + root.Client + "\x00" + session.id + "\x00" + table + "\x00" + event.RowID
			var existing string
			markerErr := cfg.Store.DB.QueryRowContext(ctx, "SELECT digest FROM native_db_rows WHERE source_key=?", key).Scan(&existing)
			if markerErr != nil && markerErr != sql.ErrNoRows {
				rows.Close()
				return markerErr
			}
			if existing == hash {
				report.Existing++
				continue
			}
			if err = commitSegment(ctx, cfg, key, root.Path, env, 0, int64(len(raw)), report); err != nil {
				rows.Close()
				return err
			}
			if _, err = cfg.Store.DB.ExecContext(ctx, "INSERT INTO native_db_rows(source_key,digest) VALUES(?,?) ON CONFLICT(source_key) DO UPDATE SET digest=excluded.digest", key, hash); err != nil {
				rows.Close()
				return err
			}
			report.Bytes += int64(len(event.Data))
			for _, part := range event.Parts {
				report.Bytes += int64(len(part.Data))
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
