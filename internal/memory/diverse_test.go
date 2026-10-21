package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
)

func diverseFixture(t *testing.T, e *Engine, raw *archive.Store, records []archive.Record) {
	t.Helper()
	for start := 0; start < len(records); start += 64 {
		if err := raw.PutBatch(records[start:min(start+64, len(records))]); err != nil {
			t.Fatal(err)
		}
	}
	for step := 0; step < 10000; step++ {
		n, err := e.Sync(context.Background(), 256)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	t.Fatal("diverse fixture did not converge")
}

func checkDiverseCitations(t *testing.T, e *Engine, result SearchResult) {
	t.Helper()
	for _, hit := range result.Hits {
		evidence, err := e.ReadSource(context.Background(), hit.Citation)
		if err != nil || evidence.Text != hit.Snippet || evidence.ProjectID != hit.ProjectID || evidence.SessionID != hit.SessionID || evidence.Client != hit.Client {
			t.Fatalf("diverse citation mismatch: %s: %v", hit.Citation.RequestID, err)
		}
	}
}

func TestDiverseReachesOlderEvidenceBehindMoreThanThousandRepeats(t *testing.T) {
	e, raw, _ := testEngine(t)
	records := []archive.Record{sample("older-answer", "s", "p", "diverseneedle accepted independent decision")}
	for i := 0; i < 1100; i++ {
		r := sample(fmt.Sprintf("repeat-%04d", i), "s", "p", "diverseneedle repeated reminder without a decision")
		r.StartedAt = r.StartedAt.Add(time.Duration(i+1) * time.Second)
		records = append(records, r)
	}
	diverseFixture(t, e, raw, records)
	q := Query{Text: "diverseneedle", ProjectID: "p", Limit: 5}
	literal, err := e.Search(context.Background(), q)
	if err != nil || len(literal.Hits) != 5 || literal.Hits[0].Citation.RequestID != "repeat-1099" {
		t.Fatalf("literal baseline changed: %d hits, %v", len(literal.Hits), err)
	}
	q.Mode = "literal"
	explicit, err := e.Search(context.Background(), q)
	if err != nil || !reflect.DeepEqual(literal, explicit) {
		t.Fatal("explicit literal mode changed default results", err)
	}
	q.Mode = "diverse"
	result, err := e.Search(context.Background(), q)
	if err != nil || len(result.Hits) != 2 || result.Hits[0].Citation.RequestID != "repeat-1099" || result.Hits[1].Citation.RequestID != "older-answer" || !result.Truncated {
		t.Fatalf("repeated contexts hid older evidence: %+v, %v", result, err)
	}
	checkDiverseCitations(t, e, result)
}

func TestDiverseSpreadsMoreThanThousandLongMessageWindows(t *testing.T) {
	e, raw, _ := testEngine(t)
	var body strings.Builder
	for i := 0; i < 1100; i++ {
		prefix := fmt.Sprintf("windowneedle window%04d ", i)
		body.WriteString(prefix)
		body.WriteString(strings.Repeat("x", 1344-len(prefix)))
	}
	long := sample("long-message", "s", "p", body.String())
	long.StartedAt = long.StartedAt.Add(time.Hour)
	diverseFixture(t, e, raw, []archive.Record{sample("independent", "s", "p", "windowneedle independent older evidence"), long})
	var windows int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE request_id='long-message'`).Scan(&windows); err != nil || windows <= 1000 {
		t.Fatalf("fixture lacks window pressure: %d %v", windows, err)
	}
	literal, err := e.Search(context.Background(), Query{Text: "windowneedle", ProjectID: "p", Limit: 5})
	if err != nil || len(literal.Hits) != 1 || literal.Hits[0].Citation.RequestID != "long-message" {
		t.Fatal("long-window literal baseline changed", err)
	}
	result, err := e.Search(context.Background(), Query{Text: "windowneedle", ProjectID: "p", Mode: "diverse", Limit: 5})
	if err != nil || len(result.Hits) != 2 || result.Hits[1].Citation.RequestID != "independent" || !result.Truncated {
		t.Fatalf("long windows hid independent evidence: %+v %v", result, err)
	}
	checkDiverseCitations(t, e, result)
}

func TestDiverseFallsBackPastDeletedReplacedAndMovedContentLeaders(t *testing.T) {
	for _, mutation := range []string{"delete", "replace", "move"} {
		t.Run(mutation, func(t *testing.T) {
			e, raw, _ := testEngine(t)
			older := sample("current-older", "s", "p", "fallbackneedle identical evidence")
			leader := sample("newest-leader", "s", "p", "fallbackneedle identical evidence")
			leader.StartedAt = leader.StartedAt.Add(time.Hour)
			diverseFixture(t, e, raw, []archive.Record{older, leader})
			switch mutation {
			case "delete", "replace":
				if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id=?`, leader.RequestID); err != nil {
					t.Fatal(err)
				}
				if mutation == "replace" {
					leader.OriginalRequest = []byte(`{"messages":[{"role":"user","content":"unrelated replacement"}]}`)
					if err := raw.PutBatch([]archive.Record{leader}); err != nil {
						t.Fatal(err)
					}
				}
			case "move":
				if _, err := raw.DB.Exec(`UPDATE records SET facets_json='{"project.id":["other"],"client":["codex"]}' WHERE request_id=?`, leader.RequestID); err != nil {
					t.Fatal(err)
				}
			}
			result, err := e.Search(context.Background(), Query{Text: "fallbackneedle", ProjectID: "p", Mode: "diverse", Limit: 5})
			if err != nil || len(result.Hits) != 1 || result.Hits[0].Citation.RequestID != older.RequestID {
				t.Fatalf("invalid content leader suppressed current fallback: %+v %v", result, err)
			}
			checkDiverseCitations(t, e, result)
		})
	}
}

func TestDiverseReplenishesShortPrefixAfterLiteralAndRevisionFailures(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale_%t", stale), func(t *testing.T) {
			e, raw, _ := testEngine(t)
			records := []archive.Record{sample("supported-answer", "s", "p", "error/code accepted remedy")}
			for i := 0; i < 24; i++ {
				text := fmt.Sprintf("error code unconfirmed%02d", i)
				if stale {
					text = fmt.Sprintf("error/code obsolete%02d", i)
				}
				r := sample(fmt.Sprintf("leader-%02d", i), "s", "p", text)
				r.StartedAt = r.StartedAt.Add(time.Duration(i+1) * time.Second)
				records = append(records, r)
			}
			// Extra matching texts enable the recent-prefix probe without adding
			// authorized occurrences to the actual result set.
			for i := 0; i < 1024; i++ {
				records = append(records, sample(fmt.Sprintf("foreign-%04d", i), "s", "foreign", fmt.Sprintf("error code foreign%04d", i)))
			}
			diverseFixture(t, e, raw, records)
			if stale {
				for _, record := range records[1:25] {
					if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id=?`, record.RequestID); err != nil {
						t.Fatal(err)
					}
				}
			}
			q, err := validateQuery(Query{Text: "error/code", ProjectID: "p", Mode: "diverse", Limit: 1}, false)
			if err != nil {
				t.Fatal(err)
			}
			prefix, err := e.selectDiverseCandidates(context.Background(), q, 16, true)
			if err != nil || len(prefix) != 16 {
				t.Fatalf("fixture did not exercise short prefix: %d %v", len(prefix), err)
			}
			short, err := e.finishSearch(context.Background(), q, prefix)
			if err != nil || len(short.Hits) != 0 {
				t.Fatal("fixture leaders unexpectedly passed final checks", err)
			}
			result, err := e.Search(context.Background(), q)
			if err != nil || len(result.Hits) != 1 || result.Hits[0].Citation.RequestID != "supported-answer" {
				t.Fatalf("short prefix was mistaken for exhaustion: %+v %v", result, err)
			}
			checkDiverseCitations(t, e, result)
		})
	}
}

func TestDiverseFastPrefixMatchesFullAndFallsBackForOlderMatches(t *testing.T) {
	e, raw, _ := testEngine(t)
	records := make([]archive.Record, 1100)
	for i := range records {
		r := sample(fmt.Sprintf("eligible-%04d", i), "selected", "p", fmt.Sprintf("prefixneedle independent%04d", i))
		r.StartedAt = r.StartedAt.Add(time.Duration(i) * time.Second)
		records[i] = r
	}
	for _, kind := range []string{"project", "session", "client", "before"} {
		r := sample("excluded-"+kind, "selected", "p", "prefixneedle excluded "+kind)
		r.StartedAt = r.StartedAt.Add(1500 * time.Second)
		switch kind {
		case "project":
			r.Facets["project.id"] = []string{"foreign"}
		case "session":
			r.SessionID = "different"
		case "client":
			r.Facets["client"] = []string{"other-client"}
		case "before":
			r.StartedAt = r.StartedAt.Add(time.Hour)
		}
		records = append(records, r)
	}
	diverseFixture(t, e, raw, records)
	base := records[0].StartedAt
	q := Query{Text: "prefixneedle", ProjectID: "p", SessionID: "selected", Client: "codex", After: base.Add(-time.Second).Format(time.RFC3339Nano), Before: base.Add(2000 * time.Second).Format(time.RFC3339Nano), Mode: "diverse", Limit: 5}
	check := func(t *testing.T, wantFast bool) {
		t.Helper()
		fast, err := e.selectDiverseCandidates(context.Background(), q, 16, true)
		if err != nil || (len(fast) >= q.Limit) != wantFast {
			t.Fatalf("unexpected fast prefix size: %d %v", len(fast), err)
		}
		full, err := e.selectDiverseCandidates(context.Background(), q, 1001, false)
		if err != nil {
			t.Fatal(err)
		}
		want, err := e.finishSearch(context.Background(), q, full)
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.Search(context.Background(), q)
		if err != nil || !reflect.DeepEqual(got.Hits, want.Hits) || len(got.Hits) != 5 {
			t.Fatalf("fast/fallback prefix changed scoped global ordering: %+v %v", got, err)
		}
		for i, hit := range got.Hits {
			if hit.Citation.RequestID != fmt.Sprintf("eligible-%04d", 1099-i) {
				t.Fatalf("unexpected scoped candidate %d: %s", i, hit.Citation.RequestID)
			}
		}
		checkDiverseCitations(t, e, got)
	}
	t.Run("complete_scoped_recent_prefix", func(t *testing.T) { check(t, true) })
	// These authorized newer sources have no matching chunks. A short/empty
	// recent result must replenish globally rather than hiding old evidence.
	if _, err := e.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1030)
 INSERT INTO sources(request_id,revision,sequence,project_id,client,session_id,event_at,event_ns,partial,issues_text)
 SELECT 'padding-'||x,'fixture',0,'p','codex','selected','',?+x,0,'' FROM n`, base.Add(1500*time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	t.Run("short_recent_prefix_falls_back", func(t *testing.T) { check(t, false) })
	newest := sample("recent-only", "selected", "p", "prefixneedle newest independent evidence")
	newest.StartedAt = base.Add(1600 * time.Second)
	diverseFixture(t, e, raw, []archive.Record{newest})
	t.Run("nonempty_short_recent_prefix_also_falls_back", func(t *testing.T) {
		fast, err := e.selectDiverseCandidates(context.Background(), q, 16, true)
		if err != nil || len(fast) != 1 {
			t.Fatalf("fixture did not create a nonempty incomplete prefix: %d %v", len(fast), err)
		}
		full, err := e.selectDiverseCandidates(context.Background(), q, 1001, false)
		if err != nil {
			t.Fatal(err)
		}
		want, err := e.finishSearch(context.Background(), q, full)
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.Search(context.Background(), q)
		if err != nil || len(got.Hits) != 5 || !reflect.DeepEqual(got.Hits, want.Hits) || got.Hits[0].Citation.RequestID != newest.RequestID {
			t.Fatalf("nonempty short prefix hid older independent evidence: %+v %v", got, err)
		}
		checkDiverseCitations(t, e, got)
	})
}

func TestDiversePreservesRoleToolAndPointerAttribution(t *testing.T) {
	e, raw, _ := testEngine(t)
	r := sample("typed", "s", "p", "unused")
	r.OriginalRequest = []byte(`{"messages":[{"role":"user","content":"typedneedle answer"},{"role":"assistant","content":"typedneedle answer"},{"role":"tool","content":"typedneedle answer"},{"role":"assistant","content":"typedneedle answer"},{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"shell_a","arguments":"typedneedle answer"}},{"id":"b","type":"function","function":{"name":"shell_b","arguments":"typedneedle answer"}}]},{"role":"system","content":"typedneedle forbidden system"},{"role":"developer","content":"typedneedle forbidden developer"}]}`)
	diverseFixture(t, e, raw, []archive.Record{r})
	literal, err := e.Search(context.Background(), Query{Text: "typedneedle", ProjectID: "p", Limit: 10})
	if err != nil || len(literal.Hits) != 6 {
		t.Fatalf("literal occurrence attribution changed: %d %v", len(literal.Hits), err)
	}
	result, err := e.Search(context.Background(), Query{Text: "typedneedle", ProjectID: "p", Mode: "diverse", Limit: 10})
	if err != nil || len(result.Hits) != 5 {
		t.Fatalf("typed excerpts incorrectly merged: %d %v", len(result.Hits), err)
	}
	roles, tools, pointers := map[string]int{}, map[string]bool{}, map[string]bool{}
	for _, hit := range result.Hits {
		roles[hit.Role]++
		if hit.ToolName != "" {
			tools[hit.ToolName] = true
		}
		if pointers[hit.Citation.Pointer] {
			t.Fatal("diverse output reused a message pointer")
		}
		pointers[hit.Citation.Pointer] = true
	}
	if roles["user"] != 1 || roles["assistant"] != 3 || roles["tool"] != 1 || !tools["shell_a"] || !tools["shell_b"] {
		t.Fatalf("role/tool evidence lost attribution: %v %v", roles, tools)
	}
	checkDiverseCitations(t, e, result)
}

func TestDiverseValidationAndCancellation(t *testing.T) {
	e, raw, _ := testEngine(t)
	diverseFixture(t, e, raw, []archive.Record{sample("r", "s", "p", "needle punctuation !!!")})
	for _, q := range []Query{{Text: "!!!", Mode: "diverse"}, {Text: "needle", Mode: "unknown"}} {
		if _, err := e.Search(context.Background(), q); !errors.Is(err, ErrInvalidQuery) {
			t.Fatal("invalid diverse query accepted", err)
		}
	}
	if _, err := e.Recap(context.Background(), Query{ProjectID: "p", Mode: "diverse"}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal("empty diverse recap accepted", err)
	}
	if recap, err := e.Recap(context.Background(), Query{ProjectID: "p"}); err != nil || len(recap.Hits) != 1 {
		t.Fatal("default empty recap changed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Search(ctx, Query{Text: "needle", Mode: "diverse"}); !errors.Is(err, context.Canceled) || errors.Is(err, ErrQueryBudget) {
		t.Fatal("parent cancellation was hidden as a budget error", err)
	}
}

func TestDiverseBudgetIncludesConnectionWait(t *testing.T) {
	e, _, _ := testEngine(t)
	var held []*sql.Conn
	for i := 0; i < 4; i++ {
		connection, err := e.readDB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, connection)
	}
	defer func() {
		for _, connection := range held {
			connection.Close()
		}
	}()
	start := time.Now()
	result, err := e.Search(context.Background(), Query{Text: "needle", Mode: "diverse", Limit: 5})
	if !errors.Is(err, ErrQueryBudget) || len(result.Hits) != 0 || time.Since(start) > 8*time.Second {
		t.Fatalf("pool wait escaped explicit query budget: elapsed=%s err=%v", time.Since(start), err)
	}
}
