package recall

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cpa-session-archive/internal/memory"
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
