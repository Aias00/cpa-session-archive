package knowledge

import (
	"context"
	"time"

	"cpa-session-archive/internal/memory"
)

func validity(state string, reasons ...string) *memory.MemoryValidity {
	return &memory.MemoryValidity{State: state, Reasons: reasons}
}

// Inactive review decisions remain inactive independently of a successor's
// expiry or source availability. Review dates request revalidation, not a new
// factual verdict or automatic resurrection of an older claim.
func (s *Store) entryValidity(ctx context.Context, entry memory.MemoryEntry) *memory.MemoryValidity {
	switch entry.Status {
	case "draft":
		return validity("draft", "unreviewed_candidate")
	case "archived":
		return validity("archived", "human_archived")
	case "superseded":
		return validity("superseded", "human_reviewed_replacement")
	case "disputed":
		return validity("disputed", "human_reviewed_conflict")
	case "verified":
	default:
		return validity("invalid_metadata", "unknown_canonical_status")
	}
	if entry.SupersededBy != "" {
		return validity("superseded", "reviewed_replacement_reference")
	}
	if _, err := normalizeRelations(entry.ID, entry.Relations); err != nil {
		return validity("invalid_metadata", "invalid_relationship_shape")
	}
	for _, relation := range entry.Relations {
		if relation.Kind == "conflicts_with" {
			return validity("disputed", "unresolved_conflict_reference")
		}
	}
	if entry.ReviewAfter != "" {
		if _, err := time.Parse("2006-01-02", entry.ReviewAfter); err != nil {
			return validity("invalid_metadata", "invalid_review_after")
		}
		if entry.ReviewAfter < s.now().Format("2006-01-02") {
			return validity("review_due", "review_after_elapsed")
		}
	}
	if err := s.validateSources(ctx, entry); err != nil {
		return validity("source_unavailable", "retained_evidence_not_validated")
	}
	return validity("current", "reviewed_and_eligible")
}
