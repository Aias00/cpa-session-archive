// cpa-memory-extract runs explicit batches; it never configures a client or
// activates canonical knowledge. Reports may contain private retained evidence.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"cpa-session-archive/internal/archivefeed"
	"cpa-session-archive/internal/knowledge"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/memoryextract"
	"cpa-session-archive/internal/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "memory extraction:", err)
		os.Exit(1)
	}
}

func privateToken(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot read token file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("token file must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 || len(strings.TrimSpace(string(raw))) < 16 {
		return "", fmt.Errorf("invalid token file")
	}
	return strings.TrimSpace(string(raw)), nil
}

// Exclusive creation preserves existing files. No private report is printed by
// default; stdout contains only job IDs, counts and operational status.
func output(path string, value any, summary any, stdout io.Writer) error {
	if path == "" {
		return json.NewEncoder(stdout).Encode(summary)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create private output file; choose a new path")
	}
	if err := json.NewEncoder(f).Encode(value); err != nil {
		f.Close()
		return fmt.Errorf("cannot write private output")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(summary)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("choose scan, run, list, status, show, pause, resume, resolve or stage")
	}
	action := args[0]
	flags := flag.NewFlagSet("cpa-memory-extract "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	state := flags.String("state", "", "dedicated private extraction task database (never archive or projection)")
	project := flags.String("project-id", "", "exact project identity; required for evidence operations")
	session := flags.String("session-id", "", "optional exact session selection within that project")
	bundle := flags.String("bundle", "", "offline source-snapshot bundle JSON, at most 32 MiB")
	archiveEndpoint := flags.String("archive-endpoint", "", "explicit loopback archive feed origin, alternative to bundle")
	archiveToken := flags.String("archive-token-file", "", "private dedicated archive feed token")
	providerEndpoint := flags.String("provider-endpoint", "", "explicit loopback OpenAI-compatible model origin")
	model := flags.String("model", "", "explicit extraction model ID")
	modelToken := flags.String("model-token-file", "", "private model credential file")
	root := flags.String("project-root", "", "registered canonical project Git root for comparison or draft staging")
	pages := flags.Int("scan-pages", 1, "bounded source-discovery pages per invocation")
	maxJobs := flags.Int("max-jobs", 1, "maximum model jobs per invocation, 1..100")
	jobID := flags.String("job-id", "", "exact extraction job ID")
	attempt := flags.String("expected-attempt", "", "exact uncertain attempt token inspected by the operator")
	resolution := flags.String("resolution", "", "operator reconciliation: abandon or retry")
	confirmed := flags.Bool("confirm-uncertain", false, "operator confirms uncertain prior call was inspected; retry may duplicate billing")
	outputPath := flags.String("output", "", "new private JSON report path; stdout contains aggregate metadata only")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *state == "" {
		return fmt.Errorf("state is required and positional arguments are not supported")
	}
	allowed := map[string]bool{"scan": true, "run": true, "list": true, "status": true, "show": true, "pause": true, "resume": true, "resolve": true, "stage": true}
	if !allowed[action] {
		return fmt.Errorf("unknown extraction action")
	}
	jobs, err := memoryextract.OpenJobs(*state, memoryextract.DefaultLimits())
	if err != nil {
		return err
	}
	defer jobs.Close()
	switch action {
	case "list":
		items, err := jobs.List(ctx, 100)
		if err != nil {
			return err
		}
		metadata := []map[string]any{}
		for _, job := range items {
			metadata = append(metadata, map[string]any{"id": job.ID, "state": job.State, "attempts": job.Attempts, "next_attempt": job.NextAttempt})
		}
		return json.NewEncoder(stdout).Encode(metadata)
	case "status":
		s, err := jobs.Status(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(s)
	case "pause", "resume":
		if err := jobs.Pause(ctx, action == "pause"); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"paused": action == "pause"})
	case "resolve":
		if err := jobs.ResolveUncertain(ctx, *jobID, *attempt, *resolution, *confirmed); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]string{"job_id": *jobID, "resolution": *resolution})
	case "show":
		if *outputPath == "" {
			return fmt.Errorf("show requires a new private output path")
		}
		job, err := jobs.Get(ctx, *jobID)
		if err != nil {
			return err
		}
		return output(*outputPath, job, map[string]string{"job_id": job.ID, "state": job.State}, stdout)
	}
	if *project == "" {
		return fmt.Errorf("project-id is required")
	}
	selection := memoryextract.Selection{ProjectID: *project, SessionID: *session}
	var feed storage.MemoryFeed
	var archiveID string
	if *bundle != "" {
		if *archiveEndpoint != "" || *archiveToken != "" {
			return fmt.Errorf("choose bundle or archive feed")
		}
		f, err := os.Open(*bundle)
		if err != nil {
			return fmt.Errorf("cannot open source bundle")
		}
		local, err := memoryextract.DecodeBundle(f)
		f.Close()
		if err != nil {
			return err
		}
		feed = local
		identity, err := local.MemoryIdentity(ctx)
		if err != nil {
			return err
		}
		archiveID = identity.ArchiveID
	} else {
		if *archiveEndpoint == "" || *archiveToken == "" {
			return fmt.Errorf("bundle or explicit archive endpoint and token are required")
		}
		token, err := privateToken(*archiveToken)
		if err != nil {
			return err
		}
		client, err := archivefeed.NewClient(*archiveEndpoint, token)
		if err != nil {
			return err
		}
		defer client.Close()
		identity, err := client.MemoryIdentity(ctx)
		if err != nil {
			return err
		}
		feed = client
		archiveID = identity.ArchiveID
	}
	source, err := memoryextract.NewSourceReader(feed, archiveID, selection)
	if err != nil {
		return err
	}
	runner := &memoryextract.Runner{Jobs: jobs, Feed: feed, Source: source, Selection: selection, Limits: memoryextract.DefaultLimits()}
	if *root != "" {
		library, err := knowledge.New(knowledge.Config{Projects: map[string]string{*project: *root}, Scope: memory.Scope{ProjectID: *project}, Source: source})
		if err != nil {
			return err
		}
		runner.Knowledge = library
	}
	if action == "stage" {
		if runner.Knowledge == nil {
			return fmt.Errorf("stage requires the canonical project-root")
		}
		receipts, err := runner.Stage(ctx, *jobID)
		if err != nil {
			return err
		}
		return output(*outputPath, receipts, map[string]any{"job_id": *jobID, "draft_receipts": receipts, "activated": false}, stdout)
	}
	if *providerEndpoint == "" || *model == "" || *modelToken == "" {
		return fmt.Errorf("explicit provider-endpoint, model and model-token-file are required; no implicit model calls")
	}
	token, err := privateToken(*modelToken)
	if err != nil {
		return err
	}
	provider, err := memoryextract.NewHTTPProvider(memoryextract.HTTPConfig{Endpoint: *providerEndpoint, Token: token, Model: *model})
	if err != nil {
		return err
	}
	runner.Provider = provider
	page, registered, err := runner.Discover(ctx, *pages)
	if err != nil {
		return err
	}
	ids := []string{}
	for _, job := range registered {
		ids = append(ids, job.ID)
	}
	if action == "scan" {
		return output(*outputPath, page, map[string]any{"jobs": ids, "caught_up": page.CaughtUp, "complete": page.Complete, "issues": page.Issues, "model_calls": 0}, stdout)
	}
	reports, err := runner.Process(ctx, *maxJobs)
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"registered_jobs": ids, "reports_completed": len(reports), "activated": false})
		return err
	}
	candidates, rejected := 0, 0
	for _, report := range reports {
		candidates += len(report.Candidates)
		rejected += len(report.Rejected)
	}
	return output(*outputPath, reports, map[string]any{"jobs": ids, "reports": len(reports), "candidates": candidates, "rejected": rejected, "caught_up": page.CaughtUp, "complete": page.Complete, "activated": false}, stdout)
}
