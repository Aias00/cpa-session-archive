package memory

import (
	"context"
	"errors"
)

var ErrProjectSourceUnavailable = errors.New("committed project source is unavailable or changed")

// ProjectContext provides read-only evidence from registered project roots.
// It is independent of extraction, knowledge review and projection ownership.
type ProjectContext interface {
	ProjectContext(context.Context, ProjectContextQuery) (ProjectContextResult, error)
	ReadProjectSource(context.Context, ProjectGitCitation) (ProjectGitEvidence, error)
}

type ProjectContextQuery struct {
	ProjectID      string `json:"project_id,omitempty" jsonschema:"Exact registered project ID; omission uses a project-scoped credential. Owner-wide clients must select a project explicitly."`
	Intent         string `json:"intent,omitempty" jsonschema:"overview, architecture, followups, installation or all (default). Select an intent instead of sending a natural-language question as a literal search."`
	IncludeHistory bool   `json:"include_history,omitempty" jsonschema:"Explicitly include bounded project-scoped archive searches. Default false. Search failures are disclosed as incomplete coverage, never absence of evidence."`
}

// Git citations are exact, one-based inclusive line ranges at one committed
// project snapshot. SHA256 binds the complete file, not a generated summary.
type ProjectGitCitation struct {
	ProjectID string `json:"project_id"`
	Commit    string `json:"commit"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type ProjectGitEvidence struct {
	Citation ProjectGitCitation `json:"citation"`
	Text     string             `json:"text"`
}

type ProjectContextSection struct {
	Intent    string               `json:"intent"`
	State     string               `json:"state"`
	Documents []ProjectGitEvidence `json:"documents"`
	Memories  []MemoryEntry        `json:"reviewed_memories"`
	History   []Hit                `json:"historical_excerpts"`
	Gaps      []string             `json:"gaps"`
	Truncated bool                 `json:"truncated"`
}

type ProjectContextCoverage struct {
	Documents string   `json:"documents"`
	Knowledge string   `json:"knowledge"`
	History   string   `json:"history"`
	Issues    []string `json:"issues"`
}

// Evidence presence does not certify that an excerpt answers a question or
// describes current behavior. Committed documents may themselves be proposals.
type ProjectContextResult struct {
	ProjectID    string                  `json:"project_id"`
	Commit       string                  `json:"commit"`
	Intent       string                  `json:"intent"`
	Kind         string                  `json:"kind"`
	EvidenceOnly bool                    `json:"evidence_only"`
	Sections     []ProjectContextSection `json:"sections"`
	Coverage     ProjectContextCoverage  `json:"coverage"`
	Truncated    bool                    `json:"truncated"`
}
