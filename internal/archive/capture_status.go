package archive

import (
	"fmt"
	"os"
	"sync"
	"time"

	"cpa-session-archive/internal/boundedqueue"
	"cpa-session-archive/internal/outbox"
)

const CaptureStatusHeader = "X-CPA-Archive-Capture"

// CaptureStatus is process-local producer telemetry, never a durable loss ledger.
type CaptureStatus struct {
	OutboxEnabled           bool                  `json:"outbox_enabled"`
	OutboxCapacityWaits     uint64                `json:"outbox_capacity_waits"`
	Outbox                  *outbox.Status        `json:"outbox,omitempty"`
	OutboxPersisted         uint64                `json:"outbox_persisted"`
	OutboxDuplicates        uint64                `json:"outbox_duplicates"`
	OutboxWriteErrors       uint64                `json:"outbox_write_errors"`
	OutboxReadErrors        uint64                `json:"outbox_read_errors"`
	OutboxOversizeDropped   uint64                `json:"outbox_oversize_dropped"`
	OutboxCommitted         uint64                `json:"outbox_committed"`
	OutboxQuarantined       uint64                `json:"outbox_quarantined"`
	CommitReceiptMissing    uint64                `json:"commit_receipt_missing"`
	Version                 int                   `json:"version"`
	Epoch                   string                `json:"epoch"`
	Since                   string                `json:"since"`
	Completed               uint64                `json:"completed"`
	Enqueued                uint64                `json:"enqueued"`
	QueueDropped            uint64                `json:"queue_dropped"`
	ByteDropped             uint64                `json:"byte_dropped"`
	OversizeDropped         uint64                `json:"oversize_dropped"`
	InvalidDropped          uint64                `json:"invalid_dropped"`
	HTTPAccepted            uint64                `json:"http_accepted"`
	DeliveryRetries         uint64                `json:"delivery_retries"`
	DeliveryRejected        uint64                `json:"delivery_rejected"`
	ShutdownDiscarded       uint64                `json:"shutdown_discarded"`
	ShutdownActiveDiscarded uint64                `json:"shutdown_active_discarded"`
	DuplicateCompletions    uint64                `json:"duplicate_completions"`
	RecoveredFromResponse   uint64                `json:"recovered_from_response"`
	CatalogSkipped          uint64                `json:"catalog_skipped"`
	MissingRequestState     uint64                `json:"missing_request_state"`
	InFlight                int                   `json:"in_flight"`
	ActiveRequests          int                   `json:"active_requests"`
	Queue                   boundedqueue.Snapshot `json:"queue"`
}

type captureMetrics struct {
	mu    sync.Mutex
	value CaptureStatus
}

func (m *captureMetrics) update(fn func(*CaptureStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.value.Epoch == "" {
		now := time.Now().UTC()
		m.value = CaptureStatus{Version: 1, Epoch: fmt.Sprintf("%d-%d", os.Getpid(), now.UnixNano()), Since: now.Format(time.RFC3339Nano)}
	}
	fn(&m.value)
}

func (p *Plugin) CaptureStatus() CaptureStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out CaptureStatus
	p.metrics.update(func(s *CaptureStatus) { out = *s })
	out.OutboxEnabled = p.spool != nil
	out.ActiveRequests = len(p.active)
	out.Queue = p.q.Snapshot()
	return out
}

func (p *Plugin) enqueue(rec Record) {
	weight, err := QueueWeight(rec)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.metrics.update(func(s *CaptureStatus) {
		if p.closed {
			s.ShutdownDiscarded++
			return
		}
		if err != nil {
			s.InvalidDropped++
			return
		}
		switch p.q.TryPush(rec, weight) {
		case boundedqueue.Accepted:
			s.Enqueued++
		case boundedqueue.CountLimit:
			s.QueueDropped++
		case boundedqueue.ByteLimit:
			s.ByteDropped++
		case boundedqueue.Oversize:
			s.OversizeDropped++
		}
	})
}
