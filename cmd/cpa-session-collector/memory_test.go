package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

func TestMemoryListenerRequiresLoopbackDedicatedTokenAndDatabase(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.sqlite")
	raw, err := archive.OpenStore(archivePath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.DB.Close()
	tokenFile := filepath.Join(dir, "read.token")
	token := strings.Repeat("a", 48)
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := memoryConfig{DB: filepath.Join(dir, "memory.sqlite"), Listen: "127.0.0.1:0", TokenFile: tokenFile}
	for _, change := range []func(*memoryConfig){func(c *memoryConfig) { c.Listen = "0.0.0.0:0" }, func(c *memoryConfig) { c.TokenFile = "" }, func(c *memoryConfig) { c.DB = archivePath }} {
		invalid := cfg
		change(&invalid)
		if service, err := startMemory(context.Background(), storage.NewSQLite(raw), archivePath, nil, invalid); err == nil {
			service.Close()
			t.Fatalf("accepted invalid configuration: %+v", invalid)
		}
	}
	service, err := startMemory(context.Background(), storage.NewSQLite(raw), archivePath, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	url := "http://" + service.listener.Addr().String() + "/v1/status"
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal(response.Status)
	}
	request, _ := http.NewRequest("GET", url, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !strings.Contains(string(body), "backfill_done") {
		t.Fatalf("%s %s", response.Status, body)
	}
}
