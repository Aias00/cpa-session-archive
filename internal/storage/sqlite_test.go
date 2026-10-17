package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
)

func TestSQLiteBackendRoundTrip(t *testing.T) {
	store, err := archive.OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	backend := NewSQLite(store)
	t.Cleanup(func() { _ = backend.Close() })
	ctx := context.Background()
	if err = backend.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if exists, err := backend.SessionExists(ctx, "missing"); err != nil || exists {
		t.Fatalf("missing session: exists=%v err=%v", exists, err)
	}
	when := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	if err = backend.PutBatch([]archive.Record{{RequestID: "request", SessionID: "session", StartedAt: when, CompletedAt: when}}); err != nil {
		t.Fatal(err)
	}
	if exists, err := backend.SessionExists(ctx, "session"); err != nil || !exists {
		t.Fatalf("stored session: exists=%v err=%v", exists, err)
	}
	if _, err = backend.RefreshSessionExportDigests(ctx, 1); err != nil {
		t.Fatal(err)
	}
	snapshot, err := backend.BeginSnapshot(when.Add(-time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SessionCount() != 1 {
		t.Fatalf("sessions=%d", snapshot.SessionCount())
	}
	if err = snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err = backend.Close(); err != nil {
		t.Fatal(err)
	}
	if err = backend.Ping(ctx); err == nil {
		t.Fatal("closed backend reported healthy")
	}
	if _, err = backend.SessionExists(ctx, "session"); err == nil {
		t.Fatal("closed backend reported session absence instead of storage failure")
	}
}

func TestSQLiteWriteRetryClassification(t *testing.T) {
	backend := &SQLite{}
	for _, test := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("database is locked"), true},
		{fmt.Errorf("write batch: %w", errors.New("database is busy")), true},
		{errors.New("disk is full"), false},
	} {
		if got := backend.RetryableWrite(test.err); got != test.want {
			t.Fatalf("RetryableWrite(%v)=%v, want %v", test.err, got, test.want)
		}
	}
}
