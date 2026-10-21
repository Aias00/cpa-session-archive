package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/boundedqueue"
	"cpa-session-archive/internal/ingeststatus"
)

type ingestionMetrics struct {
	mu    sync.Mutex
	state ingeststatus.Status
}

func (m *ingestionMetrics) initializeLocked() {
	if m.state.Epoch != "" {
		return
	}
	now := time.Now().UTC()
	m.state.Epoch = fmt.Sprintf("%d-%d", os.Getpid(), now.UnixNano())
	m.state.Since = now.Format(time.RFC3339Nano)
	m.state.Boundary = "collector_http_ingest"
	m.state.Acknowledgement = "in_memory_queue"
}

func (m *ingestionMetrics) received() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeLocked()
	m.state.ReceivedRequests++
}

func (m *ingestionMetrics) invalid() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeLocked()
	m.state.InvalidRequests++
}

func (m *ingestionMetrics) enqueue(q *boundedqueue.Queue[ingestDelivery], rec archive.Record) boundedqueue.Rejection {
	return m.admit(q, ingestDelivery{Record: rec})
}

func (m *ingestionMetrics) admit(q *boundedqueue.Queue[ingestDelivery], delivery ingestDelivery) boundedqueue.Rejection {
	rec := delivery.Record
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeLocked()
	weight, err := archive.QueueWeight(rec)
	if err != nil {
		m.state.QueueRejected++
		m.state.OversizeRejected++
		return boundedqueue.Oversize
	}
	reason := q.TryPush(delivery, weight)
	switch reason {
	case boundedqueue.Accepted:
		m.state.EnqueuedDeliveries++
		if rec.Truncated {
			m.state.TruncatedDeliveries++
		}
		return boundedqueue.Accepted
	default:
		m.state.QueueRejected++
		if reason == boundedqueue.ByteLimit {
			m.state.ByteRejected++
		}
		if reason == boundedqueue.Oversize {
			m.state.OversizeRejected++
		}
		return reason
	}
}

func (m *ingestionMetrics) busy() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeLocked()
	m.state.BusyRejected++
}

func (m *ingestionMetrics) bufferRecord() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeLocked()
	m.state.Buffered++
}

func (m *ingestionMetrics) beginWrite(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeLocked()
	m.state.Buffered -= min(n, m.state.Buffered)
	m.state.WriteInFlight += n
}

func (m *ingestionMetrics) committed(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.CommittedDeliveries += uint64(n)
	m.state.WriteInFlight -= n
	m.state.LastCommitAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func (m *ingestionMetrics) writeError(n int, retryable bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.WriteErrors++
	if retryable {
		m.state.Retries++
	} else {
		m.state.DiscardedDeliveries += uint64(n)
		m.state.WriteInFlight -= n
	}
}

func (s *server) ingestionStatus() ingeststatus.Status {
	m := &s.ingestion
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeLocked()
	out := m.state
	queue := s.q.Snapshot()
	out.QueueDepth, out.QueueCapacity = queue.Depth, queue.Capacity
	out.QueueBytes, out.QueueMaxBytes = queue.RetainedBytes, queue.MaxBytes
	return out
}

// Reports are best-effort loopback telemetry, not authenticated durability ACKs.
// Keep only the most recently reporting producer and expose its observation time.
func (m *ingestionMetrics) observeProducer(header string) {
	if header == "" {
		return
	}
	var report archive.CaptureStatus
	if len(header) > 4096 || json.Unmarshal([]byte(header), &report) != nil || report.Version != 1 || report.Epoch == "" || len(report.Epoch) > 128 {
		return
	}
	for _, r := range report.Epoch {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return
		}
	}
	if report.Queue.Depth < 0 || report.Queue.Capacity < 0 || report.Queue.RetainedBytes < 0 || report.Queue.MaxBytes < 0 || report.InFlight < 0 || report.ActiveRequests < 0 {
		return
	}
	if out := report.Outbox; out != nil && (out.Entries < 0 || out.Pending < 0 || out.Quarantined < 0 || out.Bytes < 0 || out.MaxBytes <= 0 || out.MaxEntries <= 0 || out.Pending > out.Entries || out.Quarantined != out.Entries-out.Pending) {
		return
	}
	if _, err := time.Parse(time.RFC3339Nano, report.Since); err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeLocked()
	m.state.ProducerObserved = true
	m.state.Producer = &ingeststatus.ProducerObservation{Capture: report, ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano)}
}
