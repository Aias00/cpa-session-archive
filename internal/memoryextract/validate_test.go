package memoryextract

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"cpa-session-archive/internal/memory"
)

type excerptSource struct {
	in         Input
	wrongScope bool
}

func (s excerptSource) VerifyInput(_ context.Context, in Input) error {
	if in.ProjectID != s.in.ProjectID || in.SessionID != s.in.SessionID || !reflect.DeepEqual(in.Excerpts, s.in.Excerpts) {
		return memory.ErrSourceUnavailable
	}
	return nil
}

func (s excerptSource) ReadSource(_ context.Context, c memory.Citation) (memory.Evidence, error) {
	for _, e := range s.in.Excerpts {
		if c.RequestID == e.Citation.RequestID && c.Revision == e.Citation.Revision && c.Side == e.Citation.Side && c.Pointer == e.Citation.Pointer && c.Start >= e.Citation.Start && c.End <= e.Citation.End {
			project := s.in.ProjectID
			if s.wrongScope {
				project = "other-project"
			}
			return memory.Evidence{Citation: c, ProjectID: project, SessionID: s.in.SessionID, Kind: e.Kind, Text: string([]rune(e.Text)[c.Start-e.Citation.Start : c.End-e.Citation.Start])}, nil
		}
	}
	return memory.Evidence{}, memory.ErrSourceUnavailable
}

func syntheticInput() Input {
	return Input{Version: Version, ArchiveID: strings.Repeat("a", 64), ProjectID: "synthetic-project", SessionID: "synthetic-session", RequestID: "synthetic-request", Revision: strings.Repeat("b", 64), Sequence: 1, KnowledgeComplete: true, Excerpts: []Excerpt{}}
}

func addExcerpt(in *Input, role, kind, text string) Excerpt {
	id := fmt.Sprintf("excerpt-%d", len(in.Excerpts))
	e := Excerpt{ID: id, Role: role, Kind: kind, Text: text, Citation: memory.Citation{NormalizerVersion: memory.NormalizerVersion, RequestID: in.RequestID, Revision: in.Revision, Side: "original_request", Pointer: "/" + id, Start: 0, End: utf8.RuneCountInString(text)}}
	in.Excerpts = append(in.Excerpts, e)
	return e
}

// Twenty public labels exercise mechanical attribution/grounding. They do not
// claim to measure any particular language model's semantic extraction quality.
func TestTwentyLabeledAttributionsAndExactUnicodeCitations(t *testing.T) {
	in := syntheticInput()
	response := Response{Candidates: []DraftCandidate{}}
	labels := []struct{ attribution, role, kind, text string }{
		{"user_requirement", "user", "text", "请只使用项目本地缓存，不要上传会话。"},
		{"user_report", "user", "text", "我在测试环境观察到队列重启后恢复。"},
		{"assistant_suggestion", "assistant", "text", "建议先增加有上限的失败重试机制。"},
		{"assistant_report", "assistant", "text", "我认为当前构建已经完成，仍需核对工具输出。"},
		{"execution_result", "tool", "tool_result", "fixture: unit suite failed; exit code 1"},
	}
	for _, label := range labels {
		for n := 0; n < 4; n++ {
			text := fmt.Sprintf("前缀%d：%s", n, label.text)
			e := addExcerpt(&in, label.role, label.kind, text)
			response.Candidates = append(response.Candidates, DraftCandidate{Kind: "fact", Title: fmt.Sprintf("Public fixture %s %d", label.attribution, n), Statement: fmt.Sprintf("Synthetic claim %d: %s", n, label.text), Attribution: label.attribution, Supports: []Support{{ExcerptID: e.ID, Quote: label.text}}, Action: "new"})
		}
	}
	report, err := BuildReport(context.Background(), in, response, excerptSource{in: in})
	if err != nil || len(report.Candidates) != 20 || len(report.Rejected) != 0 {
		t.Fatal("labeled attribution corpus failed", err, len(report.Candidates), report.Rejected)
	}
	for i, item := range report.Candidates {
		expected := response.Candidates[i]
		if !item.RequiresHumanReview || item.Attribution != expected.Attribution || item.Proposal.Global || item.Proposal.ProjectID != in.ProjectID || len(item.Evidence) != 1 || len(item.Proposal.Sources) != 1 {
			t.Fatal("candidate lost scope, review requirement or attribution")
		}
		actual, err := (excerptSource{in: in}).ReadSource(context.Background(), item.Proposal.Sources[0])
		if err != nil || actual.Text != expected.Supports[0].Quote || item.Evidence[0].Quote != actual.Text || item.Evidence[0].Citation.Start != 4 {
			t.Fatal("exact Unicode quote did not roundtrip", err)
		}
		if !strings.HasPrefix(item.Proposal.Statement, attributionLabels[expected.Attribution]+": ") {
			t.Fatal("Git proposal lost source attribution")
		}
	}
}

func TestUnsupportedPromotionsQuotesAndTargetsRemainRejected(t *testing.T) {
	in := syntheticInput()
	e := addExcerpt(&in, "assistant", "text", "I suggest using a durable retry queue, but have not run any checks.")
	in.Existing = []memory.MemoryEntry{{ID: "accepted-policy", Revision: strings.Repeat("c", 64), ProjectID: in.ProjectID, Status: "verified", Statement: "A separate prior policy exists."}}
	valid := DraftCandidate{Kind: "experience", Title: "Durable retry suggestion", Statement: e.Text, Attribution: "assistant_suggestion", Supports: []Support{{ExcerptID: e.ID, Quote: e.Text}}, Action: "new"}
	for _, name := range []string{"assistant_as_execution", "assistant_as_requirement", "invented_quote", "foreign_target", "stale_target", "retired_target", "unknown_action", "secret_kind"} {
		t.Run(name, func(t *testing.T) {
			raw := valid
			raw.Supports = append([]Support(nil), valid.Supports...)
			input := in
			input.Existing = append([]memory.MemoryEntry(nil), in.Existing...)
			switch name {
			case "assistant_as_execution":
				raw.Attribution = "execution_result"
			case "assistant_as_requirement":
				raw.Attribution = "user_requirement"
			case "invented_quote":
				raw.Supports[0].Quote = "All checks passed and production was deployed."
			case "foreign_target":
				raw.Action = "corrects"
				raw.Targets = []Target{{ID: "other-project-claim", Revision: strings.Repeat("c", 64)}}
			case "stale_target":
				raw.Action = "supersedes"
				raw.Targets = []Target{{ID: "accepted-policy", Revision: strings.Repeat("d", 64)}}
			case "retired_target":
				raw.Action = "corrects"
				raw.Targets = []Target{{ID: "accepted-policy", Revision: strings.Repeat("c", 64)}}
				input.Existing[0].Status = "superseded"
			case "unknown_action":
				raw.Action = "approve"
			case "secret_kind":
				raw.Kind = "preference"
			}
			report, err := BuildReport(context.Background(), input, Response{Candidates: []DraftCandidate{raw}}, excerptSource{in: input})
			if err != nil || len(report.Candidates) != 0 || len(report.Rejected) != 1 {
				t.Fatal("unsupported output escaped", name, err)
			}
		})
	}
	if _, err := BuildReport(context.Background(), in, Response{Candidates: []DraftCandidate{valid}}, excerptSource{in: in, wrongScope: true}); err == nil {
		t.Fatal("out-of-scope source accepted")
	}
}

func TestRelationshipsAreRevisionBoundSuggestionsAndDuplicatesStayInactive(t *testing.T) {
	for _, action := range []string{"new", "corrects", "supersedes", "conflicts_with", "duplicate"} {
		t.Run(action, func(t *testing.T) {
			in := syntheticInput()
			e := addExcerpt(&in, "user", "text", "The synthetic project should now use a bounded durable queue.")
			target := memory.MemoryEntry{ID: "previous-policy", ProjectID: in.ProjectID, Revision: strings.Repeat("c", 64), Status: "verified", Statement: "A different earlier policy."}
			in.Existing = []memory.MemoryEntry{target}
			raw := DraftCandidate{Kind: "decision", Title: "Bound the durable queue", Statement: e.Text, Attribution: "user_requirement", Supports: []Support{{ExcerptID: e.ID, Quote: e.Text}}, Action: action}
			if action != "new" {
				raw.Targets = []Target{{ID: target.ID, Revision: target.Revision}}
			}
			report, err := BuildReport(context.Background(), in, Response{Candidates: []DraftCandidate{raw}}, excerptSource{in: in})
			if err != nil || len(report.Candidates) != 1 {
				t.Fatal(err, report.Rejected)
			}
			item := report.Candidates[0]
			if item.Action != action || !item.RequiresHumanReview {
				t.Fatal("relationship stopped being a suggestion")
			}
			if action != "new" && action != "duplicate" && (len(item.Proposal.Relations) != 1 || item.Proposal.Relations[0].Kind != action || item.Proposal.Relations[0].TargetRevision != target.Revision) {
				t.Fatal("relationship revision binding lost")
			}
			if action == "duplicate" && (len(item.Proposal.Relations) != 0 || len(item.DuplicateOf) != 1) {
				t.Fatal("duplicate suggestion became a lifecycle change")
			}
		})
	}
	in := syntheticInput()
	e := addExcerpt(&in, "user", "text", "The synthetic decision is already recorded in this project.")
	in.Existing = []memory.MemoryEntry{{ID: "same-statement", Revision: strings.Repeat("c", 64), ProjectID: in.ProjectID, Status: "verified", Statement: e.Text}}
	raw := DraftCandidate{Kind: "decision", Title: "Already retained decision", Statement: e.Text, Attribution: "user_requirement", Supports: []Support{{ExcerptID: e.ID, Quote: e.Text}}, Action: "new"}
	report, err := BuildReport(context.Background(), in, Response{Candidates: []DraftCandidate{raw, raw}}, excerptSource{in: in})
	if err != nil || len(report.Candidates) != 1 || report.Candidates[0].Action != "duplicate" || len(report.Rejected) != 1 {
		t.Fatal("exact duplicates were not suppressed", err)
	}
	key := Key(in, "provider-a")
	changed := in
	changed.SessionID = "other-session"
	if key == Key(changed, "provider-a") || key == Key(in, "provider-b") {
		t.Fatal("job key crossed scope/provider")
	}
}
