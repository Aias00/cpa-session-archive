package memory

import (
	"context"
	"encoding/json"
	"testing"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/nativehistory"
)

func TestNativeSourceSearchCitationAndNoMissingHTTPResponse(t *testing.T) {
	e, store, _ := testEngine(t)
	for _, client := range []string{"codex", "claude-code", "kiro-cli"} {
		line := `{"type":"response_item","payload":{"type":"message","role":"user","content":"native-needle"}}`
		if client == "claude-code" {
			line = `{"type":"user","message":{"role":"user","content":"native-needle"}}`
		}
		if client == "kiro-cli" {
			line = `{"kind":"Prompt","data":{"content":[{"kind":"text","data":"native-needle"}]}}`
		}
		env := nativehistory.Envelope{Version: 1, Client: client, Context: nativehistory.Context{CodexResponses: true}, Lines: []nativehistory.Line{{Raw: line}}}
		raw, _ := json.Marshal(env)
		r := sample(client, "native-session", "native-project", "")
		r.SourceFormat = nativehistory.Format
		r.OriginalRequest = raw
		r.Response = nil
		r.Facets["client"] = []string{client}
		if doc := Normalize(r); len(doc.Messages) != 1 || doc.Partial {
			t.Fatalf("native normalization %+v", doc)
		}
		if err := store.PutBatch([]archive.Record{r}); err != nil {
			t.Fatal(err)
		}
	}
	syncAll(t, e)
	for _, client := range []string{"codex", "claude-code", "kiro-cli"} {
		result, err := e.Search(context.Background(), Query{Text: "native-needle", Client: client})
		if err != nil || len(result.Hits) != 1 {
			t.Fatalf("%s %+v %v", client, result, err)
		}
		evidence, err := e.ReadSource(context.Background(), result.Hits[0].Citation)
		if err != nil || evidence.Text != "native-needle" || evidence.Partial {
			t.Fatalf("citation %+v %v", evidence, err)
		}
	}
}
