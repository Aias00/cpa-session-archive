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

func TestCoverageDoesNotTreatAmbiguityAsIntentional(t *testing.T) {
	for _, reason := range []string{"opaque or unsupported Codex response item omitted", "opaque SSE content omitted", "malformed or auxiliary native record retained", "Kiro metadata or unsupported event retained without indexing", "auxiliary native source retained without indexing"} {
		if got := classifyCoverageIssue(reason); got.Category != "ambiguous" {
			t.Fatalf("%q was mislabeled: %+v", reason, got)
		}
	}
	unknown := "new diagnostic containing private text"
	got := classifyCoverage([]string{unknown, unknown, "missing response", "missing original_request"})
	want := []CoverageIssue{{Code: "missing_payload", Category: "missing"}, {Code: "unclassified", Category: "unknown"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("classification leaked detail or double counted a reason: %+v", got)
	}
}

func TestCoverageDistinguishesKnownExclusionsWithoutChangingMessages(t *testing.T) {
	e := nativehistory.Envelope{Version: 1, Client: "codex", Lines: []nativehistory.Line{
		{Raw: `{"type":"response_item","payload":{"type":"reasoning","encrypted_content":"opaque"}}`},
		{Raw: `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"retained evidence"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}}`},
	}}
	payload, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	r := sample("native", "s", "p", "")
	r.SourceFormat, r.OriginalRequest = nativehistory.Format, payload
	doc := Normalize(r)
	if len(doc.Messages) != 1 || doc.Messages[0].Text != "retained evidence" || doc.Messages[0].Pointer != "/lines/1/raw/payload/content/0" || NormalizerVersion != 2 {
		t.Fatalf("text/citation semantics changed: %+v", doc)
	}
	for _, issue := range classifyCoverage(doc.Issues) {
		if issue.Category != "intentional" {
			t.Fatalf("known exclusions remained ambiguous: %+v", issue)
		}
	}
}

func TestCoverageAggregatesMigrateUpdateRollbackAndDelete(t *testing.T) {
	e, raw, path := testEngine(t)
	complete := sample("complete", "s", "p", "retained evidence")
	complete.Response = []byte(`{"choices":[{"message":{"role":"assistant","content":"answer"}}]}`)
	excluded := complete
	excluded.RequestID = "excluded"
	excluded.OriginalRequest = []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`)
	missing := complete
	missing.RequestID, missing.OriginalRequest, missing.Response = "missing", nil, nil
	if err := raw.PutBatch([]archive.Record{complete, excluded, missing}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	check := func(engine *Engine, sources, expected, investigate int64) {
		t.Helper()
		s, err := engine.Status(context.Background())
		if err != nil || s.Coverage == nil {
			t.Fatalf("status failed: %v", err)
		}
		c := s.Coverage
		if c.SourcesWithIssues != sources || c.ExpectedOmissionsOnlySources != expected || c.NeedsInvestigationSources != investigate {
			t.Fatalf("incorrect coverage totals: %+v", c)
		}
	}
	check(e, 2, 1, 1)
	// Legacy writers update stored diagnostics without changing text rows.
	if _, err := e.db.Exec(`UPDATE sources SET issues_text='opaque or unsupported Codex response item omitted' WHERE request_id='excluded'`); err != nil {
		t.Fatal(err)
	}
	check(e, 2, 0, 2)
	if _, err := e.db.Exec(`UPDATE sources SET issues_text='' WHERE request_id='excluded'`); err != nil {
		t.Fatal(err)
	}
	check(e, 1, 0, 2)
	status, err := e.Status(context.Background())
	if err != nil || status.Coverage.UnexplainedPartialSources != 1 {
		t.Fatalf("partial source without explanation was hidden: %+v %v", status.Coverage, err)
	}
	if _, err := e.db.Exec(`UPDATE sources SET issues_text='opaque or unsupported Codex response item omitted' WHERE request_id='excluded'`); err != nil {
		t.Fatal(err)
	}
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM sources`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check(e, 2, 0, 2)
	for _, name := range []string{"coverage_sources_insert", "coverage_sources_delete", "coverage_sources_update"} {
		if _, err := e.db.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.db.Exec(`DROP TABLE coverage_groups; CREATE TRIGGER reject_chunk_rebuild BEFORE INSERT ON chunks BEGIN SELECT RAISE(FAIL,'unexpected FTS rebuild'); END;`); err != nil {
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
	check(reopened, 2, 0, 2)
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id IN ('excluded','missing')`); err != nil {
		t.Fatal(err)
	}
	syncAll(t, reopened)
	check(reopened, 0, 0, 0)
}
