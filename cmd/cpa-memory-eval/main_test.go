package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/recalltest"
)

func privateDataset() recalltest.Dataset {
	return recalltest.Dataset{
		Version: 1,
		Name:    "PRIVATE_DATASET_NAME",
		Records: []recalltest.Record{{
			ID:        "PRIVATE_RECORD_IDENTIFIER",
			ProjectID: "PRIVATE_PROJECT_IDENTIFIER",
			SessionID: "PRIVATE_SESSION_IDENTIFIER",
			Client:    "codex",
			EventAt:   "2026-09-01T08:00:00Z",
			Messages: []recalltest.Message{{
				Role:    "assistant",
				Content: "privateLookupKeyword: the accepted decision is PRIVATE_EVIDENCE_ANCHOR.",
			}},
		}},
		Cases: []recalltest.Case{{
			ID:       "PRIVATE_CASE_IDENTIFIER",
			Category: "exact_error",
			Query: memory.Query{
				Text:      "privateLookupKeyword",
				ProjectID: "PRIVATE_PROJECT_IDENTIFIER",
				SessionID: "PRIVATE_SESSION_IDENTIFIER",
				Limit:     5,
			},
			RelevantIDs: []string{"PRIVATE_RECORD_IDENTIFIER"},
			Evidence: []recalltest.Evidence{{
				RecordID: "PRIVATE_RECORD_IDENTIFIER",
				Contains: []string{"PRIVATE_EVIDENCE_ANCHOR"},
			}},
		}},
	}
}

func writeDataset(t *testing.T, dataset recalltest.Dataset) string {
	t.Helper()
	data, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private-corpus.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runEval(t *testing.T, corpus string, flags ...string) (int, []byte, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := append([]string{"--corpus", corpus}, flags...)
	code := run(context.Background(), args, &stdout, &stderr)
	return code, stdout.Bytes(), stderr.String()
}

func decodeReport(t *testing.T, data []byte) recalltest.Report {
	t.Helper()
	var report recalltest.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("invalid aggregate JSON: %v", err)
	}
	return report
}

func assertPrivateMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("report permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func assertNoPrivateText(t *testing.T, output string, includeCaseID bool) {
	t.Helper()
	markers := []string{
		"PRIVATE_DATASET_NAME", "PRIVATE_RECORD_IDENTIFIER", "PRIVATE_PROJECT_IDENTIFIER",
		"PRIVATE_SESSION_IDENTIFIER", "privateLookupKeyword", "PRIVATE_EVIDENCE_ANCHOR",
	}
	if !includeCaseID {
		markers = append(markers, "PRIVATE_CASE_IDENTIFIER")
	}
	for _, marker := range markers {
		if strings.Contains(output, marker) {
			t.Fatalf("report leaked private marker %q", marker)
		}
	}
}

func TestRunAggregateReportExcludesPrivateLabels(t *testing.T) {
	corpus := writeDataset(t, privateDataset())
	output := filepath.Join(t.TempDir(), "aggregate.json")
	code, stdout, stderr := runEval(t, corpus, "--output", output)
	if code != 0 || stderr != "" {
		t.Fatalf("run = %d, stderr %q", code, stderr)
	}
	report := decodeReport(t, stdout)
	if report.Metrics.Answerable != 1 || report.Metrics.RecallAt5 != 1 || !report.SafetyPassed() {
		t.Fatalf("unexpected aggregate metrics: %+v", report.Metrics)
	}
	if report.DatasetSHA256 == "" {
		t.Fatal("aggregate omitted corpus digest")
	}
	assertNoPrivateText(t, string(stdout)+stderr, false)
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, stdout) {
		t.Fatal("aggregate file differs from stdout")
	}
	assertPrivateMode(t, output)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdout, &raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["cases"]; exists {
		t.Fatal("aggregate includes per-case results")
	}
}

func TestRunOptionalCaseReportIsPrivateAndContainsMetricsOnly(t *testing.T) {
	corpus := writeDataset(t, privateDataset())
	output := filepath.Join(t.TempDir(), "cases.json")
	code, stdout, stderr := runEval(t, corpus, "--case-report", output)
	if code != 0 || stderr != "" {
		t.Fatalf("run = %d, stderr %q", code, stderr)
	}
	assertNoPrivateText(t, string(stdout)+stderr, false)
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var cases []recalltest.CaseResult
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || cases[0].ID != "PRIVATE_CASE_IDENTIFIER" || cases[0].Recalled != 1 {
		t.Fatalf("unexpected case metrics: %+v", cases)
	}
	assertNoPrivateText(t, string(data), true)
	assertPrivateMode(t, output)
}

func TestRunNeverOverwritesReportsOrFollowsSymlinks(t *testing.T) {
	corpus := writeDataset(t, privateDataset())
	for _, flag := range []string{"--output", "--case-report"} {
		for _, kind := range []string{"existing", "symlink-existing", "symlink-missing"} {
			t.Run(strings.TrimPrefix(flag, "--")+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "report.json")
				target := path
				const sentinel = "existing private report must remain untouched\n"
				if kind != "existing" {
					target = filepath.Join(dir, "target.json")
				}
				if kind != "symlink-missing" {
					if err := os.WriteFile(target, []byte(sentinel), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if kind != "existing" {
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
				code, stdout, stderr := runEval(t, corpus, flag, path)
				if code != 1 || len(stdout) != 0 || stderr == "" {
					t.Fatalf("run = %d, stdout bytes %d, stderr %q", code, len(stdout), stderr)
				}
				assertNoPrivateText(t, stderr, false)
				if kind == "symlink-missing" {
					if _, err := os.Lstat(target); !os.IsNotExist(err) {
						t.Fatalf("dangling symlink target was created: %v", err)
					}
				} else {
					data, err := os.ReadFile(target)
					if err != nil || string(data) != sentinel {
						t.Fatalf("existing report changed: %v", err)
					}
				}
				if kind != "existing" {
					got, err := os.Readlink(path)
					if err != nil || got != target {
						t.Fatalf("report symlink changed: %v", err)
					}
				}
			})
		}
	}
}

func TestRunQualityMissRequiresAnExplicitRecallGate(t *testing.T) {
	dataset := privateDataset()
	dataset.Cases[0].Category = "paraphrase"
	dataset.Cases[0].Query.Text = "an intentionally unmatched paraphrase"
	corpus := writeDataset(t, dataset)
	for _, test := range []struct {
		name  string
		flags []string
		code  int
	}{
		{name: "default permits measured misses"},
		{name: "disabled", flags: []string{"--min-recall-at-5=-1"}},
		{name: "zero threshold", flags: []string{"--min-recall-at-5=0"}},
		{name: "requested threshold", flags: []string{"--min-recall-at-5=0.5"}, code: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runEval(t, corpus, test.flags...)
			if code != test.code {
				t.Fatalf("run = %d, want %d; stderr %q", code, test.code, stderr)
			}
			report := decodeReport(t, stdout)
			if report.Metrics.Answerable != 1 || report.Metrics.RecallAt5 != 0 || !report.SafetyPassed() {
				t.Fatalf("miss was not measured safely: %+v", report.Metrics)
			}
			if (stderr != "") != (test.code != 0) {
				t.Fatalf("unexpected gate diagnostic: %q", stderr)
			}
			assertNoPrivateText(t, string(stdout)+stderr, false)
		})
	}
}

func TestRunForbiddenEvidenceFailsEvenWithoutQualityGate(t *testing.T) {
	dataset := privateDataset()
	forbidden := dataset.Records[0]
	forbidden.ID = "PRIVATE_FORBIDDEN_IDENTIFIER"
	forbidden.EventAt = "2026-09-02T08:00:00Z"
	forbidden.Messages = []recalltest.Message{{
		Role:    "assistant",
		Content: "privateLookupKeyword: a different prohibited instruction remains visible in this source.",
	}}
	dataset.Records = append(dataset.Records, forbidden)
	dataset.Cases[0].ForbiddenIDs = []string{forbidden.ID}
	corpus := writeDataset(t, dataset)
	code, stdout, stderr := runEval(t, corpus)
	if code != 3 || stderr == "" {
		t.Fatalf("run = %d, stderr %q", code, stderr)
	}
	report := decodeReport(t, stdout)
	if report.Metrics.RecallAt5 != 1 || report.Metrics.ForbiddenHits != 1 || report.SafetyPassed() {
		t.Fatalf("forbidden evidence did not trigger safety gate: %+v", report.Metrics)
	}
	assertNoPrivateText(t, string(stdout)+stderr, false)
	if strings.Contains(string(stdout)+stderr, forbidden.ID) {
		t.Fatal("safety failure leaked the forbidden source identifier")
	}
}

func TestRunRejectsNonfiniteRecallThreshold(t *testing.T) {
	for _, value := range []string{"NaN", "Inf", "+Inf", "-Inf"} {
		t.Run(value, func(t *testing.T) {
			code, stdout, stderr := runEval(t, filepath.Join(t.TempDir(), "absent-corpus.json"), "--min-recall-at-5="+value)
			if code != 2 || len(stdout) != 0 || !strings.Contains(stderr, "usage:") {
				t.Fatalf("invalid threshold run = %d, stdout bytes %d, stderr %q", code, len(stdout), stderr)
			}
		})
	}
}

func TestRunModeSelectionAndAggregateReporting(t *testing.T) {
	for _, test := range []struct {
		name    string
		queries []string
		flags   []string
		want    string
	}{
		{name: "default literal", queries: []string{""}, want: "literal"},
		{name: "preserve diverse", queries: []string{"diverse"}, want: "diverse"},
		{name: "preserve mixed", queries: []string{"", "diverse"}, want: "mixed"},
		{name: "override literal", queries: []string{"diverse", ""}, flags: []string{"--mode=literal"}, want: "literal"},
		{name: "override diverse", queries: []string{"literal", ""}, flags: []string{"--mode=diverse"}, want: "diverse"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataset := privateDataset()
			item := dataset.Cases[0]
			dataset.Cases = nil
			for index, mode := range test.queries {
				copy := item
				copy.ID += strings.Repeat("-copy", index)
				copy.Query.Mode = mode
				dataset.Cases = append(dataset.Cases, copy)
			}
			corpus := writeDataset(t, dataset)
			before, err := os.ReadFile(corpus)
			if err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := runEval(t, corpus, test.flags...)
			if code != 0 || stderr != "" {
				t.Fatalf("run = %d, stderr %q", code, stderr)
			}
			report := decodeReport(t, stdout)
			if report.RetrievalMode != test.want || report.Metrics.RecallAt5 != 1 {
				t.Fatalf("mode = %q, want %q; metrics %+v", report.RetrievalMode, test.want, report.Metrics)
			}
			assertNoPrivateText(t, string(stdout), false)
			after, err := os.ReadFile(corpus)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("CLI mode override rewrote the dataset", err)
			}
		})
	}
}

func TestRunRejectsUnknownModeBeforeReadingCorpus(t *testing.T) {
	for _, value := range []string{"semantic", "mixed", "Literal", " diverse"} {
		t.Run(value, func(t *testing.T) {
			code, stdout, stderr := runEval(t, filepath.Join(t.TempDir(), "absent-corpus.json"), "--mode="+value)
			if code != 2 || len(stdout) != 0 || !strings.Contains(stderr, "usage:") {
				t.Fatalf("unknown mode run = %d, stdout bytes %d, stderr %q", code, len(stdout), stderr)
			}
		})
	}
}
