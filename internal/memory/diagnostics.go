package memory

import (
	"context"
	"cpa-session-archive/internal/ingeststatus"
	"time"
)

// Aliases preserve callers while collector telemetry lives outside recall.
type ProducerObservation = ingeststatus.ProducerObservation
type IngestionStatus = ingeststatus.Status

type diagnosticReader struct {
	Reader
	diagnostics func(context.Context) (ingeststatus.Status, error)
}

// WithIngestionStatus attaches collector-owned diagnostics at the read boundary.
// Apply credential scoping outside this decorator so global counts are removed.
func WithIngestionStatus(reader Reader, diagnostics func() IngestionStatus) Reader {
	if diagnostics == nil {
		return reader
	}
	return WithIngestionStatusContext(reader, func(context.Context) (ingeststatus.Status, error) { return diagnostics(), nil })
}

// WithIngestionStatusContext reads optional diagnostics using each request's
// context. Failed observations are omitted, never replaced with a cached report.
// Each observation has a one-second budget independent of successful retrieval.
// Scope the returned reader outside this decorator to hide global diagnostics.
func WithIngestionStatusContext(reader Reader, diagnostics func(context.Context) (ingeststatus.Status, error)) Reader {
	if diagnostics == nil {
		return reader
	}
	return &diagnosticReader{Reader: reader, diagnostics: diagnostics}
}

func (r *diagnosticReader) annotate(ctx context.Context, s Status) Status {
	s.Ingestion = nil
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	snapshot, err := r.diagnostics(ctx)
	if err != nil {
		return s
	}
	s.Ingestion = &snapshot
	return s
}

func (r *diagnosticReader) Status(ctx context.Context) (Status, error) {
	s, err := r.Reader.Status(ctx)
	if err == nil {
		s = r.annotate(ctx, s)
	}
	return s, err
}

func (r *diagnosticReader) Search(ctx context.Context, q Query) (SearchResult, error) {
	result, err := r.Reader.Search(ctx, q)
	if err == nil {
		result.Status = r.annotate(ctx, result.Status)
	}
	return result, err
}

func (r *diagnosticReader) Recap(ctx context.Context, q Query) (RecapResult, error) {
	result, err := r.Reader.Recap(ctx, q)
	if err == nil {
		result.Status = r.annotate(ctx, result.Status)
	}
	return result, err
}
