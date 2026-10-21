package knowledge

import (
	"context"
	"testing"

	"cpa-session-archive/internal/memory"
)

func TestDraftRecallIncludesValidCustomIdentifiersWithoutActivatingThem(t *testing.T) {
	s, _, _, _, _, cite := fixture(t)
	p := proposal(cite)
	p.ID = "custom-reviewed-claim"
	custom, err := s.Propose(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = ""
	p.OperationID = "second-draft-operation"
	automatic, err := s.Propose(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Memories(context.Background(), memory.KnowledgeQuery{Global: true, Drafts: true})
	if err != nil || len(result.Entries) != 2 {
		t.Fatal("a custom candidate identifier was hidden from draft recall", err)
	}
	ids := map[string]bool{}
	for _, entry := range result.Entries {
		ids[entry.ID] = true
		if entry.Validity == nil || entry.Validity.State != "draft" {
			t.Fatal("candidate was not explicitly unreviewed")
		}
	}
	if !ids[custom.ID] || !ids[automatic.ID] {
		t.Fatal("draft listing lost the committed candidate identity")
	}
	current, err := s.Memories(context.Background(), memory.KnowledgeQuery{Global: true})
	if err != nil || len(current.Entries) != 0 {
		t.Fatal("draft listing activated a candidate", err)
	}
}
