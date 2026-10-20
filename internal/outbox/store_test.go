package outbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func openFixture(t *testing.T, bytes int64, entries int) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "private", "outbox.sqlite"), bytes, entries)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOutboxBoundsIdempotenceAndQuarantine(t *testing.T) {
	ctx := context.Background()
	s := openFixture(t, 8, 2)
	first, inserted, err := s.Put(ctx, []byte("first"))
	if err != nil || !inserted {
		t.Fatal("first admission", err)
	}
	duplicate, inserted, err := s.Put(ctx, []byte("first"))
	if err != nil || inserted || duplicate != first {
		t.Fatal("duplicate charged capacity", err)
	}
	if _, _, err = s.Put(ctx, []byte("long")); !errors.Is(err, ErrFull) {
		t.Fatal("byte bound", err)
	}
	second, _, err := s.Put(ctx, []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Quarantine(ctx, first, 503); err == nil {
		t.Fatal("transient failure quarantined")
	}
	if err = s.Quarantine(ctx, first, 413); err != nil {
		t.Fatal(err)
	}
	next, err := s.Next(ctx)
	if err != nil || next.Digest != second {
		t.Fatal("quarantine blocked pending work", err)
	}
	status, err := s.Status(ctx)
	if err != nil || status.Bytes != 8 || status.Quarantined != 1 || status.Pending != 1 {
		t.Fatalf("accounting %+v %v", status, err)
	}
	if err = s.Acknowledge(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err = s.Acknowledge(ctx, second); err != nil {
		t.Fatal("repeated receipt", err)
	}
	if _, err = s.Next(ctx); !errors.Is(err, ErrEmpty) {
		t.Fatal("quarantined payload replayed", err)
	}
	if _, _, err = s.Put(ctx, []byte("oversized")); !errors.Is(err, ErrOversize) {
		t.Fatal("single delivery bound", err)
	}
}

func TestOutboxConcurrentCountBound(t *testing.T) {
	s := openFixture(t, 1<<20, 4)
	var accepted atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func(n int) {
			defer group.Done()
			_, inserted, err := s.Put(context.Background(), []byte{byte(n)})
			if err != nil && !errors.Is(err, ErrFull) {
				t.Error(err)
			}
			if inserted {
				accepted.Add(1)
			}
		}(i)
	}
	group.Wait()
	status, err := s.Status(context.Background())
	if err != nil || accepted.Load() != 4 || status.Entries != 4 || status.Bytes != 4 {
		t.Fatalf("concurrent quota: %+v %v", status, err)
	}
}

func TestOutboxCrashHelper(t *testing.T) {
	path := os.Getenv("CPA_OUTBOX_CRASH_FIXTURE")
	if path == "" {
		t.Skip()
	}
	s, err := Open(path, 1024, 4)
	if err != nil {
		os.Exit(2)
	}
	if _, _, err = s.Put(context.Background(), []byte("committed-before-exit")); err != nil {
		os.Exit(3)
	}
	// Deliberately omit Close and all deferred cleanup to exercise WAL recovery.
	os.Exit(0)
}

func TestOutboxRecoversAfterProcessExitAndRetainsOnLowerLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "outbox.sqlite")
	child := exec.Command(os.Args[0], "-test.run=^TestOutboxCrashHelper$")
	child.Env = append(os.Environ(), "CPA_OUTBOX_CRASH_FIXTURE="+path)
	if err := child.Run(); err != nil {
		t.Fatal("crash fixture failed", err)
	}
	s, err := Open(path, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entry, err := s.Next(context.Background())
	if err != nil || string(entry.Body) != "committed-before-exit" {
		t.Fatal("committed outbox lost at process exit", err)
	}
	if _, _, err = s.Put(context.Background(), []byte("x")); !errors.Is(err, ErrFull) {
		t.Fatal("lower limit evicted pending work", err)
	}
	if _, inserted, err := s.Put(context.Background(), entry.Body); err != nil || inserted {
		t.Fatal("restart replay duplicated stored bytes", err)
	}
	if err = s.Acknowledge(context.Background(), entry.Digest); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status(context.Background())
	if err != nil || status.Bytes != 0 || status.Entries != 0 {
		t.Fatal("receipt did not release capacity", err)
	}
}

func TestOutboxRejectsUnsafePathAndDetectsPayloadCorruption(t *testing.T) {
	dir := t.TempDir()
	public := filepath.Join(dir, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(filepath.Join(public, "outbox.sqlite"), 1024, 4); err == nil {
		s.Close()
		t.Fatal("public outbox directory accepted")
	}
	private := filepath.Join(dir, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(private, "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(private, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(link, 1024, 4); err == nil {
		s.Close()
		t.Fatal("symlink outbox accepted")
	}
	s := openFixture(t, 1024, 4)
	if _, _, err := s.Put(context.Background(), []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE deliveries SET body=?`, []byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Next(context.Background()); !errors.Is(err, ErrCorrupt) {
		t.Fatal("corrupted delivery replayed", err)
	}
}
