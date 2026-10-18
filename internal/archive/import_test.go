package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImportSnapshotPreservesTargetAndBuildsProjections(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sqlite")
	targetPath := filepath.Join(dir, "target.sqlite")
	source, err := OpenStore(sourcePath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer source.DB.Close()
	target, err := OpenStore(targetPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer target.DB.Close()
	now := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	records := []Record{
		{RequestID: "shared", SessionID: "s", StartedAt: now, CompletedAt: now, OriginalRequest: []byte(`{"input":"source-shared"}`)},
		{RequestID: "missing", SessionID: "s", StartedAt: now, CompletedAt: now, OriginalRequest: []byte(`{"input":"` + strings.Repeat("nested", 12000) + `"}`), UpstreamRequest: []byte(`{"input":"retained upstream"}`), Response: []byte(`{"output":[]}`), Summary: "legacy summary", Facets: map[string][]string{"project.id": {"p"}, "project.name": {"project"}, "turn.id": {"turn-1"}, "tool.name": {"read_file"}}},
	}
	if err := source.PutBatch(records); err != nil {
		t.Fatal(err)
	}
	own := records[0]
	own.OriginalRequest = []byte(`{"input":"keep-target"}`)
	if err := target.PutBatch([]Record{own}); err != nil {
		t.Fatal(err)
	}
	var fence int64
	target.DB.QueryRow(`SELECT sequence FROM archive_ingest_clock WHERE id=1`).Scan(&fence)
	report, err := target.ImportSnapshot(context.Background(), sourcePath, 8)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 1 || report.Existing != 1 || report.Conflicts != 1 || report.Remaining != 0 {
		t.Fatalf("%+v", report)
	}
	existing, err := target.Request(context.Background(), "shared")
	if err != nil || !strings.Contains(string(existing.OriginalRequest), "keep-target") {
		t.Fatalf("target overwritten: %+v %v", existing, err)
	}
	got, err := target.Request(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.OriginalRequest) != string(records[1].OriginalRequest) || string(got.UpstreamRequest) != string(records[1].UpstreamRequest) || got.Summary != "legacy summary" {
		t.Fatal("payload or metadata lost")
	}
	for _, query := range []string{`SELECT COUNT(*) FROM turn_records WHERE request_id='missing' AND turn_id='turn-1'`, `SELECT COUNT(*) FROM record_facets WHERE request_id='missing' AND name='project.id' AND value='p'`, `SELECT COUNT(*) FROM session_facets WHERE session_id='s' AND name='project.id' AND value='p'`, `SELECT COUNT(*) FROM session_indexed_requests WHERE request_id='missing'`, `SELECT COUNT(*) FROM archive_ingest_events WHERE sequence>? AND request_id='missing'`} {
		var n int
		var args []any
		if strings.Contains(query, "?") {
			args = []any{fence}
		}
		if err := target.DB.QueryRow(query, args...).Scan(&n); err != nil || n != 1 {
			t.Fatalf("projection %s: n=%d %v", query, n, err)
		}
	}
	var requests int
	target.DB.QueryRow(`SELECT requests FROM session_summaries WHERE session_id='s'`).Scan(&requests)
	if requests != 2 {
		t.Fatal(requests)
	}
	again, err := target.ImportSnapshot(context.Background(), sourcePath, 8)
	if err != nil || again.Imported != 0 || again.Existing != 2 || again.Remaining != 0 {
		t.Fatalf("not idempotent: %+v %v", again, err)
	}
	var after int64
	target.DB.QueryRow(`SELECT sequence FROM archive_ingest_clock WHERE id=1`).Scan(&after)
	if after != fence+1 {
		t.Fatalf("duplicate import emitted events: %d %d", fence, after)
	}
}

func TestImportSnapshotRollsBackBatchAndResumes(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sqlite")
	source, err := OpenStore(sourcePath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.DB.Close()
	target, err := OpenStore(filepath.Join(dir, "target.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer target.DB.Close()
	if err := source.PutBatch([]Record{{RequestID: "a", SessionID: "s"}, {RequestID: "b", SessionID: "s"}}); err != nil {
		t.Fatal(err)
	}
	target.DB.Exec(`CREATE TRIGGER reject_b BEFORE INSERT ON records WHEN NEW.request_id='b' BEGIN SELECT RAISE(FAIL,'test failure'); END`)
	if _, err := target.ImportSnapshot(context.Background(), sourcePath, 8); err == nil {
		t.Fatal("expected failed batch")
	}
	for _, table := range []string{"records", "turn_records", "session_summaries", "archive_ingest_events"} {
		var n int
		target.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
		if n != 0 {
			t.Fatalf("partial batch persisted in %s", table)
		}
	}
	target.DB.Exec(`DROP TRIGGER reject_b`)
	report, err := target.ImportSnapshot(context.Background(), sourcePath, 1)
	if err != nil || report.Imported != 2 || report.Remaining != 0 {
		t.Fatalf("%+v %v", report, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := target.ImportSnapshot(ctx, sourcePath, 1); err == nil {
		t.Fatal("canceled import succeeded")
	}
	if _, err := target.ImportSnapshot(context.Background(), target.DBPath, 1); err == nil {
		t.Fatal("self import allowed")
	}
}

func TestImportSnapshotRejectsCorruptCASAndHandlesEscapedPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source archive.sqlite")
	source, err := OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.DB.Close()
	target, err := OpenStore(filepath.Join(dir, "target.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer target.DB.Close()
	if err := source.PutBatch([]Record{{RequestID: "a", SessionID: "s", OriginalRequest: []byte(`{"input":"source text"}`)}}); err != nil {
		t.Fatal(err)
	}
	valid, err := target.ImportSnapshot(context.Background(), path, 8)
	if err != nil || valid.Imported != 1 {
		t.Fatalf("space-containing path failed: %+v %v", valid, err)
	}
	if _, err := target.DB.Exec(`DELETE FROM records`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.Exec(`UPDATE blobs SET data=?`, gzipBytes([]byte("wrong hash"))); err != nil {
		t.Fatal(err)
	}
	if err := source.DB.Close(); err != nil {
		t.Fatal(err)
	}
	escaped := filepath.Join(dir, "source archive ?#.sqlite")
	if err := os.Rename(path, escaped); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ImportSnapshot(context.Background(), escaped, 8); err == nil {
		t.Fatal("corrupt source imported")
	}
	var n int
	target.DB.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&n)
	if n != 0 {
		t.Fatal("corrupt record became visible")
	}
}

func TestImportSnapshotRangeExportsOnlyCommittedDelta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.sqlite")
	source, err := OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.DB.Close()
	stamp := time.Now().UTC()
	record := func(id string) Record {
		return Record{RequestID: id, SessionID: "s", StartedAt: stamp, CompletedAt: stamp, OriginalRequest: []byte(`{"input":"` + id + `"}`)}
	}
	source.PutBatch([]Record{record("old")})
	after, _ := source.MemoryWatermark(context.Background())
	source.PutBatch([]Record{record("new")})
	through, _ := source.MemoryWatermark(context.Background())
	source.PutBatch([]Record{record("future")})
	target, err := OpenStore(filepath.Join(dir, "delta.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer target.DB.Close()
	report, err := target.ImportSnapshotRange(context.Background(), path, 8, after, through)
	if err != nil || report.SourceRecords != 1 || report.Imported != 1 || report.Remaining != 0 {
		t.Fatalf("range %+v %v", report, err)
	}
	var n int
	target.DB.QueryRow("select count(*) from records where request_id='new'").Scan(&n)
	if n != 1 {
		t.Fatal("new record missing")
	}
	target.DB.QueryRow("select count(*) from records").Scan(&n)
	if n != 1 {
		t.Fatal("unselected source leaked")
	}
	report, err = target.ImportSnapshotRange(context.Background(), path, 8, after, through)
	if err != nil || report.Imported != 0 || report.Existing != 1 {
		t.Fatalf("replay %+v %v", report, err)
	}
	if _, err = target.ImportSnapshotRange(context.Background(), path, 8, through, after); err == nil {
		t.Fatal("invalid range accepted")
	}
	if _, err = target.ImportSnapshotRange(context.Background(), path, 8, after, through+100); err == nil {
		t.Fatal("future watermark accepted")
	}
}
