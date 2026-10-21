package recalltest

import (
	"context"
	"errors"
	"math"

	"cpa-session-archive/internal/embedding"
)

// CalibrationOptions defines the search before any candidate is evaluated.
// Calibration uses new disposable databases for every threshold. Its costs
// include repeated indexing; it does not benchmark an incremental ranker.
type CalibrationOptions struct {
	Embedder      embedding.Embedder
	Thresholds    []float64
	MinimumRecall float64
	TempParent    string
}

type CalibrationCandidate struct {
	Minimum  float64  `json:"minimum"`
	Eligible bool     `json:"eligible"`
	Failures []string `json:"failures"`
	Report   Report   `json:"report"`
}

// CalibrationReport is aggregate-only and records a selection, not a rollout
// authorization. A successful selection still requires a separate held-out run.
type CalibrationReport struct {
	Version         int                    `json:"version"`
	Purpose         string                 `json:"purpose"`
	DatasetSHA256   string                 `json:"dataset_sha256"`
	MinimumRecall   float64                `json:"minimum_recall_at_5"`
	SelectionRule   string                 `json:"selection_rule"`
	Thresholds      []float64              `json:"thresholds"`
	SelectedMinimum *float64               `json:"selected_minimum"`
	Candidates      []CalibrationCandidate `json:"candidates"`
}

func (r CalibrationReport) CalibrationPassed() bool {
	if r.Version != 1 || r.SelectedMinimum == nil || len(r.Thresholds) == 0 || len(r.Candidates) != len(r.Thresholds) || math.IsNaN(r.MinimumRecall) || math.IsInf(r.MinimumRecall, 0) || r.MinimumRecall < .8 || r.MinimumRecall > 1 {
		return false
	}
	var selected *float64
	profile := ""
	for i, c := range r.Candidates {
		if math.IsNaN(c.Minimum) || c.Minimum < 0 || c.Minimum > 1 || c.Minimum != r.Thresholds[i] || (i > 0 && c.Minimum <= r.Thresholds[i-1]) || c.Report.DatasetSHA256 != r.DatasetSHA256 || c.Report.RetrievalMode != "hybrid" || c.Report.Semantic == nil || c.Report.Semantic.Minimum != c.Minimum {
			return false
		}
		if i == 0 {
			profile = c.Report.Semantic.Fingerprint
		}
		if profile == "" || c.Report.Semantic.Fingerprint != profile {
			return false
		}
		eligible := len(calibrationFailures(c.Report, r.MinimumRecall)) == 0
		if c.Eligible != eligible {
			return false
		}
		if eligible && selected == nil {
			value := c.Minimum
			selected = &value
		}
	}
	return selected != nil && *selected == *r.SelectedMinimum
}

// CompletePassed rejects partial retrieval, including an empty result obtained
// from an incomplete semantic index. Raw feed lag is a separate measurement:
// frozen stale-source fixtures deliberately leave it nonzero.
func (r Report) CompletePassed() bool {
	return r.Metrics.TruncatedCases == 0 && ((r.Semantic == nil && r.RetrievalMode != "hybrid") || (r.Semantic != nil && r.Semantic.CoverageComplete))
}

func calibrationFailures(r Report, minimumRecall float64) []string {
	failures := []string{}
	if !r.SafetyPassed() {
		failures = append(failures, "citation_scope_or_forbidden")
	}
	if !r.CompletePassed() {
		failures = append(failures, "incomplete_retrieval")
	}
	if r.Metrics.Unanswerable < 1 || r.Metrics.EmptyPassCases != r.Metrics.Unanswerable {
		failures = append(failures, "negative_false_positive")
	}
	if r.Metrics.Answerable < 1 || math.IsNaN(r.Metrics.RecallAt5) || math.IsInf(r.Metrics.RecallAt5, 0) || r.Metrics.RecallAt5 < minimumRecall || r.Metrics.RecallAt5 > 1 {
		failures = append(failures, "positive_recall")
	}
	return failures
}

// Calibrate selects the lowest predeclared threshold that passes every gate.
// No candidate is selected after an execution failure or provider change.
// This function never reads a held-out corpus or changes a production setting.
func Calibrate(ctx context.Context, dataset Dataset, options CalibrationOptions) (CalibrationReport, error) {
	var out CalibrationReport
	if err := dataset.Validate(); err != nil {
		return out, err
	}
	// The observed baseline is a regression fixture, never a tuning split.
	// Its canonical digest also catches whitespace-only reserialization.
	if datasetDigest(dataset) == "14b78da53361fb4f6dedfd3f01faac394c981cd33a62598165a582d20269328e" {
		return out, errors.New("the observed frozen baseline cannot be used for calibration")
	}
	if options.Embedder == nil || math.IsNaN(options.MinimumRecall) || math.IsInf(options.MinimumRecall, 0) || options.MinimumRecall < .8 || options.MinimumRecall > 1 || len(options.Thresholds) < 1 || len(options.Thresholds) > 100 {
		return out, errors.New("invalid calibration configuration")
	}
	for i, minimum := range options.Thresholds {
		if math.IsNaN(minimum) || math.IsInf(minimum, 0) || minimum < 0 || minimum > 1 || (i > 0 && minimum <= options.Thresholds[i-1]) {
			return out, errors.New("calibration thresholds must be distinct ascending values from zero to one")
		}
	}
	positive, negative := 0, 0
	for _, item := range dataset.Cases {
		if item.ExpectEmpty {
			negative++
		} else {
			positive++
		}
	}
	if positive == 0 || negative == 0 {
		return out, errors.New("calibration requires both supported and empty-expected cases")
	}
	fingerprint, dimensions := options.Embedder.Fingerprint(), options.Embedder.Dimensions()
	if fingerprint == "" || len(fingerprint) > 1024 || dimensions < 1 || dimensions > 4096 {
		return out, errors.New("invalid calibration provider identity")
	}
	out = CalibrationReport{Version: 1, Purpose: "calibration_only_requires_held_out_evaluation", MinimumRecall: options.MinimumRecall, SelectionRule: "lowest_threshold_passing_safety_complete_all_negatives_and_minimum_recall", Thresholds: append([]float64(nil), options.Thresholds...), Candidates: []CalibrationCandidate{}}
	for _, minimum := range out.Thresholds {
		if ctx.Err() != nil {
			return CalibrationReport{}, errors.New("calibration canceled")
		}
		if options.Embedder.Fingerprint() != fingerprint || options.Embedder.Dimensions() != dimensions {
			return CalibrationReport{}, errors.New("calibration provider identity changed")
		}
		r, err := Run(ctx, dataset, Options{Mode: "hybrid", TempParent: options.TempParent, Semantic: &SemanticOptions{Embedder: options.Embedder, Minimum: minimum}})
		if err != nil {
			return CalibrationReport{}, errors.New("isolated calibration candidate failed")
		}
		if options.Embedder.Fingerprint() != fingerprint || options.Embedder.Dimensions() != dimensions {
			return CalibrationReport{}, errors.New("calibration provider identity changed")
		}
		if out.DatasetSHA256 == "" {
			out.DatasetSHA256 = r.DatasetSHA256
		}
		if r.DatasetSHA256 != out.DatasetSHA256 || r.Semantic == nil || (len(out.Candidates) > 0 && r.Semantic.Fingerprint != out.Candidates[0].Report.Semantic.Fingerprint) {
			return CalibrationReport{}, errors.New("calibration dataset or profile changed")
		}
		failures := calibrationFailures(r, options.MinimumRecall)
		eligible := len(failures) == 0
		out.Candidates = append(out.Candidates, CalibrationCandidate{Minimum: minimum, Eligible: eligible, Failures: failures, Report: r})
		if eligible && out.SelectedMinimum == nil {
			selected := minimum
			out.SelectedMinimum = &selected
		}
	}
	return out, nil
}
