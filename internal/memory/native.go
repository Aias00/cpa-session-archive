package memory

import (
	"encoding/json"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/nativehistory"
)

func normalizeNative(r archive.Record) Document {
	d := Document{Messages: []Message{}, Issues: []string{}}
	var envelope nativehistory.Envelope
	if json.Unmarshal(r.OriginalRequest, &envelope) != nil {
		d.issue("invalid native history envelope")
		return d
	}
	native := nativehistory.Normalize(envelope)
	d.Partial, d.Issues = native.Partial, native.Issues
	if r.Truncated {
		d.issue("retained native history was truncated")
	}
	for _, message := range native.Messages {
		d.Messages = append(d.Messages, Message{Role: message.Role, Text: message.Text, Side: "original_request", Pointer: message.Pointer, NativeID: message.NativeID, Kind: message.Kind, ToolName: message.ToolName, ToolCallID: message.ToolCallID, Model: message.Model, MessageAt: message.MessageAt})
	}
	return d
}
