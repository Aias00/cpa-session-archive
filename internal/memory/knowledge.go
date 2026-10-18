package memory

import "context"

// Knowledge is the canonical Git adapter consumed by optional MCP tools.
// Review is intentionally absent from this model-facing interface.
type Knowledge interface {
	Memories(context.Context, KnowledgeQuery) (KnowledgeResult, error)
	Propose(context.Context, MemoryProposal) (MemoryEntry, error)
}
type KnowledgeQuery struct {
	ProjectID string `json:"project_id,omitempty"`
	Global    bool   `json:"global,omitempty"`
	Drafts    bool   `json:"drafts,omitempty"`
	Text      string `json:"text,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type MemoryProposal struct {
	OperationID      string     `json:"operation_id" jsonschema:"Stable 8-128 character operation key for safe retries; use a new key for an intentional edit."`
	ID               string     `json:"id,omitempty"`
	ExpectedRevision string     `json:"expected_revision,omitempty" jsonschema:"Current candidate revision when editing an existing draft."`
	BaseRevision     string     `json:"base_revision,omitempty" jsonschema:"Current confirmed entry revision when proposing a correction."`
	ProjectID        string     `json:"project_id,omitempty"`
	Global           bool       `json:"global,omitempty"`
	Kind             string     `json:"kind"`
	Title            string     `json:"title"`
	Statement        string     `json:"statement"`
	Sources          []Citation `json:"sources" jsonschema:"One to eight exact source citations returned by archive_search."`
}
type MemoryEntry struct {
	ID           string     `json:"id" yaml:"id"`
	Title        string     `json:"title" yaml:"title"`
	Kind         string     `json:"kind" yaml:"memory_kind"`
	Statement    string     `json:"statement" yaml:"summary"`
	Status       string     `json:"status" yaml:"status"`
	ProjectID    string     `json:"project_id,omitempty" yaml:"archive_project_id,omitempty"`
	Global       bool       `json:"global" yaml:"archive_global"`
	Sources      []Citation `json:"sources,omitempty" yaml:"archive_sources,omitempty"`
	Revision     string     `json:"revision" yaml:"-"`
	Commit       string     `json:"commit" yaml:"-"`
	OperationID  string     `json:"operation_id,omitempty" yaml:"operation_id,omitempty"`
	BaseRevision string     `json:"base_revision,omitempty" yaml:"base_revision,omitempty"`
	ReviewAfter  string     `json:"review_after,omitempty" yaml:"review_after,omitempty"`
}
type KnowledgeResult struct {
	Entries     []MemoryEntry `json:"entries"`
	Truncated   bool          `json:"truncated"`
	Unavailable int           `json:"unavailable_sources"`
	Authority   string        `json:"authority"`
}
