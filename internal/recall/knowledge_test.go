package recall

import (
	"context"
	"cpa-session-archive/internal/memory"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
)

type testKnowledge struct {
	proposed bool
	query    memory.KnowledgeQuery
	queries  int
	proposal memory.MemoryProposal
}

func (k *testKnowledge) Memories(_ context.Context, q memory.KnowledgeQuery) (memory.KnowledgeResult, error) {
	k.query = q
	k.queries++
	result := memory.KnowledgeResult{Authority: "reviewed-git", States: map[string]int{"current": 1, "superseded": 1}, Entries: []memory.MemoryEntry{{ID: "memory-successor", Status: "verified", Statement: "Reviewed knowledge", Validity: &memory.MemoryValidity{State: "current", Reasons: []string{"reviewed_and_eligible"}}}}}
	if q.View == "all" {
		result.Entries = append(result.Entries, memory.MemoryEntry{ID: "memory-example", Status: "superseded", Statement: "Prior reviewed knowledge", SupersededBy: "memory-successor", Validity: &memory.MemoryValidity{State: "superseded", Reasons: []string{"human_reviewed_replacement"}}})
	}
	return result, nil
}
func (k *testKnowledge) Propose(_ context.Context, p memory.MemoryProposal) (memory.MemoryEntry, error) {
	k.proposed = true
	k.proposal = p
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
		if tool.Name == "project_memory" {
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil || strings.Join(schema.Properties["view"].Enum, ",") != ",current,all" {
				t.Fatalf("knowledge lifecycle view schema: %s %v", raw, err)
			}
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatal("lifecycle inspection is not marked read-only")
			}
		}
	}
	for _, view := range []string{"", "current", "all"} {
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "project_memory", Arguments: map[string]any{"global": true, "view": view, "text": "reviewed", "limit": 2}})
		if err != nil || r.IsError || library.query.View != view || !library.query.Global || library.query.Drafts || library.query.Text != "reviewed" || library.query.Limit != 2 {
			t.Fatal("lifecycle query lost its scope or view", r, err)
		}
		raw, err := json.Marshal(r.StructuredContent)
		var result memory.KnowledgeResult
		if err != nil || json.Unmarshal(raw, &result) != nil || result.States["superseded"] != 1 || len(result.Entries) == 0 || result.Entries[0].Validity == nil || result.Entries[0].Validity.State != "current" {
			t.Fatal("lifecycle metadata lost at the MCP boundary", err)
		}
		if view == "all" && (len(result.Entries) != 2 || result.Entries[1].Validity == nil || result.Entries[1].Validity.State != "superseded" || result.Entries[1].SupersededBy != "memory-successor") {
			t.Fatal("lifecycle inspection lost inactive metadata")
		}
	}
	before := library.queries
	r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "project_memory", Arguments: map[string]any{"global": true, "view": "invalid"}})
	if err == nil && !r.IsError || library.queries != before {
		t.Fatal("unsupported view reached the knowledge reader")
	}
	relation := memory.MemoryRelation{Kind: "corrects", TargetID: "memory-target", TargetRevision: strings.Repeat("a", 64)}
	r, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_propose", Arguments: map[string]any{"operation_id": "test-operation", "global": true, "kind": "decision", "title": "Reviewed draft", "statement": "A candidate statement is not active knowledge.", "sources": []any{}, "relations": []memory.MemoryRelation{relation}}})
	if err != nil || r.IsError || !library.proposed || len(library.proposal.Relations) != 1 || library.proposal.Relations[0] != relation {
		t.Fatal(r, err)
	}
}
