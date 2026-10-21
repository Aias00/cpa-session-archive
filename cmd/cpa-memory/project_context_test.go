package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cpa-session-archive/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRealStdioProjectContextCompositionAndDocumentReadback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Synthetic instrument\nPurpose: measure humidity.\n\n## Architecture\nThe sampler feeds a bounded queue.\n\n## Installation\nBuild the instrument with its local setup.\n\n## TODO\nRemaining work: add calibration telemetry.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "README.md"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	config := t.TempDir()
	token := filepath.Join(config, "read.token")
	projects := filepath.Join(config, "projects.json")
	if err := os.WriteFile(token, []byte(strings.Repeat("t", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"p": root})
	if err := os.WriteFile(projects, data, 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestMCPStdioHelper$")
	command.Env = append(os.Environ(), "CPA_RECALL_STDIO_HELPER=1", "CPA_RECALL_TEST_ENDPOINT="+server.URL, "CPA_RECALL_TEST_TOKEN="+token, "CPA_RECALL_TEST_PROJECTS="+projects)
	client := mcp.NewClient(&mcp.Implementation{Name: "project-context-stdio", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 7 {
		t.Fatal("registered project composition did not expose the seven expected read tools", err)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "project_context", Arguments: map[string]any{"project_id": "p", "intent": "all"}})
	if err != nil || result.IsError {
		t.Fatal("real stdio context call failed", err)
	}
	data, _ = json.Marshal(result.StructuredContent)
	var packet memory.ProjectContextResult
	if err := json.Unmarshal(data, &packet); err != nil || len(packet.Sections) != 4 || !packet.EvidenceOnly || packet.Coverage.History != "not_requested" {
		t.Fatal("real process lost evidence/intent/coverage metadata", err)
	}
	for _, section := range packet.Sections {
		if len(section.Documents) == 0 {
			t.Fatal("real process lost an intent's Git evidence")
		}
		doc := section.Documents[0]
		data, _ = json.Marshal(doc.Citation)
		var arguments map[string]any
		_ = json.Unmarshal(data, &arguments)
		read, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "project_read_document", Arguments: arguments})
		if err != nil || read.IsError {
			t.Fatal("real process could not read its own Git citation", err)
		}
		data, _ = json.Marshal(read.StructuredContent)
		var evidence memory.ProjectGitEvidence
		if err := json.Unmarshal(data, &evidence); err != nil || evidence != doc {
			t.Fatal("Git citation roundtrip changed the original text or reference", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("default project context queried archive history without opt-in")
	}
}
