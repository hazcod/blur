package assistant

import (
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// maxWhenCaptures bounds how many capture times one "when" answer lists.
const maxWhenCaptures = 6

// isWhenQuestion recognises a short question about timing, such as "when did he
// say that?" or "when was that?".
func isWhenQuestion(question string) bool {
	ws := splitWords(question)
	for len(ws) > 0 {
		switch ws[0].s {
		case "and", "also", "then", "please", "so":
			ws = ws[1:]
			continue
		}
		break
	}
	return len(ws) > 0 && ws[0].s == "when"
}

// NameMentions counts whole-word, case-insensitive occurrences of a person's
// name in text. A multi-word name counts the least frequent of its words, so
// both must appear.
func NameMentions(text, name string) int {
	want := make(map[string]int)
	for _, w := range splitWords(name) {
		want[w.s] = 0
	}
	if len(want) == 0 {
		return 0
	}
	start := -1
	count := func(end int) {
		if start >= 0 {
			if n, ok := want[strings.ToLower(text[start:end])]; ok {
				want[strings.ToLower(text[start:end])] = n + 1
			}
			start = -1
		}
	}
	for i, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		count(i)
	}
	count(len(text))
	least := -1
	for _, n := range want {
		if least < 0 || n < least {
			least = n
		}
	}
	return max(least, 0)
}

// DominantMentions keeps the evidence that is about a person rather than merely
// listing them. A chat sidebar mentions every contact once; the conversation
// itself repeats the name in its header and messages. When any item mentions
// the name repeatedly, single passing mentions and items far below the best
// count are dropped. Input order is preserved.
func DominantMentions(items []Evidence, name string) []Evidence {
	counts := make([]int, len(items))
	top := 0
	for i, item := range items {
		counts[i] = NameMentions(item.Text, name)
		top = max(top, counts[i])
	}
	var kept []Evidence
	for i, item := range items {
		if counts[i] > 0 && counts[i]*4 >= top && (counts[i] >= 2 || top == 1) {
			kept = append(kept, item)
		}
	}
	return kept
}

// capturedWhenAnswer answers "when did he say that?" from capture metadata
// alone. It lists when the cited sources were saved, in the user's zone, and
// cites them. A saved screen can be newer than the message it shows, which the
// answer says. ok is false when no source has a usable capture time.
func capturedWhenAnswer(plan Plan, evidence []Evidence, cited []string, diagnosis Diagnosis, zone *time.Location) (Result, bool) {
	var timed []Evidence
	for _, item := range evidence {
		if item.Layer == LayerSource && !item.TimeUnknown && !item.CapturedAt.IsZero() && validSourceID(item.SourceID) {
			timed = append(timed, item)
		}
	}
	if plan.Entity != "" {
		if about := DominantMentions(timed, plan.Entity); len(about) > 0 {
			timed = about
		}
	}
	// The sources the earlier answer cited are what "that" refers to.
	var anchored []Evidence
	for _, item := range timed {
		if slices.Contains(cited, item.SourceID) {
			anchored = append(anchored, item)
		}
	}
	if len(anchored) > 0 {
		timed = anchored
	}
	if len(timed) == 0 {
		return Result{}, false
	}
	sort.SliceStable(timed, func(i, j int) bool { return timed[i].CapturedAt.Before(timed[j].CapturedAt) })
	if len(timed) > maxWhenCaptures {
		timed = timed[len(timed)-maxWhenCaptures:]
	}
	if zone == nil {
		zone = time.UTC
	}
	line := func(item Evidence) string {
		text := item.CapturedAt.In(zone).Format("Monday 2 January at 15:04")
		if app := strings.TrimSpace(item.AppName); app != "" {
			text += " in " + app
		}
		return text
	}
	var text strings.Builder
	ids := make([]string, 0, len(timed))
	for _, item := range timed {
		ids = append(ids, item.SourceID)
	}
	if len(timed) == 1 {
		text.WriteString("Captured " + line(timed[0]) + ".")
	} else {
		text.WriteString("Captured:")
		for _, item := range timed {
			text.WriteString("\n- " + line(item))
		}
	}
	text.WriteString("\n\nThese are the times the screen was saved; the message itself may be a little older.")
	answer, err := ValidateAnswer(ModelAnswer{Text: text.String(), SourceIDs: ids}, ids)
	if err != nil {
		return Result{}, false
	}
	return Result{Answer: answer, Sources: timed, Direct: true, Plan: plan, ConversationTurns: 1, IndexingPending: diagnosis.IndexingPending}, true
}
