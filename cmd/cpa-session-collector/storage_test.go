package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

// Unimplemented methods panic if a handler crosses an unexpected storage boundary.
type collectorTestBackend struct {
	storage.Backend
	pingErr  error
	exists   bool
	rows     []archive.Record
	filters  map[string]string
	limit    int
	writes   int
	writeErr error
}

func (b *collectorTestBackend) Ping(context.Context) error { return b.pingErr }
func (b *collectorTestBackend) SessionExists(context.Context, string) (bool, error) {
	return b.exists, nil
}
func (b *collectorTestBackend) PutBatch(rows []archive.Record) error {
	b.writes++
	b.rows = append(b.rows, rows...)
	return b.writeErr
}
func (b *collectorTestBackend) RetryableWrite(error) bool { return false }
func (b *collectorTestBackend) SessionsFiltered(_ context.Context, limit int, filters map[string]string) ([]archive.SessionSummary, error) {
	b.limit, b.filters = limit, filters
	return []archive.SessionSummary{{SessionID: "backend-session", Requests: 2}}, nil
}

func TestCollectorUsesInjectedStorage(t *testing.T) {
	backend := &collectorTestBackend{exists: true}
	s := &server{s: backend, q: testRecordQueue(t, 1), tickets: map[string]exportTicket{}}
	response := httptest.NewRecorder()
	s.ingest(response, httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(`{"request_id":"request","session_id":"backend-session"}`)))
	if response.Code != http.StatusAccepted || backend.writes != 0 {
		t.Fatalf("ingest status=%d writes=%d", response.Code, backend.writes)
	}
	entry := <-s.q.Items()
	s.flush([]archive.Record{entry.Value})
	entry.Done()
	if backend.writes != 1 || len(backend.rows) != 1 || backend.rows[0].SessionID != "backend-session" {
		t.Fatalf("writes=%d rows=%+v", backend.writes, backend.rows)
	}
	response = httptest.NewRecorder()
	s.sessions(response, httptest.NewRequest(http.MethodGet, "/v1/sessions?limit=7&project.name=example", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"session_id":"backend-session"`) || backend.limit != 7 || backend.filters["project.name"] != "example" {
		t.Fatalf("query status=%d body=%s limit=%d filters=%v", response.Code, response.Body.String(), backend.limit, backend.filters)
	}
	response = httptest.NewRecorder()
	s.exportTicket(response, httptest.NewRequest(http.MethodGet, "/v1/export-tickets?session_id=backend-session", nil))
	if response.Code != http.StatusOK || len(s.tickets) != 1 {
		t.Fatalf("ticket status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	s.health(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health=%d", response.Code)
	}
	backend.pingErr = errors.New("backend unavailable")
	response = httptest.NewRecorder()
	s.health(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusServiceUnavailable || strings.TrimSpace(response.Body.String()) != "unhealthy" {
		t.Fatalf("unhealthy status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCollectorDoesNotClassifyBackendErrors(t *testing.T) {
	backend := &collectorTestBackend{writeErr: errors.New("database is locked")}
	s := &server{s: backend}
	s.flush([]archive.Record{{RequestID: "request", SessionID: "session"}})
	if backend.writes != 1 {
		t.Fatalf("collector overrode backend retry decision: writes=%d", backend.writes)
	}
}

type collectorTestSnapshot struct {
	storage.Snapshot
	closed bool
}

func (s *collectorTestSnapshot) ExportSessionJSONL(_ context.Context, sessionID, digest string, destination io.Writer) error {
	if sessionID != "session" || digest != "digest" {
		return errors.New("unexpected snapshot export")
	}
	_, err := io.WriteString(destination, "{\"session_id\":\"session\"}\n")
	return err
}

func (s *collectorTestSnapshot) Close() error {
	s.closed = true
	return nil
}

type collectorSnapshotBackend struct {
	storage.Backend
	snapshot storage.Snapshot
}

func (b *collectorSnapshotBackend) BeginSnapshot(time.Time, *int64) (storage.Snapshot, error) {
	return b.snapshot, nil
}

func TestCollectorExportsInjectedSnapshotInConfiguredDirectory(t *testing.T) {
	snapshot := &collectorTestSnapshot{}
	backend := &collectorSnapshotBackend{snapshot: snapshot}
	registry, err := newStableSnapshotRegistry(time.Minute, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	registry.now = func() time.Time { return now }
	entry, err := registry.create(backend, now, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{s: backend, exportDir: t.TempDir(), snapshotRegistry: registry}
	s.snapshotOnce.Do(func() {})
	artifact, size, err := s.prepareStableSnapshotExport(
		httptest.NewRequest(http.MethodGet, "/export", nil),
		exportTicket{Snapshot: entry.ID, SessionID: "session", RecordsSHA256: "digest"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = artifact.Close()
		_ = os.Remove(artifact.Name())
	})
	body, err := io.ReadAll(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(artifact.Name()) != s.exportDir || string(body) != "{\"session_id\":\"session\"}\n" || size != int64(len(body)) {
		t.Fatalf("artifact=%s size=%d body=%q", artifact.Name(), size, body)
	}
	now = now.Add(2 * time.Minute)
	registry.cleanup()
	if !snapshot.closed {
		t.Fatal("expired backend snapshot was not closed")
	}
}
