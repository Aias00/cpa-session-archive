package projectcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/memory"
)

type packetReader struct {
	queries []memory.Query
	search  func(memory.Query) (memory.SearchResult, error)
	read    func(memory.Citation) (memory.Evidence, error)
}

func (r *packetReader) Search(_ context.Context, q memory.Query) (memory.SearchResult, error) {
	r.queries = append(r.queries, q)
	return r.search(q)
}
func (r *packetReader) ReadSource(_ context.Context, c memory.Citation) (memory.Evidence, error) {
	return r.read(c)
}
func (*packetReader) Recap(context.Context, memory.Query) (memory.RecapResult, error) {
	return memory.RecapResult{}, errors.New("unexpected recap")
}
func (*packetReader) Status(context.Context) (memory.Status, error) {
	return memory.Status{}, errors.New("unexpected status")
}

type packetKnowledge struct {
	result memory.KnowledgeResult
	err    error
	query  memory.KnowledgeQuery
	writes int
}

func (k *packetKnowledge) Memories(_ context.Context, q memory.KnowledgeQuery) (memory.KnowledgeResult, error) {
	k.query = q
	return k.result, k.err
}
func (k *packetKnowledge) Propose(context.Context, memory.MemoryProposal) (memory.MemoryEntry, error) {
	k.writes++
	return memory.MemoryEntry{}, errors.New("unexpected proposal")
}

func packetFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Fixture\nPurpose: collect public sensor readings.\n\n## Architecture\nThe worker uses a bounded channel.\n\n## Installation\nBuild the fixture and set its local configuration.\n\n## TODO\nRemaining work: add a temperature probe.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "README.md"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	return root
}

func packetStore(t *testing.T, reader memory.Reader, knowledge memory.Knowledge) *Store {
	t.Helper()
	s, err := New(Config{Projects: map[string]string{"p": packetFixture(t)}, Scope: memory.Scope{ProjectID: "p"}, Source: reader, Knowledge: knowledge})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOptionalFailuresKeepEvidenceAndDoNotBecomeEmptyClaims(t *testing.T) {
	reader := &packetReader{search: func(memory.Query) (memory.SearchResult, error) {
		return memory.SearchResult{}, errors.New("PRIVATE source transport diagnostic")
	}}
	knowledge := &packetKnowledge{err: errors.New("PRIVATE Git configuration")}
	s := packetStore(t, reader, knowledge)
	packet, err := s.ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "installation", IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	section := packet.Sections[0]
	if len(section.Documents) == 0 || section.State != "incomplete" || packet.Coverage.Knowledge != "unavailable" || packet.Coverage.History != "unavailable" {
		t.Fatal("dependency failures discarded Git evidence or became complete absence")
	}
	for _, gap := range section.Gaps {
		if gap == "no_matching_historical_excerpt" || gap == "no_matching_reviewed_memory" {
			t.Fatal("failed lookup was described as no matching evidence")
		}
	}
	data, _ := json.Marshal(packet)
	if strings.Contains(string(data), "PRIVATE") {
		t.Fatal("dependency diagnostics leaked")
	}
	if len(reader.queries) != 2 || knowledge.query.ProjectID != "p" || knowledge.query.Global || knowledge.query.Drafts || knowledge.writes != 0 {
		t.Fatal("intent packet broadened scope, queried drafts or wrote knowledge")
	}
	for _, q := range reader.queries {
		if q.ProjectID != "p" || q.Mode != "diverse" || q.Limit != 2 {
			t.Fatal("archive history lost project and bounded literal selection")
		}
	}
}

func TestHistoryRevalidatesExactTextAndDoesNotContaminateOtherSections(t *testing.T) {
	citation := memory.Citation{NormalizerVersion: 2, RequestID: "one", Revision: "version", Side: "request", Pointer: "/messages/0/content", Start: 0, End: 4}
	reader := &packetReader{search: func(q memory.Query) (memory.SearchResult, error) {
		if q.Text == "用途" || q.Text == "purpose" {
			return memory.SearchResult{}, errors.New("transport unavailable")
		}
		return memory.SearchResult{Hits: []memory.Hit{{ProjectID: "p", Snippet: "安装 evidence", Citation: citation}, {ProjectID: "other", Snippet: "wrong project", Citation: citation}}, Status: memory.Status{BackfillDone: true, MessageMetadataDone: true}}, nil
	}, read: func(c memory.Citation) (memory.Evidence, error) {
		return memory.Evidence{ProjectID: "p", Citation: c, Text: "安装 evidence"}, nil
	}}
	s := packetStore(t, reader, nil)
	packet, err := s.ProjectContext(context.Background(), memory.ProjectContextQuery{IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if packet.Coverage.History != "unavailable" || packet.Sections[0].State != "incomplete" {
		t.Fatal("aggregate coverage lost the failed intent")
	}
	for _, section := range packet.Sections[1:] {
		if len(section.History) != 1 || section.History[0].ProjectID != "p" || section.State != "incomplete" {
			t.Fatal("cross-project evidence escaped live validation")
		}
	}
	// A fully valid later intent must not inherit an earlier intent's failure.
	reader.search = func(q memory.Query) (memory.SearchResult, error) {
		if q.Text == "用途" || q.Text == "purpose" {
			return memory.SearchResult{}, errors.New("transport unavailable")
		}
		return memory.SearchResult{Status: memory.Status{BackfillDone: true, MessageMetadataDone: true}}, nil
	}
	packet, err = s.ProjectContext(context.Background(), memory.ProjectContextQuery{IncludeHistory: true})
	if err != nil || packet.Sections[0].State != "incomplete" || packet.Sections[1].State == "incomplete" {
		t.Fatal("section history coverage was replaced by cumulative packet state", err)
	}
	reader.search = func(memory.Query) (memory.SearchResult, error) {
		return memory.SearchResult{Hits: []memory.Hit{{ProjectID: "p", Snippet: "stale text", Citation: citation}}, Status: memory.Status{BackfillDone: true, MessageMetadataDone: true}}, nil
	}
	packet, err = s.ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "installation", IncludeHistory: true})
	if err != nil || len(packet.Sections[0].History) != 0 || packet.Coverage.History != "partial" {
		t.Fatal("current source text was substituted under a stale search excerpt", err)
	}
}

func TestContextNeverActivatesDraftsOrImplicitlySearchesHistory(t *testing.T) {
	knowledge := &packetKnowledge{result: memory.KnowledgeResult{Authority: "reviewed-git", Entries: []memory.MemoryEntry{
		{ID: "accepted", ProjectID: "p", Status: "verified", Title: "Installation", Statement: "Use the committed fixture setup."},
		{ID: "review-due", ProjectID: "p", Status: "verified", Title: "Installation", Statement: "PRIVATE review-due claim", Validity: &memory.MemoryValidity{State: "review_due"}},
		{ID: "source-unavailable", ProjectID: "p", Status: "verified", Title: "Installation", Statement: "PRIVATE unvalidated claim", Validity: &memory.MemoryValidity{State: "source_unavailable"}},
		{ID: "draft", ProjectID: "p", Status: "draft", Title: "Installation", Statement: "PRIVATE draft"},
		{ID: "other", ProjectID: "other", Status: "verified", Title: "Installation", Statement: "PRIVATE other"},
		{ID: "global", ProjectID: "p", Global: true, Status: "verified", Title: "Installation", Statement: "PRIVATE global"},
	}}}
	reader := &packetReader{search: func(memory.Query) (memory.SearchResult, error) {
		t.Fatal("history queried without explicit request")
		return memory.SearchResult{}, nil
	}}
	s := packetStore(t, reader, knowledge)
	packet, err := s.ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "installation"})
	if err != nil || len(packet.Sections[0].Memories) != 1 || packet.Sections[0].Memories[0].ID != "accepted" || packet.Coverage.Knowledge != "partial" || knowledge.writes != 0 {
		t.Fatal("unreviewed or out-of-scope knowledge entered the packet", err)
	}
	data, _ := json.Marshal(packet)
	if strings.Contains(string(data), "PRIVATE") || len(reader.queries) != 0 {
		t.Fatal("draft/private knowledge or an implicit archive query escaped")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ProjectContext(ctx, memory.ProjectContextQuery{}); err == nil {
		t.Fatal("canceled context became a valid packet")
	}
}

func TestLifecycleCountsKeepContextCoverageIncomplete(t *testing.T) {
	for _, state := range []string{"disputed", "review_due"} {
		t.Run(state, func(t *testing.T) {
			issue := "reviewed_knowledge_has_unresolved_conflicts"
			if state == "review_due" {
				issue = "reviewed_knowledge_requires_revalidation"
			}
			knowledge := &packetKnowledge{result: memory.KnowledgeResult{Authority: "reviewed-git", Entries: []memory.MemoryEntry{}, States: map[string]int{state: 1}}}
			packet, err := packetStore(t, nil, knowledge).ProjectContext(context.Background(), memory.ProjectContextQuery{Intent: "installation"})
			if err != nil || packet.Coverage.Knowledge != "partial" || packet.Sections[0].State != "incomplete" || len(packet.Sections[0].Memories) != 0 {
				t.Fatal("inactive knowledge became complete empty coverage", err)
			}
			if !strings.Contains(strings.Join(packet.Coverage.Issues, ","), issue) {
				t.Fatal("knowledge lifecycle reason was discarded")
			}
		})
	}
}

func TestDocumentSelectionKeepsBodyAndInheritedTopicWithExactLines(t *testing.T) {
	doc := gitDocument{path: "docs/design.md", text: "# Architecture\n\n## Responsibilities\nThe worker owns the bounded queue.\n\n## Empty heading\n\n"}
	doc.hash = documentHash(doc.text)
	blocks := documentBlocks(doc)
	if len(blocks) != 1 || !strings.Contains(blocks[0].heading, "Architecture") || !strings.Contains(blocks[0].text, "bounded queue") {
		t.Fatal("heading-only excerpts displaced the body or parent topic")
	}
	selected, _, _ := selectDocuments(gitSnapshot{projectID: "p", commit: strings.Repeat("a", 40), documents: []gitDocument{doc}}, intents[1])
	if len(selected) != 1 {
		t.Fatal("inherited architecture topic was not selected")
	}
	read, err := excerptLines(doc.text, selected[0].Citation.StartLine, selected[0].Citation.EndLine)
	if err != nil || read != selected[0].Text {
		t.Fatal("heading inheritance changed the original cited text", err)
	}
}
