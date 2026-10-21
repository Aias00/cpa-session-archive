package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrSessionNotFound           = errors.New("session not found")
	ErrSessionManagementConflict = errors.New("session management revision conflict")
	ErrInvalidSessionManagement  = errors.New("invalid session management")
)

// No foreign key targets the rebuildable session summary projection.
const sessionManagementSchema = `CREATE TABLE IF NOT EXISTS session_management(
	session_id TEXT PRIMARY KEY,
	title TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	starred INTEGER NOT NULL DEFAULT 0,
	archived INTEGER NOT NULL DEFAULT 0,
	revision INTEGER NOT NULL CHECK(revision>0),
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS session_management_tags(
	session_id TEXT NOT NULL,
	tag TEXT NOT NULL,
	PRIMARY KEY(session_id,tag)
);
CREATE INDEX IF NOT EXISTS idx_session_management_tags_tag ON session_management_tags(tag,session_id);
CREATE INDEX IF NOT EXISTS idx_session_summaries_browse_time ON session_summaries(last_at DESC,session_id ASC);`

type SessionManagement struct {
	SessionID string   `json:"session_id"`
	Title     string   `json:"title"`
	Note      string   `json:"note"`
	Tags      []string `json:"tags"`
	Starred   bool     `json:"starred"`
	Archived  bool     `json:"archived"`
	Revision  int64    `json:"revision"`
	UpdatedAt string   `json:"updated_at"`
}

// SessionManagementUpdate replaces only user-maintained display metadata.
type SessionManagementUpdate struct {
	Title    string   `json:"title"`
	Note     string   `json:"note"`
	Tags     []string `json:"tags"`
	Starred  bool     `json:"starred"`
	Archived bool     `json:"archived"`
}

type ManagedSession struct {
	SessionSummary
	Management SessionManagement `json:"management"`
}

type ManagedSessionPage struct {
	Sessions []ManagedSession `json:"sessions"`
	Total    int              `json:"total"`
	Limit    int              `json:"limit"`
	Offset   int              `json:"offset"`
}

type SessionBrowseOptions struct {
	Limit        int               `json:"limit"`
	Offset       int               `json:"offset"`
	Query        string            `json:"q"`
	Tag          string            `json:"tag"`
	Archived     string            `json:"archived"`
	Starred      bool              `json:"starred"`
	Filters      map[string]string `json:"filters"`
	View         string            `json:"view"`
	InactiveDays int               `json:"inactive_days"`
}

func invalidSessionManagement(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidSessionManagement, message)
}

func normalizeSessionManagement(update SessionManagementUpdate) (SessionManagementUpdate, error) {
	if !utf8.ValidString(update.Title) || !utf8.ValidString(update.Note) {
		return update, invalidSessionManagement("text must be valid UTF-8")
	}
	if strings.ContainsFunc(update.Title, unicode.IsControl) || strings.ContainsRune(update.Note, 0) {
		return update, invalidSessionManagement("unsupported control character")
	}
	update.Title = strings.TrimSpace(update.Title)
	update.Note = strings.TrimSpace(update.Note)
	if utf8.RuneCountInString(update.Title) > 200 || utf8.RuneCountInString(update.Note) > 8000 || len(update.Tags) > 24 {
		return update, invalidSessionManagement("text or tag count exceeds limit")
	}
	unique := make(map[string]bool, len(update.Tags))
	tags := make([]string, 0, len(update.Tags))
	for _, tag := range update.Tags {
		if !utf8.ValidString(tag) {
			return update, invalidSessionManagement("tags must be valid UTF-8")
		}
		if strings.ContainsFunc(tag, unicode.IsControl) || strings.ContainsAny(tag, ",，") {
			return update, invalidSessionManagement("invalid tag")
		}
		tag = strings.TrimSpace(tag)
		if tag == "" || utf8.RuneCountInString(tag) > 48 {
			return update, invalidSessionManagement("invalid tag")
		}
		if !unique[tag] {
			unique[tag] = true
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	update.Tags = tags
	return update, nil
}

func sessionManagementWithQuery(ctx context.Context, q queryContext, id string) (SessionManagement, error) {
	out := SessionManagement{SessionID: id, Tags: []string{}}
	err := q.QueryRowContext(ctx, `SELECT COALESCE(m.title,''),COALESCE(m.note,''),COALESCE(m.starred,0),COALESCE(m.archived,0),COALESCE(m.revision,0),COALESCE(m.updated_at,'') FROM session_summaries s LEFT JOIN session_management m ON m.session_id=s.session_id WHERE s.session_id=?`, id).Scan(&out.Title, &out.Note, &out.Starred, &out.Archived, &out.Revision, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrSessionNotFound
	}
	if err != nil {
		return out, err
	}
	rows, err := q.QueryContext(ctx, `SELECT tag FROM session_management_tags WHERE session_id=? ORDER BY tag`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var tag string
		if err = rows.Scan(&tag); err != nil {
			return out, err
		}
		out.Tags = append(out.Tags, tag)
	}
	return out, rows.Err()
}

func (s *Store) SessionManagement(ctx context.Context, id string) (SessionManagement, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SessionManagement{}, err
	}
	defer tx.Rollback()
	out, err := sessionManagementWithQuery(ctx, tx, id)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) UpdateSessionManagement(ctx context.Context, id string, expectedRevision int64, update SessionManagementUpdate) (SessionManagement, error) {
	if expectedRevision < 0 || expectedRevision == math.MaxInt64 {
		return SessionManagement{}, invalidSessionManagement("invalid expected revision")
	}
	update, err := normalizeSessionManagement(update)
	if err != nil {
		return SessionManagement{}, err
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return SessionManagement{}, err
	}
	defer conn.Close()
	// Reserving the writer before reading makes initial creation and updates atomic.
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return SessionManagement{}, err
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	current, err := sessionManagementWithQuery(ctx, conn, id)
	if err != nil {
		return current, err
	}
	if current.Revision != expectedRevision {
		return current, ErrSessionManagementConflict
	}
	out := SessionManagement{SessionID: id, Title: update.Title, Note: update.Note, Tags: update.Tags, Starred: update.Starred, Archived: update.Archived, Revision: expectedRevision + 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	_, err = conn.ExecContext(ctx, `INSERT INTO session_management(session_id,title,note,starred,archived,revision,updated_at,manually_edited) VALUES(?,?,?,?,?,?,?,1) ON CONFLICT(session_id) DO UPDATE SET title=excluded.title,note=excluded.note,starred=excluded.starred,archived=excluded.archived,revision=excluded.revision,updated_at=excluded.updated_at,manually_edited=1`, id, out.Title, out.Note, out.Starred, out.Archived, out.Revision, out.UpdatedAt)
	if err != nil {
		return SessionManagement{}, err
	}
	if _, err = conn.ExecContext(ctx, `DELETE FROM session_management_tags WHERE session_id=?`, id); err != nil {
		return SessionManagement{}, err
	}
	for _, tag := range out.Tags {
		if _, err = conn.ExecContext(ctx, `INSERT INTO session_management_tags(session_id,tag) VALUES(?,?)`, id, tag); err != nil {
			return SessionManagement{}, err
		}
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return out, err
}

// BrowseSessions is a bounded live view; it does not create an export snapshot.
func (s *Store) BrowseSessions(ctx context.Context, options SessionBrowseOptions) (ManagedSessionPage, error) {
	options, from, args, err := sessionSelection(options, time.Now().UTC())
	out := ManagedSessionPage{Sessions: []ManagedSession{}, Limit: options.Limit, Offset: options.Offset}
	if err != nil {
		return out, err
	}

	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*)`+from, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	selectSQL := `SELECT s.session_id,s.requests,s.first_at,s.last_at,s.key_id,COALESCE(s.principal_id,''),
	COALESCE((SELECT alias FROM credential_principals p WHERE p.principal_id=s.principal_id AND alias<>'' ORDER BY updated_at DESC LIMIT 1),''),
	COALESCE((SELECT GROUP_CONCAT(DISTINCT credential_hash) FROM records r WHERE r.session_id=s.session_id AND COALESCE(credential_hash,'')<>''),''),s.model,s.project,s.summary,
	COALESCE((SELECT GROUP_CONCAT(value,CHAR(31)) FROM session_facets f WHERE f.session_id=s.session_id AND f.name='request.kind'),''),
	COALESCE((SELECT GROUP_CONCAT(value,CHAR(31)) FROM session_facets f WHERE f.session_id=s.session_id AND f.name='thread.source'),''),
	COALESCE(m.title,''),COALESCE(m.note,''),COALESCE(m.starred,0),COALESCE(m.archived,0),COALESCE(m.revision,0),COALESCE(m.updated_at,'')`
	pageArgs := append(append([]any{}, args...), options.Limit, options.Offset)
	order := ` ORDER BY COALESCE(m.starred,0) DESC,s.last_at DESC,s.session_id ASC`
	// Apply ordering and offsets to narrow identities before loading note text,
	// credential aggregates and other display fields for the selected page.
	pageSQL := `WITH selected AS MATERIALIZED (SELECT s.session_id` + from + order + ` LIMIT ? OFFSET ?) ` + selectSQL +
		` FROM selected selected_sessions JOIN session_summaries s ON s.session_id=selected_sessions.session_id LEFT JOIN session_management m ON m.session_id=s.session_id` + order
	rows, err := tx.QueryContext(ctx, pageSQL, pageArgs...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item ManagedSession
		var credentials, kinds, sources string
		m := &item.Management
		if err = rows.Scan(&item.SessionID, &item.Requests, &item.FirstAt, &item.LastAt, &item.KeyID, &item.PrincipalID, &item.PrincipalAlias, &credentials, &item.Model, &item.Project, &item.Summary, &kinds, &sources, &m.Title, &m.Note, &m.Starred, &m.Archived, &m.Revision, &m.UpdatedAt); err != nil {
			rows.Close()
			return out, err
		}
		item.CredentialHashes = splitCommaValues(credentials)
		item.Kinds = splitProjectionValues(kinds)
		item.ThreadSources = splitProjectionValues(sources)
		item.Summary = cleanTurnText(item.Summary)
		m.SessionID = item.SessionID
		m.Tags = []string{}
		out.Sessions = append(out.Sessions, item)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	// Fetch tags only for this page and within the same read transaction.
	if len(out.Sessions) > 0 {
		positions := make(map[string]int, len(out.Sessions))
		tagArgs := make([]any, 0, len(out.Sessions))
		marks := make([]string, 0, len(out.Sessions))
		for i, item := range out.Sessions {
			positions[item.SessionID] = i
			tagArgs = append(tagArgs, item.SessionID)
			marks = append(marks, "?")
		}
		tags, queryErr := tx.QueryContext(ctx, `SELECT session_id,tag FROM session_management_tags WHERE session_id IN (`+strings.Join(marks, ",")+`) ORDER BY session_id,tag`, tagArgs...)
		if queryErr != nil {
			return out, queryErr
		}
		for tags.Next() {
			var id, tag string
			if queryErr = tags.Scan(&id, &tag); queryErr != nil {
				tags.Close()
				return out, queryErr
			}
			i := positions[id]
			out.Sessions[i].Management.Tags = append(out.Sessions[i].Management.Tags, tag)
		}
		queryErr = tags.Err()
		closeErr = tags.Close()
		if queryErr != nil {
			return out, queryErr
		}
		if closeErr != nil {
			return out, closeErr
		}
	}
	return out, tx.Commit()
}
