package archive

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestAutomaticSessionViewsUseIndexedExplicitEvidence(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	now := time.Now().UTC()
	type fixture struct {
		id     string
		days   int
		facets map[string][]string
	}
	fixtures := []fixture{
		{"main", 1, map[string][]string{"thread.source": {"user"}, "project.id": {"git:https://example.test/a/repo.git"}}},
		{"mixed-main", 2, map[string][]string{"thread.source": {"user", "system"}, "project.id": {"git:https://example.test/b/repo.git"}}},
		{"native-main", 50, map[string][]string{"thread.source": {"native-history"}, "native.agent_role": {"main"}, "project.path": {"/work/repo"}}},
		{"unknown", 60, map[string][]string{"project.name": {"repo"}}},
		{"subagent", 60, map[string][]string{"thread.source": {"native-history"}, "native.agent_role": {"subagent"}, "native.parent_id": {"parent"}, "project.id": {"native:device:unassigned"}}},
		{"parent-agent", 60, map[string][]string{"native.parent_agent_id": {"parent-agent"}, "project.id": {"p"}}},
		{"branch", 60, map[string][]string{"native.parent_id": {"parent"}, "project.id": {"p"}}},
		{"conflicting-role", 1, map[string][]string{"thread.source": {"user"}, "native.agent_role": {"subagent"}, "project.id": {"p"}}},
		{"system", 60, map[string][]string{"thread.source": {"system"}, "workspace.id": {"workspace:exact"}}},
		{"system-unknown", 60, map[string][]string{"thread.source": {"system", "unknown"}, "git.repository": {"https://example.test/org/repo.git"}}},
		{"prewarm", 60, map[string][]string{"request.kind": {"prewarm"}, "git.remote": {"https://example.test/org/repo.git"}}},
		{"prewarm-mixed", 60, map[string][]string{"request.kind": {"prewarm", "turn"}, "workspace.path": {"/full/workspace/repo"}}},
		{"ambiguous", 60, map[string][]string{"project.id": {"native:device:ambiguous:session"}, "project.path": {"/work/repo"}}},
		{"identity-conflict", 60, map[string][]string{"native.identity_conflict": {"true"}, "project.id": {"git:https://example.test/org/repo.git"}}},
		{"blank", 60, map[string][]string{"project.path": {"   "}}},
		{"relative", 60, map[string][]string{"project.path": {"repo"}}},
		{"windows", 60, map[string][]string{"project.path": {`C:\work\repo`}}},
		{"unc", 60, map[string][]string{"project.path": {`\\server\work\repo`}}},
	}
	for _, item := range fixtures {
		record := managedSessionTestRecord(item.id, item.id)
		record.Facets = item.facets
		record.StartedAt, record.CompletedAt = now.AddDate(0, 0, -item.days), now.AddDate(0, 0, -item.days)
		if err := store.PutBatch([]Record{record}); err != nil {
			t.Fatal(err)
		}
	}
	checks := []struct {
		view string
		want []string
	}{
		{"main", []string{"main", "mixed-main", "native-main"}},
		{"subagent", []string{"subagent", "parent-agent", "conflicting-role"}},
		{"system", []string{"system"}},
		{"prewarm", []string{"prewarm"}},
		{"recent", []string{"main", "mixed-main", "conflicting-role"}},
		{"unassigned", []string{"unknown", "subagent", "ambiguous", "identity-conflict", "blank", "relative"}},
	}
	for _, check := range checks {
		page, err := store.BrowseSessions(ctx, SessionBrowseOptions{View: check.view})
		if err != nil {
			t.Fatalf("%s: %v", check.view, err)
		}
		ids := []string{}
		for _, row := range page.Sessions {
			ids = append(ids, row.SessionID)
		}
		sort.Strings(ids)
		sort.Strings(check.want)
		if !reflect.DeepEqual(ids, check.want) || page.Total != len(check.want) {
			t.Fatalf("%s got=%v total=%d want=%v", check.view, ids, page.Total, check.want)
		}
		preview := bulkPlan(t, store, SessionBrowseOptions{View: check.view, Limit: 1, Offset: 1}, SessionBulkAction{AddTags: []string{"view-scope"}})
		if preview.Total != page.Total {
			t.Fatalf("view %s browse=%d frozen=%d", check.view, page.Total, preview.Total)
		}
		frozen := []string{}
		for _, row := range preview.Items {
			frozen = append(frozen, row.SessionID)
		}
		sort.Strings(frozen)
		if !reflect.DeepEqual(ids, frozen) {
			t.Fatalf("view %s freeze mismatch: browse=%v frozen=%v", check.view, ids, frozen)
		}
	}
	page, err := store.BrowseSessions(ctx, SessionBrowseOptions{View: "inactive", InactiveDays: 45})
	if err != nil || page.Total != len(fixtures)-3 {
		t.Fatalf("inactive=%d want=%d error=%v", page.Total, len(fixtures)-3, err)
	}
	page, err = store.BrowseSessions(ctx, SessionBrowseOptions{View: "inactive", InactiveDays: 55})
	if err != nil || page.Total != len(fixtures)-4 {
		t.Fatalf("inactive55=%d want=%d error=%v", page.Total, len(fixtures)-4, err)
	}
	// Two repositories with the same final directory name retain exact scopes.
	page, err = store.BrowseSessions(ctx, SessionBrowseOptions{Filters: map[string]string{"project.id": "git:https://example.test/a/repo.git"}})
	if err != nil || page.Total != 1 || page.Sessions[0].SessionID != "main" {
		t.Fatalf("exact project filter=%+v error=%v", page, err)
	}
}

func TestSessionBulkUndoRestoreProtectsNewActivityAndJobListIsBounded(t *testing.T) {
	ctx := context.Background()
	store := openManagedSessionTestStore(t)
	if err := store.PutBatch([]Record{managedSessionTestRecord("r1", "session")}); err != nil {
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
	if err := store.PutBatch([]Record{managedSessionTestRecord("r2", "session")}); err != nil {
		t.Fatal(err)
	}
	job, err := store.UndoSessionBulk(ctx, job.ID, 100)
	if err != nil || job.UndoConflicts != 1 || job.Items[0].Reason != "new_activity" {
		t.Fatalf("undo restore=%+v error=%v", job, err)
	}
	m, err := store.SessionManagement(ctx, "session")
	if err != nil || m.Archived {
		t.Fatalf("active session rearchived: %+v error=%v", m, err)
	}
	for range 21 {
		bulkPlan(t, store, SessionBrowseOptions{}, SessionBulkAction{AddTags: []string{"batch"}})
	}
	jobs, err := store.ListSessionBulk(ctx)
	if err != nil || len(jobs) != 20 {
		t.Fatalf("list count=%d error=%v", len(jobs), err)
	}
	old, err := store.SessionBulk(ctx, job.ID)
	if err != nil || old.ID != job.ID {
		t.Fatalf("old job cannot be loaded directly: error=%v", err)
	}
}
