package projectcontext

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/memory"
)

func fixtureGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is unavailable")
	}
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "user.name=Context Fixture", "-c", "user.email=context@example.invalid", "-c", "commit.gpgSign=false"}
	cmd := exec.Command(git, append(base, args...)...)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C"}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git failed: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

func writeGitFile(t *testing.T, root, name, text string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func commitGitFixture(t *testing.T, root string) string {
	t.Helper()
	fixtureGit(t, root, "add", "-A")
	fixtureGit(t, root, "commit", "--quiet", "-m", "Synthetic committed project documents")
	return fixtureGit(t, root, "rev-parse", "HEAD")
}

func gitFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	fixtureGit(t, root, "init", "--quiet")
	for name, text := range files {
		writeGitFile(t, root, name, text)
	}
	commitGitFixture(t, root)
	return root
}

func ownerGitSource(t *testing.T, root string) *gitSource {
	t.Helper()
	source, err := newGitSource(map[string]string{"project-a": root}, memory.Scope{AllowAll: true})
	if err != nil {
		t.Fatal("registered fixture unavailable", err)
	}
	return source
}

func citationFor(snapshot gitSnapshot, document gitDocument, start, end int) memory.ProjectGitCitation {
	return memory.ProjectGitCitation{ProjectID: snapshot.projectID, Commit: snapshot.commit, Path: document.path, SHA256: document.hash, StartLine: start, EndLine: end}
}

func TestGitSnapshotReadsCommittedAllowedDocumentsAndLeavesDirtyState(t *testing.T) {
	want := map[string]string{
		"README.md": "# Overview\nCommitted project overview.\n", "AGENTS.md": "Reviewed instructions.\n",
		"TODO.md": "Pending synthetic followup.\n", "roadmap.rst": "Planned work.\n", "INSTALL": "Installation instructions.\n",
		"Architecture.TXT": "Component boundaries.\n", "docs/design.md": "Committed architecture.\n", "docs/nested/setup.TXT": "Supported nested document.\n",
	}
	files := make(map[string]string, len(want)+4)
	for name, text := range want {
		files[name] = text
	}
	files[".env"] = "PRIVATE_FIXTURE_SECRET=not-project-context\n"
	files["main.go"] = "package private\n"
	files["private.txt"] = "Not an allowed root basename.\n"
	files["docs/evaluation.json"] = `{"private_label":"must-not-be-read"}`
	root := gitFixture(t, files)
	commit := fixtureGit(t, root, "rev-parse", "HEAD")
	writeGitFile(t, root, "README.md", "Dirty uncommitted replacement must not appear.\n")
	fixtureGit(t, root, "add", "README.md")
	writeGitFile(t, root, "docs/untracked.md", "Untracked content must not appear.\n")
	before := fixtureGit(t, root, "status", "--porcelain=v1", "-z")
	source := ownerGitSource(t, root)
	snapshot, err := source.snapshot(context.Background(), "project-a")
	if err != nil || snapshot.commit != commit || snapshot.projectID != "project-a" || snapshot.truncated || len(snapshot.issues) != 0 {
		t.Fatal("committed snapshot failed", err)
	}
	if len(snapshot.documents) != len(want) {
		t.Fatalf("allowed document count %d, want %d", len(snapshot.documents), len(want))
	}
	for _, document := range snapshot.documents {
		text, ok := want[document.path]
		if !ok || document.text != text || document.hash != documentHash(text) {
			t.Fatal("snapshot included unregistered, dirty or unbound content")
		}
	}
	if fixtureGit(t, root, "status", "--porcelain=v1", "-z") != before {
		t.Fatal("Git reader changed the index or working tree")
	}
}

func TestGitAuthorizationAndExactRegisteredRoots(t *testing.T) {
	rootA := gitFixture(t, map[string]string{"README.md": "Project A.\n"})
	rootB := gitFixture(t, map[string]string{"README.md": "Project B.\n"})
	projects := map[string]string{"project-a": rootA, "project-b": rootB}
	owner, err := newGitSource(projects, memory.Scope{AllowAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.snapshot(context.Background(), "project-b"); err != nil {
		t.Fatal("owner could not select a registered project", err)
	}
	for _, id := range []string{"", "unregistered-project"} {
		if _, err := owner.snapshot(context.Background(), id); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
			t.Fatal("owner accessed an unselected or unregistered project")
		}
	}
	project, err := newGitSource(projects, memory.Scope{ProjectID: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	projects["project-a"] = rootB
	snapshot, err := project.snapshot(context.Background(), "")
	if err != nil || snapshot.projectID != "project-a" || snapshot.documents[0].text != "Project A.\n" {
		t.Fatal("credential project default or registration isolation failed", err)
	}
	if _, err := project.snapshot(context.Background(), "project-b"); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
		t.Fatal("project credential selected another project")
	}
	for _, scope := range []memory.Scope{{SessionID: "session-a"}, {ProjectID: "project-a", SessionID: "session-a"}} {
		session, err := newGitSource(projects, scope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.snapshot(context.Background(), "project-a"); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
			t.Fatal("session credential accessed project-level documents")
		}
		if _, err := session.read(context.Background(), citationFor(snapshot, snapshot.documents[0], 1, 1)); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
			t.Fatal("session credential read a project-level citation")
		}
	}
	for _, scope := range []memory.Scope{{}, {AllowAll: true, ProjectID: "project-a"}, {AllowAll: true, SessionID: "session-a"}} {
		if _, err := newGitSource(nil, scope); err == nil {
			t.Fatal("ambiguous credential scope was accepted")
		}
	}
	nested := filepath.Join(rootA, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{nested, t.TempDir(), "relative-root"} {
		_, err := newGitSource(map[string]string{"project-a": root}, memory.Scope{AllowAll: true})
		if err == nil || strings.Contains(err.Error(), root) {
			t.Fatal("non-root registration was accepted or leaked a local path")
		}
	}
}

func TestGitReaderIgnoresInheritedRepositoryAndConfigurationOverrides(t *testing.T) {
	root := gitFixture(t, map[string]string{"README.md": "Authorized repository.\n"})
	other := gitFixture(t, map[string]string{"README.md": "Unauthorized repository.\n"})
	for key, value := range map[string]string{
		"GIT_DIR": filepath.Join(other, ".git"), "GIT_WORK_TREE": other, "GIT_COMMON_DIR": filepath.Join(other, ".git"),
		"GIT_OBJECT_DIRECTORY": filepath.Join(other, ".git/objects"), "GIT_INDEX_FILE": filepath.Join(other, ".git/index"),
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.bare", "GIT_CONFIG_VALUE_0": "true",
		"GIT_CONFIG_PARAMETERS": "'core.bare'='true'", "GIT_NAMESPACE": "untrusted-namespace",
	} {
		t.Setenv(key, value)
	}
	source := ownerGitSource(t, root)
	snapshot, err := source.snapshot(context.Background(), "project-a")
	if err != nil || len(snapshot.documents) != 1 || snapshot.documents[0].text != "Authorized repository.\n" {
		t.Fatal("inherited Git environment redirected the read", err)
	}
}

func TestGitReaderSupportsAnExactLinkedWorktreeRoot(t *testing.T) {
	root := gitFixture(t, map[string]string{"README.md": "Committed linked worktree document.\n"})
	linked := filepath.Join(t.TempDir(), "linked")
	fixtureGit(t, root, "worktree", "add", "--quiet", "--detach", linked)
	snapshot, err := ownerGitSource(t, linked).snapshot(context.Background(), "project-a")
	if err != nil || len(snapshot.documents) != 1 || snapshot.documents[0].text != "Committed linked worktree document.\n" {
		t.Fatal("registered linked worktree was not readable", err)
	}
}

func TestGitSnapshotDoesNotApplyLocalReplacementRefs(t *testing.T) {
	root := gitFixture(t, map[string]string{"README.md": "Original committed document.\n"})
	original := fixtureGit(t, root, "rev-parse", "HEAD")
	writeGitFile(t, root, "README.md", "Replacement-ref content must not impersonate the original commit.\n")
	replacement := commitGitFixture(t, root)
	fixtureGit(t, root, "update-ref", "HEAD", original)
	fixtureGit(t, root, "replace", original, replacement)
	snapshot, err := ownerGitSource(t, root).snapshot(context.Background(), "project-a")
	if err != nil || snapshot.commit != original || len(snapshot.documents) != 1 || snapshot.documents[0].text != "Original committed document.\n" {
		t.Fatal("replacement refs changed the meaning of a commit citation", err)
	}
}

func TestGitReaderExcludesSymlinksSubmodulesAndForbiddenPaths(t *testing.T) {
	root := gitFixture(t, map[string]string{"AGENTS.md": "Allowed committed text.\n", "docs/design.md": "Allowed design.\n"})
	outside := t.TempDir()
	writeGitFile(t, outside, "private.md", "Synthetic external private content.\n")
	if err := os.Symlink(filepath.Join(outside, "private.md"), filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "private.md"), filepath.Join(root, "docs/link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "docs/linked-directory")); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, root, "add", "-A")
	commit := fixtureGit(t, root, "rev-parse", "HEAD")
	fixtureGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+commit+",docs/submodule")
	fixtureGit(t, root, "commit", "--quiet", "-m", "Synthetic excluded object modes")
	source := ownerGitSource(t, root)
	snapshot, err := source.snapshot(context.Background(), "project-a")
	if err != nil || len(snapshot.documents) != 2 {
		t.Fatal("nonregular objects entered project context", err)
	}
	for _, name := range []string{"README.md", "docs/link.md", "docs/linked-directory/private.md", "docs/submodule/README.md", ".env", "main.go", "docs/../AGENTS.md", "/README.md", "docs/design.md\x00", "docs\\design.md", "docs/./design.md", "docs/private.json"} {
		citation := citationFor(snapshot, snapshot.documents[0], 1, 1)
		citation.Path = name
		if _, err := source.read(context.Background(), citation); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
			t.Fatal("nonregular or forbidden citation was accepted")
		}
	}
}

func TestGitSourceBudgetsAreExplicitAndNeverReturnPartialFileText(t *testing.T) {
	for _, kind := range []string{"file", "total", "documents", "manifest"} {
		t.Run(kind, func(t *testing.T) {
			files := map[string]string{}
			issue := ""
			switch kind {
			case "file":
				files["README.md"] = "Retained complete file.\n"
				files["docs/large.md"] = strings.Repeat("x", gitFileBytes+1)
				issue = "git_file_size_limit"
			case "total":
				for i := 0; i < 6; i++ {
					files[fmt.Sprintf("docs/file-%02d.md", i)] = strings.Repeat("x", 100<<10)
				}
				issue = "git_total_size_limit"
			case "documents":
				for i := 0; i < gitDocumentLimit+1; i++ {
					files[fmt.Sprintf("docs/file-%03d.md", i)] = "Bounded complete document.\n"
				}
				issue = "git_document_limit"
			case "manifest":
				for i := 0; i < 1400; i++ {
					files[fmt.Sprintf("docs/%s-%04d.md", strings.Repeat("x", 160), i)] = "Bounded document.\n"
				}
				issue = "git_manifest_limit"
			}
			root := gitFixture(t, files)
			snapshot, err := ownerGitSource(t, root).snapshot(context.Background(), "project-a")
			if err != nil || !snapshot.truncated || !containsIssue(snapshot.issues, issue) {
				t.Fatal("snapshot budget omission was not disclosed", err)
			}
			if len(snapshot.documents) == 0 || len(snapshot.documents) > gitDocumentLimit {
				t.Fatal("snapshot document budget was violated")
			}
			total := 0
			for _, document := range snapshot.documents {
				if len(document.text) > gitFileBytes || document.text != files[document.path] {
					t.Fatal("snapshot returned oversized or truncated file text")
				}
				total += len(document.text)
			}
			if total > gitTotalBytes {
				t.Fatal("snapshot total budget was violated")
			}
			if kind == "file" {
				citation := citationFor(snapshot, snapshot.documents[0], 1, 1)
				citation.Path = "docs/large.md"
				citation.SHA256 = documentHash(files[citation.Path])
				if _, err := ownerGitSource(t, root).read(context.Background(), citation); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
					t.Fatal("direct citation bypassed the per-file byte limit")
				}
			}
		})
	}
}

func containsIssue(issues []string, want string) bool {
	for _, issue := range issues {
		if issue == want {
			return true
		}
	}
	return false
}

func TestGitCitationReconstructsExactLinesAndRejectsStaleOrForgedEvidence(t *testing.T) {
	text := "# 项目\r\nLine two.\r\n第三行\nLast line without newline."
	root := gitFixture(t, map[string]string{"README.md": text})
	source := ownerGitSource(t, root)
	snapshot, err := source.snapshot(context.Background(), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	citation := citationFor(snapshot, snapshot.documents[0], 2, 3)
	evidence, err := source.read(context.Background(), citation)
	if err != nil || evidence.Citation != citation || evidence.Text != "Line two.\r\n第三行\n" {
		t.Fatal("citation did not reconstruct exact original lines", err)
	}
	for _, edit := range []func(*memory.ProjectGitCitation){
		func(c *memory.ProjectGitCitation) { c.ProjectID = "project-b" },
		func(c *memory.ProjectGitCitation) { c.Commit = strings.Repeat("0", 40) },
		func(c *memory.ProjectGitCitation) { c.Commit = "HEAD" },
		func(c *memory.ProjectGitCitation) { c.SHA256 = strings.Repeat("0", 64) },
		func(c *memory.ProjectGitCitation) { c.SHA256 = "invalid" },
		func(c *memory.ProjectGitCitation) { c.StartLine = 0 },
		func(c *memory.ProjectGitCitation) { c.EndLine = 1 },
		func(c *memory.ProjectGitCitation) { c.EndLine = 5 },
	} {
		forged := citation
		edit(&forged)
		if _, err := source.read(context.Background(), forged); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
			t.Fatal("forged or out-of-range citation was accepted")
		}
	}
	writeGitFile(t, root, "TODO.md", "Another committed change.\n")
	commitGitFixture(t, root)
	if _, err := source.read(context.Background(), citation); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
		t.Fatal("citation survived a HEAD change despite an unchanged source file")
	}
	if err := source.verifySnapshot(context.Background(), "project-a", snapshot.commit); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
		t.Fatal("late snapshot verification accepted a changed HEAD")
	}
}

func TestGitSnapshotAndReadRejectHeadChangesDuringDocumentLoading(t *testing.T) {
	root := gitFixture(t, map[string]string{"README.md": "Original committed document.\n"})
	source := ownerGitSource(t, root)
	snapshot, err := source.snapshot(context.Background(), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	writeGitFile(t, root, "TODO.md", "A concurrent committed document.\n")
	next := commitGitFixture(t, root)
	fixtureGit(t, root, "update-ref", "HEAD", snapshot.commit)
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	wrapper := filepath.Join(t.TempDir(), "bounded-git-wrapper")
	script := "#!/bin/sh\nfor argument do\n  if [ \"$argument\" = cat-file ]; then\n    " + quote(source.git) + " update-ref HEAD " + next + " || exit 1\n  fi\ndone\nexec " + quote(source.git) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	source.git = wrapper
	if _, err := source.snapshot(context.Background(), "project-a"); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
		t.Fatal("snapshot returned references invalidated while loading blobs")
	}
	fixtureGit(t, root, "update-ref", "HEAD", snapshot.commit)
	if _, err := source.read(context.Background(), citationFor(snapshot, snapshot.documents[0], 1, 1)); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
		t.Fatal("source read returned evidence invalidated while loading its blob")
	}
}

func TestGitReadHonorsCancellationAndOutputCaps(t *testing.T) {
	root := gitFixture(t, map[string]string{"README.md": strings.Repeat("x", 1024)})
	source := ownerGitSource(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.snapshot(ctx, "project-a"); !errors.Is(err, context.Canceled) {
		t.Fatal("snapshot ignored caller cancellation")
	}
	commit := fixtureGit(t, root, "rev-parse", "HEAD")
	if err := source.verifySnapshot(ctx, "project-a", commit); !errors.Is(err, context.Canceled) {
		t.Fatal("snapshot verification ignored caller cancellation")
	}
	oid := fixtureGit(t, root, "rev-parse", "HEAD:README.md")
	raw, overflow, err := source.command(context.Background(), root, 128, "cat-file", "blob", oid)
	if err != nil || !overflow || len(raw) != 128 {
		t.Fatal("Git subprocess output cap did not bound memory", err)
	}
	limited := &limitedGitOutput{limit: 128}
	if _, err := io.Copy(limited, strings.NewReader(strings.Repeat("x", 4096))); err == nil || !limited.exceeded || limited.data.Len() != 128 {
		t.Fatal("io.Copy bypassed the Git output limit")
	}
}

func TestExcerptLinesPreservesEndingsWithoutInventingTrailingLines(t *testing.T) {
	for _, test := range []struct {
		text       string
		start, end int
		want       string
	}{
		{"one\ntwo\n", 1, 1, "one\n"}, {"one\ntwo\n", 2, 2, "two\n"},
		{"one\r\ntwo", 1, 2, "one\r\ntwo"}, {"\n", 1, 1, "\n"}, {"中文", 1, 1, "中文"},
	} {
		got, err := excerptLines(test.text, test.start, test.end)
		if err != nil || got != test.want {
			t.Fatal("excerpt line range changed original bytes", err)
		}
	}
	for _, test := range []struct {
		text       string
		start, end int
	}{{"", 1, 1}, {"one\n", 2, 2}, {"one\n", 0, 1}, {"one\n", 2, 1}, {"one\n", 1, 3}, {"a\x00b", 1, 1}, {string([]byte{255}), 1, 1}} {
		if _, err := excerptLines(test.text, test.start, test.end); !errors.Is(err, memory.ErrProjectSourceUnavailable) {
			t.Fatal("invalid citation line range was accepted")
		}
	}
}
