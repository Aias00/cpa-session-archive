package memory

import (
	"encoding/json"
	"strings"

	"cpa-session-archive/internal/archive"
)

// IsInternalSource recognizes explicit structured request tags, never words in
// conversation text. Extraction callers must set request_kind in the supported
// X-Codex-Turn-Metadata header when routing model calls through the archive.
func IsInternalSource(r archive.Record) bool {
	internal := func(value string) bool {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "memory", "memory-extraction":
			return true
		}
		return false
	}
	if internal(r.RequestKind) {
		return true
	}
	for _, kind := range r.Facets["request.kind"] {
		if internal(kind) {
			return true
		}
	}
	var tagged func(any, int) bool
	tagged = func(value any, depth int) bool {
		if depth > 4 {
			return false
		}
		if raw, ok := value.(string); ok {
			var decoded map[string]any
			if json.Unmarshal([]byte(raw), &decoded) != nil {
				return false
			}
			value = decoded
		}
		metadata, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if kind, ok := metadata["request_kind"].(string); ok && internal(kind) {
			return true
		}
		for _, key := range []string{"metadata", "client_metadata", "x-codex-turn-metadata", "X-Codex-Turn-Metadata"} {
			if tagged(metadata[key], depth+1) {
				return true
			}
		}
		return false
	}
	if tagged(r.Metadata, 0) {
		return true
	}
	var root map[string]any
	return json.Unmarshal(r.OriginalRequest, &root) == nil && tagged(root, 0)
}
