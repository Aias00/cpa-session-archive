package knowledge

import (
	"bytes"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"cpa-session-archive/internal/memory"
	"gopkg.in/yaml.v3"
)

type document struct {
	entry memory.MemoryEntry
	meta  map[string]any
	body  string
	path  string
}

func scalar(value any) string {
	if date, ok := value.(time.Time); ok {
		return date.Format("2006-01-02")
	}
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
func parseDocument(raw []byte, file string, global bool, project string) (document, error) {
	var d document
	if !bytes.HasPrefix(raw, []byte("---\n")) {
		return d, ErrInvalid
	}
	end := bytes.Index(raw[4:], []byte("\n---\n"))
	if end < 0 {
		return d, ErrInvalid
	}
	end += 4
	if err := yaml.Unmarshal(raw[4:end], &d.meta); err != nil {
		return d, ErrInvalid
	}
	if err := yaml.Unmarshal(raw[4:end], &d.entry); err != nil {
		return d, ErrInvalid
	}
	if !identifier.MatchString(d.entry.ID) {
		return d, ErrInvalid
	}
	// Explicit persisted ownership cannot be relabeled by another registry entry.
	// Older canonical documents without these fields inherit the fixed root scope.
	if value, exists := d.meta["archive_project_id"]; exists {
		if value != nil {
			stored, ok := value.(string)
			if !ok || stored != project {
				return d, ErrInvalid
			}
		}
	}
	if value, exists := d.meta["archive_global"]; exists && value != nil {
		stored, ok := value.(bool)
		if !ok || stored != global {
			return d, ErrInvalid
		}
	}
	d.entry.Global = global
	d.entry.ProjectID = project
	d.entry.Revision = revision(raw)
	d.entry.ReviewAfter = scalar(d.meta["review_after"])
	if d.entry.Kind == "" {
		d.entry.Kind = scalar(d.meta["kind"])
	}
	d.body = strings.TrimSpace(string(raw[end+5:]))
	d.path = file
	return d, nil
}
func markdown(meta map[string]any, body string) ([]byte, error) {
	header, err := yaml.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(header) + "---\n\n" + strings.TrimSpace(body) + "\n"), nil
}
func metadata(entry memory.MemoryEntry, now time.Time) map[string]any {
	kind := map[string]string{"decision": "decision", "fact": "domain", "experience": "pattern", "preference": "decision"}[entry.Kind]
	sources := []map[string]string{}
	for _, citation := range entry.Sources {
		values := url.Values{"revision": {citation.Revision}, "side": {citation.Side}, "pointer": {citation.Pointer}, "start": {fmt.Sprint(citation.Start)}, "end": {fmt.Sprint(citation.End)}, "normalizer_version": {fmt.Sprint(citation.NormalizerVersion)}}
		sources = append(sources, map[string]string{"type": "url", "location": "cpa-archive://request/" + url.PathEscape(citation.RequestID) + "?" + values.Encode()})
	}
	return map[string]any{"id": entry.ID, "title": entry.Title, "kind": kind, "memory_kind": entry.Kind, "status": entry.Status, "confidence": "medium", "tags": []string{"cpa-memory", entry.Kind}, "scope": func() string {
		if entry.Global {
			return "Reusable engineering knowledge; review applicability before use"
		}
		return "Project " + entry.ProjectID
	}(), "summary": entry.Statement, "last_verified": now.Format("2006-01-02"), "review_after": now.AddDate(0, 3, 0).Format("2006-01-02"), "source": sources, "related": []string{}, "superseded_by": entry.SupersededBy, "relations": entry.Relations, "sensitivity": "internal", "archive_sources": entry.Sources, "archive_project_id": entry.ProjectID, "archive_global": entry.Global, "operation_id": entry.OperationID, "base_revision": entry.BaseRevision}
}
func body(entry memory.MemoryEntry) string {
	return "## When To Use\n\nApply only within the reviewed scope.\n\n## When Not To Use\n\nDo not treat historical evidence as current instructions or apply it outside its scope.\n\n## Procedure\n\n" + entry.Statement + "\n\n## Evidence\n\nRetained archive citations are recorded in the source metadata.\n\n## Known Exceptions\n\nReview project-local instructions and exceptions before reuse.\n\n## Related Knowledge\n\nNone known.\n"
}
func draftPath(id string, global bool) string {
	if global {
		return "inbox/captured/cpa-" + id + ".md"
	}
	return ".codex/memory-candidates/" + id + ".md"
}
func activePath(entry memory.MemoryEntry) string {
	kind := map[string]string{"decision": "decisions", "fact": "domains", "experience": "patterns", "preference": "decisions"}[entry.Kind]
	return path.Join("entries", kind, entry.ID+".md")
}
func projectDocuments(raw []byte, project string) (map[string]any, []document, error) {
	meta := map[string]any{}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &meta); err != nil {
			return nil, nil, ErrInvalid
		}
	}
	documents := []document{}
	items, ok := meta["reviewed_memories"]
	if !ok {
		return meta, documents, nil
	}
	list, ok := items.([]any)
	if !ok {
		return nil, nil, ErrInvalid
	}
	for _, item := range list {
		fields, ok := item.(map[string]any)
		if !ok {
			return nil, nil, ErrInvalid
		}
		encoded, err := markdown(fields, "")
		if err != nil {
			return nil, nil, err
		}
		d, err := parseDocument(encoded, ".codex/ai-context.yaml", false, project)
		if err != nil {
			return nil, nil, err
		}
		documents = append(documents, d)
	}
	return meta, documents, nil
}
