package archive

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"cpa-session-archive/internal/boundedqueue"
)

func captureFixture(t *testing.T, capacity int) *Plugin {
	t.Helper()
	p := NewPlugin()
	q, err := boundedqueue.New[Record](capacity, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	p.q = q
	t.Cleanup(p.Shutdown)
	return p
}

func TestCatalogLifecycleDoesNotCreateEmptyConversation(t *testing.T) {
	p := captureFixture(t, 8)
	p.captureResponse(intercept{RequestID: "catalog", SourceFormat: "openai", StatusCode: 200, Body: []byte(`{"object":"list","data":[]}`)})
	p.complete(completion{RequestID: "catalog", SourceFormat: "openai", StatusCode: 200, Outcome: "succeeded"})
	status := p.CaptureStatus()
	if status.CatalogSkipped != 1 || status.Queue.Depth != 0 || status.MissingRequestState != 0 {
		t.Fatalf("catalog archived as a missing conversation: %+v", status)
	}
	p.captureResponse(intercept{RequestID: "model", SourceFormat: "openai", Model: "fixture", RequestedModel: "fixture", StatusCode: 200, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"evidence"}]}`), Body: []byte(`{"object":"list","data":[]}`)})
	p.complete(completion{RequestID: "model", SourceFormat: "openai", Model: "fixture", Outcome: "succeeded", StatusCode: 200})
	item := <-p.q.Items()
	if len(item.Value.OriginalRequest) == 0 || len(item.Value.Response) == 0 {
		t.Fatal("response fallback discarded real model data")
	}
	item.Done()
	p.complete(completion{RequestID: "failed", SourceFormat: "openai-response", Model: "fixture", Outcome: "failed", StatusCode: 500})
	if status := p.CaptureStatus(); status.CatalogSkipped != 1 || status.MissingRequestState != 1 || status.Queue.Depth != 1 {
		t.Fatalf("failed execution was misidentified as catalog: %+v", status)
	}
}

func TestStreamHeaderCachesOriginalAndIgnoresLateReplay(t *testing.T) {
	p := captureFixture(t, 4)
	original := []byte(`{"input":"retained marker"}`)
	expected := bytes.Clone(original)
	p.captureChunk(intercept{RequestID: "stream", SourceFormat: "openai-response", Model: "fixture", ChunkIndex: -1, OriginalRequest: original, RequestBody: []byte(`{"translated":true}`), Body: []byte("header-only")})
	original[0] = 'x'
	body := []byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n")
	p.captureChunk(intercept{RequestID: "stream", ChunkIndex: 0, Body: body})
	done := completion{RequestID: "stream", SourceFormat: "openai-response", Model: "fixture", Stream: true, Outcome: "succeeded", StatusCode: 200}
	p.complete(done)
	p.complete(done)
	p.captureChunk(intercept{RequestID: "stream", ChunkIndex: 1, Body: []byte("late")})
	item := <-p.q.Items()
	if !bytes.Equal(item.Value.OriginalRequest, expected) || !bytes.Equal(item.Value.Response, body) || len(item.Value.UpstreamRequest) != 0 {
		t.Fatal("header caching or original/provider boundary failed")
	}
	item.Done()
	status := p.CaptureStatus()
	if status.Enqueued != 1 || status.DuplicateCompletions != 1 || status.ActiveRequests != 0 || status.RecoveredFromResponse != 1 || status.Queue.RetainedBytes != 0 {
		t.Fatalf("late callbacks retained state or replaced a good record: %+v", status)
	}
}

func TestCaptureQueueRejectsByCountAndRetainedBytes(t *testing.T) {
	p := captureFixture(t, 1)
	p.complete(completion{RequestID: "one"})
	p.complete(completion{RequestID: "two"})
	if p.CaptureStatus().QueueDropped != 1 {
		t.Fatal("count overflow invisible")
	}
	item := <-p.q.Items()
	weight, err := QueueWeight(item.Value)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.q.SetMaxBytes(weight + weight/2); err != nil {
		t.Fatal(err)
	}
	p.complete(completion{RequestID: "new"})
	if p.CaptureStatus().ByteDropped != 1 {
		t.Fatal("in-flight bytes were not reserved")
	}
	item.Done()
	if err := p.q.SetMaxBytes(1); err != nil {
		t.Fatal(err)
	}
	p.complete(completion{RequestID: "big"})
	if status := p.CaptureStatus(); status.OversizeDropped != 1 || status.Queue.RetainedBytes != 0 {
		t.Fatalf("oversize rejection leaked quota: %+v", status)
	}
}

type captureTransport func(*http.Request) (*http.Response, error)

func (f captureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCaptureShutdownCancelsRequestAndReleasesQueuedData(t *testing.T) {
	p := NewPlugin()
	entered := make(chan struct{})
	p.client.Transport = captureTransport(func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	raw, _ := json.Marshal(lifecycle{ConfigYAML: []byte("endpoint: http://127.0.0.1:1\nqueue_size: 64\nqueue_max_bytes: 1048576\n")})
	if _, err := p.configure(raw); err != nil {
		t.Fatal(err)
	}
	p.complete(completion{RequestID: "one"})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("sender did not start")
	}
	p.complete(completion{RequestID: "two"})
	done := make(chan struct{})
	go func() { p.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel delivery")
	}
	if s := p.CaptureStatus(); s.ShutdownDiscarded != 2 || s.InFlight != 0 || s.Queue.Depth != 0 || s.Queue.RetainedBytes != 0 {
		t.Fatalf("shutdown retained queued payloads: %+v", s)
	}
	p.Shutdown()
}

func TestCaptureDeliveryPermanentAndTransientResponses(t *testing.T) {
	for _, code := range []int{400, 413, 422, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			p := captureFixture(t, 1)
			p.endpoint = "http://127.0.0.1/ingest"
			attempts := 0
			p.client.Transport = captureTransport(func(r *http.Request) (*http.Response, error) {
				attempts++
				var report CaptureStatus
				if err := json.Unmarshal([]byte(r.Header.Get(CaptureStatusHeader)), &report); err != nil || report.Version != 1 {
					t.Fatal("missing capture report")
				}
				status := code
				if attempts > 1 {
					status = http.StatusAccepted
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}, nil
			})
			result := p.sendWithRetry(Record{RequestID: "fixture"})
			if code == 503 {
				if result != deliveryAccepted || attempts != 2 || p.CaptureStatus().DeliveryRetries != 1 {
					t.Fatal("temporary response did not retry")
				}
			} else if result != deliveryRejected || attempts != 1 || p.CaptureStatus().DeliveryRetries != 0 {
				t.Fatal("permanent rejection retried")
			}
		})
	}
}
