// Package memory provides local source-grounded recall of retained requests.
package memory

import (
	"context"
	"errors"
)

var (
	ErrInvalidQuery      = errors.New("invalid query")
	ErrSourceUnavailable = errors.New("source revision unavailable")
)

// Query values are literal text and exact scope filters, never SQL/FTS syntax.
type Query struct {
	Text      string `json:"text"`
	ProjectID string `json:"project_id,omitempty"`
	Client    string `json:"client,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	After     string `json:"after,omitempty"`
	Before    string `json:"before,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// Citation ranges are Unicode rune offsets within one normalized message.
type Citation struct {
	NormalizerVersion int    `json:"normalizer_version"`
	RequestID         string `json:"request_id"`
	Revision          string `json:"revision"`
	Side              string `json:"side"`
	Pointer           string `json:"pointer"`
	Start             int    `json:"start"`
	End               int    `json:"end"`
}

type Hit struct {
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
	UniqueChunks       int64  `json:"unique_chunks"`
	DeduplicatedChunks int64  `json:"deduplicated_chunks"`
	ScopeFiltered      bool   `json:"scope_filtered,omitempty"`
	BackfillDone       bool   `json:"backfill_done"`
	BackfillAfterID    int64  `json:"backfill_after_id"`
	Checkpoint         int64  `json:"checkpoint"`
	Watermark          int64  `json:"watermark"`
	Lag                int64  `json:"lag"`
	Sources            int64  `json:"sources"`
	Chunks             int64  `json:"chunks"`
	PartialSources     int64  `json:"partial_sources"`
	Failures           int64  `json:"failures"`
	LastError          string `json:"last_error,omitempty"`
}

type SearchResult struct {
	Hits      []Hit  `json:"hits"`
	Truncated bool   `json:"truncated"`
	Status    Status `json:"status"`
}

type Evidence struct {
	ProjectID string   `json:"project_id"`
	SessionID string   `json:"session_id"`
	Client    string   `json:"client"`
	Citation  Citation `json:"citation"`
	Text      string   `json:"text"`
	Partial   bool     `json:"partial"`
	Issues    []string `json:"issues"`
}

type RecapResult struct {
	Text   string `json:"text"`
	Hits   []Hit  `json:"hits"`
	Status Status `json:"status"`
}

// Reader is the capability an HTTP/MCP frontend needs from a recall engine.
type Reader interface {
	Search(context.Context, Query) (SearchResult, error)
	ReadSource(context.Context, Citation) (Evidence, error)
	Recap(context.Context, Query) (RecapResult, error)
	Status(context.Context) (Status, error)
}
