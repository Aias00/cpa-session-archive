package memory

import (
	"context"
	"testing"

	"cpa-session-archive/internal/archive"
	sqlite3 "github.com/mattn/go-sqlite3"
)

func TestStatusUsesOneSnapshotAcrossCountsAndCoverage(t *testing.T) {
	e, raw, _ := testEngine(t)
	if err := raw.PutBatch([]archive.Record{sample("one", "s", "p", "evidence")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	if _, err := e.db.Exec(`ALTER TABLE corpus_stats RENAME TO corpus_stats_data;
 CREATE VIEW corpus_stats AS SELECT id,sources,chunks,partial_sources,unique_chunks+coverage_probe() AS unique_chunks FROM corpus_stats_data`); err != nil {
		t.Fatal(err)
	}
	e.readDB.SetMaxOpenConns(1)
	e.readDB.SetMaxIdleConns(1)
	connection, err := e.readDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	updated := false
	var updateErr error
	err = connection.Raw(func(driver any) error {
		return driver.(*sqlite3.SQLiteConn).RegisterFunc("coverage_probe", func() int {
			if !updated {
				updated = true
				_, updateErr = e.db.Exec(`UPDATE sources SET issues_text='opaque reasoning omitted' WHERE request_id='one'`)
			}
			return 0
		}, false)
	})
	connection.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, err := e.Status(context.Background())
	if err != nil || updateErr != nil || !updated {
		t.Fatalf("concurrent probe failed: %v %v updated=%v", err, updateErr, updated)
	}
	if before.Coverage.NeedsInvestigationSources != 1 || before.Coverage.ExpectedOmissionsOnlySources != 0 {
		t.Fatal("status mixed old counts with newer coverage", before.Coverage)
	}
	after, err := e.Status(context.Background())
	if err != nil || after.Coverage.NeedsInvestigationSources != 0 || after.Coverage.ExpectedOmissionsOnlySources != 1 {
		t.Fatalf("next snapshot did not observe the commit: %+v %v", after.Coverage, err)
	}
}
