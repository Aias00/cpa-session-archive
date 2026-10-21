package archive

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
