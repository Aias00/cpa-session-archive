package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

func bulkHTTP(t *testing.T, s *server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	switch strings.Split(path, "?")[0] {
	case "/v1/session-bulk-preview":
		s.sessionBulkPreview(w, r)
	case "/v1/session-bulk-apply":
		s.sessionBulkApply(w, r)
	case "/v1/session-bulk-undo":
		s.sessionBulkUndo(w, r)
	default:
		s.sessionBulk(w, r)
	}
	return w
}

func TestSessionBulkHTTPFullSelectionBoundedStepAndUndo(t *testing.T) {
	s := sessionManagerFixture(t)
	store := s.s.(*storage.SQLite)
	now := time.Now()
	for i := range 3 {
		if err := store.PutBatch([]archive.Record{{RequestID: fmt.Sprint("bulk-request-", i), SessionID: fmt.Sprint("bulk-session-", i), StartedAt: now, CompletedAt: now, Facets: map[string][]string{"project.name": {"bulk-project"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	w := bulkHTTP(t, s, "POST", "/v1/session-bulk-preview", `{"selection":{"limit":1,"offset":2,"filters":{"project.name":"bulk-project"}},"action":{"archived":true}}`)
	var job archive.SessionBulkJob
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 200 || job.Total != 3 || job.ID == "" {
		t.Fatalf("full preview failed: status=%d job=%+v err=%v", w.Code, job, err)
	}
	if got := bulkHTTP(t, s, "GET", "/v1/session-bulk?id="+job.ID, ""); got.Code != 200 {
		t.Fatalf("job lookup status=%d", got.Code)
	}
	w = bulkHTTP(t, s, "POST", "/v1/session-bulk-apply", fmt.Sprintf(`{"id":%q,"limit":1}`, job.ID))
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 200 || job.Applied != 1 || job.Pending != 2 {
		t.Fatalf("bounded apply failed: status=%d job=%+v err=%v", w.Code, job, err)
	}
	w = bulkHTTP(t, s, "POST", "/v1/session-bulk-undo", fmt.Sprintf(`{"id":%q}`, job.ID))
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 200 || job.Undone != 1 {
		t.Fatalf("undo failed: status=%d job=%+v err=%v", w.Code, job, err)
	}
	page, err := store.BrowseSessions(context.Background(), archive.SessionBrowseOptions{Filters: map[string]string{"project.name": "bulk-project"}})
	if err != nil || page.Total != 3 {
		t.Fatalf("undo did not restore session visibility: total=%d err=%v", page.Total, err)
	}
	w = bulkHTTP(t, s, "GET", "/v1/session-bulk", "")
	var list struct {
		Jobs []archive.SessionBulkJob `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Jobs) != 1 {
		t.Fatalf("job listing status=%d jobs=%d err=%v", w.Code, len(list.Jobs), err)
	}
}

func TestSessionBulkHTTPRejectsMalformedRequests(t *testing.T) {
	s := sessionManagerFixture(t)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/v1/session-bulk-preview", "", 405},
		{"PUT", "/v1/session-bulk", "", 405},
		{"GET", "/v1/session-bulk?id=x&id=y", "", 400},
		{"GET", "/v1/session-bulk?id=x&other=y", "", 400},
		{"GET", "/v1/session-bulk?other=x", "", 400},
		{"GET", "/v1/session-bulk?id=", "", 400},
		{"GET", "/v1/session-bulk?id=a%00b", "", 400},
		{"GET", "/v1/session-bulk?id=bulk-00000000000000000000000000000000", "", 404},
		{"POST", "/v1/session-bulk-preview?q=x", `{}`, 400},
		{"POST", "/v1/session-bulk-preview", `[]`, 400},
		{"POST", "/v1/session-bulk-preview", `null`, 400},
		{"POST", "/v1/session-bulk-preview", `{"action":{"archived":true}}`, 400},
		{"POST", "/v1/session-bulk-preview", `{"selection":null,"action":{"archived":true}}`, 400},
		{"POST", "/v1/session-bulk-preview", `{} {}`, 400},
		{"POST", "/v1/session-bulk-preview", `{"action":{"archived":true,"archived":false}}`, 400},
		{"POST", "/v1/session-bulk-preview", `{"selection":{"unknown":1},"action":{"archived":true}}`, 400},
		{"POST", "/v1/session-bulk-preview", `{"action":{"archived":true},"x":"` + strings.Repeat("x", 65536) + `"}`, 400},
		{"POST", "/v1/session-bulk-apply", `{"id":"x","limit":0}`, 400},
		{"POST", "/v1/session-bulk-apply", `{"id":"x","limit":null}`, 400},
		{"POST", "/v1/session-bulk-apply", `{"id":"x","limit":101}`, 400},
		{"POST", "/v1/session-bulk-apply", `{"id":"x","id":"y"}`, 400},
		{"POST", "/v1/session-bulk-apply", `{"id":"a\nb"}`, 400},
		{"POST", "/v1/session-bulk-undo", `{"id":"x","extra":true}`, 400},
	} {
		t.Run(test.method+test.path+test.body[:min(len(test.body), 50)], func(t *testing.T) {
			w := bulkHTTP(t, s, test.method, test.path, test.body)
			if w.Code != test.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d want=%d body=%s", w.Code, test.status, w.Body.String())
			}
		})
	}
	for _, contentType := range []string{"", "text/plain", "application/json;bad"} {
		r := httptest.NewRequest("POST", "/v1/session-bulk-preview", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		s.sessionBulkPreview(w, r)
		if w.Code != 415 {
			t.Fatalf("media %q status=%d", contentType, w.Code)
		}
	}
}

type failingBulkManager struct {
	storage.Backend
	err   error
	calls int
}

func (m *failingBulkManager) PlanSessionBulk(context.Context, archive.SessionBulkPlanRequest) (archive.SessionBulkJob, error) {
	m.calls++
	return archive.SessionBulkJob{}, m.err
}
func (m *failingBulkManager) SessionBulk(context.Context, string) (archive.SessionBulkJob, error) {
	m.calls++
	return archive.SessionBulkJob{}, m.err
}
func (m *failingBulkManager) ListSessionBulk(context.Context) ([]archive.SessionBulkJob, error) {
	m.calls++
	return nil, m.err
}
func (m *failingBulkManager) ApplySessionBulk(context.Context, string, int) (archive.SessionBulkJob, error) {
	m.calls++
	return archive.SessionBulkJob{}, m.err
}
func (m *failingBulkManager) UndoSessionBulk(context.Context, string, int) (archive.SessionBulkJob, error) {
	m.calls++
	return archive.SessionBulkJob{}, m.err
}

func TestSessionBulkHTTPOptionalBackendAndSanitizedErrors(t *testing.T) {
	s := sessionManagerFixture(t)
	s.s = backendWithoutSessionManagement{s.s}
	for _, test := range []struct{ method, path, body string }{
		{"POST", "/v1/session-bulk-preview", `{}`}, {"GET", "/v1/session-bulk", ""}, {"POST", "/v1/session-bulk-apply", `{"id":"x"}`}, {"POST", "/v1/session-bulk-undo", `{"id":"x"}`},
	} {
		if w := bulkHTTP(t, s, test.method, test.path, test.body); w.Code != 501 {
			t.Fatalf("unsupported status=%d", w.Code)
		}
	}
	for _, test := range []struct {
		err    error
		status int
	}{{archive.ErrSessionManagementConflict, 409}, {errors.New("private backend diagnostics"), 500}} {
		backend := &failingBulkManager{Backend: s.s, err: test.err}
		s.s = backend
		w := bulkHTTP(t, s, "POST", "/v1/session-bulk-apply", `{"id":"bulk-00000000000000000000000000000000"}`)
		if w.Code != test.status || backend.calls != 1 || strings.Contains(w.Body.String(), "private backend diagnostics") {
			t.Fatalf("unsafe error handling status=%d calls=%d body=%s", w.Code, backend.calls, w.Body.String())
		}
	}
}

func TestSessionBrowserViewParametersAreReservedAndValidated(t *testing.T) {
	s := sessionManagerFixture(t)
	for _, query := range []string{"view=recent&inactive_days=30", "view=main", "view=inactive&inactive_days=1"} {
		w := httptest.NewRecorder()
		s.sessionBrowser(w, httptest.NewRequest("GET", "/v1/session-browser?"+query, nil))
		if w.Code != 200 {
			t.Fatalf("valid view %s status=%d body=%s", query, w.Code, w.Body.String())
		}
	}
	for _, query := range []string{"view=unknown", "view=recent&view=all", "inactive_days=0", "inactive_days=3651", "inactive_days=bad", "q=a%00b", "client=a%0Ab"} {
		w := httptest.NewRecorder()
		s.sessionBrowser(w, httptest.NewRequest("GET", "/v1/session-browser?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("invalid view %s status=%d", query, w.Code)
		}
	}
}
