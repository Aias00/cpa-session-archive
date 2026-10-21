package archive

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"cpa-session-archive/internal/outbox"
)

// Status is cached by delivery workers; capture callbacks never query SQLite.
func (p *Plugin) refreshSpoolStatus() {
	p.spoolStatusMu.Lock()
	defer p.spoolStatusMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := p.spool.Status(ctx)
	if err == nil {
		p.metrics.update(func(s *CaptureStatus) { s.Outbox = &status })
	}
}

func (p *Plugin) waitSpoolRetry() bool {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-p.stop:
		return false
	case <-timer.C:
		return true
	}
}

// Persistence is asynchronous to response capture. The in-memory reservation
// remains charged until persistence succeeds or shutdown discards unspooled work.
func (p *Plugin) spoolWriter() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stop:
			return
		case item := <-p.q.Items():
			p.persistDelivery(item.Value)
			item.Done()
		}
	}
}

func (p *Plugin) persistDelivery(rec Record) {
	body, err := json.Marshal(rec)
	if err != nil {
		p.metrics.update(func(s *CaptureStatus) { s.InvalidDropped++ })
		return
	}
	for {
		_, inserted, err := p.spool.Put(p.ctx, body)
		if err == nil {
			p.metrics.update(func(s *CaptureStatus) {
				if inserted {
					s.OutboxPersisted++
				} else {
					s.OutboxDuplicates++
				}
			})
			p.refreshSpoolStatus()
			select {
			case p.spoolWake <- struct{}{}:
			default:
			}
			return
		}
		if errors.Is(err, outbox.ErrOversize) {
			p.metrics.update(func(s *CaptureStatus) { s.OutboxOversizeDropped++ })
			return
		}
		p.metrics.update(func(s *CaptureStatus) {
			if errors.Is(err, outbox.ErrFull) {
				s.OutboxCapacityWaits++
			} else {
				s.OutboxWriteErrors++
			}
		})
		if !p.waitSpoolRetry() {
			p.metrics.update(func(s *CaptureStatus) { s.ShutdownDiscarded++ })
			return
		}
	}
}

// One replay worker preserves admission order; quarantine retains permanent
// rejections while allowing the next pending delivery to proceed.
func (p *Plugin) spoolSender() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stop:
			return
		default:
		}
		entry, err := p.spool.Next(p.ctx)
		if err != nil {
			if !errors.Is(err, outbox.ErrEmpty) {
				p.metrics.update(func(s *CaptureStatus) { s.OutboxReadErrors++ })
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-p.stop:
				timer.Stop()
				return
			case <-p.spoolWake:
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		p.metrics.update(func(s *CaptureStatus) { s.InFlight++ })
		result, code := p.sendPayloadWithRetry(entry.Body, true)
		p.metrics.update(func(s *CaptureStatus) { s.InFlight-- })
		if result == deliveryStopped {
			return
		}
		for {
			if result == deliveryAccepted {
				err = p.spool.Acknowledge(p.ctx, entry.Digest)
			} else {
				err = p.spool.Quarantine(p.ctx, entry.Digest, code)
			}
			if err == nil {
				break
			}
			p.metrics.update(func(s *CaptureStatus) { s.OutboxWriteErrors++ })
			if !p.waitSpoolRetry() {
				return
			}
		}
		p.metrics.update(func(s *CaptureStatus) {
			if result == deliveryAccepted {
				s.HTTPAccepted++
				s.OutboxCommitted++
			} else {
				s.DeliveryRejected++
				s.OutboxQuarantined++
			}
		})
		p.refreshSpoolStatus()
	}
}
