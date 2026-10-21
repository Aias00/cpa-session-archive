package recalltest

import (
	"context"
	"crypto/sha256"

	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/storage"
)

type Options struct {
	Repetitions int
	TempParent  string
	Mode        string
	Semantic    *SemanticOptions
}
type CaseResult struct {
	ID                    string  `json:"id"`
	Category              string  `json:"category"`
	Relevant              int     `json:"relevant"`
	Recalled              int     `json:"recalled"`
	ReciprocalRank        float64 `json:"reciprocal_rank"`
	ExpectEmpty           bool    `json:"expect_empty"`
	EmptyPass             bool    `json:"empty_pass"`
	Returned              int     `json:"returned"`
	DuplicateExcerptSlots int     `json:"duplicate_excerpt_slots"`
	ForbiddenHits         int     `json:"forbidden_hits"`
	CitationChecks        int     `json:"citation_checks"`
	CitationFailures      int     `json:"citation_failures"`
	ScopeViolations       int     `json:"scope_violations"`
	Truncated             bool    `json:"truncated"`
}
type Metrics struct {
	Cases                    int     `json:"cases"`
	Answerable               int     `json:"answerable"`
	Unanswerable             int     `json:"unanswerable"`
	FoundCases               int     `json:"found_cases"`
	EmptyPassCases           int     `json:"empty_pass_cases"`
	RelevantSources          int     `json:"relevant_sources"`
	RecalledSources          int     `json:"recalled_sources"`
	HitAt5                   float64 `json:"hit_at_5"`
	RecallAt5                float64 `json:"recall_at_5"`
	MRRAt5                   float64 `json:"mrr_at_5"`
	EmptyPassRate            float64 `json:"empty_pass_rate"`
	ReturnedHits             int     `json:"returned_hits"`
	DuplicateExcerptSlots    int     `json:"duplicate_excerpt_slots"`
	DuplicateExcerptRate     float64 `json:"duplicate_excerpt_rate"`
	ForbiddenHits            int     `json:"forbidden_hits"`
	CitationChecks           int     `json:"citation_checks"`
	CitationFailures         int     `json:"citation_failures"`
	ScopeViolations          int     `json:"scope_violations"`
	TruncatedCases           int     `json:"truncated_cases"`
	recallSum, reciprocalSum float64
}
type Latency struct {
	Samples int     `json:"samples"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
	MaxMS   float64 `json:"max_ms"`
}
type Report struct {
	Semantic             *SemanticReport     `json:"semantic,omitempty"`
	Version              int                 `json:"version"`
	DatasetSHA256        string              `json:"dataset_sha256"`
	RetrievalMode        string              `json:"retrieval_mode"`
	GradingUnit          string              `json:"grading_unit"`
	SyntheticLatencyOnly bool                `json:"isolated_fixture_latency_only"`
	GOOS                 string              `json:"goos"`
	GOARCH               string              `json:"goarch"`
	Repetitions          int                 `json:"repetitions"`
	Records              int                 `json:"records"`
	PostIndexChanges     int                 `json:"post_index_changes"`
	ProjectionLag        int64               `json:"projection_lag"`
	ArchiveBytes         int64               `json:"archive_bytes"`
	ProjectionBytes      int64               `json:"projection_bytes"`
	Metrics              Metrics             `json:"metrics"`
	Categories           map[string]*Metrics `json:"categories"`
	Latency              Latency             `json:"search_latency"`
	Cases                []CaseResult        `json:"-"`
}

func (report Report) SafetyPassed() bool {
	return report.Metrics.CitationFailures == 0 && report.Metrics.ScopeViolations == 0 && report.Metrics.ForbiddenHits == 0
}

func (m *Metrics) add(result CaseResult) {
	m.Cases++
	m.ReturnedHits += result.Returned
	m.DuplicateExcerptSlots += result.DuplicateExcerptSlots
	m.ForbiddenHits += result.ForbiddenHits
	m.CitationChecks += result.CitationChecks
	m.CitationFailures += result.CitationFailures
	m.ScopeViolations += result.ScopeViolations
	if result.Truncated {
		m.TruncatedCases++
	}
	if result.ExpectEmpty {
		m.Unanswerable++
		if result.EmptyPass {
			m.EmptyPassCases++
		}
	} else {
		m.Answerable++
		m.RelevantSources += result.Relevant
		m.RecalledSources += result.Recalled
		if result.Recalled > 0 {
			m.FoundCases++
		}
		m.recallSum += float64(result.Recalled) / float64(result.Relevant)
		m.reciprocalSum += result.ReciprocalRank
	}
}
func (m *Metrics) finish() {
	if m.Answerable > 0 {
		m.HitAt5 = float64(m.FoundCases) / float64(m.Answerable)
		m.RecallAt5 = m.recallSum / float64(m.Answerable)
		m.MRRAt5 = m.reciprocalSum / float64(m.Answerable)
	}
	if m.Unanswerable > 0 {
		m.EmptyPassRate = float64(m.EmptyPassCases) / float64(m.Unanswerable)
	}
	if m.ReturnedHits > 0 {
		m.DuplicateExcerptRate = float64(m.DuplicateExcerptSlots) / float64(m.ReturnedHits)
	}
}
func fixtureRecord(record Record) archive.Record {
	body, _ := json.Marshal(struct {
		Messages []Message `json:"messages"`
	}{record.Messages})
	when, _ := time.Parse(time.RFC3339Nano, record.EventAt)
	client := fixtureClient(record)
	return archive.Record{RequestID: record.ID, SessionID: record.SessionID, StartedAt: when, CompletedAt: when.Add(time.Second), Outcome: "success", StatusCode: 200, SourceFormat: "openai", OriginalRequest: body, Response: []byte(`{"choices":[]}`), Facets: map[string][]string{"project.id": {record.ProjectID}, "client": {client}}}
}
func synchronize(ctx context.Context, engine *memory.Engine) error {
	for step := 0; step < 100000; step++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := engine.Sync(ctx, 256)
		if err != nil {
			return err
		}
		if n == 0 {
			status, err := engine.Status(ctx)
			if err != nil {
				return err
			}
			if status.BackfillDone && status.MessageMetadataDone && status.Lag == 0 {
				if status.Semantic == nil || status.Semantic.CoverageComplete {
					return nil
				}
				if _, err := engine.SyncSemantic(ctx, 64); err != nil {
					return err
				}
			}
		}
	}
	return errors.New("isolated projection did not converge")
}
func fileBytes(path string) int64 {
	var size int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil {
			size += info.Size()
		}
	}
	return size
}

func effectiveRetrievalMode(dataset Dataset, override string) (string, error) {
	if override != "" && override != "literal" && override != "diverse" && override != "hybrid" {
		return "", errors.New("evaluation mode must be literal, diverse or hybrid")
	}
	mode := ""
	for _, item := range dataset.Cases {
		current := item.Query.Mode
		if current == "" {
			current = "literal"
		}
		if current != "literal" && current != "diverse" && current != "hybrid" {
			return "", errors.New("unsupported evaluation query mode")
		}
		if override != "" {
			current = override
		}
		if mode == "" {
			mode = current
		} else if mode != current {
			mode = "mixed"
		}
	}
	return mode, nil
}

// Run always creates disposable private databases, including for private datasets.
// Its aggregate report contains no dataset names, queries, labels or snippets.
func Run(ctx context.Context, dataset Dataset, options Options) (Report, error) {
	var report Report
	if err := dataset.Validate(); err != nil {
		return report, err
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	mode, err := effectiveRetrievalMode(dataset, options.Mode)
	if err != nil {
		return report, err
	}
	if options.Semantic == nil {
		for _, item := range dataset.Cases {
			if options.Mode == "hybrid" || (options.Mode == "" && item.Query.Mode == "hybrid") {
				return report, errors.New("hybrid evaluation requires an explicitly configured local embedder")
			}
		}
	}
	canonical := datasetDigest(dataset)
	if dataset.fileDigest != "" && canonical != dataset.contentDigest {
		return report, errors.New("evaluation dataset changed after loading")
	}
	if options.Repetitions == 0 {
		options.Repetitions = 1
	}
	if options.Repetitions < 1 || options.Repetitions > 100 {
		return report, errors.New("repetitions must be 1 to 100")
	}
	root, err := os.MkdirTemp(options.TempParent, "cpa-recall-eval-")
	if err != nil {
		return report, errors.New("isolated evaluation directory unavailable")
	}
	defer os.RemoveAll(root)
	rawPath, projectionPath := filepath.Join(root, "archive.sqlite"), filepath.Join(root, "memory.sqlite")
	raw, err := archive.OpenStore(rawPath, false)
	if err != nil {
		return report, errors.New("isolated archive unavailable")
	}
	defer raw.DB.Close()
	var open []memory.OpenOption
	var measured *measuredEmbedder
	if options.Semantic != nil {
		if err := options.Semantic.validate(); err != nil {
			return report, err
		}
		measured = &measuredEmbedder{embedder: options.Semantic.Embedder}
		open = append(open, memory.WithSemantic(measured, options.Semantic.Minimum))
	}
	engine, err := memory.Open(projectionPath, storage.NewSQLite(raw), open...)
	if err != nil {
		return report, errors.New("isolated projection unavailable")
	}
	defer engine.Close()
	for offset := 0; offset < len(dataset.Records); offset += 64 {
		end := min(offset+64, len(dataset.Records))
		batch := make([]archive.Record, 0, end-offset)
		for _, record := range dataset.Records[offset:end] {
			batch = append(batch, fixtureRecord(record))
		}
		if err = raw.PutBatch(batch); err != nil {
			return report, errors.New("isolated fixture insertion failed")
		}
	}
	if err = synchronize(ctx, engine); err != nil {
		return report, errors.New("isolated initial projection failed")
	}
	for _, change := range dataset.Changes {
		// Full replacement is a fixture fault operation, not ordinary archive upsert.
		if _, err = raw.DB.ExecContext(ctx, `DELETE FROM records WHERE request_id=?`, change.ID); err != nil {
			return report, errors.New("isolated fixture change failed")
		}
		if change.Kind == "replace" {
			if err = raw.PutBatch([]archive.Record{fixtureRecord(*change.Record)}); err != nil {
				return report, errors.New("isolated fixture replacement failed")
			}
		}
		if change.SyncProjection {
			if err = synchronize(ctx, engine); err != nil {
				return report, errors.New("isolated change projection failed")
			}
		}
	}
	digest := dataset.fileDigest
	if digest == "" {
		digest = canonical
	}
	report = Report{Version: 1, DatasetSHA256: digest, RetrievalMode: mode, GradingUnit: "evidence_constrained_source", SyntheticLatencyOnly: true, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Repetitions: options.Repetitions, Records: len(dataset.Records), PostIndexChanges: len(dataset.Changes), Categories: map[string]*Metrics{}}
	elapsed := []float64{}
	for repeat := 0; repeat < options.Repetitions; repeat++ {
		for _, item := range dataset.Cases {
			query := item.Query
			query.Limit = 5
			if options.Mode != "" {
				query.Mode = options.Mode
			}
			scope := memory.Scope{ProjectID: query.ProjectID, SessionID: query.SessionID}
			if scope.ProjectID == "" && scope.SessionID == "" {
				scope.AllowAll = true
			}
			reader, err := memory.WithScope(engine, scope)
			if err != nil {
				return report, errors.New("evaluation reader scope unavailable")
			}
			start := time.Now()
			result, err := reader.Search(ctx, query)
			elapsed = append(elapsed, float64(time.Since(start).Nanoseconds())/1e6)
			if err != nil {
				return report, errors.New("isolated evaluation search failed")
			}
			if repeat != 0 {
				continue
			}
			graded := grade(ctx, reader, item, result)
			report.Cases = append(report.Cases, graded)
			report.Metrics.add(graded)
			if report.Categories[item.Category] == nil {
				report.Categories[item.Category] = &Metrics{}
			}
			report.Categories[item.Category].add(graded)
		}
	}
	report.Metrics.finish()
	for _, metrics := range report.Categories {
		metrics.finish()
	}
	sort.Float64s(elapsed)
	report.Latency = Latency{Samples: len(elapsed), P50MS: elapsed[int(math.Ceil(.5*float64(len(elapsed))))-1], P95MS: elapsed[int(math.Ceil(.95*float64(len(elapsed))))-1], MaxMS: elapsed[len(elapsed)-1]}
	status, err := engine.Status(ctx)
	if err != nil {
		return report, errors.New("isolated evaluation status failed")
	}
	report.ProjectionLag = status.Lag
	if measured != nil {
		report.Semantic = measured.report(status.Semantic, options.Semantic.Minimum)
	}
	report.ArchiveBytes, report.ProjectionBytes = fileBytes(rawPath), fileBytes(projectionPath)
	return report, nil
}
func grade(ctx context.Context, reader memory.Reader, item Case, result memory.SearchResult) CaseResult {
	out := CaseResult{ID: item.ID, Category: item.Category, Relevant: len(item.RelevantIDs), ExpectEmpty: item.ExpectEmpty, Returned: len(result.Hits), EmptyPass: item.ExpectEmpty && len(result.Hits) == 0, Truncated: result.Truncated}
	found := map[string]bool{}
	excerpts := map[[32]byte]bool{}
	forbidden := map[string]bool{}
	for _, id := range item.ForbiddenIDs {
		forbidden[id] = true
	}
	for rank, hit := range result.Hits {
		if rank >= 5 {
			break
		}
		fingerprint := sha256.Sum256([]byte(hit.Snippet))
		if excerpts[fingerprint] {
			out.DuplicateExcerptSlots++
		}
		excerpts[fingerprint] = true
		if forbidden[hit.Citation.RequestID] {
			out.ForbiddenHits++
		}
		scopeOK := (item.Query.ProjectID == "" || hit.ProjectID == item.Query.ProjectID) && (item.Query.SessionID == "" || hit.SessionID == item.Query.SessionID) && (item.Query.Client == "" || hit.Client == item.Query.Client)
		if !scopeOK {
			out.ScopeViolations++
		}
		out.CitationChecks++
		evidence, err := reader.ReadSource(ctx, hit.Citation)
		if err != nil || evidence.Text != hit.Snippet || evidence.Citation != hit.Citation {
			out.CitationFailures++
			continue
		}
		if (item.Query.ProjectID != "" && evidence.ProjectID != item.Query.ProjectID) || (item.Query.SessionID != "" && evidence.SessionID != item.Query.SessionID) || (item.Query.Client != "" && evidence.Client != item.Query.Client) {
			if scopeOK {
				out.ScopeViolations++
			}
			continue
		}
		if !scopeOK {
			continue
		}
		for _, label := range item.Evidence {
			if label.RecordID != hit.Citation.RequestID || (label.Side != "" && label.Side != hit.Citation.Side) || (label.Pointer != "" && label.Pointer != hit.Citation.Pointer) {
				continue
			}
			matches := true
			for _, anchor := range label.Contains {
				if !strings.Contains(hit.Snippet, anchor) {
					matches = false
					break
				}
			}
			if matches {
				found[label.RecordID] = true
				if out.ReciprocalRank == 0 {
					out.ReciprocalRank = 1 / float64(rank+1)
				}
				break
			}
		}
	}
	out.Recalled = len(found)
	return out
}
