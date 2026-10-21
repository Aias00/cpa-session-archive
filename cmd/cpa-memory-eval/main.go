// cpa-memory-eval measures frozen recall labels in disposable private databases.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cpa-session-archive/internal/embedding"
	"cpa-session-archive/internal/recallruntime"
	"cpa-session-archive/internal/recalltest"
)

// Files are exclusive and private: existing reports are never overwritten.
func writeReport(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(value)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("cpa-memory-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	corpus := flags.String("corpus", "", "frozen evaluation JSON (synthetic or owner-controlled private)")
	output := flags.String("output", "", "new aggregate report file (0600); aggregate JSON also goes to stdout")
	details := flags.String("case-report", "", "optional new private per-case metric file (0600), never queries or snippets")
	mode := flags.String("mode", "", "optional literal, diverse or hybrid retrieval override; default preserves fixture queries")
	embedEndpoint := flags.String("embedding-endpoint", "", "explicit literal-loopback local embedding origin")
	embedToken := flags.String("embedding-token-file", "", "private dedicated local embedding credential")
	embedFingerprint := flags.String("embedding-fingerprint", "", "pinned local model and input-policy fingerprint")
	embedDimensions := flags.Int("embedding-dimensions", 384, "pinned embedding dimensions")
	semanticMinimum := flags.Float64("semantic-minimum", .8, "minimum semantic cosine (0 to 1), fixed before evaluation")
	repeats := flags.Int("repetitions", 1, "query repetitions for isolated latency sampling (1 to 100)")
	timeout := flags.Duration("timeout", 2*time.Minute, "total isolated evaluation deadline")
	minimum := flags.Float64("min-recall-at-5", -1, "optional mean recall gate (0 to 1); disabled by default")
	requireEmpty := flags.Bool("require-empty-pass", false, "require every empty-expected case to return no evidence")
	requireComplete := flags.Bool("require-complete", false, "reject truncated retrieval or incomplete semantic coverage")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *corpus == "" || *timeout <= 0 || *repeats < 1 || *repeats > 100 || math.IsNaN(*minimum) || math.IsInf(*minimum, 0) || *minimum < -1 || *minimum > 1 || (*minimum < 0 && *minimum != -1) || (*output != "" && *output == *details) || (*mode != "" && *mode != "literal" && *mode != "diverse" && *mode != "hybrid") || math.IsNaN(*semanticMinimum) || math.IsInf(*semanticMinimum, 0) || *semanticMinimum < 0 || *semanticMinimum > 1 || *embedDimensions < 1 || *embedDimensions > 4096 || (*embedEndpoint == "" && (*embedToken != "" || *embedFingerprint != "")) || (*embedEndpoint != "" && (*embedToken == "" || *embedFingerprint == "")) {
		fmt.Fprintln(stderr, "usage: cpa-memory-eval --corpus dataset.json [--output new-report.json] [--case-report new-private-cases.json] [--repetitions 3] [--mode literal|diverse|hybrid] [--require-empty-pass] [--require-complete] [--embedding-endpoint http://127.0.0.1:PORT --embedding-token-file private.token --embedding-fingerprint PIN]")
		return 2
	}
	file, err := os.Open(*corpus)
	if err != nil {
		fmt.Fprintln(stderr, "evaluation dataset unavailable")
		return 1
	}
	dataset, err := recalltest.Load(file)
	_ = file.Close()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	deadline, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	options := recalltest.Options{Repetitions: *repeats, Mode: *mode}
	if *embedEndpoint != "" {
		token, err := recallruntime.ReadTokenFile(*embedToken)
		if err != nil {
			fmt.Fprintln(stderr, "local embedding credential unavailable")
			return 1
		}
		client, err := embedding.NewHTTP(*embedEndpoint, token, *embedFingerprint, *embedDimensions)
		if err != nil {
			fmt.Fprintln(stderr, "invalid local embedding configuration")
			return 1
		}
		defer client.Close()
		options.Semantic = &recalltest.SemanticOptions{Embedder: client, Minimum: *semanticMinimum}
	}
	report, err := recalltest.Run(deadline, dataset, options)
	if err != nil {
		fmt.Fprintln(stderr, "isolated evaluation failed")
		return 1
	}
	if *output != "" {
		if writeReport(*output, report) != nil {
			fmt.Fprintln(stderr, "aggregate report could not be created")
			return 1
		}
	}
	if *details != "" {
		if writeReport(*details, report.Cases) != nil {
			fmt.Fprintln(stderr, "private case report could not be created")
			return 1
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(report) != nil {
		return 1
	}
	if !report.SafetyPassed() {
		fmt.Fprintln(stderr, "evaluation failed citation, scope or forbidden-source gates")
		return 3
	}
	if *requireComplete && !report.CompletePassed() {
		fmt.Fprintln(stderr, "evaluation failed the requested complete-retrieval gate")
		return 3
	}
	if *requireEmpty && report.Metrics.EmptyPassCases != report.Metrics.Unanswerable {
		fmt.Fprintln(stderr, "evaluation failed the requested empty-result gate")
		return 3
	}
	if *minimum >= 0 && report.Metrics.RecallAt5 < *minimum {
		fmt.Fprintln(stderr, "evaluation fell below the requested recall gate")
		return 3
	}
	return 0
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
