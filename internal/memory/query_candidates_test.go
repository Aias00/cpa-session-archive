package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
)

func TestSearchCandidateBoundPreservesOrderScopeAndCitations(t *testing.T) {
	e, raw, _ := testEngine(t)
	records := make([]archive.Record, 1100)
	for i := range records {
		r := sample(fmt.Sprintf("candidate-%04d", i), "s", "p", fmt.Sprintf("marker unique%04d ", i)+strings.Repeat("marker payload ", 200))
		r.StartedAt = r.StartedAt.Add(time.Duration(i) * time.Second)
		records[i] = r
	}
	other := sample("foreign", "s", "other", "marker")
	other.StartedAt = records[len(records)-1].StartedAt.Add(time.Hour)
	records = append(records, other)
	if err := raw.PutBatch(records); err != nil {
		t.Fatal(err)
	}
	for {
		n, err := e.Sync(context.Background(), 256)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	var uniqueMatches int
	if err := e.readDB.QueryRow(`SELECT COUNT(*) FROM chunk_search WHERE tokens MATCH ?`, strings.Join(lexicalTokens("marker"), " ")).Scan(&uniqueMatches); err != nil || uniqueMatches < 1024 {
		t.Fatalf("fixture must exercise broad-query admission: matches=%d err=%v", uniqueMatches, err)
	}
	check := func(t *testing.T) {
		t.Helper()
		result, err := e.Search(context.Background(), Query{Text: "marker", ProjectID: "p", Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Truncated || len(result.Hits) != 20 {
			t.Fatalf("unexpected bounded result: hits=%d truncated=%v", len(result.Hits), result.Truncated)
		}
		for i, hit := range result.Hits {
			want := fmt.Sprintf("candidate-%04d", 1099-i)
			if hit.Citation.RequestID != want || hit.ProjectID != "p" {
				t.Fatalf("hit %d: got %s/%s, want %s/p", i, hit.Citation.RequestID, hit.ProjectID, want)
			}
			evidence, err := e.ReadSource(context.Background(), hit.Citation)
			if err != nil || evidence.Text != hit.Snippet {
				t.Fatalf("candidate citation no longer resolves: %v", err)
			}
		}
	}
	t.Run("complete_recent_prefix", check)
	// Newer sources without matching chunks force the incomplete-prefix fallback.
	if _, err := e.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<4100)
 INSERT INTO sources(request_id,revision,sequence,project_id,client,session_id,event_at,event_ns,partial,issues_text)
 SELECT 'padding-'||x,'fixture',0,'p','','padding','',9223372036854775807-x,0,'' FROM n`); err != nil {
		t.Fatal(err)
	}
	t.Run("older_matches_fall_back", check)
	if _, err := e.db.Exec(`DELETE FROM sources WHERE session_id='padding'; UPDATE message_meta SET kind='summary'`); err != nil {
		t.Fatal(err)
	}
	t.Run("summary_rank_falls_back", check)
}
