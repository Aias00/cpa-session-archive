package capture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cpa-session-archive/internal/boundedqueue"
	"cpa-session-archive/internal/outbox"
	"cpa-session-archive/internal/sessionmodel"
)

type state struct {
	Record
	chunks  bytes.Buffer
	catalog bool
}
type Producer struct {
	mu              sync.Mutex
	active          map[string]*state
	q               *boundedqueue.Queue[Record]
	metrics         captureMetrics
	spool           *outbox.Store
	spoolDir        string
	spoolMaxBytes   int64
	spoolMaxEntries int
	spoolWake       chan struct{}
	spoolClose      sync.Once
	spoolStatusMu   sync.Mutex
	closed          bool
	finished        map[[32]byte]struct{}
	finishOrder     [4096][32]byte
	finishCursor    int
	ctx             context.Context
	cancel          context.CancelFunc
	stop            chan struct{}
	client          *http.Client
	endpoint        string
	max             int
	storeUpstream   bool
	wg              sync.WaitGroup
}
type CaptureEvent struct {
	RequestID, TraceID, SourceFormat, Model, RequestedModel string
	Stream                                                  bool
	Headers                                                 http.Header
	RequestHeaders                                          http.Header
	OriginalRequest, RequestBody, Body                      []byte
	StatusCode                                              int
	Metadata                                                map[string]any
	ChunkIndex                                              int
}
type CompletionEvent struct {
	RequestID, TraceID, SourceFormat, Model, RequestedModel, Outcome, Error string
	Stream                                                                  bool
	StatusCode                                                              int
	StartedAt, CompletedAt                                                  time.Time
	Metadata                                                                map[string]any
}

func NewProducer() *Producer {
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Producer{active: map[string]*state{}, finished: make(map[[32]byte]struct{}), stop: make(chan struct{}), client: &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, max: 64 << 20, ctx: ctx, cancel: cancel}
}
func (p *Producer) Configure(c CaptureConfig) error {
	if c.QueueMaxBytes <= 0 {
		return errors.New("invalid archive queue configuration")
	}
	if c.SpoolDir != "" && (!filepath.IsAbs(c.SpoolDir) || c.SpoolMaxBytes <= 0 || c.SpoolMaxEntries <= 0) {
		return errors.New("invalid archive outbox configuration")
	}
	if c.QueueSize < 64 {
		c.QueueSize = 64
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("archive plugin is stopped")
	}
	if p.q != nil && (c.SpoolDir != p.spoolDir || (p.spool != nil && (c.SpoolMaxBytes != p.spoolMaxBytes || c.SpoolMaxEntries != p.spoolMaxEntries))) {
		return errors.New("changing outbox configuration requires plugin restart")
	}
	if p.q == nil && c.SpoolDir != "" {
		spool, err := outbox.Open(filepath.Join(c.SpoolDir, "deliveries.sqlite"), c.SpoolMaxBytes, c.SpoolMaxEntries)
		if err != nil {
			return errors.New("archive outbox unavailable")
		}
		p.spool, p.spoolDir, p.spoolMaxBytes, p.spoolMaxEntries = spool, c.SpoolDir, c.SpoolMaxBytes, c.SpoolMaxEntries
		p.spoolWake = make(chan struct{}, 1)
		p.refreshSpoolStatus()
	}
	if c.MaxBodyBytes > 0 {
		p.max = c.MaxBodyBytes
	}
	if d, e := time.ParseDuration(c.Timeout); e == nil {
		client := *p.client
		client.Timeout = d
		p.client = &client
	}
	endpoint := strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080"
	}
	p.endpoint = endpoint + "/ingest"
	p.storeUpstream = c.StoreUpstreamRequest
	if p.q == nil {
		queue, err := boundedqueue.New[Record](c.QueueSize, c.QueueMaxBytes)
		if err != nil {
			return err
		}
		p.q = queue
		if p.spool == nil {
			p.wg.Add(1)
			go p.sender()
		} else {
			p.wg.Add(2)
			go p.spoolWriter()
			go p.spoolSender()
		}
	} else {
		if err := p.q.SetMaxBytes(c.QueueMaxBytes); err != nil {
			return err
		}
	}
	return nil
}
func (p *Producer) CaptureRequest(r CaptureEvent, afterAuth bool) {
	if r.RequestID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.requestFinished(r.RequestID) {
		return
	}
	s := p.active[r.RequestID]
	if s == nil {
		s = &state{}
		s.RequestID = r.RequestID
		p.active[r.RequestID] = s
	}
	s.TraceID = r.TraceID
	s.SourceFormat = r.SourceFormat
	s.Model = r.Model
	s.RequestedModel = r.RequestedModel
	s.Stream = r.Stream
	s.Metadata = sessionmodel.SanitizeMeta(r.Metadata)
	if !afterAuth {
		sessionmodel.EnrichDesktopMetadata(&s.Record, r.Headers)
	}
	if afterAuth {
		if p.storeUpstream && len(r.Body) > 0 {
			s.UpstreamRequest = sessionmodel.Limit(r.Body, p.max, &s.Truncated)
		}
	} else {
		if len(r.Body) > 0 {
			s.OriginalRequest = sessionmodel.Limit(r.Body, p.max, &s.Truncated)
		}
		sessionmodel.EnrichGenericFacets(&s.Record, r.Headers, r.Body)
		if s.Summary == "" {
			s.Summary = sessionmodel.ExtractConversationSummary(r.Body)
		}
	}
	s.SessionID = sessionmodel.SessionID(&s.Record, r.Metadata, s.OriginalRequest, s.UpstreamRequest)
	s.ParentResponseID = sessionmodel.FirstJSON(s.OriginalRequest, s.UpstreamRequest, "previous_response_id")
	keyID := sessionmodel.ArchiveKeyID(r.Metadata)
	principalID := sessionmodel.FindString(r.Metadata, "principal_id")
	credentialHash := sessionmodel.FirstNonEmpty(
		sessionmodel.FindString(r.Metadata, "credential_hash"),
		sessionmodel.FindString(r.Metadata, "key_hash"),
		sessionmodel.FindString(r.Metadata, "api_key_hash"),
	)
	if headerKeyID := sessionmodel.NativeKeyHash(r.Headers); headerKeyID != "" {
		keyID = headerKeyID
		if credentialHash == "" {
			credentialHash = headerKeyID
		}
	}
	// The post-auth hook may no longer carry the Authorization header and its
	// metadata can contain only the host-salted caller_scope. Never downgrade a
	// native identity captured by the initial request hook.
	if s.KeyID == "" || keyID != sessionmodel.FindString(r.Metadata, "caller_scope") {
		s.KeyID = keyID
	}
	if principalID != "" {
		s.PrincipalID = principalID
	}
	if credentialHash != "" {
		s.CredentialHash = credentialHash
	}
}
func (p *Producer) CaptureResponse(r CaptureEvent) {
	if r.RequestID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.requestFinished(r.RequestID) {
		return
	}
	if p.active[r.RequestID] == nil && modelCatalogResponse(r) {
		p.active[r.RequestID] = &state{catalog: true}
		return
	}
	s := p.responseState(r, false)
	s.StatusCode = r.StatusCode
	s.Response = sessionmodel.Limit(r.Body, p.max, &s.Truncated)
	s.ResponseID = sessionmodel.FirstJSON(r.Body, "id")
}
func (p *Producer) CaptureChunk(r CaptureEvent) {
	if r.ChunkIndex < -1 || r.RequestID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.requestFinished(r.RequestID) {
		return
	}
	s := p.responseState(r, true)
	if r.ChunkIndex == -1 {
		return
	}
	if s != nil {
		remaining := p.max - s.chunks.Len()
		if remaining <= 0 {
			s.Truncated = true
			return
		}
		b := r.Body
		if len(b) > remaining {
			b = b[:remaining]
			s.Truncated = true
		}
		s.chunks.Write(b)
	}
}
func (p *Producer) Complete(r CompletionEvent) {
	if r.RequestID == "" {
		p.metrics.update(func(s *CaptureStatus) { s.InvalidDropped++ })
		return
	}
	p.metrics.update(func(s *CaptureStatus) { s.Completed++ })
	p.mu.Lock()
	if p.requestFinished(r.RequestID) {
		p.mu.Unlock()
		p.metrics.update(func(s *CaptureStatus) { s.DuplicateCompletions++ })
		return
	}
	p.rememberFinished(r.RequestID)
	s := p.active[r.RequestID]
	delete(p.active, r.RequestID)
	p.mu.Unlock()
	if s == nil {
		p.metrics.update(func(s *CaptureStatus) { s.MissingRequestState++ })
		s = &state{Record: Record{RequestID: r.RequestID}}
	}
	if s.catalog {
		p.metrics.update(func(s *CaptureStatus) { s.CatalogSkipped++ })
		return
	}
	s.TraceID = r.TraceID
	s.SourceFormat = r.SourceFormat
	s.Model = r.Model
	s.RequestedModel = r.RequestedModel
	s.Stream = r.Stream
	s.Outcome = r.Outcome
	s.StatusCode = r.StatusCode
	s.Error = r.Error
	s.StartedAt = r.StartedAt
	s.CompletedAt = r.CompletedAt
	if s.SessionID == "" || s.SessionID == "request:" {
		s.SessionID = sessionmodel.SessionID(&s.Record, r.Metadata, s.OriginalRequest, s.UpstreamRequest)
	}
	if s.SessionID == "" || s.SessionID == "request:" {
		s.SessionID = "request:" + r.RequestID
	}
	if len(s.Response) == 0 && s.chunks.Len() > 0 {
		s.Response = append([]byte(nil), s.chunks.Bytes()...)
	}
	if s.ResponseID == "" {
		s.ResponseID = sessionmodel.ResponseID(s.Response)
	}
	if s.ResponsePreview == "" {
		s.ResponsePreview = sessionmodel.ExtractResponsePreview(s.Response)
	}
	sessionmodel.AddCompletionFacets(&s.Record)
	p.enqueue(s.Record)
}

func (p *Producer) sender() {
	defer p.wg.Done()
	for {
		select {
		case item := <-p.q.Items():
			p.metrics.update(func(s *CaptureStatus) { s.InFlight++ })
			result := p.sendWithRetry(item.Value)
			item.Done()
			p.metrics.update(func(s *CaptureStatus) {
				s.InFlight--
				switch result {
				case deliveryAccepted:
					s.HTTPAccepted++
				case deliveryRejected:
					s.DeliveryRejected++
				case deliveryStopped:
					s.ShutdownDiscarded++
				}
			})
		case <-p.stop:
			return
		}
	}
}

type deliveryResult int

const (
	deliveryAccepted deliveryResult = iota
	deliveryRejected
	deliveryStopped
)

func (p *Producer) sendWithRetry(rec Record) deliveryResult {
	b, err := json.Marshal(rec)
	if err != nil {
		return deliveryRejected
	}
	result, _ := p.sendPayloadWithRetry(b, false)
	return result
}

func (p *Producer) sendPayloadWithRetry(b []byte, commit bool) (deliveryResult, int) {
	digest := sha256.Sum256(b)
	expected := fmt.Sprintf("%x", digest)
	backoff := 250 * time.Millisecond
	for {
		select {
		case <-p.stop:
			return deliveryStopped, 0
		default:
		}
		p.mu.Lock()
		endpoint, client, ctx := p.endpoint, p.client, p.ctx
		p.mu.Unlock()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
		if err != nil {
			if !commit {
				return deliveryRejected, 0
			}
			p.metrics.update(func(s *CaptureStatus) { s.DeliveryRetries++ })
			if !p.waitSpoolRetry() {
				return deliveryStopped, 0
			}
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if commit {
			req.Header.Set(CommitAckHeader, CommitAckRequest)
		}
		status, _ := json.Marshal(p.CaptureStatus())
		req.Header.Set(CaptureStatusHeader, string(status))
		resp, err := client.Do(req)
		code := 0
		if err == nil {
			resp.Body.Close()
			code = resp.StatusCode
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if !commit || (code == http.StatusOK && resp.Header.Get(CommitAckHeader) == CommitAckResponse && resp.Header.Get(CommitDigestHeader) == expected) {
					return deliveryAccepted, code
				}
				p.metrics.update(func(s *CaptureStatus) { s.CommitReceiptMissing++ })
			}
			if code == 400 || code == 413 || code == 422 {
				return deliveryRejected, code
			}
		}
		p.metrics.update(func(s *CaptureStatus) { s.DeliveryRetries++ })
		log.Printf("archive delivery retry: status=%d backoff=%s", code, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-p.stop:
			if !timer.Stop() {
				<-timer.C
			}
			return deliveryStopped, 0
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}
func (p *Producer) Shutdown() {
	defer p.spoolClose.Do(func() {
		if p.spool != nil {
			_ = p.spool.Close()
		}
	})
	p.mu.Lock()
	select {
	case <-p.stop:
	default:
		p.closed = true
		p.metrics.update(func(s *CaptureStatus) { s.ShutdownActiveDiscarded += uint64(len(p.active)) })
		clear(p.active)
		close(p.stop)
		p.cancel()
	}
	p.mu.Unlock()
	p.wg.Wait()
	for {
		select {
		case item := <-p.q.Items():
			item.Done()
			p.metrics.update(func(s *CaptureStatus) { s.ShutdownDiscarded++ })
		default:
			return
		}
	}
}
