// Package embedding defines bounded, purpose-specific local text embeddings.
package embedding

import "context"

type Purpose string

const (
	Query    Purpose = "query"
	Document Purpose = "document"
	MaxBatch         = 8
)

// Embedder identifies the complete model and preprocessing contract. A changed
// fingerprint requires rebuilding stored vectors, even when dimensions match.
type Embedder interface {
	Fingerprint() string
	Dimensions() int
	Embed(context.Context, Purpose, []string) ([][]float32, error)
}
