package memory

import (
	"context"
	"testing"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

func TestAggregateRecoveryAfterLegacyWriterLosesTriggers(t *testing.T) {
	e, raw, path := testEngine(t)
	if err := raw.PutBatch([]archive.Record{sample("one", "s", "p", "evidence")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	if _, err := e.db.Exec(`DROP TRIGGER corpus_sources_insert;
 DROP TRIGGER coverage_sources_insert;
 INSERT INTO sources SELECT 'untracked',revision,sequence,project_id,client,session_id,event_at,event_ns,partial,issues_text FROM sources WHERE request_id='one'`); err != nil {
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
	status, err := reopened.Status(context.Background())
	if err != nil || status.Sources != 2 || status.Coverage.SourcesWithIssues != 2 || status.Coverage.UnexplainedPartialSources != 0 {
		t.Fatalf("legacy aggregates were not recovered: %+v %v", status, err)
	}
	if _, err := reopened.db.Exec(`INSERT INTO sources SELECT 'tracked',revision,sequence,project_id,client,session_id,event_at,event_ns,partial,issues_text FROM sources WHERE request_id='one'`); err != nil {
		t.Fatal(err)
	}
	assertCorpusStats(t, reopened)
	status, err = reopened.Status(context.Background())
	if err != nil || status.Sources != 3 || status.Coverage.SourcesWithIssues != 3 {
		t.Fatalf("recovered triggers did not track new writes: %+v %v", status, err)
	}
}
