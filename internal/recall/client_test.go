package recall

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cpa-session-archive/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestClientRefusesUnsafeEndpointsAndDoesNotForwardTokenOnRedirect(t *testing.T) {
	for _, base := range []string{"http://example.com:8319", "http://user:password@127.0.0.1:8319", "http://127.0.0.1:8319/?token=x", "http://127.0.0.1:8319/#x"} {
		if _, err := NewClient(base, strings.Repeat("t", 40)); err == nil {
			t.Fatal("unsafe endpoint accepted", base)
		}
	}
	forwarded := false
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer downstream.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, downstream.URL, 302) }))
	defer redirect.Close()
	client, err := NewClient(redirect.URL, strings.Repeat("t", 40))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
	if forwarded {
		t.Fatal("credential followed redirect")
	}
}

func TestClientTransportsRetrievalModeForSearchAndRecap(t *testing.T) {
	for _, path := range []string{"/v1/search", "/v1/recap"} {
		for _, mode := range []string{"", "literal", "diverse", "unknown"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					values := r.URL.Query()
					if r.URL.Path != path || values.Get("mode") != mode || values.Get("q") != "evidence" || values.Get("project_id") != "p" || values.Get("session_id") != "s" {
						t.Errorf("query mapping: %s", r.URL)
					}
					if mode == "" && values.Has("mode") {
						t.Error("default query added an explicit mode")
					}
					if mode == "unknown" {
						w.WriteHeader(http.StatusBadRequest)
					}
					_, _ = w.Write([]byte(`{"hits":[]}`))
				}))
				defer server.Close()
				client, err := NewClient(server.URL, strings.Repeat("x", 40))
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				query := memory.Query{Text: "evidence", ProjectID: "p", SessionID: "s", Mode: mode}
				if path == "/v1/search" {
					_, err = client.Search(context.Background(), query)
				} else {
					_, err = client.Recap(context.Background(), query)
				}
				if mode == "unknown" {
					if !errors.Is(err, memory.ErrInvalidQuery) {
						t.Fatalf("invalid mode error: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestQueryBudgetIsAnActionableErrorForClientAndMCP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "diverse" {
			t.Error("missing diverse mode")
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"private database detail","hits":[{"snippet":"private partial result"}],"text":"private partial packet"}`))
	}))
	defer server.Close()
	reader, err := NewClient(server.URL, strings.Repeat("x", 40))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx := context.Background()
	query := memory.Query{Text: "evidence", ProjectID: "p", Mode: "diverse"}
	search, err := reader.Search(ctx, query)
	if !errors.Is(err, memory.ErrQueryBudget) || len(search.Hits) != 0 {
		t.Fatalf("client search budget response: %+v %v", search, err)
	}
	recap, err := reader.Recap(ctx, query)
	if !errors.Is(err, memory.ErrQueryBudget) || len(recap.Hits) != 0 || recap.Text != "" {
		t.Fatalf("client recap budget response: %+v %v", recap, err)
	}

	a, b := mcp.NewInMemoryTransports()
	serverSession, err := NewServer(reader).Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "query-budget-regression", Version: "1"}, nil)
	session, err := client.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, name := range []string{"archive_search", "project_recap"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"text": "evidence", "project_id": "p", "mode": "diverse"}})
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("%s returned success for an exhausted budget: %+v %v", name, result, err)
		}
		body, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if result.StructuredContent != nil || !strings.Contains(string(body), memory.ErrQueryBudget.Error()) || strings.Contains(string(body), "private") {
			t.Fatalf("%s budget error exposed partial results or omitted recovery advice: %s", name, body)
		}
	}
}
func TestClientValidatesAuthBodyAndErrors(t *testing.T) {
	mode := "ok"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 40) {
			t.Error("missing credential")
		}
		if r.URL.Path != "/v1/search" || r.URL.Query().Get("q") != "中文 /tmp/a.go" || r.URL.Query().Get("project_id") != "p" {
			t.Error("wrong query mapping")
		}
		switch mode {
		case "ok":
			w.Write([]byte(`{"hits":[],"status":{"sources":2}}`))
		case "denied":
			w.WriteHeader(401)
			w.Write([]byte("secret internal detail"))
		case "oversize":
			w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, strings.Repeat("x", 40))
	if err != nil {
		t.Fatal(err)
	}
	q := memory.Query{Text: "中文 /tmp/a.go", ProjectID: "p"}
	result, err := client.Search(context.Background(), q)
	if err != nil || result.Status.Sources != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	for _, m := range []string{"denied", "oversize"} {
		mode = m
		if _, err := client.Search(context.Background(), q); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
}
