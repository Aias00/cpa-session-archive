package main

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

// Session management uses the collector's existing private management boundary.
// Public clients must use the authenticated CPA management proxy.
func (s *server) sessionBrowser(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		sessionManagementError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	manager, ok := s.s.(storage.SessionManager)
	if !ok {
		sessionManagementError(w, http.StatusNotImplemented, "unsupported", "session management unavailable")
		return
	}
	options := archive.SessionBrowseOptions{Limit: 100, Filters: map[string]string{}}
	if len(r.URL.RawQuery) > 16384 {
		sessionManagementError(w, http.StatusBadRequest, "invalid_query", "query too large")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		sessionManagementError(w, http.StatusBadRequest, "invalid_query", "invalid session query")
		return
	}
	for name, values := range query {
		if len(values) != 1 {
			err = archive.ErrInvalidSessionManagement
			break
		}
		value := values[0]
		switch name {
		case "limit":
			options.Limit, err = strconv.Atoi(value)
			if err == nil && (options.Limit < 1 || options.Limit > 1000) {
				err = archive.ErrInvalidSessionManagement
			}
		case "offset":
			options.Offset, err = strconv.Atoi(value)
		case "q":
			options.Query = value
		case "tag":
			options.Tag = value
		case "archived":
			options.Archived = value
		case "starred":
			if value != "true" && value != "false" {
				err = archive.ErrInvalidSessionManagement
			} else {
				options.Starred = value == "true"
			}
		default:
			options.Filters[name] = value
		}
		if err != nil {
			break
		}
	}
	if err != nil {
		sessionManagementError(w, http.StatusBadRequest, "invalid_query", "invalid session query")
		return
	}
	page, err := manager.BrowseSessions(r.Context(), options)
	if err != nil {
		writeSessionManagementError(w, err)
		return
	}
	writeJSON(w, page)
}

func (s *server) sessionManagement(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		sessionManagementError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or PUT required")
		return
	}
	manager, ok := s.s.(storage.SessionManager)
	if !ok {
		sessionManagementError(w, http.StatusNotImplemented, "unsupported", "session management unavailable")
		return
	}
	query, queryErr := url.ParseQuery(r.URL.RawQuery)
	ids := query["session_id"]
	if queryErr != nil || len(query) != 1 || len(ids) != 1 || ids[0] == "" || len(ids[0]) > 4096 {
		sessionManagementError(w, http.StatusBadRequest, "invalid_query", "one exact session_id required")
		return
	}
	if r.Method == http.MethodGet {
		metadata, err := manager.SessionManagement(r.Context(), ids[0])
		if err != nil {
			writeSessionManagementError(w, err)
			return
		}
		writeJSON(w, metadata)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		sessionManagementError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "application/json required")
		return
	}
	var update struct {
		ExpectedRevision *int64 `json:"expected_revision"`
		archive.SessionManagementUpdate
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&update); err != nil || update.ExpectedRevision == nil || *update.ExpectedRevision < 0 {
		sessionManagementError(w, http.StatusBadRequest, "invalid_update", "valid metadata and expected_revision required")
		return
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		sessionManagementError(w, http.StatusBadRequest, "invalid_update", "one JSON object required")
		return
	}
	metadata, err := manager.UpdateSessionManagement(r.Context(), ids[0], *update.ExpectedRevision, update.SessionManagementUpdate)
	if err != nil {
		writeSessionManagementError(w, err)
		return
	}
	writeJSON(w, metadata)
}

func managementHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeSessionManagementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, archive.ErrSessionNotFound):
		sessionManagementError(w, http.StatusNotFound, "not_found", "session not found")
	case errors.Is(err, archive.ErrSessionManagementConflict):
		sessionManagementError(w, http.StatusConflict, "revision_conflict", "session metadata changed; reload before saving")
	case errors.Is(err, archive.ErrInvalidSessionManagement):
		sessionManagementError(w, http.StatusBadRequest, "invalid_metadata", "invalid session metadata or query")
	default:
		sessionManagementError(w, http.StatusInternalServerError, "storage_error", "session management unavailable")
	}
}

func sessionManagementError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"code": code, "error": message})
}
