package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

func openManagedSessionTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	return store
}

func managedSessionTestRecord(requestID, sessionID string) Record {
	stamp := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	return Record{RequestID: requestID, SessionID: sessionID, StartedAt: stamp, CompletedAt: stamp, Summary: "Original conversation", OriginalRequest: []byte(`{"input":[{"role":"user","content":"Original evidence"}]}`)}
}

func TestSessionManagementPersistsAcrossCaptureAndReopen(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	if err := store.PutBatch([]Record{managedSessionTestRecord("r1", "session")}); err != nil {
		t.Fatal(err)
	}
	initial, err := store.SessionManagement(ctx, "session")
	if err != nil || initial.Revision != 0 || initial.Tags == nil {
		t.Fatalf("initial=%+v error=%v", initial, err)
	}
	updated, err := store.UpdateSessionManagement(ctx, "session", 0, SessionManagementUpdate{Title: "  Investigation  ", Note: " Reviewed notes\nsecond line ", Tags: []string{" backend ", "中文", "backend"}, Starred: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 1 || updated.Title != "Investigation" || updated.Note != "Reviewed notes\nsecond line" || !reflect.DeepEqual(updated.Tags, []string{"backend", "中文"}) || !updated.Starred || updated.UpdatedAt == "" {
		t.Fatalf("updated=%+v", updated)
	}
	if err = store.PutBatch([]Record{managedSessionTestRecord("r2", "session")}); err != nil {
		t.Fatal(err)
	}
	if err = store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(store.DBPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	read, err := reopened.SessionManagement(ctx, "session")
	if err != nil || !reflect.DeepEqual(updated, read) {
		t.Fatalf("read=%+v expected=%+v error=%v", read, updated, err)
	}
	page, err := reopened.BrowseSessions(ctx, SessionBrowseOptions{})
	if err != nil || page.Total != 1 || len(page.Sessions) != 1 || page.Sessions[0].Requests != 2 || !reflect.DeepEqual(page.Sessions[0].Management, updated) {
		t.Fatalf("page=%+v error=%v", page, err)
	}
	// Rebuilding the derived summary does not own or remove user annotations.
	if _, err = reopened.DB.Exec(`DELETE FROM session_summaries WHERE session_id='session'; DELETE FROM session_indexed_requests WHERE session_id='session'`); err != nil {
		t.Fatal(err)
	}
	if err = reopened.BackfillSessionIndex(ctx); err != nil {
		t.Fatal(err)
	}
	read, err = reopened.SessionManagement(ctx, "session")
	if err != nil || !reflect.DeepEqual(updated, read) {
		t.Fatalf("after rebuild=%+v error=%v", read, err)
	}
}

func TestSessionManagementConcurrentRevisionConflicts(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			store := openManagedSessionTestStore(t)
			ctx := context.Background()
			if err := store.PutBatch([]Record{managedSessionTestRecord("r1", "session")}); err != nil {
				t.Fatal(err)
			}
			var revision int64
			if existing {
				if _, err := store.UpdateSessionManagement(ctx, "session", 0, SessionManagementUpdate{Title: "First"}); err != nil {
					t.Fatal(err)
				}
				revision = 1
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var group sync.WaitGroup
			for i := range 2 {
				group.Add(1)
				go func(i int) {
					defer group.Done()
					<-start
					_, err := store.UpdateSessionManagement(ctx, "session", revision, SessionManagementUpdate{Title: fmt.Sprintf("Editor %d", i), Tags: []string{fmt.Sprintf("tag-%d", i)}})
					results <- err
				}(i)
			}
			close(start)
			group.Wait()
			close(results)
			success, conflicts := 0, 0
			for err := range results {
				switch {
				case err == nil:
					success++
				case errors.Is(err, ErrSessionManagementConflict):
					conflicts++
				default:
					t.Fatalf("unexpected error=%v", err)
				}
			}
			if success != 1 || conflicts != 1 {
				t.Fatalf("success=%d conflicts=%d", success, conflicts)
			}
			read, err := store.SessionManagement(ctx, "session")
			if err != nil || read.Revision != revision+1 || len(read.Tags) != 1 || strings.TrimPrefix(read.Tags[0], "tag-") != strings.TrimPrefix(read.Title, "Editor ") {
				t.Fatalf("read=%+v error=%v", read, err)
			}
		})
	}
}

func TestManagedSessionBrowseLiteralUnicodeAndFacetFilters(t *testing.T) {
	store := openManagedSessionTestStore(t)
	ctx := context.Background()
	a := managedSessionTestRecord("a", "one")
	a.Facets = map[string][]string{"project.name": {"repository"}, "client": {"Codex"}}
	b := managedSessionTestRecord("b", "two")
	b.Facets = map[string][]string{"project.name": {"repository"}, "client": {"Other"}}
	c := managedSessionTestRecord("c", "three")
	c.Facets = map[string][]string{"project.name": {"different"}, "client": {"Codex"}}
	if err := store.PutBatch([]Record{a, b, c}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateSessionManagement(ctx, "one", 0, SessionManagementUpdate{Title: `中文100%_ready\ok`, Note: "Precise note", Tags: []string{"release_100%", "Exact"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateSessionManagement(ctx, "two", 0, SessionManagementUpdate{Title: "中文100otherready", Tags: []string{"exact"}}); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		options SessionBrowseOptions
		want    []string
	}{
		{SessionBrowseOptions{Query: "%_"}, []string{"one"}},
		{SessionBrowseOptions{Query: `\ok`}, []string{"one"}},
		{SessionBrowseOptions{Query: "中文100"}, []string{"one", "two"}},
		{SessionBrowseOptions{Query: "Precise note"}, []string{"one"}},
		{SessionBrowseOptions{Query: "release_100%"}, []string{"one"}},
		{SessionBrowseOptions{Tag: "Exact"}, []string{"one"}},
		{SessionBrowseOptions{Tag: "exact"}, []string{"two"}},
		{SessionBrowseOptions{Query: "one"}, []string{"one"}},
		{SessionBrowseOptions{Query: "repository", Filters: map[string]string{"project.name": "repository", "client": "Codex"}}, []string{"one"}},
		{SessionBrowseOptions{Query: "Original conversation", Filters: map[string]string{"client": "Codex"}}, []string{"one", "three"}},
	}
	for i, check := range checks {
		page, err := store.BrowseSessions(ctx, check.options)
		if err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
		ids := []string{}
		for _, row := range page.Sessions {
			ids = append(ids, row.SessionID)
		}
		if page.Total != len(check.want) || !reflect.DeepEqual(ids, check.want) {
			t.Fatalf("check %d ids=%v total=%d want=%v", i, ids, page.Total, check.want)
		}
	}
}

func TestManagedSessionBrowsePagesAndArchiveState(t *testing.T) {
	store := openManagedSessionTestStore(t)
	ctx := context.Background()
	records := make([]Record, 125)
	for i := range records {
		id := fmt.Sprintf("s%03d", i)
		records[i] = managedSessionTestRecord(id, id)
	}
	if err := store.PutBatch(records); err != nil {
		t.Fatal(err)
	}
	page, err := store.BrowseSessions(ctx, SessionBrowseOptions{Limit: 120})
	if err != nil || page.Total != 125 || len(page.Sessions) != 120 || page.Sessions[0].SessionID != "s000" || page.Sessions[119].SessionID != "s119" {
		t.Fatalf("page total=%d size=%d error=%v", page.Total, len(page.Sessions), err)
	}
	second, err := store.BrowseSessions(ctx, SessionBrowseOptions{Limit: 120, Offset: 120})
	if err != nil || second.Total != 125 || len(second.Sessions) != 5 || second.Sessions[0].SessionID != "s120" {
		t.Fatalf("second=%+v error=%v", second, err)
	}
	if _, err = store.UpdateSessionManagement(ctx, "s124", 0, SessionManagementUpdate{Starred: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpdateSessionManagement(ctx, "s001", 0, SessionManagementUpdate{Starred: true, Archived: true}); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		options SessionBrowseOptions
		total   int
		first   string
	}{
		{SessionBrowseOptions{Limit: 1}, 124, "s124"},
		{SessionBrowseOptions{Archived: "only"}, 1, "s001"},
		{SessionBrowseOptions{Archived: "all", Starred: true}, 2, "s001"},
		{SessionBrowseOptions{Starred: true}, 1, "s124"},
	} {
		page, err = store.BrowseSessions(ctx, check.options)
		if err != nil || page.Total != check.total || len(page.Sessions) == 0 || page.Sessions[0].SessionID != check.first {
			t.Fatalf("page=%+v error=%v", page, err)
		}
	}
	if _, err = store.UpdateSessionManagement(ctx, "s001", 1, SessionManagementUpdate{}); err != nil {
		t.Fatal(err)
	}
	page, err = store.BrowseSessions(ctx, SessionBrowseOptions{Starred: true, Archived: "all"})
	if err != nil || page.Total != 1 || page.Sessions[0].SessionID != "s124" {
		t.Fatalf("replacement page=%+v error=%v", page, err)
	}
}

func TestSessionManagementPreservesEvidenceRevisionAndExport(t *testing.T) {
	store := openManagedSessionTestStore(t)
	ctx := context.Background()
	if err := store.PutBatch([]Record{managedSessionTestRecord("request", "session")}); err != nil {
		t.Fatal(err)
	}
	before, err := store.MemorySource(ctx, "request")
	if err != nil {
		t.Fatal(err)
	}
	watermark, err := store.MemoryWatermark(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var exportBefore, exportAfter bytes.Buffer
	if err = store.ExportSessionJSONL(ctx, "session", &exportBefore); err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpdateSessionManagement(ctx, "session", 0, SessionManagementUpdate{Title: "Display title", Note: "User notes", Tags: []string{"private-tag"}, Starred: true, Archived: true}); err != nil {
		t.Fatal(err)
	}
	after, err := store.MemorySource(ctx, "request")
	if err != nil {
		t.Fatal(err)
	}
	watermarkAfter, err := store.MemoryWatermark(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ExportSessionJSONL(ctx, "session", &exportAfter); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || watermark != watermarkAfter || !bytes.Equal(exportBefore.Bytes(), exportAfter.Bytes()) {
		t.Fatal("management update changed raw evidence, revision, feed sequence or export")
	}
	if bytes.Contains(exportAfter.Bytes(), []byte("Display title")) || bytes.Contains(exportAfter.Bytes(), []byte("private-tag")) {
		t.Fatal("display metadata entered evidence export")
	}
}

func TestSessionManagementRejectsPhantomAndInvalidWrites(t *testing.T) {
	store := openManagedSessionTestStore(t)
	ctx := context.Background()
	if _, err := store.SessionManagement(ctx, "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("read missing=%v", err)
	}
	if _, err := store.UpdateSessionManagement(ctx, "missing", 0, SessionManagementUpdate{}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("write missing=%v", err)
	}
	if err := store.PutBatch([]Record{managedSessionTestRecord("r1", "session")}); err != nil {
		t.Fatal(err)
	}
	updates := []SessionManagementUpdate{
		{Title: strings.Repeat("中", 201)},
		{Note: strings.Repeat("中", 8001)},
		{Title: "bad\nline"},
		{Title: "bad\n"},
		{Title: string([]byte{0xff})},
		{Note: "bad\x00note"},
		{Tags: []string{"bad\tlabel"}},
		{Tags: []string{"bad\t"}},
		{Tags: []string{"two,tags"}},
		{Tags: []string{"两个，标签"}},
		{Tags: []string{" "}},
		{Tags: []string{strings.Repeat("中", 49)}},
		{Tags: []string{string([]byte{0xff})}},
		{Tags: strings.Fields(strings.Repeat("tag ", 25))},
	}
	for i, update := range updates {
		if _, err := store.UpdateSessionManagement(ctx, "session", 0, update); !errors.Is(err, ErrInvalidSessionManagement) {
			t.Fatalf("invalid update %d=%v", i, err)
		}
	}
	if _, err := store.UpdateSessionManagement(ctx, "session", -1, SessionManagementUpdate{}); !errors.Is(err, ErrInvalidSessionManagement) {
		t.Fatalf("negative revision=%v", err)
	}
	for _, options := range []SessionBrowseOptions{{Limit: -1}, {Limit: 1001}, {Offset: -1}, {Offset: 1000001}, {Archived: "unknown"}, {Query: strings.Repeat("中", 1001)}, {Tag: strings.Repeat("x", 49)}} {
		if _, err := store.BrowseSessions(ctx, options); !errors.Is(err, ErrInvalidSessionManagement) {
			t.Fatalf("invalid browse=%+v error=%v", options, err)
		}
	}
	read, err := store.SessionManagement(ctx, "session")
	if err != nil || read.Revision != 0 {
		t.Fatalf("invalid writes persisted=%+v error=%v", read, err)
	}
}

func TestManagedSessionBrowseHydratesOnlySelectedPage(t *testing.T) {
	store := openManagedSessionTestStore(t)
	ctx := context.Background()
	records := make([]Record, 125)
	for i := range records {
		id := fmt.Sprintf("session-%03d", i)
		records[i] = managedSessionTestRecord(id, id)
		records[i].CredentialHash = fmt.Sprintf("credential-%03d", i)
	}
	if err := store.PutBatch(records); err != nil {
		t.Fatal(err)
	}
	store.DB.SetMaxOpenConns(1)
	conn, err := store.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int64
	err = conn.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterFunc("session_browse_probe", func(value string) string { probes.Add(1); return value }, false)
	})
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, `ALTER TABLE records RENAME TO records_browse_source`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, `CREATE VIEW records AS SELECT session_id,session_browse_probe(credential_hash) AS credential_hash FROM records_browse_source`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := store.BrowseSessions(ctx, SessionBrowseOptions{Limit: 3, Offset: 120})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 125 || len(page.Sessions) != 3 || page.Sessions[0].SessionID != "session-120" {
		t.Fatalf("page=%+v", page)
	}
	if calls := probes.Load(); calls < 3 || calls > 6 {
		t.Fatalf("credential reads=%d; expected only the three selected sessions", calls)
	}
}
