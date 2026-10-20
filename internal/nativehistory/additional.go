package nativehistory

import (
	"encoding/json"
	"fmt"
	"time"
)

// DatabaseEvent retains exact source JSON strings, not a database file or secrets table.
// Citations address the decoded JSON within data_raw/parts, like native JSONL raw.
type DatabaseEvent struct {
	Version int            `json:"version"`
	Context Context        `json:"session_context"`
	Table   string         `json:"table"`
	RowID   string         `json:"row_id"`
	Role    string         `json:"role"`
	At      string         `json:"timestamp"`
	Data    string         `json:"data_raw"`
	Parts   []DatabasePart `json:"parts,omitempty"`
}
type DatabasePart struct {
	ID   string `json:"id"`
	Data string `json:"data_raw"`
}

func additionalTime(value any) time.Time {
	if n, ok := value.(float64); ok && n > 0 {
		return time.UnixMilli(int64(n)).UTC()
	}
	return timestamp(value)
}
func (d *Document) buddyEvent(m map[string]any, p string) {
	switch text(m["type"]) {
	case "message":
		d.content(first(text(m["role"]), "assistant"), m["content"], p+"/content", text(m["id"]))
	case "function_call":
		d.add("assistant", text(m["name"])+"\n"+compact(m["arguments"]), p, first(text(m["callId"]), text(m["id"])))
	case "function_call_result":
		if output := object(m["output"]); output != nil {
			switch text(output["type"]) {
			case "text", "output_text":
				d.add("tool", text(output["text"]), p+"/output/text", first(text(m["callId"]), text(m["id"])))
			default:
				d.issue("unsupported Buddy tool output retained")
			}
		} else {
			d.content("tool", m["output"], p+"/output", first(text(m["callId"]), text(m["id"])))
		}
	case "reasoning":
		d.issue("opaque Buddy reasoning retained without indexing")
	case "ai-title", "custom-title", "session-meta", "file-history-snapshot":
	default:
		d.issue("unsupported Buddy event retained")
	}
}
func (d *Document) databaseEvent(root map[string]any, p string) {
	if root["version"] != float64(1) {
		d.issue("unsupported database snapshot version")
		return
	}
	var m map[string]any
	if json.Unmarshal([]byte(text(root["data_raw"])), &m) != nil || m == nil {
		d.issue("invalid database message retained")
		return
	}
	role := first(text(root["role"]), text(m["role"]), "assistant")
	model := first(text(object(m["model"])["id"]), text(m["modelID"]), text(m["modelId"]))
	at := text(root["timestamp"])
	start := len(d.Messages)
	if text(root["table"]) == "session_message" {
		if role == "user" || role == "system" {
			d.add(role, text(m["text"]), p+"/data_raw/text", text(root["row_id"]))
		} else if role == "compaction" {
			d.issue("database compaction retained without snapshot replay")
		} else {
			for i, v := range array(m["content"]) {
				d.databasePart(role, object(v), fmt.Sprintf("%s/data_raw/content/%d", p, i), text(root["row_id"]))
			}
		}
	} else {
		for i, v := range array(root["parts"]) {
			part := object(v)
			var content map[string]any
			if json.Unmarshal([]byte(text(part["data_raw"])), &content) != nil {
				d.issue("invalid database part retained")
				continue
			}
			d.databasePart(role, content, fmt.Sprintf("%s/parts/%d/data_raw", p, i), text(part["id"]))
		}
	}
	for i := start; i < len(d.Messages); i++ {
		m := &d.Messages[i]
		m.MessageAt = at
		if m.Role == "assistant" {
			m.Model = model
		}
		if m.Kind == "" {
			m.Kind = "text"
			if m.Role == "system" || m.Role == "developer" {
				m.Kind = "context"
			}
		}
	}
}
func (d *Document) databasePart(role string, m map[string]any, p, id string) {
	if m == nil {
		return
	}
	switch text(m["type"]) {
	case "text":
		start := len(d.Messages)
		d.add(role, text(m["text"]), p+"/text", id)
		if m["synthetic"] == true || m["ignored"] == true {
			for i := start; i < len(d.Messages); i++ {
				d.Messages[i].Kind = "context"
			}
		}
	case "tool":
		name := first(text(m["tool"]), text(m["name"]))
		call := first(text(m["callID"]), text(m["callId"]), text(m["id"]), id)
		state := object(m["state"])
		start := len(d.Messages)
		d.add("assistant", name+"\n"+compact(state["input"]), p+"/state/input", call)
		for i := start; i < len(d.Messages); i++ {
			d.Messages[i].Kind = "tool_use"
			d.Messages[i].ToolName = name
			d.Messages[i].ToolCallID = call
		}
		start = len(d.Messages)
		d.content("tool", state["output"], p+"/state/output", call)
		d.content("tool", state["content"], p+"/state/content", call)
		for i := start; i < len(d.Messages); i++ {
			d.Messages[i].Kind = "tool_result"
			d.Messages[i].ToolName = name
			d.Messages[i].ToolCallID = call
		}
	case "step-start", "step-finish":
	case "reasoning", "thinking":
		d.issue("opaque reasoning omitted")
	default:
		d.issue("opaque or unsupported database part retained")
	}
}
