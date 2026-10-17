package memory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

func testEngine(t *testing.T) (*Engine, *archive.Store, string) {
	t.Helper()
	dir := t.TempDir()
	raw, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.DB.Close() })
	path := filepath.Join(dir, "memory.sqlite")
	e, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, raw, path
}
func sample(id, session, project, text string) archive.Record {
	return archive.Record{RequestID: id, SessionID: session, SourceFormat: "openai", StartedAt: time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 10, 17, 1, 0, 1, 0, time.UTC), Outcome: "success", Facets: map[string][]string{"project.id": {project}, "client": {"codex"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":` + quote(text) + `}]}`)}
}
func quote(s string) string { // Test fixture escaping is intentionally independent of production parsing.
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}
func syncAll(t *testing.T, e *Engine) {
	t.Helper()
	for i := 0; i < 100; i++ {
		n, err := e.Sync(context.Background(), 2)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	t.Fatal("sync did not converge")
}
func TestRecallChineseSourcesAndScope(t *testing.T) {
	e, raw, _ := testEngine(t)
	ctx := context.Background()
	a := sample("a", "s", "p", "采用内置记忆存储，修复 /tmp/a.go 中 SQLITE_BUSY 错误")
	b := sample("b", "other", "q", "采用内置记忆存储，修复 /tmp/a.go 中 SQLITE_BUSY 错误")
	if err := raw.PutBatch([]archive.Record{a, b}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	for _, query := range []string{"记忆", "忆", "内置记忆存储", "/tmp/a.go", "SQLITE_BUSY"} {
		r, err := e.Search(ctx, Query{Text: query, ProjectID: "p"})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Hits) != 1 || r.Hits[0].Citation.RequestID != "a" {
			t.Fatalf("%q: %+v", query, r)
		}
		source, err := e.ReadSource(ctx, r.Hits[0].Citation)
		if err != nil {
			t.Fatal(err)
		}
		if source.Text != r.Hits[0].Snippet {
			t.Fatalf("citation mismatch %q / %q", source.Text, r.Hits[0].Snippet)
		}
	}
	if _, err := e.Search(ctx, Query{Text: "x", After: "garbage"}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
	if _, err := e.Search(ctx, Query{Text: ""}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
}
func TestRecallImmediatelyHidesChangedDeletedOrMovedSources(t *testing.T) {
	e, raw, _ := testEngine(t)
	ctx := context.Background()
	a := sample("a", "s", "p", "原始决定")
	raw.PutBatch([]archive.Record{a})
	syncAll(t, e)
	r, _ := e.Search(ctx, Query{Text: "决定"})
	if len(r.Hits) != 1 {
		t.Fatal(r)
	}
	old := r.Hits[0].Citation
	a.Facets["project.id"] = []string{"other"}
	a.Response = []byte(`{"choices":[{"message":{"role":"assistant","content":"修正决定"}}]}`)
	if err := raw.PutBatch([]archive.Record{a}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.DB.Exec(`UPDATE records SET facets_json='{"project.id":["other"],"client":["codex"]}' WHERE request_id='a'`); err != nil {
		t.Fatal(err)
	}
	r, err := e.Search(ctx, Query{Text: "决定", ProjectID: "p"})
	if err != nil || len(r.Hits) != 0 {
		t.Fatalf("stale result %+v %v", r, err)
	}
	if _, err := e.ReadSource(ctx, old); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("old source: %v", err)
	}
	syncAll(t, e)
	r, _ = e.Search(ctx, Query{Text: "修正", ProjectID: "other"})
	if len(r.Hits) != 1 {
		t.Fatal(r)
	}
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='a'`); err != nil {
		t.Fatal(err)
	}
	r, err = e.Search(ctx, Query{Text: "修正"})
	if err != nil || len(r.Hits) != 0 {
		t.Fatalf("deleted: %+v %v", r, err)
	}
	syncAll(t, e)
	status, _ := e.Status(ctx)
	if status.Sources != 0 {
		t.Fatal(status)
	}
}
func TestRecallCheckpointReplayAndHistoricalBackfill(t *testing.T) {
	e, raw, path := testEngine(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if err := raw.PutBatch([]archive.Record{sample(id, "s", "p", "历史内容 "+id)}); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a pre-change-log archive. Historical backfill must not depend on events.
	if _, err := raw.DB.Exec(`DELETE FROM archive_ingest_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Sync(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	syncAll(t, resumed)
	for i := 0; i < 10; i++ {
		if err := raw.PutBatch([]archive.Record{sample("a", "s", "p", "历史内容 a")}); err != nil {
			t.Fatal(err)
		}
	}
	syncAll(t, resumed)
	status, err := resumed.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.BackfillDone || status.Sources != 3 || status.Chunks != 3 || status.Lag != 0 {
		t.Fatal(status)
	}
}
func TestRecallLongContentPartialAndRepeatedMessages(t *testing.T) {
	e, raw, _ := testEngine(t)
	ctx := context.Background()
	a := sample("a", "s", "p", strings.Repeat("正文", 25000)+"唯一终点")
	a.Truncated = true
	b := sample("b", "s", "p", "确认")
	b.OriginalRequest = []byte(`{"messages":[{"role":"user","content":"确认"},{"role":"user","content":"确认"}]}`)
	if err := raw.PutBatch([]archive.Record{a, b}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	r, err := e.Search(ctx, Query{Text: "唯一终点"})
	if err != nil || len(r.Hits) != 1 || !r.Hits[0].Partial {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = e.Search(ctx, Query{Text: "确认"})
	if err != nil || len(r.Hits) != 2 {
		t.Fatalf("distinct user messages lost: %+v %v", r, err)
	}
	c := r.Hits[0].Citation
	c.End = 999999
	if _, err := e.ReadSource(ctx, c); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal(err)
	}
}
func TestRecapRequiresScopeAndDoesNotInventClaims(t *testing.T) {
	e, raw, _ := testEngine(t)
	ctx := context.Background()
	raw.PutBatch([]archive.Record{sample("a", "s", "p", "验证失败，尚未修复")})
	syncAll(t, e)
	if _, err := e.Recap(ctx, Query{}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
	r, err := e.Recap(ctx, Query{ProjectID: "p"})
	if err != nil || !strings.Contains(r.Text, "验证失败") || len(r.Hits) != 1 || len([]byte(r.Text)) > 8192 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestFailedProjectionDoesNotAdvanceCheckpoint(t *testing.T) {
	e, raw, _ := testEngine(t)
	ctx := context.Background()
	syncAll(t, e)
	raw.PutBatch([]archive.Record{sample("a", "s", "p", "恢复")})
	before, _ := e.Status(ctx)
	if _, err := e.db.Exec(`CREATE TRIGGER reject_chunks BEFORE INSERT ON chunks BEGIN SELECT RAISE(FAIL,'test unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Sync(ctx, 10); err == nil {
		t.Fatal("expected failed projection")
	}
	after, _ := e.Status(ctx)
	if after.Checkpoint != before.Checkpoint || after.Failures != 1 {
		t.Fatal(after)
	}
	e.db.Exec(`DROP TRIGGER reject_chunks`)
	syncAll(t, e)
	r, err := e.Search(ctx, Query{Text: "恢复"})
	if err != nil || len(r.Hits) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRecallTimeAndClientFiltersAndLiteralOperators(t *testing.T) {
	e, raw, _ := testEngine(t)
	ctx := context.Background()
	a := sample("a", "s", "p", "failed /tmp/scope.go OR literal")
	b := sample("b", "s", "p", "failed /tmp/scope.go OR literal")
	b.StartedAt = b.StartedAt.Add(time.Hour)
	b.Facets["client"] = []string{"claude"}
	if err := raw.PutBatch([]archive.Record{a, b}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	cases := []struct {
		q    Query
		want string
	}{
		{Query{Text: "scope.go", Client: "codex"}, "a"},
		{Query{Text: "scope.go", After: "2026-10-17T02:00:00Z"}, "b"},
		{Query{Text: "scope.go", Before: "2026-10-17T02:00:00Z"}, "a"},
		{Query{Text: `OR "literal"`}, ""},
	}
	for _, c := range cases {
		r, err := e.Search(ctx, c.q)
		if err != nil {
			t.Fatal(err)
		}
		if c.want == "" {
			if len(r.Hits) != 0 {
				t.Fatal(r)
			}
		} else if len(r.Hits) != 1 || r.Hits[0].Citation.RequestID != c.want {
			t.Fatalf("%+v => %+v", c.q, r)
		}
	}
}
func TestRecallDoesNotBindProjectByFolderNameAndExcludesInternalCalls(t *testing.T) {
	e, raw, _ := testEngine(t)
	ctx := context.Background()
	a := sample("a", "s", "", "工作内容")
	a.Facets = map[string][]string{"project.name": {"same-name"}, "project.path": {"/tmp/same-name"}}
	b := sample("b", "s", "p", "内部提取内容")
	b.Facets["request.kind"] = []string{"memory-extraction"}
	if err := raw.PutBatch([]archive.Record{a, b}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	r, err := e.Search(ctx, Query{Text: "内容"})
	if err != nil || len(r.Hits) != 1 || r.Hits[0].ProjectID != "" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = e.Search(ctx, Query{Text: "内容", ProjectID: "same-name"})
	if err != nil || len(r.Hits) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRecallRebuildsEarlierNormalizerProjection(t *testing.T) {
	e, raw, path := testEngine(t)
	ctx := context.Background()
	if err := raw.PutBatch([]archive.Record{sample("a", "s", "p", "parser upgrade")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	if _, err := e.db.Exec(`PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	e.Close()
	upgraded, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	status, err := upgraded.Status(ctx)
	if err != nil || status.BackfillDone || status.Sources != 0 {
		t.Fatalf("old projection was retained: %+v %v", status, err)
	}
	syncAll(t, upgraded)
	result, err := upgraded.Search(ctx, Query{Text: "upgrade"})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Citation.NormalizerVersion != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	old := result.Hits[0].Citation
	old.NormalizerVersion = 1
	if _, err := upgraded.ReadSource(ctx, old); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("old parser citation reinterpreted: %v", err)
	}
}

func TestRecallUnchangedRevisionAdvancesWithoutRewritingChunks(t *testing.T) {
	e, raw, _ := testEngine(t)
	ctx := context.Background()
	record := sample("a", "s", "p", "unchanged revision")
	if err := raw.PutBatch([]archive.Record{record}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	if _, err := e.db.Exec(`CREATE TRIGGER reject_rewrite BEFORE INSERT ON chunks BEGIN SELECT RAISE(FAIL,'unchanged chunks rewritten'); END`); err != nil {
		t.Fatal(err)
	}
	if err := raw.PutBatch([]archive.Record{record}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	status, err := e.Status(ctx)
	if err != nil || status.Lag != 0 || status.Failures != 0 {
		t.Fatalf("%+v %v", status, err)
	}
}
