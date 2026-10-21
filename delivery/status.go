package delivery

import (
	"cpa-session-archive/internal/boundedqueue"
)

const CaptureStatusHeader = "X-CPA-Archive-Capture"

// CaptureStatus is process-local producer telemetry, never a durable loss ledger.
type CaptureStatus struct {
	OutboxEnabled           bool                  `json:"outbox_enabled"`
	OutboxCapacityWaits     uint64                `json:"outbox_capacity_waits"`
	Outbox                  *OutboxStatus         `json:"outbox,omitempty"`
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

type OutboxStatus struct {
	Entries     int   `json:"entries"`
	Pending     int   `json:"pending"`
	Quarantined int   `json:"quarantined"`
	Bytes       int64 `json:"bytes"`
	MaxBytes    int64 `json:"max_bytes"`
	MaxEntries  int   `json:"max_entries"`
}
