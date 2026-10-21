// Package memoryextract produces unconfirmed, source-bound memory candidates.
// Its task ledger is operational state, never a canonical knowledge authority.
package memoryextract

import (
	"context"
	"errors"

	"cpa-session-archive/internal/memory"
)

const Version = "memory-extraction-v1"

var (
	ErrInvalid   = errors.New("invalid extraction input or output")
	ErrBudget    = errors.New("extraction budget exhausted")
	ErrPaused    = errors.New("extraction is paused")
	ErrBusy      = errors.New("extraction job or worker is already running")
	ErrUncertain = errors.New("extraction call outcome is uncertain; operator reconciliation required")
)

type Selection struct {
	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id,omitempty"`
}

// Excerpts retain speaker and tool metadata outside model control. Citations
// refer to at most 1,600 Unicode runes, using the existing normalizer contract.
type Excerpt struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Kind       string          `json:"kind"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Text       string          `json:"text"`
	Citation   memory.Citation `json:"citation"`
	Partial    bool            `json:"partial"`
}

type Input struct {
	Version           string               `json:"version"`
	ArchiveID         string               `json:"archive_id"`
	ProjectID         string               `json:"project_id"`
	SessionID         string               `json:"session_id"`
	RequestID         string               `json:"request_id"`
	Revision          string               `json:"revision"`
	Sequence          int64                `json:"sequence"`
	Window            int                  `json:"window"`
	Excerpts          []Excerpt            `json:"excerpts"`
	Existing          []memory.MemoryEntry `json:"existing"`
	KnowledgeComplete bool                 `json:"knowledge_complete"`
}

type Support struct {
	ExcerptID string `json:"excerpt_id"`
	Quote     string `json:"quote"`
}
type Target struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// Attribution separates user requirements/reports, assistant proposals/reports,
// and directly retained tool observations. It never certifies factual truth.
type DraftCandidate struct {
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Statement   string    `json:"statement"`
	Attribution string    `json:"attribution"`
	Supports    []Support `json:"supports"`
	Action      string    `json:"action"`
	Targets     []Target  `json:"targets"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
type Request struct {
	Input           Input `json:"input"`
	MaxOutputTokens int   `json:"max_output_tokens"`
}
type Response struct {
	Candidates []DraftCandidate `json:"candidates"`
	Usage      Usage            `json:"usage"`
}

type Provider interface {
	Fingerprint() string
	EstimateInputTokens(Request) (int, error)
	Extract(context.Context, Request) (Response, error)
}

// GroundedSource proves original message metadata as well as excerpt bytes.
// ReadSource alone does not expose the original speaker role.
type GroundedSource interface {
	memory.SourceReader
	VerifyInput(context.Context, Input) error
}

// CallError distinguishes a definitely rejected call from an ambiguous outcome.
// Only an explicit retryable, non-uncertain rejection may be retried unattended.
type CallError struct {
	Code      string
	Retryable bool
	Uncertain bool
}

func (e *CallError) Error() string { return "extraction provider: " + e.Code }

type Evidence struct {
	Role     string          `json:"role"`
	Kind     string          `json:"kind"`
	Quote    string          `json:"quote"`
	Citation memory.Citation `json:"citation"`
}
type Candidate struct {
	ID                  string                `json:"id"`
	Action              string                `json:"action"`
	Attribution         string                `json:"attribution"`
	RequiresHumanReview bool                  `json:"requires_human_review"`
	Evidence            []Evidence            `json:"evidence"`
	Proposal            memory.MemoryProposal `json:"proposal"`
	DuplicateOf         []Target              `json:"duplicate_of,omitempty"`
	Warnings            []string              `json:"warnings,omitempty"`
}
type Rejection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}
type Report struct {
	Version             string      `json:"version"`
	JobID               string      `json:"job_id"`
	ProjectID           string      `json:"project_id"`
	SessionID           string      `json:"session_id"`
	RequiresHumanReview bool        `json:"requires_human_review"`
	Candidates          []Candidate `json:"candidates"`
	Rejected            []Rejection `json:"rejected"`
	Usage               Usage       `json:"usage"`
}
