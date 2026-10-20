package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/boundedqueue"
	"cpa-session-archive/internal/storage"
)

func TestIngestionByteBudgetIncludesPendingWrite(t *testing.T) {
	rec := archive.Record{RequestID: "one", SessionID: "request:one"}
	weight, err := archive.QueueWeight(rec)
	if err != nil {
		t.Fatal(err)
	}
	q, err := boundedqueue.New[archive.Record](4, weight)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{q: q, s: &metricBackend{put: func([]archive.Record) error { return nil }}}
	post := func(body string) int {
		w := httptest.NewRecorder()
		s.ingest(w, httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(body)))
		return w.Code
	}
	if got := post(`{"request_id":"one"}`); got != 202 {
		t.Fatalf("admission: %d", got)
	}
	item := <-q.Items()
	if got := post(`{"request_id":"two"}`); got != 503 {
		t.Fatalf("in-flight reservation: %d", got)
	}
	if got := post(`{"request_id":"much-longer-record"}`); got != 413 {
		t.Fatalf("oversize: %d", got)
	}
	entries := []*boundedqueue.Item[archive.Record]{item}
	s.flushEntries(entries)
	if entries[0] != nil || item.Value.RequestID != "" || q.Snapshot().RetainedBytes != 0 {
		t.Fatal("completed batch retains payload or reservation")
	}
	if got := post(`{"request_id":"two"}`); got != 202 {
		t.Fatalf("released reservation: %d", got)
	}
	(<-q.Items()).Done()
	status := s.ingestionStatus()
	if status.ByteRejected != 1 || status.OversizeRejected != 1 || status.CommittedDeliveries != 1 {
		t.Fatalf("unexpected counters: %+v", status)
	}
}

type unreadableBody struct{ t *testing.T }

func (b unreadableBody) Read([]byte) (int, error) {
	b.t.Error("busy admission read request body")
	return 0, errors.New("unexpected read")
}
func (b unreadableBody) Close() error { return nil }

func TestIngestionBusyRejectsBeforeDecode(t *testing.T) {
	s := &server{q: testRecordQueue(t, 1), decodeSlots: make(chan struct{}, 1)}
	s.decodeSlots <- struct{}{}
	r := httptest.NewRequest(http.MethodPost, "/ingest", nil)
	r.Body = unreadableBody{t}
	w := httptest.NewRecorder()
	s.ingest(w, r)
	if w.Code != 503 || s.ingestionStatus().BusyRejected != 1 {
		t.Fatal("busy decoder was not rejected")
	}
}

func TestProducerObservationValidation(t *testing.T) {
	report := archive.CaptureStatus{Version: 1, Epoch: "fixture-1", Since: time.Now().UTC().Format(time.RFC3339Nano), Enqueued: 7}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var m ingestionMetrics
	m.observeProducer(string(encoded))
	if !m.state.ProducerObserved || m.state.Producer.Capture.Enqueued != 7 || m.state.Producer.ReceivedAt == "" {
		t.Fatal("valid producer report missing")
	}
	original := m.state.Producer
	for _, input := range []string{"{", strings.Repeat("x", 4097), strings.Replace(string(encoded), "fixture-1", "invalid epoch", 1), strings.Replace(string(encoded), `"version":1`, `"version":2`, 1), strings.Replace(string(encoded), `"in_flight":0`, `"in_flight":-1`, 1)} {
		m.observeProducer(input)
		if m.state.Producer != original {
			t.Fatal("invalid report replaced valid observation")
		}
	}
}

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
	s := &server{s: backend, q: testRecordQueue(t, 1)}
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
	entry := <-s.q.Items()
	batch := []archive.Record{entry.Value}
	go func() { defer close(done); s.flush(batch); clear(batch); entry.Done() }()
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

func testRecordQueue(t *testing.T, capacity int) *boundedqueue.Queue[archive.Record] {
	t.Helper()
	q, err := boundedqueue.New[archive.Record](capacity, 512<<20)
	if err != nil {
		t.Fatal(err)
	}
	return q
}
