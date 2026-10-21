package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memory"
)

func relationProposal(citation memory.Citation, id string, global bool) memory.MemoryProposal {
	p := proposal(citation)
	p.ID = id
	p.OperationID = "relation-operation-" + id
	p.Global = global
	if !global {
		p.ProjectID = "p"
	}
	p.Title = "Reviewed " + id
	p.Statement = "Synthetic independently reviewed conclusion for " + id + "."
	return p
}

func approveRelationEntry(t *testing.T, s *Store, p memory.MemoryProposal) memory.MemoryEntry {
	t.Helper()
	draft, err := s.Propose(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := s.Review(context.Background(), Review{ID: draft.ID, Global: p.Global, ProjectID: p.ProjectID, ExpectedRevision: draft.Revision, Action: "approve", Note: "A human reviewed the synthetic evidence and scope.", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func canonicalEntry(t *testing.T, s *Store, id string, global bool) document {
	t.Helper()
	project := ""
	if !global {
		project = "p"
	}
	root, err := s.root(project, global)
	if err != nil {
		t.Fatal(err)
	}
	d, found, err := s.findActive(context.Background(), root, project, global, id)
	if err != nil || !found {
		t.Fatal("canonical relation entry unavailable", err)
	}
	return d
}

func relation(kind string, target memory.MemoryEntry) memory.MemoryRelation {
	return memory.MemoryRelation{Kind: kind, TargetID: target.ID, TargetRevision: target.Revision}
}

func TestReplacementRelationsActivateOnlyWithHumanReviewInOneCommit(t *testing.T) {
	for _, global := range []bool{true, false} {
		for _, kind := range []string{"supersedes", "corrects"} {
			name := kind + map[bool]string{true: "/global", false: "/project"}[global]
			t.Run(name, func(t *testing.T) {
				s, raw, _, kb, projectRoot, cite := fixture(t)
				ctx := context.Background()
				target := approveRelationEntry(t, s, relationProposal(cite, "target-claim", global))
				p := relationProposal(cite, "replacement-claim", global)
				p.Relations = []memory.MemoryRelation{relation(kind, target)}
				draft, err := s.Propose(ctx, p)
				if err != nil || !reflect.DeepEqual(draft.Relations, p.Relations) {
					t.Fatal("proposal did not preserve typed revision-bound relation", err)
				}
				if canonicalEntry(t, s, target.ID, global).entry.Status != "verified" {
					t.Fatal("a model proposal retired the current claim")
				}
				if _, err := s.Review(ctx, Review{ID: draft.ID, Global: global, ProjectID: p.ProjectID, ExpectedRevision: draft.Revision, Action: "approve", Note: "No human confirmation."}); err == nil {
					t.Fatal("unconfirmed relation review changed effective knowledge")
				}
				approved, err := s.Review(ctx, Review{ID: draft.ID, Global: global, ProjectID: p.ProjectID, ExpectedRevision: draft.Revision, Action: "approve", Note: "A human reviewed the replacement and its target.", Confirmed: true})
				if err != nil || approved.Status != "verified" || !reflect.DeepEqual(approved.Relations, p.Relations) {
					t.Fatal("human-reviewed replacement failed", err)
				}
				retired := canonicalEntry(t, s, target.ID, global)
				if retired.entry.Status != "superseded" || retired.entry.SupersededBy != approved.ID || retired.entry.Revision == target.Revision {
					t.Fatal("replacement did not persist the target's retired state")
				}
				current, err := s.Memories(ctx, memory.KnowledgeQuery{Global: global, ProjectID: p.ProjectID})
				if err != nil || len(current.Entries) != 1 || current.Entries[0].ID != approved.ID {
					t.Fatal("current view did not exclude the superseded claim", err)
				}
				root := kb
				if !global {
					root = projectRoot
				}
				for _, entry := range []document{retired, canonicalEntry(t, s, approved.ID, global)} {
					if fileCommit(ctx, root, entry.path) != approved.Commit {
						t.Fatal("relationship transition was split across Git commits")
					}
				}
				if global {
					old, err := gitCommand(ctx, root, "show", target.Commit+":"+activePath(target))
					if err != nil || revision(old) != target.Revision {
						t.Fatal("the exact replaced revision was lost from Git history", err)
					}
				}
				var count int
				if err := raw.DB.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&count); err != nil || count != 1 {
					t.Fatal("knowledge review changed raw retained evidence", err)
				}
			})
		}
	}
}

func TestConflictRelationsRemainDisputedUntilAnotherHumanReviewedReplacement(t *testing.T) {
	for _, global := range []bool{true, false} {
		t.Run(map[bool]string{true: "global", false: "project"}[global], func(t *testing.T) {
			s, _, _, _, _, cite := fixture(t)
			target := approveRelationEntry(t, s, relationProposal(cite, "existing-claim", global))
			p := relationProposal(cite, "conflicting-claim", global)
			p.Relations = []memory.MemoryRelation{relation("conflicts_with", target)}
			conflicted := approveRelationEntry(t, s, p)
			old := canonicalEntry(t, s, target.ID, global).entry
			if conflicted.Status != "disputed" || old.Status != "disputed" {
				t.Fatal("human-reviewed conflict invented an effective winner")
			}
			current, err := s.Memories(context.Background(), memory.KnowledgeQuery{Global: global, ProjectID: p.ProjectID})
			if err != nil || len(current.Entries) != 0 {
				t.Fatal("unresolved disputed claims entered the current view", err)
			}
			resolved := relationProposal(cite, "resolved-claim", global)
			resolved.Relations = []memory.MemoryRelation{relation("corrects", conflicted), relation("supersedes", old)}
			approved := approveRelationEntry(t, s, resolved)
			if approved.Status != "verified" {
				t.Fatal("explicit human-reviewed resolution remained disputed")
			}
			for _, id := range []string{target.ID, conflicted.ID} {
				retired := canonicalEntry(t, s, id, global).entry
				if retired.Status != "superseded" || retired.SupersededBy != approved.ID {
					t.Fatal("resolution left a disputed predecessor active")
				}
			}
		})
	}
}

func TestRelationsAreNormalizedAndReorderedRetriesAreIdempotent(t *testing.T) {
	s, _, _, kb, _, cite := fixture(t)
	a := approveRelationEntry(t, s, relationProposal(cite, "target-alpha", true))
	b := approveRelationEntry(t, s, relationProposal(cite, "target-beta", true))
	p := relationProposal(cite, "replacement-claim", true)
	p.Relations = []memory.MemoryRelation{relation("corrects", b), relation("supersedes", a), relation("corrects", b)}
	draft, err := s.Propose(context.Background(), p)
	if err != nil || len(draft.Relations) != 2 || draft.Relations[0].TargetID != a.ID {
		t.Fatal("relations were not deduplicated and stably ordered", err)
	}
	head := git(t, kb, "rev-parse", "HEAD")
	p.Relations = []memory.MemoryRelation{relation("supersedes", a), relation("corrects", b)}
	again, err := s.Propose(context.Background(), p)
	if err != nil || again.Revision != draft.Revision || git(t, kb, "rev-parse", "HEAD") != head {
		t.Fatal("semantic relation reorder created another draft revision", err)
	}
	p.Relations[0].Kind = "corrects"
	if _, err := s.Propose(context.Background(), p); !errors.Is(err, ErrConflict) {
		t.Fatal("relationship edit did not require expected draft revision")
	}
	if git(t, kb, "rev-parse", "HEAD") != head {
		t.Fatal("rejected relationship edit created a commit")
	}
}

func TestRelationsRejectAmbiguousOrUnauthorizedTargetsBeforeWriting(t *testing.T) {
	s, _, _, kb, _, cite := fixture(t)
	a := approveRelationEntry(t, s, relationProposal(cite, "target-alpha", true))
	b := approveRelationEntry(t, s, relationProposal(cite, "target-beta", true))
	p := relationProposal(cite, "replacement-claim", true)
	head := git(t, kb, "rev-parse", "HEAD")
	for _, relations := range [][]memory.MemoryRelation{
		{{Kind: "unknown", TargetID: a.ID, TargetRevision: a.Revision}},
		{{Kind: "corrects", TargetID: p.ID, TargetRevision: a.Revision}},
		{{Kind: "corrects", TargetID: "unknown-target", TargetRevision: a.Revision}},
		{{Kind: "corrects", TargetID: a.ID, TargetRevision: "wrong"}},
		{{Kind: "corrects", TargetID: a.ID, TargetRevision: strings.Repeat("0", 64)}},
		{relation("supersedes", a), relation("corrects", a)},
		{relation("corrects", a), {Kind: "corrects", TargetID: a.ID, TargetRevision: b.Revision}},
		{relation("corrects", a), relation("conflicts_with", b)},
		{relation("corrects", a), relation("corrects", a), relation("corrects", a), relation("corrects", a), relation("corrects", a), relation("corrects", a), relation("corrects", a), relation("corrects", a), relation("corrects", a)},
	} {
		p.Relations = relations
		if _, err := s.Propose(context.Background(), p); err == nil {
			t.Fatal("ambiguous or stale relation was accepted")
		}
		if git(t, kb, "rev-parse", "HEAD") != head {
			t.Fatal("rejected relation changed Git history")
		}
	}
	projectProposal := relationProposal(cite, "project-only-claim", false)
	projectProposal.Relations = []memory.MemoryRelation{relation("corrects", a)}
	if _, err := s.Propose(context.Background(), projectProposal); !errors.Is(err, ErrConflict) {
		t.Fatal("a project relation crossed into the global authority")
	}
}

func TestReviewRechecksTargetRevisionAndRetainsDraftOnStaleTarget(t *testing.T) {
	s, _, _, kb, _, cite := fixture(t)
	target := approveRelationEntry(t, s, relationProposal(cite, "target-claim", true))
	p := relationProposal(cite, "replacement-claim", true)
	p.Relations = []memory.MemoryRelation{relation("corrects", target)}
	draft, err := s.Propose(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(context.Background(), Review{ID: target.ID, Global: true, ExpectedRevision: target.Revision, Action: "archive", Note: "Human separately retired the target.", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	head := git(t, kb, "rev-parse", "HEAD")
	if _, err := s.Review(context.Background(), Review{ID: draft.ID, Global: true, ExpectedRevision: draft.Revision, Action: "approve", Note: "This approval is stale.", Confirmed: true}); !errors.Is(err, ErrConflict) {
		t.Fatal("approval ignored a changed target revision")
	}
	if git(t, kb, "rev-parse", "HEAD") != head {
		t.Fatal("stale target review partially committed a transition")
	}
	retained, err := committed(context.Background(), kb, draftPath(draft.ID, true))
	if err != nil || revision(retained) != draft.Revision {
		t.Fatal("stale approval lost the pending proposal", err)
	}
	if _, err := s.Propose(context.Background(), p); !errors.Is(err, ErrConflict) {
		t.Fatal("proposal retry ignored a now-archived target")
	}
}

func TestDirtyTargetBlocksEntireRelationCommitAndPreservesHumanWork(t *testing.T) {
	for _, global := range []bool{true, false} {
		t.Run(map[bool]string{true: "global", false: "project"}[global], func(t *testing.T) {
			s, _, _, kb, projectRoot, cite := fixture(t)
			target := approveRelationEntry(t, s, relationProposal(cite, "target-claim", global))
			p := relationProposal(cite, "replacement-claim", global)
			p.Relations = []memory.MemoryRelation{relation("corrects", target)}
			draft, err := s.Propose(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			root := kb
			if !global {
				root = projectRoot
			}
			owned := filepath.Join(root, canonicalEntry(t, s, target.ID, global).path)
			before, err := os.ReadFile(owned)
			if err != nil {
				t.Fatal(err)
			}
			dirty := append(append([]byte(nil), before...), []byte("\n# uncommitted human review\n")...)
			if err := os.WriteFile(owned, dirty, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "unrelated-human.txt"), []byte("unrelated staged work\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, root, "add", "unrelated-human.txt")
			head := git(t, root, "rev-parse", "HEAD")
			if _, err := s.Review(context.Background(), Review{ID: draft.ID, Global: global, ProjectID: p.ProjectID, ExpectedRevision: draft.Revision, Action: "approve", Note: "A human confirmed but the owned file is dirty.", Confirmed: true}); !errors.Is(err, ErrConflict) {
				t.Fatal("dirty relation target was overwritten")
			}
			if git(t, root, "rev-parse", "HEAD") != head || git(t, root, "diff", "--cached", "--name-only") != "unrelated-human.txt" {
				t.Fatal("rejected relationship transition changed Git or another index entry")
			}
			after, err := os.ReadFile(owned)
			if err != nil || string(after) != string(dirty) {
				t.Fatal("rejected relationship transition lost human edits", err)
			}
		})
	}
}

func TestUnavailableReplacementEvidenceNeverReactivatesItsPredecessor(t *testing.T) {
	s, raw, engine, _, _, cite := fixture(t)
	target := approveRelationEntry(t, s, relationProposal(cite, "target-claim", true))
	if err := raw.PutBatch([]archive.Record{{RequestID: "replacement-source", SessionID: "s", Facets: map[string][]string{"project.id": {"p"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"ReplacementOnlyEvidence supports a new separately reviewed conclusion."}]}`)}}); err != nil {
		t.Fatal(err)
	}
	for {
		n, err := engine.Sync(context.Background(), 8)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	result, err := engine.Search(context.Background(), memory.Query{Text: "ReplacementOnlyEvidence"})
	if err != nil || len(result.Hits) != 1 {
		t.Fatal("synthetic replacement evidence unavailable", err)
	}
	p := relationProposal(result.Hits[0].Citation, "replacement-claim", true)
	p.Relations = []memory.MemoryRelation{relation("corrects", target)}
	approveRelationEntry(t, s, p)
	if _, err := raw.DB.Exec(`UPDATE records SET response_preview='changed replacement source revision' WHERE request_id='replacement-source'`); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReadSource(context.Background(), cite); err != nil {
		t.Fatal("predecessor evidence unexpectedly changed", err)
	}
	current, err := s.Memories(context.Background(), memory.KnowledgeQuery{Global: true})
	if err != nil || len(current.Entries) != 0 || canonicalEntry(t, s, target.ID, true).entry.Status != "superseded" {
		t.Fatal("unavailable replacement evidence revived an old corrected claim", err)
	}
}

func assertNewLineageIDRequired(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "new ID") || !strings.Contains(err.Error(), "corrects/supersedes") {
		t.Fatal("lineage edit did not explain the required explicit successor", err)
	}
}

func TestSameIDLineageGuardRejectsEveryPersistentMarker(t *testing.T) {
	for _, entry := range []memory.MemoryEntry{
		{Status: "verified", Relations: []memory.MemoryRelation{{Kind: "corrects", TargetID: "prior-claim", TargetRevision: strings.Repeat("a", 64)}}},
		{Status: "verified", SupersededBy: "replacement-claim"},
		{Status: "disputed"},
		{Status: "superseded"},
		{Status: "archived"},
	} {
		assertNewLineageIDRequired(t, requireNewLineageID(entry))
	}
	if err := requireNewLineageID(memory.MemoryEntry{Status: "verified"}); err != nil {
		t.Fatal("ordinary reviewed memory lost same-ID correction support", err)
	}
}

func TestReviewedLineageCannotBeErasedBySameIDProposalOrHandEditedDraft(t *testing.T) {
	for _, global := range []bool{true, false} {
		t.Run(map[bool]string{true: "global", false: "project"}[global], func(t *testing.T) {
			s, _, _, kb, projectRoot, cite := fixture(t)
			original := approveRelationEntry(t, s, relationProposal(cite, "original-claim", global))
			p := relationProposal(cite, "replacement-claim", global)
			p.Relations = []memory.MemoryRelation{relation("corrects", original)}
			replacement := approveRelationEntry(t, s, p)
			retired := canonicalEntry(t, s, original.ID, global).entry
			existing := approveRelationEntry(t, s, relationProposal(cite, "existing-claim", global))
			conflict := relationProposal(cite, "conflicting-claim", global)
			conflict.Relations = []memory.MemoryRelation{relation("conflicts_with", existing)}
			conflicted := approveRelationEntry(t, s, conflict)
			disputedTarget := canonicalEntry(t, s, existing.ID, global).entry
			plain := approveRelationEntry(t, s, relationProposal(cite, "archived-plain-claim", global))
			archived, err := s.Review(context.Background(), Review{ID: plain.ID, Global: global, ProjectID: plain.ProjectID, ExpectedRevision: plain.Revision, Action: "archive", Note: "A human archived the ordinary synthetic claim.", Confirmed: true})
			if err != nil || archived.Status != "archived" || len(archived.Relations) != 0 || archived.SupersededBy != "" {
				t.Fatal("plain canonical claim was not explicitly archived", err)
			}
			root := kb
			if !global {
				root = projectRoot
			}
			revive := relationProposal(cite, "invalid-archived-target", global)
			revive.Relations = []memory.MemoryRelation{relation("corrects", archived)}
			head := git(t, root, "rev-parse", "HEAD")
			if _, err := s.Propose(context.Background(), revive); !errors.Is(err, ErrConflict) || git(t, root, "rev-parse", "HEAD") != head {
				t.Fatal("an archived entry became eligible as a relation target", err)
			}
			for _, current := range []memory.MemoryEntry{replacement, retired, conflicted, disputedTarget, archived} {
				edit := relationProposal(cite, current.ID, global)
				edit.OperationID = "same-id-attempt-" + current.ID
				edit.BaseRevision = current.Revision
				head := git(t, root, "rev-parse", "HEAD")
				// Removing edges or copying their already-applied historical target
				// revisions must both fail with the same actionable conflict.
				for _, relations := range [][]memory.MemoryRelation{nil, current.Relations} {
					edit.Relations = relations
					_, err := s.Propose(context.Background(), edit)
					assertNewLineageIDRequired(t, err)
				}
				if git(t, root, "rev-parse", "HEAD") != head {
					t.Fatal("blocked in-place proposal changed Git history")
				}
				// Simulate a draft manually committed before approval, bypassing the
				// proposal API and deliberately omitting every relationship marker.
				manual := memory.MemoryEntry{ID: edit.ID, Title: edit.Title, Kind: edit.Kind, Statement: edit.Statement, Status: "draft", ProjectID: edit.ProjectID, Global: global, Sources: edit.Sources, OperationID: edit.OperationID, BaseRevision: current.Revision}
				raw, err := markdown(metadata(manual, s.now()), body(manual))
				if err != nil {
					t.Fatal(err)
				}
				file := draftPath(manual.ID, global)
				if err := commitFiles(context.Background(), root, "test: hand-edited draft cannot erase reviewed lineage", map[string][]byte{file: raw}); err != nil {
					t.Fatal(err)
				}
				head = git(t, root, "rev-parse", "HEAD")
				_, err = s.Review(context.Background(), Review{ID: manual.ID, Global: global, ProjectID: manual.ProjectID, ExpectedRevision: revision(raw), Action: "approve", Note: "Synthetic human approval must still preserve lineage.", Confirmed: true})
				assertNewLineageIDRequired(t, err)
				if git(t, root, "rev-parse", "HEAD") != head || canonicalEntry(t, s, current.ID, global).entry.Revision != current.Revision {
					t.Fatal("hand-edited draft overwrote the canonical relation node")
				}
				retained, err := committed(context.Background(), root, file)
				if err != nil || string(retained) != string(raw) {
					t.Fatal("rejected hand-edited draft was changed", err)
				}
			}
			if _, err := s.Review(context.Background(), Review{ID: conflicted.ID, Global: global, ProjectID: conflicted.ProjectID, ExpectedRevision: conflicted.Revision, Action: "archive", Note: "Human archiving remains available for disputed lineage nodes.", Confirmed: true}); err != nil {
				t.Fatal("same-ID revision protection blocked explicit human archiving", err)
			}
		})
	}
}

func TestReviewedLineageAdvancesWithNewIDAndRetainsEarlierEdges(t *testing.T) {
	for _, global := range []bool{true, false} {
		t.Run(map[bool]string{true: "global", false: "project"}[global], func(t *testing.T) {
			s, _, _, kb, projectRoot, cite := fixture(t)
			first := approveRelationEntry(t, s, relationProposal(cite, "first-claim", global))
			p := relationProposal(cite, "second-claim", global)
			p.Relations = []memory.MemoryRelation{relation("corrects", first)}
			second := approveRelationEntry(t, s, p)
			p = relationProposal(cite, "third-claim", global)
			p.Relations = []memory.MemoryRelation{relation("supersedes", second)}
			third := approveRelationEntry(t, s, p)
			prior := canonicalEntry(t, s, second.ID, global).entry
			if third.Status != "verified" || prior.Status != "superseded" || prior.SupersededBy != third.ID || !reflect.DeepEqual(prior.Relations, second.Relations) {
				t.Fatal("explicit successor discarded the predecessor's earlier edges")
			}
			old := canonicalEntry(t, s, first.ID, global).entry
			p = relationProposal(cite, "invalid-revival", global)
			p.Relations = []memory.MemoryRelation{relation("corrects", old)}
			root := kb
			if !global {
				root = projectRoot
			}
			head := git(t, root, "rev-parse", "HEAD")
			if _, err := s.Propose(context.Background(), p); !errors.Is(err, ErrConflict) || git(t, root, "rev-parse", "HEAD") != head {
				t.Fatal("a superseded predecessor became a valid relation target", err)
			}
		})
	}
}

func TestOrdinaryReviewedEntriesKeepSameIDCorrectionAndRevalidation(t *testing.T) {
	for _, global := range []bool{true, false} {
		t.Run(map[bool]string{true: "global", false: "project"}[global], func(t *testing.T) {
			s, _, _, _, _, cite := fixture(t)
			first := approveRelationEntry(t, s, relationProposal(cite, "ordinary-claim", global))
			edit := relationProposal(cite, first.ID, global)
			edit.OperationID = "ordinary-correction-operation"
			edit.BaseRevision = first.Revision
			edit.Statement = "An explicitly reviewed same-ID correction remains supported for an ordinary entry."
			second := approveRelationEntry(t, s, edit)
			if second.ID != first.ID || second.Revision == first.Revision || second.Status != "verified" || len(second.Relations) != 0 || second.SupersededBy != "" {
				t.Fatal("ordinary same-ID correction behavior changed")
			}
			edit.OperationID = "ordinary-revalidation-operation"
			edit.BaseRevision = second.Revision
			third := approveRelationEntry(t, s, edit)
			if third.ID != second.ID || third.Status != "verified" || third.Statement != second.Statement || third.Revision == second.Revision {
				t.Fatal("ordinary unchanged-claim revalidation behavior changed")
			}
		})
	}
}
