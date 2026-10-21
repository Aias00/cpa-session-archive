package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/capture"
	"cpa-session-archive/internal/outbox"
	"cpa-session-archive/record"
)

func TestJSONLBridgePersistsCompletedRecordBeforeEOFShutdown(t *testing.T) {
	config := capture.DefaultCaptureConfig()
	config.Endpoint = "http://127.0.0.1:1"
	config.SpoolDir = filepath.Join(t.TempDir(), "private")
	rec := record.Record{RequestID: "deepseek-harness:fixture", SessionID: "deepseek-harness:session", OriginalRequest: []byte(`{"messages":[{"role":"user","content":"retained"}]}`), Response: []byte(`{"choices":[{"message":{"role":"assistant","content":"answer"}}]}`)}
	var source, output bytes.Buffer
	if err := json.NewEncoder(&source).Encode(input{Version: 1, Record: rec}); err != nil {
		t.Fatal(err)
	}
	if err := run(&source, &output, config); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&output)
	var ready struct{ Ready bool }
	if dec.Decode(&ready) != nil || !ready.Ready {
		t.Fatal("missing bridge readiness")
	}
	var admitted struct{ Accepted bool }
	if dec.Decode(&admitted) != nil || !admitted.Accepted {
		t.Fatal("missing bounded admission status")
	}
	spool, err := outbox.Open(filepath.Join(config.SpoolDir, "deliveries.sqlite"), config.SpoolMaxBytes, config.SpoolMaxEntries)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	delivery, err := spool.Next(context.Background())
	if err != nil {
		t.Fatal("EOF discarded accepted outbox work", err)
	}
	var got record.Record
	if json.Unmarshal(delivery.Body, &got) != nil || got.RequestID != rec.RequestID || !bytes.Equal(got.OriginalRequest, rec.OriginalRequest) || !bytes.Equal(got.Response, rec.Response) {
		t.Fatal("bridge changed retained payloads")
	}
}

func TestJSONLBridgeRejectsInvalidVersionWithoutDisclosingInput(t *testing.T) {
	for _, raw := range []string{`{"version":2,"private":"synthetic-private-text"}`, `synthetic-private-text`} {
		config := capture.DefaultCaptureConfig()
		config.Endpoint = "http://127.0.0.1:1"
		err := run(strings.NewReader(raw), io.Discard, config)
		if err == nil || strings.Contains(err.Error(), "synthetic-private-text") {
			t.Fatal("invalid input accepted or disclosed")
		}
	}
}
