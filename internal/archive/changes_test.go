package archive

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestMemoryFeedTracksCommittedChangesAndRevisions(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	ctx := context.Background()
	record := Record{RequestID: "r", SessionID: "s", StartedAt: time.Now().UTC(), OriginalRequest: []byte(`{"messages":[{"role":"user","content":"original"}]}`), Facets: map[string][]string{"project.id": {"p"}}}
	if err := s.PutBatch([]Record{record}); err != nil {
		t.Fatal(err)
	}
	first, err := s.MemorySource(ctx, "r")
	if err != nil || len(first.Revision) != 64 {
		t.Fatalf("%+v %v", first, err)
	}
	changes, err := s.MemoryChanges(ctx, 0, 10)
	if err != nil || len(changes) != 1 || changes[0].RequestID != "r" {
		t.Fatalf("%+v %v", changes, err)
	}
	page, err := s.MemoryBackfill(ctx, 0, 10)
	if err != nil || !page.Done || len(page.IDs) != 1 || page.IDs[0] != "r" {
		t.Fatalf("%+v %v", page, err)
	}
	before, err := s.MemoryWatermark(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tx.Exec(`UPDATE records SET session_id='rolledback' WHERE request_id='r'`)
	tx.Rollback()
	after, err := s.MemoryWatermark(ctx)
	if err != nil || before != after {
		t.Fatalf("rollback emitted change %d %d %v", before, after, err)
	}
	record.Facets["project.id"] = []string{"q"}
	record.Response = []byte(`{"choices":[{"message":{"role":"assistant","content":"changed"}}]}`)
	if err := s.PutBatch([]Record{record}); err != nil {
		t.Fatal(err)
	}
	second, err := s.MemorySource(ctx, "r")
	if err != nil || second.Revision == first.Revision || second.Sequence <= first.Sequence {
		t.Fatalf("%+v %v", second, err)
	}
	if _, err := s.DB.Exec(`DELETE FROM records WHERE request_id='r'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MemorySource(ctx, "r"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	changes, err = s.MemoryChanges(ctx, before, 10)
	if err != nil || len(changes) != 2 {
		t.Fatalf("%+v %v", changes, err)
	}
}
