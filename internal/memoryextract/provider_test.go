package memoryextract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memory"
)

func providerFixture() (Request, DraftCandidate) {
	request := Request{Input: Input{Version: Version, ProjectID: "synthetic-project", ArchiveID: "synthetic-archive", RequestID: "synthetic-request", Revision: strings.Repeat("b", 64), Excerpts: []Excerpt{{ID: "excerpt-1", Role: "user", Text: "Synthetic requirement: retain a bounded queue; never infer approval."}}, Existing: []memory.MemoryEntry{{ID: "synthetic-target", Revision: strings.Repeat("a", 64)}}}, MaxOutputTokens: 256}
	candidate := DraftCandidate{Kind: "fact", Title: "Bounded queue", Statement: "The user requested a bounded queue for this synthetic fixture.", Attribution: "user_requirement", Supports: []Support{{ExcerptID: "excerpt-1", Quote: "retain a bounded queue"}}, Action: "new", Targets: []Target{}}
	return request, candidate
}

func providerCompletion(content string) map[string]any {
	return map[string]any{"id": "synthetic-completion", "object": "chat.completion", "model": "explicit-fixture-model", "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content, "refusal": nil}}}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19}}
}

func providerCandidateJSON(candidate DraftCandidate) string {
	raw, _ := json.Marshal(map[string]any{"candidates": []DraftCandidate{candidate}})
	return string(raw)
}

func providerAt(t *testing.T, endpoint string, timeout time.Duration) Provider {
	t.Helper()
	p, err := NewHTTPProvider(HTTPConfig{Endpoint: endpoint, Token: "PRIVATE-fixture-token", Model: "explicit-fixture-model", Timeout: timeout})
	if err != nil {
		t.Fatal("provider configuration failed", err)
	}
	return p
}

func TestHTTPProviderExactProtocolAndBudgetEstimate(t *testing.T) {
	request, candidate := providerFixture()
	var calls atomic.Int64
	var sentSize atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer PRIVATE-fixture-token" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Codex-Turn-Metadata") != `{"request_kind":"memory-extraction"}` {
			t.Error("request lost the exact CPA route, authentication, or extraction recursion tag")
		}
		raw, _ := io.ReadAll(r.Body)
		sentSize.Store(int64(len(raw)))
		var payload struct {
			Model               string              `json:"model"`
			Messages            []map[string]string `json:"messages"`
			ResponseFormat      map[string]string   `json:"response_format"`
			MaxCompletionTokens int                 `json:"max_completion_tokens"`
			Stream              bool                `json:"stream"`
			N                   int                 `json:"n"`
			Store               bool                `json:"store"`
			Metadata            map[string]string   `json:"metadata"`
		}
		if json.Unmarshal(raw, &payload) != nil || payload.Model != "explicit-fixture-model" || payload.MaxCompletionTokens != 256 || payload.Stream || payload.N != 1 || payload.Store || payload.ResponseFormat["type"] != "json_object" || payload.Metadata["request_kind"] != "memory-extraction" {
			t.Error("request changed the explicitly configured model or bounded JSON contract")
		}
		if len(payload.Messages) != 2 || payload.Messages[0]["role"] != "system" || !strings.Contains(payload.Messages[0]["content"], "UNTRUSTED EVIDENCE") || payload.Messages[1]["role"] != "user" {
			t.Error("evidence was allowed to select system instructions")
		}
		var input Input
		if json.Unmarshal([]byte(payload.Messages[1]["content"]), &input) != nil || input.Excerpts[0].Text != request.Input.Excerpts[0].Text || input.Existing[0].Revision != request.Input.Existing[0].Revision {
			t.Error("serialization changed supplied evidence or target identity")
		}
		json.NewEncoder(w).Encode(providerCompletion(providerCandidateJSON(candidate)))
	}))
	defer server.Close()
	provider := providerAt(t, server.URL+"/v1/", 0)
	estimated, err := provider.(interface{ EstimateInputTokens(Request) (int, error) }).EstimateInputTokens(request)
	if err != nil || estimated < len(extractionPrompt) || calls.Load() != 0 {
		t.Fatal("input estimate dispatched a call or omitted the fixed prompt", err)
	}
	response, err := provider.Extract(context.Background(), request)
	if err != nil || len(response.Candidates) != 1 || response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 7 || response.Candidates[0].Supports[0] != candidate.Supports[0] || calls.Load() != 1 || estimated < int(sentSize.Load()) {
		t.Fatal("completion failed exact evidence, complete usage, or one-call boundaries", err)
	}
	if provider.(*httpProvider).client.Timeout != 30*time.Second {
		t.Fatal("default provider timeout is no longer thirty seconds")
	}
	request.Input.Existing = append(request.Input.Existing, memory.MemoryEntry{Statement: strings.Repeat("large existing context ", 100)})
	larger, err := provider.(*httpProvider).EstimateInputTokens(request)
	if err != nil || larger <= estimated {
		t.Fatal("budget estimate omitted existing knowledge context", err)
	}
}

func TestHTTPProviderAcceptsOnlySourceBoundActions(t *testing.T) {
	for _, action := range []string{"new", "corrects", "supersedes", "conflicts_with", "duplicate"} {
		t.Run(action, func(t *testing.T) {
			request, candidate := providerFixture()
			candidate.Action = action
			if action != "new" {
				candidate.Targets = []Target{{ID: request.Input.Existing[0].ID, Revision: request.Input.Existing[0].Revision}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(providerCompletion(providerCandidateJSON(candidate)))
			}))
			defer server.Close()
			response, err := providerAt(t, server.URL, 0).Extract(context.Background(), request)
			if err != nil || len(response.Candidates) != 1 || response.Candidates[0].Action != action {
				t.Fatal("source-bound proposal action was not preserved", err)
			}
		})
	}
}

func TestHTTPProviderCPACachedCreationTokensCompatibility(t *testing.T) {
	request, candidate := providerFixture()
	for _, test := range []struct {
		name  string
		value any
		valid bool
	}{
		{"observed zero count", 0, true},
		{"positive count", 3, true},
		{"negative count", -1, false},
		{"fractional count", 0.5, false},
		{"string count", "0", false},
		{"boolean count", false, false},
		{"null count", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				completion := providerCompletion(providerCandidateJSON(candidate))
				completion["usage"].(map[string]any)["prompt_tokens_details"] = map[string]any{"cached_tokens": 0, "cache_write_tokens": 0, "cached_creation_tokens": test.value}
				completion["usage"].(map[string]any)["completion_tokens_details"] = map[string]any{"reasoning_tokens": 0}
				json.NewEncoder(w).Encode(completion)
			}))
			defer server.Close()
			response, err := providerAt(t, server.URL, 0).Extract(context.Background(), request)
			if test.valid {
				if err != nil || len(response.Candidates) != 1 || response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 7 {
					t.Fatal("observed CPA usage extension changed the candidate or total usage contract", err)
				}
			} else {
				var call *CallError
				if !errors.As(err, &call) || !call.Uncertain || call.Retryable {
					t.Fatal("invalid CPA usage count bypassed strict metering", err)
				}
			}
		})
	}
	provider := providerAt(t, "http://127.0.0.1:12345", 0).(*httpProvider)
	previous := sha256.Sum256([]byte(Version + "\x00" + provider.endpoint + "\x00" + provider.model + "\x00" + extractionPrompt + "\x00source-bound-candidates-json-v1"))
	if provider.Fingerprint() == hex.EncodeToString(previous[:]) {
		t.Fatal("CPA response-schema compatibility change reused the previous profile")
	}
}

func TestHTTPProviderCPANativeFinishReasonCompatibility(t *testing.T) {
	request, candidate := providerFixture()
	for _, test := range []struct {
		name, normalized string
		native           any
		valid            bool
		code             string
	}{
		{name: "observed stop", normalized: "stop", native: "stop", valid: true},
		{name: "route specific stop", normalized: "stop", native: "end_turn", valid: true},
		{name: "native length", normalized: "stop", native: "length", code: "completion_truncated"},
		{name: "padded native length", normalized: "stop", native: " LENGTH ", code: "completion_truncated"},
		{name: "normalized length", normalized: "length", native: "stop", code: "completion_truncated"},
		{name: "normalized tool call", normalized: "tool_calls", native: "stop", code: "schema_invalid"},
		{name: "empty", normalized: "stop", native: "", code: "schema_invalid"},
		{name: "whitespace", normalized: "stop", native: " \t", code: "schema_invalid"},
		{name: "null", normalized: "stop", native: nil, code: "schema_invalid"},
		{name: "negative", normalized: "stop", native: -1, code: "schema_invalid"},
		{name: "integer", normalized: "stop", native: 0, code: "schema_invalid"},
		{name: "fraction", normalized: "stop", native: 0.5, code: "schema_invalid"},
		{name: "boolean", normalized: "stop", native: false, code: "schema_invalid"},
		{name: "array", normalized: "stop", native: []string{"stop"}, code: "schema_invalid"},
		{name: "object", normalized: "stop", native: map[string]string{"reason": "stop"}, code: "schema_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				completion := providerCompletion(providerCandidateJSON(candidate))
				choice := completion["choices"].([]any)[0].(map[string]any)
				choice["finish_reason"], choice["native_finish_reason"] = test.normalized, test.native
				message := choice["message"].(map[string]any)
				message["reasoning_content"], message["tool_calls"] = "", []any{}
				completion["usage"].(map[string]any)["prompt_tokens_details"] = map[string]any{"cached_tokens": 0, "cache_write_tokens": 0, "cached_creation_tokens": 0}
				completion["usage"].(map[string]any)["completion_tokens_details"] = map[string]any{"reasoning_tokens": 0}
				json.NewEncoder(w).Encode(completion)
			}))
			defer server.Close()
			response, err := providerAt(t, server.URL, 0).Extract(context.Background(), request)
			if test.valid {
				if err != nil || len(response.Candidates) != 1 || response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 7 {
					t.Fatal("observed CPA choice extension changed the bounded completion contract", err)
				}
			} else {
				var call *CallError
				if !errors.As(err, &call) || call.Code != test.code || !call.Uncertain || call.Retryable || len(response.Candidates) != 0 || response.Usage != (Usage{}) {
					t.Fatal("invalid native finish reason returned a usable, retryable, or incorrectly classified result", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("native finish reason compatibility triggered an internal retry")
			}
		})
	}
	provider := providerAt(t, "http://127.0.0.1:12345", 0).(*httpProvider)
	for _, version := range []string{"source-bound-candidates-json-v1", "source-bound-candidates-json-v2"} {
		previous := sha256.Sum256([]byte(Version + "\x00" + provider.endpoint + "\x00" + provider.model + "\x00" + extractionPrompt + "\x00" + version))
		if provider.Fingerprint() == hex.EncodeToString(previous[:]) {
			t.Fatal("CPA choice extension compatibility reused an earlier profile")
		}
	}
}

func TestHTTPProviderParseFailureStagesAreFixedAndUncertain(t *testing.T) {
	request, candidate := providerFixture()
	for _, test := range []struct {
		name, code string
		edit       func(map[string]any)
	}{
		{"truncated", "completion_truncated", func(m map[string]any) { m["choices"].([]any)[0].(map[string]any)["finish_reason"] = "length" }},
		{"refused", "completion_refused", func(m map[string]any) {
			m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["refusal"] = "PRIVATE-refusal"
		}},
		{"missing usage", "usage_invalid", func(m map[string]any) { delete(m, "usage") }},
		{"invalid usage", "usage_invalid", func(m map[string]any) { m["usage"].(map[string]any)["completion_tokens"] = "PRIVATE-count" }},
		{"unknown choice field", "schema_invalid", func(m map[string]any) { m["choices"].([]any)[0].(map[string]any)["PRIVATE-choice"] = true }},
		{"case alias native field", "schema_invalid", func(m map[string]any) { m["choices"].([]any)[0].(map[string]any)["Native_Finish_Reason"] = "stop" }},
		{"unknown native extension", "schema_invalid", func(m map[string]any) { m["choices"].([]any)[0].(map[string]any)["native_stop_reason"] = "stop" }},
		{"bindings", "bindings_invalid", func(m map[string]any) {
			changed := candidate
			changed.Supports = []Support{{ExcerptID: "excerpt-1", Quote: "PRIVATE-fabricated-quote"}}
			m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] = providerCandidateJSON(changed)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			completion := providerCompletion(providerCandidateJSON(candidate))
			test.edit(completion)
			raw, err := json.Marshal(completion)
			if err != nil {
				t.Fatal(err)
			}
			result, err := providerParseCompletion(raw, request.Input)
			var call *CallError
			if !errors.As(err, &call) || call.Code != test.code || !call.Uncertain || call.Retryable || strings.Contains(call.Error(), "PRIVATE") || len(result.Candidates) != 0 || result.Usage != (Usage{}) {
				t.Fatal("parse error leaked values or lost its fixed uncertain stage", err)
			}
		})
	}
}

func TestHTTPProviderRejectsMalformedOrUnboundCompletionsAsUncertain(t *testing.T) {
	request, candidate := providerFixture()
	validContent := providerCandidateJSON(candidate)
	tests := []struct {
		name string
		edit func(map[string]any)
		raw  string
	}{
		{name: "missing usage", edit: func(m map[string]any) { delete(m, "usage") }},
		{name: "null usage", edit: func(m map[string]any) { m["usage"] = nil }},
		{name: "missing input usage", edit: func(m map[string]any) { delete(m["usage"].(map[string]any), "prompt_tokens") }},
		{name: "null output usage", edit: func(m map[string]any) { m["usage"].(map[string]any)["completion_tokens"] = nil }},
		{name: "negative usage", edit: func(m map[string]any) { m["usage"].(map[string]any)["prompt_tokens"] = -1 }},
		{name: "fractional usage", edit: func(m map[string]any) { m["usage"].(map[string]any)["prompt_tokens"] = 1.5 }},
		{name: "boolean usage", edit: func(m map[string]any) { m["usage"].(map[string]any)["prompt_tokens"] = true }},
		{name: "string usage", edit: func(m map[string]any) { m["usage"].(map[string]any)["prompt_tokens"] = "12" }},
		{name: "usage mismatch", edit: func(m map[string]any) { m["usage"].(map[string]any)["total_tokens"] = 20 }},
		{name: "unknown usage", edit: func(m map[string]any) { m["usage"].(map[string]any)["tokens"] = 12 }},
		{name: "case alias usage", edit: func(m map[string]any) { m["usage"].(map[string]any)["Prompt_Tokens"] = 12 }},
		{name: "unknown usage detail", edit: func(m map[string]any) {
			m["usage"].(map[string]any)["completion_tokens_details"] = map[string]any{"PRIVATE-field": 1}
		}},
		{name: "negative usage detail", edit: func(m map[string]any) {
			m["usage"].(map[string]any)["prompt_tokens_details"] = map[string]any{"cached_tokens": -1}
		}},
		{name: "case alias CPA usage detail", edit: func(m map[string]any) {
			m["usage"].(map[string]any)["prompt_tokens_details"] = map[string]any{"Cached_Creation_Tokens": 0}
		}},
		{name: "unknown response field", edit: func(m map[string]any) { m["PRIVATE-details"] = "PRIVATE-value" }},
		{name: "multiple choices", edit: func(m map[string]any) { m["choices"] = append(m["choices"].([]any), m["choices"].([]any)[0]) }},
		{name: "no choices", edit: func(m map[string]any) { m["choices"] = []any{} }},
		{name: "length", edit: func(m map[string]any) { m["choices"].([]any)[0].(map[string]any)["finish_reason"] = "length" }},
		{name: "content filter", edit: func(m map[string]any) { m["choices"].([]any)[0].(map[string]any)["finish_reason"] = "content_filter" }},
		{name: "tool finish", edit: func(m map[string]any) { m["choices"].([]any)[0].(map[string]any)["finish_reason"] = "tool_calls" }},
		{name: "refusal", edit: func(m map[string]any) {
			m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["refusal"] = "PRIVATE-refusal"
		}},
		{name: "tool request", edit: func(m map[string]any) {
			m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["tool_calls"] = []any{map[string]any{"id": "tool"}}
		}},
		{name: "wrong role", edit: func(m map[string]any) {
			m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["role"] = "system"
		}},
		{name: "null content", edit: func(m map[string]any) {
			m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] = nil
		}},
		{name: "duplicate wrapper", raw: `{"choices":[],"choices":[],"usage":{}}`},
		{name: "trailing wrapper", raw: `{} {}`},
	}
	for _, content := range []struct{ name, value string }{
		{"duplicate candidate container", `{"candidates":[],"candidates":[]}`},
		{"trailing candidate container", validContent + ` {}`},
		{"unknown container", `{"candidates":[],"PRIVATE-value":true}`},
		{"null candidates", `{"candidates":null}`},
		{"duplicate candidate field", strings.Replace(validContent, `"kind":"fact"`, `"kind":"fact","kind":"decision"`, 1)},
		{"case alias candidate", strings.Replace(validContent, `"kind":"fact"`, `"Kind":"fact"`, 1)},
		{"unknown candidate field", strings.Replace(validContent, `"kind":"fact"`, `"kind":"fact","auto_approved":true`, 1)},
		{"unsupported kind", strings.Replace(validContent, `"kind":"fact"`, `"kind":"preference"`, 1)},
		{"unsupported action", strings.Replace(validContent, `"action":"new"`, `"action":"approve"`, 1)},
		{"unsupported attribution", strings.Replace(validContent, `"attribution":"user_requirement"`, `"attribution":"verified_truth"`, 1)},
		{"rewritten quote", strings.Replace(validContent, `retain a bounded queue`, `retain an infinite queue`, 1)},
		{"unknown excerpt", strings.Replace(validContent, `excerpt-1`, `outside-excerpt`, 1)},
		{"null quote", strings.Replace(validContent, `"quote":"retain a bounded queue"`, `"quote":null`, 1)},
		{"unknown support field", strings.Replace(validContent, `"excerpt_id":"excerpt-1"`, `"excerpt_id":"excerpt-1","approved":true`, 1)},
		{"null targets", strings.Replace(validContent, `"targets":[]`, `"targets":null`, 1)},
		{"oversize statement", strings.Replace(validContent, candidate.Statement, strings.Repeat("x", 3501), 1)},
	} {
		content := content
		tests = append(tests, struct {
			name string
			edit func(map[string]any)
			raw  string
		}{name: content.name, edit: func(m map[string]any) {
			m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] = content.value
		}})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if test.raw != "" {
					io.WriteString(w, test.raw)
					return
				}
				completion := providerCompletion(validContent)
				test.edit(completion)
				json.NewEncoder(w).Encode(completion)
			}))
			defer server.Close()
			_, err := providerAt(t, server.URL, 0).Extract(context.Background(), request)
			var call *CallError
			if !errors.As(err, &call) || !call.Uncertain || call.Retryable || strings.Contains(err.Error(), "PRIVATE") || calls.Load() != 1 {
				t.Fatal("malformed output became a free, retryable, or unsanitized completion", err)
			}
		})
	}
}

func TestHTTPProviderRejectsForeignAndStaleTargets(t *testing.T) {
	request, candidate := providerFixture()
	for _, target := range []Target{{ID: "foreign", Revision: request.Input.Existing[0].Revision}, {ID: request.Input.Existing[0].ID, Revision: strings.Repeat("c", 64)}} {
		candidate.Action, candidate.Targets = "corrects", []Target{target}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(providerCompletion(providerCandidateJSON(candidate)))
		}))
		_, err := providerAt(t, server.URL, 0).Extract(context.Background(), request)
		server.Close()
		var call *CallError
		if !errors.As(err, &call) || !call.Uncertain {
			t.Fatal("provider accepted an existing-memory target outside its exact input list", err)
		}
	}
}

func TestHTTPProviderRejectionsNeverRetryInternally(t *testing.T) {
	request, _ := providerFixture()
	for _, test := range []struct {
		name, body           string
		status               int
		retryable, uncertain bool
	}{
		{"rate rejection", `{"error":{"message":"PRIVATE-rate","type":"rate_limit_error","code":"rate_limit_exceeded"}}`, 429, true, false},
		{"quota rejection", `{"error":{"message":"PRIVATE-quota","type":"insufficient_quota","code":"insufficient_quota"}}`, 429, false, false},
		{"unknown 429", `PRIVATE upstream text`, 429, false, true},
		{"auth rejection", `{"error":{"message":"PRIVATE-key","type":"authentication_error","code":null}}`, 401, false, false},
		{"server ambiguity", `{"error":{"message":"PRIVATE-error","type":"server_error"}}`, 503, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			_, err := providerAt(t, server.URL, 0).Extract(context.Background(), request)
			var call *CallError
			if !errors.As(err, &call) || call.Retryable != test.retryable || call.Uncertain != test.uncertain || calls.Load() != 1 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("HTTP rejection lost its no-retry or sanitized outcome contract", err)
			}
		})
	}
}

func TestHTTPProviderRedirectTimeoutOversizeAndProxyBoundaries(t *testing.T) {
	request, candidate := providerFixture()
	var redirected atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	_, err := providerAt(t, redirect.URL, 0).Extract(context.Background(), request)
	if err == nil || redirected.Load() != 0 {
		t.Fatal("provider followed a redirect with source text or credentials")
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	_, err = providerAt(t, slow.URL, 10*time.Millisecond).Extract(context.Background(), request)
	close(release)
	slow.Close()
	var call *CallError
	if !errors.As(err, &call) || !call.Uncertain {
		t.Fatal("timeout was treated as a definitely unprocessed request", err)
	}
	oversize := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", providerResponseLimit+1))
	}))
	_, err = providerAt(t, oversize.URL, 0).Extract(context.Background(), request)
	oversize.Close()
	if !errors.As(err, &call) || !call.Uncertain {
		t.Fatal("oversized successful response was accepted or considered free", err)
	}
	var proxied atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied.Add(1); w.WriteHeader(500) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(providerCompletion(providerCandidateJSON(candidate)))
	}))
	defer server.Close()
	_, err = providerAt(t, server.URL, 0).Extract(context.Background(), request)
	if err != nil || proxied.Load() != 0 {
		t.Fatal("provider used a system proxy", err)
	}
}

func TestHTTPProviderConfigurationAndFingerprint(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1", "http://localhost", "http://example.invalid", "http://0.0.0.0", "http://PRIVATE-key@127.0.0.1", "http://127.0.0.1?key=PRIVATE", "http://127.0.0.1?", "http://127.0.0.1#PRIVATE", "http://127.0.0.1#", "http://127.0.0.1:", "http://127.0.0.1/private", "http://127.0.0.1/%76%31", "http://127.0.0.1:0", "http://127.0.0.1:65536"} {
		_, err := NewHTTPProvider(HTTPConfig{Endpoint: endpoint, Token: "PRIVATE-token", Model: "explicit-model"})
		if err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("provider accepted a nonliteral/nonlocal endpoint or leaked input")
		}
	}
	for _, config := range []HTTPConfig{{Endpoint: "http://127.0.0.1", Model: "m"}, {Endpoint: "http://127.0.0.1", Token: "t"}, {Endpoint: "http://127.0.0.1", Token: "PRIVATE\ntoken", Model: "m"}, {Endpoint: "http://127.0.0.1", Token: "t", Model: "m", Timeout: -1}} {
		if _, err := NewHTTPProvider(config); err == nil {
			t.Fatal("provider silently supplied missing credentials/model or accepted invalid configuration")
		}
	}
	a := providerAt(t, "http://127.0.0.1:12345", 0)
	b, err := NewHTTPProvider(HTTPConfig{Endpoint: "http://127.0.0.1:12345/v1", Token: "different-token", Model: "explicit-fixture-model", Timeout: time.Second})
	if err != nil || a.Fingerprint() != b.Fingerprint() || len(a.Fingerprint()) != 64 {
		t.Fatal("credentials or equivalent endpoint spelling changed semantic provider identity", err)
	}
	c, _ := NewHTTPProvider(HTTPConfig{Endpoint: "http://127.0.0.1:12345", Token: "different-token", Model: "different-model"})
	d := providerAt(t, "http://127.0.0.1:12346", 0)
	if a.Fingerprint() == c.Fingerprint() || a.Fingerprint() == d.Fingerprint() {
		t.Fatal("fingerprint omitted the explicit endpoint or model")
	}
}

func TestHTTPProviderSourceWindowsFitDefaultInputBudget(t *testing.T) {
	provider := providerAt(t, "http://127.0.0.1:12345", 0)
	for _, test := range []struct{ name, text string }{
		{"ascii", strings.Repeat("Synthetic bounded evidence. ", 1000)},
		{"chinese", strings.Repeat("公开合成材料只作为候选证据。", 1000)},
		{"escaping", strings.Repeat("\"\\\n<>&", 1000)},
		{"quotes", strings.Repeat("\"", 6000)},
		{"slashes", strings.Repeat("\\", 6000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": test.text}}})
			record := archive.Record{RequestID: "synthetic-source", SessionID: "synthetic-session", Facets: map[string][]string{"project.id": {"synthetic-project"}}, OriginalRequest: payload}
			revision, err := sourceRevision(record)
			if err != nil {
				t.Fatal(err)
			}
			windows, err := InputsFromSnapshot(archive.SourceSnapshot{Record: record, Revision: revision, Sequence: 1}, "synthetic-archive", Selection{ProjectID: "synthetic-project"})
			if err != nil || len(windows) == 0 {
				t.Fatal("synthetic source window construction failed", err)
			}
			maximum := 0
			for _, window := range windows {
				estimate, err := provider.EstimateInputTokens(Request{Input: window, MaxOutputTokens: DefaultLimits().MaxOutputTokens})
				if err != nil {
					t.Fatal(err)
				}
				maximum = max(maximum, estimate)
			}
			t.Logf("maximum conservative input reservation: %d", maximum)
			if maximum > DefaultLimits().MaxInputTokens {
				t.Fatal("source windows exceed the default provider budget before any knowledge comparison")
			}
		})
	}
}
