package knowledge

import (
	"context"
	"testing"

	"cpa-session-archive/internal/memory"
)

type mismatchedSourceCitationReader struct {
	memory.Reader
	field string
}

// Delegate every real operation except the returned citation, simulating a
// transport that reports success while substituting another source observation.
func (r mismatchedSourceCitationReader) ReadSource(ctx context.Context, citation memory.Citation) (memory.Evidence, error) {
	evidence, err := r.Reader.ReadSource(ctx, citation)
	if err != nil {
		return evidence, err
	}
	if r.field == "revision" {
		evidence.Citation.Revision = "a-different-successful-source-revision"
	} else {
		evidence.Citation.Start++
	}
	return evidence, nil
}

func TestSourceScopeRejectsOwnerWideAndRestrictedScopeTogether(t *testing.T) {
	_, _, engine, root, projectRoot, _ := fixture(t)
	for name, scope := range map[string]memory.Scope{
		"project":         {AllowAll: true, ProjectID: "p"},
		"session":         {AllowAll: true, SessionID: "s"},
		"project_session": {AllowAll: true, ProjectID: "p", SessionID: "s"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(Config{GlobalRepository: root, Projects: map[string]string{"p": projectRoot}, Scope: scope, Source: engine}); err == nil {
				t.Fatal("ambiguous owner-wide and restricted scope was accepted")
			}
		})
	}
}

func TestSourceScopeSuccessfulCitationMismatchCannotEnterCurrentKnowledge(t *testing.T) {
	for _, field := range []string{"revision", "range"} {
		t.Run(field, func(t *testing.T) {
			s, _, engine, root, _, citation := fixture(t)
			ctx := context.Background()
			draft, err := s.Propose(ctx, proposal(citation))
			if err != nil {
				t.Fatal(err)
			}
			confirmed, err := s.Review(ctx, Review{ID: draft.ID, Global: true, ExpectedRevision: draft.Revision, Action: "approve", Note: "A human reviewed this synthetic source before the transport changed", Confirmed: true})
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.Memories(ctx, memory.KnowledgeQuery{Global: true})
			if err != nil || len(current.Entries) != 1 {
				t.Fatal("fixture did not start with current reviewed knowledge", err)
			}
			mismatch := mismatchedSourceCitationReader{Reader: engine, field: field}
			evidence, err := mismatch.ReadSource(ctx, citation)
			if err != nil || evidence.Citation == citation {
				t.Fatal("fixture did not return a successful mismatched citation", err)
			}
			s.config.Source = mismatch
			head, status := git(t, root, "rev-parse", "HEAD"), git(t, root, "status", "--porcelain", "--untracked-files=all")
			p := proposal(citation)
			p.OperationID = "mismatched-source-proposal-001"
			if _, err := s.Propose(ctx, p); err == nil {
				t.Fatal("successful source response with a substituted citation was written as a candidate")
			}
			if git(t, root, "rev-parse", "HEAD") != head || git(t, root, "status", "--porcelain", "--untracked-files=all") != status {
				t.Fatal("rejected source substitution changed canonical Git state")
			}
			current, err = s.Memories(ctx, memory.KnowledgeQuery{Global: true, View: "current"})
			if err != nil || len(current.Entries) != 0 || current.Unavailable != 1 || current.States["source_unavailable"] != 1 {
				t.Fatal("a substituted source citation remained current or hid its validation failure", err)
			}
			all, err := s.Memories(ctx, memory.KnowledgeQuery{Global: true, View: "all"})
			if err != nil || len(all.Entries) != 1 || all.Entries[0].ID != confirmed.ID || all.Entries[0].Status != "verified" || all.Entries[0].Validity == nil || all.Entries[0].Validity.State != "source_unavailable" || len(all.Entries[0].Validity.Reasons) == 0 {
				t.Fatal("lifecycle inspection lost the confirmed claim or its source-unavailable reason", err)
			}
			if evidence, err := engine.ReadSource(ctx, citation); err != nil || evidence.Citation != citation {
				t.Fatal("transport rejection changed the retained original evidence", err)
			}
			if git(t, root, "rev-parse", "HEAD") != head || git(t, root, "status", "--porcelain", "--untracked-files=all") != status {
				t.Fatal("read-time validity inspection wrote back to canonical knowledge")
			}
		})
	}
}
