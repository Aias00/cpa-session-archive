package capture

import (
	"fmt"
	"os"
	"sync"
	"time"

	"cpa-session-archive/internal/boundedqueue"
)

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

func (p *Producer) CaptureStatus() CaptureStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out CaptureStatus
	p.metrics.update(func(s *CaptureStatus) { out = *s })
	out.OutboxEnabled = p.spool != nil
	out.ActiveRequests = len(p.active)
	out.Queue = p.q.Snapshot()
	return out
}

func (p *Producer) enqueue(rec Record) bool { return p.admit(rec, false) }

func (p *Producer) admit(rec Record, deduplicate bool) bool {
	weight, err := QueueWeight(rec)
	p.mu.Lock()
	defer p.mu.Unlock()
	admitted := false
	p.metrics.update(func(s *CaptureStatus) {
		if deduplicate && p.requestFinished(rec.RequestID) {
			s.DuplicateCompletions++
			admitted = true
			return
		}
		if p.closed || p.q == nil {
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
			admitted = true
			if deduplicate {
				p.rememberFinished(rec.RequestID)
			}
		case boundedqueue.CountLimit:
			s.QueueDropped++
		case boundedqueue.ByteLimit:
			s.ByteDropped++
		case boundedqueue.Oversize:
			s.OversizeDropped++
		}
	})
	return admitted
}
