package nativehistory

import (
	"encoding/json"
	"testing"
)

func TestQoderAndBuddyConversations(t *testing.T) {
	q := envelope("qoder", `{"type":"user","sessionId":"s","cwd":"/project","message":{"role":"user","content":"qoder question"}}`, `{"type":"assistant","message":{"role":"assistant","content":"qoder answer"}}`)
	if q.Context.CWD != "/project" || len(Normalize(q).Messages) != 2 {
		t.Fatal("Qoder conversation missing")
	}
	for _, client := range []string{"workbuddy", "codebuddy"} {
		e := envelope(client, `{"type":"message","role":"user","sessionId":"s","cwd":"/project","timestamp":1792368000000,"content":[{"type":"input_text","text":"buddy question"}]}`, `{"type":"function_call","name":"shell","callId":"call1","arguments":"{}","providerData":{"model":"model-a"}}`, `{"type":"function_call_result","name":"shell","callId":"call1","output":{"type":"text","text":"buddy result"}}`)
		d := Normalize(e)
		if len(d.Messages) != 3 {
			t.Fatalf("%s messages: %+v", client, d)
		}
		if d.Messages[1].Kind != "tool_use" || d.Messages[1].ToolCallID != "call1" || d.Messages[2].Kind != "tool_result" || d.Messages[2].ToolCallID != "call1" {
			t.Fatal(d)
		}
		if d.Messages[0].MessageAt == "" || e.Context.CWD != "/project" {
			t.Fatal("missing context/time")
		}
	}
}

func TestDatabaseMessageSnapshots(t *testing.T) {
	for _, client := range []string{"opencode", "zcode"} {
		raw, _ := json.Marshal(DatabaseEvent{Version: 1, Table: "session_message", RowID: "m1", Role: "assistant", At: "2026-10-19T00:00:00Z", Data: `{"model":{"id":"model-a"},"content":[{"type":"text","text":"answer"},{"type":"tool","tool":"shell","callID":"call1","state":{"input":{"cmd":"pwd"},"output":"result"}}]}`})
		d := Normalize(Envelope{Version: 1, Client: client, Lines: []Line{{Raw: string(raw)}}})
		if len(d.Messages) != 3 || d.Messages[0].Text != "answer" || d.Messages[0].Model != "model-a" || d.Messages[1].Kind != "tool_use" || d.Messages[2].Kind != "tool_result" {
			t.Fatalf("%s %+v", client, d)
		}
	}
}
