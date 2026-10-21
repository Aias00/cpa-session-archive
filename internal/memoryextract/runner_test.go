package memoryextract

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/knowledge"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/storage"
)

type runnerIdentityFeed struct {
	storage.MemoryFeed
	id string
}

func (f runnerIdentityFeed) MemoryIdentity(ctx context.Context) (storage.FeedIdentity, error) {
	w, e := f.MemoryWatermark(ctx)
	return storage.FeedIdentity{ArchiveID: f.id, Watermark: w, HistoryComplete: true}, e
}

type runnerProvider struct {
	calls   int
	action  string
	failure error
}

func (*runnerProvider) Fingerprint() string { return "synthetic-provider-v1" }
func (*runnerProvider) EstimateInputTokens(r Request) (int, error) {
	raw, err := json.Marshal(r)
	return len(raw) + 1024, err
}
func (p *runnerProvider) Extract(_ context.Context, r Request) (Response, error) {
	p.calls++
	if p.failure != nil {
		return Response{}, p.failure
	}
	e := r.Input.Excerpts[0]
	action := p.action
	if action == "" {
		action = "new"
	}
	raw := DraftCandidate{Kind: "decision", Title: "Synthetic sourced policy", Statement: e.Text, Attribution: "user_requirement", Supports: []Support{{ExcerptID: e.ID, Quote: e.Text}}, Action: action}
	if action != "new" {
		if len(r.Input.Existing) == 0 {
			return Response{}, errors.New("missing synthetic target")
		}
		raw.Targets = []Target{{ID: r.Input.Existing[0].ID, Revision: r.Input.Existing[0].Revision}}
	}
	return Response{Candidates: []DraftCandidate{raw}, Usage: Usage{InputTokens: 100, OutputTokens: 100}}, nil
}

func runnerGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatal("synthetic Git operation failed", err, string(out))
	}
	return strings.TrimSpace(string(out))
}
func runnerRecord(id, text string) archive.Record {
	input, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": text}}})
	return archive.Record{RequestID: id, SessionID: "synthetic-session", Facets: map[string][]string{"project.id": {"synthetic-project"}}, OriginalRequest: input, Response: []byte(`{"choices":[{"message":{"role":"assistant","content":"Suggestion only: add monitoring later."}}]}`)}
}

func runnerFixture(t *testing.T) (*Runner, *archive.Store, *runnerProvider, *knowledge.Store, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := archive.OpenStore(filepath.Join(dir, "raw.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.DB.Close() })
	if err := raw.PutBatch([]archive.Record{runnerRecord("first-source", "The synthetic project must use a bounded queue with durable retries.")}); err != nil {
		t.Fatal(err)
	}
	id, err := raw.EnsureFeedIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	feed := runnerIdentityFeed{MemoryFeed: storage.NewSQLite(raw), id: id}
	selection := Selection{ProjectID: "synthetic-project", SessionID: "synthetic-session"}
	source, err := NewSourceReader(feed, id, selection)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := OpenJobs(filepath.Join(dir, "jobs.sqlite"), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.Close() })
	root := filepath.Join(dir, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	runnerGit(t, root, "init", "-q")
	runnerGit(t, root, "config", "user.name", "Synthetic extraction")
	runnerGit(t, root, "config", "user.email", "fixture@example.invalid")
	runnerGit(t, root, "config", "commit.gpgsign", "false")
	runnerGit(t, root, "config", "core.hooksPath", "/dev/null")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Synthetic project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runnerGit(t, root, "add", "README.md")
	runnerGit(t, root, "commit", "-qm", "fixture")
	library, err := knowledge.New(knowledge.Config{Projects: map[string]string{selection.ProjectID: root}, Scope: memory.Scope{ProjectID: selection.ProjectID}, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	provider := &runnerProvider{}
	return &Runner{Jobs: jobs, Feed: feed, Source: source, Provider: provider, Knowledge: library, Selection: selection, Limits: DefaultLimits()}, raw, provider, library, root
}

func TestRunnerDryRunStageHumanReviewAndExplicitCorrection(t *testing.T) {
	r, raw, p, kb, root := runnerFixture(t)
	ctx := context.Background()
	head := runnerGit(t, root, "rev-parse", "HEAD")
	page, registered, err := r.Discover(ctx, 8)
	if err != nil || len(registered) != 1 || !page.CaughtUp || !page.Complete {
		t.Fatal("initial discovery failed", err, page.Issues)
	}
	reports, err := r.Process(ctx, 1)
	if err != nil || len(reports) != 1 || len(reports[0].Candidates) != 1 || p.calls != 1 {
		t.Fatal("candidate extraction failed", err)
	}
	if runnerGit(t, root, "rev-parse", "HEAD") != head {
		t.Fatal("dry run wrote canonical Git")
	}
	current, err := kb.Memories(ctx, memory.KnowledgeQuery{ProjectID: r.Selection.ProjectID})
	if err != nil || len(current.Entries) != 0 {
		t.Fatal("unreviewed extraction became active", err)
	}
	jobID := reports[0].JobID
	receipts, err := r.Stage(ctx, jobID)
	if err != nil || len(receipts) != 1 || receipts[0].Status != "draft" {
		t.Fatal("explicit stage failed", err)
	}
	draftHead := runnerGit(t, root, "rev-parse", "HEAD")
	if _, err := r.Stage(ctx, jobID); err != nil || runnerGit(t, root, "rev-parse", "HEAD") != draftHead || p.calls != 1 {
		t.Fatal("stage replay created another Git commit or model call", err)
	}
	current, _ = kb.Memories(ctx, memory.KnowledgeQuery{ProjectID: r.Selection.ProjectID})
	if len(current.Entries) != 0 {
		t.Fatal("staging activated memory")
	}
	approved, err := kb.Review(ctx, knowledge.Review{ID: receipts[0].ID, ProjectID: r.Selection.ProjectID, ExpectedRevision: receipts[0].Revision, Action: "approve", Note: "A human reviewed the synthetic evidence and scope", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(approved.Statement, "User requirement: ") {
		t.Fatal("canonical memory lost source attribution")
	}
	projection, err := memory.Open(filepath.Join(t.TempDir(), "projection.sqlite"), storage.NewSQLite(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer projection.Close()
	for {
		n, err := projection.Sync(ctx, 16)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	for _, citation := range approved.Sources {
		actual, err := projection.ReadSource(ctx, citation)
		if err != nil || !strings.Contains(approved.Statement, actual.Text) {
			t.Fatal("extraction citation failed existing recall readback", err)
		}
	}
	if _, _, err := r.Discover(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if reports, err := r.Process(ctx, 1); err != nil || len(reports) != 0 || p.calls != 1 {
		t.Fatal("unchanged input was extracted twice", err)
	}
	if err := raw.PutBatch([]archive.Record{runnerRecord("second-source", "The synthetic project must now replace the old policy with a bounded persistent outbox.")}); err != nil {
		t.Fatal(err)
	}
	p.action = "corrects"
	if _, _, err := r.Discover(ctx, 8); err != nil {
		t.Fatal(err)
	}
	next, err := r.Process(ctx, 1)
	if err != nil || len(next) != 1 || len(next[0].Candidates) != 1 {
		t.Fatal("incremental correction extraction failed", err)
	}
	item := next[0].Candidates[0]
	if len(item.Proposal.Relations) != 1 || item.Proposal.Relations[0].TargetID != approved.ID || item.Proposal.Relations[0].TargetRevision != approved.Revision {
		t.Fatal("comparison lost exact canonical target")
	}
	receipts, err = r.Stage(ctx, next[0].JobID)
	if err != nil {
		t.Fatal(err)
	}
	current, _ = kb.Memories(ctx, memory.KnowledgeQuery{ProjectID: r.Selection.ProjectID})
	if len(current.Entries) != 1 || current.Entries[0].ID != approved.ID {
		t.Fatal("proposed correction retired old knowledge prematurely")
	}
	if _, err := kb.Review(ctx, knowledge.Review{ID: receipts[0].ID, ProjectID: r.Selection.ProjectID, ExpectedRevision: receipts[0].Revision, Action: "approve", Note: "A human reviewed the updated synthetic policy and replacement relation", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	all, err := kb.Memories(ctx, memory.KnowledgeQuery{ProjectID: r.Selection.ProjectID, View: "all"})
	if err != nil || all.States["current"] != 1 || all.States["superseded"] != 1 || p.calls != 2 {
		t.Fatal("reviewed correction did not preserve lifecycle", err)
	}
}

func TestRunnerInvalidatesChangedSourcesAndRecoversSavedOutputWithoutCallingAgain(t *testing.T) {
	r, raw, p, _, _ := runnerFixture(t)
	ctx := context.Background()
	_, registered, err := r.Discover(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.PutBatch([]archive.Record{runnerRecord("first-source", "The revised synthetic requirement uses a separate bounded queue policy.")}); err != nil {
		t.Fatal(err)
	}
	if reports, err := r.Process(ctx, 1); err != nil || len(reports) != 0 || p.calls != 0 {
		t.Fatal("changed source was sent to the provider", err)
	}
	stale, err := r.Jobs.Get(ctx, registered[0].ID)
	if err != nil || stale.State != "failed" {
		t.Fatal("stale queued job was left blocking new work", err)
	}
	_, registered, err = r.Discover(ctx, 8)
	if err != nil || len(registered) != 1 {
		t.Fatal("new revision was not discovered", err)
	}
	job := registered[0]
	request := Request{Input: job.Input, MaxOutputTokens: r.Limits.MaxOutputTokens}
	estimate, _ := p.EstimateInputTokens(request)
	job, err = r.Jobs.Reserve(ctx, job.ID, estimate, request.MaxOutputTokens)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Jobs.Start(ctx, job.ID, job.Attempt); err != nil {
		t.Fatal(err)
	}
	response, err := p.Extract(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildReport(ctx, job.Input, response, r.Source)
	if err != nil {
		t.Fatal(err)
	}
	report.JobID = job.ID
	if err := r.Jobs.SaveResult(ctx, job.ID, job.Attempt, report); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after durable output but before completion/export.
	recovered, err := r.Process(ctx, 1)
	if err != nil || len(recovered) != 1 || recovered[0].JobID != job.ID || p.calls != 1 {
		t.Fatal("saved model output was lost or called again", err)
	}
}

func TestRunnerExplicitRejectionRetriesButUncertainCallsNeedOperator(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "definite_rejection", true: "uncertain"}[uncertain], func(t *testing.T) {
			r, raw, p, _, _ := runnerFixture(t)
			ctx := context.Background()
			clock := time.Date(2032, 1, 1, 12, 0, 0, 0, time.UTC)
			r.Jobs.now = func() time.Time { return clock }
			_, jobs, err := r.Discover(ctx, 8)
			if err != nil {
				t.Fatal(err)
			}
			p.failure = &CallError{Code: "synthetic", Retryable: !uncertain, Uncertain: uncertain}
			if _, err := r.Process(ctx, 1); err == nil || p.calls != 1 {
				t.Fatal("provider failure disappeared")
			}
			job, err := r.Jobs.Get(ctx, jobs[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if uncertain {
				if job.State != "uncertain" {
					t.Fatal("unknown outcome became automatically retryable")
				}
				if err := raw.PutBatch([]archive.Record{runnerRecord("another-source", "An unrelated synthetic policy requires a bounded source-processing budget.")}); err != nil {
					t.Fatal(err)
				}
				if _, _, err := r.Discover(ctx, 8); err != nil {
					t.Fatal(err)
				}
				p.failure = nil
				if _, err := r.Process(ctx, 1); !errors.Is(err, ErrUncertain) || p.calls != 1 {
					t.Fatal("uncertain call allowed unattended work", err)
				}
				if err := r.Jobs.ResolveUncertain(ctx, job.ID, job.Attempt, "abandon", false); err == nil {
					t.Fatal("uncertain reconciliation did not require confirmation")
				}
				if err := r.Jobs.ResolveUncertain(ctx, job.ID, job.Attempt, "abandon", true); err != nil {
					t.Fatal(err)
				}
			} else {
				if job.State != "retry" {
					t.Fatal("definite rejection lost retry state")
				}
				clock = clock.Add(time.Hour)
				p.failure = nil
			}
			if reports, err := r.Process(ctx, 1); err != nil || len(reports) != 1 || p.calls != 2 {
				t.Fatal("safe resumed work did not complete", err)
			}
			status, err := r.Jobs.Status(ctx)
			if err != nil || status.TodayReservations != 2 {
				t.Fatal("retry lost its call reservation", err)
			}
		})
	}
}

func TestRunnerDoesNotReturnSavedReportWhoseSourceChanged(t *testing.T) {
	r, raw, p, _, _ := runnerFixture(t)
	ctx := context.Background()
	_, registered, err := r.Discover(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	job := registered[0]
	request := Request{Input: job.Input, MaxOutputTokens: r.Limits.MaxOutputTokens}
	estimate, _ := p.EstimateInputTokens(request)
	job, err = r.Jobs.Reserve(ctx, job.ID, estimate, request.MaxOutputTokens)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Jobs.Start(ctx, job.ID, job.Attempt); err != nil {
		t.Fatal(err)
	}
	response, err := p.Extract(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildReport(ctx, job.Input, response, r.Source)
	if err != nil {
		t.Fatal(err)
	}
	report.JobID = job.ID
	if err := r.Jobs.SaveResult(ctx, job.ID, job.Attempt, report); err != nil {
		t.Fatal(err)
	}
	if err := raw.PutBatch([]archive.Record{runnerRecord("first-source", "This source now contains a different synthetic requirement and revision.")}); err != nil {
		t.Fatal(err)
	}
	reports, err := r.Process(ctx, 1)
	if err != nil || len(reports) != 0 || p.calls != 1 {
		t.Fatal("cached report with stale citations returned or called again", err)
	}
	stored, err := r.Jobs.Get(ctx, job.ID)
	if err != nil || stored.State != "done" || stored.Report == nil {
		t.Fatal("historical generated evidence was not preserved", err)
	}
	if _, err := r.Stage(ctx, job.ID); err == nil {
		t.Fatal("stale generated candidate was staged")
	}
}

type runnerComparisonKnowledge struct{ result memory.KnowledgeResult }

func (k runnerComparisonKnowledge) Memories(context.Context, memory.KnowledgeQuery) (memory.KnowledgeResult, error) {
	return k.result, nil
}
func (runnerComparisonKnowledge) Propose(context.Context, memory.MemoryProposal) (memory.MemoryEntry, error) {
	return memory.MemoryEntry{}, ErrInvalid
}

func TestComparisonPreservesReviewStateInsteadOfPresentingDueClaimsAsCurrent(t *testing.T) {
	r, _, _, _, _ := runnerFixture(t)
	ctx := context.Background()
	_, jobs, err := r.Discover(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	r.Knowledge = runnerComparisonKnowledge{result: memory.KnowledgeResult{Entries: []memory.MemoryEntry{{ID: "prior-policy", ProjectID: r.Selection.ProjectID, Status: "verified", Revision: strings.Repeat("a", 64), Statement: "A previously reviewed synthetic policy requires revalidation.", ReviewAfter: "2000-01-01", Validity: &memory.MemoryValidity{State: "review_due", Reasons: []string{"review_after_elapsed"}}}}}}
	in, err := r.comparison(ctx, jobs[0].Input)
	if err != nil || len(in.Existing) != 1 || in.Existing[0].Validity == nil || in.Existing[0].Validity.State != "review_due" || in.Existing[0].ReviewAfter != "2000-01-01" {
		t.Fatal("model comparison lost the canonical eligibility/review state", err)
	}
}
