package nativehistory

import "testing"

func TestMessageMetadataPreservesLegacyTextAndPointers(t *testing.T) {
	env := envelope("claude-code", `{"type":"user","isMeta":true,"message":{"role":"user","content":"retained context"}}`, `{"type":"assistant","timestamp":"2026-10-18T00:00:00Z","message":{"model":"model-a","content":[{"type":"tool_use","id":"call-a","name":"shell","input":{"command":"echo test"}}]}}`, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-a","content":"tool result"}]}}`)
	d := Normalize(env)
	if len(d.Messages) != 3 {
		t.Fatal(d)
	}
	if d.Messages[0].Kind != "context" || d.Messages[0].Text != "retained context" {
		t.Fatal(d.Messages[0])
	}
	m := d.Messages[1]
	if m.Kind != "tool_use" || m.ToolName != "shell" || m.ToolCallID != "call-a" || m.Model != "model-a" || m.MessageAt != "2026-10-18T00:00:00Z" {
		t.Fatal(m)
	}
	if m.Pointer != "/lines/1/raw/message/content/0" || m.Text != "shell\n{\"command\":\"echo test\"}" {
		t.Fatal("legacy citation changed", m)
	}
	if d.Messages[2].Kind != "tool_result" || d.Messages[2].ToolCallID != "call-a" {
		t.Fatal(d.Messages[2])
	}
}
func TestCodexNativeParentAndRecordedModel(t *testing.T) {
	env := envelope("codex", `{"type":"session_meta","payload":{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}}}`, `{"type":"turn_context","payload":{"model":"model-b"}}`, `{"type":"response_item","payload":{"type":"function_call","id":"item","call_id":"call-b","name":"read","arguments":"{}"}}`)
	if env.Context.ParentID != "parent" || env.Context.AgentRole != "subagent" {
		t.Fatal(env.Context)
	}
	d := Normalize(env)
	if len(d.Messages) != 1 || d.Messages[0].ToolCallID != "call-b" || d.Messages[0].Model != "model-b" || d.Messages[0].Kind != "tool_use" {
		t.Fatal(d)
	}
}
