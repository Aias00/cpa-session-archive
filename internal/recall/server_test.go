package recall

import (
	"context"
	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/storage"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfficialMCPClientSearchReadRecapAndScope(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	raw, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.DB.Close()
	err = raw.PutBatch([]archive.Record{
		{RequestID: "a", SessionID: "s", Facets: map[string][]string{"project.id": {"p"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"Remember scoped evidence"}]}`)},
		{RequestID: "b", SessionID: "s", Facets: map[string][]string{"project.id": {"p"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"Remember scoped evidence"}]}`)},
		{RequestID: "c", SessionID: "s", Facets: map[string][]string{"project.id": {"p"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"Remember distinct scoped evidence"}]}`)},
		{RequestID: "outside", SessionID: "s", Facets: map[string][]string{"project.id": {"other"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"Remember scoped evidence"}]}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := memory.Open(filepath.Join(dir, "memory.sqlite"), storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for {
		n, err := engine.Sync(ctx, 8)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	handler, err := memory.NewHandlerWithCredentials(engine, []memory.Credential{{Token: strings.Repeat("x", 40), Scope: memory.Scope{ProjectID: "p"}}})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	reader, err := NewClient(httpServer.URL, strings.Repeat("x", 40))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	server := NewServer(reader)
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "recall-regression", Version: "1"}, nil)
	session, err := client.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 4 {
		t.Fatalf("%+v %v", tools, err)
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal("tool not read only", tool.Name)
		}
		if tool.Name == "archive_search" || tool.Name == "project_recap" {
			body, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(body, &schema); err != nil || strings.Join(schema.Properties["mode"].Enum, ",") != ",literal,diverse,hybrid" {
				t.Fatalf("%s retrieval mode schema: %s %v", tool.Name, body, err)
			}
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "archive_search", Arguments: map[string]any{"text": "evidence"}})
	if err != nil || result.IsError {
		t.Fatalf("%+v %v", result, err)
	}
	body, _ := json.Marshal(result.StructuredContent)
	var search memory.SearchResult
	if err := json.Unmarshal(body, &search); err != nil || len(search.Hits) != 3 {
		t.Fatalf("%s %v", body, err)
	}
	for _, name := range []string{"archive_search", "project_recap"} {
		for _, mode := range []string{"literal", "diverse"} {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"text": "evidence", "mode": mode}})
			if err != nil || result.IsError {
				t.Fatalf("%s mode %s: %+v %v", name, mode, result, err)
			}
			body, _ := json.Marshal(result.StructuredContent)
			var resultSearch memory.SearchResult
			expected := 3
			if mode == "diverse" {
				expected = 2
			}
			if err := json.Unmarshal(body, &resultSearch); err != nil || len(resultSearch.Hits) != expected {
				t.Fatalf("%s mode %s: %s %v", name, mode, body, err)
			}
			for _, hit := range resultSearch.Hits {
				if hit.ProjectID != "p" || hit.Citation.RequestID == "outside" {
					t.Fatalf("%s mode %s escaped credential scope", name, mode)
				}
				if _, err := reader.ReadSource(ctx, hit.Citation); err != nil {
					t.Fatalf("%s mode %s citation: %v", name, mode, err)
				}
			}
		}
	}
	for _, call := range []*mcp.CallToolParams{{Name: "archive_read_source", Arguments: search.Hits[0].Citation}, {Name: "project_recap", Arguments: map[string]any{}}, {Name: "archive_status", Arguments: map[string]any{}}} {
		r, err := session.CallTool(ctx, call)
		if err != nil || r.IsError {
			t.Fatalf("%s: %+v %v", call.Name, r, err)
		}
	}
	for _, args := range []any{map[string]any{"text": "evidence", "project_id": "other"}, map[string]any{"text": "evidence", "limit": 101}, map[string]any{"text": "evidence", "mode": "unknown"}, map[string]any{"text": 3}} {
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "archive_search", Arguments: args})
		if err == nil && !r.IsError {
			t.Fatalf("invalid arguments accepted: %+v", args)
		}
	}
	for _, text := range []string{"", "///"} {
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "project_recap", Arguments: map[string]any{"text": text, "mode": "diverse"}})
		if err == nil && !r.IsError {
			t.Fatalf("diverse recap accepted non-searchable text %q", text)
		}
	}
}
