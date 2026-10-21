package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

type bindingFeed struct {
	storage.MemoryFeed
	archiveID     string
	complete      bool
	watermark     *int64
	identityError error
	sourceError   error
	revisionError error
	sourceCalls   int
	revisionCalls int
}

func (f *bindingFeed) MemoryIdentity(ctx context.Context) (storage.FeedIdentity, error) {
	if f.identityError != nil {
		return storage.FeedIdentity{}, f.identityError
	}
	watermark, err := f.MemoryFeed.MemoryWatermark(ctx)
	if err != nil {
		return storage.FeedIdentity{}, err
	}
	if f.watermark != nil {
		watermark = *f.watermark
	}
	return storage.FeedIdentity{ArchiveID: f.archiveID, Watermark: watermark, HistoryComplete: f.complete}, nil
}

func (f *bindingFeed) MemorySource(ctx context.Context, id string) (archive.SourceSnapshot, error) {
	f.sourceCalls++
	if f.sourceError != nil {
		return archive.SourceSnapshot{}, f.sourceError
	}
	return f.MemoryFeed.MemorySource(ctx, id)
}

func (f *bindingFeed) MemoryRevision(ctx context.Context, id string) (string, error) {
	f.revisionCalls++
	if f.revisionError != nil {
		return "", f.revisionError
	}
	source, err := f.MemoryFeed.MemorySource(ctx, id)
	return source.Revision, err
}

func bindingEngine(t *testing.T) (*Engine, *archive.Store, *bindingFeed, string) {
	t.Helper()
	dir := t.TempDir()
	raw, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.DB.Close() })
	feed := &bindingFeed{MemoryFeed: storage.NewSQLite(raw), archiveID: "archive-a", complete: true}
	path := filepath.Join(dir, "memory.sqlite")
	e, err := Open(path, feed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	r := sample("binding-source", "s", "p", "bindingneedle evidence")
	r.OriginalRequest = []byte(`{"messages":[{"role":"user","content":"bindingneedle evidence"},{"role":"user","content":"bindingneedle evidence"}]}`)
	if err := raw.PutBatch([]archive.Record{r}); err != nil {
		t.Fatal(err)
	}
	return e, raw, feed, path
}

func bindingCheckpoint(t *testing.T, e *Engine) int64 {
	t.Helper()
	var checkpoint int64
	if err := e.db.QueryRow(`SELECT checkpoint FROM state WHERE id=1`).Scan(&checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func TestSourceIdentityOnlyWorkerBindsAndBindingPersists(t *testing.T) {
	e, _, feed, path := bindingEngine(t)
	ctx := context.Background()
	q := Query{Text: "bindingneedle", ProjectID: "p"}
	if result, err := e.Search(ctx, q); !errors.Is(err, ErrSourceBinding) || len(result.Hits) != 0 {
		t.Fatal("reader served or bound an unbound projection", err)
	}
	if _, err := e.Status(ctx); !errors.Is(err, ErrSourceBinding) {
		t.Fatal("status bound an unbound projection", err)
	}
	if _, err := e.ReadSource(ctx, Citation{NormalizerVersion: NormalizerVersion, RequestID: "binding-source", Revision: "unbound", Side: "original_request", Pointer: "/messages/0/content", End: 1}); !errors.Is(err, ErrSourceBinding) {
		t.Fatal("source reader bound an unbound projection", err)
	}
	var bindings int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM source_binding`).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatal("read path wrote a binding", err)
	}
	syncAll(t, e)
	result, err := e.Search(ctx, q)
	if err != nil || len(result.Hits) != 2 {
		t.Fatal("worker did not establish a usable binding", err)
	}
	checkpoint := bindingCheckpoint(t, e)
	e.Close()
	reopened, err := Open(path, feed)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if result, err := reopened.Search(ctx, q); err != nil || len(result.Hits) != 2 || bindingCheckpoint(t, reopened) != checkpoint {
		t.Fatal("restart lost binding or cursor", err)
	}
	feed.archiveID = "archive-b"
	if _, err := reopened.Sync(ctx, 64); !errors.Is(err, ErrSourceBinding) {
		t.Fatal("restart adopted a different archive", err)
	}
}

func TestLegacyProjectionRequiresExplicitSourceAdoption(t *testing.T) {
	e, raw, path := testEngine(t)
	if err := raw.PutBatch([]archive.Record{sample("legacy-source", "s", "p", "legacyneedle verified evidence")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	q := Query{Text: "legacyneedle", ProjectID: "p"}
	legacyResult, err := e.Search(context.Background(), q)
	if err != nil || len(legacyResult.Hits) != 1 {
		t.Fatal("legacy fixture did not retain a citation", err)
	}
	checkpoint := bindingCheckpoint(t, e)
	e.Close()
	otherRaw, err := archive.OpenStore(filepath.Join(t.TempDir(), "other-archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer otherRaw.DB.Close()
	if err := otherRaw.PutBatch([]archive.Record{sample("other-a", "other", "q", "other evidence a"), sample("other-b", "other", "q", "other evidence b")}); err != nil {
		t.Fatal(err)
	}
	wrong := &bindingFeed{MemoryFeed: storage.NewSQLite(otherRaw), archiveID: "different-authenticated-archive", complete: true}
	unbound, err := Open(path, wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unbound.Sync(context.Background(), 64); !errors.Is(err, ErrSourceBinding) {
		t.Fatal("populated legacy projection silently adopted another archive", err)
	}
	if bindingCheckpoint(t, unbound) != checkpoint {
		t.Fatal("rejected adoption changed the legacy checkpoint")
	}
	var bindings int
	if err := unbound.db.QueryRow(`SELECT COUNT(*) FROM source_binding`).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatal("rejected legacy adoption persisted a binding", err)
	}
	unbound.Close()
	correct := &bindingFeed{MemoryFeed: storage.NewSQLite(raw), archiveID: "verified-legacy-archive", complete: true}
	adopted, err := Open(path, correct, WithSourceAdoption())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adopted.Sync(context.Background(), 64); err != nil {
		t.Fatal("operator-authorized legacy adoption failed", err)
	}
	if bindingCheckpoint(t, adopted) != checkpoint {
		t.Fatal("adoption reset or skipped the existing checkpoint")
	}
	if evidence, err := adopted.ReadSource(context.Background(), legacyResult.Hits[0].Citation); err != nil || evidence.Text != legacyResult.Hits[0].Snippet {
		t.Fatal("adoption changed existing citation semantics", err)
	}
	adopted.Close()
	// The one-time migration option is unnecessary after the worker binds.
	restarted, err := Open(path, correct)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if result, err := restarted.Search(context.Background(), q); err != nil || !reflect.DeepEqual(result.Hits, legacyResult.Hits) {
		t.Fatal("bound projection needed adoption again or lost old citations", err)
	}
}

func TestLegacyEmptyButAdvancedProjectionCannotSilentlyAdopt(t *testing.T) {
	e, raw, path := testEngine(t)
	if err := raw.PutBatch([]archive.Record{sample("gone", "s", "p", "deleted legacy evidence")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='gone'`); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	if status, err := e.Status(context.Background()); err != nil || status.Sources != 0 || status.Checkpoint == 0 {
		t.Fatal("fixture no longer has an empty but advanced checkpoint", err)
	}
	e.Close()
	remote := &bindingFeed{MemoryFeed: storage.NewSQLite(raw), archiveID: "unverified", complete: true}
	reopened, err := Open(path, remote)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Sync(context.Background(), 64); !errors.Is(err, ErrSourceBinding) {
		t.Fatal("empty legacy index skipped adoption because its source count was zero", err)
	}
}

func TestSourceIdentityMismatchHistoryLossRollbackAndOutageFailClosed(t *testing.T) {
	e, _, feed, _ := bindingEngine(t)
	syncAll(t, e)
	ctx := context.Background()
	q := Query{Text: "bindingneedle", ProjectID: "p"}
	initial, err := e.Search(ctx, q)
	if err != nil || len(initial.Hits) == 0 {
		t.Fatal(err)
	}
	checkpoint := bindingCheckpoint(t, e)
	rollback := checkpoint - 1
	privateFailure := errors.New("PRIVATE transport failure must not enter projection diagnostics")
	for _, test := range []struct {
		name string
		edit func()
	}{
		{"identity", func() { feed.archiveID = "archive-b" }},
		{"history", func() { feed.complete = false }},
		{"rollback", func() { feed.watermark = &rollback }},
		{"outage", func() { feed.identityError = privateFailure }},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.edit()
			if _, err := e.Sync(ctx, 64); err == nil {
				t.Fatal("worker accepted incompatible or unavailable identity")
			}
			if bindingCheckpoint(t, e) != checkpoint {
				t.Fatal("worker advanced across an incompatible source")
			}
			if result, err := e.Search(ctx, q); err == nil || len(result.Hits) != 0 {
				t.Fatal("search returned cached evidence after source failure")
			}
			if evidence, err := e.ReadSource(ctx, initial.Hits[0].Citation); err == nil || evidence.Text != "" {
				t.Fatal("source reader returned evidence after source failure")
			}
			if status, err := e.Status(ctx); err == nil || status.Sources != 0 {
				t.Fatal("status treated incompatible source as current")
			}
			var id, lastError string
			if err := e.db.QueryRow(`SELECT archive_id FROM source_binding WHERE id=1`).Scan(&id); err != nil || id != "archive-a" {
				t.Fatal("stored binding changed", err)
			}
			if err := e.db.QueryRow(`SELECT last_error FROM state WHERE id=1`).Scan(&lastError); err != nil || strings.Contains(lastError, "PRIVATE") {
				t.Fatal("private feed error escaped diagnostics", err)
			}
			feed.archiveID, feed.complete, feed.watermark, feed.identityError = "archive-a", true, nil, nil
		})
	}
	if _, err := e.Sync(ctx, 64); err != nil {
		t.Fatal("same-source recovery did not resume", err)
	}
}

func TestSourceBindingDetectsRollbackDuringInitialBackfill(t *testing.T) {
	for _, laterSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "descriptor_fence", true: "later_source_fence"}[laterSource], func(t *testing.T) {
			e, _, feed, _ := bindingEngine(t)
			oldWatermark := int64(0)
			if laterSource {
				// The source is inserted after the initial descriptor snapshot.
				// Its own observed sequence must raise the durable binding fence.
				feed.watermark = &oldWatermark
			}
			if n, err := e.Sync(context.Background(), 1); err != nil || n != 1 {
				t.Fatal("initial source did not project", err)
			}
			if bindingCheckpoint(t, e) != 0 {
				t.Fatal("fixture no longer tests rollback before event replay")
			}
			var observed int64
			if err := e.db.QueryRow(`SELECT last_watermark FROM source_binding WHERE id=1`).Scan(&observed); err != nil || observed < 1 {
				t.Fatal("binding failed to retain its observed high-watermark", err)
			}
			feed.watermark = &oldWatermark
			if _, err := e.Sync(context.Background(), 1); !errors.Is(err, ErrSourceBinding) {
				t.Fatal("rollback above committed checkpoint was accepted", err)
			}
			if result, err := e.Search(context.Background(), Query{Text: "bindingneedle"}); !errors.Is(err, ErrSourceBinding) || len(result.Hits) != 0 {
				t.Fatal("read path hid initial-backfill rollback", err)
			}
		})
	}
}

func TestSourceBindingReadersDoNotAdvanceObservedWatermark(t *testing.T) {
	e, raw, _, _ := bindingEngine(t)
	syncAll(t, e)
	var initial int64
	if err := e.db.QueryRow(`SELECT last_watermark FROM source_binding WHERE id=1`).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	if err := raw.PutBatch([]archive.Record{sample("later-source", "s", "p", "later evidence")}); err != nil {
		t.Fatal(err)
	}
	status, err := e.Status(context.Background())
	if err != nil || status.Watermark <= initial {
		t.Fatal("reader did not observe newer source watermark", err)
	}
	if _, err := e.Search(context.Background(), Query{Text: "bindingneedle"}); err != nil {
		t.Fatal(err)
	}
	var afterReads int64
	if err := e.db.QueryRow(`SELECT last_watermark FROM source_binding WHERE id=1`).Scan(&afterReads); err != nil || afterReads != initial {
		t.Fatal("reader changed the durable source binding", err)
	}
	syncAll(t, e)
	if err := e.db.QueryRow(`SELECT last_watermark FROM source_binding WHERE id=1`).Scan(&afterReads); err != nil || afterReads != status.Watermark {
		t.Fatal("worker failed to advance observed watermark", err)
	}
}

func TestSourceOutageDoesNotDeleteEvidenceOrAdvanceCursorAndRecoveryReplays(t *testing.T) {
	e, raw, feed, _ := bindingEngine(t)
	syncAll(t, e)
	checkpoint := bindingCheckpoint(t, e)
	r := sample("binding-source", "s", "p", "bindingneedle evidence")
	r.Response = []byte(`{"choices":[{"message":{"role":"assistant","content":"bindingneedle updated evidence"}}]}`)
	if err := raw.PutBatch([]archive.Record{r}); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []error{errors.New("transport disconnected"), sql.ErrNoRows} {
		feed.sourceError, feed.revisionError = failure, failure
		if _, err := e.Sync(context.Background(), 64); err == nil {
			t.Fatal("non-tombstone source error was treated as deletion")
		}
		if bindingCheckpoint(t, e) != checkpoint {
			t.Fatal("outage advanced the checkpoint")
		}
		var sources int
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM sources WHERE request_id='binding-source'`).Scan(&sources); err != nil || sources != 1 {
			t.Fatal("outage deleted retained projection evidence", err)
		}
		if result, err := e.Search(context.Background(), Query{Text: "bindingneedle"}); err == nil || len(result.Hits) != 0 {
			t.Fatal("revision outage returned partial evidence")
		}
	}
	feed.sourceError, feed.revisionError = nil, nil
	syncAll(t, e)
	watermark, err := raw.MemoryWatermark(context.Background())
	if err != nil || bindingCheckpoint(t, e) != watermark || watermark <= checkpoint {
		t.Fatal("recovery skipped or failed to replay the pending event", err)
	}
	result, err := e.Search(context.Background(), Query{Text: "updated"})
	if err != nil || len(result.Hits) != 1 {
		t.Fatal("recovery did not index the new source", err)
	}
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='binding-source'`); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	if result, err := e.Search(context.Background(), Query{Text: "bindingneedle"}); err != nil || len(result.Hits) != 0 {
		t.Fatal("explicit not-found tombstone was not reconciled", err)
	}
}

func TestSearchUsesRevisionCapabilityWithoutTransferringFullSources(t *testing.T) {
	e, _, feed, _ := bindingEngine(t)
	syncAll(t, e)
	feed.sourceCalls, feed.revisionCalls = 0, 0
	q := Query{Text: "bindingneedle"}
	first, err := e.Search(context.Background(), q)
	if err != nil || len(first.Hits) != 2 || feed.sourceCalls != 0 || feed.revisionCalls != 1 {
		t.Fatalf("search transferred sources or repeated per-request revision checks: full=%d revisions=%d err=%v", feed.sourceCalls, feed.revisionCalls, err)
	}
	if _, err := e.Search(context.Background(), q); err != nil || feed.revisionCalls != 2 {
		t.Fatal("revision check was cached across requests", err)
	}
	if evidence, err := e.ReadSource(context.Background(), first.Hits[0].Citation); err != nil || evidence.Text != first.Hits[0].Snippet || feed.sourceCalls != 1 {
		t.Fatal("source read failed to obtain the current full source", err)
	}
}

func TestOpenRejectsArchivePathsWithoutChangingTheArchive(t *testing.T) {
	for _, table := range []string{"records", "blobs", "archive_ingest_clock"} {
		t.Run(table, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.sqlite")
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE ` + table + `(id INTEGER PRIMARY KEY, evidence TEXT); INSERT INTO ` + table + `(evidence) VALUES('retain this raw evidence')`); err != nil {
				t.Fatal(err)
			}
			db.Close()
			if err := os.Chmod(path, 0640); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(path)
			for _, alias := range []string{path, filepath.Join(filepath.Dir(path), "symlink.sqlite"), filepath.Join(filepath.Dir(path), "hardlink.sqlite")} {
				if strings.Contains(alias, "symlink") {
					if err := os.Symlink(path, alias); err != nil {
						t.Fatal(err)
					}
				} else if strings.Contains(alias, "hardlink") {
					if err := os.Link(path, alias); err != nil {
						t.Fatal(err)
					}
				}
				// A feed is needed only to satisfy Open's capability argument; no
				// feed call may happen before rejecting the mistaken file path.
				if engine, err := Open(alias, &bindingFeed{}); !errors.Is(err, ErrArchiveProjectionPath) || engine != nil {
					t.Fatal("archive file accepted as a rebuildable projection", err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("archive contents changed during rejected open", err)
			}
			afterInfo, _ := os.Stat(path)
			if afterInfo.Mode() != info.Mode() || !afterInfo.ModTime().Equal(info.ModTime()) {
				t.Fatal("archive metadata changed during rejected open")
			}
		})
	}
}
