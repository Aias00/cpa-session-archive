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
	raw.PutBatch([]archive.Record{{RequestID: "a", SessionID: "s", Facets: map[string][]string{"project.id": {"p"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"Remember scoped evidence"}]}`)}})
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
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "archive_search", Arguments: map[string]any{"text": "evidence"}})
	if err != nil || result.IsError {
		t.Fatalf("%+v %v", result, err)
	}
	body, _ := json.Marshal(result.StructuredContent)
	var search memory.SearchResult
	if err := json.Unmarshal(body, &search); err != nil || len(search.Hits) != 1 {
		t.Fatalf("%s %v", body, err)
	}
	for _, call := range []*mcp.CallToolParams{{Name: "archive_read_source", Arguments: search.Hits[0].Citation}, {Name: "project_recap", Arguments: map[string]any{}}, {Name: "archive_status", Arguments: map[string]any{}}} {
		r, err := session.CallTool(ctx, call)
		if err != nil || r.IsError {
			t.Fatalf("%s: %+v %v", call.Name, r, err)
		}
	}
	for _, args := range []any{map[string]any{"text": "evidence", "project_id": "other"}, map[string]any{"text": "evidence", "limit": 101}, map[string]any{"text": 3}} {
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "archive_search", Arguments: args})
		if err == nil && !r.IsError {
			t.Fatalf("invalid arguments accepted: %+v", args)
		}
	}
}
