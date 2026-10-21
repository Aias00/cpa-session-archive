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
	View      string `json:"view,omitempty" jsonschema:"current (default) returns usable reviewed knowledge; all inspects current canonical lifecycle records including inactive entries. Does not enumerate past Git revisions or combine with drafts=true."`
}

// Relations bind a human-reviewed transition to the target's pre-transition
// revision in the same authority/scope. They never grant cross-project access.
type MemoryRelation struct {
	Kind           string `json:"kind" yaml:"kind" jsonschema:"supersedes, corrects, or conflicts_with. Replacement and conflict kinds cannot be mixed in one proposal."`
	TargetID       string `json:"target_id" yaml:"target_id"`
	TargetRevision string `json:"target_revision" yaml:"target_revision" jsonschema:"Exact canonical content revision before human approval changes the target's status."`
}

// Validity is derived at read time. It is not another persisted knowledge
// authority and does not certify whether a statement is factually correct.
type MemoryValidity struct {
	State   string   `json:"state" yaml:"-"`
	Reasons []string `json:"reasons" yaml:"-"`
}
type MemoryProposal struct {
	OperationID      string           `json:"operation_id" jsonschema:"Stable 8-128 character operation key for safe retries; use a new key for an intentional edit."`
	ID               string           `json:"id,omitempty"`
	ExpectedRevision string           `json:"expected_revision,omitempty" jsonschema:"Current candidate revision when editing an existing draft."`
	BaseRevision     string           `json:"base_revision,omitempty" jsonschema:"Current canonical revision for same-ID correction of a verified entry with no recorded relationships or superseded_by link. Otherwise use a new ID and explicit relations to eligible targets."`
	ProjectID        string           `json:"project_id,omitempty"`
	Global           bool             `json:"global,omitempty"`
	Kind             string           `json:"kind"`
	Title            string           `json:"title"`
	Statement        string           `json:"statement"`
	Sources          []Citation       `json:"sources" jsonschema:"One to eight exact source citations returned by archive_search."`
	Relations        []MemoryRelation `json:"relations,omitempty" jsonschema:"Up to eight same-scope revision-bound relationship proposals. Only separate human review can activate them."`
}
type MemoryEntry struct {
	ID           string           `json:"id" yaml:"id"`
	Title        string           `json:"title" yaml:"title"`
	Kind         string           `json:"kind" yaml:"memory_kind"`
	Statement    string           `json:"statement" yaml:"summary"`
	Status       string           `json:"status" yaml:"status"`
	ProjectID    string           `json:"project_id,omitempty" yaml:"archive_project_id,omitempty"`
	Global       bool             `json:"global" yaml:"archive_global"`
	Sources      []Citation       `json:"sources,omitempty" yaml:"archive_sources,omitempty"`
	Revision     string           `json:"revision" yaml:"-"`
	Commit       string           `json:"commit" yaml:"-"`
	OperationID  string           `json:"operation_id,omitempty" yaml:"operation_id,omitempty"`
	BaseRevision string           `json:"base_revision,omitempty" yaml:"base_revision,omitempty"`
	ReviewAfter  string           `json:"review_after,omitempty" yaml:"review_after,omitempty"`
	Relations    []MemoryRelation `json:"relations,omitempty" yaml:"relations,omitempty"`
	SupersededBy string           `json:"superseded_by,omitempty" yaml:"superseded_by,omitempty"`
	Validity     *MemoryValidity  `json:"validity,omitempty" yaml:"-"`
}
type KnowledgeResult struct {
	Entries     []MemoryEntry  `json:"entries"`
	Truncated   bool           `json:"truncated"`
	Unavailable int            `json:"unavailable_sources"`
	Authority   string         `json:"authority"`
	States      map[string]int `json:"states,omitempty"`
}
