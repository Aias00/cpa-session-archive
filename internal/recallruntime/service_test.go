package recallruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-session-archive/internal/ingeststatus"
	"cpa-session-archive/internal/memory"
)

type testProjection struct {
	memory.Reader
	mu             sync.Mutex
	status         memory.Status
	statusErr      error
	statusCalls    int
	runs           int
	closes         int
	workerExited   bool
	activeRequests int
	unsafeClose    bool
	runStarted     chan struct{}
	runExited      chan struct{}
	stopWorker     chan struct{}
	requestStarted chan struct{}
	releaseRequest chan struct{}
}

func newTestProjection() *testProjection {
	return &testProjection{status: memory.Status{Sources: 3, BackfillDone: true, MessageMetadataDone: true}, runStarted: make(chan struct{}), runExited: make(chan struct{})}
}

func (p *testProjection) Run(ctx context.Context) {
	p.mu.Lock()
	p.runs++
	p.mu.Unlock()
	close(p.runStarted)
	select {
	case <-ctx.Done():
	case <-p.stopWorker:
	}
	p.mu.Lock()
	p.workerExited = true
	p.mu.Unlock()
	close(p.runExited)
}

func (p *testProjection) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closes++
	p.unsafeClose = !p.workerExited || p.activeRequests != 0
	return nil
}

func (p *testProjection) Status(context.Context) (memory.Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statusCalls++
	return p.status, p.statusErr
}

func (p *testProjection) Search(ctx context.Context, _ memory.Query) (memory.SearchResult, error) {
	if p.requestStarted != nil {
		p.mu.Lock()
		p.activeRequests++
		p.mu.Unlock()
		defer func() {
			p.mu.Lock()
			p.activeRequests--
			p.mu.Unlock()
		}()
		close(p.requestStarted)
		<-ctx.Done()
		<-p.releaseRequest
		return memory.SearchResult{}, ctx.Err()
	}
	status, err := p.Status(ctx)
	return memory.SearchResult{Hits: []memory.Hit{}, Status: status}, err
}

func testConfig(t *testing.T) Config {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "read.token")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("a", 48)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return Config{Listen: "127.0.0.1:0", TokenFile: tokenFile}
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("lifecycle operation did not finish")
	}
}

func TestStartRequiresLoopbackAndPrivateDistinctCredentials(t *testing.T) {
	for _, scenario := range []string{"public-bind", "missing-token", "public-token", "directory-token", "symlink-token", "oversize-token", "invalid-token", "public-registry", "malformed-registry", "unknown-registry-field", "trailing-registry", "unscoped-registry", "duplicate-registry", "reserved-owner", "reserved-scoped"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := testConfig(t)
			registry := filepath.Join(t.TempDir(), "credentials.json")
			switch scenario {
			case "public-bind":
				cfg.Listen = "0.0.0.0:0"
			case "missing-token":
				cfg.TokenFile = ""
			case "public-token":
				if err := os.Chmod(cfg.TokenFile, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory-token":
				cfg.TokenFile = t.TempDir()
			case "symlink-token":
				link := cfg.TokenFile + ".link"
				if err := os.Symlink(cfg.TokenFile, link); err != nil {
					t.Fatal(err)
				}
				cfg.TokenFile = link
			case "oversize-token":
				if err := os.WriteFile(cfg.TokenFile, []byte(strings.Repeat("x", 4097)), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid-token":
				if err := os.WriteFile(cfg.TokenFile, []byte(strings.Repeat("x", 39)+" secret"), 0600); err != nil {
					t.Fatal(err)
				}
			case "reserved-owner":
				cfg.ReservedToken = strings.Repeat("a", 48)
			default:
				cfg.CredentialsFile = registry
				body := `[{"token":"` + strings.Repeat("b", 48) + `","scope":{"project_id":"p"}}]`
				switch scenario {
				case "malformed-registry":
					body = "["
				case "unknown-registry-field":
					body = `[{"token":"` + strings.Repeat("b", 48) + `","scope":{"project_id":"p"},"other":true}]`
				case "trailing-registry":
					body += " []"
				case "unscoped-registry":
					body = `[{"token":"` + strings.Repeat("b", 48) + `","scope":{}}]`
				case "duplicate-registry":
					body = `[{"token":"` + strings.Repeat("a", 48) + `","scope":{"project_id":"p"}}]`
				case "reserved-scoped":
					cfg.ReservedToken = strings.Repeat("b", 48)
				}
				if err := os.WriteFile(registry, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				if scenario == "public-registry" {
					if err := os.Chmod(registry, 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			projection := newTestProjection()
			service, err := Start(context.Background(), projection, cfg)
			if err == nil {
				_ = service.Close()
				t.Fatal("unsafe configuration accepted")
			}
			if strings.Contains(err.Error(), strings.Repeat("a", 48)) || strings.Contains(err.Error(), strings.Repeat("b", 48)) {
				t.Fatal("configuration error exposed a credential")
			}
			if projection.runs != 0 || projection.closes != 0 {
				t.Fatal("failed Start took ownership of the projection")
			}
		})
	}
}

func TestServicePreservesAuthScopesAndIndependentReadiness(t *testing.T) {
	cfg := testConfig(t)
	registry := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(registry, []byte(`[{"token":"`+strings.Repeat("b", 48)+`","scope":{"project_id":"p"}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.CredentialsFile = registry
	cfg.Diagnostics = func(context.Context) (ingeststatus.Status, error) {
		return ingeststatus.Status{ReceivedRequests: 42}, nil
	}
	projection := newTestProjection()
	projection.statusErr = errors.New("source feed unavailable")
	service, err := Start(context.Background(), projection, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	await(t, projection.runStarted)
	projection.mu.Lock()
	if projection.statusCalls != 0 {
		t.Error("Start required a source-feed request")
	}
	projection.mu.Unlock()
	client := &http.Client{Timeout: 3 * time.Second}
	call := func(path, token string) (int, []byte) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, "http://"+service.Addr().String()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, body
	}
	if code, _ := call("/healthz", ""); code != http.StatusOK {
		t.Fatal(code)
	}
	if code, body := call("/readyz", ""); code != http.StatusServiceUnavailable || strings.Contains(string(body), "source feed") {
		t.Fatalf("readiness %d %s", code, body)
	}
	if code, _ := call("/v1/status", ""); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	projection.mu.Lock()
	projection.statusErr = nil
	projection.mu.Unlock()
	if code, _ := call("/readyz", ""); code != http.StatusOK {
		t.Fatal(code)
	}
	for _, token := range []string{strings.Repeat("a", 48), strings.Repeat("b", 48)} {
		code, body := call("/v1/status", token)
		var status memory.Status
		if err := json.Unmarshal(body, &status); err != nil || code != http.StatusOK {
			t.Fatalf("status %d %s %v", code, body, err)
		}
		if token == strings.Repeat("a", 48) {
			if status.Sources != 3 || status.Ingestion == nil || status.Ingestion.ReceivedRequests != 42 {
				t.Fatalf("owner diagnostics missing: %+v", status)
			}
		} else if status.Sources != 0 || status.Ingestion != nil || !status.ScopeFiltered {
			t.Fatalf("scoped diagnostics exposed: %+v", status)
		}
	}
	if code, _ := call("/v1/search?q=evidence&project_id=other", strings.Repeat("b", 48)); code != http.StatusBadRequest {
		t.Fatal(code)
	}
}

func TestCloseWaitsForWorkerAndActiveHTTPBeforeClosingProjection(t *testing.T) {
	projection := newTestProjection()
	projection.requestStarted = make(chan struct{})
	projection.releaseRequest = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, err := Start(ctx, projection, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	await(t, projection.runStarted)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		request, _ := http.NewRequest(http.MethodGet, "http://"+service.Addr().String()+"/v1/search?q=evidence", nil)
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 48))
		response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
		if err == nil {
			response.Body.Close()
		}
	}()
	await(t, projection.requestStarted)
	cancel()
	await(t, projection.runExited)
	projection.mu.Lock()
	if projection.closes != 0 {
		t.Error("projection closed while a handler was active")
	}
	projection.mu.Unlock()
	close(projection.releaseRequest)
	await(t, service.Done())
	await(t, requestDone)
	var calls sync.WaitGroup
	for range 5 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			if err := service.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	calls.Wait()
	projection.mu.Lock()
	defer projection.mu.Unlock()
	if projection.closes != 1 || projection.unsafeClose {
		t.Fatalf("projection close order/count: closes=%d unsafe=%v", projection.closes, projection.unsafeClose)
	}
}

func TestUnexpectedListenerOrWorkerExitStopsServiceAndReportsFailure(t *testing.T) {
	for _, component := range []string{"listener", "worker"} {
		t.Run(component, func(t *testing.T) {
			projection := newTestProjection()
			projection.stopWorker = make(chan struct{})
			service, err := Start(context.Background(), projection, testConfig(t))
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			await(t, projection.runStarted)
			if component == "listener" {
				if err := service.listener.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				close(projection.stopWorker)
			}
			await(t, service.Done())
			if service.Err() == nil {
				t.Fatal("unexpected component exit was reported as success")
			}
			projection.mu.Lock()
			defer projection.mu.Unlock()
			if projection.closes != 1 || projection.unsafeClose {
				t.Fatal("failed service leaked or closed an active projection")
			}
		})
	}
}
