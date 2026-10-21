package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
)

type budgetFailureReader struct{ Reader }

func (budgetFailureReader) Search(context.Context, Query) (SearchResult, error) {
	return SearchResult{Hits: []Hit{{Snippet: "private partial result"}}}, fmt.Errorf("private database detail: %w", ErrQueryBudget)
}

func (budgetFailureReader) Recap(context.Context, Query) (RecapResult, error) {
	return RecapResult{Hits: []Hit{{Snippet: "private partial result"}}, Text: "private partial packet"}, fmt.Errorf("private database detail: %w", ErrQueryBudget)
}

func TestMemoryHTTPQueryBudgetIsAnActionableError(t *testing.T) {
	token := strings.Repeat("x", 48)
	handler, err := NewHandler(budgetFailureReader{}, token)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/search", "/v1/recap"} {
		request := httptest.NewRequest(http.MethodGet, path+"?q=evidence&project_id=p&mode=diverse", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusUnprocessableEntity || len(body) != 1 || body["error"] != ErrQueryBudget.Error() {
			t.Fatalf("%s budget response: %d %s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "private") || strings.Contains(response.Body.String(), "hits") {
			t.Fatalf("%s leaked partial results or internal details", path)
		}
	}
}

func TestMemoryHTTPAuthScopeAndCitations(t *testing.T) {
	e, raw, _ := testEngine(t)
	raw.PutBatch([]archive.Record{sample("a", "s", "p", "<script>alert(1)</script> 记忆证据")})
	syncAll(t, e)
	token := strings.Repeat("x", 48)
	if _, err := NewHandler(e, ""); err == nil {
		t.Fatal("empty token accepted")
	}
	handler, err := NewHandler(e, token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	call := func(method, path, bearer, origin string) (int, string) {
		t.Helper()
		r, _ := http.NewRequest(method, server.URL+path, nil)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable recall")
		}
		return response.StatusCode, string(body)
	}
	if code, _ := call("GET", "/v1/search?q=记忆", "", ""); code != 401 {
		t.Fatal(code)
	}
	if code, _ := call("GET", "/v1/status", token, "https://evil.example"); code != 403 {
		t.Fatal(code)
	}
	if code, _ := call("POST", "/v1/search", token, ""); code != 405 {
		t.Fatal(code)
	}
	if code, _ := call("GET", "/v1/search?q=a&limit=no", token, ""); code != 400 {
		t.Fatal(code)
	}
	if code, _ := call("GET", "/v1/recap", token, ""); code != 400 {
		t.Fatal(code)
	}
	for _, path := range []string{"/v1/search", "/v1/recap"} {
		for _, mode := range []string{"literal", "diverse", "unknown"} {
			code, body := call("GET", path+"?q=记忆&project_id=p&mode="+mode, token, "")
			if mode == "unknown" {
				if code != http.StatusBadRequest {
					t.Fatalf("%s accepted invalid mode: %d", path, code)
				}
				continue
			}
			var result SearchResult
			if err := json.Unmarshal([]byte(body), &result); code != http.StatusOK || err != nil || len(result.Hits) != 1 {
				t.Fatalf("%s mode %s: %d %s", path, mode, code, body)
			}
		}
		for _, text := range []string{"", "///"} {
			if code, _ := call("GET", path+"?project_id=p&mode=diverse&q="+url.QueryEscape(text), token, ""); code != http.StatusBadRequest {
				t.Fatalf("%s accepted non-searchable diverse text %q: %d", path, text, code)
			}
		}
	}
	if code, _ := call("GET", "/v1/recap?project_id=p&mode=literal", token, ""); code != http.StatusOK {
		t.Fatalf("literal recap rejected empty text: %d", code)
	}
	code, body := call("GET", "/v1/search?q=记忆&project_id=p", token, server.URL)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var result SearchResult
	if err := json.Unmarshal([]byte(body), &result); err != nil || len(result.Hits) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	c := result.Hits[0].Citation
	q := url.Values{"request_id": {c.RequestID}, "revision": {c.Revision}, "side": {c.Side}, "pointer": {c.Pointer}, "start": {strconv.Itoa(c.Start)}, "end": {strconv.Itoa(c.End)}, "normalizer_version": {strconv.Itoa(c.NormalizerVersion)}}
	if code, body := call("GET", "/v1/source?"+q.Encode(), token, ""); code != 200 || !strings.Contains(body, "记忆证据") {
		t.Fatalf("%d %s", code, body)
	}
	raw.DB.Exec(`DELETE FROM records WHERE request_id='a'`)
	if code, _ := call("GET", "/v1/source?"+q.Encode(), token, ""); code != 410 {
		t.Fatal(code)
	}
	if code, body := call("GET", "/", "", ""); code != 200 || strings.Contains(body, token) {
		t.Fatalf("UI %d", code)
	}
}
