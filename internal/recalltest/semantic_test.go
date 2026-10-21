package recalltest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cpa-session-archive/internal/embedding"
)

// This fake verifies plumbing and grading; it measures no semantic quality.
type syntheticEmbedder struct{ fail bool }

func (syntheticEmbedder) Fingerprint() string { return "synthetic-evaluator-plumbing-only-r384-o96-v1" }
func (syntheticEmbedder) Dimensions() int     { return 2 }
func (e syntheticEmbedder) Embed(_ context.Context, _ embedding.Purpose, texts []string) ([][]float32, error) {
	if e.fail {
		return nil, errors.New("PRIVATE provider request details")
	}
	vectors := make([][]float32, len(texts))
	for i := range texts {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestHybridEvaluatorUsesExplicitModelWithoutChangingEvidenceLabels(t *testing.T) {
	dataset := smallDataset()
	dataset.Cases[0].Query.Text = "an unrelated synthetic wording"
	dataset.Cases[0].Category = "paraphrase"
	before, _ := json.Marshal(dataset)
	report, err := Run(context.Background(), dataset, Options{Mode: "hybrid", Semantic: &SemanticOptions{Embedder: syntheticEmbedder{}, Minimum: .8}, TempParent: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if report.RetrievalMode != "hybrid" || report.Metrics.RecallAt5 != 1 || !report.SafetyPassed() || report.Semantic == nil || !report.Semantic.CoverageComplete || report.Semantic.DocumentWindows == 0 || report.Semantic.QueryTexts != 1 || report.Semantic.Dimensions != 2 {
		t.Fatalf("hybrid evaluator mechanics failed: %+v", report)
	}
	after, _ := json.Marshal(dataset)
	if !bytes.Equal(before, after) {
		t.Fatal("hybrid mode rewrote query/evidence labels")
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "unrelated synthetic") {
		t.Fatal("semantic report leaked corpus text")
	}
}

func TestHybridEvaluationDoesNotImplicitlySelectOrIgnoreAProvider(t *testing.T) {
	if _, err := Run(context.Background(), smallDataset(), Options{Mode: "hybrid"}); err == nil {
		t.Fatal("hybrid evaluation silently used literal mode")
	}
	_, err := Run(context.Background(), smallDataset(), Options{Mode: "hybrid", Semantic: &SemanticOptions{Embedder: syntheticEmbedder{fail: true}, Minimum: .8}})
	if err == nil || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("embedding failure disappeared or leaked provider details")
	}
}
