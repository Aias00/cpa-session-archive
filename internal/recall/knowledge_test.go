package recall

import (
	"context"
	"cpa-session-archive/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
)

type testKnowledge struct{ proposed bool }

func (k *testKnowledge) Memories(context.Context, memory.KnowledgeQuery) (memory.KnowledgeResult, error) {
	return memory.KnowledgeResult{Authority: "reviewed-git", Entries: []memory.MemoryEntry{{ID: "memory-example", Status: "verified", Statement: "Reviewed knowledge"}}}, nil
}
func (k *testKnowledge) Propose(_ context.Context, p memory.MemoryProposal) (memory.MemoryEntry, error) {
	k.proposed = true
	return memory.MemoryEntry{ID: "memory-draft", Status: "draft", Statement: p.Statement}, nil
}
func TestMCPKnowledgeToolsNeverExposeReview(t *testing.T) {
	ctx := context.Background()
	library := &testKnowledge{}
	// Reader methods are not called in this knowledge-boundary test.
	reader, err := NewClient("http://127.0.0.1:1", strings.Repeat("t", 40))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	server := NewServerWithKnowledge(reader, library, true)
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "knowledge-regression", Version: "1"}, nil)
	session, err := client.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 6 {
		t.Fatal(tools, err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "memory_review" || tool.Name == "memory_approve" {
			t.Fatal("review exposed")
		}
		if tool.Name == "memory_propose" && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint) {
			t.Fatal("write tool falsely marked read-only")
		}
	}
	r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "project_memory", Arguments: map[string]any{"global": true}})
	if err != nil || r.IsError {
		t.Fatal(r, err)
	}
	r, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_propose", Arguments: map[string]any{"operation_id": "test-operation", "global": true, "kind": "decision", "title": "Reviewed draft", "statement": "A candidate statement is not active knowledge.", "sources": []any{}}})
	if err != nil || r.IsError || !library.proposed {
		t.Fatal(r, err)
	}
}
