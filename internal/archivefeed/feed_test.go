package archivefeed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/ingeststatus"
	"cpa-session-archive/internal/storage"
)

const testToken = "dedicated-fixture-token-at-least-32-characters"

type fakeFeed struct {
	source    func(context.Context, string) (archive.SourceSnapshot, error)
	changes   func(context.Context, int64, int) ([]archive.SourceChange, error)
	backfill  func(context.Context, int64, int) (archive.SourcePage, error)
	watermark func(context.Context) (int64, error)
}

func (f *fakeFeed) MemorySource(ctx context.Context, id string) (archive.SourceSnapshot, error) {
	if f.source != nil {
		return f.source(ctx, id)
	}
	return archive.SourceSnapshot{Record: archive.Record{RequestID: id}, Revision: "opaque:revision", Sequence: 7}, nil
}
func (f *fakeFeed) MemoryChanges(ctx context.Context, after int64, limit int) ([]archive.SourceChange, error) {
	if f.changes != nil {
		return f.changes(ctx, after, limit)
	}
	return []archive.SourceChange{}, nil
}
func (f *fakeFeed) MemoryBackfill(ctx context.Context, after int64, limit int) (archive.SourcePage, error) {
	if f.backfill != nil {
		return f.backfill(ctx, after, limit)
	}
	return archive.SourcePage{IDs: []string{}, AfterID: after, Done: true}, nil
}
func (f *fakeFeed) MemoryWatermark(ctx context.Context) (int64, error) {
	if f.watermark != nil {
		return f.watermark(ctx)
	}
	return 7, nil
}
func fixtureServer(t *testing.T, feed storage.MemoryFeed, cfg ServerConfig) *httptest.Server {
	t.Helper()
	if cfg.ArchiveID == "" {
		cfg.ArchiveID = "archive-fixture"
	}
	if cfg.Token == "" {
		cfg.Token = testToken
	}
	handler, err := NewHandler(feed, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}
func fixtureClient(t *testing.T, origin string) *Client {
	t.Helper()
	client, err := NewClient(origin, testToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRoundTripOrderedChangesBackfillAndTombstones(t *testing.T) {
	raw, err := archive.OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.DB.Close()
	identity, err := raw.EnsureFeedIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backend := storage.NewSQLite(raw)
	if _, ok := any(backend).(storage.IdentityFeed); ok {
		t.Fatal("local legacy feed unexpectedly requires an identity handshake")
	}
	when := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"first", "second", "third"} {
		if err := raw.PutBatch([]archive.Record{{RequestID: id, SessionID: "fixture", StartedAt: when, CompletedAt: when, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"synthetic"}]}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	server := fixtureServer(t, backend, ServerConfig{ArchiveID: identity, HistoryComplete: true, Diagnostics: func() ingeststatus.Status { return ingeststatus.Status{} }})
	client := fixtureClient(t, server.URL)
	manifest, err := client.MemoryIdentity(context.Background())
	if err != nil || manifest.ArchiveID != identity || manifest.Watermark < 3 || !manifest.HistoryComplete {
		t.Fatal("invalid manifest", err)
	}
	page, err := client.MemoryBackfill(context.Background(), 0, 2)
	if err != nil || len(page.IDs) != 2 || page.IDs[0] != "first" || page.IDs[1] != "second" || page.Done {
		t.Fatal("invalid first backfill page", err)
	}
	last, err := client.MemoryBackfill(context.Background(), page.AfterID, 2)
	if err != nil || len(last.IDs) != 1 || last.IDs[0] != "third" || !last.Done {
		t.Fatal("invalid final backfill page", err)
	}
	local, err := backend.MemorySource(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := client.MemorySource(context.Background(), "second")
	if err != nil || remote.Revision != local.Revision || remote.Sequence != local.Sequence || string(remote.Record.OriginalRequest) != string(local.Record.OriginalRequest) {
		t.Fatal("source transport changed evidence or revision", err)
	}
	if revision, err := client.MemoryRevision(context.Background(), "second"); err != nil || revision != local.Revision {
		t.Fatal("revision transport changed opaque revision", err)
	}
	if _, err := raw.DB.Exec(`DELETE FROM records WHERE request_id='second'`); err != nil {
		t.Fatal(err)
	}
	changes, err := client.MemoryChanges(context.Background(), 0, 256)
	if err != nil || len(changes) != 4 || changes[3].RequestID != "second" {
		t.Fatalf("ordered tombstone event missing: %+v %v", changes, err)
	}
	for index, change := range changes {
		if index > 0 && change.Sequence <= changes[index-1].Sequence {
			t.Fatal("change events are unordered")
		}
	}
	for _, read := range []func() error{
		func() error { _, err := backend.MemorySource(context.Background(), "second"); return err },
		func() error { _, err := client.MemorySource(context.Background(), "second"); return err },
		func() error { _, err := client.MemoryRevision(context.Background(), "second"); return err },
	} {
		if err := read(); !errors.Is(err, storage.ErrSourceNotFound) {
			t.Fatal("explicit tombstone was not mapped", err)
		}
	}
	if _, err := client.IngestionStatus(context.Background()); err != nil {
		t.Fatal("ingestion diagnostics unavailable", err)
	}
}

func authenticatedRequest(t *testing.T, server, path, method string, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, server+Prefix+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestReadinessAuthenticationOriginAndStrictRequests(t *testing.T) {
	ready := make(chan struct{})
	server := fixtureServer(t, &fakeFeed{}, ServerConfig{Ready: ready})
	for _, path := range []string{"manifest", "changes", "backfill", "source?id=x", "revision?id=x", "ingestion", "unknown"} {
		response := authenticatedRequest(t, server.URL, path, "GET", nil)
		if response.StatusCode != 503 || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("readiness not enforced on %s: %d", path, response.StatusCode)
		}
	}
	close(ready)
	for _, test := range []struct {
		path    string
		method  string
		headers map[string]string
		status  int
	}{
		{"manifest", "GET", map[string]string{"Authorization": "Bearer wrong-secret"}, 401},
		{"manifest", "GET", map[string]string{"Origin": "http://127.0.0.1"}, 403},
		{"manifest", "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"manifest", "POST", nil, 405},
		{"changes?after=-1", "GET", nil, 400},
		{"changes?after=%2B1", "GET", nil, 400},
		{"changes?after=1&after=2", "GET", nil, 400},
		{"changes?limit=257", "GET", nil, 400},
		{"backfill?after_id=9223372036854775808", "GET", nil, 400},
		{"backfill?limit=0", "GET", nil, 400},
		{"source", "GET", nil, 400},
		{"revision?id=x&unexpected=1", "GET", nil, 400},
		{"unknown", "GET", nil, 400},
	} {
		response := authenticatedRequest(t, server.URL, test.path, test.method, test.headers)
		if response.StatusCode != test.status || response.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("request %s = %d, want %d", test.path, response.StatusCode, test.status)
		}
	}
}

func TestOnlyExplicitMissingSentinelProduces404(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"missing", storage.ErrSourceNotFound, 404},
		{"raw sql absence is not transport proof", sql.ErrNoRows, 503},
		{"backend outage", errors.New("PRIVATE-backend-path-and-key"), 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			feed := &fakeFeed{source: func(context.Context, string) (archive.SourceSnapshot, error) {
				return archive.SourceSnapshot{}, test.err
			}}
			server := fixtureServer(t, feed, ServerConfig{})
			for _, route := range []string{"source?id=x", "revision?id=x"} {
				response := authenticatedRequest(t, server.URL, route, "GET", nil)
				body, _ := io.ReadAll(response.Body)
				if response.StatusCode != test.want || strings.Contains(string(body), "PRIVATE") {
					t.Fatal("backend error leaked or incorrectly erased a source")
				}
			}
		})
	}
}

func TestClientRejectsUnsafeOriginsAndDoesNotFollowRedirects(t *testing.T) {
	for _, origin := range []string{"https://127.0.0.1:1234", "http://localhost:1234", "http://0.0.0.0:1234", "http://192.0.2.1:1234", "http://user:secret@127.0.0.1:1234", "http://127.0.0.1/path", "http://127.0.0.1?x=1", "http://127.0.0.1/#fragment", "http://127.0.0.1:", "http://127.0.0.1:99999"} {
		if _, err := NewClient(origin, testToken); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe origin accepted or disclosed: %q", origin)
		}
	}
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer origin.Close()
	client := fixtureClient(t, origin.URL)
	if client.transport.Proxy != nil {
		t.Fatal("feed client permits environment proxies")
	}
	if _, err := client.MemoryIdentity(context.Background()); err == nil || redirected.Load() != 0 {
		t.Fatal("client followed a redirect or accepted its response")
	}
}

func protocolHeaders(w http.ResponseWriter, identity string) {
	w.Header().Set(HeaderVersion, Version)
	w.Header().Set(HeaderArchiveID, identity)
	w.Header().Set("Content-Type", "application/json")
}

func TestClientPinsIdentityAndChecksEveryResponseBeforeAbsence(t *testing.T) {
	var identity atomic.Value
	identity.Store("archive-original")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := identity.Load().(string)
		protocolHeaders(w, id)
		if strings.HasSuffix(r.URL.Path, "/manifest") {
			writeJSON(w, manifest{1, id, 9, true})
			return
		}
		writeFailure(w, 404, "source_missing")
	}))
	defer server.Close()
	client := fixtureClient(t, server.URL)
	if _, err := client.MemoryIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity.Store("archive-replacement")
	for _, read := range []func() error{
		func() error { _, err := client.MemoryIdentity(context.Background()); return err },
		func() error { _, err := client.MemorySource(context.Background(), "x"); return err },
		func() error { _, err := client.MemoryRevision(context.Background(), "x"); return err },
		func() error { _, err := client.MemoryChanges(context.Background(), 0, 1); return err },
		func() error { _, err := client.MemoryBackfill(context.Background(), 0, 1); return err },
		func() error { _, err := client.IngestionStatus(context.Background()); return err },
	} {
		err := read()
		if err == nil || errors.Is(err, storage.ErrSourceNotFound) || strings.Contains(err.Error(), "archive-replacement") {
			t.Fatal("changed feed identity accepted or misclassified", err)
		}
	}
}

func TestClientDoesNotMistakeMetadataOrUnproven404ForDeletion(t *testing.T) {
	for _, test := range []struct {
		name, route, version, identity, code string
		wantMissing                          bool
	}{
		{"explicit source", "source", Version, "archive-a", "source_missing", true},
		{"explicit revision", "revision", Version, "archive-a", "source_missing", true},
		{"metadata", "manifest", Version, "archive-a", "source_missing", false},
		{"changes", "changes", Version, "archive-a", "source_missing", false},
		{"wrong code", "source", Version, "archive-a", "not_found", false},
		{"wrong version", "source", "2", "archive-a", "source_missing", false},
		{"missing identity", "source", Version, "", "source_missing", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				protocolHeaders(w, test.identity)
				w.Header().Set(HeaderVersion, test.version)
				writeFailure(w, 404, test.code)
			}))
			defer server.Close()
			client := fixtureClient(t, server.URL)
			var err error
			switch test.route {
			case "source":
				_, err = client.MemorySource(context.Background(), "x")
			case "revision":
				_, err = client.MemoryRevision(context.Background(), "x")
			case "changes":
				_, err = client.MemoryChanges(context.Background(), 0, 1)
			default:
				_, err = client.MemoryIdentity(context.Background())
			}
			if err == nil || errors.Is(err, storage.ErrSourceNotFound) != test.wantMissing {
				t.Fatal("incorrect absence classification", err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientBodyLimitsRejectOversizeWithoutClaimingValidPrefix(t *testing.T) {
	client := fixtureClient(t, "http://127.0.0.1:1")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cap := int64(MaxMetadataBytes)
		if strings.HasSuffix(r.URL.Path, "/source") {
			cap = MaxSourceBytes
		}
		headers := http.Header{}
		headers.Set(HeaderVersion, Version)
		headers.Set(HeaderArchiveID, "archive-a")
		headers.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: 200, Header: headers, ContentLength: cap + 1, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	if _, err := client.MemorySource(context.Background(), "x"); err == nil {
		t.Fatal("oversized source accepted")
	}
	if _, err := client.MemoryRevision(context.Background(), "x"); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocolHeaders(w, "archive-a")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, `{"revision":"opaque"}`+strings.Repeat(" ", 1024))
	}))
	defer server.Close()
	streaming := fixtureClient(t, server.URL)
	var value revision
	if _, err := streaming.get(context.Background(), "revision", url.Values{"id": {"x"}}, &value, 64, false); err == nil {
		t.Fatal("oversized response accepted by decoding its valid prefix")
	}
	if decode([]byte(`{"revision":"valid"} {"extra":true}`), &value) == nil || decode([]byte(`null`), &value) == nil {
		t.Fatal("invalid protocol body accepted")
	}
}

func TestServerHistoryIsExplicitAndMetadataCannotExceedItsLimit(t *testing.T) {
	if _, err := NewHandler(&fakeFeed{}, ServerConfig{ArchiveID: "fixture", Token: "short"}); err == nil {
		t.Fatal("weak feed token accepted")
	}
	server := fixtureServer(t, &fakeFeed{}, ServerConfig{})
	client := fixtureClient(t, server.URL)
	identity, err := client.MemoryIdentity(context.Background())
	if err != nil || identity.HistoryComplete {
		t.Fatal("unknown backend claimed complete unpruned history", err)
	}
	response := httptest.NewRecorder()
	writeJSON(response, map[string]string{"private": strings.Repeat("x", MaxMetadataBytes)})
	if response.Code != 503 || strings.Contains(response.Body.String(), "private") {
		t.Fatal("oversized metadata was partially published")
	}
	bad := &fakeFeed{changes: func(context.Context, int64, int) ([]archive.SourceChange, error) {
		return []archive.SourceChange{{Sequence: 2, RequestID: "a"}, {Sequence: 1, RequestID: "b"}}, nil
	}}
	badServer := fixtureServer(t, bad, ServerConfig{})
	if got := authenticatedRequest(t, badServer.URL, "changes", "GET", nil); got.StatusCode != 503 {
		t.Fatal("server published unordered change events")
	}
}

func TestSourceAndRevisionConcurrencyAreIndependent(t *testing.T) {
	entered := make(chan struct{}, 6)
	release := make(chan struct{})
	feed := &fakeFeed{source: func(ctx context.Context, id string) (archive.SourceSnapshot, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return archive.SourceSnapshot{Record: archive.Record{RequestID: id}, Revision: "opaque", Sequence: 1}, nil
		case <-ctx.Done():
			return archive.SourceSnapshot{}, ctx.Err()
		}
	}}
	server := fixtureServer(t, feed, ServerConfig{})
	client := fixtureClient(t, server.URL)
	results := make(chan error, 6)
	for range 2 {
		go func() { _, err := client.MemorySource(context.Background(), "x"); results <- err }()
	}
	for range 4 {
		go func() { _, err := client.MemoryRevision(context.Background(), "x"); results <- err }()
	}
	for range 6 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("source/revision concurrency was coupled or undersized")
		}
	}
	for _, read := range []func() error{
		func() error { _, err := client.MemorySource(context.Background(), "x"); return err },
		func() error { _, err := client.MemoryRevision(context.Background(), "x"); return err },
	} {
		if err := read(); err == nil || errors.Is(err, storage.ErrSourceNotFound) {
			close(release)
			t.Fatal("busy feed was accepted or treated as missing", err)
		}
	}
	close(release)
	for range 6 {
		if err := <-results; err != nil {
			t.Fatal("admitted request failed", err)
		}
	}
}

func TestClientCancellationAndCloseCancelServerWork(t *testing.T) {
	for _, closeClient := range []bool{false, true} {
		t.Run(map[bool]string{false: "context", true: "client close"}[closeClient], func(t *testing.T) {
			entered, stopped := make(chan struct{}), make(chan struct{})
			feed := &fakeFeed{source: func(ctx context.Context, id string) (archive.SourceSnapshot, error) {
				close(entered)
				<-ctx.Done()
				close(stopped)
				return archive.SourceSnapshot{}, ctx.Err()
			}}
			server := fixtureServer(t, feed, ServerConfig{})
			client := fixtureClient(t, server.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := client.MemorySource(ctx, "x"); result <- err }()
			<-entered
			if closeClient {
				_ = client.Close()
			} else {
				cancel()
			}
			select {
			case err := <-result:
				if err == nil || !closeClient && !errors.Is(err, context.Canceled) {
					t.Fatal("client cancellation not propagated", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("client cancellation blocked")
			}
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("server retained canceled work")
			}
		})
	}
}

func TestMalformedOrderedPagesAreRejected(t *testing.T) {
	for _, body := range []string{`{"changes":[{"sequence":2,"request_id":"a"},{"sequence":1,"request_id":"b"}]}`, `{"changes":null}`, `{}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			protocolHeaders(w, "archive-a")
			_, _ = io.WriteString(w, body)
		}))
		client := fixtureClient(t, server.URL)
		if _, err := client.MemoryChanges(context.Background(), 0, 256); err == nil {
			t.Fatal("malformed changes accepted")
		}
		server.Close()
	}
	if validPage(page{IDs: []string{}, AfterID: 0, Done: false}, 0, 256) || validPage(page{IDs: []string{}, AfterID: 1, Done: true}, 0, 256) || validPage(page{IDs: []string{"x", "x"}, AfterID: 2, Done: true}, 0, 256) {
		t.Fatal("nonprogressing or duplicate backfill accepted")
	}
	data, _ := json.Marshal(manifest{1, "archive-a", 9, true})
	if !strings.Contains(string(data), `"history_complete":true`) {
		t.Fatal("manifest contract changed")
	}
}
