package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

func sessionManagerFixture(t *testing.T) *server {
	t.Helper()
	store, err := archive.OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })
	now := time.Now()
	if err = store.PutBatch([]archive.Record{{RequestID: "request", SessionID: "session/with'quote", Summary: "Original title", StartedAt: now, CompletedAt: now, Facets: map[string][]string{"project.name": {"project"}}}}); err != nil {
		t.Fatal(err)
	}
	return &server{s: storage.NewSQLite(store)}
}

func TestSessionManagementHTTPRevisionAndLifecycle(t *testing.T) {
	s := sessionManagerFixture(t)
	path := "/v1/session-metadata?session_id=session%2Fwith%27quote"
	get := httptest.NewRecorder()
	s.sessionManagement(get, httptest.NewRequest(http.MethodGet, path, nil))
	var metadata archive.SessionManagement
	if err := json.Unmarshal(get.Body.Bytes(), &metadata); err != nil || get.Code != 200 || metadata.Revision != 0 {
		t.Fatalf("initial metadata status=%d body=%s err=%v", get.Code, get.Body.String(), err)
	}
	update := `{"expected_revision":0,"title":"整理后的标题","note":"Reviewed locally","tags":["work"],"starred":true,"archived":true}`
	put := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.sessionManagement(w, r)
		return w
	}
	saved := put(update)
	if err := json.Unmarshal(saved.Body.Bytes(), &metadata); err != nil || saved.Code != 200 || metadata.Revision != 1 || !metadata.Archived || metadata.Title != "整理后的标题" {
		t.Fatalf("saved status=%d body=%s err=%v", saved.Code, saved.Body.String(), err)
	}
	conflict := put(update)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	for _, test := range []struct {
		query string
		total int
	}{
		{"", 0},
		{"?archived=only&tag=work&starred=true&q=整理&project.name=project", 1},
		{"?archived=all&project.name=other", 0},
	} {
		w := httptest.NewRecorder()
		s.sessionBrowser(w, httptest.NewRequest(http.MethodGet, "/v1/session-browser"+test.query, nil))
		var page archive.ManagedSessionPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || page.Total != test.total {
			t.Fatalf("query=%s status=%d body=%s err=%v", test.query, w.Code, w.Body.String(), err)
		}
	}
	// A management archive action never removes the original session.
	records, err := s.s.Session(context.Background(), "session/with'quote")
	if err != nil || len(records) != 1 || records[0].Summary != "Original title" {
		t.Fatalf("raw session changed: records=%+v err=%v", records, err)
	}
}

func TestSessionManagementHTTPRejectsInvalidRequests(t *testing.T) {
	s := sessionManagerFixture(t)
	for _, test := range []struct {
		method, path, body, contentType string
		status                          int
	}{
		{"POST", "/v1/session-browser", "", "", 405},
		{"GET", "/v1/session-browser?limit=bad", "", "", 400},
		{"GET", "/v1/session-browser?limit=0", "", "", 400},
		{"GET", "/v1/session-browser?offset=-1", "", "", 400},
		{"GET", "/v1/session-browser?archived=deleted", "", "", 400},
		{"GET", "/v1/session-browser?starred=1", "", "", 400},
		{"GET", "/v1/session-browser?q=a&q=b", "", "", 400},
		{"DELETE", "/v1/session-metadata?session_id=missing", "", "", 405},
		{"GET", "/v1/session-metadata", "", "", 400},
		{"GET", "/v1/session-metadata?session_id=a&session_id=b", "", "", 400},
		{"GET", "/v1/session-metadata?session_id=missing", "", "", 404},
		{"PUT", "/v1/session-metadata?session_id=missing", `{"expected_revision":0}`, "text/plain", 415},
		{"PUT", "/v1/session-metadata?session_id=missing", `{}`, "application/json", 400},
		{"PUT", "/v1/session-metadata?session_id=missing", `{"expected_revision":null}`, "application/json", 400},
		{"PUT", "/v1/session-metadata?session_id=missing", `{"expected_revision":-1}`, "application/json", 400},
		{"PUT", "/v1/session-metadata?session_id=missing", `{"expected_revision":0,"unknown":true}`, "application/json", 400},
		{"PUT", "/v1/session-metadata?session_id=missing", `{"expected_revision":0} {}`, "application/json", 400},
		{"PUT", "/v1/session-metadata?session_id=missing", `{"expected_revision":0}`, "application/json", 404},
		{"PUT", "/v1/session-metadata?session_id=missing", `{"expected_revision":0,"note":"` + strings.Repeat("x", 65536) + `"}`, "application/json", 400},
	} {
		t.Run(test.method+" "+test.path+" "+test.body[:min(len(test.body), 40)], func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.contentType != "" {
				r.Header.Set("Content-Type", test.contentType)
			}
			w := httptest.NewRecorder()
			if strings.HasPrefix(test.path, "/v1/session-browser") {
				s.sessionBrowser(w, r)
			} else {
				s.sessionManagement(w, r)
			}
			if w.Code != test.status || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("status=%d want=%d headers=%v body=%s", w.Code, test.status, w.Header(), w.Body.String())
			}
		})
	}
}

type backendWithoutSessionManagement struct{ storage.Backend }

func TestSessionManagementOptionalBackend(t *testing.T) {
	s := sessionManagerFixture(t)
	s.s = backendWithoutSessionManagement{s.s}
	for _, handler := range []http.HandlerFunc{s.sessionBrowser, s.sessionManagement} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/?session_id=known", nil))
		if w.Code != http.StatusNotImplemented {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}
