package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cpa-session-archive/internal/embedding"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/recalltest"
)

const fixtureFingerprint = "calibration-http-fixture-r384-o96-v1"

func privateDataset() recalltest.Dataset {
	return recalltest.Dataset{
		Version: 1, Name: "PRIVATE_CALIBRATION_NAME",
		Records: []recalltest.Record{{
			ID: "PRIVATE_SOURCE_IDENTIFIER", ProjectID: "PRIVATE_PROJECT_IDENTIFIER", SessionID: "PRIVATE_SESSION_IDENTIFIER",
			Client: "codex", EventAt: "2026-10-01T08:00:00Z",
			Messages: []recalltest.Message{{Role: "assistant", Content: "The independently reviewed decision is PRIVATE_EVIDENCE_ANCHOR."}},
		}},
		Cases: []recalltest.Case{
			{ID: "PRIVATE_POSITIVE_CASE", Category: "paraphrase", Query: memory.Query{Text: "PRIVATE_POSITIVE_QUERY", ProjectID: "PRIVATE_PROJECT_IDENTIFIER", Limit: 5}, RelevantIDs: []string{"PRIVATE_SOURCE_IDENTIFIER"}, Evidence: []recalltest.Evidence{{RecordID: "PRIVATE_SOURCE_IDENTIFIER", Contains: []string{"PRIVATE_EVIDENCE_ANCHOR"}}}},
			{ID: "PRIVATE_NEGATIVE_CASE", Category: "missing", Query: memory.Query{Text: "PRIVATE_NEGATIVE_QUERY", ProjectID: "PRIVATE_PROJECT_IDENTIFIER", Limit: 5}, ExpectEmpty: true},
		},
	}
}

func writeDataset(t *testing.T, dataset recalltest.Dataset) string {
	t.Helper()
	raw, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

// This provider tests the HTTP and CLI contract, never model quality.
func fixture(t *testing.T, negativeMatch, failed bool) ([]string, string) {
	t.Helper()
	token := "PRIVATE_EMBEDDING_TOKEN_" + strings.Repeat("t", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/embed" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("unexpected embedding request contract")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request struct {
			Fingerprint string            `json:"fingerprint"`
			Purpose     embedding.Purpose `json:"purpose"`
			Texts       []string          `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Fingerprint != fixtureFingerprint {
			t.Error("unexpected embedding input contract")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if failed {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"PRIVATE_BACKEND_DIAGNOSTIC"}`))
			return
		}
		vectors := make([][]float32, len(request.Texts))
		for i, text := range request.Texts {
			vectors[i] = []float32{1, 0}
			if !negativeMatch && request.Purpose == embedding.Query && text == "PRIVATE_NEGATIVE_QUERY" {
				vectors[i] = []float32{0, 1}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"fingerprint": fixtureFingerprint, "dimensions": 2, "vectors": vectors})
	}))
	t.Cleanup(server.Close)
	directory := t.TempDir()
	tokenFile := filepath.Join(directory, "embedding.token")
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "calibration.json")
	return []string{"--corpus", writeDataset(t, privateDataset()), "--output", output, "--embedding-endpoint", server.URL, "--embedding-token-file", tokenFile, "--embedding-fingerprint", fixtureFingerprint, "--embedding-dimensions", "2"}, output
}

func runCalibration(ctx context.Context, args []string) (int, []byte, string) {
	var stdout, stderr bytes.Buffer
	code := run(ctx, args, &stdout, &stderr)
	return code, stdout.Bytes(), stderr.String()
}

func checkPrivate(t *testing.T, output string) {
	t.Helper()
	if strings.Contains(output, "PRIVATE_") {
		t.Fatal("calibration output leaked private corpus or credential data")
	}
}

func TestCalibrationWritesPrivateAggregateAndSelectsLowestPassingThreshold(t *testing.T) {
	args, output := fixture(t, false, false)
	corpus, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCalibration(context.Background(), args)
	if code != 0 || stderr != "" {
		t.Fatalf("calibration exit %d, stderr %q", code, stderr)
	}
	var report recalltest.CalibrationReport
	if err := json.Unmarshal(stdout, &report); err != nil {
		t.Fatal(err)
	}
	if report.SelectedMinimum == nil || *report.SelectedMinimum != .78 || !report.CalibrationPassed() {
		t.Fatal("calibration did not select the lowest passing declared threshold")
	}
	checkPrivate(t, string(stdout)+stderr)
	data, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(data, stdout) {
		t.Fatal("aggregate file did not preserve stdout report", err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("aggregate report is not private", err)
	}
	after, err := os.ReadFile(args[1])
	if err != nil || !bytes.Equal(corpus, after) {
		t.Fatal("calibration modified its dataset", err)
	}
}

func TestCalibrationGateMissRetainsAggregateWithoutSelectingPolicy(t *testing.T) {
	args, output := fixture(t, true, false)
	args = append(args, "--thresholds", "0.80,0.90")
	code, stdout, stderr := runCalibration(context.Background(), args)
	if code != 3 || !strings.Contains(stderr, "no threshold") {
		t.Fatalf("gate miss exit %d, stderr %q", code, stderr)
	}
	var report recalltest.CalibrationReport
	if err := json.Unmarshal(stdout, &report); err != nil {
		t.Fatal(err)
	}
	if report.SelectedMinimum != nil || report.CalibrationPassed() {
		t.Fatal("gate miss became a selected threshold")
	}
	data, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(data, stdout) {
		t.Fatal("gate miss discarded or changed the aggregate", err)
	}
	checkPrivate(t, string(data)+stderr)
}

func TestCalibrationProviderFailureAndCancellationAreExecutionFailures(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider", true: "canceled"}[cancel], func(t *testing.T) {
			args, output := fixture(t, false, !cancel)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			if cancel {
				stop()
			}
			code, stdout, stderr := runCalibration(ctx, args)
			if code != 1 || len(stdout) != 0 || !strings.Contains(stderr, "isolated calibration failed") {
				t.Fatalf("runtime failure exit %d, stdout bytes %d, stderr %q", code, len(stdout), stderr)
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("runtime failure left a successful-looking report")
			}
			checkPrivate(t, stderr)
		})
	}
}

func TestCalibrationRejectsUnavailablePrivateInputsWithoutLeakingOrReporting(t *testing.T) {
	for _, kind := range []string{"missing corpus", "malformed corpus", "no negatives", "no positives", "public credential", "invalid credential"} {
		t.Run(kind, func(t *testing.T) {
			args, output := fixture(t, false, false)
			switch kind {
			case "missing corpus":
				args[1] = filepath.Join(t.TempDir(), "PRIVATE_MISSING_CORPUS")
			case "malformed corpus":
				if err := os.WriteFile(args[1], []byte(`{"PRIVATE_MALFORMED_INPUT":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "no negatives":
				dataset := privateDataset()
				dataset.Cases = dataset.Cases[:1]
				args[1] = writeDataset(t, dataset)
			case "no positives":
				dataset := privateDataset()
				dataset.Cases = dataset.Cases[1:]
				args[1] = writeDataset(t, dataset)
			case "public credential":
				if err := os.Chmod(args[7], 0644); err != nil {
					t.Fatal(err)
				}
			case "invalid credential":
				if err := os.WriteFile(args[7], []byte("PRIVATE_SHORT_TOKEN"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, stdout, stderr := runCalibration(context.Background(), args)
			if code != 1 || len(stdout) != 0 || stderr == "" {
				t.Fatalf("invalid private input exit %d, stdout bytes %d, stderr %q", code, len(stdout), stderr)
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("invalid private input left a calibration report")
			}
			checkPrivate(t, stderr)
		})
	}
}

func TestCalibrationNeverOverwritesReportsOrFollowsOutputSymlinks(t *testing.T) {
	for _, kind := range []string{"existing", "symlink-existing", "symlink-missing"} {
		t.Run(kind, func(t *testing.T) {
			args, output := fixture(t, false, false)
			args = append(args, "--thresholds", "0.8")
			target := output
			if kind != "existing" {
				target = filepath.Join(filepath.Dir(output), "target.json")
			}
			const sentinel = "PRIVATE_EXISTING_REPORT"
			if kind != "symlink-missing" {
				if err := os.WriteFile(target, []byte(sentinel), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "existing" {
				if err := os.Symlink(target, output); err != nil {
					t.Fatal(err)
				}
			}
			code, stdout, stderr := runCalibration(context.Background(), args)
			if code != 1 || len(stdout) != 0 || !strings.Contains(stderr, "report could not be created") {
				t.Fatalf("output conflict exit %d, stdout bytes %d, stderr %q", code, len(stdout), stderr)
			}
			checkPrivate(t, stderr)
			if kind == "symlink-missing" {
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatal("dangling report symlink target was created")
				}
			} else {
				data, err := os.ReadFile(target)
				if err != nil || string(data) != sentinel {
					t.Fatal("existing report contents changed", err)
				}
			}
			if kind != "existing" {
				link, err := os.Readlink(output)
				if err != nil || link != target {
					t.Fatal("report symlink changed", err)
				}
			}
		})
	}
}

func TestCalibrationConfigurationIsValidatedBeforeOpeningPrivateFiles(t *testing.T) {
	base := []string{"--corpus", "/unavailable/PRIVATE_CORPUS", "--output", "/unavailable/PRIVATE_OUTPUT", "--embedding-endpoint", "http://127.0.0.1:1", "--embedding-token-file", "/unavailable/PRIVATE_TOKEN", "--embedding-fingerprint", fixtureFingerprint, "--embedding-dimensions", "2"}
	tests := map[string][]string{}
	for _, flag := range []string{"--corpus", "--output", "--embedding-endpoint", "--embedding-token-file", "--embedding-fingerprint", "--embedding-dimensions"} {
		args := append([]string(nil), base...)
		for i := 0; i < len(args); i += 2 {
			if args[i] == flag {
				args = append(args[:i], args[i+2:]...)
				break
			}
		}
		tests["missing "+flag] = args
	}
	for _, value := range []string{"", "NaN", "Inf", "-Inf", "-0.1", "1.1", "0.8,0.8", "0.9,0.8", "0.8,", ",0.8", "private-invalid-threshold"} {
		tests["thresholds "+value] = append(append([]string(nil), base...), "--thresholds="+value)
	}
	tests["too many thresholds"] = append(append([]string(nil), base...), "--thresholds="+strings.Repeat("0.8,", 100)+"0.9")
	for _, value := range []string{"NaN", "Inf", "-Inf", "0.79", "-1", "1.01"} {
		tests["recall "+value] = append(append([]string(nil), base...), "--min-recall-at-5="+value)
	}
	for _, args := range [][]string{{"--timeout", "0"}, {"--timeout", "-1s"}, {"--embedding-dimensions", "4097"}, {"--embedding-dimensions", "0"}, {"--embedding-endpoint", "https://127.0.0.1"}, {"--embedding-endpoint", "http://example.invalid"}, {"--embedding-endpoint", "http://user:PRIVATE_TOKEN@127.0.0.1"}, {"--embedding-fingerprint", "PRIVATE_FINGERPRINT\n"}, {"--mode", "hybrid"}, {"PRIVATE_EXTRA_ARGUMENT"}} {
		tests[strings.Join(args, " ")] = append(append([]string(nil), base...), args...)
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runCalibration(context.Background(), args)
			if code != 2 || len(stdout) != 0 || !strings.Contains(stderr, "usage:") {
				t.Fatalf("configuration exit %d, stdout bytes %d, stderr %q", code, len(stdout), stderr)
			}
			checkPrivate(t, stderr)
		})
	}
}

func TestThresholdListDefaultsAndBounds(t *testing.T) {
	got, err := parseThresholds(defaultThresholds)
	want := []float64{.78, .80, .82, .84, .86, .88, .90, .92, .94, .96}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("default thresholds changed", err)
	}
	got, err = parseThresholds(" 0 , 0.8 , 1 ")
	if err != nil || !reflect.DeepEqual(got, []float64{0, .8, 1}) {
		t.Fatal("valid inclusive thresholds rejected", err)
	}
}
