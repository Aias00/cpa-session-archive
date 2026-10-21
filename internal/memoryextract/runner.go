package memoryextract

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/storage"
)

type Runner struct {
	Jobs      *Jobs
	Feed      storage.MemoryFeed
	Source    *SourceReader
	Provider  Provider
	Knowledge memory.Knowledge
	Selection Selection
	Limits    Limits
}

func (r *Runner) limits() Limits {
	if r.Limits.MaxInputTokens == 0 {
		return DefaultLimits()
	}
	return r.Limits
}

func (r *Runner) configured() error {
	if r.Jobs == nil || r.Source == nil || r.Feed == nil || r.Provider == nil || r.Provider.Fingerprint() == "" || r.Selection.ProjectID == "" {
		return ErrInvalid
	}
	return nil
}

func (r *Runner) comparison(ctx context.Context, in Input) (Input, error) {
	in.Existing = []memory.MemoryEntry{}
	in.KnowledgeComplete = false
	if r.Knowledge != nil {
		result, err := r.Knowledge.Memories(ctx, memory.KnowledgeQuery{ProjectID: in.ProjectID, View: "all", Limit: 100})
		if err != nil {
			return in, fmt.Errorf("canonical knowledge comparison unavailable")
		}
		in.KnowledgeComplete = !result.Truncated
		for _, entry := range result.Entries {
			if entry.Global || entry.ProjectID != in.ProjectID {
				return in, ErrInvalid
			}
			if entry.SupersededBy != "" || entry.Status != "verified" && entry.Status != "disputed" {
				continue
			}
			// Retain exact target identity and claim, without unrelated source
			// payloads or operational metadata in the model prompt.
			in.Existing = append(in.Existing, memory.MemoryEntry{ID: entry.ID, Title: entry.Title, Kind: entry.Kind, Statement: entry.Statement, Status: entry.Status, ProjectID: entry.ProjectID, Revision: entry.Revision, ReviewAfter: entry.ReviewAfter, Validity: entry.Validity})
		}
		sort.Slice(in.Existing, func(i, j int) bool { return in.Existing[i].ID < in.Existing[j].ID })
	}
	for {
		n, err := r.Provider.EstimateInputTokens(Request{Input: in, MaxOutputTokens: r.limits().MaxOutputTokens})
		if err != nil {
			return in, err
		}
		if n <= r.limits().MaxInputTokens {
			return in, nil
		}
		if len(in.Existing) == 0 {
			return in, ErrBudget
		}
		in.Existing = in.Existing[:len(in.Existing)-1]
		in.KnowledgeComplete = false
	}
}

// Discover durably registers the selected sources before advancing its cursor.
// A discovery cursor is not a claim that queued candidates have been reviewed.
func (r *Runner) Discover(ctx context.Context, maxPages int) (ScanPage, []Job, error) {
	var page ScanPage
	if err := r.configured(); err != nil {
		return page, nil, err
	}
	cursor, err := r.Jobs.Cursor(ctx, r.Selection, r.Provider.Fingerprint())
	if err != nil {
		return page, nil, err
	}
	page, err = Scan(ctx, r.Feed, ScanOptions{Selection: r.Selection, Cursor: cursor, PageSize: 32, MaxPages: maxPages})
	if err != nil {
		return page, nil, err
	}
	inputs := make([]Input, 0, len(page.Inputs))
	for _, in := range page.Inputs {
		if err := r.Source.VerifyInput(ctx, in); err != nil {
			return page, nil, err
		}
		in, err = r.comparison(ctx, in)
		if err != nil {
			return page, nil, err
		}
		inputs = append(inputs, in)
	}
	jobs, err := r.Jobs.EnqueueAndAdvance(ctx, r.Selection, r.Provider.Fingerprint(), cursor, page.Cursor, inputs)
	return page, jobs, err
}

func (r *Runner) selected(in Input) bool {
	return in.ProjectID == r.Selection.ProjectID && (r.Selection.SessionID == "" || in.SessionID == r.Selection.SessionID)
}

// Process generates reports only. It never invokes a canonical review action
// or writes Git drafts; staging is a separate explicit operation.
func (r *Runner) Process(ctx context.Context, limit int) ([]Report, error) {
	if err := r.configured(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	jobs, err := r.Jobs.ListPendingFor(ctx, r.Selection, r.Provider.Fingerprint(), 100)
	if err != nil {
		return nil, err
	}
	reports := []Report{}
	for _, job := range jobs {
		if len(reports) >= limit {
			break
		}
		if job.Provider != r.Provider.Fingerprint() || !r.selected(job.Input) {
			continue
		}
		if job.State == "generated" && job.Report != nil {
			if err := r.Source.VerifyInput(ctx, job.Input); err != nil {
				if errors.Is(err, memory.ErrSourceUnavailable) {
					if err := r.Jobs.Complete(ctx, job.ID); err != nil {
						return reports, err
					}
					continue
				}
				return reports, err
			}
			if err := r.Jobs.Complete(ctx, job.ID); err != nil {
				return reports, err
			}
			reports = append(reports, *job.Report)
			continue
		}
		if err := r.Source.VerifyInput(ctx, job.Input); err != nil {
			if errors.Is(err, memory.ErrSourceUnavailable) {
				if err := r.Jobs.Invalidate(ctx, job.ID); err != nil {
					return reports, err
				}
				continue
			}
			return reports, fmt.Errorf("queued source is no longer available; rescan the current source")
		}
		if err := ValidateEvidence(ctx, job.Input, r.Source); err != nil {
			return reports, err
		}
		request := Request{Input: job.Input, MaxOutputTokens: r.limits().MaxOutputTokens}
		inputTokens, err := r.Provider.EstimateInputTokens(request)
		if err != nil {
			return reports, err
		}
		job, err = r.Jobs.Reserve(ctx, job.ID, inputTokens, request.MaxOutputTokens)
		if err != nil {
			return reports, err
		}
		if err = r.Jobs.Start(ctx, job.ID, job.Attempt); err != nil {
			_ = r.Jobs.Fail(context.Background(), job.ID, job.Attempt, false, false)
			return reports, err
		}
		response, err := r.Provider.Extract(ctx, request)
		if err != nil {
			var call *CallError
			retryable, uncertain := false, true
			if errors.As(err, &call) {
				retryable, uncertain = call.Retryable, call.Uncertain
			}
			if saveErr := r.Jobs.Fail(context.Background(), job.ID, job.Attempt, retryable, uncertain); saveErr != nil {
				return reports, saveErr
			}
			return reports, err
		}
		if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 || response.Usage.InputTokens > job.ReservedInput || response.Usage.OutputTokens > job.ReservedOutput {
			_ = r.Jobs.Fail(context.Background(), job.ID, job.Attempt, false, true)
			return reports, fmt.Errorf("provider usage exceeded its reservation: %w", ErrBudget)
		}
		report, err := BuildReport(ctx, job.Input, response, r.Source)
		if err != nil {
			_ = r.Jobs.Fail(context.Background(), job.ID, job.Attempt, false, false)
			return reports, err
		}
		report.JobID = job.ID
		if err := r.Jobs.SaveResult(context.Background(), job.ID, job.Attempt, report); err != nil {
			return reports, err
		}
		if err := r.Jobs.Complete(context.Background(), job.ID); err != nil {
			return reports, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

type DraftReceipt struct {
	JobID    string `json:"job_id"`
	ID       string `json:"id"`
	Status   string `json:"status"`
	Revision string `json:"revision,omitempty"`
}

func proposalMatches(entry memory.MemoryEntry, p memory.MemoryProposal) bool {
	if entry.ID != p.ID || entry.Title != p.Title || entry.Kind != p.Kind || entry.Statement != p.Statement || entry.Global != p.Global || entry.ProjectID != p.ProjectID || entry.OperationID != p.OperationID {
		return false
	}
	if len(entry.Sources) != len(p.Sources) || len(entry.Relations) != len(p.Relations) {
		return false
	}
	for i, citation := range entry.Sources {
		if citation != p.Sources[i] {
			return false
		}
	}
	a := append([]memory.MemoryRelation(nil), entry.Relations...)
	b := append([]memory.MemoryRelation(nil), p.Relations...)
	less := func(values []memory.MemoryRelation) func(int, int) bool {
		return func(i, j int) bool { return values[i].TargetID < values[j].TargetID }
	}
	sort.Slice(a, less(a))
	sort.Slice(b, less(b))
	for i, relation := range a {
		if relation != b[i] {
			return false
		}
	}
	return true
}

// Stage revalidates retained evidence and creates candidates through the existing
// proposal capability. It has no review capability and never activates memory.
// Replaying after a Git commit/receipt interruption reconciles the same draft.
func (r *Runner) Stage(ctx context.Context, id string) ([]DraftReceipt, error) {
	if r.Jobs == nil || r.Source == nil || r.Knowledge == nil || r.Selection.ProjectID == "" {
		return nil, ErrInvalid
	}
	job, err := r.Jobs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !r.selected(job.Input) || job.Report == nil || job.State != "done" && job.State != "generated" {
		return nil, ErrInvalid
	}
	if err := r.Source.VerifyInput(ctx, job.Input); err != nil {
		return nil, err
	}
	if err := ValidateEvidence(ctx, job.Input, r.Source); err != nil {
		return nil, err
	}
	receipts := []DraftReceipt{}
	for _, item := range job.Report.Candidates {
		if !item.RequiresHumanReview || item.Proposal.Global || item.Proposal.ProjectID != job.Input.ProjectID || item.Proposal.ID != item.ID {
			return receipts, ErrInvalid
		}
		if item.Action == "duplicate" {
			receipts = append(receipts, DraftReceipt{JobID: id, ID: item.ID, Status: "duplicate_suggestion"})
			continue
		}
		canonical, err := r.Knowledge.Memories(ctx, memory.KnowledgeQuery{ProjectID: job.Input.ProjectID, View: "all", Limit: 100})
		if err != nil {
			return receipts, err
		}
		drafts, err := r.Knowledge.Memories(ctx, memory.KnowledgeQuery{ProjectID: job.Input.ProjectID, Drafts: true, Limit: 100})
		if err != nil {
			return receipts, err
		}
		found := false
		for _, entry := range append(canonical.Entries, drafts.Entries...) {
			if entry.Global || entry.ProjectID != job.Input.ProjectID {
				return receipts, ErrInvalid
			}
			if entry.ID == item.ID {
				if !proposalMatches(entry, item.Proposal) {
					return receipts, fmt.Errorf("existing candidate differs; operator review required")
				}
				receipts = append(receipts, DraftReceipt{JobID: id, ID: entry.ID, Status: entry.Status, Revision: entry.Revision})
				found = true
				break
			}
			if normalizedStatement(entry.Statement) == normalizedStatement(item.Proposal.Statement) {
				receipts = append(receipts, DraftReceipt{JobID: id, ID: entry.ID, Status: "duplicate_suggestion", Revision: entry.Revision})
				found = true
				break
			}
		}
		if found {
			continue
		}
		entry, err := r.Knowledge.Propose(ctx, item.Proposal)
		if err != nil {
			return receipts, err
		}
		receipts = append(receipts, DraftReceipt{JobID: id, ID: entry.ID, Status: entry.Status, Revision: entry.Revision})
	}
	return receipts, nil
}
