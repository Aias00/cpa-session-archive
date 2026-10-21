package recalltest

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// These tests inspect labels only. They must not run retrieval on the held-out
// split before a calibration choice has been fixed outside the test suite.
func TestSyntheticSemanticSplitsAreFrozenAndBalanced(t *testing.T) {
	for _, spec := range []struct {
		split, hash    string
		records, cases int
	}{
		{"calibration", "e203f926d0617b6b6366cae2cf3a98d8accb3d7ec30737e463ad1105421ca4b3", 30, 48},
		{"held-out", "e03a0b32ec87b0ac4ce62e1354c78cc7593adfbfdf07b6714399b44e6eda5237", 20, 32},
	} {
		t.Run(spec.split, func(t *testing.T) {
			dataset := loadSyntheticSplit(t, spec.split)
			if dataset.fileDigest != spec.hash || len(dataset.Records) != spec.records || len(dataset.Cases) != spec.cases || len(dataset.Changes) != 0 {
				t.Fatal("synthetic split identity changed; version data before further calibration")
			}
			if !strings.Contains(dataset.Description, "Model-authored synthetic") || !strings.Contains(dataset.Description, "developer previously observed baseline-v1") {
				t.Fatal("synthetic provenance or prior baseline observation is undisclosed")
			}
			records := map[string]Record{}
			for _, record := range dataset.Records {
				records[record.ID] = record
			}
			positive, negative, toolEvidence := 0, 0, 0
			languages, features := map[string]int{}, map[string]int{}
			queries := map[string]bool{}
			for _, item := range dataset.Cases {
				labels := splitLabels(item.Note)
				if labels["split"] != spec.split || labels["language"] == "" || labels["feature"] == "" {
					t.Fatal("case lost its declared calibration slice")
				}
				languages[labels["language"]]++
				features[labels["feature"]]++
				if item.Query.ProjectID == "" || item.Query.SessionID == "" || item.Query.Client == "" || item.Query.Limit != 5 || item.Query.Mode != "" {
					t.Fatal("case lost explicit scope, fixed top five, or mode-neutral labels")
				}
				if utf8.RuneCountInString(item.Query.Text) > 256 || len(strings.Fields(item.Query.Text)) > 16 {
					t.Fatal("synthetic query exceeds the runtime query budget")
				}
				key := normalizedFixtureText(item.Query.Text)
				if queries[key] {
					t.Fatal("duplicate wording cannot pad semantic split coverage")
				}
				queries[key] = true
				if item.ExpectEmpty {
					negative++
					if labels["polarity"] != "negative" || item.Category != "missing" {
						t.Fatal("negative label metadata disagrees with the evidence contract")
					}
					if strings.HasPrefix(labels["feature"], "scope-") && len(item.ForbiddenIDs) != 1 {
						t.Fatal("scope-negative case lost its out-of-scope true-answer source")
					}
					for _, id := range item.ForbiddenIDs {
						r := records[id]
						if item.Query.ProjectID == r.ProjectID && item.Query.SessionID == r.SessionID && item.Query.Client == fixtureClient(r) {
							t.Fatal("related in-scope non-answer was incorrectly made a forbidden source")
						}
					}
				} else {
					positive++
					if labels["polarity"] != "positive" || len(item.Evidence) != 1 {
						t.Fatal("positive label metadata disagrees with the evidence contract")
					}
					label := item.Evidence[0]
					if label.Side != "original_request" || label.Pointer != "/messages/1/content" {
						t.Fatal("positive evidence no longer distinguishes the answer from its user prompt")
					}
					if records[label.RecordID].Messages[1].Role == "tool" {
						toolEvidence++
					}
				}
			}
			if positive != spec.cases/2 || negative != spec.cases/2 || languages["en"] < 16 || languages["zh"] < 10 || languages["mixed"] < 6 || toolEvidence < 6 {
				t.Fatal("semantic split lost balanced labels, bilingual questions, mixed code, or tool evidence")
			}
			if features["weak-association"] < 12 || features["scope-project"] < 1 || features["scope-session"] < 1 || features["scope-client"] < 1 {
				t.Fatal("semantic split lost weak-association or scope boundary negatives")
			}
		})
	}
}

func TestSyntheticSemanticSplitsDoNotReuseBaselineOrEachOther(t *testing.T) {
	texts, queries, projects, sessions := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	for splitIndex, filename := range []string{"baseline-v1.json", "semantic-calibration-v1.json", "semantic-held-out-v1.json"} {
		raw, err := os.ReadFile("testdata/" + filename)
		if err != nil {
			t.Fatal(err)
		}
		dataset, err := Load(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		claim := func(seen map[string]int, value string) {
			t.Helper()
			key := normalizedFixtureText(value)
			if previous, exists := seen[key]; exists && previous != splitIndex {
				t.Fatal("synthetic split reused another corpus's query, message, project, or session")
			}
			seen[key] = splitIndex
		}
		for _, record := range dataset.Records {
			claim(projects, record.ProjectID)
			claim(sessions, record.SessionID)
			for _, message := range record.Messages {
				claim(texts, message.Content)
			}
		}
		for _, item := range dataset.Cases {
			claim(queries, item.Query.Text)
		}
	}
}

func loadSyntheticSplit(t *testing.T, split string) Dataset {
	t.Helper()
	raw, err := os.ReadFile("testdata/semantic-" + split + "-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return dataset
}

func splitLabels(note string) map[string]string {
	labels := map[string]string{}
	for _, part := range strings.Split(note, ";") {
		if key, value, found := strings.Cut(strings.TrimSpace(part), "="); found {
			labels[key] = value
		}
	}
	return labels
}

func normalizedFixtureText(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}
