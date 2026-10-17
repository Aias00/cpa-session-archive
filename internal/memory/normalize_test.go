package memory

import (
	"strings"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestNormalizeRetainedProtocolsAndTools(t *testing.T) {
	cases := []struct {
		name, request, response string
		want                    []string
	}{
		{"chat", `{"messages":[{"role":"user","content":"问题"},{"role":"user","content":"问题"}]}`, `{"choices":[{"message":{"role":"assistant","content":"答案","tool_calls":[{"id":"c","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]}}]}`, []string{"问题", "问题", "答案", "read_file", "a.go"}},
		{"responses", `{"instructions":"rule","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"function_call_output","call_id":"c","output":"exit 1"}]}`, `{"output":[{"type":"function_call","call_id":"d","name":"exec","arguments":"ls"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`, []string{"rule", "hello", "exit 1", "exec", "done"}},
		{"claude", `{"system":"rule","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"test failed"}]}]}`, `{"type":"message","role":"assistant","content":[{"type":"text","text":"answer"},{"type":"tool_use","id":"c","name":"exec","input":{"cmd":"go test"}}]}`, []string{"rule", "test failed", "answer", "exec", "go test"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Normalize(archive.Record{OriginalRequest: []byte(c.request), Response: []byte(c.response)})
			joined := ""
			for _, m := range d.Messages {
				if m.Pointer == "" || m.Side == "" {
					t.Fatal(m)
				}
				joined += m.Text + "\n"
			}
			for _, want := range c.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("missing %q: %+v", want, d)
				}
			}
			if d.Partial {
				t.Fatal(d.Issues)
			}
		})
	}
}
func TestNormalizeLongMessagesAndPartialCoverage(t *testing.T) {
	body := `{"messages":[`
	for i := 0; i < 30; i++ {
		if i > 0 {
			body += ","
		}
		body += `{"role":"user","content":"` + strings.Repeat("长", 41000) + `"}`
	}
	body += `]}`
	d := Normalize(archive.Record{OriginalRequest: []byte(body), Response: []byte(`{"choices":[]}`)})
	if len(d.Messages) != 30 || len([]rune(d.Messages[0].Text)) != 41000 {
		t.Fatalf("truncated normalized data: %d messages", len(d.Messages))
	}
	for _, raw := range []string{`broken`, `{"contents":[{"parts":[{"text":"Gemini"}]}]}`, `{"messages":[{"role":"user","content":[{"type":"image","source":{"data":"PRIVATE_BASE64"}}]}]}`} {
		d := Normalize(archive.Record{OriginalRequest: []byte(raw), Truncated: true})
		if !d.Partial || len(d.Issues) == 0 {
			t.Fatal(d)
		}
		for _, m := range d.Messages {
			if strings.Contains(m.Text, "PRIVATE_BASE64") {
				t.Fatal("indexed opaque media")
			}
		}
	}
}
func TestNormalizeStreamingEvidence(t *testing.T) {
	cases := []struct {
		name, response, want string
		partial              bool
	}{
		{"chat", "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "hello world", false},
		{"responses", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"final\"}]}]}}\n\n", "final", false},
		{"claude", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n", "partial", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Normalize(archive.Record{OriginalRequest: []byte(`{"messages":[{"role":"user","content":"start"}]}`), Response: []byte(c.response), Stream: true})
			found := false
			for _, m := range d.Messages {
				if m.Text == c.want {
					found = true
					if !strings.HasPrefix(m.Pointer, "sse:") {
						t.Fatal(m)
					}
				}
			}
			if !found || d.Partial != c.partial {
				t.Fatalf("%+v", d)
			}
		})
	}
}
