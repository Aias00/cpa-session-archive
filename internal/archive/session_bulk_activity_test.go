package archive

import (
	"context"
	"testing"
	"time"
)

func TestSessionBulkArchiveConflictsOnRetainedSourceChanges(t *testing.T) {
	for _, kind := range []string{"completion", "response", "move", "delete"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store := openManagedSessionTestStore(t)
			record := managedSessionTestRecord("request", "session")
			record.Response = []byte(`{"output":"original response"}`)
			if err := store.PutBatch([]Record{record}); err != nil {
				t.Fatal(err)
			}
			job := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true)})
			var beforeLastAt string
			var beforeRequests int
			if err := store.DB.QueryRow(`SELECT last_at,requests FROM session_summaries WHERE session_id='session'`).Scan(&beforeLastAt, &beforeRequests); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "completion":
				record.CompletedAt = record.CompletedAt.Add(time.Minute)
				if err := store.PutBatch([]Record{record}); err != nil {
					t.Fatal(err)
				}
			case "response":
				record.Response = []byte(`{"output":"updated response"}`)
				if err := store.PutBatch([]Record{record}); err != nil {
					t.Fatal(err)
				}
			case "move":
				if _, err := store.DB.Exec(`UPDATE records SET session_id='other-session' WHERE request_id='request'`); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if _, err := store.DB.Exec(`DELETE FROM records WHERE request_id='request'`); err != nil {
					t.Fatal(err)
				}
			}
			// Known request IDs do not refresh this summary through PutBatch.
			// Source events must detect changes even while both fields agree.
			var afterLastAt string
			var afterRequests int
			if err := store.DB.QueryRow(`SELECT last_at,requests FROM session_summaries WHERE session_id='session'`).Scan(&afterLastAt, &afterRequests); err != nil {
				t.Fatal(err)
			}
			if afterLastAt != beforeLastAt || afterRequests != beforeRequests {
				t.Fatal("fixture no longer exercises unchanged summary fields")
			}
			var sourceFence int64
			if err := store.DB.QueryRow(`SELECT sequence FROM archive_ingest_clock WHERE id=1`).Scan(&sourceFence); err != nil {
				t.Fatal(err)
			}
			job = applyBulkAll(t, store, job.ID)
			if job.Applied != 0 || job.Conflicts != 1 || job.Items[0].Reason != "new_activity" {
				t.Fatalf("source change did not conflict: %+v", job)
			}
			m, err := store.SessionManagement(ctx, "session")
			if err != nil || m.Archived || m.Revision != 0 {
				t.Fatalf("changed source was archived: %+v error=%v", m, err)
			}
			var afterFence int64
			if err = store.DB.QueryRow(`SELECT sequence FROM archive_ingest_clock WHERE id=1`).Scan(&afterFence); err != nil || afterFence != sourceFence {
				t.Fatalf("management changed source feed: before=%d after=%d error=%v", sourceFence, afterFence, err)
			}
		})
	}
}

func TestSessionBulkUndoRestoreConflictsOnRetainedRequestUpdate(t *testing.T) {
	for _, kind := range []string{"completion", "response"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store := openManagedSessionTestStore(t)
			record := managedSessionTestRecord("request", "session")
			record.Response = []byte(`{"output":"original response"}`)
			if err := store.PutBatch([]Record{record}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpdateSessionManagement(ctx, "session", 0, SessionManagementUpdate{Archived: true}); err != nil {
				t.Fatal(err)
			}
			job := bulkPlan(t, store, SessionBrowseOptions{Archived: "only"}, SessionBulkAction{Archived: bulkBool(false)})
			job = applyBulkAll(t, store, job.ID)
			if job.Applied != 1 {
				t.Fatalf("restore=%+v", job)
			}
			if kind == "completion" {
				record.CompletedAt = record.CompletedAt.Add(time.Minute)
			} else {
				record.Response = []byte(`{"output":"updated response"}`)
			}
			if err := store.PutBatch([]Record{record}); err != nil {
				t.Fatal(err)
			}
			job, err := store.UndoSessionBulk(ctx, job.ID, 100)
			if err != nil || job.UndoConflicts != 1 || job.Items[0].Reason != "new_activity" {
				t.Fatalf("undo rearchive=%+v error=%v", job, err)
			}
			m, err := store.SessionManagement(ctx, "session")
			if err != nil || m.Archived || m.Revision != 2 {
				t.Fatalf("changed retained request rearchived: %+v error=%v", m, err)
			}
		})
	}
}

func TestSessionBulkLegacySourceFenceAndUnfencedPreviewMigration(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	seedBulkSummaries(t, store, 1)
	// A pre-event archive has source fence zero, which is a valid baseline.
	job := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true)})
	job = applyBulkAll(t, store, job.ID)
	if job.Applied != 1 {
		t.Fatalf("legacy source fence zero was rejected: %+v", job)
	}
	if _, err := store.UndoSessionBulk(ctx, job.ID, 100); err != nil {
		t.Fatal(err)
	}
	unfenced := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true)})
	if _, err := store.DB.Exec(`ALTER TABLE session_bulk_items DROP COLUMN expected_event_sequence`); err != nil {
		t.Fatal(err)
	}
	path := store.DBPath
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	unfenced = applyBulkAll(t, reopened, unfenced.ID)
	if unfenced.Conflicts != 1 || unfenced.Items[0].Reason != "new_activity" {
		t.Fatalf("unfenced old preview was allowed to archive: %+v", unfenced)
	}
	fresh := bulkPlan(t, reopened, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true)})
	fresh = applyBulkAll(t, reopened, fresh.ID)
	if fresh.Applied != 1 {
		t.Fatalf("new preview after migration failed: %+v", fresh)
	}
}

func TestAutomaticTemporalViewsUseRetainedCompletionBeforePreview(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	now := time.Now().UTC()
	record := managedSessionTestRecord("request", "session")
	record.StartedAt, record.CompletedAt = now.AddDate(0, 0, -90), now.AddDate(0, 0, -90)
	if err := store.PutBatch([]Record{record}); err != nil {
		t.Fatal(err)
	}
	var oldLastAt string
	if err := store.DB.QueryRow(`SELECT last_at FROM session_summaries WHERE session_id='session'`).Scan(&oldLastAt); err != nil {
		t.Fatal(err)
	}
	// This update precedes the preview, so its source-event token is already
	// part of the frozen baseline. Temporal selection must see the completion.
	record.CompletedAt = now
	record.Response = []byte(`{"output":"newly completed retained request"}`)
	if err := store.PutBatch([]Record{record}); err != nil {
		t.Fatal(err)
	}
	var staleLastAt string
	if err := store.DB.QueryRow(`SELECT last_at FROM session_summaries WHERE session_id='session'`).Scan(&staleLastAt); err != nil {
		t.Fatal(err)
	}
	if staleLastAt != oldLastAt {
		t.Fatal("fixture no longer exercises a stale summary timestamp")
	}
	var fenceBefore int64
	if err := store.DB.QueryRow(`SELECT sequence FROM archive_ingest_clock WHERE id=1`).Scan(&fenceBefore); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		view string
		want int
	}{{"recent", 1}, {"inactive", 0}} {
		selection := SessionBrowseOptions{View: check.view, InactiveDays: 30}
		page, err := store.BrowseSessions(ctx, selection)
		if err != nil || page.Total != check.want || len(page.Sessions) != check.want {
			t.Fatalf("%s browse count=%d want=%d error=%v", check.view, page.Total, check.want, err)
		}
		job := bulkPlan(t, store, selection, SessionBulkAction{Archived: bulkBool(true)})
		if job.Total != check.want {
			t.Fatalf("%s frozen count=%d want=%d", check.view, job.Total, check.want)
		}
		if check.view == "inactive" {
			job = applyBulkAll(t, store, job.ID)
			if job.Applied != 0 {
				t.Fatal("recently completed retained request was archived as inactive")
			}
		}
	}
	var fenceAfter int64
	if err := store.DB.QueryRow(`SELECT sequence FROM archive_ingest_clock WHERE id=1`).Scan(&fenceAfter); err != nil || fenceAfter != fenceBefore {
		t.Fatalf("temporal views changed source feed: before=%d after=%d error=%v", fenceBefore, fenceAfter, err)
	}
	m, err := store.SessionManagement(ctx, "session")
	if err != nil || m.Archived || m.Revision != 0 {
		t.Fatalf("temporal views changed management metadata: %+v error=%v", m, err)
	}
}
