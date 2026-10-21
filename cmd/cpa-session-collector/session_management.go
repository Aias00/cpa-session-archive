package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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
	options := archive.SessionBrowseOptions{Limit: 100, InactiveDays: 30, Filters: map[string]string{}}
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
		if len(values) != 1 || !validManagementText(name) || !validManagementText(values[0]) {
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
		case "view":
			options.View = value
		case "inactive_days":
			options.InactiveDays, err = strconv.Atoi(value)
			if err == nil && (options.InactiveDays < 1 || options.InactiveDays > 3650) {
				err = archive.ErrInvalidSessionManagement
			}
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

func validManagementText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func (s *server) sessionBulkPreview(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		sessionManagementError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	manager, ok := s.s.(storage.SessionBulkManager)
	if !ok {
		sessionManagementError(w, http.StatusNotImplemented, "unsupported", "bulk session management unavailable")
		return
	}
	var request struct {
		Selection *archive.SessionBrowseOptions `json:"selection"`
		Action    *archive.SessionBulkAction    `json:"action"`
	}
	if !decodeSessionBulkJSON(w, r, &request) {
		return
	}
	if request.Selection == nil || request.Action == nil {
		sessionManagementError(w, http.StatusBadRequest, "invalid_update", "explicit selection and action objects required")
		return
	}
	job, err := manager.PlanSessionBulk(r.Context(), archive.SessionBulkPlanRequest{Selection: *request.Selection, Action: *request.Action})
	if err != nil {
		writeSessionManagementError(w, err)
		return
	}
	writeJSON(w, job)
}

func (s *server) sessionBulk(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		sessionManagementError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	manager, ok := s.s.(storage.SessionBulkManager)
	if !ok {
		sessionManagementError(w, http.StatusNotImplemented, "unsupported", "bulk session management unavailable")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 16384 || len(query) > 1 || (len(query) == 1 && (len(query["id"]) != 1 || !validSessionBulkID(query.Get("id")))) {
		sessionManagementError(w, http.StatusBadRequest, "invalid_query", "one exact job id or no query required")
		return
	}
	if len(query) == 0 {
		jobs, err := manager.ListSessionBulk(r.Context())
		if err != nil {
			writeSessionManagementError(w, err)
			return
		}
		writeJSON(w, map[string]any{"jobs": jobs})
		return
	}
	job, err := manager.SessionBulk(r.Context(), query.Get("id"))
	if err != nil {
		writeSessionManagementError(w, err)
		return
	}
	writeJSON(w, job)
}

func (s *server) sessionBulkApply(w http.ResponseWriter, r *http.Request) {
	s.sessionBulkStep(w, r, false)
}

func (s *server) sessionBulkUndo(w http.ResponseWriter, r *http.Request) {
	s.sessionBulkStep(w, r, true)
}

func (s *server) sessionBulkStep(w http.ResponseWriter, r *http.Request, undo bool) {
	managementHeaders(w)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		sessionManagementError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	manager, ok := s.s.(storage.SessionBulkManager)
	if !ok {
		sessionManagementError(w, http.StatusNotImplemented, "unsupported", "bulk session management unavailable")
		return
	}
	var step struct {
		ID    string          `json:"id"`
		Limit json.RawMessage `json:"limit"`
	}
	if !decodeSessionBulkJSON(w, r, &step) {
		return
	}
	limit := 100
	if step.Limit != nil {
		limit = 0
		if err := json.Unmarshal(step.Limit, &limit); err != nil {
			sessionManagementError(w, http.StatusBadRequest, "invalid_step", "step limit must be an integer")
			return
		}
	}
	if !validSessionBulkID(step.ID) || limit < 1 || limit > 100 {
		sessionManagementError(w, http.StatusBadRequest, "invalid_step", "valid job id and limit 1..100 required")
		return
	}
	var job archive.SessionBulkJob
	var err error
	if undo {
		job, err = manager.UndoSessionBulk(r.Context(), step.ID, limit)
	} else {
		job, err = manager.ApplySessionBulk(r.Context(), step.ID, limit)
	}
	if err != nil {
		writeSessionManagementError(w, err)
		return
	}
	writeJSON(w, job)
}

func validSessionBulkID(id string) bool {
	return id != "" && len(id) <= 4096 && validManagementText(id)
}

// Mutations are one bounded step. Clients inspect the job after ambiguous errors
// rather than automatically resending a potentially committed request.
func decodeSessionBulkJSON(w http.ResponseWriter, r *http.Request, output any) bool {
	if r.URL.RawQuery != "" {
		sessionManagementError(w, http.StatusBadRequest, "invalid_query", "no query parameters allowed")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		sessionManagementError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "application/json required")
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if err != nil || !utf8.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		sessionManagementError(w, http.StatusBadRequest, "invalid_update", "one bounded JSON object required")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = uniqueJSONValue(decoder); err == nil {
		_, err = decoder.Token()
		if err == io.EOF {
			decoder = json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(output)
		} else {
			err = archive.ErrInvalidSessionManagement
		}
	}
	if err != nil {
		sessionManagementError(w, http.StatusBadRequest, "invalid_update", "valid JSON without unknown or duplicate fields required")
		return false
	}
	return true
}

func uniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	keys := map[string]bool{}
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return archive.ErrInvalidSessionManagement
			}
			keys[name] = true
		}
		if err := uniqueJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
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
