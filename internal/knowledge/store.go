package knowledge

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"cpa-session-archive/internal/memory"
)

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,80}$`)
var operationID = regexp.MustCompile(`^[A-Za-z0-9_.-]{8,128}$`)

type Config struct {
	GlobalRepository string
	Projects         map[string]string
	Scope            memory.Scope
	Source           memory.Reader
}
type Store struct {
	config Config
	now    func() time.Time
}
type Review struct {
	ID               string
	ProjectID        string
	Global           bool
	ExpectedRevision string
	Action           string
	Note             string
	Confirmed        bool
}

func New(config Config) (*Store, error) {
	if config.Source == nil || (!config.Scope.AllowAll && config.Scope.ProjectID == "" && config.Scope.SessionID == "") {
		return nil, ErrInvalid
	}
	ctx := context.Background()
	if config.GlobalRepository != "" {
		root, err := repositoryRoot(ctx, config.GlobalRepository)
		if err != nil {
			return nil, err
		}
		config.GlobalRepository = root
	}
	projects := map[string]string{}
	for id, path := range config.Projects {
		if strings.TrimSpace(id) == "" {
			return nil, ErrInvalid
		}
		root, err := repositoryRoot(ctx, path)
		if err != nil {
			return nil, err
		}
		projects[id] = root
	}
	config.Projects = projects
	if config.GlobalRepository == "" && len(projects) == 0 {
		return nil, fmt.Errorf("no canonical Git knowledge roots configured")
	}
	return &Store{config: config, now: time.Now}, nil
}
func (s *Store) root(project string, global bool) (string, error) {
	if global {
		if project != "" || !s.config.Scope.AllowAll || s.config.GlobalRepository == "" {
			return "", ErrInvalid
		}
		return s.config.GlobalRepository, nil
	}
	if project == "" {
		project = s.config.Scope.ProjectID
	}
	if project == "" || s.config.Scope.SessionID != "" || (!s.config.Scope.AllowAll && s.config.Scope.ProjectID != project) {
		return "", ErrInvalid
	}
	root := s.config.Projects[project]
	if root == "" {
		return "", fmt.Errorf("project is not registered with a canonical Git root")
	}
	return root, nil
}
func (s *Store) documents(ctx context.Context, root, project string, global, drafts bool) ([]document, error) {
	if !global && !drafts {
		paths, err := gitCommand(ctx, root, "ls-tree", "--name-only", "HEAD", "--", ".codex/ai-context.yaml")
		if err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			return nil, nil
		}
		raw, err := committed(ctx, root, ".codex/ai-context.yaml")
		if err != nil {
			return nil, err
		}
		_, docs, err := projectDocuments(raw, project)
		return docs, err
	}
	prefix := "entries"
	if drafts {
		if global {
			prefix = "inbox/captured"
		} else {
			prefix = ".codex/memory-candidates"
		}
	}
	paths, err := trackedPaths(ctx, root, prefix)
	if err != nil {
		return nil, err
	}
	out := []document{}
	for _, path := range paths {
		if drafts && global && !strings.HasPrefix(path, "inbox/captured/cpa-memory-") {
			continue
		}
		raw, err := committed(ctx, root, path)
		if err != nil {
			return nil, err
		}
		d, err := parseDocument(raw, path, global, project)
		if err != nil {
			return nil, fmt.Errorf("invalid canonical knowledge document")
		}
		out = append(out, d)
	}
	return out, nil
}
func (s *Store) validateSources(ctx context.Context, entry memory.MemoryEntry) error {
	for _, citation := range entry.Sources {
		evidence, err := s.config.Source.ReadSource(ctx, citation)
		if err != nil {
			return err
		}
		if !entry.Global && evidence.ProjectID != entry.ProjectID {
			return fmt.Errorf("source does not belong to the requested project")
		}
	}
	return nil
}
func (s *Store) Memories(ctx context.Context, q memory.KnowledgeQuery) (memory.KnowledgeResult, error) {
	out := memory.KnowledgeResult{Entries: []memory.MemoryEntry{}, Authority: "reviewed-git"}
	if q.Limit < 0 || q.Limit > 100 || utf8.RuneCountInString(q.Text) > 256 {
		return out, ErrInvalid
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if !q.Global && q.ProjectID == "" {
		q.ProjectID = s.config.Scope.ProjectID
	}
	root, err := s.root(q.ProjectID, q.Global)
	if err != nil {
		return out, err
	}
	unlock, err := lockRepository(ctx, root)
	if err != nil {
		return out, err
	}
	defer unlock()
	documents, err := s.documents(ctx, root, q.ProjectID, q.Global, q.Drafts)
	if err != nil {
		return out, err
	}
	for _, d := range documents {
		entry := d.entry
		if scalar(d.meta["sensitivity"]) == "restricted" && q.Global {
			continue
		}
		if q.Drafts {
			if entry.Status != "draft" {
				continue
			}
		} else {
			if entry.Status != "verified" {
				continue
			}
			if date := entry.ReviewAfter; date != "" && date < s.now().Format("2006-01-02") {
				out.Unavailable++
				continue
			}
			if err := s.validateSources(ctx, entry); err != nil {
				out.Unavailable++
				continue
			}
		}
		text := strings.ToLower(entry.Title + "\n" + entry.Statement)
		matched := true
		for _, term := range strings.Fields(strings.ToLower(q.Text)) {
			if !strings.Contains(text, term) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if len(out.Entries) >= q.Limit {
			out.Truncated = true
			break
		}
		entry.Commit = fileCommit(ctx, root, d.path)
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}
func (s *Store) findActive(ctx context.Context, root, project string, global bool, id string) (document, bool, error) {
	docs, err := s.documents(ctx, root, project, global, false)
	if err != nil {
		return document{}, false, err
	}
	var found document
	count := 0
	for _, d := range docs {
		if d.entry.ID == id {
			found = d
			count++
		}
	}
	if count > 1 {
		return document{}, false, fmt.Errorf("duplicate canonical memory identifier")
	}
	return found, count == 1, nil
}
func (s *Store) Propose(ctx context.Context, p memory.MemoryProposal) (memory.MemoryEntry, error) {
	var empty memory.MemoryEntry
	if !operationID.MatchString(p.OperationID) || !utf8.ValidString(p.Statement) || utf8.RuneCountInString(p.Statement) < 10 || utf8.RuneCountInString(p.Statement) > 4000 || utf8.RuneCountInString(p.Title) < 3 || utf8.RuneCountInString(p.Title) > 160 || strings.ContainsAny(p.Title, "\r\n") || len(p.Sources) < 1 || len(p.Sources) > 8 {
		return empty, ErrInvalid
	}
	if p.Kind != "decision" && p.Kind != "fact" && p.Kind != "experience" && p.Kind != "preference" {
		return empty, ErrInvalid
	}
	if !p.Global && p.ProjectID == "" {
		p.ProjectID = s.config.Scope.ProjectID
	}
	root, err := s.root(p.ProjectID, p.Global)
	if err != nil {
		return empty, err
	}
	unlock, err := lockRepository(ctx, root)
	if err != nil {
		return empty, err
	}
	defer unlock()
	if p.ID == "" {
		p.ID = "memory-" + revision([]byte(p.ProjectID + "\x00" + fmt.Sprint(p.Global) + "\x00" + p.OperationID))[:24]
	}
	if !identifier.MatchString(p.ID) {
		return empty, ErrInvalid
	}
	entry := memory.MemoryEntry{ID: p.ID, Title: p.Title, Kind: p.Kind, Statement: p.Statement, Status: "draft", ProjectID: p.ProjectID, Global: p.Global, Sources: p.Sources, OperationID: p.OperationID, BaseRevision: p.BaseRevision}
	if err := s.validateSources(ctx, entry); err != nil {
		return empty, err
	}
	active, exists, err := s.findActive(ctx, root, p.ProjectID, p.Global, p.ID)
	if err != nil {
		return empty, err
	}
	if exists && (p.BaseRevision != active.entry.Revision || p.Kind != active.entry.Kind) {
		return empty, ErrConflict
	}
	file := draftPath(p.ID, p.Global)
	tracked, err := gitCommand(ctx, root, "ls-tree", "--name-only", "HEAD", "--", file)
	if err != nil {
		return empty, err
	}
	if len(tracked) > 0 {
		raw, err := committed(ctx, root, file)
		if err != nil {
			return empty, err
		}
		prior, err := parseDocument(raw, file, p.Global, p.ProjectID)
		if err != nil {
			return empty, err
		}
		if prior.entry.Status == "draft" {
			if sameProposal(prior.entry, entry) {
				prior.entry.Commit = fileCommit(ctx, root, file)
				return prior.entry, nil
			}
			if p.ExpectedRevision != prior.entry.Revision {
				return empty, ErrConflict
			}
		} else if !exists {
			return empty, ErrConflict
		}
	}
	meta := metadata(entry, s.now())
	meta["candidate_state"] = "captured"
	if p.ExpectedRevision != "" {
		meta["revises_draft"] = p.ExpectedRevision
	}
	raw, err := markdown(meta, body(entry))
	if err != nil {
		return empty, err
	}
	if err := commitFiles(ctx, root, "memory: capture sourced candidate "+p.ID, map[string][]byte{file: raw}); err != nil {
		return empty, err
	}
	parsed, err := parseDocument(raw, file, p.Global, p.ProjectID)
	if err != nil {
		return empty, err
	}
	parsed.entry.Commit = fileCommit(ctx, root, file)
	return parsed.entry, nil
}
func sameProposal(a, b memory.MemoryEntry) bool {
	if a.OperationID != b.OperationID || a.ID != b.ID || a.Title != b.Title || a.Kind != b.Kind || a.Statement != b.Statement || a.Global != b.Global || a.ProjectID != b.ProjectID || a.BaseRevision != b.BaseRevision || len(a.Sources) != len(b.Sources) {
		return false
	}
	for i := range a.Sources {
		if a.Sources[i] != b.Sources[i] {
			return false
		}
	}
	return true
}
