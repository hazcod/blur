package client

import (
	"encoding/csv"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/intent"
)

var entryCodeExpression = regexp.MustCompile(`(?s)^[\pL_][\pL\pN_]*(?:\.[\pL_][\pL\pN_]*)*\s*(?:(?::=|=)\s*.+|\(.*\)\s*;?)$`)
var entryNumericFunction = regexp.MustCompile(`(?i)^(?:sqrt|abs|sin|cos|tan|log|ln|exp|pow|round|floor|ceil)\s*\([0-9.e+*/%,^()\s-]+\)\s*[?？؟!]*$`)
var entryCodeDeclaration = regexp.MustCompile(`(?m)^(?:func[ \t]+[\pL_][\pL\pN_]*\s*\(|(?:async[ \t]+)?def[ \t]+[\pL_][\pL\pN_]*\s*\(|class[ \t]+[\pL_][\pL\pN_]*\s*(?:\(|:|\{)|package[ \t]+[\pL_][\pL\pN_]*[ \t]*\r?\n|#include[ \t]*[<"])`)
var entrySQLStatement = regexp.MustCompile(`(?is)^(?:select\s+.+\s+from\s+.+|insert\s+into\s+.+|update\s+\S+\s+set\s+.+|delete\s+from\s+.+|create\s+table\s+.+);$`)
var entryDataHeader = regexp.MustCompile(`^[\pL_][\pL\pN_-]*$`)
var entryQuotePairs = [][2]string{{`"`, `"`}, {"'", "'"}, {"“", "”"}, {"‘", "’"}, {"«", "»"}}

// Raw data has no current instruction. Ask about its use before the default
// note route or a model can mistake quoted commands for user authority.
// Explicit note saving and recognizable meeting notes take precedence.
func entryDataClarification(input string) string {
	if !utf8.ValidString(input) || strings.ContainsRune(input, 0) {
		return "This text contains invalid characters. Remove null bytes or paste it as ordinary text, then try again."
	}
	q := strings.TrimSpace(input)
	if strings.HasPrefix(q, "```") || strings.HasPrefix(q, "~~~") || strings.HasPrefix(q, "$ ") || strings.HasPrefix(q, "% ") || strings.HasPrefix(q, "#!") || entryCodeExpression.MatchString(q) || entryCodeDeclaration.MatchString(q) || entrySQLStatement.MatchString(q) {
		return "What would you like to do with this code: explain it, change it, or save it as a note?"
	}
	if (strings.HasPrefix(q, "{") || strings.HasPrefix(q, "[")) && json.Valid([]byte(q)) || strings.HasPrefix(q, "---\n") || entryDelimitedData(q) {
		return "What would you like to do with this data: analyze it or save it as a note?"
	}
	if u, err := url.ParseRequestURI(q); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return "Would you like to save this link as a note, or ask about content you have already captured?"
	}
	for _, quotes := range entryQuotePairs {
		if len(q) >= len(quotes[0])+len(quotes[1]) && strings.HasPrefix(q, quotes[0]) && strings.HasSuffix(q, quotes[1]) {
			return "What would you like me to do with this quoted text: discuss it or save it as a note?"
		}
	}
	if !strings.ContainsFunc(q, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
		return "What would you like me to do with this? You can ask a question or save it with “note:”."
	}
	return ""
}

func entryDelimitedData(input string) bool {
	if !strings.Contains(input, "\n") {
		return false
	}
	for _, separator := range []rune{',', '\t'} {
		r := csv.NewReader(strings.NewReader(input))
		r.Comma = separator
		header, err := r.Read()
		if err != nil || len(header) < 2 || len(header) > 32 {
			continue
		}
		valid := true
		for _, field := range header {
			if len(field) > 64 || !entryDataHeader.MatchString(strings.TrimSpace(field)) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		row, err := r.Read()
		if err == nil && len(row) == len(header) {
			return true
		}
	}
	return false
}

func entryQuotedQuestion(input string) bool {
	q := strings.TrimSpace(input)
	for _, quotes := range entryQuotePairs {
		if len(q) >= len(quotes[0])+len(quotes[1]) && strings.HasPrefix(q, quotes[0]) && strings.HasSuffix(q, quotes[1]) {
			return intent.Question(q[len(quotes[0]) : len(q)-len(quotes[1])])
		}
	}
	return false
}

func entryEmptyPrefixClarification(input string) string {
	head, body, found := strings.Cut(pasteRequestBody(input), ":")
	if !found || strings.TrimSpace(body) != "" {
		return ""
	}
	switch strings.TrimSpace(head) {
	case "ask":
		return "What would you like to ask?"
	case "note":
		return "What would you like to save as a note?"
	case "task", "todo":
		return "What task would you like to add?"
	case "reminder":
		return "What should I remind you to do, and when?"
	case "remember", "preference", "rule", "writing rule":
		return "What would you like me to remember?"
	}
	return ""
}

func entryReadOnlyQuestion(input string) bool {
	q := strings.ToLower(strings.Join(strings.Fields(input), " "))
	// A polite assistant-directed request may be a write even with a question
	// mark. Let the selected interpreter distinguish its requested operation.
	for _, prefix := range []string{"please ", "can you ", "could you ", "would you ", "will you ", "do you mind ", "would you mind ", "could you please "} {
		if strings.HasPrefix(q, prefix) {
			return false
		}
	}
	return true
}
