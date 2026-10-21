package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/ingeststatus"
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

func TestContextualDiagnosticsUseRequestContextAndOmitFailedObservations(t *testing.T) {
	e, raw, _ := testEngine(t)
	if err := raw.PutBatch([]archive.Record{sample("one", "s", "p", "evidence")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	fail := false
	calls := 0
	reader := WithIngestionStatusContext(e, func(got context.Context) (ingeststatus.Status, error) {
		calls++
		if got.Value(contextKey{}) != "request" {
			t.Error("diagnostics lost the request context")
		}
		if fail {
			return ingeststatus.Status{ReceivedRequests: 99}, errors.New("collector unavailable")
		}
		return ingeststatus.Status{Epoch: "fresh-observation", ReceivedRequests: 42}, nil
	})
	first, err := reader.Status(ctx)
	if err != nil || first.Ingestion == nil || first.Ingestion.ReceivedRequests != 42 {
		t.Fatalf("initial diagnostics: %+v %v", first, err)
	}
	fail = true
	status, err := reader.Status(ctx)
	if err != nil || status.Ingestion != nil {
		t.Fatalf("failed observation reused diagnostics: %+v %v", status, err)
	}
	search, err := reader.Search(ctx, Query{Text: "evidence"})
	if err != nil || len(search.Hits) != 1 || search.Status.Ingestion != nil {
		t.Fatalf("telemetry failure disrupted evidence: %+v %v", search, err)
	}
	recap, err := reader.Recap(ctx, Query{ProjectID: "p"})
	if err != nil || len(recap.Hits) != 1 || recap.Status.Ingestion != nil || calls != 4 {
		t.Fatalf("telemetry failure disrupted recap: calls=%d %+v %v", calls, recap, err)
	}
}

func TestContextualDiagnosticsRespectCancellation(t *testing.T) {
	e, _, _ := testEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	observed := make(chan struct{})
	finished := make(chan struct{})
	reader := WithIngestionStatusContext(e, func(got context.Context) (ingeststatus.Status, error) {
		close(observed)
		<-got.Done()
		return ingeststatus.Status{}, got.Err()
	})
	go func() {
		defer close(finished)
		_, _ = reader.Status(ctx)
	}()
	select {
	case <-observed:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("diagnostics callback was not reached")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("diagnostics ignored request cancellation")
	}
}

func TestDiagnosticTimeoutPreservesSuccessfulSearch(t *testing.T) {
	e, raw, _ := testEngine(t)
	if err := raw.PutBatch([]archive.Record{sample("one", "s", "p", "evidence")}); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	observed := make(chan error, 1)
	reader := WithIngestionStatusContext(e, func(ctx context.Context) (ingeststatus.Status, error) {
		<-ctx.Done()
		observed <- ctx.Err()
		return ingeststatus.Status{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result SearchResult
		err    error
	}
	done := make(chan outcome, 1)
	started := time.Now()
	go func() {
		result, err := reader.Search(ctx, Query{Text: "evidence"})
		done <- outcome{result, err}
	}()
	select {
	case got := <-done:
		if got.err != nil || len(got.result.Hits) != 1 || got.result.Status.Ingestion != nil {
			t.Fatalf("diagnostic timeout disrupted evidence retrieval: %+v %v", got.result, got.err)
		}
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("optional diagnostics did not enforce a time budget")
	}
	if !errors.Is(<-observed, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatal("optional diagnostics exceeded their own time budget")
	}
}
