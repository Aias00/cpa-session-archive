package memory

import (
	"context"
	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/nativehistory"
	"cpa-session-archive/internal/storage"
	"encoding/json"
	"testing"
)

func TestDialogueSearchPriorityAndContextCitation(t *testing.T) {
	e, raw, _ := testEngine(t)
	env := nativehistory.Envelope{Version: 1, Client: "claude-code", Lines: []nativehistory.Line{
		{Raw: `{"type":"user","isMeta":true,"message":{"role":"user","content":"needle context"}}`},
		{Raw: `{"type":"assistant","message":{"role":"assistant","model":"model-a","content":[{"type":"tool_use","id":"c","name":"shell","input":{"x":"needle"}}]}}`},
		{Raw: `{"type":"user","message":{"role":"user","content":"needle dialogue"}}`},
		{Raw: `{"type":"assistant","message":{"role":"assistant","content":"needle answer"}}`},
	}}
	body, _ := json.Marshal(env)
	r := sample("native-meta", "s", "p", "")
	r.SourceFormat = nativehistory.Format
	r.OriginalRequest = body
	if err := raw.PutBatch([]archive.Record{r}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	result, err := e.Search(context.Background(), Query{Text: "needle"})
	if err != nil || len(result.Hits) != 3 {
		t.Fatalf("hits %+v %v", result, err)
	}
	if result.Hits[0].Kind != "text" || result.Hits[1].Kind != "text" || result.Hits[2].Kind != "tool_use" || result.Hits[2].ToolName != "shell" {
		t.Fatalf("rank %+v", result.Hits)
	}
	source, _ := raw.MemorySource(context.Background(), r.RequestID)
	c := Citation{NormalizerVersion: 2, RequestID: r.RequestID, Revision: source.Revision, Side: "original_request", Pointer: "/lines/0/raw/message/content", Start: 0, End: 14}
	evidence, err := e.ReadSource(context.Background(), c)
	if err != nil || evidence.Text != "needle context" || evidence.Kind != "context" {
		t.Fatalf("old context citation %+v %v", evidence, err)
	}
}
func TestMetadataUpgradeDoesNotRebuildTextOrInvalidateCitations(t *testing.T) {
	e, raw, path := testEngine(t)
	r := sample("legacy", "s", "p", "legacy needle")
	raw.PutBatch([]archive.Record{r})
	syncAll(t, e)
	result, _ := e.Search(context.Background(), Query{Text: "needle"})
	citation := result.Hits[0].Citation
	var before int
	e.db.QueryRow("select count(*) from chunk_texts").Scan(&before)
	e.db.Exec("drop table message_meta")
	e.db.Exec("drop table message_meta_sources")
	e.db.Exec("drop table message_meta_state")
	e.Close()
	reopened, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	syncAll(t, reopened)
	var after int
	reopened.db.QueryRow("select count(*) from chunk_texts").Scan(&after)
	if before != after {
		t.Fatalf("text rebuilt %d %d", before, after)
	}
	evidence, err := reopened.ReadSource(context.Background(), citation)
	if err != nil || evidence.Text != "legacy needle" {
		t.Fatalf("citation %+v %v", evidence, err)
	}
	status, err := reopened.Status(context.Background())
	if err != nil || !status.MessageMetadataDone {
		t.Fatalf("metadata status %+v %v", status, err)
	}
	result, err = reopened.Search(context.Background(), Query{Text: "needle"})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Kind != "text" {
		t.Fatalf("metadata %+v %v", result, err)
	}
}

func TestHTTPToolMetadataAndBackfillSurvivesRestart(t *testing.T) {
	e, raw, path := testEngine(t)
	r := sample("http-tools", "s", "p", "")
	r.OriginalRequest = []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call","function":{"name":"shell","arguments":"{\"x\":\"needle\"}"}}]},{"role":"tool","tool_call_id":"call","content":"needle result"}]}`)
	raw.PutBatch([]archive.Record{r})
	syncAll(t, e)
	result, err := e.Search(context.Background(), Query{Text: "needle"})
	if err != nil || len(result.Hits) != 2 {
		t.Fatalf("hits %+v %v", result, err)
	}
	for _, hit := range result.Hits {
		if hit.Kind != "tool_use" && hit.Kind != "tool_result" {
			t.Fatalf("tool classified as dialogue %+v", hit)
		}
	}
	e.Close()
	reopened, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var done bool
	reopened.db.QueryRow("select done from message_meta_state").Scan(&done)
	if !done {
		t.Fatal("restart discarded durable metadata checkpoint")
	}
}

func TestMetadataReupgradeRepairsRowsWrittenByOldCollector(t *testing.T) {
	e, raw, path := testEngine(t)
	raw.PutBatch([]archive.Record{sample("a", "s", "p", "needle")})
	syncAll(t, e)
	e.db.Exec("delete from message_meta_sources where request_id='a'")
	e.Close()
	next, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	var done bool
	next.db.QueryRow("select done from message_meta_state").Scan(&done)
	if done {
		t.Fatal("missing metadata not queued after rollback")
	}
	syncAll(t, next)
	var n int
	next.db.QueryRow("select count(*) from message_meta_sources where request_id='a'").Scan(&n)
	if n != 1 {
		t.Fatal("missing metadata not restored")
	}
}
