package knowledge

import (
	"context"
	"fmt"
	"sort"

	"cpa-session-archive/internal/memory"
)

// Only ordinary verified entries allow in-place correction. Inactive records
// and reviewed relationship nodes retain their identities and historical edges.
func requireNewLineageID(entry memory.MemoryEntry) error {
	if entry.Status != "verified" || len(entry.Relations) > 0 || entry.SupersededBy != "" {
		return fmt.Errorf("same-ID changes require a verified entry without reviewed lineage; use a new ID and, for an eligible verified/disputed target, corrects/supersedes: %w", ErrConflict)
	}
	return nil
}

func validMemoryRevision(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Relations target pre-transition content revisions in the same registered
// authority and scope. Replacements and unresolved conflicts cannot be mixed.
func normalizeRelations(id string, values []memory.MemoryRelation) ([]memory.MemoryRelation, error) {
	if len(values) > 8 {
		return nil, ErrInvalid
	}
	if len(values) == 0 {
		return nil, nil
	}
	seen := map[string]memory.MemoryRelation{}
	class := ""
	for _, relation := range values {
		nextClass := "replacement"
		switch relation.Kind {
		case "supersedes", "corrects":
		case "conflicts_with":
			nextClass = "conflict"
		default:
			return nil, ErrInvalid
		}
		if !identifier.MatchString(relation.TargetID) || relation.TargetID == id || !validMemoryRevision(relation.TargetRevision) || class != "" && class != nextClass {
			return nil, ErrInvalid
		}
		class = nextClass
		if prior, ok := seen[relation.TargetID]; ok && prior != relation {
			return nil, ErrInvalid
		}
		seen[relation.TargetID] = relation
	}
	out := make([]memory.MemoryRelation, 0, len(seen))
	for _, relation := range seen {
		out = append(out, relation)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TargetID < out[j].TargetID })
	return out, nil
}

func equalRelations(id string, a, b []memory.MemoryRelation) bool {
	a, err := normalizeRelations(id, a)
	if err != nil {
		return false
	}
	b, err = normalizeRelations(id, b)
	if err != nil || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Recheck every target both at proposal and approval. A recorded relationship
// remains historical after approval changes the target's canonical bytes.
func (s *Store) relationTargets(ctx context.Context, root string, entry memory.MemoryEntry) ([]document, error) {
	relations, err := normalizeRelations(entry.ID, entry.Relations)
	if err != nil {
		return nil, err
	}
	if len(relations) == 0 {
		return nil, nil
	}
	documents, err := s.documents(ctx, root, entry.ProjectID, entry.Global, false)
	if err != nil {
		return nil, err
	}
	byID := make(map[string][]document, len(documents))
	for _, d := range documents {
		byID[d.entry.ID] = append(byID[d.entry.ID], d)
	}
	targets := make([]document, 0, len(relations))
	for _, relation := range relations {
		found := byID[relation.TargetID]
		if len(found) != 1 {
			return nil, ErrConflict
		}
		target := found[0]
		if target.entry.Revision != relation.TargetRevision || target.entry.SupersededBy != "" || (target.entry.Status != "verified" && target.entry.Status != "disputed") {
			return nil, ErrConflict
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func relationUpdates(entry memory.MemoryEntry, targets []document) (map[string]document, string) {
	updates := make(map[string]document, len(targets))
	byID := make(map[string]document, len(targets))
	for _, target := range targets {
		byID[target.entry.ID] = target
	}
	status := "verified"
	for _, relation := range entry.Relations {
		target := byID[relation.TargetID]
		target.meta = copyMeta(target.meta)
		if relation.Kind == "conflicts_with" {
			status = "disputed"
			target.entry.Status = "disputed"
		} else {
			target.entry.Status = "superseded"
			target.entry.SupersededBy = entry.ID
			target.meta["superseded_by"] = entry.ID
		}
		target.meta["status"] = target.entry.Status
		updates[target.entry.ID] = target
	}
	return updates, status
}
