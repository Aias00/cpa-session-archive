package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/archivefeed"
	"cpa-session-archive/internal/ingeststatus"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/projectionlock"
	"cpa-session-archive/internal/storage"
)

func testEnvironment(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	env := map[string]string{"MEMORY_DB": filepath.Join(dir, "memory.sqlite"), "MEMORY_ARCHIVE_TOKEN_FILE": filepath.Join(dir, "source.token"), "MEMORY_READ_TOKEN_FILE": filepath.Join(dir, "read.token"), "MEMORY_LISTEN_ADDR": "127.0.0.1:0"}
	if err := os.WriteFile(env["MEMORY_ARCHIVE_TOKEN_FILE"], []byte(strings.Repeat("a", 48)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env["MEMORY_READ_TOKEN_FILE"], []byte(strings.Repeat("r", 48)), 0600); err != nil {
		t.Fatal(err)
	}
	return env
}

func getter(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func TestConfigRequiresPrivateAbsoluteProjectionAndDedicatedTokenPaths(t *testing.T) {
	for _, scenario := range []string{"missing-db", "relative-db", "public-db", "directory-db", "symlink-db", "missing-source-token", "missing-read-token"} {
		t.Run(scenario, func(t *testing.T) {
			env := testEnvironment(t)
			switch scenario {
			case "missing-db":
				delete(env, "MEMORY_DB")
			case "relative-db":
				env["MEMORY_DB"] = "memory.sqlite"
			case "public-db":
				if err := os.WriteFile(env["MEMORY_DB"], nil, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory-db":
				env["MEMORY_DB"] = t.TempDir()
			case "symlink-db":
				if err := os.Symlink(env["MEMORY_READ_TOKEN_FILE"], env["MEMORY_DB"]); err != nil {
					t.Fatal(err)
				}
			case "missing-source-token":
				delete(env, "MEMORY_ARCHIVE_TOKEN_FILE")
			case "missing-read-token":
				delete(env, "MEMORY_READ_TOKEN_FILE")
			}
			if _, err := loadConfig(getter(env)); err == nil {
				t.Fatal("invalid daemon configuration accepted")
			}
		})
	}
	env := testEnvironment(t)
	delete(env, "MEMORY_LISTEN_ADDR")
	cfg, err := loadConfig(getter(env))
	if err != nil || cfg.archiveEndpoint != "http://127.0.0.1:8318" || cfg.runtime.Listen != "127.0.0.1:8319" {
		t.Fatalf("default daemon addresses: %v", err)
	}
}

func TestDaemonStartFailureReleasesProjectionOwnership(t *testing.T) {
	env := testEnvironment(t)
	env["MEMORY_READ_TOKEN_FILE"] = env["MEMORY_ARCHIVE_TOKEN_FILE"]
	if err := run(context.Background(), getter(env)); err == nil || !strings.Contains(err.Error(), "distinct") {
		t.Fatalf("shared credential accepted: %v", err)
	}
	lock, err := projectionlock.Acquire(env["MEMORY_DB"])
	if err != nil {
		t.Fatalf("failed startup leaked projection ownership: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

type fixtureFeed struct {
	mu       sync.Mutex
	exists   bool
	sequence int64
}

func (f *fixtureFeed) MemoryWatermark(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sequence, nil
}
func (f *fixtureFeed) MemoryBackfill(context.Context, int64, int) (archive.SourcePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	page := archive.SourcePage{IDs: []string{}, AfterID: 1, Done: true}
	if f.exists {
		page.IDs = []string{"one"}
	}
	return page, nil
}
func (f *fixtureFeed) MemoryChanges(_ context.Context, after int64, _ int) ([]archive.SourceChange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if after < f.sequence {
		return []archive.SourceChange{{Sequence: f.sequence, RequestID: "one"}}, nil
	}
	return []archive.SourceChange{}, nil
}
func (f *fixtureFeed) MemoryRevision(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists {
		return "", storage.ErrSourceNotFound
	}
	return strings.Repeat("1", 64), nil
}
func (f *fixtureFeed) MemorySource(context.Context, string) (archive.SourceSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists {
		return archive.SourceSnapshot{}, storage.ErrSourceNotFound
	}
	return archive.SourceSnapshot{Record: archive.Record{RequestID: "one", SessionID: "session", Facets: map[string][]string{"project.id": {"p"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"Synthetic independent memory evidence"}]}`), StartedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}, Revision: strings.Repeat("1", 64), Sequence: 1}, nil
}

func TestDaemonStartsOfflineRecoversAndRevalidatesDeletedEvidenceOverHTTP(t *testing.T) {
	env := testEnvironment(t)
	fixture := &fixtureFeed{exists: true, sequence: 1}
	feedHandler, err := archivefeed.NewHandler(fixture, archivefeed.ServerConfig{ArchiveID: "synthetic-independent-feed", Token: strings.Repeat("a", 48), HistoryComplete: true, Diagnostics: func() ingeststatus.Status { return ingeststatus.Status{Epoch: "collector-test", ReceivedRequests: 42} }})
	if err != nil {
		t.Fatal(err)
	}
	var available atomic.Bool
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !available.Load() {
			http.Error(w, "collector unavailable", http.StatusServiceUnavailable)
			return
		}
		feedHandler.ServeHTTP(w, r)
	}))
	defer feedServer.Close()
	env["MEMORY_ARCHIVE_ENDPOINT"] = feedServer.URL
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	env["MEMORY_LISTEN_ADDR"] = reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, getter(env)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not release its resources")
		}
	})
	client := &http.Client{Timeout: time.Second}
	call := func(path string) (int, []byte, error) {
		request, err := http.NewRequest(http.MethodGet, "http://"+env["MEMORY_LISTEN_ADDR"]+path, nil)
		if err != nil {
			return 0, nil, err
		}
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("r", 48))
		response, err := client.Do(request)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		return response.StatusCode, body, err
	}
	waitStatus := func(path string, expected int) []byte {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			code, body, err := call(path)
			if err == nil && code == expected {
				return body
			}
			if time.Now().After(deadline) {
				t.Fatalf("daemon %s did not reach status %d: code=%d err=%v", path, expected, code, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitStatus("/healthz", http.StatusOK)
	if code, _, err := call("/readyz"); err != nil || code != http.StatusServiceUnavailable {
		t.Fatalf("offline readiness: %d %v", code, err)
	}
	if code, body, err := call("/v1/search?q=evidence&project_id=p"); err != nil || code != http.StatusInternalServerError || strings.Contains(string(body), `"hits"`) {
		t.Fatalf("offline feed became an empty success: %d %v", code, err)
	}
	second := make(map[string]string, len(env))
	for key, value := range env {
		second[key] = value
	}
	second["MEMORY_LISTEN_ADDR"] = "127.0.0.1:0"
	if err := run(context.Background(), getter(second)); err == nil || !strings.Contains(err.Error(), "owned") {
		t.Fatalf("second writer accepted: %v", err)
	}
	available.Store(true)
	waitStatus("/readyz", http.StatusOK)
	body := waitStatus("/v1/search?q=evidence&project_id=p", http.StatusOK)
	var result memory.SearchResult
	if err := json.Unmarshal(body, &result); err != nil || len(result.Hits) != 1 || result.Status.Ingestion == nil || result.Status.Ingestion.ReceivedRequests != 42 {
		t.Fatalf("independent evidence/diagnostics unavailable: %v", err)
	}
	citation := result.Hits[0].Citation
	values := url.Values{"request_id": {citation.RequestID}, "revision": {citation.Revision}, "side": {citation.Side}, "pointer": {citation.Pointer}, "start": {strconv.Itoa(citation.Start)}, "end": {strconv.Itoa(citation.End)}, "normalizer_version": {strconv.Itoa(citation.NormalizerVersion)}}
	if code, _, err := call("/v1/source?" + values.Encode()); err != nil || code != http.StatusOK {
		t.Fatalf("source HTTP roundtrip: %d %v", code, err)
	}
	fixture.mu.Lock()
	fixture.exists, fixture.sequence = false, 2
	fixture.mu.Unlock()
	if code, _, err := call("/v1/source?" + values.Encode()); err != nil || code != http.StatusGone {
		t.Fatalf("deleted source remained readable: %d %v", code, err)
	}
}

func TestLegacyAdoptionRequiresExplicitBoolean(t *testing.T) {
	for _, value := range []string{"", "false", "true", "1", "TRUE", "yes"} {
		env := testEnvironment(t)
		env["MEMORY_ADOPT_ARCHIVE_SOURCE"] = value
		cfg, err := loadConfig(getter(env))
		valid := value == "" || value == "false" || value == "true"
		if (err == nil) != valid {
			t.Fatalf("adoption config %q validation mismatch", value)
		}
		if err == nil && cfg.adoptSource != (value == "true") {
			t.Fatal("legacy adoption enabled without explicit true")
		}
	}
}
