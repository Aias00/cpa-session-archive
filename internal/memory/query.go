package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

func validateQuery(q Query, allowEmpty bool) (Query, error) {
	q.Text = strings.TrimSpace(q.Text)
	if (!allowEmpty && q.Text == "") || utf8.RuneCountInString(q.Text) > 256 || len(strings.Fields(q.Text)) > 16 || !utf8.ValidString(q.Text) || q.Limit < 0 || q.Limit > 100 {
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
	terms := strings.Fields(strings.ToLower(q.Text))
	tokens := lexicalTokens(q.Text)
	where := []string{`c.role NOT IN ('system','developer')`, `COALESCE(m.kind,'') NOT IN ('context','image')`}
	args := []any{}
	from := `chunks c JOIN sources s ON s.request_id=c.request_id JOIN chunk_texts t ON t.id=c.text_id LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer`
	if len(tokens) > 0 {
		from += ` JOIN chunk_search f ON f.docid=t.id`
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
	query := `SELECT s.request_id,s.revision,s.project_id,s.client,s.session_id,s.event_at,s.partial,c.side,c.pointer,c.role,c.start,c.end,t.text,COALESCE(m.kind,CASE WHEN c.role='tool' THEN 'tool_result' ELSE 'text' END),COALESCE(m.tool_name,''),COALESCE(m.tool_call_id,''),COALESCE(m.model,''),COALESCE(m.message_at,'') FROM ` + from
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += ` ORDER BY CASE WHEN COALESCE(m.kind,'') IN ('tool_use','tool_result') OR c.role='tool' THEN 2 WHEN COALESCE(m.kind,'')='summary' THEN 1 ELSE 0 END,s.event_ns DESC,s.request_id,c.id LIMIT 1001`
	rows, err := e.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	type candidate struct {
		hit  Hit
		text string
	}
	candidates := []candidate{}
	for rows.Next() {
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
	if len(candidates) > 1000 {
		out.Truncated = true
		candidates = candidates[:1000]
	}
	revisions := map[string]string{}
	seen := map[string]bool{}
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
			source, err := e.feed.MemorySource(ctx, c.hit.Citation.RequestID)
			if errors.Is(err, sql.ErrNoRows) {
				revisions[c.hit.Citation.RequestID] = ""
				continue
			}
			if err != nil {
				return out, err
			}
			rev = source.Revision
			revisions[c.hit.Citation.RequestID] = rev
		}
		if rev != c.hit.Citation.Revision {
			continue
		}
		key := c.hit.Citation.RequestID + "\x00" + c.hit.Citation.Side + "\x00" + c.hit.Citation.Pointer
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(out.Hits) >= q.Limit {
			out.Truncated = true
			break
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
		out.Hits = append(out.Hits, c.hit)
	}
	out.Status, err = e.Status(ctx)
	return out, err
}
func (e *Engine) ReadSource(ctx context.Context, c Citation) (Evidence, error) {
	out := Evidence{Citation: c}
	if c.NormalizerVersion != NormalizerVersion || c.RequestID == "" || c.Revision == "" || c.Start < 0 || c.End <= c.Start || c.End-c.Start > 1600 {
		return out, ErrSourceUnavailable
	}
	source, err := e.feed.MemorySource(ctx, c.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
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
