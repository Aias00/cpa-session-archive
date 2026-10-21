package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memoryextract"
)

func cliFile(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.WriteFile(path, value, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineCLIReportsArePrivateAndStagingNeverApproves(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	record := archive.Record{RequestID: "public-fixture", SessionID: "fixture-session", Facets: map[string][]string{"project.id": {"fixture-project"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"SYNTHETIC_PRIVATE_MARKER: retain the bounded local queue for this project."}]}`), Response: []byte(`{"choices":[{"message":{"role":"assistant","content":"I suggest observing queue latency."}}]}`)}
	raw, _ := json.Marshal(record)
	sum := sha256.Sum256(raw)
	bundle := memoryextract.Bundle{ArchiveID: strings.Repeat("a", 64), Watermark: 1, Sources: []archive.SourceSnapshot{{Record: record, Revision: hex.EncodeToString(sum[:]), Sequence: 1}}}
	encoded, _ := json.Marshal(bundle)
	bundlePath := filepath.Join(dir, "bundle.json")
	cliFile(t, bundlePath, encoded)
	token := strings.Repeat("t", 40)
	tokenPath := filepath.Join(dir, "token")
	cliFile(t, tokenPath, []byte(token))
	state := filepath.Join(dir, "jobs.sqlite")
	reportPath := filepath.Join(dir, "report.json")
	root := filepath.Join(dir, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(string(out), err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	git("config", "commit.gpgsign", "false")
	git("config", "core.hooksPath", "/dev/null")
	cliFile(t, filepath.Join(root, "README.md"), []byte("# Fixture\n"))
	git("add", "README.md")
	git("commit", "-qm", "fixture")
	initial := git("rev-parse", "HEAD")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Codex-Turn-Metadata") != `{"request_kind":"memory-extraction"}` {
			t.Error("model request lacked recursion tag")
		}
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) != 2 {
			t.Error("invalid provider request")
			w.WriteHeader(400)
			return
		}
		var input memoryextract.Input
		if json.Unmarshal([]byte(request.Messages[1].Content), &input) != nil || len(input.Excerpts) == 0 {
			t.Error("invalid synthetic input")
			w.WriteHeader(400)
			return
		}
		e := input.Excerpts[0]
		content, _ := json.Marshal(map[string]any{"candidates": []memoryextract.DraftCandidate{{Kind: "decision", Title: "Retain bounded queue", Statement: e.Text, Attribution: "user_requirement", Supports: []memoryextract.Support{{ExcerptID: e.ID, Quote: e.Text}}, Action: "new", Targets: []memoryextract.Target{}}}})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 100, "total_tokens": 200}})
	}))
	defer server.Close()
	args := []string{"run", "--state", state, "--bundle", bundlePath, "--project-id", "fixture-project", "--session-id", "fixture-session", "--project-root", root, "--provider-endpoint", server.URL, "--model", "synthetic-model", "--model-token-file", tokenPath, "--scan-pages", "8", "--output", reportPath}
	var stdout, stderr bytes.Buffer
	if err := run(ctx, args, &stdout, &stderr); err != nil {
		t.Fatal(err, stderr.String())
	}
	if calls.Load() != 1 || git("rev-parse", "HEAD") != initial {
		t.Fatal("dry run wrote Git or called twice")
	}
	if strings.Contains(stdout.String(), "SYNTHETIC_PRIVATE_MARKER") || strings.Contains(stdout.String(), token) {
		t.Fatal("default CLI output leaked source text or credential")
	}
	private, err := os.ReadFile(reportPath)
	if err != nil || !bytes.Contains(private, []byte("SYNTHETIC_PRIVATE_MARKER")) {
		t.Fatal("private report lost review evidence", err)
	}
	info, err := os.Stat(reportPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("report permissions are not private", err)
	}
	var reports []memoryextract.Report
	if json.Unmarshal(private, &reports) != nil || len(reports) != 1 || len(reports[0].Candidates) != 1 {
		t.Fatal("invalid report")
	}
	stdout.Reset()
	if err := run(ctx, []string{"stage", "--state", state, "--bundle", bundlePath, "--project-id", "fixture-project", "--session-id", "fixture-session", "--project-root", root, "--job-id", reports[0].JobID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !strings.Contains(stdout.String(), `"status":"draft"`) || !strings.Contains(stdout.String(), `"activated":false`) {
		t.Fatal("stage activated or re-extracted the candidate", stdout.String())
	}
	canonical, err := os.ReadFile(filepath.Join(root, ".codex", "ai-context.yaml"))
	if err == nil && len(canonical) > 0 {
		t.Fatal("CLI wrote active canonical project memory")
	}
	stagedHead := git("rev-parse", "HEAD")
	stdout.Reset()
	args = args[:len(args)-2]
	if err := run(ctx, args, &stdout, &stderr); err != nil || calls.Load() != 1 || git("rev-parse", "HEAD") != stagedHead {
		t.Fatal("unchanged run was not idempotent", err)
	}
	for _, action := range []string{"list", "status"} {
		stdout.Reset()
		if err := run(ctx, []string{action, "--state", state}, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stdout.String(), "SYNTHETIC_PRIVATE_MARKER") || strings.Contains(stdout.String(), token) {
			t.Fatal("operational status leaked private contents")
		}
	}
	stdout.Reset()
	if err := run(ctx, []string{"show", "--state", state, "--job-id", reports[0].JobID}, &stdout, &stderr); err == nil {
		t.Fatal("show printed private job contents without an output file")
	}
}

func TestCLIRequiresExplicitModelAndScopeAndPreservesOutputFiles(t *testing.T) {
	ctx := context.Background()
	var stdout, stderr bytes.Buffer
	if err := run(ctx, nil, &stdout, &stderr); err == nil {
		t.Fatal("missing action accepted")
	}
	if err := run(ctx, []string{"run"}, &stdout, &stderr); err == nil {
		t.Fatal("missing state accepted")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"run", "--state", filepath.Join(dir, "jobs.sqlite")}, &stdout, &stderr); err == nil {
		t.Fatal("missing project accepted")
	}
	path := filepath.Join(dir, "existing.json")
	cliFile(t, path, []byte("preserved"))
	if err := output(path, map[string]string{"secret": "new"}, nil, &stdout); err == nil {
		t.Fatal("existing output overwritten")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "preserved" {
		t.Fatal("existing output changed", err)
	}
}
