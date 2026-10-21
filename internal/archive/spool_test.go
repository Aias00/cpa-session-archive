package archive

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
	"gopkg.in/yaml.v3"
)

func spoolFixture(t *testing.T, dir string, entries int, transport captureTransport) *Plugin {
	t.Helper()
	p := NewPlugin()
	p.client.Transport = transport
	t.Cleanup(p.Shutdown)
	config, _ := yaml.Marshal(map[string]any{"endpoint": "http://127.0.0.1:1", "spool_dir": dir, "spool_max_entries": entries, "spool_max_bytes": 1 << 20})
	raw, _ := json.Marshal(lifecycle{ConfigYAML: config})
	if _, err := p.configure(raw); err != nil {
		t.Fatal(err)
	}
	return p
}
func awaitCapture(t *testing.T, p *Plugin, ready func(CaptureStatus) bool) CaptureStatus {
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
	p.complete(completion{RequestID: "committed-fixture"})
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
			p.complete(completion{RequestID: "replayed-fixture"})
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
	p.complete(completion{RequestID: "rejected"})
	p.complete(completion{RequestID: "accepted"})
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
	p.complete(completion{RequestID: "persisted"})
	awaitCapture(t, p, func(s CaptureStatus) bool { return s.OutboxPersisted == 1 && s.InFlight == 1 })
	p.complete(completion{RequestID: "unspooled"})
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
	rawFor := func(path string) []byte {
		config, _ := yaml.Marshal(map[string]any{"endpoint": "http://127.0.0.1:1", "spool_dir": path, "spool_max_entries": 8, "spool_max_bytes": 1 << 20, "timeout": "2s"})
		raw, _ := json.Marshal(lifecycle{ConfigYAML: config})
		return raw
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "other")} {
		if _, err := p.configure(rawFor(path)); err == nil {
			t.Fatal("live outbox replacement accepted")
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			if _, err := p.configure(rawFor(dir)); err != nil {
				t.Error(err)
			}
		}
	}()
	p.complete(completion{RequestID: "concurrent-config"})
	awaitCapture(t, p, func(s CaptureStatus) bool { return s.OutboxCommitted == 1 })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reconfiguration stalled")
	}
}
