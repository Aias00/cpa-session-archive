package sessionarchive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/capture"
	"cpa-session-archive/record"

	"github.com/yetone/magpie/internal/usage"
)

func TestObserverPreservesAttemptsAndNativeScope(t *testing.T) {
	records := make(chan record.Record, 4)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rec record.Record
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			t.Error(err)
		}
		records <- rec
		w.WriteHeader(http.StatusAccepted)
	}))
	defer collector.Close()
	config := capture.DefaultCaptureConfig()
	config.Endpoint = collector.URL
	observer := New(config)
	if observer.Begin(usage.Request{}) != nil {
		t.Fatal("observer started outside serving lifecycle")
	}
	stop := observer.Serve()
	defer stop()
	watch := observer.Begin(usage.Request{Session: "session", Agent: "codex", Header: http.Header{"Authorization": {"Bearer synthetic-private-key"}}})
	start := time.Now().UTC()
	watch.Attempt(usage.Record{Time: start, Model: "first", Status: 503, BodyIn: `{"input":"hello"}`, BodyOut: `{"error":"retry"}`})
	watch.Attempt(usage.Record{Time: start, Model: "second", Status: 200, BodyIn: `{"input":"hello"}`, BodyOut: `{"output":[]}`})
	watch.End(usage.Record{Time: start, Status: 200})
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case rec := <-records:
			if rec.SessionID != "magpie:session" || rec.Client != "codex" || rec.TraceID == "" || rec.Facets["capture.layer"][0] != "scrubbed-observer" {
				t.Fatalf("lost provenance: %+v", rec)
			}
			if i == 0 && rec.Outcome != "failed" || i == 1 && rec.Outcome != "succeeded" {
				t.Fatal("attempt outcome lost")
			}
			raw, _ := json.Marshal(rec)
			if strings.Contains(string(raw), "synthetic-private-key") {
				t.Fatal("retained bearer credential")
			}
			if ids[rec.RequestID] {
				t.Fatal("attempt IDs collide")
			}
			ids[rec.RequestID] = true
		case <-time.After(3 * time.Second):
			t.Fatal("no archived attempt")
		}
	}
	stop()
	stop()
	if observer.Begin(usage.Request{}) != nil {
		t.Fatal("observer active after stop")
	}
}

func TestObserverMarksHostTruncationAndRecordsEarlyRejection(t *testing.T) {
	records := make(chan record.Record, 2)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rec record.Record
		_ = json.NewDecoder(r.Body).Decode(&rec)
		records <- rec
		w.WriteHeader(202)
	}))
	defer collector.Close()
	config := capture.DefaultCaptureConfig()
	config.Endpoint = collector.URL
	observer := New(config)
	stop := observer.Serve()
	defer stop()
	watch := observer.Begin(usage.Request{})
	watch.Attempt(usage.Record{Status: 200, BodyIn: `{"input":"hello"}`, BodyOut: "partial" + usage.BodyCut})
	observer.Begin(usage.Request{}).End(usage.Record{Status: 403})
	for i := 0; i < 2; i++ {
		select {
		case rec := <-records:
			if i == 0 && !rec.Truncated || i == 1 && rec.Outcome != "failed" {
				t.Fatal("lost partial/rejection state")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("missing record")
		}
	}
}
