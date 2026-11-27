package intent

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// TaskListRead describes a read of the user's saved task collection. Generic
// requests need only local records; topical ones retain the normal bounded
// title/meaning search. This recognizer never grants mutation authority.
type TaskListRead struct {
	Question                                                   string
	Generic                                                    bool
	State                                                      string // open, completed, cancelled, or all
	Window                                                     string // local calendar period, never a model-supplied date
	RemindersOnly, Count, Next, Overdue, Upcoming, Conflicting bool
}

var taskReadSpelling = strings.NewReplacer("’", "'", "‘", "'", "‑", "-", "‐", "-", "–", "-", "—", "-", "？", "?", "﹖", "?", "！", "!", "…", "...")
var taskReadCompound = regexp.MustCompile(`\bto-dos?\b`)

func TaskListRequest(input string) (TaskListRead, bool) {
	if len(input) == 0 || len(input) > MaxInputBytes || !utf8.ValidString(input) || strings.ContainsAny(input, "\x00\n\r`\"“”{}[]:;") {
		return TaskListRead{}, false
	}
	q := strings.ToLower(strings.Join(strings.Fields(taskReadSpelling.Replace(input)), " "))
	if strings.HasPrefix(q, "'") || strings.HasSuffix(strings.TrimRight(q, "?!. "), "'") {
		return TaskListRead{}, false
	}
	q = strings.TrimSpace(strings.TrimRight(q, "?!."))
	if read, ok := dutchTaskListRequest(q); ok {
		return read, true
	}
	for _, suffix := range []string{", please", " please"} {
		q = strings.TrimSpace(strings.TrimSuffix(q, suffix))
	}
	q = strings.TrimPrefix(strings.TrimPrefix(q, "please, "), "please ")
	for _, prefix := range []string{"can you ", "could you ", "would you ", "will you ", "can i ", "could i ", "may i ", "i'd like to ", "i would like to "} {
		if strings.HasPrefix(q, prefix) {
			q = strings.TrimPrefix(q[len(prefix):], "please ")
			break
		}
	}
	q = taskReadCompound.ReplaceAllString(q, "todo")
	if q == "to do" {
		q = "todo"
	}
	q = strings.ReplaceAll(q, "to do list", "todo list")
	q = strings.NewReplacer("task-list", "task list", "tasklist", "tasks").Replace(q)
	q = strings.NewReplacer("not done", "unfinished", "not completed", "unfinished", "not finished", "unfinished", "still to do", "pending").Replace(q)
	if taskListRecordedActionItemSource(q) {
		return TaskListRead{}, false
	}
	// Reported text, definitions, hypothetical advice and negative requests
	// must not be silently replaced by the user's actual saved collection.
	for _, phrase := range []string{"don't ", "do not ", "never ", "not show", "not list", "if ", "unless ", "could improve", "should i create", "examples of", "types of", "how to implement", "what is a task", "what are tasks", "what is a todo", "what are todos", "what is a reminder", "what are reminders", "i read", "i saw", "we discussed", "they said", "captured", "article", "about reminders", "about tasks", "source code", "means ", "mean?"} {
		if strings.Contains(q, phrase) {
			return TaskListRead{}, false
		}
	}
	fields := strings.Fields(q)
	if hasWord(q, "mean") || hasWord(q, "means") || hasWord(q, "not") || hasWord(q, "didn't") || hasWord(q, "haven't") {
		return TaskListRead{}, false
	}
	if len(fields) > 1 {
		switch fields[0] {
		case "task", "tasks", "todo", "todos", "reminder", "reminders", "the", "my", "our":
			for _, copula := range []string{"are", "is", "was", "were", "have", "has"} {
				if hasWord(q, copula) {
					return TaskListRead{}, false
				}
			}
		}
	}
	named := false
	for _, word := range fields {
		switch word {
		case "task", "tasks", "todo", "todos", "reminder", "reminders", "checklist", "checklists":
			named = true
		}
	}
	if strings.Contains(q, "action items") || strings.Contains(q, "action item") || strings.Contains(q, "my list") {
		named = true
	}
	implicit := false
	for _, head := range []string{
		"what's on my plate", "whats on my plate", "what is on my plate",
		"what's left on my plate", "whats left on my plate", "what is left on my plate",
		"what's still left on my plate", "whats still left on my plate", "what is still left on my plate",
		"what do i need todo", "what do i need to do", "what should i do", "what do i have to do",
		"what do i still need to do", "what do i still need to get done", "what do i need to get done",
		"what do i still have to do", "what have i still got to do", "what do i need to take care of",
		"what should i finish", "what should i take care of", "what is still pending for me",
		"what's still pending for me", "what is still left for me to do", "anything i still need to do",
		"what have i got todo", "what have i got to do", "what have i left todo", "what have i left to do", "what's left for me todo",
		"what's left for me to do", "what is left for me to do", "what is left for me todo", "what's left to do", "what is left to do", "what remains to be done",
		"what am i supposed to do", "anything i need to do", "anything i need todo", "anything left to do",
	} {
		if q == head || strings.HasPrefix(q, head+" ") && taskListStructural(strings.Fields(q[len(head):])) {
			implicit = true
			break
		}
	}
	if implicit {
		return taskListFilters(q, true), true
	}
	if !named {
		return TaskListRead{}, false
	}
	inspection := taskListStructural(fields)
	if inspection {
		switch fields[0] {
		case "task", "tasks", "todo", "todos", "reminder", "reminders", "checklist", "checklists", "action", "my", "our", "the", "all", "open", "pending", "remaining", "outstanding", "unfinished", "incomplete", "completed", "finished", "closed", "cancelled", "canceled", "active", "current", "upcoming", "scheduled", "due", "overdue", "today", "tomorrow", "yesterday", "this", "next", "last", "any", "personal":
		default:
			inspection = false
		}
	}
	for _, head := range []string{"what ", "what's ", "whats ", "which ", "show ", "list ", "display ", "view ", "check ", "see ", "read ", "pull up ", "bring up ", "let me see ", "how many ", "do i have ", "have i got ", "any ", "my ", "when is ", "when's "} {
		if strings.HasPrefix(q, head) {
			inspection = true
			break
		}
	}
	if !inspection {
		return TaskListRead{}, false
	}
	generic := taskListStructural(fields)
	if !generic && (fields[0] == "check" || fields[0] == "read" || fields[0] == "see") {
		// "check the task validator" may be a new technical action. Only an
		// explicit personal collection makes these weaker verbs a topical read.
		if !strings.Contains(q, "my tasks") && !strings.Contains(q, "my todos") && !strings.Contains(q, "my reminders") && !strings.Contains(q, "our tasks") {
			return TaskListRead{}, false
		}
	}
	return taskListFilters(q, generic), true
}

func taskListRecordedActionItemSource(q string) bool {
	if !strings.Contains(q, "action item") || !taskListHasAnyWord(q, "meeting", "meetings", "standup", "call", "calls", "sync", "session", "recording", "transcript", "notes", "interview", "webinar", "lecture", "conference", "podcast", "keynote") {
		return false
	}
	return taskListHasAnyWord(q, "from", "during", "discussed", "mentioned", "notes", "transcript", "recap", "in") || strings.Contains(q, "out of")
}

func taskListHasAnyWord(q string, candidates ...string) bool {
	for _, candidate := range candidates {
		if hasWord(q, candidate) {
			return true
		}
	}
	return false
}

func taskListFilters(q string, generic bool) TaskListRead {
	r := TaskListRead{Question: q, Generic: generic, State: "open", Count: strings.HasPrefix(q, "how many ")}
	states := map[string]bool{}
	words := strings.Fields(q)
	for i, word := range words {
		switch word {
		case "reminder", "reminders", "scheduled", "due", "overdue":
			r.RemindersOnly = true
		case "completed", "complete", "done", "finished", "finish", "closed":
			if taskCompletionIsAnAction(words, i) {
				continue
			}
			if (word == "done" || word == "completed" || word == "finished" || word == "finish") && i >= 2 && words[i-1] == "be" && (words[i-2] == "to" || words[i-2] == "need" || words[i-2] == "needs") {
				continue
			}
			states["completed"] = true
		case "cancelled", "canceled", "cancel":
			states["cancelled"] = true
		case "open", "pending", "remaining", "remains", "outstanding", "unfinished", "incomplete", "active":
			states["open"] = true
		}
	}
	if len(states) > 1 {
		r.Conflicting = true
	}
	if hasWord(q, "all") {
		r.State = "all"
	}
	for state := range states {
		r.State = state
	}
	r.Next = r.RemindersOnly && hasWord(q, "next") && !strings.Contains(q, "next week")
	r.Overdue, r.Upcoming = hasWord(q, "overdue"), hasWord(q, "upcoming")
	if r.Overdue {
		r.RemindersOnly = true
	}
	if r.Upcoming {
		r.RemindersOnly = true
	}
	for _, window := range []string{"today", "tomorrow", "yesterday", "this week", "next week", "last week"} {
		if strings.Contains(" "+q+" ", " "+window+" ") {
			if r.Window != "" {
				r.Conflicting = true
			}
			r.Window = window
		}
	}
	return r
}

// Natural phrases such as "what should I finish?" and "what do I need to get
// done?" ask for open work. Treat completion words as a status only when they
// describe the saved task, not when the user still needs to act on it.
func taskCompletionIsAnAction(words []string, index int) bool {
	if index > 0 {
		switch words[index-1] {
		case "to", "get", "need", "needs", "should", "must", "want", "wants", "have", "has", "got":
			return true
		case "be":
			return index > 1 && (words[index-2] == "to" || words[index-2] == "need" || words[index-2] == "needs" || words[index-2] == "should")
		}
	}
	if index > 1 && words[index-1] == "i" && (words[index-2] == "should" || words[index-2] == "need" || words[index-2] == "must" || words[index-2] == "want") {
		return true
	}
	return false
}

func taskListStructural(fields []string) bool {
	for _, word := range fields {
		switch word {
		case "task", "tasks", "todo", "todos", "reminder", "reminders", "checklist", "checklists", "list", "lists", "action", "items", "item",
			"what", "what's", "whats", "which", "when", "when's", "show", "display", "view", "check", "see", "read", "pull", "up", "bring", "let", "me", "how", "many", "do", "did", "does", "i", "have", "has", "got", "is", "are", "was", "were", "been", "on", "in", "to", "for", "from", "of", "the", "a", "an", "my", "our", "any", "please",
			"all", "open", "pending", "remaining", "outstanding", "unfinished", "incomplete", "done", "completed", "complete", "finished", "finish", "closed", "cancelled", "canceled", "cancel", "active", "current", "upcoming", "next", "scheduled", "due", "overdue", "today", "tomorrow", "yesterday", "this", "last", "week", "now", "left", "still", "personal", "yet", "need", "should", "be", "supposed", "remains", "get", "take", "care":
			continue
		default:
			return false
		}
	}
	return len(fields) > 0
}
