package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"

	"cpa-session-archive/internal/nativehistory"
)

// Native segments already have bounded source boundaries. Keeping one raw blob
// avoids re-encoding originals and allocating a compressor per JSONL line.
func putNativePayload(tx *sql.Tx, raw []byte) (string, error) {
	blob := newBlob(raw, "application/vnd.cpa.native-history+json")
	if err := putBlobContext(context.Background(), tx, blob); err != nil {
		return "", err
	}
	manifest, err := json.Marshal(blobRef{Blob: blob.Hash, Encoding: "raw", Size: len(raw)})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(manifest)
	root := Blob{Hash: "sha256:" + hex.EncodeToString(sum[:]), RawSize: int64(len(manifest)), MediaType: "application/vnd.cpa.archive-manifest+json", Data: manifest}
	if err := putBlobContext(context.Background(), tx, root); err != nil {
		return "", err
	}
	return root.Hash, nil
}

func nativeDocument(raw []byte) nativehistory.Document {
	var envelope nativehistory.Envelope
	if json.Unmarshal(raw, &envelope) != nil {
		return nativehistory.Document{Partial: true, Issues: []string{"invalid native history envelope"}}
	}
	return nativehistory.Normalize(envelope)
}

func nativeTimeline(r Record) RequestTimelineView {
	view := RequestTimelineView{
		RequestID: r.RequestID, SessionID: r.SessionID, Summary: r.Summary,
		RequestedModel: r.RequestedModel, Model: r.Model, Outcome: r.Outcome,
		StartedAt: r.StartedAt, CompletedAt: r.CompletedAt, Facets: r.Facets,
		Kind: "native-history", Entries: []TimelineEntry{},
	}
	for _, message := range nativeDocument(r.OriginalRequest).Messages {
		runes := []rune(message.Text)
		truncated := len(runes) > 40000
		if truncated {
			runes = runes[:40000]
		}
		view.Entries = append(view.Entries, TimelineEntry{Role: message.Role, Kind: message.Kind, Model: message.Model, MessageAt: message.MessageAt, Label: message.ToolName, Text: string(runes), CallID: firstNonEmpty(message.ToolCallID, message.NativeID), Truncated: truncated})
	}
	view.InputItems = len(view.Entries)
	return view
}

func nativeUserText(raw []byte) string {
	var envelope nativehistory.Envelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Version != 1 || envelope.Client == "" {
		return ""
	}
	var values []string
	for _, message := range nativehistory.Normalize(envelope).Messages {
		if message.Role == "user" {
			values = append(values, message.Text)
		}
	}
	return strings.Join(values, "\n\n")
}
