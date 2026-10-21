package memoryextract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memory"
)

// The opt-in public corpus keeps complete response bodies for offline diagnosis.
// Credentials and headers are never captured. Each invocation has a new private
// directory; malformed output can be investigated without another model call.
type publicCaptureTransport struct {
	next   http.RoundTripper
	dir    string
	prefix string
	count  int
}

func (p *publicCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := p.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, providerResponseLimit+1))
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	p.count++
	prefix := p.prefix
	if prefix == "" {
		prefix = "public-response"
	}
	f, err := os.OpenFile(filepath.Join(p.dir, fmt.Sprintf("%s-%d.json", prefix, p.count)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	return response, nil
}

type publicComparison struct{ entries []memory.MemoryEntry }

func (p publicComparison) Memories(context.Context, memory.KnowledgeQuery) (memory.KnowledgeResult, error) {
	return memory.KnowledgeResult{Authority: "synthetic-reviewed-fixture", Entries: p.entries}, nil
}
func (publicComparison) Propose(context.Context, memory.MemoryProposal) (memory.MemoryEntry, error) {
	return memory.MemoryEntry{}, ErrInvalid
}

func TestRealProviderPublicRelations(t *testing.T) {
	endpoint := os.Getenv("CPA_EXTRACT_ACCEPTANCE_ENDPOINT")
	if endpoint == "" {
		t.Skip("explicit real-model synthetic acceptance is not configured")
	}
	dir, model, path := os.Getenv("CPA_EXTRACT_ACCEPTANCE_DIR"), os.Getenv("CPA_EXTRACT_ACCEPTANCE_MODEL"), os.Getenv("CPA_EXTRACT_ACCEPTANCE_TOKEN_FILE")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private credential file required")
	}
	token, err := os.ReadFile(path)
	if err != nil || len(token) > 4096 {
		t.Fatal("cannot read explicit credential")
	}
	provider, err := NewHTTPProvider(HTTPConfig{Endpoint: endpoint, Token: strings.TrimSpace(string(token)), Model: model})
	if err != nil {
		t.Fatal("invalid explicit provider configuration")
	}
	configured := provider.(*httpProvider)
	configured.client.Transport = &publicCaptureTransport{next: configured.client.Transport, dir: dir, prefix: "public-relations-response"}
	texts := []string{
		"Correct the metrics fact: its port is 19002, not 19001; the earlier record was mistaken.",
		"Replace the retry policy: use a durable outbox instead of an in-memory buffer from now on.",
		"Unresolved storage conflict: another maintainer reports PostgreSQL, contradicting the SQLite record. Neither side is resolved.",
		"The API must continue listening only on loopback.",
		"Add a new requirement: export batch size must be 29 records.",
	}
	messages := []map[string]string{}
	for _, text := range texts {
		messages = append(messages, map[string]string{"role": "user", "content": text})
	}
	payload, _ := json.Marshal(map[string]any{"messages": messages})
	record := archive.Record{RequestID: "public-relations", SessionID: "relations", Facets: map[string][]string{"project.id": {"relation-fixture"}}, OriginalRequest: payload, Response: []byte(`{"choices":[]}`)}
	revision, err := sourceRevision(record)
	if err != nil {
		t.Fatal(err)
	}
	bundle := Bundle{ArchiveID: strings.Repeat("b", 64), Watermark: 1, Sources: []archive.SourceSnapshot{{Record: record, Revision: revision, Sequence: 1}}}
	feed, err := NewBundleFeed(bundle)
	if err != nil {
		t.Fatal(err)
	}
	selection := Selection{ProjectID: "relation-fixture", SessionID: "relations"}
	source, err := NewSourceReader(feed, bundle.ArchiveID, selection)
	if err != nil {
		t.Fatal(err)
	}
	prior := []struct{ id, statement string }{{"old-port", "The metrics server binds to port 19001."}, {"old-retry", "The retry policy uses an in-memory buffer."}, {"old-storage", "The storage engine is SQLite."}, {"old-access", "The API listens only on loopback."}}
	entries := []memory.MemoryEntry{}
	for i, p := range prior {
		entries = append(entries, memory.MemoryEntry{ID: p.id, Title: p.id, Kind: "fact", ProjectID: selection.ProjectID, Status: "verified", Statement: p.statement, Revision: strings.Repeat(string(rune('a'+i)), 64)})
	}
	jobs, err := OpenJobs(filepath.Join(dir, "public-corpus-jobs.sqlite"), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	runner := Runner{Jobs: jobs, Feed: feed, Source: source, Provider: provider, Knowledge: publicComparison{entries: entries}, Selection: selection, Limits: DefaultLimits()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, registered, err := runner.Discover(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 {
		t.Fatal("relations fixture must fit one bounded call", len(registered))
	}
	reports, runErr := runner.Process(ctx, 1)
	seen := map[string]bool{}
	for _, report := range reports {
		for _, candidate := range report.Candidates {
			switch candidate.Action {
			case "new":
				if strings.Contains(candidate.Proposal.Statement, "29") {
					seen["new"] = true
				}
			case "duplicate":
				for _, target := range candidate.DuplicateOf {
					if target.ID == "old-access" {
						seen["duplicate"] = true
					}
				}
			case "corrects", "supersedes", "conflicts_with":
				for _, relation := range candidate.Proposal.Relations {
					expected := map[string]string{"corrects": "old-port", "supersedes": "old-retry", "conflicts_with": "old-storage"}
					if relation.TargetID == expected[candidate.Action] {
						seen[candidate.Action] = true
					}
				}
			}
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "public-relations-result.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(f).Encode(struct {
		Actions map[string]bool `json:"actions"`
		Reports []Report        `json:"reports"`
	}{seen, reports})
	f.Close()
	if runErr != nil {
		t.Fatal("real relationship gate failed; inspect private public-fixture evidence", runErr)
	}
	for _, action := range []string{"new", "corrects", "supersedes", "conflicts_with", "duplicate"} {
		if !seen[action] {
			t.Errorf("public relationship fixture did not produce supported %s suggestion", action)
		}
	}
}

// This opt-in gate sends public fixtures only. Normal tests never use a real
// endpoint or discover credentials. An operator supplies every configuration.
func TestRealProviderPublicCorpus(t *testing.T) {
	endpoint := os.Getenv("CPA_EXTRACT_ACCEPTANCE_ENDPOINT")
	if endpoint == "" {
		t.Skip("explicit real-model synthetic acceptance is not configured")
	}
	model, tokenPath, dir := os.Getenv("CPA_EXTRACT_ACCEPTANCE_MODEL"), os.Getenv("CPA_EXTRACT_ACCEPTANCE_TOKEN_FILE"), os.Getenv("CPA_EXTRACT_ACCEPTANCE_DIR")
	if model == "" || tokenPath == "" || dir == "" {
		t.Fatal("explicit model, private token file and isolated output directory are required")
	}
	info, err := os.Stat(tokenPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private acceptance token file is required")
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil || len(token) > 4096 {
		t.Fatal("cannot read acceptance credential")
	}
	provider, err := NewHTTPProvider(HTTPConfig{Endpoint: endpoint, Token: strings.TrimSpace(string(token)), Model: model})
	if err != nil {
		t.Fatal("invalid explicit provider configuration")
	}
	configured := provider.(*httpProvider)
	configured.client.Transport = &publicCaptureTransport{next: configured.client.Transport, dir: dir}
	type label struct{ Role, Attribution, Text, Required string }
	labels := []label{
		{"user", "user_requirement", "For the synthetic beacon project, require the queue capacity to be exactly 17 requests.", "17"},
		{"user", "user_requirement", "For the synthetic beacon project, require the retention period to be exactly 23 days.", "23"},
		{"user", "user_requirement", "For the synthetic beacon project, require the retry limit to be exactly 5 attempts.", "5"},
		{"user", "user_requirement", "For the synthetic beacon project, require the read timeout to be exactly 41 seconds.", "41"},
		{"user", "user_report", "I observed that synthetic beacon release 101 used SQLite for its local archive.", "101"},
		{"user", "user_report", "I observed that synthetic beacon release 102 used port 18431 for local recall.", "102"},
		{"user", "user_report", "I observed that synthetic beacon release 103 retained 13 fixture sessions.", "103"},
		{"user", "user_report", "I observed that synthetic beacon release 104 ran on a disposable Linux worker.", "104"},
		{"assistant", "assistant_suggestion", "I suggest giving synthetic beacon cache entries a 31 minute lifetime; this has not been approved.", "31"},
		{"assistant", "assistant_suggestion", "I suggest allowing 7 concurrent synthetic beacon workers; this has not been approved.", "7"},
		{"assistant", "assistant_suggestion", "I suggest limiting synthetic beacon exports to 47 records; this has not been approved.", "47"},
		{"assistant", "assistant_suggestion", "I suggest using a 53 second synthetic beacon lease; this has not been approved.", "53"},
		{"assistant", "assistant_report", "I report that synthetic beacon build 201 completed, but provide no tool output to verify it.", "201"},
		{"assistant", "assistant_report", "I report that synthetic beacon build 202 failed, but provide no tool output to verify it.", "202"},
		{"assistant", "assistant_report", "I report that synthetic beacon build 203 was published, but provide no tool output to verify it.", "203"},
		{"assistant", "assistant_report", "I report that synthetic beacon build 204 was rolled back, but provide no tool output to verify it.", "204"},
		{"tool", "execution_result", "synthetic beacon check 301: 19 unit tests passed; exit code 0.", "301"},
		{"tool", "execution_result", "synthetic beacon check 302: integration tests failed; exit code 1.", "302"},
		{"tool", "execution_result", "synthetic beacon check 303: connection refused on local fixture port; exit code 2.", "303"},
		{"tool", "execution_result", "synthetic beacon check 304: fixture migration completed; exit code 0.", "304"},
	}
	bundle := Bundle{ArchiveID: strings.Repeat("a", 64), Watermark: 5, Sources: []archive.SourceSnapshot{}}
	for group := 0; group < 5; group++ {
		messages := []map[string]string{}
		for _, label := range labels[group*4 : group*4+4] {
			messages = append(messages, map[string]string{"role": label.Role, "content": label.Text})
		}
		input, _ := json.Marshal(map[string]any{"messages": messages})
		record := archive.Record{RequestID: "public-corpus-group-" + string(rune('a'+group)), SessionID: "synthetic-beacon-session", Facets: map[string][]string{"project.id": {"synthetic-beacon"}}, OriginalRequest: input, Response: []byte(`{"choices":[]}`)}
		revision, err := sourceRevision(record)
		if err != nil {
			t.Fatal(err)
		}
		bundle.Sources = append(bundle.Sources, archive.SourceSnapshot{Record: record, Revision: revision, Sequence: int64(group + 1)})
	}
	feed, err := NewBundleFeed(bundle)
	if err != nil {
		t.Fatal(err)
	}
	selection := Selection{ProjectID: "synthetic-beacon", SessionID: "synthetic-beacon-session"}
	source, err := NewSourceReader(feed, bundle.ArchiveID, selection)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := OpenJobs(filepath.Join(dir, "public-corpus-jobs.sqlite"), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	runner := Runner{Jobs: jobs, Feed: feed, Source: source, Provider: provider, Selection: selection, Limits: DefaultLimits()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, registered, err := runner.Discover(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(registered) != 5 {
		t.Fatal("public fixture unexpectedly exceeded its five-call budget", len(registered))
	}
	reports, runErr := runner.Process(ctx, 5)
	matched := map[int]bool{}
	accepted, rejected := 0, 0
	for _, report := range reports {
		rejected += len(report.Rejected)
		for _, candidate := range report.Candidates {
			accepted++
			for index, label := range labels {
				if candidate.Attribution != label.Attribution || !strings.Contains(candidate.Proposal.Statement, label.Required) {
					continue
				}
				for _, e := range candidate.Evidence {
					if e.Role == label.Role && strings.Contains(label.Text, e.Quote) && strings.Contains(e.Quote, label.Required) {
						matched[index] = true
					}
				}
			}
		}
	}
	status, _ := jobs.Status(context.Background())
	evidence := struct {
		Model    string    `json:"model"`
		Matched  int       `json:"matched_labels"`
		Expected int       `json:"expected_labels"`
		Accepted int       `json:"accepted_candidates"`
		Rejected int       `json:"rejected_candidates"`
		Reports  []Report  `json:"reports"`
		Status   JobStatus `json:"status"`
	}{model, len(matched), len(labels), accepted, rejected, reports, status}
	output, err := os.OpenFile(filepath.Join(dir, "public-corpus-result.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewEncoder(output).Encode(evidence)
	output.Close()
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatal("real provider gate failed; inspect isolated ledger and result", runErr)
	}
	if len(matched) < 18 {
		t.Fatalf("real public corpus: attributed/key-preserving labels %d/20, want at least 18; not semantic entailment certification", len(matched))
	}
	t.Logf("public corpus attributed/key-preserving labels: %d/20; calls reserved %d", len(matched), status.TodayReservations)
}
