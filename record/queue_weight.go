package record

import (
	"errors"
	"math"
)

var ErrQueueMetadata = errors.New("record metadata exceeds supported queue accounting shape")

// QueueWeight estimates retained payload and metadata storage, not total process
// RSS. Capacities count retained backing arrays, including short subslices.
func QueueWeight(r Record) (int64, error) {
	n := int64(1024) + int64(cap(r.OriginalRequest)) + int64(cap(r.UpstreamRequest)) + int64(cap(r.Response))
	for _, s := range []string{r.RequestID, r.TraceID, r.SessionID, r.ParentResponseID, r.ResponseID, r.KeyID, r.PrincipalID, r.CredentialHash, r.PrincipalAlias, r.Summary, r.ResponsePreview, r.ProjectPath, r.ProjectName, r.GitRemote, r.ThreadID, r.TurnID, r.WindowID, r.RequestKind, r.ThreadSource, r.Client, r.SourceFormat, r.RequestedModel, r.Model, r.Outcome, r.Error} {
		n += int64(len(s))
	}
	metadata, err := metadataWeight(r.Metadata, 0)
	if err != nil {
		return 0, err
	}
	n += metadata
	for key, values := range r.Facets {
		n += int64(96 + len(key) + 24*len(values))
		for _, value := range values {
			n += int64(len(value))
		}
	}
	return n, nil
}

func metadataWeight(value any, depth int) (int64, error) {
	if depth > 64 {
		return 0, ErrQueueMetadata
	}
	var n int64 = 32
	switch v := value.(type) {
	case nil, bool, float64, float32, int, int64, int32, uint, uint64, uint32:
	case string:
		n += int64(len(v))
	case []byte:
		n += int64(cap(v))
	case []any:
		n += int64(24 + 16*len(v))
		for _, entry := range v {
			weight, err := metadataWeight(entry, depth+1)
			if err != nil || weight > math.MaxInt64-n {
				return 0, ErrQueueMetadata
			}
			n += weight
		}
	case map[string]any:
		for key, entry := range v {
			weight, err := metadataWeight(entry, depth+1)
			if err != nil || weight > math.MaxInt64-n-int64(96+len(key)) {
				return 0, ErrQueueMetadata
			}
			n += int64(96+len(key)) + weight
		}
	default:
		return 0, ErrQueueMetadata
	}
	return n, nil
}
