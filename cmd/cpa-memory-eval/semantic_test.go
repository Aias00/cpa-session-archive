package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/recalltest"
)

func emptyGateDataset(queries ...string) recalltest.Dataset {
	dataset := privateDataset()
	dataset.Records[0].Messages = append(dataset.Records[0].Messages, recalltest.Message{
		Role: "assistant", Content: "PRIVATE_MISSING_TOPIC remains uninvestigated; no value or result was established.",
	})
	for index, text := range queries {
		dataset.Cases = append(dataset.Cases, recalltest.Case{
			ID: "PRIVATE_EMPTY_CASE" + strings.Repeat("-copy", index), Category: "missing",
			Query:       memory.Query{Text: text, ProjectID: "PRIVATE_PROJECT_IDENTIFIER", SessionID: "PRIVATE_SESSION_IDENTIFIER", Limit: 5},
			ExpectEmpty: true,
		})
	}
	return dataset
}

func TestRequireEmptyPassIsAnExplicitQualityGateAndPreservesAggregate(t *testing.T) {
	for _, test := range []struct {
		name      string
		queries   []string
		flags     []string
		code      int
		emptyPass int
	}{
		{name: "default records false positive", queries: []string{"PRIVATE_MISSING_TOPIC"}},
		{name: "explicit false preserves baseline", queries: []string{"PRIVATE_MISSING_TOPIC"}, flags: []string{"--require-empty-pass=false"}},
		{name: "required false positive fails", queries: []string{"PRIVATE_MISSING_TOPIC"}, flags: []string{"--require-empty-pass"}, code: 3},
		{name: "all negatives must pass", queries: []string{"no-evidence-for-this-query", "PRIVATE_MISSING_TOPIC"}, flags: []string{"--require-empty-pass"}, code: 3, emptyPass: 1},
		{name: "empty result passes", queries: []string{"no-evidence-for-this-query"}, flags: []string{"--require-empty-pass"}, emptyPass: 1},
		{name: "no negatives do not fail", flags: []string{"--require-empty-pass"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			corpus := writeDataset(t, emptyGateDataset(test.queries...))
			before, err := os.ReadFile(corpus)
			if err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "aggregate.json")
			flags := append([]string{"--output", output}, test.flags...)
			code, stdout, stderr := runEval(t, corpus, flags...)
			if code != test.code {
				t.Fatalf("run = %d, want %d; stderr %q", code, test.code, stderr)
			}
			report := decodeReport(t, stdout)
			if report.Metrics.Unanswerable != len(test.queries) || report.Metrics.EmptyPassCases != test.emptyPass || report.Metrics.RecallAt5 != 1 || !report.SafetyPassed() {
				t.Fatalf("quality/safety boundaries changed: %+v", report.Metrics)
			}
			if test.code == 3 && !strings.Contains(stderr, "empty-result gate") || test.code == 0 && stderr != "" {
				t.Fatalf("incorrect quality diagnostic: %q", stderr)
			}
			assertNoPrivateText(t, string(stdout)+stderr, false)
			if strings.Contains(string(stdout)+stderr, "PRIVATE_") {
				t.Fatal("empty-result gate leaked case or query data")
			}
			data, err := os.ReadFile(output)
			if err != nil || !bytes.Equal(data, stdout) {
				t.Fatal("quality gate discarded or changed the aggregate report", err)
			}
			assertPrivateMode(t, output)
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(stdout, &raw); err != nil {
				t.Fatal(err)
			}
			if _, exists := raw["cases"]; exists {
				t.Fatal("quality gate exposed per-case labels in aggregate")
			}
			after, err := os.ReadFile(corpus)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("quality gate changed frozen questions or evidence", err)
			}
		})
	}
}
