package memory

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
)

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
