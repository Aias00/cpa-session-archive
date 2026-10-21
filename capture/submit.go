package capture

import (
	"context"
	"errors"
	"time"

	"cpa-session-archive/internal/sessionmodel"
)

// Submit accepts a completed host record without emulating CPA lifecycle hooks.
// Acceptance is bounded in-memory admission, not an archive commit receipt.
// It normalizes metadata/previews without translating the bounded raw bodies.
func (p *Producer) Submit(rec Record) error {
	if rec.RequestID == "" {
		return errors.New("capture requires a request ID")
	}
	p.mu.Lock()
	if p.closed || p.q == nil {
		p.mu.Unlock()
		return errors.New("capture is not running")
	}
	maxBody := p.max
	p.mu.Unlock()
	rec.OriginalRequest = sessionmodel.Limit(rec.OriginalRequest, maxBody, &rec.Truncated)
	rec.UpstreamRequest = sessionmodel.Limit(rec.UpstreamRequest, maxBody, &rec.Truncated)
	rec.Response = sessionmodel.Limit(rec.Response, maxBody, &rec.Truncated)
	// Rebuild mutable facets before deriving them; the caller retains its own map.
	facets := make(map[string][]string, len(rec.Facets))
	for k, v := range rec.Facets {
		facets[k] = append([]string(nil), v...)
	}
	rec.Facets = facets
	rec.Metadata = sessionmodel.SanitizeMeta(rec.Metadata)
	if rec.SessionID == "" {
		rec.SessionID = sessionmodel.SessionID(&rec, rec.Metadata, rec.OriginalRequest)
	}
	if rec.SessionID == "" || rec.SessionID == "request:" {
		rec.SessionID = "request:" + rec.RequestID
	}
	sessionmodel.EnrichGenericFacets(&rec, nil, rec.OriginalRequest)
	if rec.Summary == "" {
		rec.Summary = sessionmodel.ExtractConversationSummary(rec.OriginalRequest)
	}
	if rec.ResponsePreview == "" {
		rec.ResponsePreview = sessionmodel.ExtractResponsePreview(rec.Response)
	}
	if rec.ResponseID == "" {
		rec.ResponseID = sessionmodel.ResponseID(rec.Response)
	}
	sessionmodel.AddCompletionFacets(&rec)
	p.metrics.update(func(s *CaptureStatus) { s.Completed++ })
	if !p.admit(rec, true) {
		return errors.New("capture admission rejected")
	}
	return nil
}

// Drain waits for volatile admission to reach delivery or the durable outbox.
// It does not require the collector to be online once the outbox is persisted.
func (p *Producer) Drain(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s := p.CaptureStatus(); s.Queue.Depth == 0 && s.Queue.RetainedBytes == 0 && (s.OutboxEnabled || s.InFlight == 0) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
