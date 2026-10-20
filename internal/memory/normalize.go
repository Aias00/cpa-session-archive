package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/nativehistory"
)

const NormalizerVersion = 2

type Message struct {
	Role       string `json:"role"`
	Kind       string `json:"kind,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Model      string `json:"model,omitempty"`
	MessageAt  string `json:"message_at,omitempty"`
	Text       string `json:"text"`
	Side       string `json:"side"`
	Pointer    string `json:"pointer"`
	NativeID   string `json:"native_id,omitempty"`
}
type Document struct {
	Messages []Message `json:"messages"`
	Partial  bool      `json:"partial"`
	Issues   []string  `json:"issues"`
}

func (d *Document) issue(s string) {
	d.Partial = true
	for _, issue := range d.Issues {
		if issue == s {
			return
		}
	}
	d.Issues = append(d.Issues, s)
}
func (d *Document) add(role, text, side, pointer, id string) {
	if strings.TrimSpace(text) != "" {
		m := Message{Role: role, Text: text, Side: side, Pointer: pointer, NativeID: id}
		m.Kind = messageKind(m)
		if role == "tool" {
			m.ToolCallID = id
		}
		d.Messages = append(d.Messages, m)
	}
}
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func arr(v any) []any          { a, _ := v.([]any); return a }
func str(v any) string         { s, _ := v.(string); return s }
func compact(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// Normalize operates on retained payloads, not the truncated UI timeline.
// Pointers locate JSON messages; ranges refer to their versioned normalized text. SSE
// pointers identify reconstructed event groups and are explicitly synthetic.
func Normalize(r archive.Record) Document {
	if r.SourceFormat == nativehistory.Format {
		return normalizeNative(r)
	}
	d := Document{Messages: []Message{}, Issues: []string{}}
	if r.Truncated {
		d.issue("retained payload was truncated")
	}
	if r.Error != "" || r.StatusCode >= 400 {
		d.issue("upstream request did not complete successfully")
	}
	for _, part := range []struct {
		side  string
		raw   []byte
		input bool
	}{{"original_request", r.OriginalRequest, true}, {"response", r.Response, false}} {
		if len(bytes.TrimSpace(part.raw)) == 0 {
			d.issue("missing " + part.side)
			continue
		}
		var root any
		if json.Unmarshal(part.raw, &root) == nil {
			d.payload(obj(root), part.side, "", part.input)
		} else if !part.input && (bytes.Contains(part.raw, []byte("data:")) || bytes.Contains(part.raw, []byte("event:"))) {
			d.stream(part.raw)
		} else {
			d.issue("invalid JSON in " + part.side)
		}
	}
	return d
}
func (d *Document) payload(root map[string]any, side, prefix string, input bool) {
	if root == nil {
		d.issue("unsupported non-object payload")
		return
	}
	if input {
		recognized := false
		for _, key := range []string{"system", "instructions"} {
			if value, ok := root[key]; ok {
				d.content("system", value, side, prefix+"/"+key, "")
				recognized = true
			}
		}
		if messages, ok := root["messages"]; ok {
			recognized = true
			for i, value := range arr(messages) {
				d.message(obj(value), side, fmt.Sprintf("%s/messages/%d", prefix, i))
			}
			if arr(messages) == nil {
				d.issue("messages is not an array")
			}
		}
		if value, ok := root["input"]; ok {
			recognized = true
			if text, ok := value.(string); ok {
				d.add("user", text, side, prefix+"/input", "")
			} else {
				for i, item := range arr(value) {
					d.message(obj(item), side, fmt.Sprintf("%s/input/%d", prefix, i))
				}
				if arr(value) == nil {
					d.issue("unsupported Responses input")
				}
			}
		}
		if !recognized {
			d.issue("unsupported request protocol")
		}
		return
	}
	if choices, ok := root["choices"]; ok {
		for i, item := range arr(choices) {
			choice := obj(item)
			if message := obj(choice["message"]); message != nil {
				d.message(message, side, fmt.Sprintf("%s/choices/%d/message", prefix, i))
			} else if text := str(choice["text"]); text != "" {
				d.add("assistant", text, side, fmt.Sprintf("%s/choices/%d/text", prefix, i), "")
			} else {
				d.issue("unavailable completion message")
			}
		}
		return
	}
	if output, ok := root["output"]; ok {
		for i, item := range arr(output) {
			d.message(obj(item), side, fmt.Sprintf("%s/output/%d", prefix, i))
		}
		return
	}
	if _, ok := root["content"]; ok {
		d.message(root, side, prefix)
		return
	}
	if nested := obj(root["response"]); nested != nil {
		d.payload(nested, side, prefix+"/response", false)
		return
	}
	d.issue("unsupported response protocol")
}
func (d *Document) message(m map[string]any, side, pointer string) {
	if m == nil {
		d.issue("unsupported message item")
		return
	}
	role := str(m["role"])
	if role == "" {
		role = "assistant"
	}
	id := str(m["id"])
	if role == "tool" {
		id = firstID(str(m["tool_call_id"]), id)
	}
	switch str(m["type"]) {
	case "function_call", "custom_tool_call":
		arguments := m["arguments"]
		if str(m["type"]) == "custom_tool_call" {
			arguments = m["input"]
		}
		d.addTool(str(m["name"]), compact(arguments), side, pointer, id, firstID(str(m["call_id"]), id))
		return
	case "function_call_output", "custom_tool_call_output":
		if output, ok := m["output"].(string); ok {
			d.add("tool", output, side, pointer, str(m["call_id"]))
		} else {
			d.content("tool", m["output"], side, pointer+"/output", str(m["call_id"]))
		}
		return
	case "agent_message":
		role = str(m["author"])
		switch role {
		case "user", "assistant", "system", "developer", "tool":
		default:
			role = "agent"
		}
		d.content(role, m["content"], side, pointer+"/content", id)
		return
	case "additional_tools":
		d.issue("tool declarations omitted")
		return
	case "reasoning":
		d.issue("opaque reasoning omitted")
		return
	case "", "message":
	default:
		d.issue("unsupported message type")
		return
	}
	if content, ok := m["content"]; ok {
		d.content(role, content, side, pointer+"/content", id)
	}
	for i, item := range arr(m["tool_calls"]) {
		call := obj(item)
		function := obj(call["function"])
		if function == nil {
			d.issue("unsupported tool call")
			continue
		}
		d.addTool(str(function["name"]), compact(function["arguments"]), side, fmt.Sprintf("%s/tool_calls/%d", pointer, i), str(call["id"]), str(call["id"]))
	}
	if function := obj(m["function_call"]); function != nil {
		d.addTool(str(function["name"]), compact(function["arguments"]), side, pointer+"/function_call", "", "")
	}
}
func (d *Document) content(role string, value any, side, pointer, id string) {
	if value == nil {
		return
	}
	if text, ok := value.(string); ok {
		d.add(role, text, side, pointer, id)
		return
	}
	blocks := arr(value)
	if blocks == nil {
		d.issue("unsupported content value")
		return
	}
	for i, item := range blocks {
		block := obj(item)
		p := fmt.Sprintf("%s/%d", pointer, i)
		switch str(block["type"]) {
		case "text", "input_text", "output_text":
			d.add(role, str(block["text"]), side, p+"/text", id)
		case "tool_use":
			d.addTool(str(block["name"]), compact(block["input"]), side, p, str(block["id"]), str(block["id"]))
		case "tool_result":
			d.content("tool", block["content"], side, p+"/content", str(block["tool_use_id"]))
		case "refusal":
			d.add(role, str(block["refusal"]), side, p+"/refusal", id)
		case "reasoning", "thinking", "redacted_thinking":
			d.issue("opaque reasoning omitted")
		case "image", "input_image", "output_image", "image_url", "audio", "input_audio", "video":
			d.issue("media content omitted")
		default:
			d.issue("opaque or unsupported content block omitted")
		}
	}
}

func (d *Document) stream(raw []byte) {
	// Each retained SSE event can contain multiple data lines. Process the final
	// unterminated event too, but do not treat it as proof of stream completion.
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	events := []string{}
	data := []string{}
	flush := func() {
		if len(data) > 0 {
			events = append(events, strings.Join(data, "\n"))
			data = nil
		}
	}
	for _, line := range lines {
		if line == "" {
			flush()
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	type streamPart struct{ role, text, name, args, id, pointer string }
	parts := map[string]*streamPart{}
	order := []string{}
	complete := false
	family := ""
	var final map[string]any
	part := func(key, pointer string) *streamPart {
		if p := parts[key]; p != nil {
			return p
		}
		p := &streamPart{role: "assistant", pointer: pointer}
		parts[key] = p
		order = append(order, key)
		return p
	}
	for _, event := range events {
		if event == "[DONE]" {
			complete = true
			continue
		}
		var root map[string]any
		if json.Unmarshal([]byte(event), &root) != nil {
			d.issue("malformed SSE event")
			continue
		}
		kind := str(root["type"])
		switch kind {
		case "response.completed":
			final = obj(root["response"])
			complete = true
			family = "responses"
		case "response.output_text.delta":
			family = "responses"
			p := part("responses", "sse:responses/output_text")
			p.text += str(root["delta"])
		case "message_start":
			family = "claude"
			message := obj(root["message"])
			if len(arr(message["content"])) > 0 {
				d.message(message, "response", "sse:claude/message_start/message")
			}
		case "content_block_start":
			family = "claude"
			key := fmt.Sprint(root["index"])
			block := obj(root["content_block"])
			p := part("claude:"+key, "sse:claude/content_blocks/"+key)
			switch str(block["type"]) {
			case "text":
				p.text += str(block["text"])
			case "tool_use":
				p.name = str(block["name"])
				p.id = str(block["id"])
				if input := obj(block["input"]); len(input) > 0 {
					p.args = compact(input)
				}
			case "thinking", "redacted_thinking":
				d.issue("opaque reasoning omitted")
			default:
				d.issue("opaque SSE content omitted")
			}
		case "content_block_delta":
			family = "claude"
			key := fmt.Sprint(root["index"])
			delta := obj(root["delta"])
			p := part("claude:"+key, "sse:claude/content_blocks/"+key)
			switch str(delta["type"]) {
			case "text_delta":
				p.text += str(delta["text"])
			case "input_json_delta":
				p.args += str(delta["partial_json"])
			case "thinking_delta", "signature_delta":
				d.issue("opaque reasoning omitted")
			default:
				d.issue("opaque SSE delta omitted")
			}
		case "message_stop":
			family = "claude"
			complete = true
		case "error", "response.failed", "response.incomplete":
			d.issue("upstream SSE failure")
		}
		if choices, ok := root["choices"]; ok {
			family = "chat"
			for _, item := range arr(choices) {
				choice := obj(item)
				key := fmt.Sprint(choice["index"])
				delta := obj(choice["delta"])
				p := part("chat:"+key, "sse:chat/choices/"+key+"/message")
				if role := str(delta["role"]); role != "" {
					p.role = role
				}
				p.text += str(delta["content"])
				if str(choice["finish_reason"]) != "" {
					complete = true
				}
				for _, tool := range arr(delta["tool_calls"]) {
					call := obj(tool)
					index := fmt.Sprint(call["index"])
					function := obj(call["function"])
					t := part("chat:"+key+":tool:"+index, "sse:chat/choices/"+key+"/tool_calls/"+index)
					t.name += str(function["name"])
					t.args += str(function["arguments"])
					if id := str(call["id"]); id != "" {
						t.id = id
					}
				}
			}
		}
	}
	if final != nil {
		d.payload(final, "response", "sse:response.completed/response", false)
	} else {
		for _, key := range order {
			p := parts[key]
			d.add(p.role, p.text, "response", p.pointer, "")
			if p.name != "" || p.args != "" {
				d.addTool(p.name, p.args, "response", p.pointer+"/function", p.id, p.id)
			}
		}
	}
	if family == "" {
		d.issue("unsupported SSE protocol")
	}
	if !complete {
		d.issue("incomplete retained stream")
	}
}

func firstID(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func (d *Document) addTool(name, args, side, pointer, id, callID string) {
	before := len(d.Messages)
	d.add("assistant", name+"\n"+args, side, pointer, id)
	if len(d.Messages) == before {
		return
	}
	m := &d.Messages[len(d.Messages)-1]
	m.Kind = "tool_use"
	m.ToolName = name
	m.ToolCallID = callID
}
