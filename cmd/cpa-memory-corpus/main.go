// cpa-memory-corpus prepares and checks owner-controlled retrieval labels.
// It never connects to an archive, calls a model, or invents gold evidence.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/recalltest"
)

const minimumQuestions = 50

var categories = []string{"exact_error", "path", "chinese", "paraphrase", "old_decision", "correction", "branch", "missing", "repetition"}

type summary struct {
	Version                    int            `json:"version"`
	Action                     string         `json:"action"`
	DatasetSHA256              string         `json:"dataset_sha256"`
	QuestionSlots              int            `json:"question_slots,omitempty"`
	QuestionCases              int            `json:"question_cases,omitempty"`
	UniqueScopedQuestions      int            `json:"unique_scoped_questions,omitempty"`
	Records                    int            `json:"records,omitempty"`
	Answerable                 int            `json:"answerable,omitempty"`
	Unanswerable               int            `json:"unanswerable,omitempty"`
	Categories                 map[string]int `json:"categories,omitempty"`
	MissingCategories          []string       `json:"missing_categories,omitempty"`
	Draft                      bool           `json:"draft"`
	StructuralValidationPassed bool           `json:"structural_validation_passed"`
	OwnerAuthorshipVerified    bool           `json:"owner_authorship_verified"`
	EvaluationPerformed        bool           `json:"evaluation_performed"`
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func draftDataset(questions int) recalltest.Dataset {
	dataset := recalltest.Dataset{
		Version: 1,
		Name:    "unfinished-owner-corpus",
		Description: "Unfinished private labeling draft; no questions or evidence are invented. " +
			"Allowed categories: " + strings.Join(categories, ", ") + ". " +
			"Supply real owner questions and supporting records/anchors before validation.",
		Records: []recalltest.Record{},
		Cases:   make([]recalltest.Case, 0, questions),
	}
	for index := range questions {
		dataset.Cases = append(dataset.Cases, recalltest.Case{
			ID:    fmt.Sprintf("owner-question-%03d", index+1),
			Query: memory.Query{Limit: 5},
			Note: "Unfinished: choose an allowed category, supply a real scoped question, " +
				"then label supporting records and exact evidence anchors, or explicitly expect no evidence.",
		})
	}
	return dataset
}

func createPrivateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

type questionKey struct {
	Text, Project, Client, Session, After, Before string
}

func normalizedTime(value string) (string, time.Time, error) {
	if value == "" {
		return "", time.Time{}, nil
	}
	stamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", time.Time{}, err
	}
	return stamp.UTC().Format(time.RFC3339Nano), stamp, nil
}

func validateOwnerQuestions(dataset recalltest.Dataset) (summary, error) {
	result := summary{Version: 1, Action: "validate", QuestionCases: len(dataset.Cases), Records: len(dataset.Records), Categories: map[string]int{}}
	if len(dataset.Cases) < minimumQuestions {
		return result, fmt.Errorf("owner corpus requires at least %d distinct scoped questions", minimumQuestions)
	}
	for _, category := range categories {
		result.Categories[category] = 0
	}
	seen := map[questionKey]bool{}
	for index, item := range dataset.Cases {
		if item.Query.Mode != "" && item.Query.Mode != "literal" && item.Query.Mode != "diverse" && item.Query.Mode != "hybrid" {
			return result, fmt.Errorf("case at position %d has an unsupported retrieval mode", index)
		}
		if strings.TrimSpace(item.Query.ProjectID) == "" {
			return result, fmt.Errorf("case at position %d requires an explicit project scope", index)
		}
		if !utf8.ValidString(item.Query.Text) || utf8.RuneCountInString(item.Query.Text) > 256 || len(strings.Fields(item.Query.Text)) > 16 {
			return result, fmt.Errorf("case at position %d exceeds the current query text limits", index)
		}
		after, afterStamp, afterErr := normalizedTime(item.Query.After)
		before, beforeStamp, beforeErr := normalizedTime(item.Query.Before)
		if afterErr != nil || beforeErr != nil || after != "" && before != "" && !afterStamp.Before(beforeStamp) {
			return result, fmt.Errorf("case at position %d requires a valid increasing time range", index)
		}
		key := questionKey{
			Text:    strings.Join(strings.Fields(strings.ToLower(item.Query.Text)), " "),
			Project: item.Query.ProjectID, Client: item.Query.Client, Session: item.Query.SessionID,
			After: after, Before: before,
		}
		if seen[key] {
			return result, fmt.Errorf("case at position %d duplicates an earlier scoped question", index)
		}
		seen[key] = true
		result.Categories[item.Category]++
		if item.ExpectEmpty {
			result.Unanswerable++
		} else {
			result.Answerable++
		}
	}
	for _, category := range categories {
		if result.Categories[category] == 0 {
			result.MissingCategories = append(result.MissingCategories, category)
		}
	}
	result.UniqueScopedQuestions = len(seen)
	result.StructuralValidationPassed = true
	return result, nil
}

func usage(stderr io.Writer) int {
	fmt.Fprintln(stderr, "usage: cpa-memory-corpus init --output new-private-draft.json [--questions 50]")
	fmt.Fprintln(stderr, "       cpa-memory-corpus validate --corpus private-completed-corpus.json")
	return 2
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usage(stderr)
	}
	var result summary
	switch args[0] {
	case "init":
		flags := flag.NewFlagSet("init", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		output := flags.String("output", "", "new private draft path outside the repository")
		questions := flags.Int("questions", minimumQuestions, "unfinished question slots (50 to 1000)")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *output == "" || *questions < minimumQuestions || *questions > 1000 {
			return usage(stderr)
		}
		data, err := json.MarshalIndent(draftDataset(*questions), "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, "private draft could not be prepared")
			return 1
		}
		data = append(data, '\n')
		if err := createPrivateFile(*output, data); err != nil {
			fmt.Fprintln(stderr, "private draft could not be created; choose a new file in an existing private directory")
			return 1
		}
		result = summary{Version: 1, Action: "init", DatasetSHA256: digest(data), QuestionSlots: *questions, Draft: true}
	case "validate":
		flags := flag.NewFlagSet("validate", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		corpus := flags.String("corpus", "", "owner-completed private dataset")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *corpus == "" {
			return usage(stderr)
		}
		file, err := os.Open(*corpus)
		if err != nil {
			fmt.Fprintln(stderr, "private dataset unavailable")
			return 1
		}
		data, err := io.ReadAll(io.LimitReader(file, recalltest.MaxDatasetBytes+1))
		_ = file.Close()
		if err != nil || len(data) > recalltest.MaxDatasetBytes {
			fmt.Fprintln(stderr, "private dataset unreadable or oversized")
			return 1
		}
		dataset, err := recalltest.Load(bytes.NewReader(data))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		result, err = validateOwnerQuestions(dataset)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		result.DatasetSHA256 = digest(data)
	default:
		return usage(stderr)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(result) != nil {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
