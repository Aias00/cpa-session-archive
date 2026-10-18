package knowledge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/storage"
)

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func repository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-q")
	git(t, root, "config", "user.name", "Knowledge Test")
	git(t, root, "config", "user.email", "test@example.invalid")
	os.WriteFile(filepath.Join(root, "README.md"), []byte("canonical knowledge\n"), 0600)
	git(t, root, "add", "README.md")
	git(t, root, "commit", "-qm", "initial")
	return root
}
func fixture(t *testing.T) (*Store, *archive.Store, *memory.Engine, string, string, memory.Citation) {
	t.Helper()
	kb := repository(t)
	project := repository(t)
	dir := t.TempDir()
	raw, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.DB.Close() })
	if err := raw.PutBatch([]archive.Record{{RequestID: "source", SessionID: "s", Facets: map[string][]string{"project.id": {"p"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"Retain reviewed Git conclusions as the memory authority."}]}`)}}); err != nil {
		t.Fatal(err)
	}
	engine, err := memory.Open(filepath.Join(dir, "memory.sqlite"), storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	for {
		n, err := engine.Sync(context.Background(), 8)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	hits, err := engine.Search(context.Background(), memory.Query{Text: "Git"})
	if err != nil || len(hits.Hits) != 1 {
		t.Fatal(err, hits)
	}
	store, err := New(Config{GlobalRepository: kb, Projects: map[string]string{"p": project}, Scope: memory.Scope{AllowAll: true}, Source: engine})
	if err != nil {
		t.Fatal(err)
	}
	return store, raw, engine, kb, project, hits.Hits[0].Citation
}
func proposal(c memory.Citation) memory.MemoryProposal {
	return memory.MemoryProposal{OperationID: "operation-001", Global: true, Kind: "decision", Title: "Review before reuse", Statement: "Reusable knowledge must remain reviewed and Git-backed.", Sources: []memory.Citation{c}}
}
func TestGitMemoryLifecyclePreservesOtherStagedChanges(t *testing.T) {
	s, _, _, kb, _, cite := fixture(t)
	ctx := context.Background()
	os.WriteFile(filepath.Join(kb, "unrelated.txt"), []byte("staged human work\n"), 0600)
	git(t, kb, "add", "unrelated.txt")
	p := proposal(cite)
	draft, err := s.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Status != "draft" || draft.Revision == "" || draft.Commit == "" {
		t.Fatal(draft)
	}
	staged := git(t, kb, "diff", "--cached", "--name-only")
	if staged != "unrelated.txt" {
		t.Fatalf("staged work changed: %s", staged)
	}
	again, err := s.Propose(ctx, p)
	if err != nil || again.Revision != draft.Revision || again.Commit != draft.Commit {
		t.Fatalf("non-idempotent: %+v %v", again, err)
	}
	list, err := s.Memories(ctx, memory.KnowledgeQuery{Global: true})
	if err != nil || len(list.Entries) != 0 {
		t.Fatalf("draft activated %+v %v", list, err)
	}
	if _, err := s.Review(ctx, Review{ID: draft.ID, Global: true, ExpectedRevision: draft.Revision, Action: "approve", Note: "Reviewed the cited evidence"}); err == nil {
		t.Fatal("unattested approval accepted")
	}
	active, err := s.Review(ctx, Review{ID: draft.ID, Global: true, ExpectedRevision: draft.Revision, Action: "approve", Note: "Reviewed the cited evidence", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	list, err = s.Memories(ctx, memory.KnowledgeQuery{Global: true, Text: "reviewed"})
	if err != nil || len(list.Entries) != 1 || list.Entries[0].Status != "verified" {
		t.Fatalf("%+v %v", list, err)
	}
	p.ID = active.ID
	p.OperationID = "correction-002"
	p.BaseRevision = active.Revision
	p.Statement = "Corrected knowledge requires explicit evidence review and Git history."
	correction, err := s.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := s.Memories(ctx, memory.KnowledgeQuery{Global: true})
	if prior.Entries[0].Statement != active.Statement {
		t.Fatal("draft correction changed active memory")
	}
	corrected, err := s.Review(ctx, Review{ID: correction.ID, Global: true, ExpectedRevision: correction.Revision, Action: "approve", Note: "Reviewed the corrected claim", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if corrected.Statement == active.Statement || corrected.Revision == active.Revision {
		t.Fatal("correction missing")
	}
	archived, err := s.Review(ctx, Review{ID: corrected.ID, Global: true, ExpectedRevision: corrected.Revision, Action: "archive", Note: "Retire this test decision", Confirmed: true})
	if err != nil || archived.Status != "archived" {
		t.Fatal(archived, err)
	}
	list, err = s.Memories(ctx, memory.KnowledgeQuery{Global: true})
	if err != nil || len(list.Entries) != 0 {
		t.Fatal(list, err)
	}
	if git(t, kb, "diff", "--cached", "--name-only") != "unrelated.txt" {
		t.Fatal("human index altered")
	}
}
func TestGitMemoryScopeSourceValidationAndOptimisticRevision(t *testing.T) {
	s, raw, engine, kb, project, cite := fixture(t)
	ctx := context.Background()
	p := proposal(cite)
	d, err := s.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	changed := p
	changed.ID = d.ID
	changed.ExpectedRevision = "wrong"
	changed.Statement = "A changed statement still needs a valid revision."
	if _, err := s.Propose(ctx, changed); err == nil {
		t.Fatal("stale edit accepted")
	}
	changed.ExpectedRevision = d.Revision
	changed.OperationID = "edit-operation"
	d2, err := s.Propose(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(ctx, Review{ID: d.ID, Global: true, ExpectedRevision: d.Revision, Action: "approve", Note: "Review", Confirmed: true}); err == nil {
		t.Fatal("stale review accepted")
	}
	raw.DB.Exec(`DELETE FROM records WHERE request_id='source'`)
	if _, err := s.Review(ctx, Review{ID: d2.ID, Global: true, ExpectedRevision: d2.Revision, Action: "approve", Note: "Review", Confirmed: true}); err == nil {
		t.Fatal("deleted evidence activated")
	}
	scoped, err := New(Config{GlobalRepository: kb, Projects: map[string]string{"p": project}, Scope: memory.Scope{ProjectID: "p"}, Source: engine})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Memories(ctx, memory.KnowledgeQuery{Global: true}); err == nil {
		t.Fatal("global scope escaped")
	}
	if _, err := scoped.Memories(ctx, memory.KnowledgeQuery{ProjectID: "q"}); err == nil {
		t.Fatal("project scope escaped")
	}
}
func TestProjectMemoryPreservesContextAndFailsClosedOnDirtyOwnedFile(t *testing.T) {
	s, _, _, _, project, cite := fixture(t)
	ctx := context.Background()
	contextPath := filepath.Join(project, ".codex/ai-context.yaml")
	os.MkdirAll(filepath.Dir(contextPath), 0700)
	os.WriteFile(contextPath, []byte("project: test-project\nglobal_knowledge: [canonical-memory-authority]\nlocal_exceptions: []\n"), 0600)
	git(t, project, "add", ".codex/ai-context.yaml")
	git(t, project, "commit", "-qm", "project context")
	p := proposal(cite)
	p.Global = false
	p.ProjectID = "p"
	p.Kind = "fact"
	d, err := s.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	active, err := s.Review(ctx, Review{ID: d.ID, ProjectID: "p", ExpectedRevision: d.Revision, Action: "approve", Note: "Reviewed project fact", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(contextPath)
	if !strings.Contains(string(text), "canonical-memory-authority") {
		t.Fatal("context overwritten")
	}
	os.WriteFile(contextPath, append(text, []byte("# uncommitted human edit\n")...), 0600)
	if _, err := s.Review(ctx, Review{ID: active.ID, ProjectID: "p", ExpectedRevision: active.Revision, Action: "archive", Note: "Reviewed archive", Confirmed: true}); err == nil {
		t.Fatal("unreviewed local context was committed")
	}
	list, err := s.Memories(ctx, memory.KnowledgeQuery{ProjectID: "p"})
	if err != nil || len(list.Entries) != 1 {
		t.Fatal("committed HEAD was not used", list, err)
	}
}

func TestActiveGitMemoryStopsRecallingInvalidEvidence(t *testing.T) {
	s, raw, _, _, _, cite := fixture(t)
	ctx := context.Background()
	draft, err := s.Propose(ctx, proposal(cite))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(ctx, Review{ID: draft.ID, Global: true, ExpectedRevision: draft.Revision, Action: "approve", Note: "Reviewed source", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	raw.DB.Exec(`UPDATE records SET response_preview='changed evidence revision' WHERE request_id='source'`)
	list, err := s.Memories(ctx, memory.KnowledgeQuery{Global: true})
	if err != nil || len(list.Entries) != 0 || list.Unavailable != 1 {
		t.Fatalf("stale active evidence disclosed: %+v %v", list, err)
	}
}
func TestGitMemoryCommitFailurePreservesWorkingTreeAndOtherIndex(t *testing.T) {
	s, _, _, kb, _, cite := fixture(t)
	os.WriteFile(filepath.Join(kb, "unrelated.txt"), []byte("user staged work"), 0600)
	git(t, kb, "add", "unrelated.txt")
	hook := filepath.Join(kb, ".git/hooks/pre-commit")
	os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0700)
	head := git(t, kb, "rev-parse", "HEAD")
	if _, err := s.Propose(context.Background(), proposal(cite)); err == nil {
		t.Fatal("rejected Git commit reported successful")
	}
	if git(t, kb, "rev-parse", "HEAD") != head || git(t, kb, "diff", "--cached", "--name-only") != "unrelated.txt" {
		t.Fatal("Git state changed after failed commit")
	}
	if len(git(t, kb, "status", "--porcelain", "--untracked-files=all", "--", "inbox")) != 0 {
		t.Fatal("failed candidate left visible work")
	}
}
