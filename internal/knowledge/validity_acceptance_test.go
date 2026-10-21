package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/projectcontext"
)

// These tests use disposable repositories and synthetic conclusions only.
// JSON construction also checks the public field names used by model proposals.
func validityAcceptanceRelation(t *testing.T, p memory.MemoryProposal, kind string, targets ...memory.MemoryEntry) memory.MemoryProposal {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	relations := []map[string]string{}
	for _, target := range targets {
		relations = append(relations, map[string]string{"kind": kind, "target_id": target.ID, "target_revision": target.Revision})
	}
	fields["relations"] = relations
	raw, err = json.Marshal(fields)
	if err != nil || json.Unmarshal(raw, &p) != nil {
		t.Fatal("could not construct relation proposal", err)
	}
	return p
}

func validityAcceptanceQuery(t *testing.T, project, text, view string, limit int) memory.KnowledgeQuery {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"project_id": project, "global": project == "", "text": text, "view": view, "limit": limit})
	var q memory.KnowledgeQuery
	if err != nil || json.Unmarshal(raw, &q) != nil {
		t.Fatal("could not construct validity query", err)
	}
	return q
}

func validityAcceptanceState(t *testing.T, entry memory.MemoryEntry) (string, string) {
	t.Helper()
	raw, err := json.Marshal(entry)
	var view struct {
		SupersededBy string `json:"superseded_by"`
		Validity     struct {
			State string `json:"state"`
		} `json:"validity"`
	}
	if err != nil || json.Unmarshal(raw, &view) != nil {
		t.Fatal("could not inspect validity metadata", err)
	}
	return view.Validity.State, view.SupersededBy
}

func validityAcceptanceApprove(t *testing.T, s *Store, p memory.MemoryProposal) memory.MemoryEntry {
	t.Helper()
	draft, err := s.Propose(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := s.Review(context.Background(), Review{ID: draft.ID, ProjectID: draft.ProjectID, Global: draft.Global, ExpectedRevision: draft.Revision, Action: "approve", Note: "Human acceptance fixture review of evidence and relationship", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func validityAcceptanceEditMetadata(t *testing.T, s *Store, root string, entry memory.MemoryEntry, edit func(map[string]any)) memory.MemoryEntry {
	t.Helper()
	if !entry.Global {
		t.Fatal("metadata helper only edits a disposable global entry")
	}
	path := activePath(entry)
	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseDocument(raw, path, true, "")
	if err != nil {
		t.Fatal(err)
	}
	edit(doc.meta)
	raw, err = markdown(doc.meta, doc.body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), raw, 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "--", path)
	git(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "human fixture metadata review")
	updated, found, err := s.findActive(context.Background(), root, "", true, entry.ID)
	if err != nil || !found {
		t.Fatal("edited fixture entry is unavailable", err)
	}
	return updated.entry
}

func validityAcceptanceSecondSource(t *testing.T, raw *archive.Store, engine *memory.Engine) memory.Citation {
	t.Helper()
	if err := raw.PutBatch([]archive.Record{{RequestID: "validity-replacement-source", SessionID: "s", Facets: map[string][]string{"project.id": {"p"}}, OriginalRequest: []byte(`{"messages":[{"role":"assistant","content":"independentreplacementneedle: the independently reviewed successor uses the revised policy."}]}`)}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		n, err := engine.Sync(context.Background(), 16)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	result, err := engine.Search(context.Background(), memory.Query{Text: "independentreplacementneedle", ProjectID: "p"})
	if err != nil || len(result.Hits) != 1 {
		t.Fatal("independent successor evidence was not projected", err)
	}
	return result.Hits[0].Citation
}

func TestValidityAcceptanceReplacementNeverResurrectsRetiredConclusion(t *testing.T) {
	for _, kind := range []string{"supersedes", "corrects"} {
		for _, invalidation := range []string{"source", "review_due"} {
			t.Run(kind+"_"+invalidation, func(t *testing.T) {
				s, raw, engine, root, _, originalCitation := fixture(t)
				clock := time.Date(2032, 1, 15, 12, 0, 0, 0, time.UTC)
				s.now = func() time.Time { return clock }
				oldProposal := proposal(originalCitation)
				oldProposal.Statement = "retiredneedle: the original reviewed policy uses the earlier setting."
				old := validityAcceptanceApprove(t, s, oldProposal)
				old = validityAcceptanceEditMetadata(t, s, root, old, func(meta map[string]any) { meta["review_after"] = "2099-01-01" })
				newCitation := validityAcceptanceSecondSource(t, raw, engine)
				p := proposal(newCitation)
				p.OperationID = "relation-successor-001"
				p.Statement = "successorneedle: the newly reviewed policy uses the revised setting."
				p = validityAcceptanceRelation(t, p, kind, old)
				draft, err := s.Propose(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				before, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "", "retiredneedle", "current", 1))
				if err != nil || len(before.Entries) != 1 || before.Entries[0].ID != old.ID {
					t.Fatal("a proposed relationship prematurely retired confirmed knowledge", err)
				}
				next, err := s.Review(context.Background(), Review{ID: draft.ID, Global: true, ExpectedRevision: draft.Revision, Action: "approve", Note: "Human reviewed the replacement and the pinned prior revision", Confirmed: true})
				if err != nil {
					t.Fatal(err)
				}
				after, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "", "retiredneedle", "current", 1))
				if err != nil || len(after.Entries) != 0 {
					t.Fatal("text/limit filtering resurrected a persistently retired conclusion", err)
				}
				if after.States["superseded"] != 1 || after.States["current"] != 1 {
					t.Fatal("text filtering concealed canonical lifecycle classifications")
				}
				current, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "current", 1))
				if err != nil || len(current.Entries) != 1 || current.Entries[0].ID != next.ID {
					t.Fatal("confirmed successor did not become current", err)
				}
				if invalidation == "source" {
					if _, err := raw.DB.Exec(`UPDATE records SET response_preview='changed successor evidence revision' WHERE request_id='validity-replacement-source'`); err != nil {
						t.Fatal(err)
					}
				} else {
					clock = clock.AddDate(0, 4, 0)
				}
				current, err = s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "current", 100))
				if err != nil || len(current.Entries) != 0 {
					t.Fatal("an unavailable or overdue successor caused old knowledge to reactivate", err)
				}
				all, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "all", 100))
				if err != nil || len(all.Entries) != 2 {
					t.Fatal("lifecycle inspection lost retained conclusions", err)
				}
				for _, entry := range all.Entries {
					state, by := validityAcceptanceState(t, entry)
					if entry.ID == old.ID && (entry.Status != "superseded" || state != "superseded" || by != next.ID || entry.Statement != old.Statement) {
						t.Fatal("retirement metadata or the original conclusion was not preserved")
					}
					if entry.ID == next.ID && ((invalidation == "source" && state != "source_unavailable") || (invalidation == "review_due" && state != "review_due")) {
						t.Fatal("successor validity failure was hidden or mislabeled")
					}
				}
				if _, err := engine.ReadSource(context.Background(), originalCitation); err != nil {
					t.Fatal("curated retirement altered the old raw evidence", err)
				}
				var records int
				if err := raw.DB.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&records); err != nil || records != 2 {
					t.Fatal("relationship handling removed raw history", err)
				}
			})
		}
	}
}

func TestValidityAcceptanceConflictRequiresExplicitReviewedResolution(t *testing.T) {
	s, _, _, _, _, citation := fixture(t)
	first := validityAcceptanceApprove(t, s, proposal(citation))
	p := proposal(citation)
	p.OperationID = "conflict-candidate-001"
	p.Statement = "The alternative reviewed conclusion explicitly conflicts with the existing policy."
	p = validityAcceptanceRelation(t, p, "conflicts_with", first)
	second := validityAcceptanceApprove(t, s, p)
	current, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "current", 100))
	if err != nil || len(current.Entries) != 0 {
		t.Fatal("an unresolved conflict was silently presented as current knowledge", err)
	}
	all, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "all", 100))
	if err != nil || len(all.Entries) != 2 {
		t.Fatal("conflict inspection did not retain both conclusions", err)
	}
	for _, entry := range all.Entries {
		state, _ := validityAcceptanceState(t, entry)
		if entry.Status != "disputed" || state != "disputed" {
			t.Fatal("conflict endpoints were not both explicitly disputed")
		}
		if entry.ID == first.ID {
			first = entry
		} else if entry.ID == second.ID {
			second = entry
		}
	}
	resolution := proposal(citation)
	resolution.OperationID = "conflict-resolution-001"
	resolution.Statement = "Human-reviewed resolution replaces both disputed conclusions with this explicit policy."
	resolution = validityAcceptanceRelation(t, resolution, "corrects", first, second)
	resolved := validityAcceptanceApprove(t, s, resolution)
	current, err = s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "current", 100))
	if err != nil || len(current.Entries) != 1 || current.Entries[0].ID != resolved.ID {
		t.Fatal("explicit reviewed resolution did not become the sole current conclusion", err)
	}
	all, err = s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "all", 100))
	if err != nil || len(all.Entries) != 3 {
		t.Fatal(err)
	}
	for _, entry := range all.Entries {
		if entry.ID != resolved.ID {
			state, by := validityAcceptanceState(t, entry)
			if entry.Status != "superseded" || state != "superseded" || by != resolved.ID {
				t.Fatal("resolved conflict left an ambiguous active endpoint")
			}
		}
	}
}

func TestValidityAcceptanceProjectContextDisclosesDisputeAndArchiveDoesNotPickWinner(t *testing.T) {
	s, _, engine, _, projectRoot, citation := fixture(t)
	p := proposal(citation)
	p.Global, p.ProjectID = false, "p"
	p.Statement = "Architecture evidence supports the first explicitly reviewed queue policy."
	first := validityAcceptanceApprove(t, s, p)
	p.OperationID = "project-conflict-candidate-001"
	p.Statement = "Architecture evidence supports an incompatible second queue policy requiring human resolution."
	p = validityAcceptanceRelation(t, p, "conflicts_with", first)
	second := validityAcceptanceApprove(t, s, p)
	packets, err := projectcontext.New(projectcontext.Config{Projects: map[string]string{"p": projectRoot}, Scope: memory.Scope{ProjectID: "p"}, Source: engine, Knowledge: s})
	if err != nil {
		t.Fatal(err)
	}
	for _, archiveOne := range []bool{false, true} {
		if archiveOne {
			if _, err := s.Review(context.Background(), Review{ID: second.ID, ProjectID: "p", ExpectedRevision: second.Revision, Action: "archive", Note: "Human archived one disputed claim without endorsing the other", Confirmed: true}); err != nil {
				t.Fatal(err)
			}
		}
		packet, err := packets.ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "architecture"})
		if err != nil || len(packet.Sections) != 1 || packet.Coverage.Knowledge != "partial" {
			t.Fatal("project context hid unresolved knowledge coverage", err)
		}
		if len(packet.Sections[0].Memories) != 0 || packet.Sections[0].State != "incomplete" || !strings.Contains(strings.Join(packet.Coverage.Issues, ","), "reviewed_knowledge_has_unresolved_conflicts") {
			t.Fatal("project context promoted a disputed claim or reported an ordinary empty match")
		}
		current, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "p", "", "current", 100))
		if err != nil || len(current.Entries) != 0 || current.States["disputed"] == 0 {
			t.Fatal("archiving one side silently approved the remaining disputed conclusion", err)
		}
	}
}

func TestValidityAcceptanceTargetsArePinnedAndScopedBeforeGitWrites(t *testing.T) {
	for _, failure := range []string{"stale_proposal", "stale_review", "invalid_source", "cross_scope"} {
		t.Run(failure, func(t *testing.T) {
			s, raw, engine, root, projectRoot, citation := fixture(t)
			base := proposal(citation)
			if failure == "cross_scope" {
				base.Global, base.ProjectID = false, "p"
			}
			target := validityAcceptanceApprove(t, s, base)
			p := proposal(validityAcceptanceSecondSource(t, raw, engine))
			p.OperationID = "guarded-replacement-001"
			p.Statement = "The proposed replacement must preserve scope and pinned revision checks."
			if failure == "stale_proposal" {
				target.Revision = strings.Repeat("0", 64)
			}
			p = validityAcceptanceRelation(t, p, "supersedes", target)
			if failure == "stale_proposal" || failure == "cross_scope" {
				head, projectHead := git(t, root, "rev-parse", "HEAD"), git(t, projectRoot, "rev-parse", "HEAD")
				if _, err := s.Propose(context.Background(), p); err == nil {
					t.Fatal("stale or out-of-scope target was admitted as a relation")
				}
				if git(t, root, "rev-parse", "HEAD") != head || git(t, projectRoot, "rev-parse", "HEAD") != projectHead {
					t.Fatal("rejected relationship changed an authority")
				}
				return
			}
			draft, err := s.Propose(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "stale_review" {
				validityAcceptanceEditMetadata(t, s, root, target, func(meta map[string]any) { meta["review_note"] = "A later human review changes the target revision." })
			} else {
				if _, err := raw.DB.Exec(`UPDATE records SET response_preview='source changed before confirmation' WHERE request_id='validity-replacement-source'`); err != nil {
					t.Fatal(err)
				}
			}
			head := git(t, root, "rev-parse", "HEAD")
			if _, err := s.Review(context.Background(), Review{ID: draft.ID, Global: true, ExpectedRevision: draft.Revision, Action: "approve", Note: "Review must recheck the target and evidence", Confirmed: true}); err == nil {
				t.Fatal("changed relation target or evidence was approved")
			}
			if git(t, root, "rev-parse", "HEAD") != head {
				t.Fatal("failed review committed a partial replacement")
			}
			current, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "current", 100))
			if err != nil || len(current.Entries) != 1 || current.Entries[0].ID != target.ID {
				t.Fatal("failed replacement retired the original conclusion", err)
			}
		})
	}
}

func TestValidityAcceptanceSharedRootDoesNotGrantCrossProjectRelations(t *testing.T) {
	s, raw, engine, _, projectRoot, citation := fixture(t)
	p := proposal(citation)
	p.Global, p.ProjectID = false, "p"
	target := validityAcceptanceApprove(t, s, p)
	shared, err := New(Config{Projects: map[string]string{"p": projectRoot, "q": projectRoot}, Scope: memory.Scope{AllowAll: true}, Source: engine})
	if err != nil {
		// Refusing ambiguous project authority registrations is a valid boundary.
		return
	}
	if err := raw.PutBatch([]archive.Record{{RequestID: "validity-other-project-source", SessionID: "q-session", Facets: map[string][]string{"project.id": {"q"}}, OriginalRequest: []byte(`{"messages":[{"role":"assistant","content":"otherprojectneedle: a separate project has independently reviewed evidence."}]}`)}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		n, err := engine.Sync(context.Background(), 16)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	source, err := engine.Search(context.Background(), memory.Query{Text: "otherprojectneedle", ProjectID: "q"})
	if err != nil || len(source.Hits) != 1 {
		t.Fatal("other project fixture did not get separate authorized evidence", err)
	}
	listed, err := shared.Memories(context.Background(), validityAcceptanceQuery(t, "q", "", "all", 100))
	if err == nil && len(listed.Entries) != 0 {
		t.Error("a shared Git root relabeled another project's canonical conclusions")
	}
	p = proposal(source.Hits[0].Citation)
	p.OperationID = "other-project-replacement-001"
	p.Global, p.ProjectID = false, "q"
	p.Statement = "A separate project's candidate must not retire a conclusion from the first project."
	p = validityAcceptanceRelation(t, p, "supersedes", target)
	head := git(t, projectRoot, "rev-parse", "HEAD")
	if _, err := shared.Propose(context.Background(), p); err == nil {
		t.Error("a shared Git root granted cross-project replacement capability")
	}
	if git(t, projectRoot, "rev-parse", "HEAD") != head {
		t.Fatal("rejected cross-project relationship changed the canonical repository")
	}
}

func TestValidityAcceptanceMultiFileReviewPreservesDirtyAndStagedWork(t *testing.T) {
	for _, failure := range []string{"dirty_target", "commit_hook"} {
		t.Run(failure, func(t *testing.T) {
			s, _, _, root, _, citation := fixture(t)
			target := validityAcceptanceApprove(t, s, proposal(citation))
			p := proposal(citation)
			p.OperationID = "atomic-replacement-001"
			p.Statement = "A multi-file replacement must commit atomically without touching unrelated user work."
			p = validityAcceptanceRelation(t, p, "corrects", target)
			draft, err := s.Propose(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			unrelated := filepath.Join(root, "unrelated-review.txt")
			if err := os.WriteFile(unrelated, []byte("human staged work\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, root, "add", "unrelated-review.txt")
			targetFile := filepath.Join(root, activePath(target))
			if failure == "dirty_target" {
				file, err := os.OpenFile(targetFile, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("\nHUMAN_UNCOMMITTED_NOTE\n"); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(root, ".git/hooks/pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			head := git(t, root, "rev-parse", "HEAD")
			status, index := git(t, root, "status", "--porcelain", "--untracked-files=all"), git(t, root, "diff", "--cached", "--binary")
			before, err := os.ReadFile(targetFile)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Review(context.Background(), Review{ID: draft.ID, Global: true, ExpectedRevision: draft.Revision, Action: "approve", Note: "Approval must not absorb dirty work", Confirmed: true}); err == nil {
				t.Fatal("dirty or rejected multi-file review reported success")
			}
			after, err := os.ReadFile(targetFile)
			if err != nil || string(after) != string(before) || git(t, root, "rev-parse", "HEAD") != head || git(t, root, "status", "--porcelain", "--untracked-files=all") != status || git(t, root, "diff", "--cached", "--binary") != index {
				t.Fatal("failed review changed the target, HEAD or unrelated work", err)
			}
			if _, err := os.Stat(filepath.Join(root, activePath(draft))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed review left a half-promoted successor", err)
			}
		})
	}
}

func TestValidityAcceptanceLegacyReviewDatesAndExplicitInspection(t *testing.T) {
	s, _, _, root, _, citation := fixture(t)
	clock := time.Date(2032, 2, 28, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	entry := validityAcceptanceApprove(t, s, proposal(citation))
	entry = validityAcceptanceEditMetadata(t, s, root, entry, func(meta map[string]any) {
		delete(meta, "review_after")
		delete(meta, "relations")
		delete(meta, "superseded_by")
	})
	current, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "", 100))
	if err != nil || len(current.Entries) != 1 || current.Entries[0].ID != entry.ID {
		t.Fatal("legacy reviewed entries without new lifecycle fields became unreadable", err)
	}
	entry = validityAcceptanceEditMetadata(t, s, root, entry, func(meta map[string]any) { meta["review_after"] = "2032-02-28" })
	current, err = s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "current", 100))
	if err != nil || len(current.Entries) != 1 {
		t.Fatal("review deadline stopped being inclusive for its UTC calendar day", err)
	}
	clock = clock.AddDate(0, 0, 1)
	current, err = s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "current", 100))
	if err != nil || len(current.Entries) != 0 {
		t.Fatal("overdue review was still returned as current knowledge", err)
	}
	all, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "all", 100))
	if err != nil || len(all.Entries) != 1 {
		t.Fatal("explicit inspection lost review-due knowledge", err)
	}
	if state, _ := validityAcceptanceState(t, all.Entries[0]); state != "review_due" {
		t.Fatal("review freshness was not exposed separately from the persisted conclusion")
	}
	validityAcceptanceEditMetadata(t, s, root, entry, func(meta map[string]any) { meta["review_after"] = "not-a-calendar-date" })
	current, err = s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "current", 100))
	if err != nil || len(current.Entries) != 0 {
		t.Fatal("malformed freshness metadata was treated as valid indefinitely", err)
	}
	all, err = s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "all", 100))
	if err != nil || len(all.Entries) != 1 {
		t.Fatal("malformed metadata was hidden from explicit inspection", err)
	}
	if state, _ := validityAcceptanceState(t, all.Entries[0]); state != "invalid_metadata" {
		t.Fatal("invalid dates were mislabeled as ordinary expiry")
	}
	if _, err := s.Memories(context.Background(), validityAcceptanceQuery(t, "", "", "activate", 100)); err == nil {
		t.Fatal("unknown validity view was silently accepted")
	}
	drafts := validityAcceptanceQuery(t, "", "", "all", 100)
	drafts.Drafts = true
	if _, err := s.Memories(context.Background(), drafts); err == nil {
		t.Fatal("unreviewed drafts were allowed to mix with canonical lifecycle inspection")
	}
}
