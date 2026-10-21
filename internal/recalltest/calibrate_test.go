package recalltest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"cpa-session-archive/internal/embedding"
	"cpa-session-archive/internal/memory"
)

type calibrationEmbedder struct {
	negativeScore  float64
	calls          int
	failAfter      int
	driftAfter     int
	driftDimension bool
}

func (e *calibrationEmbedder) Fingerprint() string {
	if e.driftAfter > 0 && e.calls > e.driftAfter && !e.driftDimension {
		return "synthetic-changed-profile"
	}
	return "synthetic-calibration-plumbing-only"
}
func (e *calibrationEmbedder) Dimensions() int {
	if e.driftAfter > 0 && e.calls > e.driftAfter && e.driftDimension {
		return 3
	}
	return 2
}
func (e *calibrationEmbedder) Embed(_ context.Context, purpose embedding.Purpose, texts []string) ([][]float32, error) {
	e.calls++
	if e.failAfter > 0 && e.calls > e.failAfter {
		return nil, errors.New("PRIVATE provider diagnostic")
	}
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		score := 1.0
		if purpose == embedding.Query {
			score = .95
			if strings.Contains(text, "absent") {
				score = e.negativeScore
			}
		}
		vectors[i] = []float32{float32(score), float32(math.Sqrt(1 - score*score))}
	}
	return vectors, nil
}

func calibrationDataset() Dataset {
	d := smallDataset()
	d.Cases[0].Query.Text = "PRIVATE differently phrased supported question"
	d.Cases[0].Category = "paraphrase"
	d.Cases = append(d.Cases, Case{ID: "PRIVATE-negative", Category: "missing", Query: memory.Query{Text: "PRIVATE absent question", ProjectID: "p"}, ExpectEmpty: true})
	return d
}

func TestCalibrationSelectsPredeclaredLowestEligibleAndPreservesInput(t *testing.T) {
	d := calibrationDataset()
	before, _ := json.Marshal(d)
	parent := t.TempDir()
	report, err := Calibrate(context.Background(), d, CalibrationOptions{Embedder: &calibrationEmbedder{negativeScore: .85}, Thresholds: []float64{.8, .9, .92, .98}, MinimumRecall: .8, TempParent: parent})
	if err != nil {
		t.Fatal(err)
	}
	if !report.CalibrationPassed() || *report.SelectedMinimum != .9 || len(report.Candidates) != 4 || report.Candidates[0].Eligible || !report.Candidates[1].Eligible || !report.Candidates[2].Eligible || report.Candidates[3].Eligible {
		t.Fatalf("incorrect selection: %+v", report)
	}
	if strings.Join(report.Candidates[0].Failures, ",") != "negative_false_positive" || strings.Join(report.Candidates[3].Failures, ",") != "positive_recall" {
		t.Fatal("positive/negative failures were not kept separate")
	}
	for _, candidate := range report.Candidates {
		if candidate.Report.DatasetSHA256 != report.DatasetSHA256 || candidate.Report.RetrievalMode != "hybrid" || candidate.Report.Semantic.Minimum != candidate.Minimum || !candidate.Report.CompletePassed() {
			t.Fatal("candidate lost input, mode, threshold or completeness")
		}
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "case-id") || strings.Contains(string(raw), "snippets") {
		t.Fatal("aggregate calibration leaked private corpus data")
	}
	after, _ := json.Marshal(d)
	if !bytes.Equal(before, after) {
		t.Fatal("calibration changed questions or evidence")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("calibration did not close/remove disposable databases", err)
	}
}

func TestCalibrationDoesNotSelectAnAllEmptyOrUnsafeCandidate(t *testing.T) {
	r, err := Calibrate(context.Background(), calibrationDataset(), CalibrationOptions{Embedder: &calibrationEmbedder{negativeScore: .95}, Thresholds: []float64{.9, .98}, MinimumRecall: .8, TempParent: t.TempDir()})
	if err != nil || r.CalibrationPassed() || r.SelectedMinimum != nil || len(r.Candidates) != 2 {
		t.Fatal("infeasible calibration was promoted", err)
	}
	good := Report{Metrics: Metrics{Answerable: 1, Unanswerable: 1, EmptyPassCases: 1, RecallAt5: 1}, Semantic: &SemanticReport{CoverageComplete: true}}
	if len(calibrationFailures(good, .8)) != 0 {
		t.Fatal("valid calibration report failed")
	}
	for _, mutate := range []func(*Report){
		func(r *Report) { r.Metrics.ForbiddenHits = 1 },
		func(r *Report) { r.Metrics.ScopeViolations = 1 },
		func(r *Report) { r.Metrics.CitationFailures = 1 },
		func(r *Report) { r.Metrics.TruncatedCases = 1 },
		func(r *Report) { r.Semantic = &SemanticReport{CoverageComplete: false} },
		func(r *Report) { r.Metrics.RecallAt5 = math.NaN() },
		func(r *Report) { r.Metrics.RecallAt5 = math.Inf(1) },
	} {
		r := good
		mutate(&r)
		if len(calibrationFailures(r, .8)) == 0 {
			t.Fatal("unsafe or incomplete report passed")
		}
	}
}

func TestCalibrationRejectsObservedBaselineAndInconsistentSelection(t *testing.T) {
	f, err := os.Open("testdata/baseline-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Load(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []Dataset{d, cloneDataset(t, d)} {
		e := &calibrationEmbedder{}
		if _, err := Calibrate(context.Background(), fixture, CalibrationOptions{Embedder: e, Thresholds: []float64{.9}, MinimumRecall: .8}); err == nil || e.calls != 0 {
			t.Fatal("observed frozen baseline was used for tuning")
		}
	}
	r, err := Calibrate(context.Background(), calibrationDataset(), CalibrationOptions{Embedder: &calibrationEmbedder{negativeScore: .85}, Thresholds: []float64{.9, .92}, MinimumRecall: .8, TempParent: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var copy CalibrationReport
	raw, _ := json.Marshal(r)
	if err := json.Unmarshal(raw, &copy); err != nil || !copy.CalibrationPassed() {
		t.Fatal("valid roundtripped report lost selection", err)
	}
	wrong := .92
	copy.SelectedMinimum = &wrong
	if copy.CalibrationPassed() {
		t.Fatal("nonminimal selection was accepted")
	}
	copy.SelectedMinimum = r.SelectedMinimum
	copy.Candidates[0].Report.Metrics.TruncatedCases++
	if copy.CalibrationPassed() {
		t.Fatal("stale eligible flag was trusted")
	}
}

func TestCalibrationExecutionFailureDiscardsEarlierSelection(t *testing.T) {
	r, err := Calibrate(context.Background(), calibrationDataset(), CalibrationOptions{Embedder: &calibrationEmbedder{negativeScore: .85, failAfter: 3}, Thresholds: []float64{.9, .92}, MinimumRecall: .8, TempParent: t.TempDir()})
	if err == nil || strings.Contains(err.Error(), "PRIVATE") || r.CalibrationPassed() || len(r.Candidates) != 0 {
		t.Fatal("provider error retained a partial successful selection or exposed details", err)
	}
}

func TestCalibrationProviderDriftDiscardsEarlierSelection(t *testing.T) {
	for _, dimension := range []bool{false, true} {
		r, err := Calibrate(context.Background(), calibrationDataset(), CalibrationOptions{Embedder: &calibrationEmbedder{negativeScore: .85, driftAfter: 3, driftDimension: dimension}, Thresholds: []float64{.9, .92}, MinimumRecall: .8, TempParent: t.TempDir()})
		if err == nil || r.CalibrationPassed() || len(r.Candidates) != 0 {
			t.Fatal("provider drift retained a partial selected threshold", err)
		}
	}
}

func TestCalibrationRejectsUnboundedOrTrivialSearchBeforeModelCalls(t *testing.T) {
	for _, thresholds := range [][]float64{nil, {.9, .8}, {.8, .8}, {-.1}, {1.1}, {math.NaN()}, {math.Inf(1)}, make([]float64, 101)} {
		e := &calibrationEmbedder{}
		if _, err := Calibrate(context.Background(), calibrationDataset(), CalibrationOptions{Embedder: e, Thresholds: thresholds, MinimumRecall: .8}); err == nil || e.calls != 0 {
			t.Fatal("invalid threshold grid reached model")
		}
	}
	for _, minimum := range []float64{0, .79, 1.1, math.NaN(), math.Inf(1)} {
		e := &calibrationEmbedder{}
		if _, err := Calibrate(context.Background(), calibrationDataset(), CalibrationOptions{Embedder: e, Thresholds: []float64{.8}, MinimumRecall: minimum}); err == nil || e.calls != 0 {
			t.Fatal("trivial or invalid recall gate reached model")
		}
	}
	for _, empty := range []bool{false, true} {
		d := calibrationDataset()
		if empty {
			d.Cases = d.Cases[1:]
		} else {
			d.Cases = d.Cases[:1]
		}
		e := &calibrationEmbedder{}
		if _, err := Calibrate(context.Background(), d, CalibrationOptions{Embedder: e, Thresholds: []float64{.8}, MinimumRecall: .8}); err == nil || e.calls != 0 {
			t.Fatal("one-sided calibration reached model")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := &calibrationEmbedder{}
	if _, err := Calibrate(ctx, calibrationDataset(), CalibrationOptions{Embedder: e, Thresholds: []float64{.8}, MinimumRecall: .8}); err == nil || e.calls != 0 {
		t.Fatal("canceled calibration reached model")
	}
}

func TestEmptyGradingRequiresCompleteRetrieval(t *testing.T) {
	item := Case{ID: "negative", Category: "missing", ExpectEmpty: true}
	for _, result := range []memory.SearchResult{
		{Truncated: true},
		{Retrieval: &memory.RetrievalStatus{Mode: "hybrid", SemanticComplete: false}},
		{Truncated: true, Retrieval: &memory.RetrievalStatus{Mode: "hybrid", SemanticComplete: true}},
	} {
		graded := grade(context.Background(), nil, item, result)
		if graded.EmptyPass || !graded.Truncated {
			t.Fatal("incomplete empty result was graded as a true negative")
		}
	}
	for _, result := range []memory.SearchResult{{}, {Retrieval: &memory.RetrievalStatus{Mode: "hybrid", SemanticComplete: true}}} {
		if !grade(context.Background(), nil, item, result).EmptyPass {
			t.Fatal("complete empty result failed")
		}
	}
}
