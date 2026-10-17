package memory

import (
	"encoding/hex"
	"strings"
	"unicode"
)

// Encode terms so punctuation and FTS operators can never become query syntax.
// CJK characters and adjacent bigrams allow short Chinese literal searches.
func lexicalTokens(text string) []string {
	out := []string{}
	seen := map[string]bool{}
	var word []rune
	var previous rune
	add := func(s string) {
		token := "w" + hex.EncodeToString([]byte(s))
		if !seen[token] {
			out = append(out, token)
			seen[token] = true
		}
	}
	flush := func() {
		if len(word) > 0 {
			add(string(word))
			word = nil
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			flush()
			add(string(r))
			if previous != 0 {
				add(string([]rune{previous, r}))
			}
			previous = r
		} else {
			previous = 0
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				word = append(word, r)
			} else {
				flush()
			}
		}
	}
	flush()
	return out
}
func indexTokens(text string) string { return strings.Join(lexicalTokens(text), " ") }
