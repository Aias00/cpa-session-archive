// cpa-memory-calibrate selects a candidate threshold using an independent corpus.
// Its aggregate report is calibration evidence, never a rollout policy.
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"cpa-session-archive/internal/embedding"
	"cpa-session-archive/internal/recallruntime"
	"cpa-session-archive/internal/recalltest"
)

const defaultThresholds = "0.78,0.80,0.82,0.84,0.86,0.88,0.90,0.92,0.94,0.96"

func parseThresholds(raw string) ([]float64, error) {
	parts := strings.Split(raw, ",")
	if len(parts) > 100 {
		return nil, fmt.Errorf("at most 100 calibration thresholds are permitted")
	}
	thresholds := make([]float64, len(parts))
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 || (i > 0 && value <= thresholds[i-1]) {
			return nil, fmt.Errorf("thresholds must be finite, distinct, ascending values from 0 to 1")
		}
		thresholds[i] = value
	}
	return thresholds, nil
}

// Existing reports and symlinks are never overwritten, including on gate misses.
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
	flags := flag.NewFlagSet("cpa-memory-calibrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	corpus := flags.String("corpus", "", "independent owner-controlled calibration corpus, never the frozen baseline or held-out gate")
	output := flags.String("output", "", "required new aggregate calibration report file (0600)")
	thresholdList := flags.String("thresholds", defaultThresholds, "predeclared distinct ascending minimum cosines from 0 to 1")
	minimumRecall := flags.Float64("min-recall-at-5", .8, "minimum mean positive recall, from 0.8 to 1")
	timeout := flags.Duration("timeout", 10*time.Minute, "total isolated calibration deadline")
	embedEndpoint := flags.String("embedding-endpoint", "", "required literal-loopback local embedding origin")
	embedToken := flags.String("embedding-token-file", "", "required private dedicated local embedding credential file")
	embedFingerprint := flags.String("embedding-fingerprint", "", "required pinned local model and input-policy fingerprint")
	embedDimensions := flags.Int("embedding-dimensions", 0, "required pinned embedding dimensions")
	usage := func() int {
		fmt.Fprintln(stderr, "usage: cpa-memory-calibrate --corpus independent-calibration.json --output new-calibration-report.json --embedding-endpoint http://127.0.0.1:PORT --embedding-token-file private.token --embedding-fingerprint PIN --embedding-dimensions N [--thresholds 0.80,0.84,0.88] [--min-recall-at-5 0.8] [--timeout 10m]")
		return 2
	}
	if flags.Parse(args) != nil || flags.NArg() != 0 || *corpus == "" || *output == "" || *timeout <= 0 || *embedEndpoint == "" || *embedToken == "" || *embedFingerprint == "" || *embedDimensions < 1 || *embedDimensions > 4096 || math.IsNaN(*minimumRecall) || math.IsInf(*minimumRecall, 0) || *minimumRecall < .8 || *minimumRecall > 1 {
		return usage()
	}
	thresholds, err := parseThresholds(*thresholdList)
	if err != nil {
		return usage()
	}
	// Validate origin and identity before opening corpus or credential files.
	validation, err := embedding.NewHTTP(*embedEndpoint, strings.Repeat("x", 32), *embedFingerprint, *embedDimensions)
	if err != nil {
		return usage()
	}
	_ = validation.Close()
	file, err := os.Open(*corpus)
	if err != nil {
		fmt.Fprintln(stderr, "calibration dataset unavailable")
		return 1
	}
	dataset, err := recalltest.Load(file)
	_ = file.Close()
	if err != nil {
		fmt.Fprintln(stderr, "invalid calibration dataset")
		return 1
	}
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
	deadline, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	report, err := recalltest.Calibrate(deadline, dataset, recalltest.CalibrationOptions{Embedder: client, Thresholds: thresholds, MinimumRecall: *minimumRecall})
	if err != nil {
		fmt.Fprintln(stderr, "isolated calibration failed")
		return 1
	}
	if writeReport(*output, report) != nil {
		fmt.Fprintln(stderr, "aggregate calibration report could not be created")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(report) != nil {
		return 1
	}
	if report.SelectedMinimum == nil || !report.CalibrationPassed() {
		fmt.Fprintln(stderr, "calibration found no threshold satisfying all required gates")
		return 3
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
