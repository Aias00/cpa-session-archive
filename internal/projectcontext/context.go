// Package projectcontext builds bounded evidence packets from registered Git
// documents, reviewed knowledge and explicitly requested archive searches.
package projectcontext

import (
	"context"
	"crypto/sha256"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cpa-session-archive/internal/memory"
)

const (
	maxDocumentExcerpts = 3
	maxMemoryEntries    = 3
	maxHistoryExcerpts  = 3
	maxExcerptRunes     = 1400
)

type Config struct {
	Projects  map[string]string
	Scope     memory.Scope
	Source    memory.Reader
	Knowledge memory.Knowledge
}

type Store struct {
	git       *gitSource
	source    memory.Reader
	knowledge memory.Knowledge
}

func New(config Config) (*Store, error) {
	git, err := newGitSource(config.Projects, config.Scope)
	if err != nil {
		return nil, err
	}
	return &Store{git: git, source: config.Source, knowledge: config.Knowledge}, nil
}

type intentSpec struct {
	name, purpose string
	terms         []string
	history       []string
}

var intents = []intentSpec{
	{"overview", "project purpose and capabilities", []string{"overview", "purpose", "features", "introduction", "why", "what", "概述", "用途", "功能", "介绍"}, []string{"用途", "purpose"}},
	{"architecture", "recorded architecture and data flow", []string{"architecture", "components", "data flow", "design", "架构", "模块", "数据流", "设计"}, []string{"架构", "architecture"}},
	{"followups", "recorded open work, not a fresh issue audit", []string{"todo", "roadmap", "known issues", "limitations", "remaining", "next steps", "follow-up", "待办", "后续", "未完成", "缺口", "限制", "问题"}, []string{"待办", "TODO"}},
	{"installation", "documented setup, not an executed installation", []string{"install", "installation", "setup", "quickstart", "getting started", "build", "deploy", "deployment", "configuration", "prerequisites", "requirements", "安装", "部署", "配置", "启动", "环境"}, []string{"安装", "install"}},
}

func selectIntents(value string) ([]intentSpec, string, error) {
	if value == "" || value == "all" {
		return intents, "all", nil
	}
	for _, intent := range intents {
		if intent.name == value {
			return []intentSpec{intent}, value, nil
		}
	}
	return nil, "", memory.ErrInvalidQuery
}

// English cues match word boundaries; Chinese cues deliberately match text.
// This routes evidence, not probabilities or an assertion of answerability.
func cueMatch(text, cue string) bool {
	text = strings.ToLower(text)
	for offset := 0; offset <= len(text); {
		i := strings.Index(text[offset:], cue)
		if i < 0 {
			return false
		}
		i += offset
		first, _ := utf8.DecodeRuneInString(cue)
		if first > unicode.MaxASCII {
			return true
		}
		left, right := true, true
		if i > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:i])
			left = !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}
		if end := i + len(cue); end < len(text) {
			r, _ := utf8.DecodeRuneInString(text[end:])
			right = !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}
		if left && right {
			return true
		}
		offset = i + len(cue)
	}
	return false
}

func cueScore(text string, terms []string) int {
	score := 0
	for _, cue := range terms {
		if cueMatch(text, cue) {
			score++
		}
	}
	return score
}

type documentBlock struct {
	document gitDocument
	start    int
	end      int
	heading  string
	text     string
	score    int
}

func markdownHeading(line string) string {
	trimmed := strings.TrimSpace(line)
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level < 1 || level > 6 || level == len(trimmed) || trimmed[level] != ' ' {
		return ""
	}
	return strings.TrimSpace(trimmed[level:])
}

func documentBlocks(document gitDocument) []documentBlock {
	lines := strings.SplitAfter(document.text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	blocks := []documentBlock{}
	start, size, heading, fence := 0, 0, "", ""
	headings := [6]string{}
	flush := func(end int) {
		if end <= start {
			return
		}
		text, err := excerptLines(document.text, start+1, end)
		body := false
		for _, line := range lines[start:end] {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && markdownHeading(line) == "" && trimmed != "---" && trimmed != "```" && trimmed != "~~~" {
				body = true
				break
			}
		}
		if err == nil && body && utf8.RuneCountInString(text) <= maxExcerptRunes {
			blocks = append(blocks, documentBlock{document: document, start: start + 1, end: end, heading: heading, text: text})
		}
	}
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if fence == "" {
			if next := markdownHeading(line); next != "" {
				flush(index)
				level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
				headings[level-1] = next
				for i := level; i < len(headings); i++ {
					headings[i] = ""
				}
				start, size, heading = index, 0, strings.Join(headings[:level], " / ")
			}
		}
		count := utf8.RuneCountInString(line)
		if index > start && (size+count > maxExcerptRunes || index-start >= 60) {
			flush(index)
			start, size = index, 0
		}
		// Long single lines cannot be clipped while preserving line citations.
		// They are omitted and disclosed by the packet builder.
		if count > maxExcerptRunes {
			flush(index)
			start, size = index+1, 0
			continue
		}
		size += count
		for _, marker := range []string{"```", "~~~"} {
			if strings.HasPrefix(trimmed, marker) {
				if fence == "" {
					fence = marker
				} else if fence == marker {
					fence = ""
				}
				break
			}
		}
	}
	flush(len(lines))
	return blocks
}

func selectDocuments(snapshot gitSnapshot, spec intentSpec) ([]memory.ProjectGitEvidence, bool, bool) {
	blocks := []documentBlock{}
	longLine := false
	for _, document := range snapshot.documents {
		for _, line := range strings.SplitAfter(document.text, "\n") {
			if utf8.RuneCountInString(line) > maxExcerptRunes {
				longLine = true
			}
		}
		for _, block := range documentBlocks(document) {
			block.score = 8*cueScore(block.heading, spec.terms) + 3*cueScore(strings.NewReplacer("-", " ", "_", " ", "/", " ", ".", " ").Replace(document.path), spec.terms) + cueScore(block.text, spec.terms)
			if spec.name == "overview" && block.start == 1 && strings.HasPrefix(strings.ToLower(document.path), "readme") {
				block.score += 12
			}
			if block.score > 0 {
				blocks = append(blocks, block)
			}
		}
	}
	sort.SliceStable(blocks, func(i, j int) bool {
		if blocks[i].score != blocks[j].score {
			return blocks[i].score > blocks[j].score
		}
		if blocks[i].document.path != blocks[j].document.path {
			return blocks[i].document.path < blocks[j].document.path
		}
		return blocks[i].start < blocks[j].start
	})
	// Select the first excerpt from each matching document before additional
	// windows from the same file, without merging their citation identities.
	seen := map[string]int{}
	type rounded struct {
		block documentBlock
		round int
	}
	rounds := make([]rounded, 0, len(blocks))
	for _, block := range blocks {
		seen[block.document.path]++
		rounds = append(rounds, rounded{block, seen[block.document.path]})
	}
	sort.SliceStable(rounds, func(i, j int) bool { return rounds[i].round < rounds[j].round })
	result := []memory.ProjectGitEvidence{}
	for _, item := range rounds[:min(maxDocumentExcerpts, len(rounds))] {
		block := item.block
		result = append(result, memory.ProjectGitEvidence{Citation: memory.ProjectGitCitation{ProjectID: snapshot.projectID, Commit: snapshot.commit, Path: block.document.path, SHA256: block.document.hash, StartLine: block.start, EndLine: block.end}, Text: block.text})
	}
	return result, len(rounds) > maxDocumentExcerpts, longLine
}

func addIssue(values *[]string, issue string) {
	for _, existing := range *values {
		if existing == issue {
			return
		}
	}
	*values = append(*values, issue)
}

func (s *Store) reviewed(ctx context.Context, projectID string) ([]memory.MemoryEntry, string, []string) {
	if s.knowledge == nil {
		return nil, "not_configured", nil
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := s.knowledge.Memories(bounded, memory.KnowledgeQuery{ProjectID: projectID, Limit: 12})
	if err != nil || result.Authority != "reviewed-git" {
		return nil, "unavailable", []string{"reviewed_knowledge_unavailable"}
	}
	state := "complete"
	issues := []string{}
	if result.Truncated || result.Unavailable > 0 || len(result.Entries) > 12 {
		state = "partial"
		issues = append(issues, "reviewed_knowledge_incomplete")
	}
	entries := []memory.MemoryEntry{}
	for _, entry := range result.Entries[:min(12, len(result.Entries))] {
		if entry.Global || entry.ProjectID != projectID || entry.Status != "verified" || len(entry.Sources) > 8 || utf8.RuneCountInString(entry.Statement) > 4000 {
			state = "partial"
			addIssue(&issues, "reviewed_knowledge_ineligible")
			continue
		}
		valid := true
		for _, citation := range entry.Sources {
			if s.source == nil {
				valid = false
				break
			}
			evidence, err := s.source.ReadSource(bounded, citation)
			if err != nil || evidence.ProjectID != projectID || evidence.Citation != citation {
				valid = false
				break
			}
		}
		if !valid {
			state = "partial"
			addIssue(&issues, "reviewed_knowledge_source_unavailable")
			continue
		}
		entries = append(entries, entry)
	}
	return entries, state, issues
}

func (s *Store) history(ctx context.Context, projectID string, spec intentSpec) ([]memory.Hit, string, []string, bool) {
	hits, issues := []memory.Hit{}, []string{"bounded_literal_history_not_exhaustive"}
	if s.source == nil {
		return hits, "unavailable", append(issues, "archive_history_unavailable"), false
	}
	state, truncated, completed := "complete", false, 0
	seen := map[[32]byte]bool{}
	for _, term := range spec.history {
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		result, err := s.source.Search(bounded, memory.Query{Text: term, Mode: "diverse", ProjectID: projectID, Limit: 2})
		if err != nil {
			cancel()
			state = "partial"
			addIssue(&issues, "archive_history_search_failed")
			continue
		}
		completed++
		if result.Truncated || !result.Status.BackfillDone || !result.Status.MessageMetadataDone || result.Status.Lag != 0 || (result.Retrieval != nil && !result.Retrieval.SemanticComplete) {
			state, truncated = "partial", true
			addIssue(&issues, "archive_history_incomplete")
		}
		for _, hit := range result.Hits[:min(2, len(result.Hits))] {
			evidence, err := s.source.ReadSource(bounded, hit.Citation)
			if err != nil || hit.ProjectID != projectID || evidence.ProjectID != projectID || evidence.Citation != hit.Citation || evidence.Text != hit.Snippet {
				state = "partial"
				addIssue(&issues, "archive_history_source_unavailable")
				continue
			}
			key := sha256.Sum256([]byte(hit.Role + "\x00" + hit.Kind + "\x00" + hit.ToolName + "\x00" + hit.Snippet))
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(hits) == maxHistoryExcerpts {
				state, truncated = "partial", true
				addIssue(&issues, "archive_history_excerpt_limit")
				continue
			}
			hits = append(hits, hit)
		}
		if len(result.Hits) > 2 {
			state, truncated = "partial", true
			addIssue(&issues, "archive_history_excerpt_limit")
		}
		cancel()
	}
	if completed == 0 {
		state = "unavailable"
	}
	return hits, state, issues, truncated
}

func (s *Store) ReadProjectSource(ctx context.Context, citation memory.ProjectGitCitation) (memory.ProjectGitEvidence, error) {
	return s.git.read(ctx, citation)
}

// ProjectContext returns routed excerpts, not an inferred answer or review.
// All Git data uses one HEAD snapshot; optional failures are explicitly partial.
func (s *Store) ProjectContext(ctx context.Context, query memory.ProjectContextQuery) (memory.ProjectContextResult, error) {
	specs, intent, err := selectIntents(query.Intent)
	if err != nil {
		return memory.ProjectContextResult{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot, err := s.git.snapshot(bounded, query.ProjectID)
	if err != nil {
		return memory.ProjectContextResult{}, err
	}
	out := memory.ProjectContextResult{ProjectID: snapshot.projectID, Commit: snapshot.commit, Intent: intent, Kind: "cited_evidence_packet", EvidenceOnly: true, Sections: []memory.ProjectContextSection{}, Coverage: memory.ProjectContextCoverage{Documents: "complete", Knowledge: "not_configured", History: "not_requested", Issues: []string{}}, Truncated: snapshot.truncated}
	if snapshot.truncated {
		out.Coverage.Documents = "partial"
	}
	for _, issue := range snapshot.issues {
		addIssue(&out.Coverage.Issues, issue)
	}
	entries, knowledgeState, issues := s.reviewed(bounded, snapshot.projectID)
	out.Coverage.Knowledge = knowledgeState
	for _, issue := range issues {
		addIssue(&out.Coverage.Issues, issue)
	}
	if query.IncludeHistory {
		out.Coverage.History = "complete"
	}
	for _, spec := range specs {
		sectionHistoryState := "not_requested"
		documents, clipped, longLine := selectDocuments(snapshot, spec)
		section := memory.ProjectContextSection{Intent: spec.name, State: "no_matching_evidence", Documents: documents, Memories: []memory.MemoryEntry{}, History: []memory.Hit{}, Gaps: []string{}, Truncated: clipped || snapshot.truncated}
		if clipped {
			addIssue(&section.Gaps, "document_excerpt_limit")
		}
		if longLine {
			section.Truncated = true
			out.Coverage.Documents = "partial"
			addIssue(&section.Gaps, "document_long_line_omitted")
		}
		if len(documents) == 0 {
			if snapshot.truncated || longLine {
				addIssue(&section.Gaps, "committed_document_search_incomplete")
			} else {
				addIssue(&section.Gaps, "no_matching_committed_document")
			}
		}
		for _, entry := range entries {
			if spec.name != "overview" && cueScore(entry.Title+"\n"+entry.Statement, spec.terms) == 0 {
				continue
			}
			if len(section.Memories) == maxMemoryEntries {
				section.Truncated = true
				addIssue(&section.Gaps, "reviewed_memory_limit")
				break
			}
			section.Memories = append(section.Memories, entry)
		}
		if len(section.Memories) == 0 {
			switch knowledgeState {
			case "not_configured":
				addIssue(&section.Gaps, "reviewed_knowledge_not_configured")
			case "complete":
				addIssue(&section.Gaps, "no_matching_reviewed_memory")
			default:
				addIssue(&section.Gaps, "reviewed_knowledge_incomplete")
			}
		}
		if query.IncludeHistory {
			var state string
			var historyTruncated bool
			section.History, state, issues, historyTruncated = s.history(bounded, snapshot.projectID, spec)
			sectionHistoryState = state
			section.Truncated = section.Truncated || historyTruncated
			if state == "unavailable" || (state == "partial" && out.Coverage.History != "unavailable") {
				out.Coverage.History = state
			}
			for _, issue := range issues {
				addIssue(&section.Gaps, issue)
				addIssue(&out.Coverage.Issues, issue)
			}
			if len(section.History) == 0 {
				if state == "complete" {
					addIssue(&section.Gaps, "no_matching_historical_excerpt")
				} else {
					addIssue(&section.Gaps, "history_evidence_unavailable_or_incomplete")
				}
			}
		} else {
			addIssue(&section.Gaps, "history_not_requested")
		}
		if len(section.Documents)+len(section.Memories)+len(section.History) > 0 {
			section.State = "evidence_found"
		}
		if knowledgeState == "partial" || knowledgeState == "unavailable" || (query.IncludeHistory && sectionHistoryState != "complete") || snapshot.truncated || longLine {
			section.State = "incomplete"
		}
		if spec.name == "followups" {
			addIssue(&section.Gaps, "recorded_work_only_not_a_live_issue_audit")
		}
		out.Truncated = out.Truncated || section.Truncated
		out.Sections = append(out.Sections, section)
	}
	if ctx.Err() != nil {
		return memory.ProjectContextResult{}, ctx.Err()
	}
	if bounded.Err() != nil {
		return memory.ProjectContextResult{}, memory.ErrQueryBudget
	}
	if err := s.git.verifySnapshot(bounded, snapshot.projectID, snapshot.commit); err != nil {
		if ctx.Err() != nil {
			return memory.ProjectContextResult{}, ctx.Err()
		}
		if bounded.Err() != nil {
			return memory.ProjectContextResult{}, memory.ErrQueryBudget
		}
		return memory.ProjectContextResult{}, err
	}
	return out, nil
}

var _ memory.ProjectContext = (*Store)(nil)
