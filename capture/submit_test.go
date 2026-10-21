package capture

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubmitBoundsAndOwnsHostEvidence(t *testing.T) {
	p := captureFixture(t, 2)
	p.max = 16
	input := []byte("a very long host payload that must be bounded")
	facets := map[string][]string{"capture.host": {"synthetic"}}
	rec := Record{RequestID: "host:request", SessionID: "host:session", OriginalRequest: input, Response: []byte(`{"output":[]}`), Facets: facets, Metadata: map[string]any{"Authorization": "synthetic-secret"}}
	if err := p.Submit(rec); err != nil {
		t.Fatal(err)
	}
	input[0] = 'x'
	facets["capture.host"][0] = "changed"
	if err := p.Submit(rec); err != nil {
		t.Fatal(err)
	}
	item := <-p.q.Items()
	defer item.Done()
	if !item.Value.Truncated || len(item.Value.OriginalRequest) != 16 || item.Value.OriginalRequest[0] != 'a' || item.Value.Facets["capture.host"][0] != "synthetic" || item.Value.SessionID != "host:session" {
		t.Fatal("submit lost ownership, bounds or explicit scope")
	}
	if len(item.Value.Metadata) != 0 || p.CaptureStatus().DuplicateCompletions != 1 {
		t.Fatal("unsafe metadata or duplicate submission")
	}
}

func TestRejectedSubmitCanRetryAndDrainTracksInFlightReservation(t *testing.T) {
	p := captureFixture(t, 1)
	if err := p.Submit(Record{RequestID: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Submit(Record{RequestID: "two"}); err == nil {
		t.Fatal("full queue acknowledged")
	}
	item := <-p.q.Items()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if p.Drain(ctx) == nil {
		t.Fatal("drain ignored retained in-flight reservation")
	}
	item.Done()
	if err := p.Submit(Record{RequestID: "two", OriginalRequest: []byte("retry")}); err != nil {
		t.Fatal("rejected submission poisoned retry", err)
	}
	item = <-p.q.Items()
	if !bytes.Equal(item.Value.OriginalRequest, []byte("retry")) {
		t.Fatal("retry lost data")
	}
	item.Done()
	if err := p.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.Shutdown()
	if err := p.Submit(Record{RequestID: "late"}); err == nil {
		t.Fatal("accepted after shutdown")
	}
}

func TestDeliveryDoesNotFollowCollectorRedirect(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); w.WriteHeader(202) }))
	defer target.Close()
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer collector.Close()
	p := NewProducer()
	defer p.Shutdown()
	config := DefaultCaptureConfig()
	config.Endpoint = collector.URL
	if err := p.Configure(config); err != nil {
		t.Fatal(err)
	}
	if err := p.Submit(Record{RequestID: "redirect", OriginalRequest: []byte("private synthetic evidence")}); err != nil {
		t.Fatal(err)
	}
	awaitCapture(t, p, func(s CaptureStatus) bool { return s.DeliveryRetries > 0 })
	p.Shutdown()
	if leaked.Load() != 0 {
		t.Fatal("delivery followed collector redirect")
	}
}
