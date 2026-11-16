package intent

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var conditionalOperation = regexp.MustCompile(`\b(?:if|unless|once|when|whenever|upon|until|before|after|later|eventually|sometime|someday|pending|maybe|perhaps|probably|possibly|provided|assuming)\b|\bnot (?:yet|now|today|immediately)\b|\bas (?:soon|long) as\b|\b(?:on condition|in case|subject to|depending on|contingent on)\b`)
var negatedMutation = regexp.MustCompile(`\b(?:do not|don['’]t|never|must not|should not|will not|won['’]t)\s+(?:actually\s+)?(?:cancel|stop|complete|finish|mark|move|reschedule|change|rename|postpone|update|make)\b`)
var negatedTaskCreation = regexp.MustCompile(`\b(?:do not|don['’]t|never|must not|should not)\s+(?:create|add|save|make|record)\b`)
var taskRequestHead = regexp.MustCompile(`(?i)^(?:(?:add|adding|create|creating|make|making)\s+(?:a|this)\s+(?:task|to-do|todo)(?:\s*:\s*|\s+|$)|(?:task|todo)\s*:\s*)`)
var taskRequestConnector = regexp.MustCompile(`(?i)^(?:to|called|named|titled)\s+`)
var taskListRequest = regexp.MustCompile(`(?is)^(?:add|put)\s+(.+?)\s+(?:to|on)\s+(?:(?:my|the)\s+)?(?:tasks|task list|todo list|to-do list)\s*[?.!]*$`)
var nonCurrentTaskStart = regexp.MustCompile(`^(?:yesterday|previously|earlier|according to|would|could|should|might|may)(?: |$)`)

// TaskMutationAllowed checks the current operation independently of app-owned
// title data. A request to change a reminder time is immediate; a conditional
// request to change the task is not. Deferred mutations need clarification.
func TaskMutationAllowed(request Request, kind Kind) bool {
	if kind != TaskEdit && kind != TaskComplete && kind != TaskCancel {
		return false
	}
	if !authority(request.Input, kind) || ResolveTarget(request.Input, request.Targets) == "" {
		return false
	}
	op := mutationOperationText(request.Input, request.Targets, kind)
	if conditionalOperation.MatchString(op) || negatedMutation.MatchString(op) {
		return false
	}
	return kind == TaskEdit || !TaskMutationTiming(op)
}

// TaskMutationNeedsClarification also recognizes a mutation behind an
// unsupported lead-in/modal, so the UI does not turn it into an executable
// model interpretation or an unrelated saved note.
func TaskMutationNeedsClarification(request Request) bool {
	if ResolveTarget(request.Input, request.Targets) == "" || QuestionPrefix(request.Input) {
		return false
	}
	// Inspect the operation's first mutation verb, not words inside a
	// quoted rename argument ("rename task 1 to Cancel the release").
	for _, word := range words(mutationOperationText(request.Input, request.Targets, TaskEdit)) {
		if kind := mutationWordKind(word); kind != "" {
			return !TaskMutationAllowed(request, kind)
		}
	}
	return false
}

func mutationWordKind(word string) Kind {
	switch word {
	case "cancel", "stop":
		return TaskCancel
	case "complete", "finish", "done", "mark":
		return TaskComplete
	case "move", "reschedule", "change", "rename", "postpone", "update", "make":
		return TaskEdit
	}
	return ""
}

func mutationOperationText(input string, targets []Target, kind Kind) string {
	q := normalized(input)
	resolved := ResolveTarget(input, targets)
	for _, target := range targets {
		if target.Ref != resolved {
			continue
		}
		title := normalized(target.Title)
		if title == "" {
			continue
		}
		for offset := 0; offset < len(q); {
			at := strings.Index(q[offset:], title)
			if at < 0 {
				break
			}
			at += offset
			left := strings.TrimSpace(q[:at])
			right := q[at+len(title):]
			quoted := false
			if r, width := utf8.DecodeLastRuneInString(left); width > 0 {
				if close, ok := closingQuote(r); ok && strings.HasPrefix(right, string(close)) {
					quoted = true
					left = strings.TrimSpace(left[:len(left)-width])
				}
			}
			named := false
			for _, cue := range []string{"called", "named", "titled", "task", "reminder"} {
				if left == cue || strings.HasSuffix(left, " "+cue) {
					named = true
					break
				}
			}
			if quoted && !named {
				prefix := words(requestBody(left))
				quoted = len(prefix) == 1 && mutationWordKind(prefix[0]) != "" || len(prefix) == 2 && mutationWordKind(prefix[0]) != "" && prefix[1] == "the"
			}
			if (named || quoted) && (quoted || boundTitleEnd(right)) {
				q = q[:at] + strings.Repeat(" ", len(title)) + q[at+len(title):]
			}
			offset = at + len(title)
		}
	}
	// Only an explicitly quoted rename argument is opaque data. An unquoted
	// trailing condition must remain visible even if a model includes it in
	// the proposed title span.
	if kind == TaskEdit && strings.HasPrefix(requestBody(q), "rename ") {
		if at := strings.Index(q, " to "); at >= 0 {
			start := at + len(" to ")
			if _, end, ok := quoteAtStart(q[start:]); ok {
				q = q[:start] + strings.Repeat(" ", end) + q[start+end:]
			}
		}
	}
	return normalized(q)
}

func boundTitleEnd(tail string) bool {
	tail = strings.TrimSpace(tail)
	if tail == "" || strings.ContainsRune(",.;:!?", rune(tail[0])) {
		return true
	}
	ws := words(tail)
	if len(ws) == 0 {
		return true
	}
	for _, boundary := range []string{"to", "done", "complete", "completed", "please", "now", "because", "since", "and", "but", "if", "unless", "once", "when", "whenever", "upon", "until", "before", "after", "later", "pending", "provided", "assuming"} {
		if ws[0] == boundary {
			return true
		}
	}
	return false
}

type taskCreationRequest struct {
	title    string
	explicit bool
	literal  bool
	personal bool
}

func taskCreationParts(input string) taskCreationRequest {
	body := politeInput(input)
	if head := taskRequestHead.FindStringIndex(body); head != nil {
		literal := strings.Contains(body[:head[1]], ":")
		title := strings.TrimSpace(body[head[1]:])
		if connector := taskRequestConnector.FindStringIndex(title); connector != nil {
			title = strings.TrimSpace(title[connector[1]:])
		}
		if inner, end, quoted := quoteAtStart(title); quoted && strings.Trim(strings.TrimSpace(title[end:]), ".!?") == "" {
			title, literal = inner, true
		}
		return taskCreationRequest{title: title, explicit: true, literal: literal}
	}
	if match := taskListRequest.FindStringSubmatch(body); match != nil {
		return taskCreationRequest{title: match[1], explicit: true}
	}
	q := normalized(body)
	q = strings.NewReplacer("i'm ", "i am ", "i’m ", "i am ", "i'll ", "i will ", "i’ll ", "i will ", "i've ", "i have ", "i’ve ", "i have ", "i'd ", "i would ", "i’d ", "i would ").Replace(q)
	if match := personalTaskPrefix.FindStringSubmatch(q); match != nil {
		if original, ok := canonicalSpan(input, match[1]); ok {
			return taskCreationRequest{title: original, personal: true}
		}
	}
	return taskCreationRequest{title: body}
}

func taskAuthority(input, text string) bool {
	if _, note := NoteRequest(input); note {
		return false
	}
	for _, prefix := range []string{"ask:", "remember:", "preference:", "rule:", "writing rule:"} {
		if strings.HasPrefix(requestBody(input), prefix) {
			return false
		}
	}
	request := taskCreationParts(input)
	if strings.TrimSpace(request.title) == "" || !taskTitleMatches(request, text) {
		return false
	}
	if request.literal {
		return true
	}
	op, ok := withoutQuotedData(request.title)
	if !ok || conditionalOperation.MatchString(op) {
		return false
	}
	if request.explicit {
		return true
	}
	if leadingNegative(op) || negatedTaskCreation.MatchString(op) || personalNonAction.MatchString(op) {
		return false
	}
	if request.personal {
		return true
	}
	q := normalized(op)
	if Question(q) || nonCurrentTaskStart.MatchString(q) || q == "" {
		return false
	}
	if _, _, quoted := quoteAtStart(request.title); quoted {
		return false
	}
	ws := words(q)
	if len(ws) == 0 {
		return false
	}
	first, _ := utf8.DecodeRuneInString(ws[0])
	if !unicode.IsLetter(first) {
		return false
	}
	head := strings.Join(ws[:min(3, len(ws))], " ")
	return !shorthandStatement.MatchString(head) && !personalSupportingClause.MatchString(head) && !finiteSubjectPredicate(q)
}

func taskTitleMatches(request taskCreationRequest, text string) bool {
	want, got := normalized(request.title), normalized(text)
	if !request.literal {
		want, got = strings.TrimRight(want, ".!?"), strings.TrimRight(got, ".!?")
	}
	return got != "" && got == want
}

func leadingNegative(input string) bool {
	q := normalized(input)
	for _, prefix := range []string{"do not ", "don't ", "don’t ", "not ", "never ", "must not ", "should not ", "will not ", "won't ", "won’t "} {
		if strings.HasPrefix(q, prefix) {
			return true
		}
	}
	return false
}

func politeInput(input string) string {
	body := strings.TrimSpace(input)
	for {
		prefix := notePolitePrefix.FindStringIndex(body)
		if prefix == nil {
			return body
		}
		body = strings.TrimSpace(body[prefix[1]:])
		if strings.HasPrefix(strings.ToLower(body), "mind ") {
			body = strings.TrimSpace(body[len("mind "):])
		}
	}
}

func closingQuote(open rune) (rune, bool) {
	switch open {
	case '"', '\'', '`':
		return open, true
	case '“':
		return '”', true
	case '‘':
		return '’', true
	}
	return 0, false
}

func quoteAtStart(value string) (inner string, end int, ok bool) {
	open, width := utf8.DecodeRuneInString(value)
	close, quoted := closingQuote(open)
	if !quoted {
		return "", 0, false
	}
	escaped := false
	for at, r := range value[width:] {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == close {
			return value[width : width+at], width + at + utf8.RuneLen(r), true
		}
	}
	return "", 0, false
}

func withoutQuotedData(value string) (string, bool) {
	var out strings.Builder
	for at := 0; at < len(value); {
		r, width := utf8.DecodeRuneInString(value[at:])
		_, quote := closingQuote(r)
		if quote && (at == 0 || r != '\'' && r != '‘' || !unicode.IsLetter(previousRune(value[:at]))) {
			_, end, ok := quoteAtStart(value[at:])
			if !ok {
				return "", false
			}
			out.WriteString(" title ")
			at += end
			continue
		}
		out.WriteRune(r)
		at += width
	}
	return normalized(out.String()), true
}

func previousRune(value string) rune {
	r, _ := utf8.DecodeLastRuneInString(value)
	return r
}
