package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const fixtureFingerprint = "synthetic-embedding-v1"

func testClient(t *testing.T, handler http.HandlerFunc) (*HTTPClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewHTTP(server.URL, strings.Repeat("t", 40), fixtureFingerprint, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, server
}

func TestHTTPEmbedsPurposeAndChecksModelContract(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/embed" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 40) {
			t.Error("invalid authenticated embedding request")
		}
		var request struct {
			Fingerprint string   `json:"fingerprint"`
			Purpose     Purpose  `json:"purpose"`
			Texts       []string `json:"texts"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Fingerprint != fixtureFingerprint || request.Purpose != Query || len(request.Texts) != 2 || request.Texts[0] != "中文 code/path.go" {
			t.Error("embedding input mapping")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[1,0],[0,1]]}`))
	})
	got, err := client.Embed(context.Background(), Query, []string{"中文 code/path.go", "SQLITE_BUSY"})
	if err != nil || len(got) != 2 || got[1][1] != 1 || client.Dimensions() != 2 || client.Fingerprint() != fixtureFingerprint {
		t.Fatalf("embedding contract: %v", err)
	}
}

func TestHTTPRejectsUnsafeOriginAndNeverFollowsRedirect(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1:1", "http://localhost:1", "http://example.com", "http://127.0.0.1:0", "http://127.0.0.1:", "http://user:password@127.0.0.1:1", "http://127.0.0.1:1?token=private", "http://127.0.0.1:1/other"} {
		if _, err := NewHTTP(endpoint, strings.Repeat("t", 40), fixtureFingerprint, 2); err == nil {
			t.Fatalf("unsafe embedding endpoint accepted: %s", endpoint)
		}
	}
	forwarded := make(chan struct{}, 1)
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded <- struct{}{} }))
	defer downstream.Close()
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, downstream.URL, 307) })
	if _, err := client.Embed(context.Background(), Query, []string{"evidence"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("redirect accepted: %v", err)
	}
	select {
	case <-forwarded:
		t.Fatal("credential followed redirect")
	default:
	}
	if client.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("embedding requests use system proxy")
	}
}

func TestHTTPMalformedOrFailedResponseNeverBecomesEmptySuccess(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"fingerprint":"synthetic-embedding-v1","dimensions":2}`, `{"fingerprint":"other","dimensions":2,"vectors":[[1,0]]}`, `{"fingerprint":"synthetic-embedding-v1","dimensions":3,"vectors":[[1,0]]}`, `{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":null}`, `{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[]}`, `{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[null]}`, `{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[0,0]]}`, `{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[1]]}`, `{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[1e100,0]]}`, `{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[1,0]],"private":"diagnostic"}`, `{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[1,0]]} {}`, strings.Repeat("x", maxResponseBytes+1)} {
		client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})
		if vectors, err := client.Embed(context.Background(), Query, []string{"private input"}); !errors.Is(err, ErrUnavailable) || vectors != nil {
			t.Fatalf("malformed response accepted or misclassified: %v", err)
		}
	}
	for _, status := range []int{400, 401, 404, 413, 422, 429, 500, 503} {
		client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("private input and token"))
		})
		if _, err := client.Embed(context.Background(), Query, []string{"private input"}); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private") {
			t.Fatalf("backend error leaked or became success: %v", err)
		}
	}
}

func TestHTTPRejectsDuplicateAndAliasedResponseControls(t *testing.T) {
	for _, body := range []string{
		`{"fingerprint":"other","fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[1,0]]}`,
		`{"fingerprint":"synthetic-embedding-v1","dimensions":3,"dimensions":2,"vectors":[[1,0]]}`,
		`{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[],"vectors":[[1,0]]}`,
		`{"Fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[1,0]]}`,
		`{"fingerprint":"synthetic-embedding-v1","Dimensions":2,"vectors":[[1,0]]}`,
		`{"fingerprint":"synthetic-embedding-v1","dimensions":true,"vectors":[[1,0]]}`,
		`{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[true,0]]}`,
		`{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[null,1]]}`,
		`{"fingerprint":"synthetic-embedding-v1","dimensions":2,"vectors":[[1,"0"]]}`,
	} {
		client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})
		if vectors, err := client.Embed(context.Background(), Query, []string{"synthetic text"}); !errors.Is(err, ErrUnavailable) || vectors != nil {
			t.Fatalf("ambiguous response controls accepted: %v", err)
		}
	}
}

func TestHTTPInputBoundsAndCancellation(t *testing.T) {
	called := false
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	for _, test := range []struct {
		purpose Purpose
		texts   []string
	}{{"other", []string{"text"}}, {Query, nil}, {Query, make([]string, MaxBatch+1)}, {Query, []string{" "}}, {Query, []string{string([]byte{255})}}, {Query, []string{strings.Repeat("中", 257)}}, {Document, []string{strings.Repeat("中", 385)}}} {
		if _, err := client.Embed(context.Background(), test.purpose, test.texts); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid input accepted: %v", err)
		}
	}
	if called {
		t.Fatal("invalid input reached provider")
	}
	started := make(chan struct{})
	cancelled, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := cancelled.Embed(ctx, Query, []string{"text"}); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("request did not reach provider")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider ignored cancellation")
	}
}
