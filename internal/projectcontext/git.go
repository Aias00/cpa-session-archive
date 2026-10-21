package projectcontext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cpa-session-archive/internal/memory"
)

const (
	gitDocumentLimit = 64
	gitFileBytes     = 128 << 10
	gitTotalBytes    = 512 << 10
	gitManifestBytes = 256 << 10
	gitDeadline      = 5 * time.Second
)

type gitDocument struct{ path, text, hash string }

type gitSnapshot struct {
	projectID, commit string
	documents         []gitDocument
	truncated         bool
	issues            []string
}

type gitSource struct {
	projects map[string]string
	scope    memory.Scope
	git      string
}

func newGitSource(projects map[string]string, scope memory.Scope) (*gitSource, error) {
	if (!scope.AllowAll && scope.ProjectID == "" && scope.SessionID == "") || (scope.AllowAll && (scope.ProjectID != "" || scope.SessionID != "")) {
		return nil, memory.ErrInvalidQuery
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, memory.ErrProjectSourceUnavailable
	}
	git, err = filepath.Abs(git)
	if err != nil {
		return nil, memory.ErrProjectSourceUnavailable
	}
	s := &gitSource{projects: make(map[string]string, len(projects)), scope: scope, git: git}
	for id, root := range projects {
		if strings.TrimSpace(id) == "" || !filepath.IsAbs(root) || containsControl(id) || containsControl(root) {
			return nil, errors.New("invalid project Git registration")
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, memory.ErrProjectSourceUnavailable
		}
		ctx, cancel := context.WithTimeout(context.Background(), gitDeadline)
		err = s.exactRoot(ctx, resolved)
		cancel()
		if err != nil {
			return nil, memory.ErrProjectSourceUnavailable
		}
		s.projects[id] = resolved
	}
	return s, nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 32 || r == 127 {
			return true
		}
	}
	return false
}

func (s *gitSource) selected(projectID string) (string, string, error) {
	// A session credential does not grant access to project-wide Git documents.
	if s.scope.SessionID != "" {
		return "", "", memory.ErrProjectSourceUnavailable
	}
	if s.scope.ProjectID != "" {
		if projectID != "" && projectID != s.scope.ProjectID {
			return "", "", memory.ErrProjectSourceUnavailable
		}
		projectID = s.scope.ProjectID
	}
	root, ok := s.projects[projectID]
	if projectID == "" || !ok {
		return "", "", memory.ErrProjectSourceUnavailable
	}
	return projectID, root, nil
}

type limitedGitOutput struct {
	data     bytes.Buffer
	limit    int
	exceeded bool
}

var errGitOutputLimit = errors.New("Git output limit reached")

func (b *limitedGitOutput) Write(value []byte) (int, error) {
	remaining := b.limit - b.data.Len()
	if len(value) > remaining {
		_, _ = b.data.Write(value[:remaining])
		b.exceeded = true
		return remaining, errGitOutputLimit
	}
	return b.data.Write(value)
}

func gitFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return memory.ErrProjectSourceUnavailable
}

func (s *gitSource) command(ctx context.Context, root string, limit int, args ...string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	base := []string{"--no-pager", "--no-optional-locks", "--no-replace-objects", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never"}
	cmd := exec.CommandContext(ctx, s.git, append(base, args...)...)
	cmd.Dir = root
	// No inherited GIT_DIR, worktree, index, alternates, injected config, pager,
	// credential helper or executable search path may redirect a read. Repository
	// config stays available, while global/system config and lazy fetch are disabled.
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_LITERAL_PATHSPECS=1"}
	output := &limitedGitOutput{limit: limit}
	cmd.Stdout, cmd.Stderr = output, io.Discard
	cmd.WaitDelay = 500 * time.Millisecond
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if output.exceeded {
		return output.data.Bytes(), true, nil
	}
	if err != nil {
		return nil, false, memory.ErrProjectSourceUnavailable
	}
	return output.data.Bytes(), false, nil
}

func (s *gitSource) exactRoot(ctx context.Context, root string) error {
	raw, overflow, err := s.command(ctx, root, 16<<10, "rev-parse", "--show-toplevel")
	if err != nil || overflow {
		return gitFailure(ctx)
	}
	resolved, err := filepath.EvalSymlinks(strings.TrimSuffix(string(raw), "\n"))
	if err != nil || filepath.Clean(resolved) != filepath.Clean(root) {
		return memory.ErrProjectSourceUnavailable
	}
	return nil
}

func validGitHash(value string, lengths ...int) bool {
	validLength := false
	for _, size := range lengths {
		validLength = validLength || len(value) == size
	}
	if !validLength {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (s *gitSource) head(ctx context.Context, root string) (string, error) {
	if err := s.exactRoot(ctx, root); err != nil {
		return "", err
	}
	raw, overflow, err := s.command(ctx, root, 128, "rev-parse", "--verify", "HEAD^{commit}")
	commit := strings.TrimSuffix(string(raw), "\n")
	if err != nil || overflow || !validGitHash(commit, 40, 64) {
		return "", gitFailure(ctx)
	}
	return commit, nil
}

func allowedDocumentPath(name string) bool {
	if name == "" || !utf8.ValidString(name) || containsControl(name) || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || path.Clean(name) != name {
		return false
	}
	extension := strings.ToLower(path.Ext(name))
	if strings.HasPrefix(name, "docs/") {
		return extension == ".md" || extension == ".rst" || extension == ".txt"
	}
	if strings.Contains(name, "/") || extension != "" && extension != ".md" && extension != ".rst" && extension != ".txt" {
		return false
	}
	base := strings.ToLower(strings.TrimSuffix(name, path.Ext(name)))
	switch base {
	case "readme", "agents", "todo", "roadmap", "install", "installation", "architecture", "contributing", "changelog":
		return true
	}
	return false
}

type gitEntry struct {
	mode, kind, oid, path string
	size                  int64
}

func parseGitTree(raw []byte, incomplete bool) ([]gitEntry, error) {
	entries := []gitEntry{}
	for len(raw) > 0 {
		end := bytes.IndexByte(raw, 0)
		if end < 0 {
			if incomplete {
				break
			}
			return nil, memory.ErrProjectSourceUnavailable
		}
		row := raw[:end]
		raw = raw[end+1:]
		separator := bytes.IndexByte(row, '\t')
		if separator < 0 {
			return nil, memory.ErrProjectSourceUnavailable
		}
		fields := strings.Fields(string(row[:separator]))
		if len(fields) != 4 || !validGitHash(fields[2], 40, 64) {
			return nil, memory.ErrProjectSourceUnavailable
		}
		entry := gitEntry{mode: fields[0], kind: fields[1], oid: fields[2], path: string(row[separator+1:])}
		if entry.kind == "blob" {
			var err error
			entry.size, err = strconv.ParseInt(fields[3], 10, 64)
			if err != nil || entry.size < 0 {
				return nil, memory.ErrProjectSourceUnavailable
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func regularDocument(entry gitEntry) bool {
	return entry.kind == "blob" && (entry.mode == "100644" || entry.mode == "100755") && allowedDocumentPath(entry.path)
}

func (s *gitSource) blob(ctx context.Context, root string, entry gitEntry) (string, bool, error) {
	if entry.size > gitFileBytes {
		return "", true, nil
	}
	raw, overflow, err := s.command(ctx, root, gitFileBytes, "cat-file", "blob", entry.oid)
	if err != nil {
		return "", false, err
	}
	if overflow {
		return "", true, nil
	}
	if int64(len(raw)) != entry.size || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return "", false, memory.ErrProjectSourceUnavailable
	}
	return string(raw), false, nil
}

func documentHash(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

func (s *gitSource) snapshot(ctx context.Context, projectID string) (gitSnapshot, error) {
	var out gitSnapshot
	projectID, root, err := s.selected(projectID)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, gitDeadline)
	defer cancel()
	commit, err := s.head(ctx, root)
	if err != nil {
		return out, err
	}
	out = gitSnapshot{projectID: projectID, commit: commit, documents: []gitDocument{}, issues: []string{}}
	issue := func(code string) {
		out.truncated = true
		for _, present := range out.issues {
			if present == code {
				return
			}
		}
		out.issues = append(out.issues, code)
	}
	manifest, overflow, err := s.command(ctx, root, gitManifestBytes, "ls-tree", "-l", "-z", "--full-tree", commit)
	if err != nil {
		return gitSnapshot{}, err
	}
	if overflow {
		issue("git_manifest_limit")
	}
	entries, err := parseGitTree(manifest, overflow)
	if err != nil {
		return gitSnapshot{}, err
	}
	for _, entry := range entries {
		if entry.path == "docs" && entry.mode == "040000" && entry.kind == "tree" {
			remaining := gitManifestBytes - len(manifest)
			if remaining <= 0 {
				issue("git_manifest_limit")
				break
			}
			raw, overflow, err := s.command(ctx, root, remaining, "ls-tree", "-r", "-l", "-z", "--full-tree", commit, "--", "docs")
			if err != nil {
				return gitSnapshot{}, err
			}
			if overflow {
				issue("git_manifest_limit")
			}
			docs, err := parseGitTree(raw, overflow)
			if err != nil {
				return gitSnapshot{}, err
			}
			entries = append(entries, docs...)
			break
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	total := 0
	for _, entry := range entries {
		if !regularDocument(entry) {
			continue
		}
		if len(out.documents) >= gitDocumentLimit {
			issue("git_document_limit")
			continue
		}
		if entry.size > gitFileBytes {
			issue("git_file_size_limit")
			continue
		}
		if entry.size > int64(gitTotalBytes-total) {
			issue("git_total_size_limit")
			continue
		}
		text, overflow, err := s.blob(ctx, root, entry)
		if err != nil {
			return gitSnapshot{}, err
		}
		if overflow {
			issue("git_file_size_limit")
			continue
		}
		out.documents = append(out.documents, gitDocument{path: entry.path, text: text, hash: documentHash(text)})
		total += len(text)
	}
	current, err := s.head(ctx, root)
	if err != nil || current != commit {
		return gitSnapshot{}, gitFailure(ctx)
	}
	return out, nil
}

func (s *gitSource) verifySnapshot(ctx context.Context, projectID, commit string) error {
	_, root, err := s.selected(projectID)
	if err != nil || !validGitHash(commit, 40, 64) {
		return memory.ErrProjectSourceUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, gitDeadline)
	defer cancel()
	current, err := s.head(ctx, root)
	if err != nil {
		return err
	}
	if current != commit {
		return memory.ErrProjectSourceUnavailable
	}
	return nil
}

// excerptLines preserves original line endings. A final newline terminates the
// last real line rather than creating another empty line for a citation.
func excerptLines(text string, start, end int) (string, error) {
	if text == "" || !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 || start < 1 || end < start {
		return "", memory.ErrProjectSourceUnavailable
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if end > len(lines) {
		return "", memory.ErrProjectSourceUnavailable
	}
	return strings.Join(lines[start-1:end], ""), nil
}

func (s *gitSource) read(ctx context.Context, citation memory.ProjectGitCitation) (memory.ProjectGitEvidence, error) {
	var out memory.ProjectGitEvidence
	projectID, root, err := s.selected(citation.ProjectID)
	if err != nil || !allowedDocumentPath(citation.Path) || !validGitHash(citation.Commit, 40, 64) || !validGitHash(citation.SHA256, 64) || citation.StartLine < 1 || citation.EndLine < citation.StartLine {
		return out, memory.ErrProjectSourceUnavailable
	}
	citation.ProjectID = projectID
	ctx, cancel := context.WithTimeout(ctx, gitDeadline)
	defer cancel()
	commit, err := s.head(ctx, root)
	if err != nil {
		return out, err
	}
	if commit != citation.Commit {
		return out, memory.ErrProjectSourceUnavailable
	}
	raw, overflow, err := s.command(ctx, root, gitManifestBytes, "ls-tree", "-l", "-z", "--full-tree", commit, "--", citation.Path)
	if err != nil {
		return out, err
	}
	if overflow {
		return out, memory.ErrProjectSourceUnavailable
	}
	entries, err := parseGitTree(raw, false)
	if err != nil || len(entries) != 1 || entries[0].path != citation.Path || !regularDocument(entries[0]) {
		return out, memory.ErrProjectSourceUnavailable
	}
	text, overflow, err := s.blob(ctx, root, entries[0])
	if err != nil {
		return out, err
	}
	if overflow || documentHash(text) != citation.SHA256 {
		return out, memory.ErrProjectSourceUnavailable
	}
	excerpt, err := excerptLines(text, citation.StartLine, citation.EndLine)
	if err != nil {
		return out, err
	}
	current, err := s.head(ctx, root)
	if err != nil || current != commit {
		return out, gitFailure(ctx)
	}
	return memory.ProjectGitEvidence{Citation: citation, Text: excerpt}, nil
}
