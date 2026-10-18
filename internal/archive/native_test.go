package archive

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/nativehistory"
)

func TestNativeHistoryTimelineAndFullUserText(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	env := nativehistory.Envelope{Version: 1, Client: "codex", Context: nativehistory.Context{CodexResponses: true}, Lines: []nativehistory.Line{{Raw: `{"type":"response_item","payload":{"type":"message","role":"user","content":"complete native user message"}}`}, {Raw: `{"type":"response_item","payload":{"type":"message","role":"assistant","content":"native answer"}}`}}}
	body, _ := json.Marshal(env)
	stamp := time.Now().UTC()
	if err := store.PutBatch([]Record{{RequestID: "n", SessionID: "s", SourceFormat: nativehistory.Format, OriginalRequest: body, StartedAt: stamp, CompletedAt: stamp, Summary: "complete native", Facets: map[string][]string{"request.kind": {"native-history"}}}}); err != nil {
		t.Fatal(err)
	}
	view, err := store.RequestTimeline(context.Background(), "n")
	if err != nil || view.Kind != "native-history" || len(view.Entries) != 2 {
		t.Fatalf("timeline %+v %v", view, err)
	}
	groups, err := store.sessionTurnGroups(context.Background(), "s")
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups %+v %v", groups, err)
	}
	value, err := store.BuildTurnText(context.Background(), "s", groups[0].summary.TurnID)
	if err != nil || !strings.Contains(value, "complete native user message") {
		t.Fatalf("user text %q %v", value, err)
	}
}

func TestNativePayloadRetainsEnvelopeBytes(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	raw := []byte(`{ "version":1, "client":"codex", "lines":[{"offset":0,"raw":"{\"type\":\"response_item\",\"payload\":{\"encrypted_content\":\"retained\"}}\n"}] }`)
	stamp := time.Now().UTC()
	if err := store.PutBatch([]Record{{RequestID: "native", SessionID: "s", SourceFormat: nativehistory.Format, OriginalRequest: raw, StartedAt: stamp, CompletedAt: stamp}}); err != nil {
		t.Fatal(err)
	}
	record, err := store.Request(context.Background(), "native")
	if err != nil || string(record.OriginalRequest) != string(raw) {
		t.Fatal("native envelope bytes changed", err)
	}
}
