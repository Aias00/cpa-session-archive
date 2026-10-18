package knowledge

import (
	"context"
	"strings"

	"cpa-session-archive/internal/memory"
	"gopkg.in/yaml.v3"
)

func (s *Store) Review(ctx context.Context, review Review) (memory.MemoryEntry, error) {
	var empty memory.MemoryEntry
	if !review.Confirmed || !identifier.MatchString(review.ID) || strings.TrimSpace(review.Note) == "" || review.ExpectedRevision == "" || (review.Action != "approve" && review.Action != "archive") {
		return empty, ErrInvalid
	}
	if !review.Global && review.ProjectID == "" {
		review.ProjectID = s.config.Scope.ProjectID
	}
	root, err := s.root(review.ProjectID, review.Global)
	if err != nil {
		return empty, err
	}
	unlock, err := lockRepository(ctx, root)
	if err != nil {
		return empty, err
	}
	defer unlock()
	active, hasActive, err := s.findActive(ctx, root, review.ProjectID, review.Global, review.ID)
	if err != nil {
		return empty, err
	}
	files := map[string][]byte{}
	var next document
	if review.Action == "approve" {
		file := draftPath(review.ID, review.Global)
		raw, err := committed(ctx, root, file)
		if err != nil {
			return empty, err
		}
		draft, err := parseDocument(raw, file, review.Global, review.ProjectID)
		if err != nil {
			return empty, err
		}
		if draft.entry.Status != "draft" || draft.entry.Revision != review.ExpectedRevision {
			return empty, ErrConflict
		}
		if hasActive && draft.entry.BaseRevision != active.entry.Revision {
			return empty, ErrConflict
		}
		if !hasActive && draft.entry.BaseRevision != "" {
			return empty, ErrConflict
		}
		if err := s.validateSources(ctx, draft.entry); err != nil {
			return empty, err
		}
		next = draft
		next.entry.Status = "verified"
		next.meta["status"] = "verified"
		next.meta["last_verified"] = s.now().Format("2006-01-02")
		next.meta["review_after"] = s.now().AddDate(0, 3, 0).Format("2006-01-02")
		next.meta["review_note"] = review.Note
		next.meta["candidate_state"] = "promoted"
		lineage := copyMeta(draft.meta)
		lineage["status"] = "archived"
		lineage["candidate_state"] = "promoted"
		lineage["promoted_to"] = review.ID
		lineage["review_note"] = review.Note
		files[file], err = markdown(lineage, draft.body)
		if err != nil {
			return empty, err
		}
	} else {
		if !hasActive || active.entry.Revision != review.ExpectedRevision {
			return empty, ErrConflict
		}
		next = active
		next.entry.Status = "archived"
		next.meta["status"] = "archived"
		next.meta["review_note"] = review.Note
	}
	var canonical string
	if review.Global {
		canonical = activePath(next.entry)
		if hasActive {
			canonical = active.path
		}
		files[canonical], err = markdown(next.meta, next.body)
	} else {
		canonical = ".codex/ai-context.yaml"
		original := []byte{}
		tracked, e := gitCommand(ctx, root, "ls-tree", "--name-only", "HEAD", "--", canonical)
		if e != nil {
			return empty, e
		}
		if len(tracked) > 0 {
			original, err = committed(ctx, root, canonical)
			if err != nil {
				return empty, err
			}
		}
		context, documents, e := projectDocuments(original, review.ProjectID)
		if e != nil {
			return empty, e
		}
		if len(context) == 0 {
			context["project"] = review.ProjectID
			context["global_knowledge"] = []string{}
			context["local_exceptions"] = []any{}
		}
		list := []map[string]any{}
		replaced := false
		for _, d := range documents {
			if d.entry.ID == review.ID {
				list = append(list, next.meta)
				replaced = true
			} else {
				list = append(list, d.meta)
			}
		}
		if !replaced {
			list = append(list, next.meta)
		}
		context["reviewed_memories"] = list
		files[canonical], err = yaml.Marshal(context)
	}
	if err != nil {
		return empty, err
	}
	if err := commitFiles(ctx, root, "memory: "+review.Action+" reviewed entry "+review.ID, files); err != nil {
		return empty, err
	}
	result, found, err := s.findActive(ctx, root, review.ProjectID, review.Global, review.ID)
	if err != nil {
		return empty, err
	}
	if !found {
		return empty, ErrInvalid
	}
	result.entry.Commit = fileCommit(ctx, root, canonical)
	return result.entry, nil
}
func copyMeta(meta map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range meta {
		out[key] = value
	}
	return out
}
