package client

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/intent"
)

// pastedNoteAction routes explicit notes and confident meeting pastes locally.
// Its read-only branch keeps analysis requests out of the note fallback.
func pastedNoteAction(input string) (intent.Action, bool) {
	if !utf8.ValidString(input) || strings.ContainsRune(input, 0) {
		return intent.Action{}, false
	}
	if body, ok := intent.NoteRequest(input); ok {
		return intent.Action{Kind: intent.Note, Text: body}, true
	}
	if !strings.ContainsAny(input, "\n\r") {
		return intent.Action{}, false
	}
	first, _, _ := strings.Cut(strings.TrimLeftFunc(input, unicode.IsSpace), "\n")
	request := pasteRequestBody(first)
	if pasteAnalysisRequest(request) {
		if len(input) > MaxQueryBytes {
			return intent.Action{Kind: intent.Clarify, Text: "This paste is too large to analyze in one request. Save it as a note, then ask a focused question."}, true
		}
		return intent.Action{Kind: intent.Ask, Text: input, Mode: "general"}, true
	}
	if !meetingPasteStructure(input) {
		return intent.Action{}, false
	}
	if strings.HasPrefix(request, "don't ") || strings.HasPrefix(request, "do not ") || strings.HasPrefix(request, "never ") {
		return intent.Action{Kind: intent.Clarify, Text: "Do you want to review these notes without saving them?"}, true
	}
	// A current action request followed by evidence still needs the ordinary
	// operation/target authority path. Speaker-owned actions inside the paste
	// do not match these leading user-request prefixes.
	for _, prefix := range []string{"remind ", "reminder:", "task:", "todo:", "create ", "add ", "schedule ", "cancel ", "complete ", "finish ", "move ", "reschedule ", "rename ", "delete ", "send "} {
		if strings.HasPrefix(request, prefix) {
			return intent.Action{}, false
		}
	}
	return intent.Action{Kind: intent.Note, Text: input}, true
}

func pasteRequestBody(input string) string {
	q := strings.ToLower(strings.Join(strings.Fields(input), " "))
	for {
		changed := false
		for _, prefix := range []string{"please ", "can you ", "could you ", "would you ", "will you ", "do you mind ", "mind ", "i'd like you to ", "i’d like you to ", "i would like you to "} {
			if strings.HasPrefix(q, prefix) {
				q, changed = strings.TrimSpace(q[len(prefix):]), true
				break
			}
		}
		if !changed {
			return q
		}
	}
}

func pasteAnalysisRequest(request string) bool {
	word := request
	if at := strings.IndexFunc(word, func(r rune) bool { return !unicode.IsLetter(r) }); at >= 0 {
		word = word[:at]
	}
	switch word {
	case "summarize", "summarise", "extract", "translate", "rewrite", "rephrase", "explain", "analyse", "analyze", "list", "compare", "identify", "find", "show", "review", "read", "what", "why", "how", "which", "when", "who", "where", "is", "are", "did", "does":
		return true
	}
	for _, prefix := range []string{"write a summary", "draft a summary", "create a summary", "make a summary", "generate a summary", "give me a summary", "help me understand", "turn these notes into", "turn this transcript into"} {
		if strings.HasPrefix(request, prefix) {
			return true
		}
	}
	return false
}

// Require independent document signals rather than assuming every multiline
// message is a note. Only recognition is bounded to the first 256 lines; the
// returned note always retains the entire original string.
func meetingPasteStructure(input string) bool {
	var nonempty, speakers int
	var meeting, attendance, decision, actions bool
	lines := strings.SplitN(input, "\n", 257)
	if len(lines) > 256 {
		lines = lines[:256]
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		nonempty++
		q := strings.ToLower(strings.TrimLeft(line, "#*- \t0123456789.)"))
		for _, cue := range []string{"meeting", "minutes", "standup", "stand-up", "retrospective", "workshop", "transcript", "interview"} {
			if strings.Contains(q, cue) {
				meeting = true
				break
			}
		}
		for _, cue := range []string{"attendees:", "attendees", "participants:", "participants", "present:", "people:"} {
			if strings.HasPrefix(q, cue) {
				attendance = true
			}
		}
		for _, cue := range []string{"decision:", "decisions", "agreed:", "final decision:", "decision recorded:"} {
			if strings.HasPrefix(q, cue) {
				decision = true
			}
		}
		for _, cue := range []string{"action item", "action register", "actions:", "next steps", "todo:", "[ ]", "[x]"} {
			if strings.HasPrefix(q, cue) {
				actions = true
			}
		}
		if colon := strings.IndexByte(q, ':'); colon > 0 && colon < 80 && !strings.Contains(q[:colon], "http") {
			// Timestamped or named speaker lines contribute only alongside a
			// meeting heading, never enough to turn arbitrary text into a note.
			speakers++
		}
	}
	return nonempty >= 3 && (attendance && (meeting || decision || actions) || decision && actions || meeting && (decision || actions || speakers >= 2))
}
