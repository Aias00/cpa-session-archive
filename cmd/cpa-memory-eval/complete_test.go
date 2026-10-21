package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-session-archive/internal/recalltest"
)

func TestCompleteGateRejectsTruncatedSuccessAndRetainsReport(t *testing.T) {
	dataset := privateDataset()
	for i := 0; i < 6; i++ {
		record := dataset.Records[0]
		record.ID = fmt.Sprintf("PRIVATE_EXTRA_%d", i)
		record.Messages = []recalltest.Message{{Role: "assistant", Content: fmt.Sprintf("privateLookupKeyword unrelated reminder %d", i)}}
		dataset.Records = append(dataset.Records, record)
	}
	corpus := writeDataset(t, dataset)
	for _, required := range []bool{false, true} {
		output := filepath.Join(t.TempDir(), "report.json")
		flags := []string{"--output", output}
		want := 0
		if required {
			flags = append(flags, "--require-complete")
			want = 3
		}
		code, stdout, stderr := runEval(t, corpus, flags...)
		if code != want || (required && !strings.Contains(stderr, "complete-retrieval gate")) {
			t.Fatalf("complete gate returned %d, stderr %q", code, stderr)
		}
		r := decodeReport(t, stdout)
		if r.Metrics.TruncatedCases != 1 || r.CompletePassed() || !r.SafetyPassed() {
			t.Fatal("truncation did not remain independent of citation/scope safety")
		}
		data, err := os.ReadFile(output)
		if err != nil || !bytes.Equal(data, stdout) {
			t.Fatal("complete gate lost the aggregate report", err)
		}
		assertNoPrivateText(t, string(stdout)+stderr, false)
		assertPrivateMode(t, output)
	}
}
