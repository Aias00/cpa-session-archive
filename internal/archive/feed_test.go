package archive

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestFeedIdentityStableConcurrentAndCopied(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "raw.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	id, err := store.EnsureFeedIdentity(context.Background())
	if err != nil || len(id) != 64 {
		t.Fatal("identity unavailable", err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			got, err := store.EnsureFeedIdentity(context.Background())
			if err != nil || got != id {
				t.Error("concurrent identity changed", err)
			}
		})
	}
	workers.Wait()
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM archive_feed_identity`).Scan(&count); err != nil || count != 1 {
		t.Fatal("identity table is not single-row", err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.sqlite")
	if _, err := store.DB.Exec(`VACUUM INTO ?`, copyPath); err != nil {
		t.Fatal(err)
	}
	copy, err := OpenStore(copyPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.DB.Close()
	if got, err := copy.EnsureFeedIdentity(context.Background()); err != nil || got != id {
		t.Fatal("database copy lost archive identity", err)
	}
	other, err := OpenStore(filepath.Join(t.TempDir(), "other.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.DB.Close()
	if got, err := other.EnsureFeedIdentity(context.Background()); err != nil || got == id {
		t.Fatal("independent database reused identity", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.EnsureFeedIdentity(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("identity creation ignored cancellation", err)
	}
}
