package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/archivefeed"
	"cpa-session-archive/internal/ingeststatus"
	"cpa-session-archive/internal/storage"
)

func TestArchiveFeedSetupRequiresPrivateTokenAndLoopback(t *testing.T) {
	dir := t.TempDir()
	raw, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.DB.Close()
	path := filepath.Join(dir, "feed.token")
	if err := os.WriteFile(path, []byte(strings.Repeat("f", 48)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct{ listen, token string }{{"0.0.0.0:0", path}, {"127.0.0.1:0", path + ".missing"}} {
		if _, err := configureArchiveFeed(context.Background(), storage.NewSQLite(raw), invalid.listen, invalid.token, nil, nil); err == nil {
			t.Fatal("invalid feed configuration accepted")
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := configureArchiveFeed(context.Background(), storage.NewSQLite(raw), "127.0.0.1:0", path, nil, nil); err == nil {
		t.Fatal("public feed credential accepted")
	}
	fifo := filepath.Join(dir, "invalid-fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := configureArchiveFeed(context.Background(), storage.NewSQLite(raw), "127.0.0.1:0", fifo, nil, nil); err == nil || time.Since(started) > time.Second {
		t.Fatal("an invalid feed token blocked collector startup")
	}
}

func TestCollectorProcessWithoutMemoryBackend(t *testing.T) {
	if os.Getenv("CPA_TEST_COLLECTOR_PROCESS") == "1" {
		if err := run(); err != nil {
			os.Exit(2)
		}
		return
	}
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestCollectorProcessWithoutMemoryBackend$")
	for _, pair := range os.Environ() {
		if !strings.HasPrefix(pair, "ARCHIVE_") && !strings.HasPrefix(pair, "MEMORY_") && !strings.HasPrefix(pair, "LISTEN_ADDR=") && !strings.HasPrefix(pair, "CPA_TEST_COLLECTOR_PROCESS=") {
			command.Env = append(command.Env, pair)
		}
	}
	command.Env = append(command.Env,
		"CPA_TEST_COLLECTOR_PROCESS=1", "LISTEN_ADDR="+address,
		"ARCHIVE_DB="+filepath.Join(dir, "archive.sqlite"),
		"ARCHIVE_FEED_TOKEN_FILE="+filepath.Join(dir, "missing-feed.token"),
		"MEMORY_ENABLED=true", "MEMORY_DB="+filepath.Join(dir, "archive.sqlite"),
		"MEMORY_READ_TOKEN_FILE="+filepath.Join(dir, "missing-recall.token"), "MEMORY_LISTEN_ADDR=0.0.0.0:0")
	var log bytes.Buffer
	command.Stdout, command.Stderr = &log, &log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { command.Process.Kill(); command.Wait() })
	client := &http.Client{Timeout: time.Second}
	origin := "http://" + address
	ready := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get(origin + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("collector depended on invalid memory configuration")
	}
	response, err := client.Get(origin + archivefeed.Prefix + "manifest")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("invalid optional feed was not isolated")
	}
	body, _ := json.Marshal(archive.Record{RequestID: "isolated-ingest", SessionID: "s", SourceFormat: "openai", OriginalRequest: []byte(`{"messages":[{"role":"user","content":"independent capture"}]}`)})
	request, _ := http.NewRequest(http.MethodPost, origin+"/ingest", bytes.NewReader(body))
	request.Header.Set(archive.CommitAckHeader, archive.CommitAckRequest)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	digest := sha256.Sum256(body)
	if response.StatusCode != 200 || response.Header.Get(archive.CommitAckHeader) != archive.CommitAckResponse || response.Header.Get(archive.CommitDigestHeader) != hex.EncodeToString(digest[:]) {
		t.Fatal("collector did not commit with its memory backend absent")
	}
	response, err = client.Get(origin + "/v1/ingest-status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var status ingeststatus.Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil || status.CommittedDeliveries != 1 {
		t.Fatal("independent collector did not retain commit diagnostics", err)
	}
}
