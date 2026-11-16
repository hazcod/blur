package intent

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var notePolitePrefix = regexp.MustCompile(`(?i)^(?:please|can you|could you|would you|will you|do you mind|i['’]d like you to|i would like you to|i want you to)\s+`)
var noteRequestPrefix = regexp.MustCompile(`(?i)^(?:(?:save|saving) this as a note|(?:save|saving|take|taking|make|making) (?:these|the following) (?:meeting )?notes|(?:save|saving|take|taking|make|making) (?:a|this) note|(?:jot|jotting|write|writing) (?:this )?down|(?:record|recording) this|note to self|note)(?:\s*:\s*|\s+)`)
var noteBodyConnector = regexp.MustCompile(`(?i)^(?:that|about|saying|of)\s+`)
var noteNegativePrefix = regexp.MustCompile(`(?i)^(?:do not|don['’]t|never|not)\s+`)
var noteRetentionOperation = regexp.MustCompile(`(?i)^(?:save|store|remember|note|record|capture|keep|retain)(?:\s|\pP|$)`)

// NoteRequest extracts an explicit note-taking request without interpreting its
// content as instructions. It returns a span of the original input, preserving
// case, spacing, punctuation and multi-line content. Storage owns the size
// limit; the model's smaller intent-input limit does not apply to pasted notes.
func NoteRequest(input string) (string, bool) {
	if !utf8.ValidString(input) || strings.ContainsRune(input, 0) {
		return "", false
	}
	body := noteRequestBody(input)
	prefix := noteRequestPrefix.FindStringIndex(body)
	if prefix == nil {
		return "", false
	}
	literal := strings.Contains(body[:prefix[1]], ":")
	body = body[prefix[1]:]
	if !literal {
		if connector := noteBodyConnector.FindStringIndex(body); connector != nil {
			body = body[connector[1]:]
		}
	}
	return body, strings.TrimSpace(body) != ""
}

// NoteSavingDenied distinguishes a negated save operation from negation inside
// an explicitly requested note body. It grants no authority to store the input.
func NoteSavingDenied(input string) bool {
	body := noteRequestBody(input)
	prefix := noteNegativePrefix.FindStringIndex(body)
	if prefix == nil {
		return false
	}
	operation := body[prefix[1]:]
	return noteRequestPrefix.MatchString(operation+" ") || noteRetentionOperation.MatchString(operation)
}

func noteRequestBody(input string) string {
	body := strings.TrimLeftFunc(input, unicode.IsSpace)
	for {
		prefix := notePolitePrefix.FindStringIndex(body)
		if prefix == nil {
			break
		}
		body = strings.TrimLeftFunc(body[prefix[1]:], unicode.IsSpace)
		if strings.HasPrefix(strings.ToLower(body), "mind ") {
			body = strings.TrimLeftFunc(body[len("mind "):], unicode.IsSpace)
		}
	}
	return body
}
