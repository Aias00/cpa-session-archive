// Package ingeststatus defines collector telemetry shared by independent readers.
package ingeststatus

import "cpa-session-archive/internal/archive"

type ProducerObservation struct {
	Capture    archive.CaptureStatus `json:"capture"`
	ReceivedAt string                `json:"received_at"`
}

// Status counts this collector's HTTP delivery attempts, not unique records or
// a durable loss ledger. Native imports bypass this boundary.
type Status struct {
	Epoch               string               `json:"epoch"`
	Since               string               `json:"since"`
	Boundary            string               `json:"boundary"`
	Acknowledgement     string               `json:"acknowledgement"`
	ProducerObserved    bool                 `json:"producer_observed"`
	Producer            *ProducerObservation `json:"producer,omitempty"`
	ReceivedRequests    uint64               `json:"received_requests"`
	InvalidRequests     uint64               `json:"invalid_requests"`
	BusyRejected        uint64               `json:"busy_rejected"`
	EnqueuedDeliveries  uint64               `json:"enqueued_deliveries"`
	QueueRejected       uint64               `json:"queue_rejected"`
	ByteRejected        uint64               `json:"byte_rejected"`
	OversizeRejected    uint64               `json:"oversize_rejected"`
	TruncatedDeliveries uint64               `json:"truncated_deliveries"`
	CommittedDeliveries uint64               `json:"committed_deliveries"`
	DiscardedDeliveries uint64               `json:"discarded_deliveries"`
	WriteErrors         uint64               `json:"write_errors"`
	Retries             uint64               `json:"retries"`
	QueueDepth          int                  `json:"queue_depth"`
	QueueCapacity       int                  `json:"queue_capacity"`
	QueueBytes          int64                `json:"queue_bytes"`
	QueueMaxBytes       int64                `json:"queue_max_bytes"`
	Buffered            int                  `json:"buffered"`
	WriteInFlight       int                  `json:"write_in_flight"`
	LastCommitAt        string               `json:"last_commit_at,omitempty"`
}
