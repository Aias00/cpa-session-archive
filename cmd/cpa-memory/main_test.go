package main

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("CPA_RECALL_STDIO_HELPER") != "1" {
		return
	}
	args := []string{"--endpoint", os.Getenv("CPA_RECALL_TEST_ENDPOINT"), "--token-file", os.Getenv("CPA_RECALL_TEST_TOKEN"), "--allow-all-projects"}
	if projects := os.Getenv("CPA_RECALL_TEST_PROJECTS"); projects != "" {
		args = append(args, "--projects-file", projects)
	}
	if err := run(context.Background(), args); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
func TestRealStdioHandshakeAndStatus(t *testing.T) {
	token := strings.Repeat("t", 40)
	path := filepath.Join(t.TempDir(), "read.token")
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("incorrect HTTP boundary")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"backfill_done":true,"lag":0,"sources":7}`))
	}))
	defer server.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestMCPStdioHelper$")
	command.Env = append(os.Environ(), "CPA_RECALL_STDIO_HELPER=1", "CPA_RECALL_TEST_ENDPOINT="+server.URL, "CPA_RECALL_TEST_TOKEN="+path)
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-regression", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 4 {
		t.Fatalf("%+v %v", tools, err)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "archive_status", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestBridgeRequiresExplicitScopeAndPrivateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	os.WriteFile(path, []byte(strings.Repeat("x", 40)), 0600)
	if err := run(context.Background(), []string{"--token-file", path}); err == nil {
		t.Fatal("implicit owner scope accepted")
	}
	os.Chmod(path, 0644)
	if err := run(context.Background(), []string{"--token-file", path, "--allow-all-projects"}); err == nil {
		t.Fatal("public token file accepted")
	}
}
