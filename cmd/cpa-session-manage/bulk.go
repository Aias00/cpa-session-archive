package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"cpa-session-archive/internal/archive"
)

func validCLIText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func validBrowseView(view string) bool {
	switch view {
	case "all", "recent", "inactive", "main", "subagent", "system", "prewarm", "unassigned":
		return true
	default:
		return false
	}
}

// A preview freezes every matching session, regardless of list pagination. Apply
// and undo perform one explicit bounded step by default. The optional loop only
// advances after a successful response and never retries an ambiguous mutation.
func runBulk(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	command := args[0]
	flags := flag.NewFlagSet("cpa-session-manage "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = io.WriteString(stderr, "Usage: cpa-session-manage "+command+" [options]\nPreview freezes all matches. Apply/undo performs one step of at most 100 sessions by default. --until-done continues only after confirmed steps. Inspect bulk-show after errors; explicitly resume remaining work.\n")
		flags.PrintDefaults()
	}
	endpoint := flags.String("endpoint", "", "CPA management origin; HTTPS or literal-loopback HTTP")
	tokenFile := flags.String("token-file", "", "private file containing the CPA management credential")
	var plan archive.SessionBulkPlanRequest
	var jobID, archiveAction, starAction, starred string
	var facets repeatedFlag
	var addTags, removeTags repeatedFlag
	limit := 100
	var untilDone bool
	if command == "bulk-preview" {
		flags.StringVar(&plan.Selection.Query, "q", "", "literal search of session labels, summary, notes and tags")
		flags.StringVar(&plan.Selection.Tag, "tag", "", "exact existing user tag")
		flags.StringVar(&plan.Selection.Archived, "archived", "exclude", "selection: exclude, only or all")
		flags.StringVar(&starred, "starred", "false", "selection: true selects favorites only")
		flags.StringVar(&plan.Selection.View, "view", "all", "all, recent, inactive, main, subagent, system, prewarm or unassigned")
		flags.IntVar(&plan.Selection.InactiveDays, "inactive-days", 30, "inactive view threshold in days, 1..3650")
		flags.Var(&facets, "filter", "exact facet NAME=VALUE; repeat for AND")
		flags.StringVar(&archiveAction, "archive", "", "action: true archives; false restores")
		flags.StringVar(&starAction, "star", "", "action: true favorites; false removes favorite")
		flags.Var(&addTags, "add-tag", "action: add a user tag; repeat for several")
		flags.Var(&removeTags, "remove-tag", "action: remove a user tag; repeat for several")
		flags.BoolVar(&plan.Action.IncludeStarred, "include-starred", false, "allow archiving favorite sessions")
		flags.BoolVar(&plan.Action.IncludeEdited, "include-edited", false, "allow archiving sessions with user-maintained metadata")
	} else {
		flags.StringVar(&jobID, "job-id", "", "exact preview job ID; omit for bulk-show to list jobs")
		if command != "bulk-show" {
			flags.IntVar(&limit, "limit", 100, "one explicit step size, 1..100; repeat only after inspecting progress")
			flags.BoolVar(&untilDone, "until-done", false, "continue confirmed bounded steps to completion; stop on any error")
		}
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	changed := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { changed[f.Name] = true })
	if command == "bulk-preview" {
		if !validBrowseView(plan.Selection.View) || plan.Selection.InactiveDays < 1 || plan.Selection.InactiveDays > 3650 || (plan.Selection.Archived != "exclude" && plan.Selection.Archived != "only" && plan.Selection.Archived != "all") || (starred != "true" && starred != "false") || !validCLIText(plan.Selection.Query) || !validCLIText(plan.Selection.Tag) {
			return errors.New("invalid bulk selection")
		}
		plan.Selection.Starred = starred == "true"
		plan.Selection.Filters = map[string]string{}
		reserved := map[string]bool{"q": true, "tag": true, "archived": true, "starred": true, "view": true, "inactive_days": true, "limit": true, "offset": true}
		for _, facet := range facets {
			name, value, ok := strings.Cut(facet, "=")
			if !ok || name == "" || value == "" || reserved[name] || !validCLIText(name) || !validCLIText(value) {
				return errors.New("filter must be a unique facet NAME=VALUE without reserved query names")
			}
			if _, duplicate := plan.Selection.Filters[name]; duplicate {
				return errors.New("filter must be a unique facet NAME=VALUE")
			}
			plan.Selection.Filters[name] = value
		}
		for name, value := range map[string]string{"archive": archiveAction, "star": starAction} {
			if !changed[name] {
				continue
			}
			if value != "true" && value != "false" {
				return errors.New(name + " must be true or false")
			}
			boolean := value == "true"
			if name == "archive" {
				plan.Action.Archived = &boolean
			} else {
				plan.Action.Starred = &boolean
			}
		}
		plan.Action.AddTags, plan.Action.RemoveTags = []string(addTags), []string(removeTags)
		if plan.Action.Archived == nil && plan.Action.Starred == nil && len(addTags) == 0 && len(removeTags) == 0 {
			return errors.New("specify archive, star, add-tag or remove-tag action")
		}
		for _, tag := range append(append([]string{}, addTags...), removeTags...) {
			if !validCLIText(tag) || strings.TrimSpace(tag) == "" {
				return errors.New("invalid action tag")
			}
		}
	} else if (command != "bulk-show" && jobID == "") || (changed["job-id"] && (jobID == "" || len(jobID) > 4096 || !validCLIText(jobID))) || limit < 1 || limit > 100 {
		return errors.New("valid job-id and step limit 1..100 required")
	}
	client, err := newClient(*endpoint, *tokenFile)
	if err != nil {
		return err
	}
	defer client.transport.CloseIdleConnections()
	query := url.Values{}
	path, method := "/session-bulk", http.MethodGet
	var input any
	var output any = &archive.SessionBulkJob{}
	switch command {
	case "bulk-preview":
		path, method, input = "/session-bulk-preview", http.MethodPost, plan
	case "bulk-show":
		if jobID == "" {
			output = &struct {
				Jobs []archive.SessionBulkJob `json:"jobs"`
			}{}
		} else {
			query.Set("id", jobID)
		}
	case "bulk-apply", "bulk-undo":
		path, method = "/session-"+command, http.MethodPost
		input = struct {
			ID    string `json:"id"`
			Limit int    `json:"limit"`
		}{jobID, limit}
	}
	if err = client.request(ctx, method, path, query, input, output); err != nil {
		return err
	}
	if untilDone {
		job := output.(*archive.SessionBulkJob)
		for steps := 1; ; steps++ {
			if job.ID != jobID {
				return errors.New("invalid bulk job response; inspect bulk-show before resuming")
			}
			if command == "bulk-apply" && job.Status == "complete" || command == "bulk-undo" && job.Status == "undone" {
				break
			}
			remaining := job.Pending
			expectedStatus := "running"
			if command == "bulk-undo" {
				remaining, expectedStatus = job.Applied, "undoing"
			}
			if remaining <= 0 || job.Status != expectedStatus {
				return errors.New("bulk job made no progress; inspect bulk-show before resuming")
			}
			if steps >= 10000 {
				return errors.New("bulk step safety limit reached; inspect bulk-show before explicitly resuming")
			}
			var next archive.SessionBulkJob
			if err = client.request(ctx, method, path, query, input, &next); err != nil {
				return err
			}
			nextRemaining := next.Pending
			if command == "bulk-undo" {
				nextRemaining = next.Applied
			}
			if nextRemaining >= remaining {
				return errors.New("bulk job made no progress; inspect bulk-show before resuming")
			}
			*job = next
		}
	}
	return json.NewEncoder(stdout).Encode(output)
}
