package cpahost

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"testing"
	"unicode/utf8"
)

func TestManagementUIEmbedsSelfContainedWorkspace(t *testing.T) {
	if !utf8.Valid(managementHTML) {
		t.Fatal("embedded workspace must be valid UTF-8")
	}
	for _, required := range []string{`id="root"`, `data-ui="shadcn"`, `<script type="module"`, "会话工作台", "Session Workspace"} {
		if !bytes.Contains(managementHTML, []byte(required)) {
			t.Errorf("embedded workspace missing %q; run pnpm build:frontend", required)
		}
	}
	for _, pattern := range []string{`(?i)<script\b[^>]*\bsrc\s*=`, `(?i)<link\b[^>]*\brel=["']?stylesheet`, `(?i)<link\b[^>]*\brel=["']?modulepreload`} {
		if regexp.MustCompile(pattern).Match(managementHTML) {
			t.Errorf("workspace must bundle runtime resources in one HTML file: %s", pattern)
		}
	}
	for _, forbidden := range []string{"/@vite/client", "sk-live-", "cpamp_actual_secret_", "response.blob()", "link.download"} {
		if bytes.Contains(managementHTML, []byte(forbidden)) {
			t.Errorf("embedded workspace contains forbidden runtime or credential literal %q", forbidden)
		}
	}
}

func TestManagementResourceServesWorkspaceWithoutCollector(t *testing.T) {
	p := NewPlugin()
	for _, test := range []struct {
		path   string
		status int
	}{
		{"/v0/resource/plugins/cpa-session-archive/index.html", http.StatusOK},
		{"/v0/resource/plugins/cpa-session-archive/unknown.js", http.StatusNotFound},
	} {
		request, _ := json.Marshal(managementRequest{Method: http.MethodGet, Path: test.path})
		response, err := p.handleManagement(request)
		var envelope struct {
			OK     bool               `json:"ok"`
			Result managementResponse `json:"result"`
		}
		if err != nil || json.Unmarshal(response, &envelope) != nil || !envelope.OK || envelope.Result.StatusCode != test.status {
			t.Fatalf("resource status mismatch for %s: err=%v", test.path, err)
		}
		if test.status == http.StatusOK && (!bytes.Equal(envelope.Result.Body, managementHTML) || envelope.Result.Headers.Get("Cache-Control") != "no-store, max-age=0") {
			t.Fatal("resource must serve the exact embedded workspace with caching disabled")
		}
	}
}
