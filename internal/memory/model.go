// Package memory provides local source-grounded recall of retained requests.
package memory

import (
	"context"
	"errors"
)

var (
	ErrInvalidQuery        = errors.New("invalid query")
	ErrSourceUnavailable   = errors.New("source revision unavailable")
	ErrQueryBudget         = errors.New("retrieval query exceeded its time budget; narrow scope, time range or text")
	ErrSemanticUnavailable = errors.New("semantic retrieval unavailable; verify the configured local embedding service")
)

// Query text is never SQL/FTS syntax. Only explicit hybrid mode uses semantics.
type Query struct {
	Text      string `json:"text"`
	Mode      string `json:"mode,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Client    string `json:"client,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	After     string `json:"after,omitempty"`
	Before    string `json:"before,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// Citation ranges are Unicode rune offsets within one normalized message.
type Citation struct {
	NormalizerVersion int    `json:"normalizer_version" yaml:"normalizer_version"`
	RequestID         string `json:"request_id" yaml:"request_id"`
	Revision          string `json:"revision" yaml:"revision"`
	Side              string `json:"side" yaml:"side"`
	Pointer           string `json:"pointer" yaml:"pointer"`
	Start             int    `json:"start" yaml:"start"`
	End               int    `json:"end" yaml:"end"`
}

type Hit struct {
	Kind       string `json:"kind,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Model      string `json:"model,omitempty"`
	MessageAt  string `json:"message_at,omitempty"`

	Citation  Citation `json:"citation"`
	Snippet   string   `json:"snippet"`
	ProjectID string   `json:"project_id"`
	Client    string   `json:"client"`
	SessionID string   `json:"session_id"`
	Role      string   `json:"role"`
	EventAt   string   `json:"event_at"`
	Partial   bool     `json:"partial"`
}

type Status struct {
	Semantic            *SemanticStatus  `json:"semantic,omitempty"`
	Coverage            *CoverageSummary `json:"coverage,omitempty"`
	Ingestion           *IngestionStatus `json:"ingestion,omitempty"`
	MessageMetadataDone bool             `json:"message_metadata_done"`
	UniqueChunks        int64            `json:"unique_chunks"`
	DeduplicatedChunks  int64            `json:"deduplicated_chunks"`
	ScopeFiltered       bool             `json:"scope_filtered,omitempty"`
	BackfillDone        bool             `json:"backfill_done"`
	BackfillAfterID     int64            `json:"backfill_after_id"`
	Checkpoint          int64            `json:"checkpoint"`
	Watermark           int64            `json:"watermark"`
	Lag                 int64            `json:"lag"`
	Sources             int64            `json:"sources"`
	Chunks              int64            `json:"chunks"`
	PartialSources      int64            `json:"partial_sources"`
	Failures            int64            `json:"failures"`
	LastError           string           `json:"last_error,omitempty"`
}

type SearchResult struct {
	Retrieval *RetrievalStatus `json:"retrieval,omitempty"`
	Hits      []Hit            `json:"hits"`
	Truncated bool             `json:"truncated"`
	Status    Status           `json:"status"`
}

// RetrievalStatus discloses optional semantic coverage without global counts.
type RetrievalStatus struct {
	Mode             string `json:"mode"`
	SemanticComplete bool   `json:"semantic_complete"`
	Fingerprint      string `json:"fingerprint"`
}

type Evidence struct {
	Kind       string `json:"kind,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Model      string `json:"model,omitempty"`
	MessageAt  string `json:"message_at,omitempty"`

	ProjectID      string          `json:"project_id"`
	SessionID      string          `json:"session_id"`
	Client         string          `json:"client"`
	Citation       Citation        `json:"citation"`
	Text           string          `json:"text"`
	Partial        bool            `json:"partial"`
	Issues         []string        `json:"issues"`
	CoverageIssues []CoverageIssue `json:"coverage_issues,omitempty"`
}

type RecapResult struct {
	Retrieval *RetrievalStatus `json:"retrieval,omitempty"`
	Text      string           `json:"text"`
	Hits      []Hit            `json:"hits"`
	Status    Status           `json:"status"`
}

// SearchReader retrieves scoped evidence without owning projection lifecycle.
type SearchReader interface {
	Search(context.Context, Query) (SearchResult, error)
	Recap(context.Context, Query) (RecapResult, error)
}

// SourceReader validates evidence against retained source revisions.
type SourceReader interface {
	ReadSource(context.Context, Citation) (Evidence, error)
}

type StatusReader interface {
	Status(context.Context) (Status, error)
}

// Reader is the capability an HTTP/MCP frontend needs from a recall engine.
type Reader interface {
	SearchReader
	SourceReader
	StatusReader
}

// Projection is owned by the memory service, not by HTTP/MCP callers. Alternative
// retrieval implementations must preserve Reader's scope and citation contract.
// Run processes retained changes until cancellation; Close follows Run's return.
type Projection interface {
	Reader
	Run(context.Context)
	Close() error
}
