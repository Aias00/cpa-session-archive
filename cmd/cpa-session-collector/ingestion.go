package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memory"
)

type ingestionMetrics struct {
	mu    sync.Mutex
	state memory.IngestionStatus
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

func (m *ingestionMetrics) enqueue(q chan archive.Record, rec archive.Record) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeLocked()
	select {
	case q <- rec:
		m.state.EnqueuedDeliveries++
		if rec.Truncated {
			m.state.TruncatedDeliveries++
		}
		return true
	default:
		m.state.QueueRejected++
		return false
	}
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

func (s *server) ingestionStatus() memory.IngestionStatus {
	m := &s.ingestion
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeLocked()
	out := m.state
	out.QueueDepth, out.QueueCapacity = len(s.q), cap(s.q)
	return out
}
