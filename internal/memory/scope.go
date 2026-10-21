package memory

import (
	"context"
	"fmt"
)

// Scope is fixed by the credential issuer, not by model-provided tool arguments.
type Scope struct {
	ProjectID string `json:"project_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	AllowAll  bool   `json:"allow_all,omitempty"`
}
type Credential struct {
	Token string `json:"token"`
	Scope Scope  `json:"scope"`
}
type scopedReader struct {
	reader Reader
	scope  Scope
}

func WithScope(reader Reader, scope Scope) (Reader, error) {
	if reader == nil || (!scope.AllowAll && scope.ProjectID == "" && scope.SessionID == "") || (scope.AllowAll && (scope.ProjectID != "" || scope.SessionID != "")) {
		return nil, fmt.Errorf("an explicit project/session scope or owner-wide permission is required")
	}
	return &scopedReader{reader: reader, scope: scope}, nil
}
func (s *scopedReader) query(q Query) (Query, error) {
	if s.scope.ProjectID != "" {
		if q.ProjectID != "" && q.ProjectID != s.scope.ProjectID {
			return q, ErrInvalidQuery
		}
		q.ProjectID = s.scope.ProjectID
	}
	if s.scope.SessionID != "" {
		if q.SessionID != "" && q.SessionID != s.scope.SessionID {
			return q, ErrInvalidQuery
		}
		q.SessionID = s.scope.SessionID
	}
	return q, nil
}
func (s *scopedReader) status(status Status) Status {
	if !s.scope.AllowAll {
		status.ScopeFiltered = true
		status.Sources = 0
		status.Chunks = 0
		status.UniqueChunks = 0
		status.DeduplicatedChunks = 0
		status.PartialSources = 0
		status.Coverage = nil
		status.Ingestion = nil
		status.Semantic = nil
	}
	return status
}
func (s *scopedReader) Search(ctx context.Context, q Query) (SearchResult, error) {
	q, err := s.query(q)
	if err != nil {
		return SearchResult{}, err
	}
	result, err := s.reader.Search(ctx, q)
	result.Status = s.status(result.Status)
	return result, err
}
func (s *scopedReader) Recap(ctx context.Context, q Query) (RecapResult, error) {
	q, err := s.query(q)
	if err != nil {
		return RecapResult{}, err
	}
	result, err := s.reader.Recap(ctx, q)
	result.Status = s.status(result.Status)
	return result, err
}
func (s *scopedReader) ReadSource(ctx context.Context, c Citation) (Evidence, error) {
	evidence, err := s.reader.ReadSource(ctx, c)
	if err != nil {
		return Evidence{}, err
	}
	if (s.scope.ProjectID != "" && evidence.ProjectID != s.scope.ProjectID) || (s.scope.SessionID != "" && evidence.SessionID != s.scope.SessionID) {
		return Evidence{}, ErrSourceUnavailable
	}
	return evidence, nil
}
func (s *scopedReader) Status(ctx context.Context) (Status, error) {
	status, err := s.reader.Status(ctx)
	return s.status(status), err
}
