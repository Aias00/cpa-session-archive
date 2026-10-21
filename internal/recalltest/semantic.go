package recalltest

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"cpa-session-archive/internal/embedding"
	"cpa-session-archive/internal/memory"
)

// SemanticOptions explicitly authorizes model processing of the selected corpus.
// No embedding implementation is selected implicitly by the evaluator.
type SemanticOptions struct {
	Embedder embedding.Embedder
	Minimum  float64
}

func (s SemanticOptions) validate() error {
	if s.Embedder == nil || math.IsNaN(s.Minimum) || math.IsInf(s.Minimum, 0) || s.Minimum < 0 || s.Minimum > 1 {
		return errors.New("invalid semantic evaluation configuration")
	}
	return nil
}

// SemanticReport contains only model identity, counts and costs, never inputs.
type SemanticReport struct {
	Fingerprint      string  `json:"fingerprint"`
	Dimensions       int     `json:"dimensions"`
	Minimum          float64 `json:"minimum"`
	DocumentWindows  int64   `json:"document_windows"`
	QueryTexts       int64   `json:"query_texts"`
	Calls            int64   `json:"calls"`
	FailedCalls      int64   `json:"failed_calls"`
	ModelMS          float64 `json:"model_ms"`
	ReadyTexts       int64   `json:"ready_texts"`
	CoverageComplete bool    `json:"coverage_complete"`
}

type measuredEmbedder struct {
	embedder                          embedding.Embedder
	mu                                sync.Mutex
	documents, queries, calls, failed int64
	elapsed                           time.Duration
}

func (m *measuredEmbedder) Fingerprint() string { return m.embedder.Fingerprint() }
func (m *measuredEmbedder) Dimensions() int     { return m.embedder.Dimensions() }
func (m *measuredEmbedder) Embed(ctx context.Context, purpose embedding.Purpose, texts []string) ([][]float32, error) {
	start := time.Now()
	vectors, err := m.embedder.Embed(ctx, purpose, texts)
	m.mu.Lock()
	m.calls++
	m.elapsed += time.Since(start)
	if purpose == embedding.Query {
		m.queries += int64(len(texts))
	} else {
		m.documents += int64(len(texts))
	}
	if err != nil {
		m.failed++
	}
	m.mu.Unlock()
	return vectors, err
}

func (m *measuredEmbedder) report(status *memory.SemanticStatus, minimum float64) *SemanticReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := &SemanticReport{Minimum: minimum, DocumentWindows: m.documents, QueryTexts: m.queries, Calls: m.calls, FailedCalls: m.failed, ModelMS: float64(m.elapsed.Nanoseconds()) / 1e6}
	if status != nil {
		out.Fingerprint, out.Dimensions = status.Fingerprint, status.Dimensions
		out.ReadyTexts, out.CoverageComplete = status.ReadyTexts, status.CoverageComplete
	}
	return out
}
