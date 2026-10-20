package archive

import (
	"crypto/sha256"
	"github.com/tidwall/gjson"
)

// The host emits model-catalog response/completion callbacks without a model
// execution or original request. Require that explicit response shape; never
// infer a catalog solely from a missing body or model name.
func modelCatalogResponse(r intercept) bool {
	if r.StatusCode != 200 || r.Stream || r.Model != "" || r.RequestedModel != "" || len(r.OriginalRequest) > 0 || len(r.RequestBody) > 0 || !gjson.ValidBytes(r.Body) {
		return false
	}
	return (gjson.GetBytes(r.Body, "object").String() == "list" && gjson.GetBytes(r.Body, "data").IsArray()) || gjson.GetBytes(r.Body, "models").IsArray()
}

// Caller holds p.mu. Response/header-init callbacks can recover a missing
// initial hook, but provider RequestBody must never masquerade as client input.
func (p *Plugin) responseState(r intercept, stream bool) *state {
	s := p.active[r.RequestID]
	if s == nil {
		p.metrics.update(func(s *CaptureStatus) { s.RecoveredFromResponse++ })
		s = &state{Record: Record{RequestID: r.RequestID, SourceFormat: r.SourceFormat, Model: r.Model, RequestedModel: r.RequestedModel, Stream: stream, Metadata: sanitizeMeta(r.Metadata), KeyID: archiveKeyID(r.Metadata)}}
		p.active[r.RequestID] = s
	}
	if len(s.OriginalRequest) == 0 && len(r.OriginalRequest) > 0 {
		s.OriginalRequest = limit(r.OriginalRequest, p.max, &s.Truncated)
		enrichDesktopMetadata(&s.Record, r.RequestHeaders)
		enrichGenericFacets(&s.Record, r.RequestHeaders, s.OriginalRequest)
		s.SessionID = sessionID(&s.Record, r.Metadata, s.OriginalRequest)
		s.Summary = extractConversationSummary(s.OriginalRequest)
	}
	if p.storeUpstream && len(s.UpstreamRequest) == 0 && len(r.RequestBody) > 0 {
		s.UpstreamRequest = limit(r.RequestBody, p.max, &s.Truncated)
	}
	return s
}

// Bounded tombstones prevent late chunks from retaining completed requests and
// duplicate completion callbacks from replacing a good record with an empty one.
// Caller holds p.mu; hashes bound retained identifier storage as well as count.
func (p *Plugin) requestFinished(id string) bool {
	_, ok := p.finished[sha256.Sum256([]byte(id))]
	return ok
}

func (p *Plugin) rememberFinished(id string) {
	key := sha256.Sum256([]byte(id))
	if len(p.finished) == len(p.finishOrder) {
		delete(p.finished, p.finishOrder[p.finishCursor])
	}
	p.finished[key] = struct{}{}
	p.finishOrder[p.finishCursor] = key
	p.finishCursor = (p.finishCursor + 1) % len(p.finishOrder)
}
