package memory

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-session-archive/internal/embedding"
)

const (
	semanticWindowRunes  = 384
	semanticOverlapRunes = 96
	semanticEligible     = `c.role NOT IN ('system','developer') AND COALESCE(m.kind,'') NOT IN ('context','image')`
)

type SemanticStatus struct {
	Fingerprint      string `json:"fingerprint"`
	Dimensions       int    `json:"dimensions"`
	KnownTexts       int64  `json:"known_texts"`
	ReadyTexts       int64  `json:"ready_texts"`
	PendingTexts     int64  `json:"pending_texts"`
	FailedTexts      int64  `json:"failed_texts"`
	BackfillDone     bool   `json:"backfill_done"`
	CoverageComplete bool   `json:"coverage_complete"`
	Failures         int64  `json:"failures"`
	LastError        string `json:"last_error,omitempty"`
}

type semanticEngine struct {
	embedder                         embedding.Embedder
	providerFingerprint, fingerprint string
	dimensions                       int
	minimum                          float64
	mu                               sync.Mutex
}

// WithSemantic enables one explicit, derived embedding profile. Literal and
// diverse retrieval remain available independently of provider availability.
func WithSemantic(embedder embedding.Embedder, minimum float64) OpenOption {
	return func(options *openOptions) { options.embedder, options.minimum = embedder, minimum }
}

func openSemantic(db *sql.DB, embedder embedding.Embedder, minimum float64) (*semanticEngine, error) {
	if embedder == nil || embedder.Fingerprint() == "" || len(embedder.Fingerprint()) > 1024 || embedder.Dimensions() <= 0 || embedder.Dimensions() > 4096 || math.IsNaN(minimum) || math.IsInf(minimum, 0) || minimum < -1 || minimum > 1 {
		return nil, errors.New("invalid semantic embedding configuration")
	}
	s := &semanticEngine{embedder: embedder, providerFingerprint: embedder.Fingerprint(), dimensions: embedder.Dimensions(), minimum: minimum}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00rune-windows-%d-%d-skip-unicode-blank-float32-l2-v1", s.providerFingerprint, s.dimensions, semanticWindowRunes, semanticOverlapRunes)))
	s.fingerprint = hex.EncodeToString(sum[:])
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS semantic_profiles(fingerprint TEXT PRIMARY KEY,dimensions INTEGER NOT NULL,after_text_id INTEGER NOT NULL DEFAULT 0,backfill_done INTEGER NOT NULL DEFAULT 0,known_texts INTEGER NOT NULL DEFAULT 0,ready_texts INTEGER NOT NULL DEFAULT 0,failed_texts INTEGER NOT NULL DEFAULT 0,failures INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS semantic_jobs(fingerprint TEXT NOT NULL REFERENCES semantic_profiles(fingerprint) ON DELETE CASCADE,text_id INTEGER NOT NULL REFERENCES chunk_texts(id) ON DELETE CASCADE,content_hash TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','retry','ready','failed')),attempts INTEGER NOT NULL DEFAULT 0,next_attempt_ns INTEGER NOT NULL DEFAULT 0,error_code TEXT NOT NULL DEFAULT '',expected_windows INTEGER NOT NULL DEFAULT -1 CHECK(expected_windows>=-1),PRIMARY KEY(fingerprint,text_id));
 CREATE INDEX IF NOT EXISTS idx_semantic_pending ON semantic_jobs(fingerprint,state,next_attempt_ns,text_id);
 CREATE TABLE IF NOT EXISTS semantic_vectors(fingerprint TEXT NOT NULL,text_id INTEGER NOT NULL,start INTEGER NOT NULL,end INTEGER NOT NULL,vector BLOB NOT NULL,PRIMARY KEY(fingerprint,text_id,start),FOREIGN KEY(fingerprint,text_id) REFERENCES semantic_jobs(fingerprint,text_id) ON DELETE CASCADE);
 CREATE TRIGGER IF NOT EXISTS semantic_job_insert AFTER INSERT ON semantic_jobs BEGIN UPDATE semantic_profiles SET known_texts=known_texts+1,ready_texts=ready_texts+(NEW.state='ready'),failed_texts=failed_texts+(NEW.state='failed') WHERE fingerprint=NEW.fingerprint; END;
 CREATE TRIGGER IF NOT EXISTS semantic_job_delete AFTER DELETE ON semantic_jobs BEGIN UPDATE semantic_profiles SET known_texts=known_texts-1,ready_texts=ready_texts-(OLD.state='ready'),failed_texts=failed_texts-(OLD.state='failed') WHERE fingerprint=OLD.fingerprint; END;
 CREATE TRIGGER IF NOT EXISTS semantic_job_state AFTER UPDATE OF state ON semantic_jobs WHEN OLD.state<>NEW.state BEGIN UPDATE semantic_profiles SET ready_texts=ready_texts+(NEW.state='ready')-(OLD.state='ready'),failed_texts=failed_texts+(NEW.state='failed')-(OLD.state='failed') WHERE fingerprint=NEW.fingerprint; END;
 CREATE TRIGGER IF NOT EXISTS semantic_chunk_delete AFTER DELETE ON chunks BEGIN DELETE FROM semantic_jobs WHERE text_id=OLD.text_id AND NOT EXISTS(SELECT 1 FROM chunks c LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer WHERE c.text_id=OLD.text_id AND ` + semanticEligible + `); END;`)
	if err != nil {
		return nil, err
	}
	var hasExpectedWindows bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('semantic_jobs') WHERE name='expected_windows')`).Scan(&hasExpectedWindows); err != nil {
		return nil, err
	}
	if !hasExpectedWindows {
		if _, err := db.Exec(`ALTER TABLE semantic_jobs ADD COLUMN expected_windows INTEGER NOT NULL DEFAULT -1 CHECK(expected_windows>=-1)`); err != nil {
			return nil, err
		}
	}
	// Disabled or older collectors can project reused low IDs without queuing
	// this profile. Revisit existing IDs in bounded pages after every open.
	if _, err := db.Exec(`INSERT INTO semantic_profiles(fingerprint,dimensions) VALUES(?,?) ON CONFLICT(fingerprint) DO UPDATE SET after_text_id=0,backfill_done=0`, s.fingerprint, s.dimensions); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *semanticEngine) current() bool {
	return s.embedder.Fingerprint() == s.providerFingerprint && s.embedder.Dimensions() == s.dimensions
}

// Called by source projection and metadata refresh inside their own transaction.
// Existing unique IDs and newly reused low IDs are admitted just like new text.
func (e *Engine) queueSemanticSource(ctx context.Context, tx *sql.Tx, id string) error {
	if e.semantic == nil {
		return nil
	}
	// Reclassification can leave an excluded occurrence holding a shared text
	// alive. Such text must not strand a pending job or retain stale vectors.
	if _, err := tx.ExecContext(ctx, `DELETE FROM semantic_jobs WHERE text_id IN(SELECT text_id FROM chunks WHERE request_id=?) AND NOT EXISTS(SELECT 1 FROM chunks c LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer WHERE c.text_id=semantic_jobs.text_id AND `+semanticEligible+`)`, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO semantic_jobs(fingerprint,text_id,content_hash)
 SELECT DISTINCT ?,t.id,t.content_hash FROM chunks c JOIN chunk_texts t ON t.id=c.text_id
 LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer
 WHERE c.request_id=? AND `+semanticEligible, e.semantic.fingerprint, id)
	return err
}

func (e *Engine) discoverSemantic(ctx context.Context) (bool, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var after int64
	if err := tx.QueryRowContext(ctx, `SELECT after_text_id FROM semantic_profiles WHERE fingerprint=?`, e.semantic.fingerprint).Scan(&after); err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,content_hash FROM chunk_texts WHERE id>? ORDER BY id LIMIT 256`, after)
	if err != nil {
		return false, err
	}
	type textID struct {
		id   int64
		hash string
	}
	page := []textID{}
	for rows.Next() {
		var item textID
		if err := rows.Scan(&item.id, &item.hash); err != nil {
			rows.Close()
			return false, err
		}
		page = append(page, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, item := range page {
		if _, err := tx.ExecContext(ctx, `DELETE FROM semantic_jobs WHERE text_id=? AND NOT EXISTS(SELECT 1 FROM chunks c LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer WHERE c.text_id=? AND `+semanticEligible+`)`, item.id, item.id); err != nil {
			return false, err
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO semantic_jobs(fingerprint,text_id,content_hash) SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM chunks c LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer WHERE c.text_id=? AND `+semanticEligible+`)`, e.semantic.fingerprint, item.id, item.hash, item.id)
		if err != nil {
			return false, err
		}
		after = item.id
	}
	if _, err = tx.ExecContext(ctx, `UPDATE semantic_profiles SET after_text_id=?,backfill_done=? WHERE fingerprint=?`, after, len(page) < 256, e.semantic.fingerprint); err != nil {
		return false, err
	}
	return len(page) > 0, tx.Commit()
}

type semanticSpan struct {
	start, end int
	text       string
	vector     []float32
}
type semanticJob struct {
	id         int64
	hash, text string
	attempts   int
	spans      []semanticSpan
}

func semanticSpans(text string) []semanticSpan {
	runes := []rune(text)
	out := []semanticSpan{}
	for start := 0; start < len(runes); {
		end := min(start+semanticWindowRunes, len(runes))
		text := string(runes[start:end])
		if strings.TrimSpace(text) != "" {
			out = append(out, semanticSpan{start: start, end: end, text: text})
		}
		if end == len(runes) {
			break
		}
		start = end - semanticOverlapRunes
	}
	return out
}

func normalizeSemantic(vector []float32, dimensions int) ([]float32, error) {
	if len(vector) != dimensions {
		return nil, ErrSemanticUnavailable
	}
	var norm float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, ErrSemanticUnavailable
		}
		norm += float64(value) * float64(value)
	}
	if norm <= 0 || math.IsInf(norm, 0) {
		return nil, ErrSemanticUnavailable
	}
	norm = math.Sqrt(norm)
	out := make([]float32, len(vector))
	for i, value := range vector {
		out[i] = float32(float64(value) / norm)
	}
	return out, nil
}

func vectorBytes(vector []float32) []byte {
	out := make([]byte, 4*len(vector))
	for i, value := range vector {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(value))
	}
	return out
}

func (e *Engine) semanticFailure(ctx context.Context, jobs []semanticJob, code string) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	for _, job := range jobs {
		state := "retry"
		delay := time.Second * time.Duration(1<<min(job.attempts, 8))
		if job.attempts >= 8 {
			state = "failed"
			delay = 5 * time.Minute
		}
		_, err = tx.ExecContext(ctx, `UPDATE semantic_jobs SET state=?,next_attempt_ns=?,error_code=? WHERE fingerprint=? AND text_id=? AND content_hash=?`, state, time.Now().Add(delay).UnixNano(), code, e.semantic.fingerprint, job.id, job.hash)
		if err != nil {
			return
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE semantic_profiles SET failures=failures+1,last_error=? WHERE fingerprint=?`, code, e.semantic.fingerprint); err != nil {
		return
	}
	_ = tx.Commit()
}

// SyncSemantic owns only derived jobs. Model work happens after every SQLite
// snapshot/transaction has closed and without the raw projection's sync mutex.
func (e *Engine) SyncSemantic(ctx context.Context, limit int) (int, error) {
	if e.semantic == nil {
		return 0, ErrSemanticUnavailable
	}
	s := e.semantic
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := e.sourceIdentity(ctx, false); err != nil {
		return 0, err
	}
	if !s.current() {
		return 0, ErrSemanticUnavailable
	}
	if limit < 1 || limit > 8 {
		limit = 8
	}
	progress, err := e.discoverSemantic(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := e.readDB.QueryContext(ctx, `SELECT j.text_id,j.content_hash,t.text,j.attempts FROM semantic_jobs j JOIN chunk_texts t ON t.id=j.text_id AND t.content_hash=j.content_hash WHERE j.fingerprint=? AND j.state IN ('pending','retry','failed') AND j.next_attempt_ns<=? AND EXISTS(SELECT 1 FROM chunks c LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer WHERE c.text_id=t.id AND `+semanticEligible+`) ORDER BY j.text_id LIMIT ?`, s.fingerprint, time.Now().UnixNano(), limit)
	if err != nil {
		return 0, err
	}
	jobs := []semanticJob{}
	for rows.Next() {
		var job semanticJob
		if err = rows.Scan(&job.id, &job.hash, &job.text, &job.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		job.attempts++
		job.spans = semanticSpans(job.text)
		jobs = append(jobs, job)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if len(jobs) == 0 {
		if progress {
			return 1, nil
		}
		return 0, nil
	}
	for _, job := range jobs {
		if _, err := e.db.ExecContext(ctx, `UPDATE semantic_jobs SET attempts=?,next_attempt_ns=? WHERE fingerprint=? AND text_id=? AND content_hash=?`, job.attempts, time.Now().Add(30*time.Second).UnixNano(), s.fingerprint, job.id, job.hash); err != nil {
			return 0, err
		}
	}
	spans := []*semanticSpan{}
	for i := range jobs {
		for j := range jobs[i].spans {
			spans = append(spans, &jobs[i].spans[j])
		}
	}
	for start := 0; start < len(spans); start += 8 {
		batch := spans[start:min(start+8, len(spans))]
		texts := make([]string, len(batch))
		for i, span := range batch {
			texts[i] = span.text
		}
		vectors, embedErr := s.embedder.Embed(ctx, embedding.Document, texts)
		if embedErr != nil || len(vectors) != len(batch) || !s.current() {
			e.semanticFailure(ctx, jobs, "embedding_failed")
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, ErrSemanticUnavailable
		}
		for i, vector := range vectors {
			normalized, err := normalizeSemantic(vector, s.dimensions)
			if err != nil {
				e.semanticFailure(ctx, jobs, "invalid_vector")
				return 0, err
			}
			batch[i].vector = normalized
		}
	}
	if !s.current() {
		e.semanticFailure(ctx, jobs, "profile_changed")
		return 0, ErrSemanticUnavailable
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	completed := 0
	for _, job := range jobs {
		var hash string
		err = tx.QueryRowContext(ctx, `SELECT t.content_hash FROM chunk_texts t JOIN semantic_jobs j ON j.text_id=t.id AND j.content_hash=t.content_hash WHERE t.id=? AND j.fingerprint=? AND EXISTS(SELECT 1 FROM chunks c LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer WHERE c.text_id=t.id AND `+semanticEligible+`)`, job.id, s.fingerprint).Scan(&hash)
		if errors.Is(err, sql.ErrNoRows) || err == nil && hash != job.hash {
			continue
		}
		if err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM semantic_vectors WHERE fingerprint=? AND text_id=?`, s.fingerprint, job.id); err != nil {
			return 0, err
		}
		for _, span := range job.spans {
			if _, err = tx.ExecContext(ctx, `INSERT INTO semantic_vectors(fingerprint,text_id,start,end,vector) VALUES(?,?,?,?,?)`, s.fingerprint, job.id, span.start, span.end, vectorBytes(span.vector)); err != nil {
				return 0, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE semantic_jobs SET state='ready',expected_windows=?,next_attempt_ns=0,error_code='' WHERE fingerprint=? AND text_id=? AND content_hash=?`, len(job.spans), s.fingerprint, job.id, job.hash); err != nil {
			return 0, err
		}
		completed++
	}
	if _, err = tx.ExecContext(ctx, `UPDATE semantic_profiles SET last_error='' WHERE fingerprint=?`, s.fingerprint); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	if completed == 0 && progress {
		return 1, nil
	}
	return completed, nil
}

func (e *Engine) runSemantic(ctx context.Context) {
	backoff := time.Second
	for {
		n, err := e.SyncSemantic(ctx, 8)
		if ctx.Err() != nil {
			return
		}
		delay := time.Second
		if err == nil {
			backoff = time.Second
			if n > 0 {
				continue
			}
		} else {
			delay = backoff
			backoff = min(2*backoff, 30*time.Second)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (e *Engine) semanticStatus(ctx context.Context) (*SemanticStatus, error) {
	if e.semantic == nil {
		return nil, nil
	}
	out := &SemanticStatus{Fingerprint: e.semantic.fingerprint, Dimensions: e.semantic.dimensions}
	err := e.readDB.QueryRowContext(ctx, `SELECT known_texts,ready_texts,failed_texts,backfill_done,failures,last_error FROM semantic_profiles WHERE fingerprint=?`, e.semantic.fingerprint).Scan(&out.KnownTexts, &out.ReadyTexts, &out.FailedTexts, &out.BackfillDone, &out.Failures, &out.LastError)
	out.PendingTexts = out.KnownTexts - out.ReadyTexts - out.FailedTexts
	out.CoverageComplete = out.BackfillDone && out.PendingTexts == 0 && out.FailedTexts == 0
	return out, err
}

func semanticScope(q Query) ([]string, []any) {
	where := []string{semanticEligible}
	args := []any{}
	for _, filter := range []struct{ column, value string }{{"project_id", q.ProjectID}, {"session_id", q.SessionID}, {"client", q.Client}} {
		if filter.value != "" {
			where = append(where, "s."+filter.column+"=?")
			args = append(args, filter.value)
		}
	}
	for _, filter := range []struct{ op, value string }{{">=", q.After}, {"<", q.Before}} {
		if filter.value != "" {
			stamp, _ := time.Parse(time.RFC3339Nano, filter.value)
			where = append(where, "s.event_ns"+filter.op+"?")
			args = append(args, stamp.UnixNano())
		}
	}
	return where, args
}

type semanticMatch struct {
	id    int64
	hash  string
	start int
	score float64
}
type semanticHeap []semanticMatch

func (h semanticHeap) Len() int { return len(h) }
func (h semanticHeap) Less(i, j int) bool {
	if h[i].score != h[j].score {
		return h[i].score < h[j].score
	}
	return h[i].id > h[j].id
}
func (h semanticHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *semanticHeap) Push(value any) { *h = append(*h, value.(semanticMatch)) }
func (h *semanticHeap) Pop() any {
	old := *h
	value := old[len(old)-1]
	*h = old[:len(old)-1]
	return value
}

func (e *Engine) semanticMatches(ctx context.Context, q Query, query []float32, limit int) ([]semanticMatch, bool, bool, error) {
	where, args := semanticScope(q)
	args = append(args, e.semantic.fingerprint)
	rows, err := e.readDB.QueryContext(ctx, `WITH eligible AS MATERIALIZED(SELECT DISTINCT c.text_id FROM sources s JOIN chunks c ON c.request_id=s.request_id LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer WHERE `+strings.Join(where, " AND ")+`)
 SELECT t.id,t.content_hash,COALESCE(j.state,''),COALESCE(j.expected_windows,-1),v.start,v.vector FROM eligible x JOIN chunk_texts t ON t.id=x.text_id LEFT JOIN semantic_jobs j ON j.text_id=t.id AND j.content_hash=t.content_hash AND j.fingerprint=? LEFT JOIN semantic_vectors v ON v.fingerprint=j.fingerprint AND v.text_id=j.text_id AND j.state='ready' ORDER BY t.id,v.start`, args...)
	if err != nil {
		return nil, false, false, err
	}
	defer rows.Close()
	complete := true
	top := &semanticHeap{}
	heap.Init(top)
	var current *semanticMatch
	count := 0
	var observedID int64
	expectedWindows, seenWindows := 0, 0
	flush := func() {
		if expectedWindows != seenWindows {
			complete = false
		}
		if current == nil || current.score < e.semantic.minimum {
			return
		}
		count++
		if top.Len() < limit {
			heap.Push(top, *current)
		} else if current.score > (*top)[0].score || current.score == (*top)[0].score && current.id < (*top)[0].id {
			heap.Pop(top)
			heap.Push(top, *current)
		}
	}
	for rows.Next() {
		if ctx.Err() != nil {
			return nil, false, false, ctx.Err()
		}
		var id int64
		var hash, state string
		var expected int
		var start sql.NullInt64
		var blob []byte
		if err = rows.Scan(&id, &hash, &state, &expected, &start, &blob); err != nil {
			return nil, false, false, err
		}
		if observedID != id {
			flush()
			current = nil
			observedID, expectedWindows, seenWindows = id, expected, 0
		}
		if state != "ready" || expected < 0 {
			complete = false
			continue
		}
		if expected == 0 {
			if start.Valid {
				return nil, false, false, ErrSemanticUnavailable
			}
			continue
		}
		if !start.Valid {
			complete = false
			continue
		}
		seenWindows++
		if len(blob) != 4*len(query) {
			return nil, false, false, ErrSemanticUnavailable
		}
		score, norm := float64(0), float64(0)
		for i, value := range query {
			v := math.Float32frombits(binary.LittleEndian.Uint32(blob[4*i:]))
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, false, false, ErrSemanticUnavailable
			}
			score += float64(v) * float64(value)
			norm += float64(v) * float64(v)
		}
		if norm <= 0 {
			return nil, false, false, ErrSemanticUnavailable
		}
		score /= math.Sqrt(norm)
		if current == nil || score > current.score {
			current = &semanticMatch{id: id, hash: hash, start: int(start.Int64), score: score}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, false, false, err
	}
	flush()
	out := append([]semanticMatch(nil), (*top)...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].id < out[j].id
	})
	return out, complete, count > limit, nil
}

func (e *Engine) semanticCandidates(ctx context.Context, q Query, matches []semanticMatch) ([]candidate, error) {
	if len(matches) == 0 {
		return nil, nil
	}
	where, args := semanticScope(q)
	values := make([]string, len(matches))
	prefix := []any{}
	for i, match := range matches {
		values[i] = "(?,?,?,?)"
		prefix = append(prefix, match.id, match.hash, i+1, match.start)
	}
	args = append(prefix, args...)
	rows, err := e.readDB.QueryContext(ctx, `WITH selected(text_id,content_hash,semantic_rank,semantic_start) AS(VALUES `+strings.Join(values, ",")+`), eligible AS(
 SELECT c.id,c.request_id,c.side,c.pointer,c.role,c.text_id,s.event_ns,x.semantic_rank,x.semantic_start,COALESCE(m.kind,CASE WHEN c.role='tool' THEN 'tool_result' ELSE 'text' END) AS kind,COALESCE(m.tool_name,'') AS tool_name,CASE WHEN COALESCE(m.kind,'') IN ('tool_use','tool_result') OR c.role='tool' THEN 2 WHEN COALESCE(m.kind,'')='summary' THEN 1 ELSE 0 END AS kind_rank
 FROM selected x JOIN chunk_texts expected ON expected.id=x.text_id AND expected.content_hash=x.content_hash JOIN chunks c ON c.text_id=expected.id JOIN sources s ON s.request_id=c.request_id LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer WHERE `+strings.Join(where, " AND ")+`), rounds AS(SELECT *,ROW_NUMBER() OVER(PARTITION BY text_id,role,kind,tool_name ORDER BY event_ns DESC,request_id,id) AS content_round,ROW_NUMBER() OVER(PARTITION BY request_id,side,pointer ORDER BY semantic_rank,id) AS message_round FROM eligible), candidates AS MATERIALIZED(SELECT * FROM rounds ORDER BY MAX(content_round,message_round),semantic_rank,kind_rank,event_ns DESC,request_id,id LIMIT 1001)
 SELECT s.request_id,s.revision,s.project_id,s.client,s.session_id,s.event_at,s.partial,c.side,c.pointer,c.role,c.start,c.end,t.text,x.kind,x.tool_name,COALESCE(m.tool_call_id,''),COALESCE(m.model,''),COALESCE(m.message_at,''),x.semantic_start
 FROM candidates x JOIN chunks c ON c.id=x.id JOIN sources s ON s.request_id=x.request_id JOIN chunk_texts t ON t.id=c.text_id LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer ORDER BY MAX(x.content_round,x.message_round),x.semantic_rank,x.kind_rank,x.event_ns DESC,x.request_id,x.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.hit.Citation.RequestID, &c.hit.Citation.Revision, &c.hit.ProjectID, &c.hit.Client, &c.hit.SessionID, &c.hit.EventAt, &c.hit.Partial, &c.hit.Citation.Side, &c.hit.Citation.Pointer, &c.hit.Role, &c.hit.Citation.Start, &c.hit.Citation.End, &c.text, &c.hit.Kind, &c.hit.ToolName, &c.hit.ToolCallID, &c.hit.Model, &c.hit.MessageAt, &c.semanticOffset); err != nil {
			return nil, err
		}
		c.hit.Citation.NormalizerVersion = NormalizerVersion
		c.semantic = true
		out = append(out, c)
	}
	return out, rows.Err()
}

func literalCandidate(c candidate, q Query) bool {
	lower := strings.ToLower(c.text)
	for _, term := range strings.Fields(strings.ToLower(q.Text)) {
		if !strings.Contains(lower, term) {
			return false
		}
	}
	return true
}
func semanticTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func fuseSemantic(q Query, lexical, semantic []candidate, matches []semanticMatch) []candidate {
	type ranked struct {
		candidate candidate
		score     float64
		round     int
	}
	out := map[string]*ranked{}
	lexRanks := map[string]int{}
	semRanks := map[string]int{}
	for i, match := range matches {
		semRanks[match.hash] = i + 1
	}
	key := func(c candidate) string {
		return fmt.Sprintf("%s\x00%s\x00%s\x00%d", c.hit.Citation.RequestID, c.hit.Citation.Side, c.hit.Citation.Pointer, c.hit.Citation.Start)
	}
	for _, c := range lexical {
		if !literalCandidate(c, q) {
			continue
		}
		hash := semanticTextHash(c.text)
		if lexRanks[hash] == 0 {
			lexRanks[hash] = len(lexRanks) + 1
		}
		k := key(c)
		out[k] = &ranked{candidate: c, score: 1 / float64(60+lexRanks[hash])}
	}
	for _, c := range semantic {
		k := key(c)
		rank := semRanks[semanticTextHash(c.text)]
		if rank == 0 {
			continue
		}
		if existing := out[k]; existing != nil {
			existing.score += 1 / float64(60+rank)
		} else {
			out[k] = &ranked{candidate: c, score: 1 / float64(60+rank)}
		}
	}
	items := make([]*ranked, 0, len(out))
	for _, item := range out {
		items = append(items, item)
	}
	less := func(a, b *ranked) bool {
		if a.score != b.score {
			return a.score > b.score
		}
		if a.candidate.hit.EventAt != b.candidate.hit.EventAt {
			return a.candidate.hit.EventAt > b.candidate.hit.EventAt
		}
		return key(a.candidate) < key(b.candidate)
	}
	sort.Slice(items, func(i, j int) bool { return less(items[i], items[j]) })
	rounds := map[string]int{}
	messageRounds := map[string]int{}
	for _, item := range items {
		c := item.candidate
		group := c.hit.Role + "\x00" + c.hit.Kind + "\x00" + c.hit.ToolName + "\x00" + semanticTextHash(c.text)
		rounds[group]++
		message := c.hit.Citation.RequestID + "\x00" + c.hit.Citation.Side + "\x00" + c.hit.Citation.Pointer
		messageRounds[message]++
		item.round = max(rounds[group], messageRounds[message])
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].round != items[j].round {
			return items[i].round < items[j].round
		}
		return less(items[i], items[j])
	})
	result := make([]candidate, len(items))
	for i, item := range items {
		result[i] = item.candidate
	}
	return result
}

func (e *Engine) searchHybrid(ctx context.Context, q Query) (SearchResult, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := e.hybridCandidates(bounded, q)
	if ctx.Err() != nil {
		return SearchResult{}, ctx.Err()
	}
	if bounded.Err() != nil {
		return SearchResult{}, ErrQueryBudget
	}
	return out, err
}

func (e *Engine) hybridCandidates(ctx context.Context, q Query) (SearchResult, error) {
	if e.semantic == nil || !e.semantic.current() {
		return SearchResult{}, ErrSemanticUnavailable
	}
	if _, err := e.sourceIdentity(ctx, false); err != nil {
		return SearchResult{}, err
	}
	vectors, err := e.semantic.embedder.Embed(ctx, embedding.Query, []string{q.Text})
	if err != nil || len(vectors) != 1 || !e.semantic.current() {
		return SearchResult{}, ErrSemanticUnavailable
	}
	query, err := normalizeSemantic(vectors[0], e.semantic.dimensions)
	if err != nil {
		return SearchResult{}, err
	}
	matches, complete, semanticTruncated, err := e.semanticMatches(ctx, q, query, 100)
	if err != nil {
		return SearchResult{}, err
	}
	lexical, err := e.selectDiverseCandidates(ctx, q, 1001, false)
	if err != nil {
		return SearchResult{}, err
	}
	semantic, err := e.semanticCandidates(ctx, q, matches)
	if err != nil {
		return SearchResult{}, err
	}
	result, err := e.finishSearch(ctx, q, fuseSemantic(q, lexical, semantic, matches))
	if err != nil {
		return SearchResult{}, err
	}
	result.Truncated = result.Truncated || !complete || semanticTruncated || len(lexical) == 1001 || len(semantic) == 1001
	result.Retrieval = &RetrievalStatus{Mode: "hybrid", SemanticComplete: complete, Fingerprint: e.semantic.fingerprint}
	return result, nil
}
