package cpahost

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	workspace "cpa-session-archive/internal/archive/web"
)

const pluginID = "cpa-session-archive"

var managementHTML = workspace.HTML()

type managementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Body    []byte
}
type managementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

func (p *Plugin) managementRegistration() map[string]any {
	base := "/plugins/" + pluginID
	return map[string]any{"Routes": []map[string]string{{"Method": "GET", "Path": base + "/stats"}, {"Method": "GET", "Path": base + "/facets"}, {"Method": "GET", "Path": base + "/sessions"}, {"Method": "GET", "Path": base + "/session-browser"}, {"Method": "GET", "Path": base + "/session-metadata"}, {"Method": "PUT", "Path": base + "/session-metadata"}, {"Method": "POST", "Path": base + "/session-bulk-preview"}, {"Method": "GET", "Path": base + "/session-bulk"}, {"Method": "POST", "Path": base + "/session-bulk-apply"}, {"Method": "POST", "Path": base + "/session-bulk-undo"}, {"Method": "GET", "Path": base + "/turns"}, {"Method": "GET", "Path": base + "/turn-text"}, {"Method": "GET", "Path": base + "/requests"}, {"Method": "GET", "Path": base + "/request-context"}, {"Method": "GET", "Path": base + "/request-view"}, {"Method": "GET", "Path": base + "/export"}, {"Method": "GET", "Path": base + "/identity-mappings"}, {"Method": "PUT", "Path": base + "/identity-mappings"}}, "Resources": []map[string]string{{"Path": "/index.html", "Menu": "会话归档", "Description": "Browse and organize sessions, inspect sources and export training data."}}}
}
func (p *Plugin) handleManagement(raw []byte) ([]byte, error) {
	var r managementRequest
	if e := json.Unmarshal(raw, &r); e != nil {
		return nil, e
	}
	path := strings.TrimRight(r.Path, "/")
	resource := "/v0/resource/plugins/" + pluginID
	if r.Method == http.MethodGet && strings.HasPrefix(path, resource) {
		status := http.StatusNotFound
		body := []byte("not found")
		if strings.TrimPrefix(path, resource) == "/index.html" {
			status = http.StatusOK
			body = managementHTML
		}
		return ok(managementResponse{
			StatusCode: status,
			Headers: http.Header{
				"Content-Type":  []string{"text/html; charset=utf-8"},
				"Cache-Control": []string{"no-store, max-age=0"},
			},
			Body: body,
		})
	}
	base := "/v0/management/plugins/" + pluginID
	var target string
	drop := map[string]bool{}
	switch {
	case r.Method == http.MethodGet && path == base+"/stats":
		target = "/v1/stats"
	case r.Method == http.MethodGet && path == base+"/facets":
		target = "/v1/facets"
	case r.Method == http.MethodGet && path == base+"/session-browser":
		target = "/v1/session-browser"
	case (r.Method == http.MethodGet || r.Method == http.MethodPut) && path == base+"/session-metadata":
		target = "/v1/session-metadata"
	case r.Method == http.MethodGet && path == base+"/session-bulk":
		target = "/v1/session-bulk"
	case r.Method == http.MethodPost && (path == base+"/session-bulk-preview" || path == base+"/session-bulk-apply" || path == base+"/session-bulk-undo"):
		target = "/v1/" + strings.TrimPrefix(path, base+"/")
		mediaType, _, err := mime.ParseMediaType(r.Headers.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			return ok(managementResponse{StatusCode: http.StatusUnsupportedMediaType, Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: []byte(`{"code":"invalid_content_type","error":"application/json required"}`)})
		}
		if len(r.Body) > 65536 {
			return ok(managementResponse{StatusCode: http.StatusBadRequest, Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: []byte(`{"code":"invalid_update","error":"bounded JSON object required"}`)})
		}
	case (r.Method == http.MethodGet || r.Method == http.MethodPut) && path == base+"/identity-mappings":
		target = "/v1/identity-mappings"
	case r.Method == http.MethodGet && path == base+"/sessions":
		if id := r.Query.Get("id"); id != "" {
			target = "/v1/sessions/" + url.PathEscape(id)
			drop["id"] = true
		} else {
			target = "/v1/sessions"
		}
	case r.Method == http.MethodGet && path == base+"/requests":
		target = "/v1/requests/" + url.PathEscape(r.Query.Get("id"))
		drop["id"] = true
	case r.Method == http.MethodGet && path == base+"/request-context":
		target = "/v1/request-context"
	case r.Method == http.MethodGet && path == base+"/request-view":
		target = "/v1/request-view"
	case r.Method == http.MethodGet && path == base+"/turns":
		target = "/v1/turns"
	case r.Method == http.MethodGet && path == base+"/turn-text":
		target = "/v1/turn-text"
	case r.Method == http.MethodGet && path == base+"/export":
		target = "/v1/export-tickets"
		if id := r.Query.Get("id"); id != "" {
			r.Query.Set("session_id", id)
		}
		drop["id"] = true
	default:
		return ok(managementResponse{StatusCode: 404, Body: []byte("{\"error\":\"not found\"}")})
	}
	p.mu.Lock()
	endpoint, client := p.endpoint, p.client
	p.mu.Unlock()
	u := strings.TrimSuffix(endpoint, "/ingest") + target
	if len(r.Query) > 0 {
		q := url.Values{}
		for k, v := range r.Query {
			if !drop[k] {
				q[k] = v
			}
		}
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
	}
	req, err := http.NewRequest(r.Method, u, bytes.NewReader(r.Body))
	if err != nil {
		return ok(managementResponse{StatusCode: http.StatusBadRequest, Body: []byte(`{"error":"invalid management request"}`)})
	}
	if len(r.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	// The authenticated host boundary terminates here; never follow a collector
	// redirect or forward its caller's management credential to another origin.
	managementClient := *client
	managementClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := managementClient.Do(req)
	if e != nil {
		body, _ := json.Marshal(map[string]any{"error": "collector unavailable", "capture": p.CaptureStatus()})
		return ok(managementResponse{StatusCode: 502, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: body})
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if target == "/v1/stats" {
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) == nil && fields != nil {
			capture, _ := json.Marshal(p.CaptureStatus())
			fields["capture"] = capture
			body, _ = json.Marshal(fields)
		}
	}
	headers := http.Header{"Content-Type": []string{resp.Header.Get("Content-Type")}}
	if v := resp.Header.Get("Content-Disposition"); v != "" {
		headers.Set("Content-Disposition", v)
	}
	for _, name := range []string{"Cache-Control", "X-Content-Type-Options", "Allow"} {
		if value := resp.Header.Get(name); value != "" {
			headers.Set(name, value)
		}
	}
	return ok(managementResponse{StatusCode: resp.StatusCode, Headers: headers, Body: body})
}
