package recall

import (
	"context"
	"cpa-session-archive/internal/memory"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func NewServerWithKnowledge(reader memory.Reader, knowledge memory.Knowledge, allowProposals bool) *mcp.Server {
	server := NewServer(reader)
	schema, err := jsonschema.For[memory.KnowledgeQuery](nil)
	if err != nil {
		panic(err)
	}
	schema.Properties["view"].Enum = []any{"", "current", "all"}
	addReadTool(server, "project_memory", "Read reviewed Git knowledge in a fixed project or explicit global scope. view=current (default) returns usable current entries; view=all inspects current canonical lifecycle records and validity reasons, not every past Git revision. Superseded, disputed, archived, review-due and unvalidated-source entries are excluded from current recall. A review date requests rechecking; it is not a factual expiry verdict. Use drafts=true separately, never with view=all. Current instructions remain authoritative.", knowledge.Memories, schema)
	if allowProposals {
		no := false
		mcp.AddTool(server, &mcp.Tool{Name: "memory_propose", Description: "Save or revise a sourced Git candidate, never activate it. Choose explicit global=true for reusable engineering knowledge, or a registered project_id for project-only facts. operation_id must be stable for retries. To edit a draft pass id and expected_revision. Same-ID base_revision corrections require a verified canonical entry without recorded relationships or a superseded_by link; otherwise use a new ID. Optional revision-bound supersedes/corrects/conflicts_with relations target eligible verified/disputed entries in the same scope. A human must separately review/confirm. Do not propose secrets, raw transcripts, or ordinary task progress as global knowledge.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no}}, func(ctx context.Context, _ *mcp.CallToolRequest, input memory.MemoryProposal) (*mcp.CallToolResult, memory.MemoryEntry, error) {
			entry, err := knowledge.Propose(ctx, input)
			return nil, entry, err
		})
	}
	return server
}
