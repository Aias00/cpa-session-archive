package nativehistory

import (
	"strconv"
	"strings"
)

func inspectIdentity(c *Context, p map[string]any) {
	source := p["source"]
	role, parent := "", ""
	if s, ok := source.(string); ok {
		switch s {
		case "cli", "vscode", "exec", "mcp":
			role = "main"
		}
	}
	sub := object(object(source)["subagent"])
	if sub != nil {
		role = "subagent"
		parent = text(object(sub["thread_spawn"])["parent_thread_id"])
	}
	explicit := first(text(p["parent_thread_id"]), text(p["forked_from_id"]))
	if parent != "" && explicit != "" && parent != explicit || role == "main" && explicit != "" {
		c.IdentityConflict = true
	}
	parent = first(parent, explicit)
	if parent != "" {
		if c.ParentID != "" && c.ParentID != parent {
			c.IdentityConflict = true
		} else {
			c.ParentID = parent
		}
		if role == "" {
			role = "subagent"
		}
	}
	if c.IdentityConflict {
		c.AgentRole = "unknown"
	} else if role != "" {
		c.AgentRole = role
	}
}

// Metadata is additive: the text, pointer, role and normalizer-v2 ranges never
// change. Tool ancestors distinguish a result's text from a dialogue message.
func annotateMessages(messages []Message, root map[string]any, prefix, client, model string) {
	at := text(root["timestamp"])
	if client == "kiro-cli" {
		at = text(object(object(root["data"])["meta"])["timestamp"])
	}
	if client == "claude-code" {
		model = text(object(root["message"])["model"])
	}
	for i := range messages {
		m := &messages[i]
		m.Kind = "text"
		m.Model = model
		m.MessageAt = at
		switch m.Role {
		case "system", "developer":
			m.Kind = "context"
		case "tool":
			m.Kind = "tool_result"
			m.ToolCallID = m.NativeID
		case "summary":
			m.Kind = "summary"
		}
		if meta, ok := root["isMeta"].(bool); ok && meta {
			m.Kind = "context"
		}
		node := any(root)
		visit := func(value any) {
			b := object(value)
			typ := first(text(b["type"]), text(b["kind"]))
			switch typ {
			case "function_call", "custom_tool_call", "local_shell_call", "tool_use":
				m.Kind = "tool_use"
				m.ToolName = text(b["name"])
				m.ToolCallID = first(text(b["call_id"]), text(b["id"]))
			case "toolUse":
				v := object(b["data"])
				m.Kind = "tool_use"
				m.ToolName = text(v["name"])
				m.ToolCallID = text(v["toolUseId"])
			case "function_call_output", "custom_tool_call_output":
				m.Kind = "tool_result"
				m.ToolCallID = text(b["call_id"])
			case "tool_result":
				m.Kind = "tool_result"
				m.ToolCallID = text(b["tool_use_id"])
			case "toolResult":
				m.Kind = "tool_result"
				m.ToolCallID = text(object(b["data"])["toolUseId"])
			}
		}
		visit(node)
		for _, key := range strings.Split(strings.TrimPrefix(strings.TrimPrefix(m.Pointer, prefix), "/"), "/") {
			switch v := node.(type) {
			case map[string]any:
				node = v[key]
			case []any:
				n, err := strconv.Atoi(key)
				if err != nil || n < 0 || n >= len(v) {
					node = nil
				} else {
					node = v[n]
				}
			default:
				node = nil
			}
			visit(node)
		}
	}
}
