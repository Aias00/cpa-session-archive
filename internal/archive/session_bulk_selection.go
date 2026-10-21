package archive

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// sessionSelection is shared by live browsing and frozen bulk previews. A view
// classifies indexed evidence without assigning identities or editing sessions.
func sessionSelection(options SessionBrowseOptions, now time.Time) (SessionBrowseOptions, string, []any, error) {
	if options.Limit == 0 {
		options.Limit = 100
	}
	if options.Limit < 1 || options.Limit > 1000 || options.Offset < 0 || options.Offset > 1000000 {
		return options, "", nil, invalidSessionManagement("invalid page bounds")
	}
	if options.Archived == "" {
		options.Archived = "exclude"
	}
	if options.Archived != "exclude" && options.Archived != "only" && options.Archived != "all" {
		return options, "", nil, invalidSessionManagement("invalid archived filter")
	}
	if options.View == "" {
		options.View = "all"
	}
	if options.InactiveDays == 0 {
		options.InactiveDays = 30
	}
	if options.InactiveDays < 1 || options.InactiveDays > 3650 {
		return options, "", nil, invalidSessionManagement("inactive_days must be between 1 and 3650")
	}
	if !utf8.ValidString(options.Query) || !utf8.ValidString(options.Tag) || strings.ContainsFunc(options.Query, unicode.IsControl) || strings.ContainsFunc(options.Tag, unicode.IsControl) || utf8.RuneCountInString(options.Query) > 1000 || utf8.RuneCountInString(options.Tag) > 48 || len(options.Filters) > 64 {
		return options, "", nil, invalidSessionManagement("invalid query bounds")
	}
	where := []string{}
	args := []any{}
	if options.Archived != "all" {
		where = append(where, `COALESCE(m.archived,0)=?`)
		args = append(args, options.Archived == "only")
	}
	if options.Starred {
		where = append(where, `COALESCE(m.starred,0)=1`)
	}
	if options.Tag != "" {
		where = append(where, `EXISTS(SELECT 1 FROM session_management_tags t WHERE t.session_id=s.session_id AND t.tag=?)`)
		args = append(args, options.Tag)
	}
	if options.Query != "" {
		where = append(where, `(s.session_id LIKE ? ESCAPE '\' OR s.project LIKE ? ESCAPE '\' OR s.summary LIKE ? ESCAPE '\' OR m.title LIKE ? ESCAPE '\' OR m.note LIKE ? ESCAPE '\' OR EXISTS(SELECT 1 FROM session_management_tags t WHERE t.session_id=s.session_id AND t.tag LIKE ? ESCAPE '\'))`)
		pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(options.Query) + "%"
		for range 6 {
			args = append(args, pattern)
		}
	}
	filterNames := make([]string, 0, len(options.Filters))
	for name, value := range options.Filters {
		if len(name) > 128 || len(value) > 4096 || !utf8.ValidString(name) || !utf8.ValidString(value) || strings.ContainsFunc(name, unicode.IsControl) || strings.ContainsFunc(value, unicode.IsControl) {
			return options, "", nil, invalidSessionManagement("invalid facet filter")
		}
		filterNames = append(filterNames, name)
	}
	sort.Strings(filterNames)
	for _, name := range filterNames {
		where = append(where, `EXISTS(SELECT 1 FROM session_facets f WHERE f.session_id=s.session_id AND f.name=? AND f.value=?)`)
		args = append(args, name, options.Filters[name])
	}
	main := `EXISTS(SELECT 1 FROM session_facets f WHERE f.session_id=s.session_id AND ((f.name='thread.source' AND f.value IN ('user','main')) OR (f.name IN ('native.agent_role','native.agent_kind') AND f.value='main')))`
	subagent := `EXISTS(SELECT 1 FROM session_facets f WHERE f.session_id=s.session_id AND ((f.name IN ('thread.source','native.agent_role','native.agent_kind') AND f.value IN ('subagent','sub_agent')) OR (f.name='native.parent_agent_id' AND TRIM(f.value)<>'')))`
	switch options.View {
	case "all":
	case "recent":
		// Retained requests can receive a later completion without refreshing
		// the derived summary. The covering index avoids loading raw payloads.
		cutoff := now.AddDate(0, 0, -7).Format(time.RFC3339Nano)
		where = append(where, `(julianday(s.last_at)>=julianday(?) OR EXISTS(SELECT 1 FROM records r INDEXED BY idx_records_session_snapshot_metadata WHERE r.session_id=s.session_id AND julianday(r.completed_at)>=julianday(?)))`)
		args = append(args, cutoff, cutoff)
	case "inactive":
		cutoff := now.AddDate(0, 0, -options.InactiveDays).Format(time.RFC3339Nano)
		where = append(where, `julianday(s.last_at)<julianday(?)`, `NOT EXISTS(SELECT 1 FROM records r INDEXED BY idx_records_session_snapshot_metadata WHERE r.session_id=s.session_id AND julianday(r.completed_at)>=julianday(?))`)
		args = append(args, cutoff, cutoff)
	case "main":
		where = append(where, main, `NOT `+subagent)
	case "subagent":
		where = append(where, subagent)
	case "system":
		// A mixed user/system session remains visible as a main session.
		where = append(where, `EXISTS(SELECT 1 FROM session_facets f WHERE f.session_id=s.session_id AND f.name='thread.source' AND f.value='system')`,
			`NOT EXISTS(SELECT 1 FROM session_facets f WHERE f.session_id=s.session_id AND f.name='thread.source' AND f.value<>'system')`, `NOT `+main, `NOT `+subagent)
	case "prewarm":
		where = append(where, `EXISTS(SELECT 1 FROM session_facets f WHERE f.session_id=s.session_id AND f.name='request.kind' AND f.value='prewarm')`,
			`NOT EXISTS(SELECT 1 FROM session_facets f WHERE f.session_id=s.session_id AND f.name='request.kind' AND f.value<>'prewarm')`)
	case "unassigned":
		// Display names never establish a project identity. Keep exact project
		// IDs, complete paths and Git remotes; do not merge by directory name.
		where = append(where, `(EXISTS(SELECT 1 FROM session_facets f WHERE f.session_id=s.session_id AND ((f.name='native.identity_conflict' AND f.value IN ('true','1')) OR (f.name='project.id' AND (f.value GLOB 'native:*:unassigned' OR f.value GLOB 'native:*:ambiguous:*')))) OR NOT EXISTS(SELECT 1 FROM session_facets f WHERE f.session_id=s.session_id AND TRIM(f.value)<>'' AND (f.name IN ('project.id','workspace.id','git.remote','git.repository') OR (f.name IN ('project.path','workspace.path') AND (SUBSTR(f.value,1,1)='/' OR f.value GLOB '[A-Za-z]:[\/]*' OR SUBSTR(f.value,1,2)=CHAR(92)||CHAR(92))))))`)
	default:
		return options, "", nil, invalidSessionManagement("invalid automatic view")
	}
	from := ` FROM session_summaries s LEFT JOIN session_management m ON m.session_id=s.session_id`
	if len(where) > 0 {
		from += ` WHERE ` + strings.Join(where, ` AND `)
	}
	return options, from, args, nil
}
