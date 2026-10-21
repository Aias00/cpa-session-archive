package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestBulkPreviewCLIPreservesFullSelectionAndExplicitAction(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var plan archive.SessionBulkPlanRequest
		if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.URL.Path != managementPrefix+"/session-bulk-preview" || len(r.URL.Query()) != 0 || r.Header.Get("Authorization") != "Bearer synthetic-management-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong preview boundary")
		}
		if plan.Selection.View != "inactive" || plan.Selection.InactiveDays != 60 || plan.Selection.Query != "repo 100%" || plan.Selection.Tag != "old" || plan.Selection.Archived != "all" || !plan.Selection.Starred || plan.Selection.Filters["project.name"] != "scope=a" || plan.Selection.Limit != 0 || plan.Selection.Offset != 0 {
			t.Errorf("wrong complete preview selection: %+v", plan.Selection)
		}
		if plan.Action.Archived == nil || !*plan.Action.Archived || plan.Action.Starred == nil || *plan.Action.Starred || !plan.Action.IncludeStarred || !plan.Action.IncludeEdited || len(plan.Action.AddTags) != 2 || len(plan.Action.RemoveTags) != 1 {
			t.Errorf("wrong explicit actions: %+v", plan.Action)
		}
		_, _ = io.WriteString(w, `{"id":"bulk-test","status":"preview","total":123}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"bulk-preview", "--endpoint", server.URL, "--token-file", testCredential(t), "--view", "inactive", "--inactive-days", "60", "--q", "repo 100%", "--tag", "old", "--archived", "all", "--starred", "true", "--filter", "project.name=scope=a", "--archive", "true", "--star", "false", "--add-tag", "one", "--add-tag", "two", "--remove-tag", "old", "--include-starred", "--include-edited"}, &stdout, &stderr)
	if err != nil || calls != 1 || !strings.Contains(stdout.String(), `"total":123`) || strings.Contains(stdout.String()+stderr.String(), "synthetic-management-token") {
		t.Fatalf("preview failed calls=%d err=%v stdout=%s", calls, err, stdout.String())
	}
}

func TestBulkCLIShowListAndSingleStep(t *testing.T) {
	for _, command := range []string{"bulk-show", "bulk-apply", "bulk-undo"} {
		for _, withID := range []bool{false, true} {
			if command != "bulk-show" && !withID {
				continue
			}
			t.Run(command+strings.Repeat("-id", map[bool]int{true: 1}[withID]), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if command == "bulk-show" {
						if r.Method != "GET" || r.URL.Path != managementPrefix+"/session-bulk" || (withID && r.URL.Query().Get("id") != "bulk-test") {
							t.Error("wrong show route")
						}
						if !withID {
							_, _ = io.WriteString(w, `{"jobs":[]}`)
							return
						}
					} else {
						var body struct {
							ID    string `json:"id"`
							Limit int    `json:"limit"`
						}
						if json.NewDecoder(r.Body).Decode(&body) != nil || body.ID != "bulk-test" || body.Limit != 2 || r.Method != "POST" || r.URL.Path != managementPrefix+"/session-"+command {
							t.Error("wrong bounded mutation")
						}
					}
					_, _ = io.WriteString(w, `{"id":"bulk-test","status":"running","pending":500}`)
				}))
				defer server.Close()
				args := []string{command, "--endpoint", server.URL, "--token-file", testCredential(t)}
				if withID {
					args = append(args, "--job-id", "bulk-test")
				}
				if command != "bulk-show" {
					args = append(args, "--limit", "2")
				}
				var stdout, stderr bytes.Buffer
				if err := run(context.Background(), args, &stdout, &stderr); err != nil || calls != 1 {
					t.Fatalf("single request failed calls=%d err=%v", calls, err)
				}
			})
		}
	}
}

func TestBulkCLIDoesNotRetryMutationOrExposeRemoteErrors(t *testing.T) {
	for _, command := range []string{"bulk-apply", "bulk-undo"} {
		t.Run(command, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(409)
				_, _ = io.WriteString(w, "private remote diagnostics")
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), []string{command, "--endpoint", server.URL, "--token-file", testCredential(t), "--job-id", "bulk-test"}, &stdout, &stderr)
			if err == nil || calls != 1 || !strings.Contains(err.Error(), "bulk-show") || strings.Contains(err.Error()+stdout.String()+stderr.String(), "private remote diagnostics") {
				t.Fatalf("unsafe mutation error calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestBulkCLIRejectsAmbiguousScopeAndDocumentsResumption(t *testing.T) {
	for _, args := range [][]string{
		{"bulk-preview"}, {"bulk-preview", "--archive", "yes"}, {"bulk-preview", "--star", ""},
		{"bulk-preview", "--archive", "true", "--filter", "view=main"},
		{"bulk-preview", "--archive", "true", "--filter", "inactive_days=60"},
		{"bulk-preview", "--archive", "true", "--filter", "client=a", "--filter", "client=b"},
		{"bulk-preview", "--archive", "true", "--view", "unknown"},
		{"bulk-preview", "--archive", "true", "--inactive-days", "0"},
		{"bulk-preview", "--archive", "true", "--add-tag", "bad\ntag"},
		{"bulk-apply"}, {"bulk-undo", "--job-id", "bulk-test", "--limit", "101"},
		{"bulk-show", "--job-id", ""}, {"bulk-apply", "--job-id", "bad\nid"},
		{"list", "--filter", "view=main"}, {"list", "--inactive-days", "3651"}, {"list", "--view", "unknown"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), args, &stdout, &stderr); err == nil {
			t.Fatalf("accepted invalid arguments %q", args)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"bulk-apply", "--help"}, &stdout, &stderr); err != nil || !strings.Contains(stderr.String(), "one step") || !strings.Contains(stderr.String(), "bulk-show") {
		t.Fatalf("missing bounded/resume help: err=%v help=%s", err, stderr.String())
	}
}

func TestBulkCLIUntilDoneContinuesOnlyConfirmedSteps(t *testing.T) {
	for _, command := range []string{"bulk-apply", "bulk-undo"} {
		t.Run(command, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var step struct {
					ID    string `json:"id"`
					Limit int    `json:"limit"`
				}
				if json.NewDecoder(r.Body).Decode(&step) != nil || step.ID != "bulk-test" || step.Limit != 100 || calls > 3 {
					t.Error("unexpected step request")
				}
				job := archive.SessionBulkJob{ID: "bulk-test", Status: "running", Total: 250, Pending: max(0, 250-calls*100), Applied: min(250, calls*100)}
				if calls == 3 {
					job.Status = "complete"
				}
				if command == "bulk-undo" {
					job.Status, job.Pending, job.Applied, job.Undone = "undoing", 0, max(0, 250-calls*100), min(250, calls*100)
					if calls == 3 {
						job.Status = "undone"
					}
				}
				_ = json.NewEncoder(w).Encode(job)
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), []string{command, "--endpoint", server.URL, "--token-file", testCredential(t), "--job-id", "bulk-test", "--until-done"}, &stdout, &stderr)
			if err != nil || calls != 3 || strings.Count(stdout.String(), "\n") != 1 {
				t.Fatalf("confirmed loop failed calls=%d err=%v stdout=%s", calls, err, stdout.String())
			}
		})
	}
}

func TestBulkCLIUntilDoneStopsOnAmbiguousResponseAndNoProgress(t *testing.T) {
	for _, failure := range []string{"disconnect", "conflict", "invalid", "no-progress", "wrong-id"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					_, _ = io.WriteString(w, `{"id":"bulk-test","status":"running","pending":150,"applied":100}`)
					return
				}
				switch failure {
				case "disconnect":
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
				case "conflict":
					w.WriteHeader(409)
					_, _ = io.WriteString(w, "private remote diagnostics")
				case "invalid":
					_, _ = io.WriteString(w, `{"broken"`)
				case "no-progress":
					_, _ = io.WriteString(w, `{"id":"bulk-test","status":"running","pending":150,"applied":100}`)
				case "wrong-id":
					_, _ = io.WriteString(w, `{"id":"bulk-other","status":"running","pending":50,"applied":200}`)
				}
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), []string{"bulk-apply", "--endpoint", server.URL, "--token-file", testCredential(t), "--job-id", "bulk-test", "--until-done"}, &stdout, &stderr)
			if err == nil || calls.Load() != 2 || !strings.Contains(err.Error(), "bulk-show") || strings.Contains(err.Error()+stdout.String()+stderr.String(), "private remote diagnostics") || stdout.Len() != 0 {
				t.Fatalf("loop failed to stop: calls=%d err=%v stdout=%s", calls.Load(), err, stdout.String())
			}
		})
	}
}
