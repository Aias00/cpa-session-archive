package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
)

func testCredential(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "management.token")
	if err := os.WriteFile(path, []byte("synthetic-management-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListUsesAuthenticatedProxyAndFacetQuery(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != managementPrefix+"/session-browser" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-management-token" {
			t.Errorf("wrong API route or missing authentication")
		}
		query := r.URL.Query()
		for name, value := range map[string]string{"q": "路径 100%", "tag": "work", "archived": "all", "starred": "true", "project.name": "repo=x", "client": "codex", "limit": "2", "offset": "4"} {
			if query.Get(name) != value {
				t.Errorf("query %s=%q want=%q", name, query.Get(name), value)
			}
		}
		_ = json.NewEncoder(w).Encode(archive.ManagedSessionPage{Sessions: []archive.ManagedSession{}, Total: 8, Limit: 2, Offset: 4})
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"list", "--endpoint", server.URL, "--token-file", testCredential(t), "--q", "路径 100%", "--tag", "work", "--archived", "all", "--starred", "true", "--filter", "project.name=repo=x", "--filter", "client=codex", "--limit", "2", "--offset", "4"}, &stdout, &stderr)
	if err != nil || requests != 1 || !strings.Contains(stdout.String(), `"total":8`) || strings.Contains(stdout.String()+stderr.String(), "synthetic-management-token") {
		t.Fatalf("list failed requests=%d err=%v stdout=%s stderr=%s", requests, err, stdout.String(), stderr.String())
	}
}

func TestEditPreservesUnspecifiedFieldsAndRequiresReviewedRevision(t *testing.T) {
	metadata := archive.SessionManagement{SessionID: "with/slash'quote", Title: "Original", Note: "Retain this note", Tags: []string{"existing"}, Starred: true, Revision: 4}
	gets, puts := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != managementPrefix+"/session-metadata" || r.URL.Query().Get("session_id") != metadata.SessionID || r.Header.Get("Authorization") != "Bearer synthetic-management-token" {
			t.Errorf("wrong metadata route, identity or authorization")
		}
		if r.Method == "GET" {
			gets++
			_ = json.NewEncoder(w).Encode(metadata)
			return
		}
		puts++
		var update struct {
			ExpectedRevision int64 `json:"expected_revision"`
			archive.SessionManagementUpdate
		}
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			t.Error(err)
		}
		if update.ExpectedRevision != 4 || update.Title != "新标题" || update.Note != metadata.Note || !update.Starred || !update.Archived || len(update.Tags) != 1 || update.Tags[0] != "existing" {
			t.Errorf("update did not preserve unedited fields: %+v", update)
		}
		metadata.Title, metadata.Archived, metadata.Revision = update.Title, true, 5
		_ = json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()
	base := []string{"edit", "--endpoint", server.URL, "--token-file", testCredential(t), "--session-id", metadata.SessionID, "--expected-revision", "4", "--title", "新标题", "--archived", "true"}
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), base, &stdout, &stderr); err != nil || puts != 1 || gets != 1 {
		t.Fatalf("edit failed err=%v gets=%d puts=%d", err, gets, puts)
	}
	if err := run(context.Background(), base, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "metadata changed") || puts != 1 || gets != 2 {
		t.Fatalf("stale edit did not stop before PUT: err=%v gets=%d puts=%d", err, gets, puts)
	}
}

func TestCLIConflictDoesNotRetryOrLeakServerBody(t *testing.T) {
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = fmt.Fprint(w, `{"session_id":"s","revision":0,"tags":[]}`)
			return
		}
		puts++
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, "private remote diagnostics")
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"edit", "--endpoint", server.URL, "--token-file", testCredential(t), "--session-id", "s", "--expected-revision", "0", "--title", "Changed"}, &stdout, &stderr)
	if err == nil || puts != 1 || !strings.Contains(err.Error(), "metadata changed") || strings.Contains(err.Error()+stdout.String()+stderr.String(), "private remote diagnostics") {
		t.Fatalf("conflict handling err=%v puts=%d", err, puts)
	}
}

func TestCLIRejectsUnsafeConfigurationAndAmbiguousEdits(t *testing.T) {
	for _, args := range [][]string{
		{}, {"other"}, {"list", "--endpoint", "http://example.com", "--token-file", "missing"},
		{"list", "--endpoint", "http://127.0.0.1/path", "--token-file", "missing"},
		{"list", "--limit", "0"}, {"list", "--offset", "-1"}, {"list", "--filter", "q=wrong"},
		{"list", "--filter", "client=a", "--filter", "client=b"},
		{"edit", "--session-id", "s", "--title", "No revision"},
		{"edit", "--session-id", "s", "--expected-revision", "0"},
		{"edit", "--session-id", "s", "--expected-revision", "0", "--archived", "yes"},
		{"edit", "--session-id", "s", "--expected-revision", "0", "--clear-tags", "--tag", "x"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(context.Background(), args, &stdout, &stderr); err == nil {
				t.Fatalf("accepted invalid arguments %v", args)
			}
		})
	}
	token := testCredential(t)
	if err := os.Chmod(token, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newClient("http://127.0.0.1:12345", token); err == nil {
		t.Fatal("accepted broad credential permissions")
	}
}

func TestCLINeverFollowsManagementRedirects(t *testing.T) {
	leaks := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaks++ }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"list", "--endpoint", origin.URL, "--token-file", testCredential(t)}, &stdout, &stderr)
	if err == nil || leaks != 0 || !strings.Contains(err.Error(), "307") {
		t.Fatalf("redirect was followed err=%v leaks=%d", err, leaks)
	}
}
