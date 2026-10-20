package memory

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/nativehistory"
	"cpa-session-archive/internal/storage"
)

func TestCoverageBackfillPreservesTextAndExistingMessageMetadata(t *testing.T) {
	e, raw, path := testEngine(t)
	envelope := nativehistory.Envelope{Version: 1, Client: "codex", Lines: []nativehistory.Line{
		{Raw: `{"type":"response_item","payload":{"type":"reasoning","encrypted_content":"opaque"}}`},
		{Raw: `{"type":"response_item","payload":{"type":"message","role":"user","content":"retained evidence"}}`},
	}}
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	record := sample("native", "s", "p", "")
	record.SourceFormat = nativehistory.Format
	record.OriginalRequest = payload
	if err := raw.PutBatch([]archive.Record{record}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	before, err := e.Search(context.Background(), Query{Text: "retained evidence", ProjectID: "p"})
	if err != nil || len(before.Hits) != 1 {
		t.Fatalf("initial search: %+v %v", before, err)
	}
	if _, err := e.db.Exec(`UPDATE sources SET issues_text='opaque or unsupported Codex response item omitted';
 ALTER TABLE message_meta_sources DROP COLUMN coverage_version;
 UPDATE message_meta_state SET done=1,after_id=0;
 CREATE TRIGGER reject_coverage_text_insert BEFORE INSERT ON chunks BEGIN SELECT RAISE(FAIL,'coverage rewrote text'); END;
 CREATE TRIGGER reject_coverage_message_insert BEFORE INSERT ON message_meta BEGIN SELECT RAISE(FAIL,'coverage rewrote message metadata'); END;
 CREATE TRIGGER reject_coverage_message_delete BEFORE DELETE ON message_meta BEGIN SELECT RAISE(FAIL,'coverage removed message metadata'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pending, err := reopened.Status(context.Background())
	if err != nil || pending.MessageMetadataDone || pending.Coverage.NeedsInvestigationSources != 1 {
		t.Fatalf("legacy metadata did not schedule refresh: %+v %v", pending, err)
	}
	syncAll(t, reopened)
	after, err := reopened.Search(context.Background(), Query{Text: "retained evidence", ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Hits, after.Hits) || !after.Status.MessageMetadataDone || after.Status.Coverage.ExpectedOmissionsOnlySources != 1 || after.Status.Coverage.NeedsInvestigationSources != 0 {
		t.Fatalf("coverage refresh changed evidence or stayed ambiguous: %+v", after)
	}
	evidence, err := reopened.ReadSource(context.Background(), before.Hits[0].Citation)
	if err != nil || evidence.Text != before.Hits[0].Snippet {
		t.Fatalf("old citation invalidated: %+v %v", evidence, err)
	}
}
