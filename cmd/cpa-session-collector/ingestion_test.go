package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

type metricBackend struct {
	storage.Backend
	put       func([]archive.Record) error
	retryable bool
}

func (b *metricBackend) PutBatch(records []archive.Record) error { return b.put(records) }
func (b *metricBackend) RetryableWrite(error) bool               { return b.retryable }

func awaitMetricWrite(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not reach synchronization point")
	}
}

func TestIngestionDiagnosticsSeparateAdmissionFromCommit(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	backend := &metricBackend{put: func([]archive.Record) error { close(entered); <-release; return nil }}
	s := &server{s: backend, q: make(chan archive.Record, 1)}
	for _, input := range []struct {
		method, body string
		code         int
	}{
		{http.MethodGet, "", 405},
		{http.MethodPost, "{", 400},
		{http.MethodPost, `{"request_id":"one","truncated":true}`, 202},
		{http.MethodPost, `{"request_id":"rejected"}`, 503},
	} {
		response := httptest.NewRecorder()
		s.ingest(response, httptest.NewRequest(input.method, "/ingest", strings.NewReader(input.body)))
		if response.Code != input.code {
			t.Fatalf("status %d want %d", response.Code, input.code)
		}
	}
	before := s.ingestionStatus()
	if before.ReceivedRequests != 4 || before.InvalidRequests != 2 || before.EnqueuedDeliveries != 1 || before.QueueRejected != 1 || before.TruncatedDeliveries != 1 || before.CommittedDeliveries != 0 || before.QueueDepth != 1 || before.ProducerObserved || before.Acknowledgement != "in_memory_queue" {
		t.Fatalf("admission was mistaken for commit or producer observation: %+v", before)
	}
	batch := []archive.Record{<-s.q}
	go func() { defer close(done); s.flush(batch) }()
	awaitMetricWrite(t, entered)
	inFlight := s.ingestionStatus()
	if inFlight.WriteInFlight != 1 || inFlight.CommittedDeliveries != 0 {
		t.Fatalf("uncommitted write acknowledged: %+v", inFlight)
	}
	close(release)
	awaitMetricWrite(t, done)
	after := s.ingestionStatus()
	if after.Epoch != before.Epoch || after.WriteInFlight != 0 || after.CommittedDeliveries != 1 || after.LastCommitAt == "" || after.DiscardedDeliveries != 0 {
		t.Fatalf("commit diagnostics: %+v", after)
	}
	fresh := (&server{}).ingestionStatus()
	if fresh.Epoch == after.Epoch || fresh.CommittedDeliveries != 0 {
		t.Fatal("new process epoch inherited prior counters")
	}
}

func TestIngestionDiagnosticsRetryAndDiscard(t *testing.T) {
	t.Run("discard", func(t *testing.T) {
		backend := &metricBackend{put: func([]archive.Record) error { return errors.New("nonretryable") }}
		s := &server{s: backend}
		s.flush([]archive.Record{{RequestID: "one"}, {RequestID: "two"}})
		got := s.ingestionStatus()
		if got.DiscardedDeliveries != 2 || got.WriteErrors != 1 || got.Retries != 0 || got.CommittedDeliveries != 0 || got.WriteInFlight != 0 {
			t.Fatalf("discard diagnostics: %+v", got)
		}
	})
	t.Run("retry", func(t *testing.T) {
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var attempts atomic.Int32
		backend := &metricBackend{retryable: true, put: func([]archive.Record) error {
			if attempts.Add(1) == 1 {
				return errors.New("transient")
			}
			close(entered)
			<-release
			return nil
		}}
		s := &server{s: backend}
		go func() { defer close(done); s.flush([]archive.Record{{RequestID: "one"}}) }()
		awaitMetricWrite(t, entered)
		pending := s.ingestionStatus()
		if pending.Retries != 1 || pending.WriteErrors != 1 || pending.WriteInFlight != 1 || pending.CommittedDeliveries != 0 {
			t.Fatalf("retry diagnostics: %+v", pending)
		}
		close(release)
		awaitMetricWrite(t, done)
		got := s.ingestionStatus()
		if attempts.Load() != 2 || got.CommittedDeliveries != 1 || got.DiscardedDeliveries != 0 || got.WriteInFlight != 0 {
			t.Fatalf("retry completion: %+v", got)
		}
	})
}
