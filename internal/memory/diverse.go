package memory

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// searchDiverse rounds across equal content and message windows before applying
// the candidate budget. Equal content never merges source identity: every hit
// retains one occurrence's metadata and independently validated citation.
func (e *Engine) searchDiverse(ctx context.Context, q Query) (SearchResult, error) {
	// Window sorting is proportional to matching occurrences, not returned rows.
	// Bound pool wait, SQL work and live source checks together for this opt-in
	// path. A budget failure must not masquerade as a complete empty result.
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := e.diverseCandidates(bounded, q)
	if ctx.Err() != nil {
		return SearchResult{}, ctx.Err()
	}
	if bounded.Err() != nil {
		return SearchResult{}, ErrQueryBudget
	}
	return out, err
}

func (e *Engine) diverseCandidates(ctx context.Context, q Query) (SearchResult, error) {
	if _, err := e.sourceIdentity(ctx, false); err != nil {
		return SearchResult{}, err
	}
	// First-round ordinary text in the newest authorized sources is a complete
	// global prefix, even if it is shorter than the candidate budget. Return it
	// only when live checks and literal/display filters produce enough hits.
	// A narrow FTS query can be cheaper than scanning recent long histories.
	// Abandon a costly recent attempt while retaining the overall budget.
	recent, cancel := context.WithTimeout(ctx, time.Second)
	candidates, err := e.selectDiverseCandidates(recent, q, max(16, q.Limit*2+1), true)
	recentExpired := recent.Err() != nil && ctx.Err() == nil
	cancel()
	if err != nil && !recentExpired {
		return SearchResult{}, err
	}
	if recentExpired {
		candidates = nil
	}
	if len(candidates) > 0 {
		result, err := e.finishSearch(ctx, q, candidates)
		if err != nil {
			return SearchResult{}, err
		}
		if len(result.Hits) >= q.Limit {
			result.Truncated = true
			return result, nil
		}
	}
	// A short recent prefix never proves exhaustion. Replenish from the full
	// 1,001-row prefix, returning only this final snapshot's validated results.
	candidates, err = e.selectDiverseCandidates(ctx, q, 1001, false)
	if err != nil {
		return SearchResult{}, err
	}
	return e.finishSearch(ctx, q, candidates)
}

func (e *Engine) selectDiverseCandidates(ctx context.Context, q Query, limit int, recent bool) ([]candidate, error) {
	var out []candidate
	where := []string{`c.role NOT IN ('system','developer')`, `COALESCE(m.kind,'') NOT IN ('context','image')`}
	args := []any{}
	sourceWhere := []string{}
	sourceArgs := []any{}
	tokens := lexicalTokens(q.Text)
	if len(tokens) > 0 {
		where = append(where, `f.tokens MATCH ?`)
		args = append(args, strings.Join(tokens, " "))
	}
	for _, filter := range []struct{ column, value string }{{"project_id", q.ProjectID}, {"client", q.Client}, {"session_id", q.SessionID}} {
		if filter.value != "" {
			clause := "s." + filter.column + "=?"
			where = append(where, clause)
			sourceWhere = append(sourceWhere, clause)
			args = append(args, filter.value)
			sourceArgs = append(sourceArgs, filter.value)
		}
	}
	for _, filter := range []struct{ op, value string }{{">=", q.After}, {"<", q.Before}} {
		if filter.value != "" {
			stamp, _ := time.Parse(time.RFC3339Nano, filter.value)
			clause := "s.event_ns" + filter.op + "?"
			where = append(where, clause)
			sourceWhere = append(sourceWhere, clause)
			args = append(args, stamp.UnixNano())
			sourceArgs = append(sourceArgs, stamp.UnixNano())
		}
	}
	// Both windows operate on authorized eligible occurrences only. The maximum
	// spreads repeated content and long messages across later candidate rounds.
	// Text is loaded only after the bounded ID selection; window sorting itself
	// still depends on the number of matching occurrences.
	// Drive the full query from matching unique text IDs. Starting from a large
	// project and testing FTS membership afterward scans unrelated history.
	query := `WITH eligible AS (
 SELECT c.id,c.request_id,c.side,c.pointer,c.role,c.text_id,s.event_ns,
 COALESCE(m.kind,CASE WHEN c.role='tool' THEN 'tool_result' ELSE 'text' END) AS kind,
 COALESCE(m.tool_name,'') AS tool_name,
 CASE WHEN COALESCE(m.kind,'') IN ('tool_use','tool_result') OR c.role='tool' THEN 2 WHEN COALESCE(m.kind,'')='summary' THEN 1 ELSE 0 END AS kind_rank
 FROM chunk_search f CROSS JOIN chunks c ON c.text_id=f.docid JOIN sources s ON s.request_id=c.request_id
 LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer
 WHERE ` + strings.Join(where, " AND ") + `),
 rounds AS (
 SELECT *,
 ROW_NUMBER() OVER (PARTITION BY text_id,role,kind,tool_name ORDER BY event_ns DESC,request_id,id) AS content_round,
 ROW_NUMBER() OVER (PARTITION BY request_id,side,pointer ORDER BY id) AS message_round
 FROM eligible),
 candidates AS MATERIALIZED (
 SELECT id,request_id,event_ns,kind_rank,MAX(content_round,message_round) AS round FROM rounds
 ORDER BY round,kind_rank,event_ns DESC,request_id,id LIMIT ` + strconv.Itoa(limit) + `)
 SELECT s.request_id,s.revision,s.project_id,s.client,s.session_id,s.event_at,s.partial,c.side,c.pointer,c.role,c.start,c.end,t.text,
 COALESCE(m.kind,CASE WHEN c.role='tool' THEN 'tool_result' ELSE 'text' END),COALESCE(m.tool_name,''),COALESCE(m.tool_call_id,''),COALESCE(m.model,''),COALESCE(m.message_at,'')
 FROM candidates x JOIN chunks c ON c.id=x.id JOIN sources s ON s.request_id=x.request_id JOIN chunk_texts t ON t.id=c.text_id
 LEFT JOIN message_meta m ON m.request_id=c.request_id AND m.side=c.side AND m.pointer=c.pointer
 ORDER BY x.round,x.kind_rank,x.event_ns DESC,x.request_id,x.id`
	if recent {
		var matches int
		if err := e.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT docid FROM chunk_search WHERE tokens MATCH ? LIMIT 1024)`, strings.Join(tokens, " ")).Scan(&matches); err != nil {
			return out, err
		}
		if matches < 1024 {
			return out, nil
		}
		sourceFilter := ""
		if len(sourceWhere) > 0 {
			sourceFilter = " WHERE " + strings.Join(sourceWhere, " AND ")
		}
		query = strings.Replace(query, `WITH eligible AS (`, `WITH recent_sources AS MATERIALIZED (SELECT request_id,event_ns,project_id,client,session_id FROM sources s`+sourceFilter+` ORDER BY event_ns DESC,request_id LIMIT 1024), eligible AS (`, 1)
		args = append(append([]any(nil), sourceArgs...), args...)
		query = strings.Replace(query, `FROM chunk_search f CROSS JOIN chunks c ON c.text_id=f.docid JOIN sources s ON s.request_id=c.request_id`, `FROM recent_sources s CROSS JOIN chunks c ON c.request_id=s.request_id`, 1)
		query = strings.Replace(query, `f.tokens MATCH ?`, `c.text_id IN (SELECT docid FROM chunk_search WHERE tokens MATCH ?)`, 1)
		query = strings.Replace(query, `FROM rounds`+"\n"+` ORDER BY`, `FROM rounds WHERE content_round=1 AND message_round=1 AND kind_rank=0`+"\n"+` ORDER BY`, 1)
	}
	rows, err := e.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	candidates := []candidate{}
	first := rows.Next()
	for next := first; next; next = rows.Next() {
		var c candidate
		if err = rows.Scan(&c.hit.Citation.RequestID, &c.hit.Citation.Revision, &c.hit.ProjectID, &c.hit.Client, &c.hit.SessionID, &c.hit.EventAt, &c.hit.Partial, &c.hit.Citation.Side, &c.hit.Citation.Pointer, &c.hit.Role, &c.hit.Citation.Start, &c.hit.Citation.End, &c.text, &c.hit.Kind, &c.hit.ToolName, &c.hit.ToolCallID, &c.hit.Model, &c.hit.MessageAt); err != nil {
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
	// Close the projection snapshot before performing live archive validations.
	return candidates, nil
}
