package capture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cpa-session-archive/internal/outbox"
)

func spoolFixture(t *testing.T, dir string, entries int, transport captureTransport) *Producer {
	t.Helper()
	p := NewProducer()
	p.client.Transport = transport
	t.Cleanup(p.Shutdown)
	config := DefaultCaptureConfig()
	config.Endpoint, config.SpoolDir = "http://127.0.0.1:1", dir
	config.SpoolMaxEntries, config.SpoolMaxBytes = entries, 1<<20
	if err := p.Configure(config); err != nil {
		t.Fatal(err)
	}
	return p
}
func awaitCapture(t *testing.T, p *Producer, ready func(CaptureStatus) bool) CaptureStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := p.CaptureStatus()
		if ready(status) {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("capture state did not converge: %+v", p.CaptureStatus())
	return CaptureStatus{}
}
func commitResponse(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	header := make(http.Header)
	header.Set(CommitAckHeader, CommitAckResponse)
	header.Set(CommitDigestHeader, fmt.Sprintf("%x", digest))
	return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(bytes.NewReader(nil))}, nil
}
func TestSpoolRemovesOnlyMatchingCommitReceipt(t *testing.T) {
	var negotiated atomic.Bool
	p := spoolFixture(t, filepath.Join(t.TempDir(), "private"), 8, func(r *http.Request) (*http.Response, error) {
		negotiated.Store(r.Header.Get(CommitAckHeader) == CommitAckRequest)
		return commitResponse(r)
	})
	p.Complete(CompletionEvent{RequestID: "committed-fixture"})
	status := awaitCapture(t, p, func(s CaptureStatus) bool { return s.OutboxCommitted == 1 && s.Outbox != nil && s.Outbox.Entries == 0 })
	if !negotiated.Load() || status.OutboxPersisted != 1 || status.HTTPAccepted != 1 {
		t.Fatal("spool bypassed negotiated persistence")
	}
}
func TestSpoolReplaysAfterLostOrMismatchedReceipt(t *testing.T) {
	for _, mode := range []string{"legacy202", "wrongDigest"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "private")
			sent := make(chan []byte, 1)
			p := spoolFixture(t, dir, 8, func(r *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				select {
				case sent <- body:
				default:
				}
				code := 202
				header := make(http.Header)
				if mode == "wrongDigest" {
					code = 200
					header.Set(CommitAckHeader, CommitAckResponse)
					header.Set(CommitDigestHeader, "wrong")
				}
				return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(bytes.NewReader(nil))}, nil
			})
			p.Complete(CompletionEvent{RequestID: "replayed-fixture"})
			awaitCapture(t, p, func(s CaptureStatus) bool { return s.OutboxPersisted == 1 && s.CommitReceiptMissing > 0 })
			original := <-sent
			p.Shutdown()
			store, err := outbox.Open(filepath.Join(dir, "deliveries.sqlite"), 1<<20, 8)
			if err != nil {
				t.Fatal(err)
			}
			entry, err := store.Next(context.Background())
			if err != nil || !bytes.Equal(entry.Body, original) {
				t.Fatal("uncertain receipt lost original bytes", err)
			}
			store.Close()
			var equal atomic.Bool
			replay := spoolFixture(t, dir, 8, func(r *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				equal.Store(bytes.Equal(body, original))
				r.Body = io.NopCloser(bytes.NewReader(body))
				return commitResponse(r)
			})
			awaitCapture(t, replay, func(s CaptureStatus) bool { return s.OutboxCommitted == 1 && s.Outbox != nil && s.Outbox.Entries == 0 })
			if !equal.Load() {
				t.Fatal("restart changed replay bytes")
			}
		})
	}
}
func TestSpoolQuarantineDoesNotBlockNextDelivery(t *testing.T) {
	p := spoolFixture(t, filepath.Join(t.TempDir(), "private"), 8, func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var rec Record
		if err = json.Unmarshal(body, &rec); err != nil {
			return nil, err
		}
		if rec.RequestID == "rejected" {
			return &http.Response{StatusCode: 413, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}, nil
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		return commitResponse(r)
	})
	p.Complete(CompletionEvent{RequestID: "rejected"})
	p.Complete(CompletionEvent{RequestID: "accepted"})
	status := awaitCapture(t, p, func(s CaptureStatus) bool {
		return s.OutboxCommitted == 1 && s.OutboxQuarantined == 1 && s.Outbox != nil && s.Outbox.Pending == 0
	})
	if status.Outbox.Entries != 1 || status.Outbox.Quarantined != 1 || status.Outbox.Bytes == 0 {
		t.Fatal("permanent rejection was deleted")
	}
}
func TestSpoolCapacityBackpressureAndShutdownPreservePersistedWork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	p := spoolFixture(t, dir, 1, func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	p.Complete(CompletionEvent{RequestID: "persisted"})
	awaitCapture(t, p, func(s CaptureStatus) bool { return s.OutboxPersisted == 1 && s.InFlight == 1 })
	p.Complete(CompletionEvent{RequestID: "unspooled"})
	awaitCapture(t, p, func(s CaptureStatus) bool { return s.OutboxCapacityWaits > 0 })
	p.Shutdown()
	status := p.CaptureStatus()
	if status.ShutdownDiscarded != 1 || status.Queue.RetainedBytes != 0 || status.InFlight != 0 {
		t.Fatalf("shutdown accounting: %+v", status)
	}
	store, err := outbox.Open(filepath.Join(dir, "deliveries.sqlite"), 1<<20, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry, err := store.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var rec Record
	if json.Unmarshal(entry.Body, &rec) != nil || rec.RequestID != "persisted" {
		t.Fatal("persisted inflight work was discarded")
	}
}

func TestSpoolReconfigurationPreservesModeAndConcurrentDelivery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	p := spoolFixture(t, dir, 8, commitResponse)
	configFor := func(path string) CaptureConfig {
		c := DefaultCaptureConfig()
		c.Endpoint, c.SpoolDir, c.SpoolMaxEntries, c.SpoolMaxBytes, c.Timeout = "http://127.0.0.1:1", path, 8, 1<<20, "2s"
		return c
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "other")} {
		if err := p.Configure(configFor(path)); err == nil {
			t.Fatal("live outbox replacement accepted")
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			if err := p.Configure(configFor(dir)); err != nil {
				t.Error(err)
			}
		}
	}()
	p.Complete(CompletionEvent{RequestID: "concurrent-config"})
	awaitCapture(t, p, func(s CaptureStatus) bool { return s.OutboxCommitted == 1 })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reconfiguration stalled")
	}
}
