package memory

import (
	"context"
	"cpa-session-archive/internal/archive"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScopedRecallRejectsCrossProjectSourceAndQuery(t *testing.T) {
	e, raw, _ := testEngine(t)
	ctx := context.Background()
	raw.PutBatch([]archive.Record{sample("a", "s-a", "p", "shared evidence"), sample("b", "s-b", "q", "shared evidence")})
	syncAll(t, e)
	scoped, err := WithScope(e, Scope{ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := scoped.Search(ctx, Query{Text: "evidence"})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].ProjectID != "p" {
		t.Fatalf("%+v %v", result, err)
	}
	if !result.Status.ScopeFiltered || result.Status.Sources != 0 || result.Status.Chunks != 0 {
		t.Fatal("global counts disclosed", result.Status)
	}
	if _, err := scoped.Search(ctx, Query{Text: "evidence", ProjectID: "q"}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
	other, _ := e.Search(ctx, Query{Text: "evidence", ProjectID: "q"})
	if _, err := scoped.ReadSource(ctx, other.Hits[0].Citation); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal("forged cross-project citation accepted", err)
	}
	if _, err := scoped.ReadSource(ctx, result.Hits[0].Citation); err != nil {
		t.Fatal(err)
	}
	if _, err := WithScope(e, Scope{}); err == nil {
		t.Fatal("implicit owner-wide scope accepted")
	}
	session, err := WithScope(e, Scope{SessionID: "s-b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.ReadSource(ctx, result.Hits[0].Citation); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal("cross-session citation accepted", err)
	}
}
func TestSeparateHTTPReadCredentialsEnforceScope(t *testing.T) {
	e, raw, _ := testEngine(t)
	raw.PutBatch([]archive.Record{sample("a", "s-a", "p", "evidence"), sample("b", "s-b", "q", "evidence")})
	syncAll(t, e)
	owner := strings.Repeat("o", 48)
	limited := strings.Repeat("p", 48)
	handler, err := NewHandlerWithCredentials(e, []Credential{{Token: owner, Scope: Scope{AllowAll: true}}, {Token: limited, Scope: Scope{ProjectID: "p"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewHandlerWithCredentials(e, []Credential{{Token: limited, Scope: Scope{}}}); err == nil {
		t.Fatal("credential without scope accepted")
	}
	if _, err := NewHandlerWithCredentials(e, []Credential{{Token: owner, Scope: Scope{AllowAll: true}}, {Token: owner, Scope: Scope{ProjectID: "p"}}}); err == nil {
		t.Fatal("duplicate token accepted")
	}
	for _, c := range []struct {
		path, token string
		code        int
	}{{"/v1/search?q=evidence", limited, 200}, {"/v1/search?q=evidence&project_id=q", limited, 400}, {"/v1/status", owner, 200}, {"/v1/status", "invalid", 401}} {
		request := httptest.NewRequest(http.MethodGet, c.path, nil)
		request.Header.Set("Authorization", "Bearer "+c.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != c.code {
			t.Fatalf("%s: %d", c.path, response.Code)
		}
	}
}
