package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/embedding"
	"cpa-session-archive/internal/storage"
)

// These vectors exercise indexing and ranking contracts, not model quality.
type semanticFake struct {
	mu          sync.Mutex
	fingerprint string
	dimensions  int
	documents   []string
	fn          func(context.Context, embedding.Purpose, []string) ([][]float32, error)
}

func newSemanticFake() *semanticFake {
	return &semanticFake{fingerprint: "deterministic-test-profile-v1", dimensions: 2}
}
func (f *semanticFake) Fingerprint() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fingerprint
}
func (f *semanticFake) Dimensions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dimensions
}
func (f *semanticFake) Embed(ctx context.Context, purpose embedding.Purpose, texts []string) ([][]float32, error) {
	f.mu.Lock()
	if purpose == embedding.Document {
		f.documents = append(f.documents, texts...)
	}
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, purpose, texts)
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, ctx.Err()
}
func (f *semanticFake) documentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.documents)
}

func semanticFixture(t *testing.T, f *semanticFake) (*Engine, *archive.Store, string) {
	t.Helper()
	dir := t.TempDir()
	raw, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.DB.Close() })
	path := filepath.Join(dir, "memory.sqlite")
	e, err := Open(path, storage.NewSQLite(raw), WithSemantic(f, 0.1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, raw, path
}

func syncSemanticAll(t *testing.T, e *Engine) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		if _, err := e.SyncSemantic(context.Background(), 8); err != nil {
			t.Fatal(err)
		}
		status, err := e.semanticStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.CoverageComplete {
			return
		}
	}
	t.Fatal("semantic backfill did not converge")
}

func semanticCount(t *testing.T, e *Engine, table string) int {
	t.Helper()
	if table != "semantic_jobs" && table != "semantic_vectors" {
		t.Fatal("invalid test table")
	}
	var n int
	if err := e.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE fingerprint=?", e.semantic.fingerprint).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSemanticWindowsCoverRuneInputAndRetainOverlap(t *testing.T) {
	for _, length := range []int{0, 1, 383, 384, 385, 672, 673, 1600} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			runes := []rune(strings.Repeat("中😀é", length/3+1))[:length]
			spans := semanticSpans(string(runes))
			covered := make([]bool, length)
			for i, span := range spans {
				if span.end-span.start > 384 || span.text != string(runes[span.start:span.end]) {
					t.Fatal("window does not identify its exact rune range")
				}
				if i > 0 && spans[i-1].end-span.start != 96 {
					t.Fatal("window overlap changed")
				}
				for j := span.start; j < span.end; j++ {
					covered[j] = true
				}
			}
			for _, present := range covered {
				if !present {
					t.Fatal("window partition lost source text")
				}
			}
		})
	}
}

func TestSemanticWhitespaceWindowsCompleteWithoutBlankModelCalls(t *testing.T) {
	f := newSemanticFake()
	f.fn = func(_ context.Context, _ embedding.Purpose, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i, text := range texts {
			if strings.TrimSpace(text) == "" {
				t.Fatal("blank input reached the embedding provider")
			}
			out[i] = []float32{1, 0}
		}
		return out, nil
	}
	e, raw, _ := semanticFixture(t, f)
	text := "开始" + strings.Repeat(" \u3000", 1800) + "结尾"
	if err := raw.PutBatch([]archive.Record{sample("whitespace", "s", "p", text)}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	syncSemanticAll(t, e)
	var expected, blanks int
	if err := e.db.QueryRow(`SELECT SUM(expected_windows),SUM(expected_windows=0) FROM semantic_jobs WHERE fingerprint=?`, e.semantic.fingerprint).Scan(&expected, &blanks); err != nil || blanks == 0 || expected == 0 || expected != f.documentCount() {
		t.Fatal("fixture did not complete both empty and substantive chunks", err)
	}
	status, err := e.Status(context.Background())
	if err != nil || status.Semantic == nil || status.Semantic.FailedTexts != 0 || status.Semantic.Failures != 0 || status.Semantic.PendingTexts != 0 || status.Semantic.ReadyTexts != status.Semantic.KnownTexts || !status.Semantic.CoverageComplete {
		t.Fatal("whitespace stranded semantic coverage or reported a false failure", err)
	}
	token := strings.Repeat("w", 48)
	handler, err := NewHandler(e, token)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var publicStatus Status
	if err := json.Unmarshal(response.Body.Bytes(), &publicStatus); err != nil || response.Code != http.StatusOK || publicStatus.Semantic == nil || publicStatus.Semantic.FailedTexts != 0 || !publicStatus.Semantic.CoverageComplete {
		t.Fatal("status API reported blank chunks as indexing failures", err)
	}
	result, err := e.Search(context.Background(), Query{Text: "paraphrase", ProjectID: "p", Mode: "hybrid"})
	if err != nil || len(result.Hits) != 1 || result.Retrieval == nil || !result.Retrieval.SemanticComplete || strings.TrimSpace(result.Hits[0].Snippet) == "" {
		t.Fatal("ready zero-window jobs became matches or incomplete coverage", err)
	}
	checkDiverseCitations(t, e, result)
	if _, err := e.db.Exec(`DELETE FROM semantic_vectors WHERE fingerprint=?`, e.semantic.fingerprint); err != nil {
		t.Fatal(err)
	}
	result, err = e.Search(context.Background(), Query{Text: "paraphrase", ProjectID: "p", Mode: "hybrid"})
	if err != nil || len(result.Hits) != 0 || result.Retrieval == nil || result.Retrieval.SemanticComplete || !result.Truncated {
		t.Fatal("missing positive-count vectors were mistaken for intentional zero-window completion", err)
	}
}

func TestSemanticExpectedWindowsColumnIsAddedToExistingSchema(t *testing.T) {
	f := newSemanticFake()
	e, raw, path := semanticFixture(t, f)
	if _, err := e.db.Exec(`ALTER TABLE semantic_jobs DROP COLUMN expected_windows`); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, storage.NewSQLite(raw), WithSemantic(f, 0.1))
	if err != nil {
		t.Fatal("prior semantic schema could not upgrade additively", err)
	}
	defer reopened.Close()
	if err := raw.PutBatch([]archive.Record{sample("upgraded", "s", "p", "schema upgrade evidence")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, reopened)
	syncSemanticAll(t, reopened)
	var expected int
	if err := reopened.db.QueryRow(`SELECT expected_windows FROM semantic_jobs WHERE fingerprint=?`, reopened.semantic.fingerprint).Scan(&expected); err != nil || expected != 1 {
		t.Fatal("upgraded schema did not persist expected window counts", err)
	}
}

func TestSemanticEmbedsSharedTextOnceAndRetainsScopedCitations(t *testing.T) {
	f := newSemanticFake()
	e, raw, _ := semanticFixture(t, f)
	if err := raw.PutBatch([]archive.Record{sample("a", "sa", "p", "采用内置数据库保存历史决策"), sample("b", "sb", "q", "采用内置数据库保存历史决策")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	syncSemanticAll(t, e)
	if f.documentCount() != 1 || semanticCount(t, e, "semantic_jobs") != 1 {
		t.Fatal("repeated source text was embedded more than once")
	}
	for project, id := range map[string]string{"p": "a", "q": "b"} {
		q := Query{Text: "回忆存储方案", ProjectID: project, Mode: "hybrid"}
		result, err := e.Search(context.Background(), q)
		if err != nil || len(result.Hits) != 1 || result.Hits[0].Citation.RequestID != id || result.Retrieval == nil || !result.Retrieval.SemanticComplete {
			t.Fatalf("shared vectors lost scoped occurrences: %d hits, %v", len(result.Hits), err)
		}
		checkDiverseCitations(t, e, result)
		q.Mode = "literal"
		literal, err := e.Search(context.Background(), q)
		if err != nil || len(literal.Hits) != 0 {
			t.Fatal("semantic relaxation affected literal matching", err)
		}
	}
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='a'`); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	if semanticCount(t, e, "semantic_vectors") != 1 {
		t.Fatal("deleting one project discarded another project's shared vector")
	}
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='b'`); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	if semanticCount(t, e, "semantic_jobs") != 0 || semanticCount(t, e, "semantic_vectors") != 0 {
		t.Fatal("deleted source left derived vectors or jobs")
	}
	status, err := e.semanticStatus(context.Background())
	if err != nil || status.KnownTexts != 0 || status.ReadyTexts != 0 || !status.CoverageComplete {
		t.Fatal("cached job counts did not follow deletion", err)
	}
}

func TestSemanticAppliesEveryScopeBeforeTopK(t *testing.T) {
	f := newSemanticFake()
	f.fn = func(_ context.Context, purpose embedding.Purpose, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i, text := range texts {
			out[i] = []float32{1, 0}
			if purpose == embedding.Document && text == "authorized evidence" {
				out[i] = []float32{0.8, 0.6}
			}
		}
		return out, nil
	}
	e, raw, _ := semanticFixture(t, f)
	base := sample("allowed", "selected", "p", "authorized evidence")
	base.StartedAt = base.StartedAt.Add(time.Minute)
	records := []archive.Record{base}
	for _, field := range []string{"project", "session", "client", "after", "before"} {
		r := sample("excluded-"+field, "selected", "p", "forbidden evidence "+field)
		r.StartedAt = base.StartedAt
		switch field {
		case "project":
			r.Facets["project.id"] = []string{"foreign"}
		case "session":
			r.SessionID = "other-session"
		case "client":
			r.Facets["client"] = []string{"other-client"}
		case "after":
			r.StartedAt = base.StartedAt.Add(-time.Hour)
		case "before":
			r.StartedAt = base.StartedAt.Add(time.Hour)
		}
		records = append(records, r)
	}
	if err := raw.PutBatch(records); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	syncSemanticAll(t, e)
	q := Query{Text: "retrieval", ProjectID: "p", SessionID: "selected", Client: "codex", After: base.StartedAt.Add(-time.Second).Format(time.RFC3339Nano), Before: base.StartedAt.Add(time.Second).Format(time.RFC3339Nano), Mode: "hybrid"}
	matches, complete, truncated, err := e.semanticMatches(context.Background(), q, []float32{1, 0}, 1)
	if err != nil || !complete || truncated || len(matches) != 1 || matches[0].hash != semanticTextHash("authorized evidence") {
		t.Fatalf("foreign high-scoring vectors consumed scoped top-k: %d matches, %v", len(matches), err)
	}
	result, err := e.Search(context.Background(), q)
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Citation.RequestID != "allowed" {
		t.Fatal("hybrid candidate expansion violated scope", err)
	}
	checkDiverseCitations(t, e, result)
}

func TestSemanticLateResultNeverAttachesToReusedTextID(t *testing.T) {
	f := newSemanticFake()
	e, raw, _ := semanticFixture(t, f)
	// Keep raw row IDs above the deleted record while leaving its derived text
	// as the only chunk, so SQLite reuses that lower derived ID deterministically.
	if err := raw.PutBatch([]archive.Record{sample("old", "s", "p", "old evidence"), sample("raw-row-guard", "s", "p", "")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	var oldID int64
	if err := e.db.QueryRow(`SELECT id FROM chunk_texts`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	mutated := false
	f.fn = func(_ context.Context, purpose embedding.Purpose, texts []string) ([][]float32, error) {
		if purpose == embedding.Document && !mutated {
			mutated = true
			if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='old'`); err != nil {
				t.Fatal(err)
			}
			syncAll(t, e)
			if err := raw.PutBatch([]archive.Record{sample("new", "s", "p", "replacement evidence")}); err != nil {
				t.Fatal(err)
			}
			syncAll(t, e)
		}
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{1, 0}
		}
		return out, nil
	}
	if _, err := e.SyncSemantic(context.Background(), 8); err != nil {
		t.Fatal(err)
	}
	var newID int64
	if err := e.db.QueryRow(`SELECT id FROM chunk_texts`).Scan(&newID); err != nil || newID != oldID {
		t.Fatal("fixture did not force low text ID reuse", err)
	}
	if semanticCount(t, e, "semantic_vectors") != 0 {
		t.Fatal("late vector was attached to replacement text")
	}
	status, err := e.semanticStatus(context.Background())
	if err != nil || status.PendingTexts != 1 || status.ReadyTexts != 0 {
		t.Fatal("replacement was not retained as a distinct pending job", err)
	}
	stale := []semanticMatch{{id: oldID, hash: semanticTextHash("old evidence"), start: 288}, {id: oldID + 1, hash: semanticTextHash("replacement evidence"), start: 0}}
	candidates, err := e.semanticCandidates(context.Background(), Query{ProjectID: "p"}, stale)
	if err != nil || len(candidates) != 0 {
		t.Fatal("candidate expansion accepted a stale text ID/hash pair", err)
	}
	syncSemanticAll(t, e)
	if semanticCount(t, e, "semantic_vectors") != 1 {
		t.Fatal("replacement job behind durable cursor was never processed")
	}
}

func TestSemanticPendingBackfillResumesAfterRestart(t *testing.T) {
	f := newSemanticFake()
	e, raw, path := semanticFixture(t, f)
	for _, id := range []string{"a", "b", "c"} {
		if err := raw.PutBatch([]archive.Record{sample(id, "s", "p", "distinct evidence "+id)}); err != nil {
			t.Fatal(err)
		}
	}
	syncAll(t, e)
	if n, err := e.SyncSemantic(context.Background(), 1); err != nil || n != 1 {
		t.Fatal("first partial batch failed", err)
	}
	before, err := e.semanticStatus(context.Background())
	if err != nil || before.ReadyTexts != 1 || before.PendingTexts != 2 {
		t.Fatal("unexpected partial indexing status", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, storage.NewSQLite(raw), WithSemantic(f, 0.1))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	syncSemanticAll(t, reopened)
	after, err := reopened.semanticStatus(context.Background())
	if err != nil || after.ReadyTexts != 3 || f.documentCount() != 3 {
		t.Fatal("restart lost jobs or repeated ready embeddings", err)
	}
}

func TestSemanticReenableRediscoversReusedLowIDsFromDisabledProjection(t *testing.T) {
	f := newSemanticFake()
	e, raw, path := semanticFixture(t, f)
	if err := raw.PutBatch([]archive.Record{sample("old", "s", "p", "first evidence"), sample("raw-row-guard", "s", "p", "")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	syncSemanticAll(t, e)
	var oldID int64
	if err := e.db.QueryRow(`SELECT id FROM chunk_texts`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	disabled, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='old'`); err != nil {
		t.Fatal(err)
	}
	syncAll(t, disabled)
	if err := raw.PutBatch([]archive.Record{sample("new", "s", "p", "new evidence while disabled")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, disabled)
	var newID int64
	if err := disabled.db.QueryRow(`SELECT id FROM chunk_texts`).Scan(&newID); err != nil || newID != oldID {
		t.Fatal("fixture did not reuse an ID below the old semantic cursor", err)
	}
	if err := disabled.Close(); err != nil {
		t.Fatal(err)
	}
	reenabled, err := Open(path, storage.NewSQLite(raw), WithSemantic(f, 0.1))
	if err != nil {
		t.Fatal(err)
	}
	defer reenabled.Close()
	status, err := reenabled.semanticStatus(context.Background())
	if err != nil || status.BackfillDone || status.CoverageComplete {
		t.Fatal("startup reported complete before bounded rediscovery", err)
	}
	syncSemanticAll(t, reenabled)
	if f.documentCount() != 2 || semanticCount(t, reenabled, "semantic_vectors") != 1 {
		t.Fatal("re-enabled profile skipped reused text behind its old cursor")
	}
	result, err := reenabled.Search(context.Background(), Query{Text: "paraphrase", ProjectID: "p", Mode: "hybrid"})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Citation.RequestID != "new" || !result.Retrieval.SemanticComplete {
		t.Fatal("re-enabled profile did not converge on current text", err)
	}
}

func TestSemanticInvalidProviderOutputStaysDerivedAndDoesNotBlockLaterJobs(t *testing.T) {
	for _, bad := range []string{"error", "count", "dimension", "zero", "nan", "infinity"} {
		t.Run(bad, func(t *testing.T) {
			f := newSemanticFake()
			e, raw, _ := semanticFixture(t, f)
			if err := raw.PutBatch([]archive.Record{sample("a", "s", "p", "poisoned evidence"), sample("b", "s", "p", "healthy evidence")}); err != nil {
				t.Fatal(err)
			}
			syncAll(t, e)
			checkpoint := bindingCheckpoint(t, e)
			f.fn = func(_ context.Context, _ embedding.Purpose, texts []string) ([][]float32, error) {
				if texts[0] != "poisoned evidence" {
					return [][]float32{{1, 0}}, nil
				}
				switch bad {
				case "error":
					return nil, errors.New("private provider detail must not persist")
				case "count":
					return nil, nil
				case "dimension":
					return [][]float32{{1}}, nil
				case "zero":
					return [][]float32{{0, 0}}, nil
				case "nan":
					return [][]float32{{float32(math.NaN()), 1}}, nil
				default:
					return [][]float32{{float32(math.Inf(1)), 1}}, nil
				}
			}
			if _, err := e.SyncSemantic(context.Background(), 1); !errors.Is(err, ErrSemanticUnavailable) {
				t.Fatal("invalid embedding was accepted", err)
			}
			if semanticCount(t, e, "semantic_vectors") != 0 || bindingCheckpoint(t, e) != checkpoint {
				t.Fatal("failed derived indexing published data or changed raw checkpoint")
			}
			if n, err := e.SyncSemantic(context.Background(), 1); err != nil || n != 1 {
				t.Fatal("poisoned retry blocked a later pending text", err)
			}
			status, err := e.semanticStatus(context.Background())
			if err != nil || status.ReadyTexts != 1 || status.PendingTexts != 1 || status.Failures != 1 || strings.Contains(status.LastError, "private") || status.CoverageComplete {
				t.Fatal("derived failure accounting or privacy contract changed", err)
			}
			if _, err := e.db.Exec(`UPDATE semantic_jobs SET attempts=7,next_attempt_ns=0 WHERE state='retry'`); err != nil {
				t.Fatal(err)
			}
			if _, err := e.SyncSemantic(context.Background(), 1); !errors.Is(err, ErrSemanticUnavailable) {
				t.Fatal(err)
			}
			status, err = e.semanticStatus(context.Background())
			if err != nil || status.FailedTexts != 1 || status.PendingTexts != 0 || status.CoverageComplete {
				t.Fatal("delayed failure was reported as complete coverage", err)
			}
			f.fn = nil
			if n, err := e.SyncSemantic(context.Background(), 1); err != nil || n != 0 {
				t.Fatal("failed job did not retain its bounded long backoff", err)
			}
			if _, err := e.db.Exec(`UPDATE semantic_jobs SET next_attempt_ns=0 WHERE state='failed'`); err != nil {
				t.Fatal(err)
			}
			if n, err := e.SyncSemantic(context.Background(), 1); err != nil || n != 1 {
				t.Fatal("service recovery could not retry a repeatedly failed job", err)
			}
			status, err = e.semanticStatus(context.Background())
			if err != nil || status.FailedTexts != 0 || status.ReadyTexts != 2 || !status.CoverageComplete {
				t.Fatal("service recovery did not restore complete coverage", err)
			}
		})
	}
}

func TestSemanticProfileDriftRejectsPublicationAndOldProfileIsNotReused(t *testing.T) {
	for _, change := range []string{"fingerprint", "dimensions"} {
		t.Run(change, func(t *testing.T) {
			f := newSemanticFake()
			e, raw, _ := semanticFixture(t, f)
			if err := raw.PutBatch([]archive.Record{sample("a", "s", "p", "profile evidence")}); err != nil {
				t.Fatal(err)
			}
			syncAll(t, e)
			f.fn = func(_ context.Context, _ embedding.Purpose, _ []string) ([][]float32, error) {
				f.mu.Lock()
				if change == "fingerprint" {
					f.fingerprint = "drifted-profile"
				} else {
					f.dimensions = 3
				}
				f.mu.Unlock()
				return [][]float32{{1, 0}}, nil
			}
			if _, err := e.SyncSemantic(context.Background(), 1); !errors.Is(err, ErrSemanticUnavailable) || semanticCount(t, e, "semantic_vectors") != 0 {
				t.Fatal("profile drift published vectors", err)
			}
			if _, err := e.Search(context.Background(), Query{Text: "profile", Mode: "hybrid"}); !errors.Is(err, ErrSemanticUnavailable) {
				t.Fatal("query accepted a drifted profile", err)
			}
		})
	}
	f := newSemanticFake()
	e, raw, path := semanticFixture(t, f)
	if err := raw.PutBatch([]archive.Record{sample("a", "s", "p", "profile evidence")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	syncSemanticAll(t, e)
	oldFingerprint := e.semantic.fingerprint
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	replacement := newSemanticFake()
	replacement.fingerprint = "explicit-new-model-profile"
	reopened, err := Open(path, storage.NewSQLite(raw), WithSemantic(replacement, 0.1))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result, err := reopened.Search(context.Background(), Query{Text: "paraphrase", Mode: "hybrid", ProjectID: "p"})
	if err != nil || len(result.Hits) != 0 || result.Retrieval == nil || result.Retrieval.SemanticComplete || result.Retrieval.Fingerprint == oldFingerprint || !result.Truncated {
		t.Fatal("new model silently reused old model's vectors", err)
	}
	syncSemanticAll(t, reopened)
	if replacement.documentCount() != 1 {
		t.Fatal("new model was not backfilled independently")
	}
}

func TestSemanticReclassificationAndSharedExcludedSurvivorsReleaseJobs(t *testing.T) {
	for _, ready := range []bool{false, true} {
		for _, action := range []string{"reclassify", "delete"} {
			t.Run(fmt.Sprintf("ready_%t_%s", ready, action), func(t *testing.T) {
				f := newSemanticFake()
				e, raw, _ := semanticFixture(t, f)
				if err := raw.PutBatch([]archive.Record{sample("eligible", "s", "p", "shared evidence"), sample("excluded", "s", "p", "shared evidence")}); err != nil {
					t.Fatal(err)
				}
				syncAll(t, e)
				if _, err := e.db.Exec(`UPDATE message_meta SET kind='context' WHERE request_id='excluded'`); err != nil {
					t.Fatal(err)
				}
				if ready {
					syncSemanticAll(t, e)
				}
				if action == "delete" {
					if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='eligible'`); err != nil {
						t.Fatal(err)
					}
					syncAll(t, e)
				} else {
					tx, err := e.db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					if _, err := tx.Exec(`UPDATE message_meta SET kind='image' WHERE request_id='eligible'`); err != nil {
						t.Fatal(err)
					}
					if err := e.queueSemanticSource(context.Background(), tx, "eligible"); err != nil {
						t.Fatal(err)
					}
					if err := tx.Commit(); err != nil {
						t.Fatal(err)
					}
				}
				if semanticCount(t, e, "semantic_jobs") != 0 || semanticCount(t, e, "semantic_vectors") != 0 {
					t.Fatal("excluded shared text stranded a job or retained a vector")
				}
				syncSemanticAll(t, e)
				result, err := e.Search(context.Background(), Query{Text: "paraphrase", Mode: "hybrid", ProjectID: "p"})
				if err != nil || len(result.Hits) != 0 || result.Retrieval == nil || !result.Retrieval.SemanticComplete {
					t.Fatal("excluded text reached semantic retrieval", err)
				}
			})
		}
	}
}

func TestHybridRefillsPastStaleSemanticOccurrence(t *testing.T) {
	for _, action := range []string{"delete", "replace", "move"} {
		t.Run(action, func(t *testing.T) {
			f := newSemanticFake()
			e, raw, _ := semanticFixture(t, f)
			older := sample("older", "s", "p", "historical accepted decision")
			leader := sample("leader", "s", "p", "historical accepted decision")
			leader.StartedAt = leader.StartedAt.Add(time.Hour)
			if err := raw.PutBatch([]archive.Record{older, leader}); err != nil {
				t.Fatal(err)
			}
			syncAll(t, e)
			syncSemanticAll(t, e)
			switch action {
			case "delete":
				if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='leader'`); err != nil {
					t.Fatal(err)
				}
			case "replace":
				leader.Response = []byte(`{"choices":[{"message":{"role":"assistant","content":"revised response"}}]}`)
				if err := raw.PutBatch([]archive.Record{leader}); err != nil {
					t.Fatal(err)
				}
			case "move":
				if _, err := raw.DB.Exec(`UPDATE records SET facets_json='{"project.id":["foreign"],"client":["codex"]}' WHERE request_id='leader'`); err != nil {
					t.Fatal(err)
				}
			}
			result, err := e.Search(context.Background(), Query{Text: "paraphrase", ProjectID: "p", Mode: "hybrid", Limit: 1})
			if err != nil || len(result.Hits) != 1 || result.Hits[0].Citation.RequestID != "older" {
				t.Fatal("stale leading occurrence hid current evidence", err)
			}
			checkDiverseCitations(t, e, result)
		})
	}
}

func TestHybridPartialCoverageAndProviderFailureAreExplicit(t *testing.T) {
	f := newSemanticFake()
	e, raw, _ := semanticFixture(t, f)
	if err := raw.PutBatch([]archive.Record{sample("a", "s", "p", "literalneedle accepted decision"), sample("b", "s", "foreign", "foreign evidence")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	q := Query{Text: "literalneedle", ProjectID: "p", Mode: "hybrid"}
	result, err := e.Search(context.Background(), q)
	if err != nil || len(result.Hits) != 1 || result.Retrieval == nil || result.Retrieval.SemanticComplete || !result.Truncated {
		t.Fatal("partial index was hidden behind lexical success", err)
	}
	if n, err := e.SyncSemantic(context.Background(), 1); err != nil || n != 1 {
		t.Fatal(err)
	}
	result, err = e.Search(context.Background(), q)
	if err != nil || result.Retrieval == nil || !result.Retrieval.SemanticComplete {
		t.Fatal("foreign pending jobs contaminated scoped completeness", err)
	}
	f.fn = func(context.Context, embedding.Purpose, []string) ([][]float32, error) {
		return nil, errors.New("unavailable")
	}
	if _, err := e.Search(context.Background(), q); !errors.Is(err, ErrSemanticUnavailable) {
		t.Fatal("query embedding failure silently fell back to literal results", err)
	}
	q.Mode = "literal"
	if result, err := e.Search(context.Background(), q); err != nil || len(result.Hits) != 1 {
		t.Fatal("optional embedding outage broke literal search", err)
	}
	f.fn = func(ctx context.Context, _ embedding.Purpose, _ []string) ([][]float32, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	q.Mode = "hybrid"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := e.Search(ctx, q); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("query cancellation became empty or partial success", err)
	}
}

func TestHybridSemanticSnippetUsesExactScoredRuneWindow(t *testing.T) {
	f := newSemanticFake()
	f.fn = func(_ context.Context, purpose embedding.Purpose, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i, text := range texts {
			out[i] = []float32{0, 1}
			if purpose == embedding.Query || strings.Contains(text, "答案") {
				out[i] = []float32{1, 0}
			}
		}
		return out, nil
	}
	e, raw, _ := semanticFixture(t, f)
	text := strings.Repeat("中😀", 300) + "答案" + strings.Repeat("尾", 200)
	if err := raw.PutBatch([]archive.Record{sample("a", "s", "p", text)}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	syncSemanticAll(t, e)
	result, err := e.Search(context.Background(), Query{Text: "paraphrase", ProjectID: "p", Mode: "hybrid"})
	if err != nil || len(result.Hits) != 1 {
		t.Fatal("semantic-only evidence was filtered by literal contains", err)
	}
	hit := result.Hits[0]
	if hit.Citation.Start != 288 || hit.Citation.End != 672 || hit.Snippet != string([]rune(text)[288:672]) {
		t.Fatal("semantic snippet did not preserve scored rune offsets")
	}
	checkDiverseCitations(t, e, result)
}

func TestSemanticTopKBudgetIsDistinctTextAndReportsTruncation(t *testing.T) {
	f := newSemanticFake()
	e, raw, _ := semanticFixture(t, f)
	records := make([]archive.Record, 102)
	for i := range records {
		records[i] = sample(fmt.Sprintf("text-%03d", i), "s", "p", fmt.Sprintf("independent evidence %03d", i))
	}
	diverseFixture(t, e, raw, records)
	syncSemanticAll(t, e)
	matches, complete, truncated, err := e.semanticMatches(context.Background(), Query{ProjectID: "p"}, []float32{1, 0}, 100)
	if err != nil || len(matches) != 100 || !complete || !truncated {
		t.Fatal("semantic unique-text budget is not explicit", err)
	}
	result, err := e.Search(context.Background(), Query{Text: "paraphrase", ProjectID: "p", Mode: "hybrid", Limit: 100})
	if err != nil || len(result.Hits) != 100 || !result.Truncated || result.Retrieval == nil || !result.Retrieval.SemanticComplete {
		t.Fatal("semantic budget exhaustion was reported as exhaustive", err)
	}
}

func TestHybridFusionRetainsLiteralChecksAndMessageRounds(t *testing.T) {
	q := Query{Text: "error/code", Mode: "hybrid"}
	makeCandidate := func(id, text string, start int) candidate {
		return candidate{hit: Hit{Role: "user", Kind: "text", Citation: Citation{RequestID: id, Side: "original_request", Pointer: "/messages/0/content", Start: start}}, text: text}
	}
	lexical := []candidate{makeCandidate("invalid-literal", "error code", 0)}
	semantic := []candidate{}
	matches := []semanticMatch{}
	for i := 0; i < 1002; i++ {
		c := makeCandidate("one-message", fmt.Sprintf("window %d", i), i*1344)
		c.semantic = true
		semantic = append(semantic, c)
		matches = append(matches, semanticMatch{hash: semanticTextHash(c.text)})
	}
	independent := makeCandidate("independent", "separate evidence", 0)
	independent.semantic = true
	semantic = append(semantic, independent)
	matches = append(matches, semanticMatch{hash: semanticTextHash(independent.text)})
	fused := fuseSemantic(q, lexical, semantic, matches)
	if len(fused) != len(semantic) || fused[0].hit.Citation.RequestID != "one-message" || fused[1].hit.Citation.RequestID != "independent" {
		t.Fatal("fusion skipped literal verification or lost message fairness")
	}
}
