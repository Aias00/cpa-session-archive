// Package cpahost adapts CPA's plugin wire protocol to shared capture.
package cpahost

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"cpa-session-archive/capture"
	"cpa-session-archive/internal/buildinfo"

	"gopkg.in/yaml.v3"
)

type Plugin struct {
	mu       sync.Mutex
	producer *capture.Producer
	endpoint string
	client   *http.Client
}
type lifecycle struct {
	ConfigYAML []byte `json:"config_yaml"`
}
type envelope struct {
	OK     bool `json:"ok"`
	Result any  `json:"result,omitempty"`
	Error  any  `json:"error,omitempty"`
}

func NewPlugin() *Plugin {
	return &Plugin{producer: capture.NewProducer(), client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext}}}
}
func (p *Plugin) Shutdown() {
	if p.producer != nil {
		p.producer.Shutdown()
	}
}
func (p *Plugin) CaptureStatus() capture.CaptureStatus {
	if p.producer == nil {
		return capture.CaptureStatus{}
	}
	return p.producer.CaptureStatus()
}

func ok(v any) ([]byte, error) { return json.Marshal(envelope{OK: true, Result: v}) }
func (p *Plugin) Handle(method string, raw []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return p.configure(raw)
	case "management.register":
		return ok(p.managementRegistration())
	case "management.handle":
		return p.handleManagement(raw)
	case "request.intercept_before":
		var r capture.CaptureEvent
		if json.Unmarshal(raw, &r) != nil {
			return nil, errors.New("invalid request")
		}
		p.producer.CaptureRequest(r, false)
		return ok(map[string]any{})
	case "request.intercept_after":
		var r capture.CaptureEvent
		if json.Unmarshal(raw, &r) != nil {
			return nil, errors.New("invalid request")
		}
		p.producer.CaptureRequest(r, true)
		return ok(map[string]any{})
	case "response.intercept_after":
		var r capture.CaptureEvent
		if json.Unmarshal(raw, &r) != nil {
			return nil, errors.New("invalid response")
		}
		p.producer.CaptureResponse(r)
		return ok(map[string]any{})
	case "response.intercept_stream_chunk":
		var r capture.CaptureEvent
		if json.Unmarshal(raw, &r) != nil {
			return nil, errors.New("invalid chunk")
		}
		p.producer.CaptureChunk(r)
		return ok(map[string]any{})
	case "request.complete":
		var r capture.CompletionEvent
		if json.Unmarshal(raw, &r) != nil {
			return nil, errors.New("invalid completion")
		}
		p.producer.Complete(r)
		return ok(map[string]any{})
	default:
		return nil, errors.New("unknown method")
	}
}

func (p *Plugin) configure(raw []byte) ([]byte, error) {
	var l lifecycle
	if json.Unmarshal(raw, &l) != nil {
		return nil, errors.New("invalid plugin lifecycle")
	}
	c := capture.DefaultCaptureConfig()
	if yaml.Unmarshal(l.ConfigYAML, &c) != nil {
		return nil, errors.New("invalid archive queue configuration")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.producer.Configure(c); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080"
	}
	p.endpoint = endpoint + "/ingest"
	if d, err := time.ParseDuration(c.Timeout); err == nil {
		client := *p.client
		client.Timeout = d
		p.client = &client
	}
	return ok(map[string]any{"schema_version": 5, "metadata": map[string]any{"Name": "cpa-session-archive", "Version": buildinfo.Version, "Author": "OneTwo", "GitHubRepository": "https://github.com/linonetwo/cpa-session-archive", "ConfigFields": []any{}}, "capabilities": map[string]any{"request_interceptor": true, "request_lifecycle_plugin": true, "response_interceptor": true, "response_stream_interceptor": true, "management_api": true}})
}
