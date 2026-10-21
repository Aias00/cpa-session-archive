package archive

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Each maximum uses an existing covering index. Including previous_session_id
// also detects records moved away from this session without scanning raw data.
const sessionBulkEventSequenceSQL = `MAX(
	COALESCE((SELECT MAX(e.sequence) FROM archive_ingest_events e WHERE e.session_id=s.session_id),0),
	COALESCE((SELECT MAX(e.sequence) FROM archive_ingest_events e WHERE e.previous_session_id=s.session_id),0))`

const sessionBulkSchema = `CREATE TABLE IF NOT EXISTS session_bulk_jobs(
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL CHECK(status IN ('preview','running','complete','undoing','undone')),
	selection_json TEXT NOT NULL,
	action_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS session_bulk_items(
	job_id TEXT NOT NULL REFERENCES session_bulk_jobs(id),
	session_id TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('pending','protected','applied','unchanged','conflict','undone','undo_conflict','cancelled')),
	reason TEXT NOT NULL DEFAULT '',
	expected_last_at TEXT NOT NULL,
	expected_requests INTEGER NOT NULL,
	expected_event_sequence INTEGER NOT NULL DEFAULT -1,
	before_title TEXT NOT NULL,
	before_note TEXT NOT NULL,
	before_tags TEXT NOT NULL,
	before_starred INTEGER NOT NULL,
	before_archived INTEGER NOT NULL,
	before_revision INTEGER NOT NULL,
	before_updated_at TEXT NOT NULL,
	before_manually_edited INTEGER NOT NULL,
	applied_revision INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(job_id,session_id)
);
CREATE INDEX IF NOT EXISTS idx_session_bulk_jobs_created ON session_bulk_jobs(created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_session_bulk_items_status ON session_bulk_items(job_id,status,session_id);`

// Bulk jobs are a durable management overlay. Frozen items contain no archive
// payloads and do not hold a long-running SQLite read transaction open.
type SessionBulkAction struct {
	Archived       *bool    `json:"archived,omitempty"`
	Starred        *bool    `json:"starred,omitempty"`
	AddTags        []string `json:"add_tags,omitempty"`
	RemoveTags     []string `json:"remove_tags,omitempty"`
	IncludeStarred bool     `json:"include_starred,omitempty"`
	IncludeEdited  bool     `json:"include_edited,omitempty"`
}

type SessionBulkPlanRequest struct {
	Selection SessionBrowseOptions `json:"selection"`
	Action    SessionBulkAction    `json:"action"`
}

type SessionBulkItem struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type SessionBulkJob struct {
	ID            string               `json:"id"`
	Status        string               `json:"status"`
	Selection     SessionBrowseOptions `json:"selection"`
	Action        SessionBulkAction    `json:"action"`
	Total         int                  `json:"total"`
	Pending       int                  `json:"pending"`
	Applied       int                  `json:"applied"`
	Conflicts     int                  `json:"conflicts"`
	Protected     int                  `json:"protected"`
	Unchanged     int                  `json:"unchanged"`
	Undone        int                  `json:"undone"`
	UndoConflicts int                  `json:"undo_conflicts"`
	Cancelled     int                  `json:"cancelled"`
	CreatedAt     string               `json:"created_at"`
	UpdatedAt     string               `json:"updated_at"`
	Items         []SessionBulkItem    `json:"items,omitempty"`
}

func ensureSessionBulkSchema(db *sql.DB) error {
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('session_management') WHERE name='manually_edited')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		// All metadata written before bulk jobs was maintained by a person.
		if _, err := db.Exec(`ALTER TABLE session_management ADD COLUMN manually_edited INTEGER NOT NULL DEFAULT 1`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(sessionBulkSchema); err != nil {
		return err
	}
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('session_bulk_items') WHERE name='expected_event_sequence')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		// A preview from before source fences cannot safely archive or rearchive
		// a session. Its sentinel always conflicts; a fresh preview is required.
		_, err := db.Exec(`ALTER TABLE session_bulk_items ADD COLUMN expected_event_sequence INTEGER NOT NULL DEFAULT -1`)
		return err
	}
	return nil
}

func normalizeSessionBulkAction(action SessionBulkAction) (SessionBulkAction, error) {
	add, err := normalizeSessionManagement(SessionManagementUpdate{Tags: action.AddTags})
	if err != nil {
		return action, err
	}
	remove, err := normalizeSessionManagement(SessionManagementUpdate{Tags: action.RemoveTags})
	if err != nil {
		return action, err
	}
	action.AddTags, action.RemoveTags = add.Tags, remove.Tags
	if action.Archived == nil && action.Starred == nil && len(action.AddTags) == 0 && len(action.RemoveTags) == 0 {
		return action, invalidSessionManagement("bulk action is empty")
	}
	for _, tag := range action.AddTags {
		if index := sort.SearchStrings(action.RemoveTags, tag); index < len(action.RemoveTags) && action.RemoveTags[index] == tag {
			return action, invalidSessionManagement("a tag cannot be both added and removed")
		}
	}
	return action, nil
}

func validSessionBulkID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// PlanSessionBulk atomically freezes every matching identity, independent of the
// browser page. The explicit bound rejects a large selection without truncation.
func (s *Store) PlanSessionBulk(ctx context.Context, request SessionBulkPlanRequest) (SessionBulkJob, error) {
	var out SessionBulkJob
	selection, from, args, err := sessionSelection(request.Selection, time.Now().UTC())
	if err != nil {
		return out, err
	}
	action, err := normalizeSessionBulkAction(request.Action)
	if err != nil {
		return out, err
	}
	// Pagination never constrains a frozen selection.
	selection.Limit, selection.Offset = 0, 0
	conn, err := s.beginSessionBulkWrite(ctx)
	if err != nil {
		return out, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	var count int
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*)`+from, args...).Scan(&count); err != nil {
		return out, err
	}
	if count > 100000 {
		return out, invalidSessionManagement("bulk selection exceeds 100000 sessions; narrow the filters")
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return out, err
	}
	id := "bulk-" + hex.EncodeToString(random[:])
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	selectionJSON, err := json.Marshal(selection)
	if err != nil {
		return out, err
	}
	actionJSON, err := json.Marshal(action)
	if err != nil {
		return out, err
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO session_bulk_jobs(id,status,selection_json,action_json,created_at,updated_at) VALUES(?,'preview',?,?,?,?)`, id, string(selectionJSON), string(actionJSON), stamp, stamp); err != nil {
		return out, err
	}
	protection := `''`
	if action.Archived != nil && *action.Archived {
		parts := []string{}
		if !action.IncludeStarred {
			parts = append(parts, `WHEN COALESCE(m.starred,0)=1 THEN 'favorite'`)
		}
		if !action.IncludeEdited {
			parts = append(parts, `WHEN COALESCE(m.manually_edited,0)=1 THEN 'manually_edited'`)
		}
		if len(parts) > 0 {
			protection = `CASE ` + strings.Join(parts, ` `) + ` ELSE '' END`
		}
	}
	insert := `INSERT INTO session_bulk_items(job_id,session_id,status,reason,expected_last_at,expected_requests,expected_event_sequence,before_title,before_note,before_tags,before_starred,before_archived,before_revision,before_updated_at,before_manually_edited)
		SELECT ?,s.session_id,CASE WHEN (` + protection + `)='' THEN 'pending' ELSE 'protected' END,` + protection + `,s.last_at,s.requests,` + sessionBulkEventSequenceSQL + `,
		COALESCE(m.title,''),COALESCE(m.note,''),COALESCE((SELECT GROUP_CONCAT(tag,CHAR(31)) FROM (SELECT tag FROM session_management_tags t WHERE t.session_id=s.session_id ORDER BY tag)),''),
		COALESCE(m.starred,0),COALESCE(m.archived,0),COALESCE(m.revision,0),COALESCE(m.updated_at,''),COALESCE(m.manually_edited,0)` + from
	insertArgs := append([]any{id}, args...)
	if _, err = conn.ExecContext(ctx, insert, insertArgs...); err != nil {
		return out, err
	}
	out, err = sessionBulkWithQuery(ctx, conn, id, true)
	if err != nil {
		return out, err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return out, err
}

func (s *Store) beginSessionBulkWrite(ctx context.Context) (*sql.Conn, error) {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func sessionBulkWithQuery(ctx context.Context, q queryContext, id string, samples bool) (SessionBulkJob, error) {
	var out SessionBulkJob
	var selectionJSON, actionJSON string
	err := q.QueryRowContext(ctx, `SELECT id,status,selection_json,action_json,created_at,updated_at FROM session_bulk_jobs WHERE id=?`, id).Scan(&out.ID, &out.Status, &selectionJSON, &actionJSON, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrSessionNotFound
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(selectionJSON), &out.Selection); err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(actionJSON), &out.Action); err != nil {
		return out, err
	}
	rows, err := q.QueryContext(ctx, `SELECT status,COUNT(*) FROM session_bulk_items WHERE job_id=? GROUP BY status`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var status string
		var count int
		if err = rows.Scan(&status, &count); err != nil {
			rows.Close()
			return out, err
		}
		out.Total += count
		switch status {
		case "pending":
			out.Pending = count
		case "protected":
			out.Protected = count
		case "applied":
			out.Applied = count
		case "unchanged":
			out.Unchanged = count
		case "conflict":
			out.Conflicts = count
		case "undone":
			out.Undone = count
		case "undo_conflict":
			out.UndoConflicts = count
		case "cancelled":
			out.Cancelled = count
		}
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	if samples {
		rows, err = q.QueryContext(ctx, `SELECT session_id,status,reason FROM session_bulk_items WHERE job_id=? ORDER BY CASE status WHEN 'conflict' THEN 0 WHEN 'undo_conflict' THEN 1 WHEN 'protected' THEN 2 ELSE 3 END,session_id LIMIT 20`, id)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var item SessionBulkItem
			if err = rows.Scan(&item.SessionID, &item.Status, &item.Reason); err != nil {
				rows.Close()
				return out, err
			}
			out.Items = append(out.Items, item)
		}
		err = rows.Err()
		closeErr = rows.Close()
		if err != nil {
			return out, err
		}
		if closeErr != nil {
			return out, closeErr
		}
	}
	return out, nil
}

func (s *Store) SessionBulk(ctx context.Context, id string) (SessionBulkJob, error) {
	if !validSessionBulkID(id) {
		return SessionBulkJob{}, invalidSessionManagement("invalid bulk job ID")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SessionBulkJob{}, err
	}
	defer tx.Rollback()
	out, err := sessionBulkWithQuery(ctx, tx, id, true)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) ListSessionBulk(ctx context.Context) ([]SessionBulkJob, error) {
	out := []SessionBulkJob{}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM session_bulk_jobs ORDER BY created_at DESC,id DESC LIMIT 20`)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	for _, id := range ids {
		job, readErr := sessionBulkWithQuery(ctx, tx, id, false)
		if readErr != nil {
			return out, readErr
		}
		out = append(out, job)
	}
	return out, tx.Commit()
}

func bulkStepLimit(limit int) (int, error) {
	if limit == 0 {
		return 100, nil
	}
	if limit < 1 || limit > 100 {
		return 0, invalidSessionManagement("bulk step must be between 1 and 100")
	}
	return limit, nil
}

type frozenSessionBulkItem struct {
	id              string
	before          SessionManagement
	lastAt          string
	requests        int
	eventSequence   int64
	manuallyEdited  bool
	appliedRevision int64
}

func frozenSessionBulkItems(ctx context.Context, q queryContext, id, status string, limit int) ([]frozenSessionBulkItem, error) {
	rows, err := q.QueryContext(ctx, `SELECT session_id,expected_last_at,expected_requests,expected_event_sequence,before_title,before_note,before_tags,before_starred,before_archived,before_revision,before_updated_at,before_manually_edited,applied_revision FROM session_bulk_items WHERE job_id=? AND status=? ORDER BY session_id LIMIT ?`, id, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []frozenSessionBulkItem{}
	for rows.Next() {
		var item frozenSessionBulkItem
		var tags string
		m := &item.before
		if err = rows.Scan(&item.id, &item.lastAt, &item.requests, &item.eventSequence, &m.Title, &m.Note, &tags, &m.Starred, &m.Archived, &m.Revision, &m.UpdatedAt, &item.manuallyEdited, &item.appliedRevision); err != nil {
			return nil, err
		}
		m.SessionID = item.id
		m.Tags = []string{}
		if tags != "" {
			m.Tags = strings.Split(tags, "\x1f")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Source events cover retained-request updates, moves and deletes that do not
// necessarily change the rebuildable summary's request count or last timestamp.
func sessionBulkActivityChanged(ctx context.Context, q queryContext, item frozenSessionBulkItem) (bool, error) {
	var lastAt string
	var requests int
	var eventSequence int64
	err := q.QueryRowContext(ctx, `SELECT s.last_at,s.requests,`+sessionBulkEventSequenceSQL+` FROM session_summaries s WHERE s.session_id=?`, item.id).Scan(&lastAt, &requests, &eventSequence)
	return lastAt != item.lastAt || requests != item.requests || eventSequence != item.eventSequence, err
}

func patchBulkSession(current SessionManagement, action SessionBulkAction) (SessionManagementUpdate, error) {
	update := SessionManagementUpdate{Title: current.Title, Note: current.Note, Tags: []string{}, Starred: current.Starred, Archived: current.Archived}
	if action.Archived != nil {
		update.Archived = *action.Archived
	}
	if action.Starred != nil {
		update.Starred = *action.Starred
	}
	tags := map[string]bool{}
	for _, tag := range current.Tags {
		tags[tag] = true
	}
	for _, tag := range action.RemoveTags {
		delete(tags, tag)
	}
	for _, tag := range action.AddTags {
		tags[tag] = true
	}
	for tag := range tags {
		update.Tags = append(update.Tags, tag)
	}
	normalized, err := normalizeSessionManagement(SessionManagementUpdate{Tags: update.Tags})
	update.Tags = normalized.Tags
	return update, err
}

func saveBulkManagement(ctx context.Context, conn *sql.Conn, id string, revision int64, update SessionManagementUpdate, manuallyEdited, restore bool) error {
	if revision <= 0 {
		return invalidSessionManagement("management revision exhausted")
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	assignments := `starred=excluded.starred,archived=excluded.archived,revision=excluded.revision,updated_at=excluded.updated_at,manually_edited=excluded.manually_edited`
	if restore {
		assignments += `,title=excluded.title,note=excluded.note`
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO session_management(session_id,title,note,starred,archived,revision,updated_at,manually_edited) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET `+assignments, id, update.Title, update.Note, update.Starred, update.Archived, revision, stamp, manuallyEdited)
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `DELETE FROM session_management_tags WHERE session_id=?`, id); err != nil {
		return err
	}
	for _, tag := range update.Tags {
		if _, err = conn.ExecContext(ctx, `INSERT INTO session_management_tags(session_id,tag) VALUES(?,?)`, id, tag); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ApplySessionBulk(ctx context.Context, id string, limit int) (SessionBulkJob, error) {
	return s.stepSessionBulk(ctx, id, limit, false)
}

func (s *Store) UndoSessionBulk(ctx context.Context, id string, limit int) (SessionBulkJob, error) {
	return s.stepSessionBulk(ctx, id, limit, true)
}

func (s *Store) stepSessionBulk(ctx context.Context, id string, limit int, undo bool) (SessionBulkJob, error) {
	var out SessionBulkJob
	if !validSessionBulkID(id) {
		return out, invalidSessionManagement("invalid bulk job ID")
	}
	limit, err := bulkStepLimit(limit)
	if err != nil {
		return out, err
	}
	conn, err := s.beginSessionBulkWrite(ctx)
	if err != nil {
		return out, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	out, err = sessionBulkWithQuery(ctx, conn, id, false)
	if err != nil {
		return out, err
	}
	if !undo && (out.Status == "undoing" || out.Status == "undone") {
		return out, ErrSessionManagementConflict
	}
	if !undo && out.Status == "complete" || undo && out.Status == "undone" {
		out, err = sessionBulkWithQuery(ctx, conn, id, true)
		if err != nil {
			return out, err
		}
		_, err = conn.ExecContext(ctx, `COMMIT`)
		return out, err
	}
	itemStatus, jobStatus := "pending", "running"
	if undo {
		itemStatus, jobStatus = "applied", "undoing"
		// Cancelling untouched items is safe and prevents a later apply call from
		// continuing an operation after undo has begun.
		if _, err = conn.ExecContext(ctx, `UPDATE session_bulk_items SET status='cancelled',reason='undo_requested' WHERE job_id=? AND status='pending'`, id); err != nil {
			return out, err
		}
	}
	items, err := frozenSessionBulkItems(ctx, conn, id, itemStatus, limit)
	if err != nil {
		return out, err
	}
	for _, item := range items {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		status, reason := "conflict", "metadata_changed"
		if undo {
			status = "undo_conflict"
		}
		current, readErr := sessionManagementWithQuery(ctx, conn, item.id)
		if readErr != nil && !errors.Is(readErr, ErrSessionNotFound) {
			return out, readErr
		}
		if errors.Is(readErr, ErrSessionNotFound) {
			reason = "session_missing"
		} else if undo {
			if current.Revision == item.appliedRevision && current.Revision < math.MaxInt64 {
				var active bool
				if item.before.Archived && !current.Archived {
					active, err = sessionBulkActivityChanged(ctx, conn, item)
					if err != nil {
						return out, err
					}
				}
				if active {
					reason = "new_activity"
				} else {
					before := item.before
					update := SessionManagementUpdate{Title: before.Title, Note: before.Note, Tags: before.Tags, Starred: before.Starred, Archived: before.Archived}
					if err = saveBulkManagement(ctx, conn, item.id, current.Revision+1, update, item.manuallyEdited, true); err != nil {
						return out, err
					}
					status, reason = "undone", ""
				}
			} else if current.Revision == math.MaxInt64 {
				reason = "revision_exhausted"
			}
		} else if current.Revision == item.before.Revision {
			var active bool
			if out.Action.Archived != nil && *out.Action.Archived {
				active, err = sessionBulkActivityChanged(ctx, conn, item)
				if err != nil {
					return out, err
				}
			}
			if active {
				reason = "new_activity"
			} else {
				update, patchErr := patchBulkSession(current, out.Action)
				if patchErr != nil {
					reason = "tag_limit"
				} else if current.Title == update.Title && current.Note == update.Note && current.Starred == update.Starred && current.Archived == update.Archived && reflect.DeepEqual(current.Tags, update.Tags) {
					status, reason = "unchanged", ""
				} else if current.Revision == math.MaxInt64 {
					reason = "revision_exhausted"
				} else {
					if err = saveBulkManagement(ctx, conn, item.id, current.Revision+1, update, item.manuallyEdited, false); err != nil {
						return out, err
					}
					if _, err = conn.ExecContext(ctx, `UPDATE session_bulk_items SET applied_revision=? WHERE job_id=? AND session_id=?`, current.Revision+1, id, item.id); err != nil {
						return out, err
					}
					status, reason = "applied", ""
				}
			}
		}
		if _, err = conn.ExecContext(ctx, `UPDATE session_bulk_items SET status=?,reason=? WHERE job_id=? AND session_id=?`, status, reason, id, item.id); err != nil {
			return out, err
		}
	}
	var remaining int
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_bulk_items WHERE job_id=? AND status=?`, id, itemStatus).Scan(&remaining); err != nil {
		return out, err
	}
	if remaining == 0 {
		jobStatus = "complete"
		if undo {
			jobStatus = "undone"
		}
	}
	if _, err = conn.ExecContext(ctx, `UPDATE session_bulk_jobs SET status=?,updated_at=? WHERE id=?`, jobStatus, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return out, err
	}
	out, err = sessionBulkWithQuery(ctx, conn, id, true)
	if err != nil {
		return out, err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return out, err
}
