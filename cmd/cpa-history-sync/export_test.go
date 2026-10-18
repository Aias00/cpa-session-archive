package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestDeltaExportIsIndependentAndDoesNotClobber(t *testing.T) {
	dir := t.TempDir()
	source, err := archive.OpenStore(filepath.Join(dir, "source.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.DB.Close()
	source.PutBatch([]archive.Record{{RequestID: "old", SessionID: "s"}})
	after, _ := source.MemoryWatermark(context.Background())
	source.PutBatch([]archive.Record{{RequestID: "new", SessionID: "s", OriginalRequest: []byte(`{"input":"new"}`)}})
	through, _ := source.MemoryWatermark(context.Background())
	path := filepath.Join(dir, "delta.sqlite")
	var output bytes.Buffer
	if err := exportCommitted(context.Background(), source, path, after, through, &output); err != nil {
		t.Fatal(err)
	}
	if err := exportCommitted(context.Background(), source, path, after, through, &output); err == nil {
		t.Fatal("existing export was replaced")
	}
	delta, err := archive.OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer delta.DB.Close()
	var count int
	delta.DB.QueryRow("SELECT COUNT(*) FROM records").Scan(&count)
	if count != 1 {
		t.Fatalf("exported %d sources", count)
	}
	if _, err := delta.Request(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
}
