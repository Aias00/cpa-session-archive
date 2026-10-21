package recall

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"cpa-session-archive/internal/memory"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type projectContextFake struct {
	mu           sync.Mutex
	queries      []memory.ProjectContextQuery
	citations    []memory.ProjectGitCitation
	result       memory.ProjectContextResult
	evidence     memory.ProjectGitEvidence
	contextError error
	readError    error
}

func (f *projectContextFake) ProjectContext(_ context.Context, query memory.ProjectContextQuery) (memory.ProjectContextResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, query)
	return f.result, f.contextError
}

func (f *projectContextFake) ReadProjectSource(_ context.Context, citation memory.ProjectGitCitation) (memory.ProjectGitEvidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.citations = append(f.citations, citation)
	return f.evidence, f.readError
}

func (f *projectContextFake) calls() ([]memory.ProjectContextQuery, []memory.ProjectGitCitation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]memory.ProjectContextQuery(nil), f.queries...), append([]memory.ProjectGitCitation(nil), f.citations...)
}

func projectContextFixture() *projectContextFake {
	citation := memory.ProjectGitCitation{
		ProjectID: "registered-project", Commit: strings.Repeat("c", 40), Path: "docs/架构 overview.md", SHA256: strings.Repeat("d", 64), StartLine: 7, EndLine: 9,
	}
	evidence := memory.ProjectGitEvidence{Citation: citation, Text: "Committed evidence\n保留原文和符号 `x`\nThird line\n"}
	return &projectContextFake{
		evidence: evidence,
		result: memory.ProjectContextResult{
			ProjectID: citation.ProjectID, Commit: citation.Commit, Intent: "overview", Kind: "evidence_packet", EvidenceOnly: true,
			Sections: []memory.ProjectContextSection{{Intent: "overview", State: "evidence", Documents: []memory.ProjectGitEvidence{evidence}, Memories: []memory.MemoryEntry{}, History: []memory.Hit{}, Gaps: []string{}}},
			Coverage: memory.ProjectContextCoverage{Documents: "available", Knowledge: "not_configured", History: "not_requested", Issues: []string{}},
		},
	}
}

func projectContextSession(t *testing.T, source memory.ProjectContext) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "project-context-regression", Version: "1"}, nil)
	AddProjectContextTools(server, source)
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "project-context-test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestProjectContextMCPToolsAreReadOnlyAndDeclareIntentSchema(t *testing.T) {
	session := projectContextSession(t, projectContextFixture())
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil || len(listed.Tools) != 2 {
		t.Fatal("project context did not expose exactly two read tools", err)
	}
	seen := map[string]bool{}
	for _, tool := range listed.Tools {
		seen[tool.Name] = true
		annotations := tool.Annotations
		if annotations == nil || !annotations.ReadOnlyHint || !annotations.IdempotentHint || annotations.DestructiveHint == nil || *annotations.DestructiveHint || annotations.OpenWorldHint == nil || *annotations.OpenWorldHint {
			t.Fatal("project context tool grants unexpected write or external capabilities", tool.Name)
		}
		if tool.Name == "project_context" {
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil || len(schema.Properties) != 3 || strings.Join(schema.Properties["intent"].Enum, ",") != ",overview,architecture,followups,installation,all" {
				t.Fatal("project context intent schema permits undeclared operations", err)
			}
			if !strings.Contains(tool.Description, "evidence packet") || !strings.Contains(tool.Description, "not a verified summary") {
				t.Fatal("project context tool overstates evidence authority")
			}
		}
	}
	if !seen["project_context"] || !seen["project_read_document"] {
		t.Fatal("project context advertised a write or approval tool")
	}
}

func TestProjectContextMCPPassesQueriesAndEvidenceWithoutRewriting(t *testing.T) {
	source := projectContextFixture()
	session := projectContextSession(t, source)
	ctx := context.Background()
	want := []memory.ProjectContextQuery{{}}
	for _, intent := range []string{"overview", "architecture", "followups", "installation", "all"} {
		want = append(want, memory.ProjectContextQuery{ProjectID: "registered-project", Intent: intent, IncludeHistory: true})
	}
	for _, query := range want {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "project_context", Arguments: query})
		if err != nil || result.IsError {
			t.Fatal("project context call failed", err)
		}
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var packet memory.ProjectContextResult
		if err := json.Unmarshal(raw, &packet); err != nil || !reflect.DeepEqual(packet, source.result) {
			t.Fatal("MCP changed evidence, authority or coverage fields", err)
		}
	}
	queries, _ := source.calls()
	if !reflect.DeepEqual(queries, want) {
		t.Fatal("MCP inferred a project, intent or history request")
	}
	citation := source.evidence.Citation
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "project_read_document", Arguments: citation})
	if err != nil || result.IsError {
		t.Fatal("project document read failed", err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var evidence memory.ProjectGitEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil || evidence != source.evidence {
		t.Fatal("MCP changed committed document text or citation", err)
	}
	_, citations := source.calls()
	if len(citations) != 1 || citations[0] != citation {
		t.Fatal("MCP rewrote the source commit, project, path, hash or line range")
	}
}

func TestProjectContextMCPSchemaRejectsUnknownIntentBeforeCallingSource(t *testing.T) {
	source := projectContextFixture()
	session := projectContextSession(t, source)
	for _, arguments := range []map[string]any{
		{"intent": "approve"},
		{"intent": "Overview"},
		{"intent": "unknown"},
		{"intent": 3},
		{"include_history": "true"},
		{"project_id": 3},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "project_context", Arguments: arguments})
		if err == nil && !result.IsError {
			t.Fatal("MCP schema accepted invalid project context input")
		}
	}
	queries, citations := source.calls()
	if len(queries) != 0 || len(citations) != 0 {
		t.Fatal("invalid intent/type reached the source; schema validation was bypassed")
	}
}

func TestProjectContextMCPPreservesSourceErrorsAsErrors(t *testing.T) {
	for _, name := range []string{"project_context", "project_read_document"} {
		t.Run(name, func(t *testing.T) {
			source := projectContextFixture()
			source.contextError = errors.New("project context is unavailable")
			source.readError = memory.ErrProjectSourceUnavailable
			session := projectContextSession(t, source)
			var arguments any = memory.ProjectContextQuery{ProjectID: "registered-project", Intent: "overview"}
			if name == "project_read_document" {
				arguments = source.evidence.Citation
			}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
			if err != nil || result == nil || !result.IsError {
				t.Fatal("source failure became an empty successful evidence packet", err)
			}
		})
	}
}

func TestProjectContextMCPIsOmittedWithoutConfiguredSource(t *testing.T) {
	session := projectContextSession(t, nil)
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil || len(listed.Tools) != 0 {
		t.Fatal("unconfigured project context advertised unusable tools", err)
	}
}
