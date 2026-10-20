package memory

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestDiagnosticsAreScopedOnAllReadPaths(t *testing.T) {
	e, raw, _ := testEngine(t)
	if err := raw.PutBatch([]archive.Record{sample("one", "s", "p", "evidence")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	reader := WithIngestionStatus(e, func() IngestionStatus { return IngestionStatus{Epoch: "owner-only-epoch", ReceivedRequests: 42} })
	for _, scope := range []Scope{{AllowAll: true}, {ProjectID: "p"}, {SessionID: "s"}} {
		scoped, err := WithScope(reader, scope)
		if err != nil {
			t.Fatal(err)
		}
		status, err := scoped.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		search, err := scoped.Search(context.Background(), Query{Text: "evidence"})
		if err != nil {
			t.Fatal(err)
		}
		recap, err := scoped.Recap(context.Background(), Query{SessionID: "s"})
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range []Status{status, search.Status, recap.Status} {
			if scope.AllowAll {
				if got.Ingestion == nil || got.Ingestion.ReceivedRequests != 42 || got.Coverage == nil {
					t.Fatalf("owner diagnostics missing: %+v", got)
				}
			} else if got.Ingestion != nil || got.Coverage != nil {
				t.Fatalf("global diagnostics disclosed: %+v", got)
			}
		}
	}
	owner, limited := strings.Repeat("o", 48), strings.Repeat("p", 48)
	handler, err := NewHandlerWithCredentials(reader, []Credential{{Token: owner, Scope: Scope{AllowAll: true}}, {Token: limited, Scope: Scope{ProjectID: "p"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{owner, limited} {
		request := httptest.NewRequest("GET", "/v1/status", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatal(response.Code)
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		_, hasIngestion := data["ingestion"]
		_, hasCoverage := data["coverage"]
		if hasIngestion != (token == owner) || hasCoverage != (token == owner) {
			t.Fatal("HTTP scope disclosed global diagnostics")
		}
	}
}
