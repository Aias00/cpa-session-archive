package memory

import "context"

// IngestionStatus describes this collector's HTTP delivery attempts, not unique
// records or a durable loss ledger. Native imports bypass this boundary.
type IngestionStatus struct {
	Epoch               string `json:"epoch"`
	Since               string `json:"since"`
	Boundary            string `json:"boundary"`
	Acknowledgement     string `json:"acknowledgement"`
	ProducerObserved    bool   `json:"producer_observed"`
	ReceivedRequests    uint64 `json:"received_requests"`
	InvalidRequests     uint64 `json:"invalid_requests"`
	EnqueuedDeliveries  uint64 `json:"enqueued_deliveries"`
	QueueRejected       uint64 `json:"queue_rejected"`
	TruncatedDeliveries uint64 `json:"truncated_deliveries"`
	CommittedDeliveries uint64 `json:"committed_deliveries"`
	DiscardedDeliveries uint64 `json:"discarded_deliveries"`
	WriteErrors         uint64 `json:"write_errors"`
	Retries             uint64 `json:"retries"`
	QueueDepth          int    `json:"queue_depth"`
	QueueCapacity       int    `json:"queue_capacity"`
	Buffered            int    `json:"buffered"`
	WriteInFlight       int    `json:"write_in_flight"`
	LastCommitAt        string `json:"last_commit_at,omitempty"`
}

type diagnosticReader struct {
	Reader
	diagnostics func() IngestionStatus
}

// WithIngestionStatus attaches collector-owned diagnostics at the read boundary.
// Apply credential scoping outside this decorator so global counts are removed.
func WithIngestionStatus(reader Reader, diagnostics func() IngestionStatus) Reader {
	if diagnostics == nil {
		return reader
	}
	return &diagnosticReader{Reader: reader, diagnostics: diagnostics}
}

func (r *diagnosticReader) annotate(s Status) Status {
	snapshot := r.diagnostics()
	s.Ingestion = &snapshot
	return s
}

func (r *diagnosticReader) Status(ctx context.Context) (Status, error) {
	s, err := r.Reader.Status(ctx)
	return r.annotate(s), err
}

func (r *diagnosticReader) Search(ctx context.Context, q Query) (SearchResult, error) {
	result, err := r.Reader.Search(ctx, q)
	result.Status = r.annotate(result.Status)
	return result, err
}

func (r *diagnosticReader) Recap(ctx context.Context, q Query) (RecapResult, error) {
	result, err := r.Reader.Recap(ctx, q)
	result.Status = r.annotate(result.Status)
	return result, err
}
