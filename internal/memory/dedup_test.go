package memory

import (
	"context"
	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
	"testing"
)

func TestSharedTextPreservesOccurrencesScopesAndCitations(t *testing.T) {
	e, raw, path := testEngine(t)
	ctx := context.Background()
	a := sample("a", "session-a", "p", "重复证据")
	a.OriginalRequest = []byte(`{"messages":[{"role":"user","content":"重复证据"},{"role":"user","content":"重复证据"}]}`)
	b := sample("b", "session-b", "q", "重复证据")
	if err := raw.PutBatch([]archive.Record{a, b}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	status, err := e.Status(ctx)
	if err != nil || status.Chunks != 3 || status.UniqueChunks != 1 || status.DeduplicatedChunks != 2 {
		t.Fatalf("no physical dedup: %+v %v", status, err)
	}
	p, err := e.Search(ctx, Query{Text: "重复", ProjectID: "p"})
	if err != nil || len(p.Hits) != 2 {
		t.Fatalf("distinct messages lost %+v %v", p, err)
	}
	q, err := e.Search(ctx, Query{Text: "重复", ProjectID: "q"})
	if err != nil || len(q.Hits) != 1 || q.Hits[0].Citation.RequestID != "b" {
		t.Fatalf("scope leak %+v %v", q, err)
	}
	if p.Hits[0].Citation.Pointer == p.Hits[1].Citation.Pointer {
		t.Fatal("two occurrences merged")
	}
	citation := q.Hits[0].Citation
	e.Close()
	reopened, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.ReadSource(ctx, citation); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='a'`); err != nil {
		t.Fatal(err)
	}
	syncAll(t, reopened)
	status, _ = reopened.Status(ctx)
	if status.UniqueChunks != 1 || status.Chunks != 1 {
		t.Fatal(status)
	}
	if _, err := reopened.ReadSource(ctx, citation); err != nil {
		t.Fatal("other project's source deleted", err)
	}
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='b'`); err != nil {
		t.Fatal(err)
	}
	syncAll(t, reopened)
	status, _ = reopened.Status(ctx)
	var indexed int
	reopened.db.QueryRow(`SELECT COUNT(*) FROM chunk_search`).Scan(&indexed)
	if status.UniqueChunks != 0 || status.Chunks != 0 || indexed != 0 {
		t.Fatalf("orphan text/FTS remains %+v indexed=%d", status, indexed)
	}
}
func TestSharedTextMigratesProjectionV2WithoutChangingNormalizer(t *testing.T) {
	e, raw, path := testEngine(t)
	ctx := context.Background()
	raw.PutBatch([]archive.Record{sample("a", "s", "p", "原始证据")})
	syncAll(t, e)
	hit, _ := e.Search(ctx, Query{Text: "证据"})
	citation := hit.Hits[0].Citation
	e.db.Exec(`PRAGMA user_version=2`)
	e.Close()
	upgraded, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	syncAll(t, upgraded)
	status, _ := upgraded.Status(ctx)
	if status.UniqueChunks != 1 {
		t.Fatal(status)
	}
	source, err := upgraded.ReadSource(ctx, citation)
	if err != nil || source.Text != hit.Hits[0].Snippet || citation.NormalizerVersion != 2 {
		t.Fatalf("citation reinterpreted: %+v %v", source, err)
	}
}
