package memoryextract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"cpa-session-archive/internal/memory"
)

func digest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Key includes source identity/scope, exact evidence, canonical comparison
// snapshot, and provider configuration. Cached work never crosses those bounds.
func Key(in Input, provider string) string {
	return digest(struct {
		Version  string
		Provider string
		Input    Input
	}{Version, provider, in})
}

func validateShape(in Input) error {
	if in.Version != Version || in.ArchiveID == "" || in.ProjectID == "" || in.SessionID == "" || in.RequestID == "" || len(in.Revision) != 64 || in.Sequence < 0 || in.Window < 0 || len(in.Excerpts) == 0 || len(in.Excerpts) > 32 || len(in.Existing) > 100 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, e := range in.Excerpts {
		c := e.Citation
		if e.ID == "" || seen[e.ID] || !utf8.ValidString(e.Text) || strings.TrimSpace(e.Text) == "" || c.NormalizerVersion != memory.NormalizerVersion || c.RequestID != in.RequestID || c.Revision != in.Revision || c.Start < 0 || c.End <= c.Start || c.End-c.Start > 1600 || c.End-c.Start != utf8.RuneCountInString(e.Text) {
			return ErrInvalid
		}
		if !((e.Role == "user" || e.Role == "assistant") && e.Kind == "text" || e.Role == "tool" && e.Kind == "tool_result") {
			return ErrInvalid
		}
		seen[e.ID] = true
	}
	return nil
}

// ValidateEvidence rechecks every exact excerpt against the retained authority.
// SourceReader.VerifyInput additionally establishes original speaker metadata.
func ValidateEvidence(ctx context.Context, in Input, source memory.SourceReader) error {
	if source == nil || validateShape(in) != nil {
		return ErrInvalid
	}
	for _, e := range in.Excerpts {
		actual, err := source.ReadSource(ctx, e.Citation)
		if err != nil {
			return err
		}
		if actual.Citation != e.Citation || actual.ProjectID != in.ProjectID || actual.SessionID != in.SessionID || actual.Text != e.Text || actual.Kind != e.Kind {
			return fmt.Errorf("extraction evidence changed or is out of scope")
		}
	}
	return ctx.Err()
}

func normalizedStatement(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

var attributionLabels = map[string]string{
	"user_requirement":     "User requirement",
	"user_report":          "User report",
	"assistant_suggestion": "Assistant suggestion",
	"assistant_report":     "Assistant report",
	"execution_result":     "Tool observation",
}

func attributedStatement(attribution, statement string) string {
	return attributionLabels[attribution] + ": " + strings.TrimSpace(statement)
}

func roleSupports(attribution string, e Excerpt) bool {
	switch attribution {
	case "user_requirement", "user_report":
		return e.Role == "user" && e.Kind == "text"
	case "assistant_suggestion", "assistant_report":
		return e.Role == "assistant" && e.Kind == "text"
	case "execution_result":
		return e.Role == "tool" && e.Kind == "tool_result"
	}
	return false
}

func candidate(ctx context.Context, in Input, raw DraftCandidate, source memory.SourceReader) (Candidate, string) {
	var out Candidate
	if raw.Kind != "decision" && raw.Kind != "fact" && raw.Kind != "experience" {
		return out, "invalid_kind"
	}
	if utf8.RuneCountInString(raw.Title) < 3 || utf8.RuneCountInString(raw.Title) > 160 || strings.ContainsAny(raw.Title, "\r\n") || !utf8.ValidString(raw.Title) || utf8.RuneCountInString(raw.Statement) < 10 || utf8.RuneCountInString(raw.Statement) > 3500 || !utf8.ValidString(raw.Statement) || attributionLabels[raw.Attribution] == "" || len(raw.Supports) == 0 || len(raw.Supports) > 8 || len(raw.Targets) > 8 {
		return out, "invalid_candidate_shape"
	}
	excerpts := map[string]Excerpt{}
	for _, e := range in.Excerpts {
		excerpts[e.ID] = e
	}
	evidence := []Evidence{}
	citations := []memory.Citation{}
	seen := map[memory.Citation]bool{}
	warnings := []string{}
	for _, support := range raw.Supports {
		e, ok := excerpts[support.ExcerptID]
		if !ok || !roleSupports(raw.Attribution, e) {
			return out, "unsupported_attribution"
		}
		if strings.TrimSpace(support.Quote) == "" || !utf8.ValidString(support.Quote) {
			return out, "unsupported_quote"
		}
		start := strings.Index(e.Text, support.Quote)
		if start < 0 {
			return out, "unsupported_quote"
		}
		c := e.Citation
		c.Start += utf8.RuneCountInString(e.Text[:start])
		c.End = c.Start + utf8.RuneCountInString(support.Quote)
		actual, err := source.ReadSource(ctx, c)
		if err != nil || actual.Citation != c || actual.ProjectID != in.ProjectID || actual.SessionID != in.SessionID || actual.Text != support.Quote || actual.Kind != e.Kind {
			return out, "source_unavailable"
		}
		if !seen[c] {
			seen[c] = true
			citations = append(citations, c)
			evidence = append(evidence, Evidence{Role: e.Role, Kind: e.Kind, Quote: support.Quote, Citation: c})
		}
		if e.Partial && len(warnings) == 0 {
			warnings = append(warnings, "partial_source_not_proof_of_completion")
		}
	}
	if !in.KnowledgeComplete {
		warnings = append(warnings, "bounded_knowledge_comparison")
	}
	statement := attributedStatement(raw.Attribution, raw.Statement)
	identity := digest([]string{Version, in.ProjectID, in.SessionID, raw.Kind, raw.Attribution, normalizedStatement(raw.Statement)})
	out = Candidate{ID: "memory-" + identity[:32], Action: raw.Action, Attribution: raw.Attribution, RequiresHumanReview: true, Evidence: evidence, Warnings: warnings}
	out.Proposal = memory.MemoryProposal{OperationID: "extract-" + identity[:48], ID: out.ID, ProjectID: in.ProjectID, Kind: raw.Kind, Title: strings.TrimSpace(raw.Title), Statement: statement, Sources: citations}
	byID := map[string]memory.MemoryEntry{}
	for _, existing := range in.Existing {
		if existing.Global || existing.ProjectID != in.ProjectID || existing.ID == "" || len(existing.Revision) != 64 || existing.SupersededBy != "" || existing.Status != "verified" && existing.Status != "disputed" {
			continue
		}
		if _, duplicate := byID[existing.ID]; duplicate {
			return Candidate{}, "ambiguous_knowledge_snapshot"
		}
		byID[existing.ID] = existing
	}
	switch raw.Action {
	case "new":
		if len(raw.Targets) != 0 {
			return Candidate{}, "unexpected_targets"
		}
	case "corrects", "supersedes", "conflicts_with", "duplicate":
		if len(raw.Targets) == 0 {
			return Candidate{}, "missing_targets"
		}
		used := map[string]bool{}
		for _, target := range raw.Targets {
			entry, found := byID[target.ID]
			if !found || entry.Revision != target.Revision || used[target.ID] || target.ID == out.ID {
				return Candidate{}, "unbound_relationship_target"
			}
			used[target.ID] = true
			if raw.Action == "duplicate" {
				out.DuplicateOf = append(out.DuplicateOf, target)
			} else {
				out.Proposal.Relations = append(out.Proposal.Relations, memory.MemoryRelation{Kind: raw.Action, TargetID: target.ID, TargetRevision: target.Revision})
			}
		}
	default:
		return Candidate{}, "invalid_action"
	}
	// Exact duplicate detection needs no model verdict. It never chooses a
	// winner among merely similar or contradictory statements.
	for _, existing := range in.Existing {
		if _, eligible := byID[existing.ID]; !eligible {
			continue
		}
		if normalizedStatement(existing.Statement) == normalizedStatement(statement) || normalizedStatement(existing.Statement) == normalizedStatement(raw.Statement) {
			out.Action = "duplicate"
			out.DuplicateOf = []Target{{ID: existing.ID, Revision: existing.Revision}}
			out.Proposal.Relations = nil
			break
		}
	}
	return out, ""
}

// BuildReport validates mechanical grounding and attribution, not semantic
// entailment. Unsupported output remains rejected; every accepted item is still
// a human-review candidate and never an active claim.
func BuildReport(ctx context.Context, in Input, response Response, source GroundedSource) (Report, error) {
	out := Report{Version: Version, ProjectID: in.ProjectID, SessionID: in.SessionID, RequiresHumanReview: true, Candidates: []Candidate{}, Rejected: []Rejection{}, Usage: response.Usage}
	if source == nil {
		return out, ErrInvalid
	}
	if err := source.VerifyInput(ctx, in); err != nil {
		return out, err
	}
	if err := ValidateEvidence(ctx, in, source); err != nil {
		return out, err
	}
	if len(response.Candidates) > 20 || response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 {
		return out, ErrInvalid
	}
	seen := map[string]bool{}
	for i, raw := range response.Candidates {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		item, reason := candidate(ctx, in, raw, source)
		if reason != "" {
			out.Rejected = append(out.Rejected, Rejection{Index: i, Reason: reason})
			continue
		}
		if seen[item.ID] {
			out.Rejected = append(out.Rejected, Rejection{Index: i, Reason: "duplicate_candidate_in_response"})
			continue
		}
		seen[item.ID] = true
		out.Candidates = append(out.Candidates, item)
	}
	return out, nil
}
