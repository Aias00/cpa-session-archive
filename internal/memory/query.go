package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"cpa-session-archive/internal/storage"
)

type candidate struct {
	hit  Hit
	text string
}

func validateQuery(q Query, allowEmpty bool) (Query, error) {
	q.Text = strings.TrimSpace(q.Text)
	if q.Mode != "" && q.Mode != "literal" && q.Mode != "diverse" {
		return q, ErrInvalidQuery
	}
	if (!allowEmpty && q.Text == "") || utf8.RuneCountInString(q.Text) > 256 || len(strings.Fields(q.Text)) > 16 || !utf8.ValidString(q.Text) || q.Limit < 0 || q.Limit > 100 {
		return q, ErrInvalidQuery
	}
	if q.Mode == "diverse" && len(lexicalTokens(q.Text)) == 0 {
		return q, ErrInvalidQuery
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	var after, before time.Time
	var err error
	if q.After != "" {
		after, err = time.Parse(time.RFC3339Nano, q.After)
		if err != nil {
			return q, ErrInvalidQuery
		}
	}
	if q.Before != "" {
		before, err = time.Parse(time.RFC3339Nano, q.Before)
		if err != nil {
			return q, ErrInvalidQuery
		}
	}
	if !after.IsZero() && !before.IsZero() && !after.Before(before) {
		return q, ErrInvalidQuery
	}
	return q, nil
}
func (e *Engine) Search(ctx context.Context, q Query) (SearchResult, error) {
	return e.search(ctx, q, false)
}
func (e *Engine) search(ctx context.Context, q Query, allowEmpty bool) (SearchResult, error) {
	out := SearchResult{Hits: []Hit{}}
	q, err := validateQuery(q, allowEmpty)
	if err != nil {
		return out, err
	}
	if q.Mode == "diverse" {
		return e.searchDiverse(ctx, q)
	}
	if _, err := e.sourceIdentity(ctx, false); err != nil {
		return SearchResult{}, err
	}
	tokens := lexicalTokens(q.Text)
	where := []string{`c.role NOT IN ('system','developer')`, `COALESCE(m.kind,'') NOT IN ('context','image')`}
	args := []any{}
	from := `chunks c JOIN sources s ON s.request_id=c.request_id LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer`
	if len(tokens) > 0 {
		from += ` JOIN chunk_search f ON f.docid=c.text_id`
		where = append(where, `f.tokens MATCH ?`)
		args = append(args, strings.Join(tokens, " "))
	}
	for _, filter := range []struct{ column, value string }{{"project_id", q.ProjectID}, {"client", q.Client}, {"session_id", q.SessionID}} {
		if filter.value != "" {
			where = append(where, "s."+filter.column+"=?")
			args = append(args, filter.value)
		}
	}
	for _, filter := range []struct{ op, value string }{{">=", q.After}, {"<", q.Before}} {
		if filter.value != "" {
			t, _ := time.Parse(time.RFC3339Nano, filter.value)
			where = append(where, "s.event_ns"+filter.op+"?")
			args = append(args, t.UnixNano())
		}
	}
	// Materialize only IDs and sort keys before loading potentially large text.
	// Otherwise SQLite reads each occurrence's text before its top-N sort, even
	// when thousands of occurrences share the same text and will be discarded.
	const kindRank = `CASE WHEN COALESCE(m.kind,'') IN ('tool_use','tool_result') OR c.role='tool' THEN 2 WHEN COALESCE(m.kind,'')='summary' THEN 1 ELSE 0 END`
	selection := `SELECT c.id,s.request_id,s.event_ns,` + kindRank + ` AS kind_rank FROM ` + from
	query := `WITH candidates AS MATERIALIZED (` + selection
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	const selectionOrder = ` ORDER BY kind_rank,s.event_ns DESC,s.request_id,c.id LIMIT 1001)`
	const resultQuery = `
 SELECT s.request_id,s.revision,s.project_id,s.client,s.session_id,s.event_at,s.partial,c.side,c.pointer,c.role,c.start,c.end,t.text,COALESCE(m.kind,CASE WHEN c.role='tool' THEN 'tool_result' ELSE 'text' END),COALESCE(m.tool_name,''),COALESCE(m.tool_call_id,''),COALESCE(m.model,''),COALESCE(m.message_at,'')
 FROM candidates x JOIN chunks c ON c.id=x.id JOIN sources s ON s.request_id=x.request_id JOIN chunk_texts t ON t.id=c.text_id
 LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer`
	const resultOrder = ` ORDER BY x.kind_rank,x.event_ns DESC,x.request_id,x.id`
	query += selectionOrder + resultQuery + resultOrder
	fullQuery := query
	// A complete rank-zero prefix inside the newest sources is also the global
	// prefix: every excluded source sorts later. Incomplete prefixes must fall
	// back to the full query, including summary/tool ranks and older evidence.
	if len(tokens) > 0 {
		var matches int
		if err := e.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT docid FROM chunk_search WHERE tokens MATCH ? LIMIT 1024)`, args[0]).Scan(&matches); err != nil {
			return out, err
		}
		if matches == 1024 {
			recentWhere := append([]string(nil), where...)
			for i, clause := range recentWhere {
				if clause == `f.tokens MATCH ?` {
					recentWhere[i] = `c.text_id IN (SELECT docid FROM matched_texts)`
				}
			}
			recentWhere = append(recentWhere, `(`+kindRank+`)=0`)
			recentQuery := `WITH recent_sources AS MATERIALIZED (SELECT request_id,event_ns,project_id,client,session_id FROM sources ORDER BY event_ns DESC,request_id LIMIT 4096),
 matched_texts AS MATERIALIZED (SELECT docid FROM chunk_search WHERE tokens MATCH ?),
 candidates AS MATERIALIZED (SELECT c.id,s.request_id,s.event_ns,` + kindRank + ` AS kind_rank
 FROM recent_sources s CROSS JOIN chunks c ON c.request_id=s.request_id
 LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer
 WHERE ` + strings.Join(recentWhere, " AND ") + selectionOrder
			// Keep the count guard inside this same statement/read snapshot.
			recentQuery += resultQuery + ` WHERE (SELECT COUNT(*) FROM candidates)=1001` + resultOrder
			query = recentQuery
			// An empty result from the guarded query triggers the original path.
		}
	}
	rows, err := e.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	candidates := []candidate{}
	first := rows.Next()
	if !first && query != fullQuery {
		readErr := rows.Err()
		rows.Close()
		if readErr != nil {
			return out, readErr
		}
		rows, err = e.readDB.QueryContext(ctx, fullQuery, args...)
		if err != nil {
			return out, err
		}
		first = rows.Next()
	}
	for next := first; next; next = rows.Next() {
		var c candidate
		err = rows.Scan(&c.hit.Citation.RequestID, &c.hit.Citation.Revision, &c.hit.ProjectID, &c.hit.Client, &c.hit.SessionID, &c.hit.EventAt, &c.hit.Partial, &c.hit.Citation.Side, &c.hit.Citation.Pointer, &c.hit.Role, &c.hit.Citation.Start, &c.hit.Citation.End, &c.text, &c.hit.Kind, &c.hit.ToolName, &c.hit.ToolCallID, &c.hit.Model, &c.hit.MessageAt)
		if err != nil {
			rows.Close()
			return out, err
		}
		c.hit.Citation.NormalizerVersion = NormalizerVersion
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return e.finishSearch(ctx, q, candidates)
}

func (e *Engine) finishSearch(ctx context.Context, q Query, candidates []candidate) (SearchResult, error) {
	out := SearchResult{Hits: []Hit{}}
	terms := strings.Fields(strings.ToLower(q.Text))
	var err error
	if len(candidates) > 1000 {
		out.Truncated = true
		candidates = candidates[:1000]
	}
	revisions := map[string]string{}
	seen := map[string]bool{}
	type excerptKey struct{ role, kind, tool, text string }
	excerpts := map[excerptKey]bool{}
	for _, c := range candidates {
		lower := strings.ToLower(c.text)
		matches := true
		for _, term := range terms {
			if !strings.Contains(lower, term) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		rev, known := revisions[c.hit.Citation.RequestID]
		if !known {
			current, err := e.sourceRevision(ctx, c.hit.Citation.RequestID)
			if errors.Is(err, storage.ErrSourceNotFound) {
				revisions[c.hit.Citation.RequestID] = ""
				continue
			}
			if err != nil {
				return SearchResult{}, err
			}
			rev = current
			revisions[c.hit.Citation.RequestID] = rev
		}
		if rev != c.hit.Citation.Revision {
			continue
		}
		key := c.hit.Citation.RequestID + "\x00" + c.hit.Citation.Side + "\x00" + c.hit.Citation.Pointer
		if seen[key] {
			continue
		}
		// Snippet range is computed on the original text (case folding can change
		// UTF-8 widths). Include the first literal match when one is present.
		runes := []rune(c.text)
		position := 0
		if len(terms) > 0 {
			if byteOffset := strings.Index(lower, terms[0]); byteOffset >= 0 {
				position = utf8.RuneCountInString(lower[:byteOffset])
			}
		}
		start := max(0, position-80)
		end := min(len(runes), max(start+320, position+utf8.RuneCountInString(q.Text)))
		c.hit.Citation.Start += start
		c.hit.Citation.End = c.hit.Citation.Start + (end - start)
		c.hit.Snippet = string(runes[start:end])
		if q.Mode == "diverse" {
			excerpt := excerptKey{c.hit.Role, c.hit.Kind, c.hit.ToolName, c.hit.Snippet}
			if excerpts[excerpt] {
				continue
			}
			excerpts[excerpt] = true
		}
		seen[key] = true
		if len(out.Hits) >= q.Limit {
			out.Truncated = true
			break
		}
		out.Hits = append(out.Hits, c.hit)
	}
	out.Status, err = e.Status(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	return out, err
}
func (e *Engine) ReadSource(ctx context.Context, c Citation) (Evidence, error) {
	out := Evidence{Citation: c}
	if c.NormalizerVersion != NormalizerVersion || c.RequestID == "" || c.Revision == "" || c.Start < 0 || c.End <= c.Start || c.End-c.Start > 1600 {
		return out, ErrSourceUnavailable
	}
	if _, err := e.sourceIdentity(ctx, false); err != nil {
		return Evidence{}, err
	}
	source, err := e.feed.MemorySource(ctx, c.RequestID)
	if errors.Is(err, storage.ErrSourceNotFound) {
		return out, ErrSourceUnavailable
	}
	if err != nil {
		return out, err
	}
	if source.Revision != c.Revision || isInternal(source.Record) {
		return out, ErrSourceUnavailable
	}
	out.ProjectID, out.Client = sourceScope(source.Record)
	out.SessionID = source.Record.SessionID
	doc := Normalize(source.Record)
	for _, message := range doc.Messages {
		if message.Side == c.Side && message.Pointer == c.Pointer {
			runes := []rune(message.Text)
			if c.End > len(runes) {
				return out, ErrSourceUnavailable
			}
			out.Kind = messageKind(message)
			out.ToolName = message.ToolName
			out.ToolCallID = message.ToolCallID
			out.Model = message.Model
			out.MessageAt = message.MessageAt
			out.Text = string(runes[c.Start:c.End])
			out.Partial = doc.Partial
			out.Issues = doc.Issues
			out.CoverageIssues = classifyCoverage(doc.Issues)
			return out, nil
		}
	}
	return out, ErrSourceUnavailable
}

// Recap is a bounded packet of literal excerpts, never an inferred task status.
func (e *Engine) Recap(ctx context.Context, q Query) (RecapResult, error) {
	if q.ProjectID == "" && q.SessionID == "" {
		return RecapResult{}, ErrInvalidQuery
	}
	if q.Limit == 0 || q.Limit > 12 {
		q.Limit = 12
	}
	results, err := e.search(ctx, q, true)
	if err != nil {
		return RecapResult{}, err
	}
	out := RecapResult{Status: results.Status, Hits: []Hit{}}
	text := "Source excerpts only; verify against current instructions.\n"
	if len(results.Hits) == 0 {
		text += "No retained evidence found.\n"
	}
	for _, hit := range results.Hits {
		entry := fmt.Sprintf("\n[%s | %s | %s | %s]\n%s\nSource: %s@%s %s:%s [%d:%d] (normalizer v%d)\n", hit.EventAt, hit.Client, hit.SessionID, hit.Role, hit.Snippet, hit.Citation.RequestID, hit.Citation.Revision, hit.Citation.Side, hit.Citation.Pointer, hit.Citation.Start, hit.Citation.End, NormalizerVersion)
		if len(text)+len(entry) > 8192 {
			break
		}
		text += entry
		out.Hits = append(out.Hits, hit)
	}
	out.Text = text
	return out, nil
}
