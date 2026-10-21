package recalltest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"cpa-session-archive/internal/memory"
)

func smallDataset() Dataset {
	return Dataset{Version: 1, Name: "PRIVATE-dataset-title", Records: []Record{{ID: "record", ProjectID: "p", SessionID: "s", EventAt: "2026-10-20T00:00:00Z", Messages: []Message{{Role: "assistant", Content: "PRIVATE query evidence is verified"}}}}, Cases: []Case{{ID: "PRIVATE-case-id", Category: "exact_error", Query: memory.Query{Text: "PRIVATE query", ProjectID: "p"}, RelevantIDs: []string{"record"}, Evidence: []Evidence{{RecordID: "record", Contains: []string{"evidence is verified"}}}}}}
}
func cloneDataset(t *testing.T, source Dataset) Dataset {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var copy Dataset
	if json.Unmarshal(raw, &copy) != nil {
		t.Fatal("clone failed")
	}
	return copy
}

func TestFrozenBaselineAndIsolatedCleanup(t *testing.T) {
	file, err := os.Open("testdata/baseline-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	dataset, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	const frozen = "3ba68a39c5489d213775c9ae1141002700a16d31b7285d565e67ba6f50c914bf"
	if dataset.fileDigest != frozen {
		t.Fatal("frozen corpus changed; version labels deliberately before tuning")
	}
	dir := t.TempDir()
	report, err := Run(context.Background(), dataset, Options{TempParent: dir, Repetitions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if report.DatasetSHA256 != frozen || report.Records != 134 || report.Metrics.Cases != 62 || report.Metrics.Answerable != 56 || report.Latency.Samples != 124 || len(report.Cases) != 62 {
		t.Fatalf("evaluation boundaries changed: %+v", report.Metrics)
	}
	if report.Metrics.RecalledSources < 36 || report.Metrics.EmptyPassCases != 6 || !report.SafetyPassed() {
		t.Fatalf("baseline regressed: %+v", report.Metrics)
	}
	for _, category := range []string{"exact_error", "path", "chinese", "branch", "correction"} {
		if report.Categories[category].RecallAt5 != 1 {
			t.Fatalf("literal category regressed: %s", category)
		}
	}
	if report.ProjectionLag <= 0 {
		t.Fatal("fixture no longer exercises deliberately stale projection")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("isolated databases were not closed and removed", err)
	}
}
func TestDatasetRejectsUnreachableOrUnboundLabels(t *testing.T) {
	edits := []struct {
		name string
		edit func(*Dataset)
	}{
		{"wrong_client", func(d *Dataset) { d.Cases[0].Query.Client = "other-client" }},
		{"wrong_side", func(d *Dataset) { d.Cases[0].Evidence[0].Side = "response" }},
		{"wrong_pointer", func(d *Dataset) { d.Cases[0].Evidence[0].Pointer = "/messages/4/content" }},
		{"system_only", func(d *Dataset) { d.Records[0].Messages[0].Role = "system" }},
		{"split_anchors", func(d *Dataset) {
			d.Records[0].Messages = []Message{{Role: "assistant", Content: "first anchor"}, {Role: "assistant", Content: "second anchor"}}
			d.Cases[0].Evidence[0].Contains = []string{"first anchor", "second anchor"}
		}},
		{"no_anchor", func(d *Dataset) { d.Cases[0].Evidence = nil }},
		{"contradictory_empty", func(d *Dataset) { d.Cases[0].ExpectEmpty = true }},
		{"duplicate_case", func(d *Dataset) { d.Cases = append(d.Cases, d.Cases[0]) }},
		{"positive_forbidden", func(d *Dataset) { d.Cases[0].ForbiddenIDs = []string{"record"} }},
		{"deleted_positive", func(d *Dataset) { d.Changes = []Change{{Kind: "delete", ID: "record"}} }},
	}
	for _, test := range edits {
		t.Run(test.name, func(t *testing.T) {
			dataset := cloneDataset(t, smallDataset())
			test.edit(&dataset)
			if dataset.Validate() == nil {
				t.Fatal("invalid gold labels accepted")
			}
		})
	}
	dataset := smallDataset()
	dataset.Cases[0].Query.Client = "codex"
	dataset.Cases[0].Evidence[0].Side = "original_request"
	dataset.Cases[0].Evidence[0].Pointer = "/messages/0/content"
	if err := dataset.Validate(); err != nil {
		t.Fatal("default client or precise valid pointer rejected", err)
	}
}
func TestLoadedDatasetCannotMasqueradeAsFrozenAfterMutation(t *testing.T) {
	raw, _ := json.Marshal(smallDataset())
	dataset, err := Load(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Records[0].Messages[0].Content += " changed-but-still-valid"
	if _, err = Run(context.Background(), dataset, Options{}); err == nil {
		t.Fatal("modified dataset reused original file digest")
	}
}
func TestCancellationAndAggregatePrivacy(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, smallDataset(), Options{TempParent: dir}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled evaluation did work", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("canceled evaluation created databases")
	}
	report, err := Run(context.Background(), smallDataset(), Options{TempParent: dir})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PRIVATE-dataset-title", "PRIVATE-case-id", "PRIVATE query", "evidence is verified"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private labels escaped aggregate report")
		}
	}
	if report.Metrics.RecallAt5 != 1 {
		t.Fatal("tiny supported answer was not graded")
	}
}

type gradeReader struct {
	memory.Reader
	read func(memory.Citation) (memory.Evidence, error)
}

func (reader gradeReader) ReadSource(_ context.Context, citation memory.Citation) (memory.Evidence, error) {
	return reader.read(citation)
}
func fixtureHit(id, pointer, text string) memory.Hit {
	return memory.Hit{Citation: memory.Citation{RequestID: id, Revision: "v", Side: "original_request", Pointer: pointer, End: len([]rune(text)), NormalizerVersion: 2}, Snippet: text, ProjectID: "p", SessionID: "s", Client: "codex"}
}
func exactReader(hits []memory.Hit) gradeReader {
	return gradeReader{read: func(c memory.Citation) (memory.Evidence, error) {
		for _, hit := range hits {
			if hit.Citation == c {
				return memory.Evidence{Citation: c, Text: hit.Snippet, ProjectID: hit.ProjectID, SessionID: hit.SessionID, Client: hit.Client}, nil
			}
		}
		return memory.Evidence{}, memory.ErrSourceUnavailable
	}}
}

func TestCorrectRequestIDWithoutSupportingExcerptDoesNotScore(t *testing.T) {
	item := Case{RelevantIDs: []string{"r"}, Query: memory.Query{ProjectID: "p"}, Evidence: []Evidence{{RecordID: "r", Contains: []string{"chosen fix"}}}}
	hits := []memory.Hit{fixtureHit("r", "/messages/0/content", "question mentions the keyword"), fixtureHit("r", "/messages/1/content", "the chosen fix was applied")}
	result := grade(context.Background(), exactReader(hits), item, memory.SearchResult{Hits: hits})
	if result.Recalled != 1 || result.ReciprocalRank != .5 || result.CitationChecks != 2 {
		t.Fatalf("source-only grading inflated evidence quality: %+v", result)
	}
	result = grade(context.Background(), exactReader(hits), item, memory.SearchResult{Hits: hits[:1]})
	if result.Recalled != 0 || result.ReciprocalRank != 0 {
		t.Fatal("unsupported matching source scored as an answer")
	}
}
func TestMacroRecallAndEmptyEvidenceAreNotConflated(t *testing.T) {
	var metrics Metrics
	metrics.add(CaseResult{Relevant: 2, Recalled: 1, ReciprocalRank: 1, Returned: 1})
	metrics.add(CaseResult{Relevant: 1, Recalled: 1, ReciprocalRank: .5, Returned: 2})
	metrics.add(CaseResult{ExpectEmpty: true, EmptyPass: true})
	metrics.add(CaseResult{ExpectEmpty: true, EmptyPass: false, Returned: 1})
	metrics.finish()
	if metrics.RecallAt5 != .75 || metrics.HitAt5 != 1 || metrics.MRRAt5 != .75 || metrics.EmptyPassRate != .5 || metrics.RelevantSources != 3 {
		t.Fatalf("incorrect macro denominators: %+v", metrics)
	}
}
func TestScopeCitationAndForbiddenFailuresAreHardGates(t *testing.T) {
	for _, metrics := range []Metrics{{ScopeViolations: 1}, {CitationFailures: 1}, {ForbiddenHits: 1}} {
		if (Report{Metrics: metrics}).SafetyPassed() {
			t.Fatal("invalid evidence passed hard gate")
		}
	}
	item := Case{Query: memory.Query{ProjectID: "p"}, RelevantIDs: []string{"good"}, ForbiddenIDs: []string{"foreign"}}
	hits := []memory.Hit{fixtureHit("foreign", "/f", "foreign content"), fixtureHit("stale", "/s", "stale content")}
	hits[0].ProjectID = "other"
	result := grade(context.Background(), gradeReader{read: func(c memory.Citation) (memory.Evidence, error) {
		if c.RequestID == "stale" {
			return memory.Evidence{}, memory.ErrSourceUnavailable
		}
		return memory.Evidence{Citation: c, Text: hits[0].Snippet, ProjectID: "other"}, nil
	}}, item, memory.SearchResult{Hits: hits})
	if result.ScopeViolations != 1 || result.CitationFailures != 1 || result.ForbiddenHits != 1 {
		t.Fatalf("invalid hit classification: %+v", result)
	}
}
func TestDuplicateExcerptMetricDoesNotMergeSourceIdentity(t *testing.T) {
	hits := []memory.Hit{fixtureHit("first", "/a", "same decision"), fixtureHit("second", "/a", "same decision")}
	item := Case{RelevantIDs: []string{"first", "second"}, Evidence: []Evidence{{RecordID: "first", Contains: []string{"same decision"}}, {RecordID: "second", Contains: []string{"same decision"}}}}
	result := grade(context.Background(), exactReader(hits), item, memory.SearchResult{Hits: hits})
	if result.Recalled != 2 || result.DuplicateExcerptSlots != 1 {
		t.Fatal("display redundancy merged independent source labels")
	}
}
