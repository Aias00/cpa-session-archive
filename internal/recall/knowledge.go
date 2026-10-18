package recall

import (
	"context"
	"cpa-session-archive/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func NewServerWithKnowledge(reader memory.Reader, knowledge memory.Knowledge, allowProposals bool) *mcp.Server {
	server := NewServer(reader)
	addReadTool(server, "project_memory", "Read confirmed Git-backed project knowledge, or explicitly request reusable global knowledge. Set drafts=true to inspect candidates separately. Active recall excludes unconfirmed, expired or invalid-source entries; current instructions remain authoritative.", knowledge.Memories)
	if allowProposals {
		no := false
		mcp.AddTool(server, &mcp.Tool{Name: "memory_propose", Description: "Save or revise a sourced Git candidate, never activate it. Choose explicit global=true for reusable engineering knowledge, or a registered project_id for project-only facts. operation_id must be stable for retries. To edit a draft pass id and expected_revision; to correct active memory pass id and base_revision. A human must separately review/confirm. Do not propose secrets, raw transcripts, or ordinary task progress as global knowledge.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no}}, func(ctx context.Context, _ *mcp.CallToolRequest, input memory.MemoryProposal) (*mcp.CallToolResult, memory.MemoryEntry, error) {
			entry, err := knowledge.Propose(ctx, input)
			return nil, entry, err
		})
	}
	return server
}
