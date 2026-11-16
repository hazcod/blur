package profile

import "strings"

// ParseExplicit recognizes a direct request to save the user's own words. It
// does not derive rules from captures or turn recall questions into assertions.
func ParseExplicit(input string) (Kind, string, bool) {
	text := strings.TrimSpace(input)
	lower := strings.ToLower(text)
	for _, prefix := range []struct {
		text string
		kind Kind
	}{
		{"writing rule:", WritingRule}, {"rule:", WritingRule}, {"preference:", Fact}, {"remember:", Fact},
	} {
		if strings.HasPrefix(lower, prefix.text) {
			return prefix.kind, strings.TrimSpace(text[len(prefix.text):]), true
		}
	}
	const prefix = "remember that "
	if !strings.HasPrefix(lower, prefix) || strings.ContainsAny(text, "?？") {
		return "", "", false
	}
	body := strings.TrimSpace(text[len(prefix):])
	words := strings.Fields(strings.ToLower(body))
	if len(words) == 0 {
		return "", "", false
	}
	switch strings.Trim(words[0], ".,;:!\"'") {
	case "article", "page", "tab", "thing", "conversation", "chat", "document", "note", "email", "message", "meeting", "call", "file", "website", "post":
		return "", "", false
	}
	// Without a delimiter require an assertion, rather than "remember that
	// article". Other natural forms can be clarified by the intent parser.
	for _, word := range words {
		switch strings.Trim(word, ".,;:!\"'") {
		case "am", "is", "are", "was", "were", "prefer", "prefers", "use", "uses", "work", "works", "live", "lives", "like", "likes", "want", "wants", "need", "needs", "have", "has":
			return Fact, body, true
		}
	}
	return "", "", false
}
