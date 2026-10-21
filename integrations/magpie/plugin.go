// Package sessionarchive is an in-tree Magpie plugin. The host's full-body
// observer is internal and is not exposed to community JavaScript middleware.
package sessionarchive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"cpa-session-archive/capture"
	"cpa-session-archive/record"

	"github.com/yetone/magpie/internal/pluginrt"
	"github.com/yetone/magpie/internal/usage"
)

type Settings struct {
	Endpoint string `json:"endpoint" title:"Archive collector endpoint"`
	SpoolDir string `json:"spoolDir" title:"Private outbox directory"`
}

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "session-archive", API: "^1", Requires: []string{"gateway"},
		Name: "Session archive", Sub: "Retain captured conversation evidence in a local archive",
		Settings: Settings{Endpoint: "http://127.0.0.1:8318"},
		Apply: func(ctx *pluginrt.Ctx) error {
			config := capture.DefaultCaptureConfig()
			config.Endpoint, _ = ctx.Config["endpoint"].(string)
			config.SpoolDir, _ = ctx.Config["spoolDir"].(string)
			ctx.OnDispose(usage.Observe(New(config)))
			return nil
		},
	})
}

type Observer struct {
	mu       sync.Mutex
	config   capture.CaptureConfig
	producer *capture.Producer
}

func New(config capture.CaptureConfig) *Observer { return &Observer{config: config} }
func (o *Observer) Bodies() usage.Bodies         { return usage.WholeBodies }
func (o *Observer) Ledger(usage.Record)          {} // Attempts are already captured by Watch.
func (o *Observer) Serve() func() {
	p := capture.NewProducer()
	if err := p.Configure(o.config); err != nil {
		p.Shutdown()
		log.Print("session archive unavailable")
		return func() {}
	}
	o.mu.Lock()
	o.producer = p
	o.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			if o.producer == p {
				o.producer = nil
			}
			o.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = p.Drain(ctx)
			cancel()
			p.Shutdown()
		})
	}
}
func (o *Observer) Begin(q usage.Request) usage.Watch {
	o.mu.Lock()
	p := o.producer
	o.mu.Unlock()
	if p == nil {
		return nil
	}
	// Never retain gateway bearer credentials in a watch or archived metadata.
	return &watch{producer: p, session: q.Session, agent: q.Agent, kind: q.Kind, id: requestID()}
}

type watch struct {
	producer                 *capture.Producer
	session, agent, kind, id string
	attempts                 int
}

func requestID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		panic("cannot allocate archive identity")
	}
	return "magpie:" + hex.EncodeToString(random[:])
}
func (w *watch) Attempt(r usage.Record) {
	w.attempts++
	rec := record.Record{
		RequestID: fmt.Sprintf("%s:%d", w.id, w.attempts), TraceID: w.id,
		Client: w.agent, SourceFormat: "magpie-observer", RequestedModel: r.Requested, Model: r.Model,
		StatusCode: r.Status, Error: r.Error, RequestKind: w.kind,
		StartedAt: r.Time, CompletedAt: r.Time.Add(time.Duration(r.Millis) * time.Millisecond),
		OriginalRequest: []byte(r.BodyIn), Response: []byte(r.BodyOut),
		Facets: map[string][]string{"capture.host": {"magpie"}, "capture.layer": {"scrubbed-observer"}, "provider.target": {r.Provider}},
	}
	if w.session != "" {
		rec.SessionID = "magpie:" + w.session
	}
	if rec.Client == "" {
		rec.Client = r.Agent
	}
	rec.Outcome = "succeeded"
	if r.Status < 200 || r.Status >= 400 || r.Error != "" {
		rec.Outcome = "failed"
	}
	rec.Stream = strings.Contains(r.BodyOut, "data:")
	rec.Truncated = strings.HasSuffix(r.BodyIn, usage.BodyCut) || strings.HasSuffix(r.BodyOut, usage.BodyCut)
	// Magpie scrubs exported bodies. They are evidence of the observer boundary,
	// never a claim to retain the original unsanitized transport bytes.
	if err := w.producer.Submit(rec); err != nil {
		log.Print("session archive admission unavailable")
	}
}
func (w *watch) End(r usage.Record) {
	if w.attempts > 0 {
		return
	}
	// A request rejected before routing still has an attributable diagnostic.
	w.Attempt(r)
}

var _ usage.Observer = (*Observer)(nil)
var _ usage.Watch = (*watch)(nil)
