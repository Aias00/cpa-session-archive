package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func bulkBool(value bool) *bool { return &value }

func seedBulkSummaries(t *testing.T, store *Store, count int) {
	t.Helper()
	_, err := store.DB.Exec(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x+1<?)
		INSERT INTO session_summaries(session_id,requests,first_at,last_at,project,summary)
		SELECT printf('s%06d',x),1,'2020-01-01T00:00:00Z','2020-01-01T00:00:00Z','same-name','Indexed session' FROM n`, count)
	if err != nil {
		t.Fatal(err)
	}
}

func bulkPlan(t *testing.T, store *Store, selection SessionBrowseOptions, action SessionBulkAction) SessionBulkJob {
	t.Helper()
	job, err := store.PlanSessionBulk(context.Background(), SessionBulkPlanRequest{Selection: selection, Action: action})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func applyBulkAll(t *testing.T, store *Store, id string) SessionBulkJob {
	t.Helper()
	for {
		job, err := store.ApplySessionBulk(context.Background(), id, 100)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "complete" {
			return job
		}
	}
}

func TestSessionBulkFreezesAllMatchesAcrossRestartAndUndo(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	seedBulkSummaries(t, store, 1203)
	job := bulkPlan(t, store, SessionBrowseOptions{Limit: 1, Offset: 1000, View: "inactive"}, SessionBulkAction{Archived: bulkBool(true), AddTags: []string{" review "}})
	if job.Total != 1203 || job.Pending != 1203 || job.Status != "preview" || len(job.Items) != 20 || job.Selection.Offset != 0 || job.Selection.Limit != 0 || job.Selection.InactiveDays != 30 {
		t.Fatalf("preview=%+v", job)
	}
	job, err := store.ApplySessionBulk(ctx, job.ID, 0)
	if err != nil || job.Applied != 100 || job.Pending != 1103 || job.Status != "running" {
		t.Fatalf("first step=%+v error=%v", job, err)
	}
	path := store.DBPath
	if err = store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	// The selection is a frozen identity list, not a saved query.
	if _, err = reopened.DB.Exec(`INSERT INTO session_summaries(session_id,requests,first_at,last_at) VALUES('new',1,'2020-01-01T00:00:00Z','2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	job = applyBulkAll(t, reopened, job.ID)
	if job.Total != 1203 || job.Applied != 1203 || job.Pending != 0 || job.Status != "complete" {
		t.Fatalf("completed=%+v", job)
	}
	again, err := reopened.ApplySessionBulk(ctx, job.ID, 100)
	if err != nil || !reflect.DeepEqual(job, again) {
		t.Fatalf("replay changed job: error=%v", err)
	}
	newManagement, err := reopened.SessionManagement(ctx, "new")
	if err != nil || newManagement.Archived || newManagement.Revision != 0 {
		t.Fatalf("new arrival was changed: %+v error=%v", newManagement, err)
	}
	for {
		job, err = reopened.UndoSessionBulk(ctx, job.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "undone" {
			break
		}
	}
	if job.Undone != 1203 || job.Applied != 0 || job.UndoConflicts != 0 {
		t.Fatalf("undone=%+v", job)
	}
	before, err := reopened.SessionManagement(ctx, "s000000")
	if err != nil || before.Revision != 2 || before.Archived || len(before.Tags) != 0 {
		t.Fatalf("restored=%+v error=%v", before, err)
	}
	again, err = reopened.UndoSessionBulk(ctx, job.ID, 100)
	if err != nil || !reflect.DeepEqual(job, again) {
		t.Fatalf("undo replay changed job: error=%v", err)
	}
	// Undo never returns to revision zero, preventing an ABA overwrite.
	if _, err = reopened.UpdateSessionManagement(ctx, "s000000", 0, SessionManagementUpdate{Title: "Stale editor"}); !errors.Is(err, ErrSessionManagementConflict) {
		t.Fatalf("stale editor accepted: %v", err)
	}
	if _, err = reopened.ApplySessionBulk(ctx, job.ID, 1); !errors.Is(err, ErrSessionManagementConflict) {
		t.Fatalf("apply after undo accepted: %v", err)
	}
	jobs, err := reopened.ListSessionBulk(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Undone != 1203 || len(jobs[0].Items) != 0 {
		t.Fatalf("jobs=%+v error=%v", jobs, err)
	}
}

func TestSessionBulkProtectionOptInsAndManualOrigin(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	seedBulkSummaries(t, store, 4)
	for _, row := range []struct {
		id string
		u  SessionManagementUpdate
	}{
		{"s000000", SessionManagementUpdate{Starred: true}},
		{"s000001", SessionManagementUpdate{Title: "Manually edited", Note: "Keep this note", Tags: []string{"existing"}}},
	} {
		if _, err := store.UpdateSessionManagement(ctx, row.id, 0, row.u); err != nil {
			t.Fatal(err)
		}
	}
	job := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true)})
	if job.Total != 4 || job.Protected != 2 || job.Pending != 2 {
		t.Fatalf("protected preview=%+v", job)
	}
	job = applyBulkAll(t, store, job.ID)
	if job.Applied != 2 || job.Protected != 2 {
		t.Fatalf("protected apply=%+v", job)
	}
	// A bulk write is not subsequently mistaken for a manual edit.
	second := bulkPlan(t, store, SessionBrowseOptions{Archived: "only"}, SessionBulkAction{Archived: bulkBool(true), AddTags: []string{"second"}})
	if second.Protected != 0 || second.Pending != 2 {
		t.Fatalf("bulk origin became manual: %+v", second)
	}
	second = applyBulkAll(t, store, second.ID)
	if second.Applied != 2 {
		t.Fatalf("second=%+v", second)
	}
	optIn := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true), IncludeStarred: true})
	if optIn.Protected != 2 {
		t.Fatalf("favorite opt-in must still respect manual protection: %+v", optIn)
	}
	optIn = bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true), IncludeEdited: true})
	if optIn.Protected != 1 || optIn.Pending != 1 {
		t.Fatalf("manual opt-in must still protect favorite: %+v", optIn)
	}
	all := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true), IncludeEdited: true, IncludeStarred: true, AddTags: []string{"batch"}})
	all = applyBulkAll(t, store, all.ID)
	if all.Applied != 2 || all.Protected != 0 {
		t.Fatalf("full opt-in=%+v", all)
	}
	manual, err := store.SessionManagement(ctx, "s000001")
	if err != nil || manual.Title != "Manually edited" || manual.Note != "Keep this note" || !reflect.DeepEqual(manual.Tags, []string{"batch", "existing"}) {
		t.Fatalf("manual state overwritten: %+v error=%v", manual, err)
	}
	if _, err = store.UndoSessionBulk(ctx, all.ID, 100); err != nil {
		t.Fatal(err)
	}
	protectedAgain := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true)})
	if protectedAgain.Protected != 2 {
		t.Fatalf("undo lost manual origin: %+v", protectedAgain)
	}
}

func TestSessionBulkFrozenConflictsAndPartialUndo(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	records := []Record{}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		records = append(records, managedSessionTestRecord("first-"+id, id))
	}
	if err := store.PutBatch(records); err != nil {
		t.Fatal(err)
	}
	job := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true), AddTags: []string{"batch"}})
	// Same-timestamp arrivals still count as activity.
	if err := store.PutBatch([]Record{managedSessionTestRecord("second-a", "a"), managedSessionTestRecord("new", "new")}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateSessionManagement(ctx, "b", 0, SessionManagementUpdate{Note: "Edited after preview"}); err != nil {
		t.Fatal(err)
	}
	job, err := store.ApplySessionBulk(ctx, job.ID, 3)
	if err != nil || job.Applied != 1 || job.Conflicts != 2 || job.Pending != 2 {
		t.Fatalf("partial=%+v error=%v", job, err)
	}
	if _, err = store.UpdateSessionManagement(ctx, "c", 1, SessionManagementUpdate{Title: "Later edit", Tags: []string{"batch"}, Archived: true}); err != nil {
		t.Fatal(err)
	}
	job, err = store.UndoSessionBulk(ctx, job.ID, 1)
	if err != nil || job.Status != "undone" || job.UndoConflicts != 1 || job.Cancelled != 2 || job.Conflicts != 2 {
		t.Fatalf("undo=%+v error=%v", job, err)
	}
	c, err := store.SessionManagement(ctx, "c")
	if err != nil || c.Title != "Later edit" || c.Revision != 2 {
		t.Fatalf("undo overwrote later edit: %+v error=%v", c, err)
	}
	for _, id := range []string{"a", "b", "d", "e", "new"} {
		m, readErr := store.SessionManagement(ctx, id)
		if readErr != nil || m.Archived {
			t.Fatalf("unexpected archived %s: %+v error=%v", id, m, readErr)
		}
	}
}

func TestSessionBulkTagsBoundsHostileIDsAndNoEvidenceMutation(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	id := `__proto__/' OR 1=1;--`
	if err := store.PutBatch([]Record{managedSessionTestRecord("r", id)}); err != nil {
		t.Fatal(err)
	}
	tags := []string{}
	for i := range 24 {
		tags = append(tags, fmt.Sprintf("tag%02d", i))
	}
	initial, err := store.UpdateSessionManagement(ctx, id, 0, SessionManagementUpdate{Title: "Manual title", Note: "Manual note", Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	var before bytes.Buffer
	if err = store.ExportSessionJSONL(ctx, id, &before); err != nil {
		t.Fatal(err)
	}
	var feedBefore int64
	if err = store.DB.QueryRow(`SELECT sequence FROM archive_ingest_clock WHERE id=1`).Scan(&feedBefore); err != nil {
		t.Fatal(err)
	}
	tooMany := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{AddTags: []string{"overflow"}})
	tooMany = applyBulkAll(t, store, tooMany.ID)
	if tooMany.Conflicts != 1 || tooMany.Items[0].Reason != "tag_limit" {
		t.Fatalf("tag overflow=%+v", tooMany)
	}
	merge := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{AddTags: []string{"replacement", "replacement"}, RemoveTags: []string{"tag00"}, Starred: bulkBool(true)})
	merge = applyBulkAll(t, store, merge.ID)
	m, err := store.SessionManagement(ctx, id)
	if err != nil || merge.Applied != 1 || m.Revision != initial.Revision+1 || len(m.Tags) != 24 || m.Title != initial.Title || m.Note != initial.Note || !m.Starred || !strings.Contains(strings.Join(m.Tags, ","), "replacement") {
		t.Fatalf("merged=%+v job=%+v error=%v", m, merge, err)
	}
	if _, err = store.UndoSessionBulk(ctx, merge.ID, 100); err != nil {
		t.Fatal(err)
	}
	m, err = store.SessionManagement(ctx, id)
	if err != nil || m.Revision != initial.Revision+2 || m.Title != initial.Title || m.Note != initial.Note || !reflect.DeepEqual(m.Tags, initial.Tags) || m.Starred {
		t.Fatalf("restored=%+v error=%v", m, err)
	}
	var after bytes.Buffer
	if err = store.ExportSessionJSONL(ctx, id, &after); err != nil {
		t.Fatal(err)
	}
	var feedAfter int64
	if err = store.DB.QueryRow(`SELECT sequence FROM archive_ingest_clock WHERE id=1`).Scan(&feedAfter); err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before.Bytes()) != sha256.Sum256(after.Bytes()) || feedBefore != feedAfter {
		t.Fatal("bulk operations changed raw export or ingestion feed")
	}
}

func TestSessionBulkInvalidActionsBoundsAndSelectionOverflow(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	for _, action := range []SessionBulkAction{
		{}, {IncludeEdited: true}, {AddTags: []string{"same"}, RemoveTags: []string{" same "}}, {AddTags: []string{"bad\x00tag"}}, {RemoveTags: []string{"a,b"}},
	} {
		if _, err := store.PlanSessionBulk(ctx, SessionBulkPlanRequest{Action: action}); !errors.Is(err, ErrInvalidSessionManagement) {
			t.Fatalf("invalid action accepted: %+v error=%v", action, err)
		}
	}
	for _, selection := range []SessionBrowseOptions{{View: "typo"}, {InactiveDays: -1}, {InactiveDays: 3651}, {Limit: 1001}, {Offset: -1}, {Filters: map[string]string{"bad\x00": "value"}}} {
		if _, err := store.PlanSessionBulk(ctx, SessionBulkPlanRequest{Selection: selection, Action: SessionBulkAction{Starred: bulkBool(true)}}); !errors.Is(err, ErrInvalidSessionManagement) {
			t.Fatalf("invalid selection accepted: %+v error=%v", selection, err)
		}
	}
	for _, id := range []string{"", "../job", "job\x00", strings.Repeat("x", 129)} {
		if _, err := store.SessionBulk(ctx, id); !errors.Is(err, ErrInvalidSessionManagement) {
			t.Fatalf("invalid id accepted: %q error=%v", id, err)
		}
	}
	if _, err := store.ApplySessionBulk(ctx, "missing", 100); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing job error=%v", err)
	}
	seedBulkSummaries(t, store, 100001)
	if _, err := store.PlanSessionBulk(ctx, SessionBulkPlanRequest{Selection: SessionBrowseOptions{Limit: 1}, Action: SessionBulkAction{Starred: bulkBool(true)}}); !errors.Is(err, ErrInvalidSessionManagement) {
		t.Fatalf("overflow selection accepted: error=%v", err)
	}
	jobs, err := store.ListSessionBulk(ctx)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("overflow left partial jobs: count=%d error=%v", len(jobs), err)
	}
	for _, limit := range []int{-1, 101} {
		if _, err = store.ApplySessionBulk(ctx, "missing", limit); !errors.Is(err, ErrInvalidSessionManagement) {
			t.Fatalf("invalid step accepted: %d error=%v", limit, err)
		}
	}
}

func TestSessionBulkPreviewAndStepAtProductionSessionScale(t *testing.T) {
	store := openManagedSessionTestStore(t)
	seedBulkSummaries(t, store, 15000)
	if _, err := store.DB.Exec(`INSERT INTO session_facets(session_id,name,value) SELECT session_id,'project.id','git:https://example.test/org/project.git' FROM session_summaries`); err != nil {
		t.Fatal(err)
	}
	// Populate the authoritative event indexes at ten source events per
	// session, including a move-away event in the previous-session index.
	if _, err := store.DB.Exec(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<9)
		INSERT INTO archive_ingest_events(sequence,request_id,session_id,previous_session_id,recorded_at)
		SELECT ROW_NUMBER() OVER(ORDER BY s.session_id,n.x),s.session_id||':'||n.x,
		CASE WHEN n.x=9 THEN 'moved-to:'||s.session_id ELSE s.session_id END,
		CASE WHEN n.x=9 THEN s.session_id ELSE '' END,s.last_at FROM session_summaries s CROSS JOIN n;
		UPDATE archive_ingest_clock SET sequence=(SELECT MAX(sequence) FROM archive_ingest_events) WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	job := bulkPlan(t, store, SessionBrowseOptions{View: "inactive", Filters: map[string]string{"project.id": "git:https://example.test/org/project.git"}}, SessionBulkAction{Archived: bulkBool(true)})
	previewDuration := time.Since(started)
	started = time.Now()
	job, err := store.ApplySessionBulk(context.Background(), job.ID, 100)
	stepDuration := time.Since(started)
	if err != nil || job.Total != 15000 || job.Applied != 100 || job.Pending != 14900 {
		t.Fatalf("scale job=%+v error=%v", job, err)
	}
	t.Logf("15000 indexed sessions / 150000 source events: atomic preview=%s; transactional 100-session step=%s", previewDuration, stepDuration)
}

func TestSessionBulkUpgradesExistingManualMetadata(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	store, err := OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	seedBulkSummaries(t, store, 1)
	if _, err = store.UpdateSessionManagement(ctx, "s000000", 0, SessionManagementUpdate{Title: "Before migration"}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec(`ALTER TABLE session_management DROP COLUMN manually_edited`); err != nil {
		t.Fatal(err)
	}
	if err = store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	job := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Archived: bulkBool(true)})
	if job.Protected != 1 || job.Pending != 0 {
		t.Fatalf("legacy manual metadata not protected: %+v", job)
	}
}

func TestSessionBulkStepRollbackAndConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	seedBulkSummaries(t, store, 203)
	job := bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{Starred: bulkBool(true)})
	if _, err := store.DB.Exec(`CREATE TRIGGER fixture_bulk_abort BEFORE UPDATE ON session_bulk_items WHEN NEW.status='applied' AND NEW.session_id='s000001' BEGIN SELECT RAISE(ABORT,'fixture abort'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySessionBulk(ctx, job.ID, 100); err == nil {
		t.Fatal("injected storage failure did not abort the step")
	}
	job, err := store.SessionBulk(ctx, job.ID)
	if err != nil || job.Pending != 203 || job.Applied != 0 || job.Status != "preview" {
		t.Fatalf("failed step was partially committed: %+v error=%v", job, err)
	}
	first, err := store.SessionManagement(ctx, "s000000")
	if err != nil || first.Revision != 0 || first.Starred {
		t.Fatalf("failed step changed metadata: %+v error=%v", first, err)
	}
	if _, err = store.DB.Exec(`DROP TRIGGER fixture_bulk_abort`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 3)
	for range 3 {
		go func() {
			_, applyErr := store.ApplySessionBulk(ctx, job.ID, 100)
			results <- applyErr
		}()
	}
	for range 3 {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	job, err = store.SessionBulk(ctx, job.ID)
	if err != nil || job.Applied != 203 || job.Pending != 0 || job.Status != "complete" {
		t.Fatalf("concurrent steps=%+v error=%v", job, err)
	}
	var invalid int
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM session_management WHERE revision<>1 OR starred<>1`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatalf("concurrent replay duplicated writes: invalid=%d error=%v", invalid, err)
	}
	job, err = store.UndoSessionBulk(ctx, job.ID, 1)
	if err != nil || job.Undone != 1 || job.Applied != 202 || job.Status != "undoing" {
		t.Fatalf("partial undo=%+v error=%v", job, err)
	}
	path := store.DBPath
	if err = store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	for {
		job, err = reopened.UndoSessionBulk(ctx, job.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "undone" {
			break
		}
	}
	if job.Undone != 203 || job.UndoConflicts != 0 {
		t.Fatalf("resumed undo=%+v", job)
	}
	if err = reopened.DB.QueryRow(`SELECT COUNT(*) FROM session_management WHERE revision<>2 OR starred<>0`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatalf("undo revisions invalid=%d error=%v", invalid, err)
	}
}
