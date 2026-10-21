package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/recalltest"
)

func invoke(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func syntheticOwnerShape(questions int) recalltest.Dataset {
	dataset := recalltest.Dataset{
		Version: 1, Name: "PRIVATE_DATASET_NAME",
		Records: []recalltest.Record{{
			ID: "PRIVATE_SOURCE_ID", ProjectID: "PRIVATE_PROJECT", SessionID: "PRIVATE_SESSION", Client: "codex",
			EventAt: "2026-10-20T00:00:00Z", Messages: []recalltest.Message{{Role: "tool", Content: "PRIVATE_ANCHOR confirms the synthetic result."}},
		}},
	}
	for index := range questions {
		dataset.Cases = append(dataset.Cases, recalltest.Case{
			ID: fmt.Sprintf("PRIVATE_CASE_%d", index), Category: categories[index%len(categories)],
			Query:       memory.Query{Text: fmt.Sprintf("PRIVATE_QUERY_%d", index), ProjectID: "PRIVATE_PROJECT", SessionID: "PRIVATE_SESSION", Limit: 5},
			RelevantIDs: []string{"PRIVATE_SOURCE_ID"}, Evidence: []recalltest.Evidence{{RecordID: "PRIVATE_SOURCE_ID", Contains: []string{"PRIVATE_ANCHOR"}}},
		})
	}
	dataset.Cases[len(dataset.Cases)-1].RelevantIDs = nil
	dataset.Cases[len(dataset.Cases)-1].Evidence = nil
	dataset.Cases[len(dataset.Cases)-1].ExpectEmpty = true
	return dataset
}

func writeFixture(t *testing.T, dataset recalltest.Dataset) (string, []byte) {
	t.Helper()
	data, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "PRIVATE_FILENAME.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func TestInitCreatesPrivateDeliberatelyUnfinishedDraft(t *testing.T) {
	output := filepath.Join(t.TempDir(), "draft.json")
	code, stdout, stderr := invoke("init", "--output", output)
	if code != 0 || stderr != "" {
		t.Fatalf("init = %d: %q", code, stderr)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("draft is not private", err)
	}
	var dataset recalltest.Dataset
	if err := json.Unmarshal(data, &dataset); err != nil {
		t.Fatal(err)
	}
	if len(dataset.Cases) != 50 || len(dataset.Records) != 0 {
		t.Fatal("draft contains unexpected records or question count")
	}
	for _, item := range dataset.Cases {
		if item.Query.Text != "" || item.Query.ProjectID != "" || item.Category != "" || len(item.Evidence) != 0 || len(item.RelevantIDs) != 0 || item.ExpectEmpty {
			t.Fatal("draft invented a question or gold label")
		}
	}
	for _, category := range categories {
		if !strings.Contains(dataset.Description, category) {
			t.Fatal("draft omitted approved category vocabulary")
		}
	}
	if _, err := recalltest.Load(bytes.NewReader(data)); err == nil {
		t.Fatal("unfinished template passed gold-label validation")
	}
	var result summary
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || !result.Draft || result.StructuralValidationPassed || result.OwnerAuthorshipVerified || result.EvaluationPerformed || result.DatasetSHA256 != digest(data) {
		t.Fatal("draft summary implied completed owner labels", err)
	}
	code, stdout, _ = invoke("validate", "--corpus", output)
	if code == 0 || stdout != "" {
		t.Fatal("validate accepted unfinished draft")
	}
}

func TestInitRefusesExistingFilesAndSymlinks(t *testing.T) {
	for _, kind := range []string{"existing", "symlink-existing", "symlink-missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path, target := filepath.Join(dir, "draft.json"), filepath.Join(dir, "target.json")
			if kind == "existing" {
				target = path
			}
			if kind != "symlink-missing" {
				if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "existing" {
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			code, stdout, stderr := invoke("init", "--output", path)
			if code != 1 || stdout != "" || strings.Contains(stderr, path) {
				t.Fatal("unsafe or disclosing overwrite result")
			}
			if kind == "symlink-missing" {
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatal("dangling symlink target was created")
				}
			} else {
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "preserve" {
					t.Fatal("existing private file overwritten", err)
				}
			}
			if kind != "existing" {
				if got, err := os.Readlink(path); err != nil || got != target {
					t.Fatal("symlink was replaced", err)
				}
			}
		})
	}
}

func TestValidationReturnsOnlyCountsAndExactHashWithoutChangingInput(t *testing.T) {
	path, before := writeFixture(t, syntheticOwnerShape(50))
	code, stdout, stderr := invoke("validate", "--corpus", path)
	if code != 0 || stderr != "" {
		t.Fatalf("validate = %d: %q", code, stderr)
	}
	if strings.Contains(stdout+stderr, "PRIVATE_") || strings.Contains(stdout+stderr, path) {
		t.Fatal("validation leaked a private name, query, scope, source, anchor or path")
	}
	var result summary
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.DatasetSHA256 != digest(before) || result.QuestionCases != 50 || result.UniqueScopedQuestions != 50 || result.Answerable != 49 || result.Unanswerable != 1 || len(result.Categories) != 9 || len(result.MissingCategories) != 0 || !result.StructuralValidationPassed || result.OwnerAuthorshipVerified || result.EvaluationPerformed {
		t.Fatalf("incorrect structural result: %+v", result)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("validation rewrote the private corpus", err)
	}
}

func TestValidationRequiresMinimumCountScopeDistinctQuestionsAndGoldEvidence(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*recalltest.Dataset)
	}{
		{"too few", func(d *recalltest.Dataset) { d.Cases = d.Cases[:49] }},
		{"scope omitted", func(d *recalltest.Dataset) { d.Cases[0].Query.ProjectID = "" }},
		{"duplicate wording and mode cannot pad", func(d *recalltest.Dataset) {
			d.Cases[1].Query.Text = " \tprivate_query_0  \n"
			d.Cases[1].Query.Mode = "hybrid"
		}},
		{"unknown retrieval mode", func(d *recalltest.Dataset) { d.Cases[0].Query.Mode = "PRIVATE_UNKNOWN_MODE" }},
		{"unbound evidence", func(d *recalltest.Dataset) { d.Cases[0].Evidence[0].Contains = []string{"PRIVATE_NONEXISTENT_ANCHOR"} }},
		{"oversized query", func(d *recalltest.Dataset) { d.Cases[0].Query.Text = strings.Repeat("界", 257) }},
		{"backward fractional range", func(d *recalltest.Dataset) {
			d.Cases[0].Query.After = "2026-10-20T00:00:00.1Z"
			d.Cases[0].Query.Before = "2026-10-20T00:00:00Z"
		}},
		{"duplicate equivalent date scope", func(d *recalltest.Dataset) {
			d.Cases[1].Query.Text = d.Cases[0].Query.Text
			d.Cases[0].Query.After = "2026-10-20T00:00:00Z"
			d.Cases[1].Query.After = "2026-10-20T01:00:00+01:00"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataset := syntheticOwnerShape(50)
			test.edit(&dataset)
			path, before := writeFixture(t, dataset)
			code, stdout, stderr := invoke("validate", "--corpus", path)
			if code != 1 || stdout != "" || stderr == "" || strings.Contains(stdout+stderr, "PRIVATE_") {
				t.Fatalf("invalid corpus accepted or leaked: code %d", code)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid corpus was changed", err)
			}
		})
	}
}

func TestValidationAcceptsHybridWithoutCallingAProvider(t *testing.T) {
	dataset := syntheticOwnerShape(50)
	dataset.Cases[0].Query.Mode = "hybrid"
	path, before := writeFixture(t, dataset)
	code, stdout, stderr := invoke("validate", "--corpus", path)
	if code != 0 || stderr != "" || strings.Contains(stdout, "PRIVATE_") {
		t.Fatal("structural validation rejected hybrid or leaked labels")
	}
	var result summary
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || !result.StructuralValidationPassed || result.EvaluationPerformed || result.OwnerAuthorshipVerified {
		t.Fatal("hybrid validation implied model evaluation or owner approval", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("hybrid validation changed the corpus", err)
	}
}

func TestValidationReportsCoverageWithoutInventingMissingCategories(t *testing.T) {
	dataset := syntheticOwnerShape(50)
	for index := range dataset.Cases {
		dataset.Cases[index].Category = "exact_error"
	}
	path, _ := writeFixture(t, dataset)
	code, stdout, stderr := invoke("validate", "--corpus", path)
	if code != 0 || stderr != "" {
		t.Fatal("structurally valid corpus rejected")
	}
	var result summary
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || len(result.MissingCategories) != 8 || result.Categories["exact_error"] != 50 || result.OwnerAuthorshipVerified {
		t.Fatal("coverage was concealed or owner quality was certified", err)
	}
}

func TestUsageBoundsDoNotCreateFiles(t *testing.T) {
	unused := filepath.Join(t.TempDir(), "never-created.json")
	for _, args := range [][]string{nil, {"unknown"}, {"init"}, {"validate"}, {"init", "--output", unused, "--questions", "49"}, {"init", "--output", unused, "--questions", "1001"}} {
		code, stdout, stderr := invoke(args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "usage:") {
			t.Fatal("invalid usage accepted")
		}
	}
	if _, err := os.Stat(unused); !os.IsNotExist(err) {
		t.Fatal("invalid arguments created an output file")
	}
}
