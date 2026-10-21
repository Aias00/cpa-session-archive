package memory

import (
	"context"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestIsInternalSourceUsesStructuredTagsOnly(t *testing.T) {
	for name, record := range map[string]archive.Record{
		"record":                {RequestKind: "memory-extraction"},
		"facet":                 {Facets: map[string][]string{"request.kind": {" MEMORY "}}},
		"metadata":              {Metadata: map[string]any{"request_kind": "memory-extraction"}},
		"body metadata":         {OriginalRequest: []byte(`{"metadata":{"request_kind":"memory-extraction"}}`)},
		"body client metadata":  {OriginalRequest: []byte(`{"client_metadata":{"request_kind":"memory"}}`)},
		"encoded turn metadata": {OriginalRequest: []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"memory-extraction\"}"}}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if !IsInternalSource(record) || !isInternal(record) {
				t.Fatal("structured internal tag was ignored")
			}
		})
	}
	ordinary := archive.Record{OriginalRequest: []byte(`{"messages":[{"role":"user","content":"Discuss request_kind=memory-extraction and the memory plugin."}]}`), Metadata: map[string]any{"notes": "memory-extraction"}, ThreadSource: "memory-extraction"}
	if IsInternalSource(ordinary) {
		t.Fatal("conversation text or thread provenance was treated as an internal tag")
	}
}

func TestRecallExcludesStructuredInternalRequestTags(t *testing.T) {
	engine, raw, _ := testEngine(t)
	ordinary := sample("ordinary", "s", "p", "structuredtagtoken")
	internal := sample("internal", "s", "p", "structuredtagtoken")
	internal.Metadata = map[string]any{"request_kind": "memory-extraction"}
	body := sample("body", "s", "p", "")
	body.OriginalRequest = []byte(`{"metadata":{"request_kind":"memory-extraction"},"input":"structuredtagtoken"}`)
	if err := raw.PutBatch([]archive.Record{ordinary, internal, body}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, engine)
	result, err := engine.Search(context.Background(), Query{Text: "structuredtagtoken", ProjectID: "p"})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Citation.RequestID != "ordinary" {
		t.Fatalf("internal model request entered recall: %v", err)
	}
}
