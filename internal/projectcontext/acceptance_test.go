package projectcontext_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/knowledge"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/projectcontext"
	"cpa-session-archive/internal/storage"
	"gopkg.in/yaml.v3"
)

// These public synthetic facts test evidence assembly, not owner relevance.
func TestAcceptanceFourIntentsUseCommittedEvidenceAndGitReadback(t *testing.T) {
	root, project, facts := acceptanceRepository(t)
	store := acceptanceStore(t, root, memory.Scope{ProjectID: project}, nil, nil)
	ctx := context.Background()
	head := acceptanceGit(t, root, "rev-parse", "HEAD")
	for _, intent := range []string{"overview", "architecture", "followups", "installation"} {
		t.Run(intent, func(t *testing.T) {
			packet, err := store.ProjectContext(ctx, memory.ProjectContextQuery{Intent: intent})
			if err != nil {
				t.Fatal(err)
			}
			if packet.ProjectID != project || packet.Commit != head || packet.Kind != "cited_evidence_packet" || !packet.EvidenceOnly || len(packet.Sections) != 1 || packet.Sections[0].Intent != intent {
				t.Fatal("intent packet lost its explicit project, evidence-only boundary, or committed snapshot")
			}
			section := packet.Sections[0]
			found := false
			for _, evidence := range section.Documents {
				acceptanceReadback(t, store, root, evidence)
				found = found || strings.Contains(evidence.Text, facts[intent])
			}
			if !found {
				t.Fatal("intent failed to expose its distinct supporting fixture phrase")
			}
			if packet.Coverage.History != "not_requested" || len(section.History) != 0 {
				t.Fatal("default packet implicitly queried or included archive history")
			}
		})
	}
	packet, err := store.ProjectContext(ctx, memory.ProjectContextQuery{Intent: "all"})
	if err != nil || len(packet.Sections) != 4 {
		t.Fatal("combined packet lost one of the four intent sections", err)
	}
	for _, section := range packet.Sections {
		found := false
		for _, evidence := range section.Documents {
			acceptanceReadback(t, store, root, evidence)
			found = found || strings.Contains(evidence.Text, facts[section.Intent])
		}
		if !found {
			t.Fatal("combined packet lost an intent's supporting evidence")
		}
	}
	if acceptanceGit(t, root, "rev-parse", "HEAD") != head || acceptanceGit(t, root, "status", "--porcelain", "--untracked-files=all") != "" {
		t.Fatal("reading project context changed its Git authority")
	}
}

func TestAcceptanceDirtyWorkIsPreservedAndDoesNotBecomeEvidence(t *testing.T) {
	root, project, facts := acceptanceRepository(t)
	store := acceptanceStore(t, root, memory.Scope{ProjectID: project}, nil, nil)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Uncommitted draft\n\nUNREVIEWED_WORKTREE_ONLY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "private-draft.md"), []byte("UNTRACKED_ONLY architecture installation followups\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("human staged work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	acceptanceGit(t, root, "add", "unrelated.txt")
	before := acceptanceGit(t, root, "status", "--porcelain", "--untracked-files=all")
	index := acceptanceGit(t, root, "diff", "--cached", "--binary")
	head := acceptanceGit(t, root, "rev-parse", "HEAD")
	packet, err := store.ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "all"})
	if err != nil {
		t.Fatal(err)
	}
	foundCommitted := false
	for _, section := range packet.Sections {
		for _, evidence := range section.Documents {
			if strings.Contains(evidence.Text, "UNREVIEWED_WORKTREE_ONLY") || strings.Contains(evidence.Text, "UNTRACKED_ONLY") {
				t.Fatal("uncommitted text became project evidence")
			}
			foundCommitted = foundCommitted || strings.Contains(evidence.Text, facts["overview"])
			acceptanceReadback(t, store, root, evidence)
		}
	}
	if !foundCommitted || acceptanceGit(t, root, "rev-parse", "HEAD") != head || acceptanceGit(t, root, "status", "--porcelain", "--untracked-files=all") != before || acceptanceGit(t, root, "diff", "--cached", "--binary") != index {
		t.Fatal("context reading lost committed evidence or altered unrelated Git work")
	}
}

func TestAcceptanceMissingEvidenceIsExplicitAndSourceChangesFailClosed(t *testing.T) {
	root, project, _ := acceptanceRepository(t)
	store := acceptanceStore(t, root, memory.Scope{ProjectID: project}, nil, nil)
	packet, err := store.ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "installation"})
	if err != nil || len(packet.Sections) != 1 || len(packet.Sections[0].Documents) == 0 {
		t.Fatal("installation fixture did not produce a source", err)
	}
	oldCitation := packet.Sections[0].Documents[0].Citation
	acceptanceGit(t, root, "rm", "docs/installation.md")
	acceptanceGit(t, root, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "remove synthetic installation instructions")
	if _, err := store.ReadProjectSource(context.Background(), oldCitation); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
		t.Fatal("old Git snapshot was silently replaced after HEAD changed", err)
	}
	packet, err = store.ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "installation", IncludeHistory: true})
	if err != nil || len(packet.Sections) != 1 {
		t.Fatal(err)
	}
	section := packet.Sections[0]
	if len(section.Documents) != 0 || len(section.History) != 0 || section.State == "evidence_found" || len(section.Gaps) == 0 || packet.Coverage.History != "unavailable" {
		t.Fatal("missing documentation or unavailable history was treated as complete evidence")
	}
}

func TestAcceptanceProjectAuthorizationIsAppliedBeforeEvidenceReads(t *testing.T) {
	root, project, _ := acceptanceRepository(t)
	store := acceptanceStore(t, root, memory.Scope{ProjectID: project}, nil, nil)
	ctx := context.Background()
	for _, q := range []memory.ProjectContextQuery{{ProjectID: "unregistered", Intent: "all"}, {ProjectID: project, Intent: "unrecognized-intent"}} {
		if _, err := store.ProjectContext(ctx, q); err == nil {
			t.Fatal("invalid project or intent was silently substituted")
		}
	}
	packet, err := store.ProjectContext(ctx, memory.ProjectContextQuery{Intent: "overview"})
	if err != nil || len(packet.Sections[0].Documents) == 0 {
		t.Fatal(err)
	}
	forged := packet.Sections[0].Documents[0].Citation
	forged.ProjectID = "unregistered"
	if _, err := store.ReadProjectSource(ctx, forged); err == nil {
		t.Fatal("a forged project citation escaped the registered scope")
	}
	sessionStore, err := projectcontext.New(projectcontext.Config{Projects: map[string]string{project: root}, Scope: memory.Scope{SessionID: "synthetic-session"}})
	if err == nil {
		if _, err := sessionStore.ProjectContext(ctx, memory.ProjectContextQuery{ProjectID: project}); err == nil {
			t.Fatal("session-only scope granted project-wide Git access")
		}
	}
}

func TestAcceptanceReviewedKnowledgeAndOptInHistoryKeepTheirSources(t *testing.T) {
	root, project, _ := acceptanceRepository(t)
	engine, raw, citation := acceptanceHistory(t, project)
	accepted := "Architecture fixture decision: keep the numerical solver behind a bounded channel."
	draft := "UNAPPROVED_SYNTHETIC_MEMORY"
	contextData, err := yaml.Marshal(map[string]any{"project": "synthetic acceptance", "reviewed_memories": []any{
		map[string]any{"id": "synthetic-approved", "title": "Architecture accepted queue", "status": "verified", "memory_kind": "decision", "summary": accepted, "review_after": "2099-01-01", "archive_sources": []memory.Citation{citation}},
		map[string]any{"id": "synthetic-draft", "title": "Architecture pending queue", "status": "draft", "memory_kind": "decision", "summary": draft},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codex", "ai-context.yaml"), contextData, 0600); err != nil {
		t.Fatal(err)
	}
	acceptanceGit(t, root, "add", ".codex/ai-context.yaml")
	acceptanceGit(t, root, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "add synthetic reviewed and draft metadata")
	scope := memory.Scope{ProjectID: project}
	reader, err := memory.WithScope(engine, scope)
	if err != nil {
		t.Fatal(err)
	}
	library, err := knowledge.New(knowledge.Config{Projects: map[string]string{project: root}, Scope: scope, Source: reader})
	if err != nil {
		t.Fatal(err)
	}
	store := acceptanceStore(t, root, scope, reader, library)
	packet, err := store.ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "architecture"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, section := range packet.Sections {
		for _, entry := range section.Memories {
			if entry.Statement == draft || entry.Status != "verified" || entry.Commit == "" {
				t.Fatal("an unreviewed or uncommitted entry became active context")
			}
			found = found || entry.Statement == accepted
		}
		if len(section.History) != 0 {
			t.Fatal("knowledge inclusion implicitly enabled history")
		}
	}
	if !found {
		t.Fatal("verified synthetic project knowledge was not included")
	}
	packet, err = store.ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "installation", IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	historyCount := 0
	for _, section := range packet.Sections {
		for _, hit := range section.History {
			historyCount++
			evidence, err := reader.ReadSource(context.Background(), hit.Citation)
			if err != nil || evidence.Text != hit.Snippet || evidence.ProjectID != project {
				t.Fatal("history excerpt lost its authorized retained source", err)
			}
		}
	}
	if historyCount == 0 || packet.Coverage.History == "not_requested" || !packet.EvidenceOnly {
		t.Fatal("opt-in history was absent or promoted beyond an evidence packet")
	}
	if _, err := raw.DB.Exec("DELETE FROM records WHERE request_id=?", "synthetic-history"); err != nil {
		t.Fatal(err)
	}
	packet, err = store.ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "architecture"})
	if err != nil {
		t.Fatal(err)
	}
	if packet.Coverage.Knowledge == "complete" {
		t.Fatal("unavailable reviewed source was reported as complete knowledge coverage")
	}
	for _, section := range packet.Sections {
		for _, entry := range section.Memories {
			if entry.Statement == accepted {
				t.Fatal("reviewed entry with deleted evidence remained available")
			}
		}
	}
}

func acceptanceRepository(t *testing.T) (string, string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	acceptanceGit(t, root, "init", "-q")
	acceptanceGit(t, root, "config", "user.name", "Synthetic Acceptance")
	acceptanceGit(t, root, "config", "user.email", "synthetic@example.invalid")
	facts := map[string]string{
		"overview":     "Juniper is a synthetic simulator for small electric circuits.",
		"architecture": "The lexer sends typed circuit nodes to the numerical solver through a bounded channel.",
		"followups":    "Replace the prototype matrix allocator with a bounded arena.",
		"installation": "Build the synthetic demo with `make install-demo` after installing the Go toolchain.",
	}
	files := map[string]string{"README.md": "# Juniper Circuit Simulator\n\n## Project overview\n\n" + facts["overview"] + "\n",
		"docs/architecture.md": "# Architecture\n\n" + facts["architecture"] + "\n",
		"TODO.md":              "# Followups\n\n- [ ] " + facts["followups"] + "\n",
		"docs/installation.md": "# Installation\n\n" + facts["installation"] + "\n"}
	for path, text := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	acceptanceGit(t, root, "add", "README.md", "docs", "TODO.md")
	acceptanceGit(t, root, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "synthetic four-intent evidence")
	return root, "synthetic:juniper", facts
}

func acceptanceStore(t *testing.T, root string, scope memory.Scope, source memory.Reader, library memory.Knowledge) *projectcontext.Store {
	t.Helper()
	store, err := projectcontext.New(projectcontext.Config{Projects: map[string]string{"synthetic:juniper": root}, Scope: scope, Source: source, Knowledge: library})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func acceptanceReadback(t *testing.T, store *projectcontext.Store, root string, evidence memory.ProjectGitEvidence) {
	t.Helper()
	got, err := store.ReadProjectSource(context.Background(), evidence.Citation)
	if err != nil || got.Text != evidence.Text || got.Citation != evidence.Citation {
		t.Fatal("project source reader did not reproduce the exact citation and excerpt", err)
	}
	c := evidence.Citation
	raw := acceptanceGitRaw(t, root, "show", c.Commit+":"+c.Path)
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != c.SHA256 {
		t.Fatal("Git citation did not bind the complete committed file")
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if c.StartLine < 1 || c.EndLine < c.StartLine || c.EndLine > len(lines) || strings.Join(lines[c.StartLine-1:c.EndLine], "") != evidence.Text {
		t.Fatal("Git citation line range did not reproduce the exact excerpt")
	}
}

func acceptanceHistory(t *testing.T, project string) (*memory.Engine, *archive.Store, memory.Citation) {
	t.Helper()
	dir := t.TempDir()
	raw, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.DB.Close() })
	if err := raw.PutBatch([]archive.Record{{RequestID: "synthetic-history", SessionID: "synthetic-session", Facets: map[string][]string{"project.id": {project}}, OriginalRequest: []byte(`{"messages":[{"role":"assistant","content":"historicalneedle architecture installation install setup configuration 安装 配置: Architecture fixture decision: keep the numerical solver behind a bounded channel. Historical installation used a fixture-only staging folder."}]}`)}}); err != nil {
		t.Fatal(err)
	}
	engine, err := memory.Open(filepath.Join(dir, "memory.sqlite"), storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	for {
		n, err := engine.Sync(context.Background(), 16)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	hits, err := engine.Search(context.Background(), memory.Query{Text: "historicalneedle", ProjectID: project})
	if err != nil || len(hits.Hits) != 1 {
		t.Fatal("synthetic history fixture was not indexed", err)
	}
	return engine, raw, hits.Hits[0].Citation
}

func acceptanceGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(string(acceptanceGitRaw(t, root, args...)))
}

func acceptanceGitRaw(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			command.Env = append(command.Env, value)
		}
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal("synthetic Git fixture command failed", err)
	}
	return output
}
