package cpahost

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSessionManagementProxyPreservesQueryBodyAndConflict(t *testing.T) {
	for _, test := range []struct{ method, path, target string }{
		{"GET", "session-browser", "/v1/session-browser"},
		{"GET", "session-metadata", "/v1/session-metadata"},
		{"PUT", "session-metadata", "/v1/session-metadata"},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			body := `{"expected_revision":3,"title":"A private title"}`
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != test.method || r.URL.Path != test.target || r.URL.Query().Get("session_id") != "scope/with'quote" {
					t.Errorf("unexpected forwarded request: %s %s", r.Method, r.URL)
				}
				if test.method == "PUT" {
					got, _ := io.ReadAll(r.Body)
					if string(got) != body || r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("body or content type changed")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"code":"revision_conflict"}`)
			}))
			defer collector.Close()
			p := &Plugin{endpoint: collector.URL + "/ingest", client: collector.Client()}
			request := managementRequest{Method: test.method, Path: "/v0/management/plugins/cpa-session-archive/" + test.path, Query: url.Values{"session_id": {"scope/with'quote"}}}
			if test.method == "PUT" {
				request.Body = []byte(body)
			}
			raw, _ := json.Marshal(request)
			response, err := p.handleManagement(raw)
			if err != nil {
				t.Fatal(err)
			}
			var reply struct {
				OK     bool               `json:"ok"`
				Result managementResponse `json:"result"`
			}
			if err = json.Unmarshal(response, &reply); err != nil || !reply.OK || reply.Result.StatusCode != 409 || string(reply.Result.Body) != `{"code":"revision_conflict"}` || reply.Result.Headers.Get("Cache-Control") != "no-store" || reply.Result.Headers.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("proxy lost conflict or private response headers: %s err=%v", response, err)
			}
		})
	}
}

func TestSessionBulkProxyForwardsExactScopeAndOneMutation(t *testing.T) {
	for _, test := range []struct{ method, path, body string }{
		{"POST", "session-bulk-preview", `{"selection":{"view":"inactive","inactive_days":30,"filters":{"project.name":"repo/with'quote"}},"action":{"archived":true}}`},
		{"GET", "session-bulk", ""},
		{"POST", "session-bulk-apply", `{"id":"bulk-test","limit":1}`},
		{"POST", "session-bulk-undo", `{"id":"bulk-test","limit":1}`},
	} {
		t.Run(test.path, func(t *testing.T) {
			calls := 0
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if r.Method != test.method || r.URL.Path != "/v1/"+test.path || string(body) != test.body || r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Key") != "" {
					t.Errorf("proxy changed scope/body or exposed host credentials")
				}
				if test.method == "GET" && r.URL.Query().Get("id") != "bulk-test" {
					t.Error("proxy lost exact job ID")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"code":"revision_conflict"}`)
			}))
			defer collector.Close()
			p := &Plugin{endpoint: collector.URL + "/ingest", client: collector.Client()}
			r := managementRequest{Method: test.method, Path: "/v0/management/plugins/cpa-session-archive/" + test.path, Headers: http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer synthetic-host-key"}, "X-Management-Key": {"synthetic-host-key"}}, Body: []byte(test.body)}
			if test.method == "GET" {
				r.Query = url.Values{"id": {"bulk-test"}}
			}
			raw, _ := json.Marshal(r)
			reply, err := p.handleManagement(raw)
			var result struct {
				Result managementResponse `json:"result"`
			}
			if err != nil || json.Unmarshal(reply, &result) != nil || calls != 1 || result.Result.StatusCode != 409 || result.Result.Headers.Get("Cache-Control") != "no-store" || strings.Contains(string(reply), "synthetic-host-key") {
				t.Fatalf("proxy failed calls=%d err=%v reply=%s", calls, err, reply)
			}
		})
	}
}

func TestSessionBulkProxyRejectsWrongMediaAndDoesNotFollowRedirects(t *testing.T) {
	leaks, calls := 0, 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaks++ }))
	defer target.Close()
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Redirect(w, r, target.URL, 307) }))
	defer collector.Close()
	p := &Plugin{endpoint: collector.URL + "/ingest", client: collector.Client()}
	for _, test := range []struct {
		media        string
		size, status int
	}{{"text/plain", 2, 415}, {"application/json", 65537, 400}, {"application/json", 2, 307}} {
		r := managementRequest{Method: "POST", Path: "/v0/management/plugins/cpa-session-archive/session-bulk-apply", Headers: http.Header{"Content-Type": {test.media}}, Body: []byte(strings.Repeat("x", test.size))}
		raw, _ := json.Marshal(r)
		reply, err := p.handleManagement(raw)
		var result struct {
			Result managementResponse `json:"result"`
		}
		if err != nil || json.Unmarshal(reply, &result) != nil || result.Result.StatusCode != test.status {
			t.Fatalf("status=%d want=%d err=%v", result.Result.StatusCode, test.status, err)
		}
	}
	if leaks != 0 || calls != 1 {
		t.Fatalf("redirect or rejected body reached backend: leaks=%d calls=%d", leaks, calls)
	}
	registration, _ := json.Marshal(p.managementRegistration())
	for _, route := range []string{"session-bulk-preview", "session-bulk", "session-bulk-apply", "session-bulk-undo"} {
		if !strings.Contains(string(registration), "/"+route+`"`) {
			t.Fatalf("missing registered management route %s", route)
		}
	}
}
