package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/boundedqueue"
)

func receiveDelivery(t *testing.T, s *server) *boundedqueue.Item[ingestDelivery] {
	t.Helper()
	select {
	case item := <-s.q.Items():
		return item
	case <-time.After(3 * time.Second):
		t.Fatal("delivery was not admitted")
		return nil
	}
}

func TestCommitReceiptWaitsForStorageAndBindsPayload(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := &server{q: testRecordQueue(t, 4), s: &metricBackend{put: func([]archive.Record) error { close(entered); <-release; return nil }}}
	body := `{"request_id":"receipt-fixture"}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(body))
	req.Header.Set(archive.CommitAckHeader, archive.CommitAckRequest)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.ingest(response, req) }()
	item := receiveDelivery(t, s)
	writeDone := make(chan struct{})
	go func() { defer close(writeDone); s.flushEntries([]*boundedqueue.Item[ingestDelivery]{item}) }()
	awaitMetricWrite(t, entered)
	select {
	case <-done:
		t.Fatal("receipt preceded archive commit")
	default:
	}
	close(release)
	awaitMetricWrite(t, done)
	awaitMetricWrite(t, writeDone)
	sum := sha256.Sum256([]byte(body))
	if response.Code != 200 || response.Header().Get(archive.CommitAckHeader) != archive.CommitAckResponse || response.Header().Get(archive.CommitDigestHeader) != hex.EncodeToString(sum[:]) {
		t.Fatal("missing committed payload receipt")
	}
	if status := s.ingestionStatus(); status.CommittedDeliveries != 1 || status.QueueBytes != 0 {
		t.Fatalf("unreleased committed work: %+v", status)
	}
}

func TestCommitFailureNeverReturnsReceipt(t *testing.T) {
	s := &server{q: testRecordQueue(t, 1), s: &metricBackend{put: func([]archive.Record) error { return errors.New("storage fixture") }}}
	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(`{"request_id":"failed"}`))
	req.Header.Set(archive.CommitAckHeader, archive.CommitAckRequest)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.ingest(response, req) }()
	s.flushEntries([]*boundedqueue.Item[ingestDelivery]{receiveDelivery(t, s)})
	awaitMetricWrite(t, done)
	if response.Code != 503 || response.Header().Get(archive.CommitAckHeader) != "" || response.Header().Get(archive.CommitDigestHeader) != "" {
		t.Fatal("failed write returned commit receipt")
	}
}

func TestCommitWaitCancellationDoesNotCancelAdmittedWork(t *testing.T) {
	s := &server{q: testRecordQueue(t, 1), s: &metricBackend{put: func([]archive.Record) error { return nil }}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(`{"request_id":"canceled"}`)).WithContext(ctx)
	req.Header.Set(archive.CommitAckHeader, archive.CommitAckRequest)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.ingest(response, req) }()
	item := receiveDelivery(t, s)
	cancel()
	awaitMetricWrite(t, done)
	if response.Header().Get(archive.CommitAckHeader) != "" {
		t.Fatal("canceled wait fabricated receipt")
	}
	flushed := make(chan struct{})
	go func() { defer close(flushed); s.flushEntries([]*boundedqueue.Item[ingestDelivery]{item}) }()
	awaitMetricWrite(t, flushed)
	if status := s.ingestionStatus(); status.CommittedDeliveries != 1 || status.QueueBytes != 0 {
		t.Fatal("canceled client stranded admitted delivery")
	}
}

func TestCommitNegotiationRejectsUnknownAndTrailingJSON(t *testing.T) {
	for _, fixture := range []struct{ ack, body string }{{"future-v2", `{"request_id":"one"}`}, {archive.CommitAckRequest, `{"request_id":"one"} {"request_id":"two"}`}} {
		s := &server{q: testRecordQueue(t, 1)}
		r := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(fixture.body))
		r.Header.Set(archive.CommitAckHeader, fixture.ack)
		w := httptest.NewRecorder()
		s.ingest(w, r)
		if w.Code != 400 || s.ingestionStatus().EnqueuedDeliveries != 0 {
			t.Fatal("unsupported negotiation or trailing JSON admitted")
		}
	}
}
