package nativehistory

import (
	"encoding/json"
	"strings"
	"testing"
)

func envelope(client string, lines ...string) Envelope {
	e := Envelope{Version: 1, Client: client}
	for i, line := range lines {
		Inspect(client, []byte(line), &e.Context)
		e.Lines = append(e.Lines, Line{Offset: int64(i), Raw: line})
	}
	return e
}
func TestAdaptersNativeTextToolsAndSnapshots(t *testing.T) {
	cases := []struct {
		client string
		lines  []string
		want   []string
		absent string
	}{
		{"codex", []string{
			`{"type":"session_meta","payload":{"id":"thread","cwd":"/work","forked_from_id":"parent","git":{"repository_url":"https://example.test/repo"}}}`,
			`{"type":"event_msg","payload":{"type":"user_message","message":"duplicate-event"}}`,
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"native-question"}]}}`,
			`{"type":"response_item","payload":{"type":"custom_tool_call","name":"patch","call_id":"call","input":"native-patch"}}`,
			`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call","output":"native-result"}}`,
			`{"type":"compacted","payload":{"message":"native-summary","replacement_history":[{"role":"user","content":"snapshot-duplicate"}]}}`,
		}, []string{"native-question", "native-patch", "native-result", "native-summary"}, "duplicate-event"},
		{"claude-code", []string{
			`{"type":"user","uuid":"u1","sessionId":"s","cwd":"/work","message":{"role":"user","content":"native-question"}}`,
			`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"shell","input":{"command":"native-patch"}}]}}`,
			`{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"native-result"}]}}`,
			`{"type":"file-history-snapshot","snapshot":{"content":"snapshot-duplicate"}}`,
		}, []string{"native-question", "native-patch", "native-result"}, "snapshot-duplicate"},
		{"kiro-cli", []string{
			`{"version":"1","kind":"Prompt","data":{"message_id":"u","content":[{"kind":"text","data":"native-question"}],"meta":{"timestamp":"2026-10-18T00:00:00Z"}}}`,
			`{"kind":"AssistantMessage","data":{"message_id":"a","content":[{"kind":"toolUse","data":{"toolUseId":"t","name":"shell","input":{"command":"native-patch"}}},{"kind":"thinking","data":{"text":"opaque-secret"}}]}}`,
			`{"kind":"ToolResults","data":{"message_id":"r","content":[{"kind":"toolResult","data":{"toolUseId":"t","content":[{"kind":"text","data":"native-result"}]}}],"results":{"t":{"text":"duplicate-result"}}}}`,
			`{"kind":"Compaction","data":{"summary":"native-summary","messages_snapshot":[{"role":"user","content":"snapshot-duplicate"}]}}`,
		}, []string{"native-question", "native-patch", "native-result", "native-summary"}, "opaque-secret"},
	}
	for _, c := range cases {
		t.Run(c.client, func(t *testing.T) {
			e := envelope(c.client, c.lines...)
			d := Normalize(e)
			var text string
			seen := map[string]bool{}
			for _, m := range d.Messages {
				text += m.Text + "\n"
				if seen[m.Pointer] {
					t.Fatalf("duplicate pointer %s", m.Pointer)
				}
				seen[m.Pointer] = true
			}
			for _, w := range c.want {
				if !strings.Contains(text, w) {
					t.Fatalf("missing %s: %+v", w, d)
				}
			}
			if strings.Contains(text, c.absent) || strings.Contains(text, "snapshot-duplicate") {
				t.Fatal("indexed replay/opaque data")
			}
			if c.client == "codex" && (e.Context.SessionID != "thread" || e.Context.ParentID != "parent" || !e.Context.CodexResponses) {
				t.Fatalf("context %+v", e.Context)
			}
		})
	}
}
func TestFallbackAndMalformedAreExplicit(t *testing.T) {
	d := Normalize(envelope("codex", `{"type":"event_msg","payload":{"type":"user_message","message":"fallback"}}`, `{broken}`))
	if len(d.Messages) != 1 || d.Messages[0].Text != "fallback" || !d.Partial {
		t.Fatalf("fallback %+v", d)
	}
	e := envelope("codex", `{"type":"response_item","payload":{"type":"reasoning","encrypted_content":"secret"}}`)
	if d = Normalize(e); !d.Partial || len(d.Messages) != 0 {
		t.Fatalf("opaque %+v", d)
	}
	raw, _ := json.Marshal(e)
	if !strings.Contains(string(raw), "encrypted_content") {
		t.Fatal("original lost")
	}
}

func BenchmarkInspectWorldState(b *testing.B) {
	raw := []byte(`{"type":"world_state","timestamp":"2026-10-18T00:00:00Z","payload":{"snapshot":"` + strings.Repeat("x", 1<<20) + `"}}`)
	c := Context{}
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Inspect("codex", raw, &c)
	}
}

func TestCodexItemCompletedFallbackAndClaudeSummary(t *testing.T) {
	d := Normalize(envelope("codex", `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u","content":[{"type":"text","text":"fallback-question"}]}}}`, `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"a","content":[{"type":"Text","text":"fallback-answer"}]}}}`))
	if len(d.Messages) != 2 || d.Messages[0].Role != "user" || d.Messages[1].Text != "fallback-answer" {
		t.Fatalf("fallback %+v", d)
	}
	d = Normalize(envelope("claude-code", `{"type":"summary","summary":"retained-summary"}`))
	if len(d.Messages) != 1 || !d.Partial {
		t.Fatalf("summary %+v", d)
	}
}
