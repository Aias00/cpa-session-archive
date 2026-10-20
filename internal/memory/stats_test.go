package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

func assertCorpusStats(t *testing.T, e *Engine) {
	t.Helper()
	s, err := e.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sources, chunks, partial, unique int64
	err = e.db.QueryRow(`SELECT (SELECT COUNT(*) FROM sources),
 (SELECT COUNT(*) FROM chunks),(SELECT COUNT(*) FROM sources WHERE partial=1),
 (SELECT COUNT(*) FROM chunk_texts)`).Scan(&sources, &chunks, &partial, &unique)
	if err != nil {
		t.Fatal(err)
	}
	if s.Sources != sources || s.Chunks != chunks || s.PartialSources != partial || s.UniqueChunks != unique {
		t.Fatalf("aggregate mismatch: %+v; actual %d/%d/%d/%d", s, sources, chunks, partial, unique)
	}
}

func TestRecallReadsDuringProjectionTransaction(t *testing.T) {
	e, raw, _ := testEngine(t)
	if err := raw.PutBatch([]archive.Record{sample("a", "s", "p", "concurrent marker")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE state SET failures=999 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	// The only writer connection remains occupied until this test returns.
	// A reader sharing it cannot finish even though WAL permits this read.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := e.Search(ctx, Query{Text: "marker", ProjectID: "p"})
	if err != nil || len(result.Hits) != 1 || result.Status.Failures != 0 {
		t.Fatalf("read blocked or observed uncommitted state: %+v %v", result, err)
	}
	if _, err := e.readDB.ExecContext(ctx, `UPDATE state SET failures=1`); err == nil {
		t.Fatal("reader pool accepted a write")
	}
}

func TestProjectionPathURICharacters(t *testing.T) {
	dir := t.TempDir()
	raw, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.DB.Close()
	e, err := Open(filepath.Join(dir, "memory ?#%.sqlite"), storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	assertCorpusStats(t, e)
}

func TestCorpusStatsLifecycle(t *testing.T) {
	e, raw, path := testEngine(t)
	a := sample("a", "s", "p", "shared text")
	b := sample("b", "s", "p", "shared text")
	if err := raw.PutBatch([]archive.Record{a, b}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	assertCorpusStats(t, e)
	// Rollback must undo cascades and aggregate updates together.
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM sources`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCorpusStats(t, e)
	a.OriginalRequest = []byte(`{"messages":[{"role":"user","content":"changed text"}]}`)
	a.Response = []byte(`{"choices":[{"message":{"role":"assistant","content":"complete"}}]}`)
	if err := raw.PutBatch([]archive.Record{a}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	assertCorpusStats(t, e)
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='b'`); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	assertCorpusStats(t, e)
	// Simulate an existing v3 corpus without the additive aggregate migration.
	for _, name := range []string{"corpus_sources_insert", "corpus_sources_delete", "corpus_sources_update", "corpus_chunks_insert", "corpus_chunks_delete", "corpus_text_insert", "corpus_text_delete"} {
		if _, err := e.db.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.db.Exec(`DROP TABLE corpus_stats`); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertCorpusStats(t, reopened)
	if _, err := reopened.db.Exec(`PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.Close()
	assertCorpusStats(t, rebuilt)
	syncAll(t, rebuilt)
	assertCorpusStats(t, rebuilt)
}
