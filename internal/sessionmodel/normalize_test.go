package sessionmodel

import "testing"

func TestResponsePreviewAcrossHostProtocols(t *testing.T) {
	for _, raw := range []string{
		`{"choices":[{"message":{"role":"assistant","content":"shared answer"}}]}`,
		`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"shared answer"}]}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"shared answer"}]}`,
	} {
		if got := ExtractResponsePreview([]byte(raw)); got != "shared answer" {
			t.Fatalf("lost host response preview: %q", got)
		}
	}
}
