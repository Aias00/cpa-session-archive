package nativehistory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any           { a, _ := v.([]any); return a }
func text(v any) string           { s, _ := v.(string); return s }
func first(values ...string) string {
	for _, s := range values {
		if s != "" {
			return s
		}
	}
	return ""
}
func compact(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}
func timestamp(v any) time.Time { s := text(v); t, _ := time.Parse(time.RFC3339Nano, s); return t }

func inspectWorkspace(c *Context, cwd string) {
	if cwd != "" && c.CWD != "" && cwd != c.CWD {
		c.ScopeAmbiguous = true
	}
	c.CWD = first(c.CWD, cwd)
}

// Inspect gathers file context before segmenting, so mirrored Codex event_msg
// records are not indexed in addition to authoritative response_item records.
func Inspect(client string, raw []byte, c *Context) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = timestamp(m["timestamp"])
	}
	switch client {
	case "codex":
		p := object(m["payload"])
		switch text(m["type"]) {
		case "session_meta":
			inspectIdentity(c, p)
			c.SessionID = first(text(p["id"]), text(p["session_id"]), c.SessionID)
			c.ParentID = first(text(p["parent_thread_id"]), text(p["forked_from_id"]), c.ParentID)
			c.Branch = first(text(p["agent_path"]), c.Branch)
			inspectWorkspace(c, text(p["cwd"]))
			remote := text(object(p["git"])["repository_url"])
			if c.GitRemote != "" && remote != "" && c.GitRemote != remote {
				c.ScopeAmbiguous = true
			}
			c.GitRemote = first(c.GitRemote, remote)
			if t := timestamp(p["timestamp"]); !t.IsZero() {
				c.CreatedAt = t
			}
		case "turn_context":
			inspectWorkspace(c, text(p["cwd"]))
			c.Model = first(c.Model, text(p["model"]))
		case "response_item":
			c.CodexResponses = true
		}
	case "claude-code", "qoder", "qoder-cn":
		c.SessionID = first(text(m["sessionId"]), c.SessionID)
		inspectWorkspace(c, text(m["cwd"]))
		c.Model = first(c.Model, text(object(m["message"])["model"]))
		if p := text(m["sourceToolAssistantUUID"]); p != "" {
			c.ParentID = first(c.ParentID, p)
		}
	case "workbuddy", "codebuddy":
		c.SessionID = first(text(m["sessionId"]), c.SessionID)
		inspectWorkspace(c, text(m["cwd"]))
		c.Model = first(c.Model, text(object(m["providerData"])["model"]))
		if c.CreatedAt.IsZero() {
			c.CreatedAt = additionalTime(m["timestamp"])
		}
		c.Title = first(text(m["customTitle"]), text(m["aiTitle"]), c.Title)
	case "kiro-cli":
		c.Title = first(text(m["title"]), c.Title)
		c.SessionID = first(c.SessionID, text(m["session_id"]))
		inspectWorkspace(c, text(m["cwd"]))
		if t := timestamp(m["created_at"]); !t.IsZero() {
			c.CreatedAt = t
		}
		if c.CreatedAt.IsZero() {
			c.CreatedAt = timestamp(object(object(m["data"])["meta"])["timestamp"])
		}
		c.Model = first(c.Model, text(object(object(object(m["session_state"])["rts_model_state"])["model_info"])["model_id"]))
	}
}
func (d *Document) issue(s string) {
	d.Partial = true
	for _, v := range d.Issues {
		if v == s {
			return
		}
	}
	d.Issues = append(d.Issues, s)
}
func (d *Document) add(role, value, pointer, id string) {
	if strings.TrimSpace(value) != "" {
		d.Messages = append(d.Messages, Message{Role: role, Text: value, Pointer: pointer, NativeID: id})
	}
}
func (d *Document) content(role string, v any, p, id string) {
	if s, ok := v.(string); ok {
		d.add(role, s, p, id)
		return
	}
	if v == nil {
		return
	}
	a := array(v)
	if a == nil {
		d.issue("unsupported native content value")
		return
	}
	for i, v := range a {
		b := object(v)
		q := fmt.Sprintf("%s/%d", p, i)
		kind := first(text(b["type"]), text(b["kind"]))
		switch kind {
		case "text", "Text", "input_text", "output_text":
			d.add(role, first(text(b["text"]), text(b["data"])), q, id)
		case "tool_use":
			d.add("assistant", text(b["name"])+"\n"+compact(b["input"]), q, text(b["id"]))
		case "toolUse":
			data := object(b["data"])
			d.add("assistant", text(data["name"])+"\n"+compact(data["input"]), q, text(data["toolUseId"]))
		case "tool_result":
			d.content("tool", b["content"], q+"/content", text(b["tool_use_id"]))
		case "toolResult":
			data := object(b["data"])
			d.content("tool", data["content"], q+"/data/content", text(data["toolUseId"]))
		case "refusal":
			d.add(role, text(b["refusal"]), q, id)
		default:
			d.issue("opaque or unsupported native content omitted")
		}
	}
}
func (d *Document) codexItem(m map[string]any, p string) {
	role := first(text(m["role"]), text(m["author"]), "assistant")
	id := first(text(m["id"]), text(m["call_id"]))
	switch text(m["type"]) {
	case "UserMessage":
		d.content("user", m["content"], p+"/content", id)
	case "AgentMessage", "message", "agent_message", "":
		switch role {
		case "user", "assistant", "system", "developer", "tool":
		default:
			role = "agent"
		}
		d.content(role, m["content"], p+"/content", id)
	case "function_call":
		d.add("assistant", text(m["name"])+"\n"+compact(m["arguments"]), p, id)
	case "custom_tool_call":
		d.add("assistant", text(m["name"])+"\n"+compact(m["input"]), p, id)
	case "function_call_output", "custom_tool_call_output":
		d.content("tool", m["output"], p+"/output", id)
	default:
		d.issue("opaque or unsupported Codex response item omitted")
	}
}
func Normalize(e Envelope) Document {
	d := Document{Messages: []Message{}, Issues: []string{}}
	if e.Version != 1 {
		d.issue("unsupported native envelope version")
		return d
	}
	model := ""
	for i, line := range e.Lines {
		if len(bytes.TrimSpace([]byte(line.Raw))) == 0 {
			continue
		}
		p := fmt.Sprintf("/lines/%d/raw", i)
		var m map[string]any
		if json.Unmarshal([]byte(line.Raw), &m) != nil || m == nil {
			d.issue("malformed or auxiliary native record retained")
			continue
		}
		start := len(d.Messages)
		if e.Client == "codex" && text(m["type"]) == "turn_context" {
			model = text(object(m["payload"])["model"])
		}
		switch e.Client {
		case "codex":
			v := object(m["payload"])
			switch text(m["type"]) {
			case "response_item":
				d.codexItem(v, p+"/payload")
			case "event_msg":
				if e.Context.CodexResponses {
					continue
				}
				switch text(v["type"]) {
				case "user_message":
					d.add("user", text(v["message"]), p+"/payload/message", text(v["id"]))
				case "agent_message":
					d.add("assistant", text(v["message"]), p+"/payload/message", text(v["id"]))
				default:
					if item := object(v["item"]); item != nil {
						d.codexItem(item, p+"/payload/item")
					}
				}
			case "compacted":
				d.issue("compacted context: replacement snapshot not replayed")
				d.add("summary", text(v["message"]), p+"/payload/message", "")
			case "session_meta", "turn_context", "world_state", "token_usage_record", "inter_agent_communication_metadata":
			default:
				d.issue("unsupported Codex event retained")
			}
		case "claude-code", "qoder", "qoder-cn":
			switch text(m["type"]) {
			case "user", "assistant":
				v := object(m["message"])
				d.content(first(text(v["role"]), text(m["type"])), v["content"], p+"/message/content", text(m["uuid"]))
				if apiErr, ok := m["isApiErrorMessage"].(bool); ok && apiErr {
					d.issue("native client recorded an API error")
				}
			case "system":
				if text(m["subtype"]) == "compact_boundary" {
					d.issue("compacted Claude context retained")
				}
			case "summary":
				d.issue("Claude compaction summary retained without snapshot replay")
				d.add("summary", text(m["summary"]), p+"/summary", text(m["leafUuid"]))
			case "queue-operation", "attachment", "last-prompt", "mode", "permission-mode", "file-history-snapshot", "ai-title", "progress":
			default:
				d.issue("unsupported Claude event retained")
			}
		case "opencode", "zcode":
			d.databaseEvent(m, p)
			continue
		case "workbuddy", "codebuddy":
			d.buddyEvent(m, p)
		case "kiro-cli":
			v := object(m["data"])
			switch text(m["kind"]) {
			case "Prompt":
				d.content("user", v["content"], p+"/data/content", text(v["message_id"]))
			case "AssistantMessage":
				d.content("assistant", v["content"], p+"/data/content", text(v["message_id"]))
			case "ToolResults":
				d.content("tool", v["content"], p+"/data/content", text(v["message_id"]))
			case "Compaction":
				d.issue("compacted Kiro context: snapshot not replayed")
				d.add("summary", text(v["summary"]), p+"/data/summary", "")
			default:
				d.issue("Kiro metadata or unsupported event retained without indexing")
			}
		default:
			d.issue("auxiliary native source retained without indexing")
		}
		annotateMessages(d.Messages[start:], m, p, e.Client, model)
	}
	return d
}
