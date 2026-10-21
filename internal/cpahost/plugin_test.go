package cpahost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cpa-session-archive/record"
)

func TestCPAWireLifecycleRemainsCompatible(t *testing.T) {
	records := make(chan record.Record, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rec record.Record
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			t.Error(err)
		}
		records <- rec
		w.WriteHeader(202)
	}))
	defer collector.Close()
	p := NewPlugin()
	defer p.Shutdown()
	raw, _ := json.Marshal(lifecycle{ConfigYAML: []byte("endpoint: " + collector.URL + "\nqueue_size: 64\n")})
	registration, err := p.Handle("plugin.register", raw)
	if err != nil {
		t.Fatal(err)
	}
	var registered struct {
		OK     bool `json:"ok"`
		Result struct {
			Version      int             `json:"schema_version"`
			Capabilities map[string]bool `json:"capabilities"`
		} `json:"result"`
	}
	if json.Unmarshal(registration, &registered) != nil || !registered.OK || registered.Result.Version != 5 || !registered.Result.Capabilities["management_api"] {
		t.Fatal("CPA registration contract changed")
	}
	for _, call := range []struct {
		method string
		body   any
	}{
		{"request.intercept_before", map[string]any{"RequestID": "wire", "SourceFormat": "openai-response", "Model": "fixture", "Body": []byte(`{"input":"original user question","session_id":"native-session"}`)}},
		{"response.intercept_stream_chunk", map[string]any{"RequestID": "wire", "ChunkIndex": 0, "Body": []byte("data: {\"id\":\"resp_fixture\"}\n\n")}},
		{"request.complete", map[string]any{"RequestID": "wire", "SourceFormat": "openai-response", "Model": "fixture", "Stream": true, "Outcome": "succeeded", "StatusCode": 200}},
	} {
		body, _ := json.Marshal(call.body)
		if _, err := p.Handle(call.method, body); err != nil {
			t.Fatal(call.method, err)
		}
	}
	select {
	case rec := <-records:
		if rec.RequestID != "wire" || rec.SessionID != "native-session" || len(rec.Response) == 0 || rec.Outcome != "succeeded" {
			t.Fatal("CPA wire event changed meaning")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no delivery")
	}
	if _, err := p.Handle("unknown", nil); err == nil {
		t.Fatal("unknown method accepted")
	}
	if _, err := p.Handle("request.complete", []byte("invalid")); err == nil {
		t.Fatal("invalid completion accepted")
	}
}
