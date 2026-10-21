package archivefeed

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cpa-session-archive/internal/ingeststatus"
	"cpa-session-archive/internal/storage"
)

type ServerConfig struct {
	ArchiveID       string
	Token           string
	HistoryComplete bool
	Ready           <-chan struct{}
	Diagnostics     func() ingeststatus.Status
}

// NewHandler requires a dedicated token. A nil Ready channel disables readiness gating.
func NewHandler(feed storage.MemoryFeed, cfg ServerConfig) (http.Handler, error) {
	if feed == nil || !validIdentity(cfg.ArchiveID) || !validToken(cfg.Token) {
		return nil, errors.New("invalid archive feed configuration")
	}
	expected := sha256.Sum256([]byte("Bearer " + cfg.Token))
	sources, revisions := make(chan struct{}, 2), make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		remote, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || net.ParseIP(remote) == nil || !net.ParseIP(remote).IsLoopback() || len(r.Header.Values("Origin")) != 0 || (r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "none") {
			writeFailure(w, http.StatusForbidden, "forbidden")
			return
		}
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(expected[:], got[:]) != 1 {
			writeFailure(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		w.Header().Set(HeaderVersion, Version)
		w.Header().Set(HeaderArchiveID, cfg.ArchiveID)
		if cfg.Ready != nil {
			select {
			case <-cfg.Ready:
			default:
				writeFailure(w, http.StatusServiceUnavailable, "not_ready")
				return
			}
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeFailure(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeFailure(w, http.StatusBadRequest, "invalid_request")
			return
		}
		name := strings.TrimPrefix(r.URL.Path, Prefix)
		if r.URL.Path != Prefix+name {
			writeFailure(w, http.StatusBadRequest, "invalid_request")
			return
		}
		switch name {
		case "manifest":
			if !validQuery(query) {
				writeFailure(w, 400, "invalid_request")
				return
			}
			watermark, err := feed.MemoryWatermark(r.Context())
			if err != nil || watermark < 0 {
				writeFailure(w, 503, "unavailable")
				return
			}
			writeJSON(w, manifest{Version: 1, ArchiveID: cfg.ArchiveID, Watermark: watermark, HistoryComplete: cfg.HistoryComplete})
		case "changes", "backfill":
			cursor := "after"
			if name == "backfill" {
				cursor = "after_id"
			}
			if !validQuery(query, cursor, "limit") {
				writeFailure(w, 400, "invalid_request")
				return
			}
			after, ok := decimal(query, cursor, 0)
			limit, limitOK := decimal(query, "limit", 64)
			if !ok || !limitOK || limit < 1 || limit > 256 {
				writeFailure(w, 400, "invalid_request")
				return
			}
			if name == "changes" {
				items, err := feed.MemoryChanges(r.Context(), after, int(limit))
				result := changes{Changes: make([]change, 0, len(items))}
				for _, item := range items {
					result.Changes = append(result.Changes, change{item.Sequence, item.RequestID})
				}
				if err != nil || !validChanges(result.Changes, after, int(limit)) {
					writeFailure(w, 503, "unavailable")
					return
				}
				writeJSON(w, result)
			} else {
				items, err := feed.MemoryBackfill(r.Context(), after, int(limit))
				result := page{items.IDs, items.AfterID, items.Done}
				if result.IDs == nil {
					result.IDs = []string{}
				}
				if err != nil || !validPage(result, after, int(limit)) {
					writeFailure(w, 503, "unavailable")
					return
				}
				writeJSON(w, result)
			}
		case "source", "revision":
			if !validQuery(query, "id") || !validID(query.Get("id")) {
				writeFailure(w, 400, "invalid_request")
				return
			}
			semaphore := sources
			if name == "revision" {
				semaphore = revisions
			}
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			default:
				writeFailure(w, 429, "busy")
				return
			}
			if name == "revision" {
				var value string
				var err error
				if revisions, ok := feed.(storage.RevisionFeed); ok {
					value, err = revisions.MemoryRevision(r.Context(), query.Get("id"))
				} else {
					item, sourceErr := feed.MemorySource(r.Context(), query.Get("id"))
					value, err = item.Revision, sourceErr
				}
				if errors.Is(err, storage.ErrSourceNotFound) {
					writeFailure(w, 404, "source_missing")
					return
				}
				if err != nil || !validRevision(value) {
					writeFailure(w, 503, "unavailable")
					return
				}
				writeJSON(w, revision{value})
			} else {
				item, err := feed.MemorySource(r.Context(), query.Get("id"))
				if errors.Is(err, storage.ErrSourceNotFound) {
					writeFailure(w, 404, "source_missing")
					return
				}
				if err != nil || item.Record.RequestID != query.Get("id") || item.Sequence < 0 || !validRevision(item.Revision) {
					writeFailure(w, 503, "unavailable")
					return
				}
				writeJSON(w, source{item.Record, item.Revision, item.Sequence})
			}
		case "ingestion":
			if !validQuery(query) {
				writeFailure(w, 400, "invalid_request")
				return
			}
			if cfg.Diagnostics == nil {
				writeFailure(w, 503, "unavailable")
				return
			}
			writeJSON(w, cfg.Diagnostics())
		default:
			writeFailure(w, 400, "invalid_request")
		}
	}), nil
}

func writeFailure(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(failure{code})
}
func writeJSON(w http.ResponseWriter, value any) {
	data, err := json.Marshal(value)
	limit := MaxMetadataBytes
	if _, fullSource := value.(source); fullSource {
		limit = MaxSourceBytes
	}
	if err != nil || len(data) > limit {
		writeFailure(w, 503, "unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
func validQuery(values url.Values, allowed ...string) bool {
	for key, values := range values {
		ok := false
		for _, candidate := range allowed {
			ok = ok || key == candidate
		}
		if !ok || len(values) != 1 {
			return false
		}
	}
	return true
}
func decimal(values url.Values, key string, fallback int64) (int64, bool) {
	items, exists := values[key]
	if !exists {
		return fallback, true
	}
	if len(items) != 1 || items[0] == "" {
		return 0, false
	}
	for _, c := range items[0] {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(items[0], 10, 64)
	return value, err == nil
}
