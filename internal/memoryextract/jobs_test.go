package memoryextract

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"cpa-session-archive/internal/memory"
)

func ledgerInput(n int) Input {
	text := "synthetic-private-ledger-quote 中文"
	in := Input{Version: Version, ArchiveID: "synthetic-archive", ProjectID: "synthetic-project", SessionID: "synthetic-session", RequestID: fmt.Sprintf("request-%d", n), Revision: strings.Repeat("b", 64), Sequence: int64(n), KnowledgeComplete: true}
	in.Excerpts = []Excerpt{{ID: "excerpt-0", Role: "user", Kind: "text", Text: text, Citation: memory.Citation{NormalizerVersion: memory.NormalizerVersion, RequestID: in.RequestID, Revision: in.Revision, Side: "original_request", Pointer: "/messages/0/content", Start: 0, End: utf8.RuneCountInString(text)}}}
	return in
}

func ledgerPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "jobs.sqlite")
}

func ledgerOpen(t *testing.T, path string, limits Limits) *Jobs {
	t.Helper()
	s, err := OpenJobs(path, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func ledgerEnqueue(t *testing.T, s *Jobs, n int) Job {
	t.Helper()
	in := ledgerInput(n)
	job, err := s.Enqueue(context.Background(), Key(in, "synthetic-provider"), in, "synthetic-provider")
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func ledgerReserve(t *testing.T, s *Jobs, id string) Job {
	t.Helper()
	job, err := s.Reserve(context.Background(), id, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func ledgerReport(job Job) Report {
	return Report{Version: Version, JobID: job.ID, ProjectID: job.Input.ProjectID, SessionID: job.Input.SessionID, RequiresHumanReview: true, Candidates: []Candidate{}, Rejected: []Rejection{}, Usage: Usage{InputTokens: 8, OutputTokens: 1}}
}

func ledgerStatus(t *testing.T, s *Jobs) JobStatus {
	t.Helper()
	status, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func ledgerState(t *testing.T, s *Jobs, id, want string) Job {
	t.Helper()
	job, err := s.Get(context.Background(), id)
	if err != nil || job.State != want {
		t.Fatalf("state = %q, error = %v; want %q", job.State, err, want)
	}
	return job
}

func TestLedgerDedupeAndSourceProviderSnapshotIdentity(t *testing.T) {
	ctx := context.Background()
	s := ledgerOpen(t, ledgerPath(t), Limits{})
	first := ledgerEnqueue(t, s, 1)
	again := ledgerEnqueue(t, s, 1)
	if again.ID != first.ID || again.State != "pending" || again.Attempts != 0 {
		t.Fatal("duplicate enqueue did not reuse untouched job")
	}
	for _, change := range []struct {
		name     string
		provider string
		edit     func(*Input)
	}{
		{"revision", "synthetic-provider", func(in *Input) { in.Revision = strings.Repeat("c", 64); in.Excerpts[0].Citation.Revision = in.Revision }},
		{"provider", "another-provider", func(*Input) {}},
		{"scope", "synthetic-provider", func(in *Input) { in.ProjectID = "another-project" }},
		{"snapshot", "synthetic-provider", func(in *Input) {
			in.Existing = []memory.MemoryEntry{{ID: "prior-memory", Revision: strings.Repeat("d", 64)}}
		}},
		{"archive", "synthetic-provider", func(in *Input) { in.ArchiveID = "another-archive" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			in := ledgerInput(1)
			change.edit(&in)
			job, err := s.Enqueue(ctx, Key(in, change.provider), in, change.provider)
			if err != nil || job.ID == first.ID {
				t.Fatalf("changed identity reused old job: %v", err)
			}
		})
	}
	if _, err := s.Enqueue(ctx, "caller-chosen-id", ledgerInput(1), "synthetic-provider"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbound key accepted: %v", err)
	}
	if status := ledgerStatus(t, s); status.TodayReservations != 0 || status.States["pending"] != 6 {
		t.Fatalf("enqueue changed budget or lost identities: %+v", status)
	}
}

func TestLedgerDispatchFencingAndResultReplay(t *testing.T) {
	ctx := context.Background()
	s := ledgerOpen(t, ledgerPath(t), Limits{})
	job := ledgerEnqueue(t, s, 1)
	if err := s.Complete(ctx, job.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pending completed: %v", err)
	}
	job = ledgerReserve(t, s, job.ID)
	if job.Attempt == "" || job.Attempts != 1 || job.State != "reserved" {
		t.Fatal("reservation missing attempt fence")
	}
	if err := s.Start(ctx, job.ID, "wrong-attempt"); !errors.Is(err, ErrBusy) {
		t.Fatalf("wrong dispatch fence accepted: %v", err)
	}
	report := ledgerReport(job)
	// Omitted optional slices become nil after JSON decoding. A successful
	// durable write must remain replayable with the original in-memory report.
	report.Candidates = []Candidate{{ID: "synthetic-candidate", RequiresHumanReview: true, Warnings: []string{}, DuplicateOf: []Target{}}}
	if err := s.SaveResult(ctx, job.ID, job.Attempt, report); !errors.Is(err, ErrBusy) {
		t.Fatalf("result accepted before dispatch: %v", err)
	}
	if err := s.Start(ctx, job.ID, job.Attempt); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx, job.ID, job.Attempt); !errors.Is(err, ErrBusy) {
		t.Fatalf("dispatch fence was reusable: %v", err)
	}
	if err := s.Fail(ctx, job.ID, "wrong-attempt", false, false); !errors.Is(err, ErrBusy) {
		t.Fatalf("wrong failure fence accepted: %v", err)
	}
	wrong := report
	wrong.SessionID = "foreign-session"
	if err := s.SaveResult(ctx, job.ID, job.Attempt, wrong); !errors.Is(err, ErrBusy) {
		t.Fatalf("foreign result accepted: %v", err)
	}
	if err := s.SaveResult(ctx, job.ID, "wrong-attempt", report); !errors.Is(err, ErrBusy) {
		t.Fatalf("wrong result fence accepted: %v", err)
	}
	for _, wrong := range []Report{{}, {Version: Version, JobID: job.ID, RequiresHumanReview: false}, {Version: Version, JobID: job.ID, RequiresHumanReview: true, Usage: Usage{InputTokens: -1}}} {
		if err := s.SaveResult(ctx, job.ID, job.Attempt, wrong); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid result accepted: %v", err)
		}
	}
	if err := s.SaveResult(ctx, job.ID, job.Attempt, report); err != nil {
		t.Fatal(err)
	}
	ledgerState(t, s, job.ID, "generated")
	if err := s.SaveResult(ctx, job.ID, job.Attempt, report); err != nil {
		t.Fatalf("identical report replay failed: %v", err)
	}
	if err := s.Complete(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, job.ID); err != nil {
		t.Fatalf("completion was not idempotent: %v", err)
	}
	if err := s.SaveResult(ctx, job.ID, job.Attempt, report); err != nil {
		t.Fatal(err)
	}
	changed := report
	changed.Usage.InputTokens++
	if err := s.SaveResult(ctx, job.ID, job.Attempt, changed); !errors.Is(err, ErrBusy) {
		t.Fatalf("changed replay accepted: %v", err)
	}
	status := ledgerStatus(t, s)
	if status.TodayReservations != 1 || status.TodayInput != 10 || status.TodayOutput != 2 || status.TodayReportedInput != 8 || status.TodayReportedOutput != 1 || status.States["done"] != 1 {
		t.Fatalf("replay charged budget or duplicated reported usage: %+v", status)
	}
	metadata, _ := json.Marshal(status)
	if bytes.Contains(metadata, []byte("synthetic-private-ledger-quote")) || bytes.Contains(metadata, []byte(job.Attempt)) || bytes.Contains(metadata, []byte(job.Input.RequestID)) {
		t.Fatal("aggregate status leaked source content or private job fields")
	}
}

func TestLedgerConcurrentDailyCapsAcrossHandles(t *testing.T) {
	for _, dimension := range []string{"calls", "input", "output"} {
		t.Run(dimension, func(t *testing.T) {
			limits := DefaultLimits()
			switch dimension {
			case "calls":
				limits.DailyCalls = 3
			case "input":
				limits.DailyInputTokens = 30
			case "output":
				limits.DailyOutputTokens = 6
			}
			path := ledgerPath(t)
			handles := make([]*Jobs, 4)
			for i := range handles {
				handles[i] = ledgerOpen(t, path, limits)
			}
			jobs := make([]Job, 12)
			for i := range jobs {
				jobs[i] = ledgerEnqueue(t, handles[0], i+1)
			}
			var wg sync.WaitGroup
			outcomes := make(chan error, len(jobs))
			for i, job := range jobs {
				wg.Add(1)
				go func(s *Jobs, id string) {
					defer wg.Done()
					deadline := time.Now().Add(10 * time.Second)
					for {
						reserved, err := s.Reserve(context.Background(), id, 10, 2)
						if errors.Is(err, ErrBusy) && time.Now().Before(deadline) {
							runtime.Gosched()
							continue
						}
						if err == nil {
							err = s.Fail(context.Background(), id, reserved.Attempt, false, false)
						}
						outcomes <- err
						return
					}
				}(handles[i%len(handles)], job.ID)
			}
			wg.Wait()
			close(outcomes)
			granted, denied := 0, 0
			for err := range outcomes {
				switch {
				case err == nil:
					granted++
				case errors.Is(err, ErrBudget):
					denied++
				default:
					t.Fatalf("unexpected concurrent reservation error: %v", err)
				}
			}
			status := ledgerStatus(t, handles[0])
			if granted != 3 || denied != 9 || status.TodayReservations != 3 || status.TodayInput != 30 || status.TodayOutput != 6 {
				t.Fatalf("daily %s cap was not atomic: granted=%d denied=%d status=%+v", dimension, granted, denied, status)
			}
		})
	}
}

func TestLedgerLiveWorkerPauseAndControlHandle(t *testing.T) {
	ctx := context.Background()
	path := ledgerPath(t)
	worker := ledgerOpen(t, path, Limits{})
	job := ledgerReserve(t, worker, ledgerEnqueue(t, worker, 1).ID)
	other := ledgerEnqueue(t, worker, 2)
	control := ledgerOpen(t, path, Limits{})
	ledgerState(t, control, job.ID, "reserved")
	if _, err := control.Reserve(ctx, other.ID, 10, 2); !errors.Is(err, ErrBusy) {
		t.Fatalf("second handle stole worker lock: %v", err)
	}
	if err := control.Start(ctx, job.ID, job.Attempt); !errors.Is(err, ErrBusy) {
		t.Fatalf("control handle could dispatch another worker's call: %v", err)
	}
	if err := control.Pause(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(ctx, job.ID, job.Attempt); !errors.Is(err, ErrPaused) {
		t.Fatalf("pause did not fence undispatched reservation: %v", err)
	}
	if err := worker.Fail(ctx, job.ID, job.Attempt, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Reserve(ctx, other.ID, 10, 2); !errors.Is(err, ErrPaused) {
		t.Fatalf("pause did not stop new reservation: %v", err)
	}
	if err := control.Pause(ctx, false); err != nil {
		t.Fatal(err)
	}
	job = ledgerReserve(t, worker, other.ID)
	if err := worker.Start(ctx, job.ID, job.Attempt); err != nil {
		t.Fatal(err)
	}
	third := ledgerOpen(t, path, Limits{})
	ledgerState(t, third, job.ID, "in_flight")
	if err := third.Pause(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := worker.SaveResult(ctx, job.ID, job.Attempt, ledgerReport(job)); err != nil {
		t.Fatalf("pause lost already generated output: %v", err)
	}
	if err := control.Complete(ctx, job.ID); err != nil {
		t.Fatalf("pause blocked staging completion: %v", err)
	}
	if err := control.SaveResult(ctx, job.ID, job.Attempt, ledgerReport(job)); err != nil {
		t.Fatalf("completed replay failed on control handle: %v", err)
	}
}

func TestLedgerRecoveryRetainsBudgetAndNeverRetriesAmbiguousDispatch(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(fmt.Sprintf("dispatched=%t", dispatched), func(t *testing.T) {
			ctx := context.Background()
			path := ledgerPath(t)
			first := ledgerOpen(t, path, Limits{})
			job := ledgerReserve(t, first, ledgerEnqueue(t, first, 1).ID)
			waiting := ledgerEnqueue(t, first, 2)
			// A handle opened before the crash must recover safely at Reserve too.
			control := ledgerOpen(t, path, Limits{})
			if dispatched {
				if err := first.Start(ctx, job.ID, job.Attempt); err != nil {
					t.Fatal(err)
				}
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			if dispatched {
				if _, err := control.Reserve(ctx, waiting.ID, 10, 2); !errors.Is(err, ErrUncertain) {
					t.Fatalf("ambiguous dispatch allowed another call: %v", err)
				}
				old := ledgerState(t, control, job.ID, "uncertain")
				if old.Attempt != job.Attempt || old.Attempts != 1 {
					t.Fatal("uncertain recovery lost exact attempt")
				}
			} else {
				next := ledgerReserve(t, control, job.ID)
				if next.Attempt == job.Attempt || next.Attempts != 2 {
					t.Fatal("undispatched recovery reused attempt or lost attempt bound")
				}
				if err := control.Fail(ctx, next.ID, next.Attempt, false, false); err != nil {
					t.Fatal(err)
				}
			}
			reopened := ledgerOpen(t, path, Limits{})
			want := 1
			if !dispatched {
				want = 2
			}
			status := ledgerStatus(t, reopened)
			if status.TodayReservations != want || status.TodayInput != want*10 || status.TodayReportedInput != 0 {
				t.Fatalf("recovery refunded unknown usage: %+v", status)
			}
		})
	}
}

func TestLedgerUncertainResolutionRequiresExactAttestedAttempt(t *testing.T) {
	ctx := context.Background()
	s := ledgerOpen(t, ledgerPath(t), Limits{})
	job := ledgerReserve(t, s, ledgerEnqueue(t, s, 1).ID)
	if err := s.Start(ctx, job.ID, job.Attempt); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(ctx, job.ID, job.Attempt, true, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		attempt, action string
		confirmed       bool
		want            error
	}{
		{job.Attempt, "retry", false, ErrInvalid},
		{"", "retry", true, ErrInvalid},
		{job.Attempt, "discard", true, ErrInvalid},
		{"wrong-attempt", "retry", true, ErrUncertain},
	} {
		if err := s.ResolveUncertain(ctx, job.ID, tc.attempt, tc.action, tc.confirmed); !errors.Is(err, tc.want) {
			t.Fatalf("unattested or stale reconciliation accepted: %v", err)
		}
		ledgerState(t, s, job.ID, "uncertain")
	}
	if err := s.ResolveUncertain(ctx, job.ID, job.Attempt, "retry", true); err != nil {
		t.Fatal(err)
	}
	second := ledgerReserve(t, s, job.ID)
	if second.Attempt == job.Attempt || second.Attempts != 2 || ledgerStatus(t, s).TodayReservations != 2 {
		t.Fatal("explicit retry reused reservation or fence")
	}
	if err := s.Start(ctx, second.ID, second.Attempt); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(ctx, second.ID, second.Attempt, true, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveUncertain(ctx, second.ID, job.Attempt, "abandon", true); !errors.Is(err, ErrUncertain) {
		t.Fatalf("old reconciliation fence altered later attempt: %v", err)
	}
	if err := s.ResolveUncertain(ctx, second.ID, second.Attempt, "abandon", true); err != nil {
		t.Fatal(err)
	}
	ledgerState(t, s, job.ID, "failed")
	waiting := ledgerEnqueue(t, s, 2)
	next := ledgerReserve(t, s, waiting.ID)
	if err := s.Fail(ctx, next.ID, next.Attempt, false, false); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerRetryBackoffAndAttemptBound(t *testing.T) {
	ctx := context.Background()
	s := ledgerOpen(t, ledgerPath(t), Limits{})
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	job := ledgerEnqueue(t, s, 1)
	for attempt := 1; attempt <= 3; attempt++ {
		job = ledgerReserve(t, s, job.ID)
		if job.Attempts != attempt {
			t.Fatal("attempt counter did not advance")
		}
		if err := s.Start(ctx, job.ID, job.Attempt); err != nil {
			t.Fatal(err)
		}
		if err := s.Fail(ctx, job.ID, job.Attempt, true, false); err != nil {
			t.Fatal(err)
		}
		if attempt == 3 {
			ledgerState(t, s, job.ID, "failed")
			break
		}
		queued := ledgerState(t, s, job.ID, "retry")
		delay := time.Duration(1<<attempt) * time.Second
		if !queued.NextAttempt.Equal(now.Add(delay)) {
			t.Fatalf("backoff=%v, want %v", queued.NextAttempt.Sub(now), delay)
		}
		if _, err := s.Reserve(ctx, job.ID, 10, 2); !errors.Is(err, ErrBusy) {
			t.Fatalf("retry ran before backoff: %v", err)
		}
		pending, err := s.ListPending(ctx, 100)
		if err != nil || len(pending) != 0 {
			t.Fatalf("not-yet-due retry listed: count=%d error=%v", len(pending), err)
		}
		now = now.Add(delay)
	}
	if status := ledgerStatus(t, s); status.TodayReservations != 3 || status.TodayInput != 30 || status.TodayReportedInput != 0 {
		t.Fatalf("safe retries refunded reservations: %+v", status)
	}
	// An explicit uncertain retry is bounded by the same maximum.
	limited := ledgerOpen(t, ledgerPath(t), Limits{MaxAttempts: 1})
	job = ledgerReserve(t, limited, ledgerEnqueue(t, limited, 1).ID)
	if err := limited.Fail(ctx, job.ID, job.Attempt, true, true); err != nil {
		t.Fatal(err)
	}
	if err := limited.ResolveUncertain(ctx, job.ID, job.Attempt, "retry", true); !errors.Is(err, ErrBudget) {
		t.Fatalf("operator retry bypassed max attempts: %v", err)
	}
	if err := limited.ResolveUncertain(ctx, job.ID, job.Attempt, "abandon", true); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerUTCDayUsageAttributionAndOverrunPause(t *testing.T) {
	ctx := context.Background()
	s := ledgerOpen(t, ledgerPath(t), Limits{DailyCalls: 1})
	now := time.Date(2026, 10, 21, 16, 59, 59, 0, time.FixedZone("PDT", -7*60*60))
	s.now = func() time.Time { return now }
	first := ledgerReserve(t, s, ledgerEnqueue(t, s, 1).ID)
	if err := s.Start(ctx, first.ID, first.Attempt); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if err := s.SaveResult(ctx, first.ID, first.Attempt, ledgerReport(first)); err != nil {
		t.Fatal(err)
	}
	status := ledgerStatus(t, s)
	if status.Day != "2026-10-22" || status.TodayReservations != 0 || status.TodayReportedInput != 0 {
		t.Fatalf("day boundary used local day or billed prior result to new day: %+v", status)
	}
	second := ledgerReserve(t, s, ledgerEnqueue(t, s, 2).ID)
	if err := s.Start(ctx, second.ID, second.Attempt); err != nil {
		t.Fatal(err)
	}
	report := ledgerReport(second)
	report.Usage.InputTokens = 11
	if err := s.SaveResult(ctx, second.ID, second.Attempt, report); err != nil {
		t.Fatal(err)
	}
	status = ledgerStatus(t, s)
	if !status.Paused || !status.UsageExceededReservation || status.TodayInput != 10 || status.TodayReportedInput != 11 || status.States["generated"] != 2 {
		t.Fatalf("usage overrun was discarded or did not pause: %+v", status)
	}
	third := ledgerEnqueue(t, s, 3)
	if _, err := s.Reserve(ctx, third.ID, 10, 2); !errors.Is(err, ErrPaused) {
		t.Fatalf("overrun allowed further dispatch: %v", err)
	}
	var oldCalls, oldReported int
	if err := s.db.QueryRow(`SELECT calls,reported_input FROM extract_budget WHERE day='2026-10-21'`).Scan(&oldCalls, &oldReported); err != nil || oldCalls != 1 || oldReported != 8 {
		t.Fatalf("late prior-day usage lost: calls=%d reported=%d error=%v", oldCalls, oldReported, err)
	}
}

func TestLedgerInvalidationAndPendingViews(t *testing.T) {
	ctx := context.Background()
	s := ledgerOpen(t, ledgerPath(t), Limits{})
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	invalid := ledgerEnqueue(t, s, 1)
	if err := s.Invalidate(ctx, invalid.ID); err != nil {
		t.Fatal(err)
	}
	if status := ledgerStatus(t, s); status.TodayReservations != 0 {
		t.Fatal("invalidation spent budget")
	}
	generated := ledgerReserve(t, s, ledgerEnqueue(t, s, 2).ID)
	if err := s.Invalidate(ctx, generated.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("reserved evidence invalidated: %v", err)
	}
	if err := s.Start(ctx, generated.ID, generated.Attempt); err != nil {
		t.Fatal(err)
	}
	if err := s.Invalidate(ctx, generated.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("in-flight evidence invalidated: %v", err)
	}
	if err := s.SaveResult(ctx, generated.ID, generated.Attempt, ledgerReport(generated)); err != nil {
		t.Fatal(err)
	}
	if err := s.Invalidate(ctx, generated.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("generated evidence invalidated: %v", err)
	}
	retry := ledgerReserve(t, s, ledgerEnqueue(t, s, 3).ID)
	if err := s.Fail(ctx, retry.ID, retry.Attempt, true, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Invalidate(ctx, retry.ID); err != nil {
		t.Fatal(err)
	}
	uncertain := ledgerReserve(t, s, ledgerEnqueue(t, s, 4).ID)
	if err := s.Fail(ctx, uncertain.ID, uncertain.Attempt, false, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Invalidate(ctx, uncertain.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("uncertain evidence invalidated without reconciliation: %v", err)
	}
	pending := ledgerEnqueue(t, s, 5)
	items, err := s.ListPending(ctx, 100)
	if err != nil || len(items) != 2 || items[0].ID != generated.ID || items[1].ID != pending.ID {
		t.Fatalf("pending view failed generated-first or included terminal jobs: count=%d error=%v", len(items), err)
	}
	all, err := s.List(ctx, 100)
	if err != nil || len(all) != 5 {
		t.Fatalf("control view omitted terminal states: count=%d error=%v", len(all), err)
	}
	for _, limit := range []int{0, -1, 1001} {
		if _, err := s.List(ctx, limit); !errors.Is(err, ErrInvalid) {
			t.Fatal("unbounded control listing accepted")
		}
		if _, err := s.ListPending(ctx, limit); !errors.Is(err, ErrInvalid) {
			t.Fatal("unbounded pending listing accepted")
		}
	}
}

func TestLedgerPendingScopeFilterPrecedesLimit(t *testing.T) {
	ctx := context.Background()
	s := ledgerOpen(t, ledgerPath(t), Limits{})
	selection := Selection{ProjectID: "synthetic-project", SessionID: "synthetic-session"}
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for i := 1; i <= 110; i++ {
		in, provider := ledgerInput(i), "synthetic-provider"
		switch i % 3 {
		case 0:
			in.ProjectID = "other-project"
		case 1:
			in.SessionID = "other-session"
		case 2:
			provider = "old-provider"
		}
		if _, err := s.Enqueue(ctx, Key(in, provider), in, provider); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	target := ledgerEnqueue(t, s, 111)
	items, err := s.ListPendingFor(ctx, selection, "synthetic-provider", 1)
	if err != nil || len(items) != 1 || items[0].ID != target.ID {
		t.Fatalf("other scopes/profiles starved selected job: count=%d error=%v", len(items), err)
	}
	projectItems, err := s.ListPendingFor(ctx, Selection{ProjectID: selection.ProjectID}, "synthetic-provider", 100)
	if err != nil || len(projectItems) <= 1 {
		t.Fatalf("project-wide scope omitted other sessions: count=%d error=%v", len(projectItems), err)
	}
	for _, invalid := range []Selection{{}, {ProjectID: " synthetic-project"}, {ProjectID: "synthetic-project", SessionID: " synthetic-session"}} {
		if _, err := s.ListPendingFor(ctx, invalid, "synthetic-provider", 1); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid scope accepted: %v", err)
		}
	}
	for _, limit := range []int{0, -1, 1001} {
		if _, err := s.ListPendingFor(ctx, selection, "synthetic-provider", limit); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unbounded filtered listing accepted: %v", err)
		}
	}
}

func TestLedgerCursorAtomicityCASAndScopeIsolation(t *testing.T) {
	ctx := context.Background()
	path := ledgerPath(t)
	s := ledgerOpen(t, path, Limits{})
	selection := Selection{ProjectID: "synthetic-project", SessionID: "synthetic-session"}
	provider := "synthetic-provider"
	initial := ScanCursor{}
	next := ScanCursor{ArchiveID: "synthetic-archive", Watermark: 10, BackfillAfterID: 2, ChangeAfter: 3, BackfillDone: true, Incomplete: true}
	jobs, err := s.EnqueueAndAdvance(ctx, selection, provider, initial, next, []Input{ledgerInput(1), ledgerInput(2)})
	if err != nil || len(jobs) != 2 {
		t.Fatalf("atomic enqueue failed: %v", err)
	}
	if current, err := s.Cursor(ctx, selection, provider); err != nil || current != next {
		t.Fatalf("cursor not stored with inputs: %+v %v", current, err)
	}
	if _, err := s.EnqueueAndAdvance(ctx, selection, provider, initial, next, []Input{ledgerInput(3)}); !errors.Is(err, ErrBusy) {
		t.Fatalf("stale cursor overwrote discovery: %v", err)
	}
	if _, err := s.Get(ctx, Key(ledgerInput(3), provider)); !errors.Is(err, ErrInvalid) {
		t.Fatal("stale cursor inserted a partial job")
	}
	advanced := next
	advanced.Watermark, advanced.ChangeAfter = 12, 12
	bad := ledgerInput(4)
	bad.SessionID = "another-session"
	if _, err := s.EnqueueAndAdvance(ctx, selection, provider, next, advanced, []Input{ledgerInput(3), bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong scope accepted: %v", err)
	}
	if _, err := s.Get(ctx, Key(ledgerInput(3), provider)); !errors.Is(err, ErrInvalid) {
		t.Fatal("failed batch retained first job")
	}
	if current, err := s.Cursor(ctx, selection, provider); err != nil || current != next {
		t.Fatal("failed batch advanced cursor")
	}
	for _, edit := range []func(*ScanCursor){
		func(c *ScanCursor) { c.ArchiveID = "other-archive" },
		func(c *ScanCursor) { c.Watermark = 9 },
		func(c *ScanCursor) { c.ChangeAfter = 2 },
		func(c *ScanCursor) { c.ChangeAfter = 11 },
		func(c *ScanCursor) { c.BackfillAfterID = 1 },
		func(c *ScanCursor) { c.BackfillDone = false },
		func(c *ScanCursor) { c.Incomplete = false },
	} {
		invalid := next
		edit(&invalid)
		if _, err := s.EnqueueAndAdvance(ctx, selection, provider, next, invalid, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("cursor rollback or coverage laundering accepted: %v", err)
		}
	}
	for _, edit := range []func(*Input){
		func(in *Input) { in.ArchiveID = "other-archive" },
		func(in *Input) { in.ProjectID = "other-project" },
		func(in *Input) { in.Sequence = 11 },
		func(in *Input) { in.Excerpts = nil },
	} {
		invalid := ledgerInput(3)
		edit(&invalid)
		if _, err := s.EnqueueAndAdvance(ctx, selection, provider, next, next, []Input{invalid}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid input advanced cursor: %v", err)
		}
	}
	if _, err := s.EnqueueAndAdvance(ctx, selection, provider, next, advanced, nil); err != nil {
		t.Fatal(err)
	}
	other := ledgerOpen(t, path, Limits{})
	if current, err := other.Cursor(ctx, selection, provider); err != nil || current != advanced {
		t.Fatal("cursor not durable across handles")
	}
	for _, isolated := range []struct {
		selection Selection
		provider  string
	}{{selection, "other-provider"}, {Selection{ProjectID: selection.ProjectID}, provider}, {Selection{ProjectID: "other-project"}, provider}} {
		if current, err := other.Cursor(ctx, isolated.selection, isolated.provider); err != nil || current != (ScanCursor{}) {
			t.Fatal("cursor crossed provider or scope boundary")
		}
	}
	if status := ledgerStatus(t, s); status.TodayReservations != 0 || status.States["pending"] != 2 {
		t.Fatalf("discovery spent call budget or duplicated jobs: %+v", status)
	}
}

func TestLedgerLimitsPersistAndReservationBounds(t *testing.T) {
	ctx := context.Background()
	path := ledgerPath(t)
	limits := Limits{MaxInputTokens: 40, MaxOutputTokens: 5, DailyCalls: 2, DailyInputTokens: 60, DailyOutputTokens: 8, MaxAttempts: 2}
	s := ledgerOpen(t, path, limits)
	job := ledgerEnqueue(t, s, 1)
	for _, tokens := range [][2]int{{0, 1}, {1, 0}, {-1, 2}, {41, 1}, {1, 6}} {
		if _, err := s.Reserve(ctx, job.ID, tokens[0], tokens[1]); !errors.Is(err, ErrBudget) {
			t.Fatalf("invalid per-call reservation accepted: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := ledgerOpen(t, path, Limits{})
	if got := ledgerStatus(t, reopened).Limits; got != limits {
		t.Fatalf("reopen reset persisted budget: %+v", got)
	}
	different := limits
	different.DailyCalls++
	if extra, err := OpenJobs(path, different); !errors.Is(err, ErrInvalid) {
		if extra != nil {
			_ = extra.Close()
		}
		t.Fatalf("silent budget reconfiguration accepted: %v", err)
	}
	for _, invalid := range []Limits{{MaxInputTokens: 8001}, {MaxOutputTokens: 1001}, {MaxAttempts: 4}, {DailyCalls: -1}} {
		if extra, err := OpenJobs(path, invalid); !errors.Is(err, ErrInvalid) {
			if extra != nil {
				_ = extra.Close()
			}
			t.Fatalf("unsafe limits accepted: %v", err)
		}
	}
}

func TestLedgerReopenRecoveryAndUndispatchedAttemptExhaustion(t *testing.T) {
	ctx := context.Background()
	for _, state := range []string{"reserved", "in_flight", "exhausted"} {
		t.Run(state, func(t *testing.T) {
			path := ledgerPath(t)
			limits := Limits{}
			if state == "exhausted" {
				limits.MaxAttempts = 1
			}
			s := ledgerOpen(t, path, limits)
			job := ledgerReserve(t, s, ledgerEnqueue(t, s, 1).ID)
			if state == "in_flight" {
				if err := s.Start(ctx, job.ID, job.Attempt); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := ledgerOpen(t, path, Limits{})
			want := map[string]string{"reserved": "pending", "in_flight": "uncertain", "exhausted": "failed"}[state]
			recovered := ledgerState(t, reopened, job.ID, want)
			if state == "in_flight" && recovered.Attempt != job.Attempt || state != "in_flight" && recovered.Attempt != "" {
				t.Fatal("recovery did not preserve only dispatched fence")
			}
			if status := ledgerStatus(t, reopened); status.TodayReservations != 1 || status.TodayInput != 10 || status.TodayReportedInput != 0 {
				t.Fatalf("reopen changed reservation accounting: %+v", status)
			}
		})
	}
}

func TestLedgerCursorCASAcrossHandlesAndMaximumSourcePage(t *testing.T) {
	ctx := context.Background()
	path := ledgerPath(t)
	a, b := ledgerOpen(t, path, Limits{}), ledgerOpen(t, path, Limits{})
	selection := Selection{ProjectID: "synthetic-project"}
	next := ScanCursor{ArchiveID: "synthetic-archive", Watermark: 10, BackfillDone: true}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, s := range []*Jobs{a, b} {
		go func(s *Jobs, n int) {
			<-start
			_, err := s.EnqueueAndAdvance(ctx, selection, "synthetic-provider", ScanCursor{}, next, []Input{ledgerInput(n)})
			results <- err
		}(s, i+1)
	}
	close(start)
	granted, stale := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			granted++
		case errors.Is(err, ErrBusy):
			stale++
		default:
			t.Fatalf("concurrent cursor update failed unexpectedly: %v", err)
		}
	}
	if granted != 1 || stale != 1 || ledgerStatus(t, a).States["pending"] != 1 {
		t.Fatalf("CAS lost atomicity: granted=%d stale=%d", granted, stale)
	}
	inputs := make([]Input, MaxScanInputs)
	for i := range inputs {
		inputs[i] = ledgerInput(i + 100)
	}
	next.Watermark = int64(MaxScanInputs + 100)
	if jobs, err := a.EnqueueAndAdvance(ctx, selection, "maximum-page-provider", ScanCursor{}, next, inputs); err != nil || len(jobs) != MaxScanInputs {
		t.Fatalf("source's valid maximum page was rejected: count=%d error=%v", len(jobs), err)
	}
	if _, err := a.EnqueueAndAdvance(ctx, selection, "too-large-provider", ScanCursor{}, next, append(inputs, ledgerInput(1))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized page was accepted: %v", err)
	}
}

func TestLedgerStorageGuardsAndForeignDatabasePreservation(t *testing.T) {
	if s, err := OpenJobs("relative.sqlite", Limits{}); !errors.Is(err, ErrInvalid) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatalf("relative ledger path accepted: %v", err)
	}
	for _, kind := range []string{"directory-permissions", "file-permissions", "symlink", "hardlink", "lock-symlink", "lock-hardlink", "foreign-sqlite", "wrong-version", "invalid-bytes"} {
		t.Run(kind, func(t *testing.T) {
			path := ledgerPath(t)
			var protected string
			switch kind {
			case "directory-permissions":
				if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
			case "file-permissions":
				if err := os.WriteFile(path, nil, 0644); err != nil {
					t.Fatal(err)
				}
				protected = path
			case "symlink", "hardlink":
				protected = path + ".target"
				if err := os.WriteFile(protected, []byte("synthetic-protected-bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				link := os.Symlink
				if kind == "hardlink" {
					link = os.Link
				}
				if err := link(protected, path); err != nil {
					t.Fatal(err)
				}
			case "lock-symlink", "lock-hardlink":
				s := ledgerOpen(t, path, Limits{})
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path + ".worker.lock"); err != nil {
					t.Fatal(err)
				}
				protected = path + ".target"
				if err := os.WriteFile(protected, []byte("synthetic-protected-lock"), 0600); err != nil {
					t.Fatal(err)
				}
				link := os.Symlink
				if kind == "lock-hardlink" {
					link = os.Link
				}
				if err := link(protected, path+".worker.lock"); err != nil {
					t.Fatal(err)
				}
			case "foreign-sqlite", "wrong-version":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				schema := `CREATE TABLE archive_sentinel(id INTEGER PRIMARY KEY,payload TEXT); INSERT INTO archive_sentinel VALUES(1,'synthetic-protected-archive');`
				if kind == "wrong-version" {
					schema += ` PRAGMA application_id=1129334104; PRAGMA user_version=999;`
				}
				if _, err := db.Exec(schema); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				protected = path
			case "invalid-bytes":
				if err := os.WriteFile(path, []byte("synthetic-non-sqlite"), 0600); err != nil {
					t.Fatal(err)
				}
				protected = path
			}
			var before []byte
			var beforeMode os.FileMode
			if protected != "" {
				var err error
				before, err = os.ReadFile(protected)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(protected)
				if err != nil {
					t.Fatal(err)
				}
				beforeMode = info.Mode()
			}
			if s, err := OpenJobs(path, Limits{}); !errors.Is(err, ErrJobsUnavailable) {
				if s != nil {
					_ = s.Close()
				}
				t.Fatalf("unsafe or foreign ledger accepted: %v", err)
			}
			if protected != "" {
				after, err := os.ReadFile(protected)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("rejected path changed protected bytes")
				}
				info, err := os.Stat(protected)
				if err != nil || info.Mode() != beforeMode {
					t.Fatal("rejected path changed protected permissions")
				}
			}
		})
	}
	t.Run("private-ledger-and-sidecars", func(t *testing.T) {
		path := ledgerPath(t)
		s := ledgerOpen(t, path, Limits{})
		ledgerEnqueue(t, s, 1)
		for _, candidate := range []string{path, path + ".worker.lock", path + "-wal", path + "-shm"} {
			info, err := os.Lstat(candidate)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				t.Fatalf("ledger or sidecar is not a private regular file: %s %v", filepath.Base(candidate), err)
			}
		}
	})
}
