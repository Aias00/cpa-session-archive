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
		r := sample(fmt.Sprintf("candidate-%04d", i), "s", "p", strings.Repeat("marker payload ", 200))
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
