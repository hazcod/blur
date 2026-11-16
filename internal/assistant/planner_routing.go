package assistant

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Routing rules that need no model: recency windows, day parts, conversation
// recaps, speaker questions and "where did I store" memory cues. Each helper is
// a pure reading of the question words so the planner stays deterministic.

const (
	// ScopeJustNow labels the window planned for "what was I just doing".
	ScopeJustNow = "just now"
	// ScopeJustNowWindow is how far back a recency question looks.
	ScopeJustNowWindow = time.Hour
	// ScopeJustNowWiden is the single wider window retrieval falls back to when
	// the recency window holds no capture.
	ScopeJustNowWiden = 6 * time.Hour
	// ConversationDefaultDays bounds a conversation recap that names no window.
	ConversationDefaultDays = 30
)

func withWords(set map[string]struct{}, words ...string) map[string]struct{} {
	if set == nil {
		set = make(map[string]struct{}, len(words))
	}
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}

// recencyGrammarWords are verbs of a recency question that must not become
// required search terms ("working" in "what was I just working on").
var recencyGrammarWords = []string{"working", "reading", "looking", "watching", "writing", "read", "work", "do", "did", "moment", "ago"}

// detectRecency recognises "just now", "a moment ago" and the present or
// just-completed activity questions "what was/did/am I just/working on".
func detectRecency(ws []word) bool {
	if hasSeq(ws, "just", "now") || hasSeq(ws, "moment", "ago") {
		return true
	}
	if len(ws) == 0 || ws[0].s != "what" && ws[0].s != "which" {
		return false
	}
	if hasSeq(ws, "i", "just") || hasSeq(ws, "we", "just") {
		return true
	}
	return startsWith(ws, "what", "am", "i", "working", "on") || startsWith(ws, "what", "am", "i", "reading") || startsWith(ws, "what", "am", "i", "doing")
}

var dayPartHours = map[string][2]int{
	"morning": {6, 12}, "afternoon": {12, 18}, "evening": {18, 24}, "night": {20, 30},
}

var monthWords = map[string]time.Month{
	"january": time.January, "february": time.February, "march": time.March, "april": time.April, "may": time.May, "june": time.June,
	"july": time.July, "august": time.August, "september": time.September, "october": time.October, "november": time.November, "december": time.December,
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April, "jun": time.June, "jul": time.July,
	"aug": time.August, "sep": time.September, "sept": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

var dayPartFillers = map[string]struct{}{"in": {}, "the": {}, "during": {}, "on": {}, "at": {}, "of": {}}

// dayPartScope combines a part of the day with the day it belongs to:
// "yesterday afternoon", "this morning", "Friday evening", "8 November
// morning", "last night". A day-part word with no day beside it ("afternoon
// tea recipe") is ordinary text and never scopes.
func dayPartScope(ws []word, now time.Time) (Scope, []string, bool) {
	for i, w := range ws {
		hours, ok := dayPartHours[w.s]
		if !ok {
			continue
		}
		day, label, used, found := dayPartAnchor(ws, i, now)
		if !found {
			continue
		}
		start := time.Date(day.Year(), day.Month(), day.Day(), hours[0], 0, 0, 0, day.Location())
		end := time.Date(day.Year(), day.Month(), day.Day(), hours[1], 0, 0, 0, day.Location())
		return Scope{Start: start, End: end, Label: label, Explicit: true}, append(used, w.s), true
	}
	return Scope{}, nil, false
}

func dayPartAnchor(ws []word, i int, now time.Time) (day time.Time, label string, used []string, ok bool) {
	part := ws[i].s
	titled := func(name string) string { return strings.ToUpper(name[:1]) + name[1:] }
	named := func(offset int, prefix string) (time.Time, string, []string, bool) {
		day := dayStart(now, offset)
		switch {
		case part == "night" && offset == 0:
			return day, "tonight", nil, true
		case part == "night" && offset == -1 && prefix == "last":
			return day, "last night", nil, true
		}
		return day, prefix + " " + part, nil, true
	}
	weekday := func(index int, wd time.Weekday, lastBefore bool) (time.Time, string, []string, bool) {
		back := (int(now.Weekday()) - int(wd) + 7) % 7
		if back == 0 && lastBefore {
			back = 7
		}
		return dayStart(now, -back), "on " + titled(ws[index].s) + " " + part, nil, true
	}
	date := func(dayWord, monthWord int) (time.Time, string, []string, bool) {
		month, isMonth := monthWords[ws[monthWord].s]
		number, err := strconv.Atoi(ws[dayWord].s)
		if !isMonth || err != nil || number < 1 || number > 31 {
			return time.Time{}, "", nil, false
		}
		date := time.Date(now.Year(), month, number, 0, 0, 0, 0, now.Location())
		if date.After(now) {
			date = date.AddDate(-1, 0, 0)
		}
		if date.Day() != number {
			return time.Time{}, "", nil, false
		}
		return date, strconv.Itoa(number) + " " + titled(month.String()) + " " + part, []string{ws[dayWord].s, ws[monthWord].s}, true
	}
	j := i - 1
	for j >= 0 {
		if _, filler := dayPartFillers[ws[j].s]; !filler {
			break
		}
		j--
	}
	if j >= 0 {
		adjacent := j == i-1
		switch cue := ws[j].s; {
		case cue == "yesterday":
			return named(-1, "yesterday")
		case cue == "today", cue == "this" && adjacent:
			return named(0, "this")
		case cue == "last" && adjacent && part == "night":
			return named(-1, "last")
		default:
			if wd, isDay := weekdayWords[cue]; isDay {
				return weekday(j, wd, j > 0 && ws[j-1].s == "last")
			}
			if j > 0 {
				if _, isMonth := monthWords[cue]; isMonth {
					if day, label, used, ok := date(j-1, j); ok {
						return day, label, used, true
					}
				}
				if _, isMonth := monthWords[ws[j-1].s]; isMonth {
					if day, label, used, ok := date(j, j-1); ok {
						return day, label, used, true
					}
				}
			}
		}
	}
	// "the afternoon of yesterday"
	k := i + 1
	if k < len(ws) && ws[k].s == "of" {
		k++
	}
	if k < len(ws) && k > i+1 {
		switch cue := ws[k].s; {
		case cue == "yesterday":
			return named(-1, "yesterday")
		case cue == "today":
			return named(0, "this")
		default:
			if wd, isDay := weekdayWords[cue]; isDay {
				return weekday(k, wd, false)
			}
		}
	}
	return time.Time{}, "", nil, false
}

// conversationRequest is a recap of the saved conversation with one person.
type conversationRequest struct {
	ok   bool
	name string // as written in the question
}

var conversationNouns = map[string]struct{}{
	"convo": {}, "convos": {}, "conversation": {}, "conversations": {}, "chat": {}, "chats": {}, "thread": {}, "threads": {},
	"dm": {}, "dms": {}, "message": {}, "messages": {}, "email": {}, "emails": {}, "texts": {},
}

// notAName words are pronouns, determiners and group nouns that follow "with"
// but never name a person.
var notAName = map[string]struct{}{
	"me": {}, "him": {}, "her": {}, "them": {}, "us": {}, "it": {}, "you": {}, "he": {}, "she": {}, "they": {}, "i": {}, "we": {},
	"his": {}, "their": {}, "its": {}, "everyone": {}, "anyone": {}, "someone": {}, "somebody": {}, "everybody": {}, "people": {},
	"team": {}, "teams": {}, "client": {}, "clients": {}, "group": {}, "channel": {}, "work": {}, "today": {}, "yesterday": {},
	"tonight": {}, "monday": {}, "tuesday": {}, "wednesday": {}, "thursday": {}, "friday": {}, "saturday": {}, "sunday": {},
	"all": {}, "each": {}, "other": {}, "another": {}, "anything": {}, "something": {}, "nothing": {}, "yet": {},
}

func isCapitalized(question string, w word) bool {
	for _, r := range question[w.start:] {
		return unicode.IsUpper(r)
	}
	return false
}

func possibleName(w word, consumed map[string]struct{}) bool {
	if _, no := notAName[w.s]; no {
		return false
	}
	if _, stop := questionStopWords[w.s]; stop {
		return false
	}
	if _, stop := planStopWords[w.s]; stop {
		return false
	}
	if _, used := consumed[w.s]; used {
		return false
	}
	if !strings.ContainsFunc(w.s, unicode.IsLetter) {
		return false
	}
	return true
}

// personNameAt reads a name beginning at ws[start]. A capitalised first word
// may continue over further capitalised words ("Samira Peeters"); a lower-case
// name is a single word. It returns the text as written, or "".
func personNameAt(question string, ws []word, start int, consumed map[string]struct{}) string {
	if start >= len(ws) || !possibleName(ws[start], consumed) {
		return ""
	}
	end := start + 1
	if isCapitalized(question, ws[start]) {
		for end < len(ws) && end < start+3 && isCapitalized(question, ws[end]) && possibleName(ws[end], consumed) {
			end++
		}
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(question[ws[start].start:ws[end-1].end], "'s"), "’s"))
}

// detectConversationRecap recognises "recap my convo/chat/thread/DMs with
// <Name>", "catch me up on <Name>" and "what did <Name> and I discuss". A recap
// of a period ("recap my day") is not one.
func detectConversationRecap(question string, ws []word, consumed map[string]struct{}) conversationRequest {
	summary := hasWord(ws, "recap", "summarize", "summarise", "summary", "review", "overview", "rundown")
	if summary {
		for i := 0; i+2 < len(ws); i++ {
			if _, noun := conversationNouns[ws[i].s]; noun && ws[i+1].s == "with" {
				if name := personNameAt(question, ws, i+2, consumed); name != "" {
					return conversationRequest{ok: true, name: name}
				}
			}
		}
	}
	for _, prefix := range [][]string{{"catch", "me", "up", "on"}, {"catch", "me", "up", "with"}, {"bring", "me", "up", "to", "speed", "on"}} {
		if startsWith(ws, prefix...) {
			if name := personNameAt(question, ws, len(prefix), consumed); name != "" {
				return conversationRequest{ok: true, name: name}
			}
		}
	}
	if startsWith(ws, "what", "did") {
		for and := 3; and+2 < len(ws) && and <= 5; and++ {
			if ws[and].s != "and" || ws[and+1].s != "i" {
				continue
			}
			switch ws[and+2].s {
			case "discuss", "discussed", "talk", "talked", "chat", "chatted", "say", "message", "messaged", "speak", "spoke":
			default:
				continue
			}
			if name := personNameAt(question, ws, 2, consumed); name != "" && strings.EqualFold(name, question[ws[2].start:ws[and-1].end]) {
				return conversationRequest{ok: true, name: name}
			}
		}
	}
	return conversationRequest{}
}

// speechVerbs report what a person said or sent. neutralSpeechVerbs say nothing
// about the topic, so they are not search terms; "promise" and the like are.
var speechVerbs = map[string]struct{}{
	"say": {}, "write": {}, "send": {}, "mention": {}, "promise": {}, "ask": {}, "tell": {}, "reply": {}, "share": {},
	"propose": {}, "suggest": {}, "request": {}, "respond": {}, "post": {}, "message": {},
}

var neutralSpeechVerbs = []string{"write", "send", "mention", "ask", "tell", "reply", "share", "respond", "post", "message"}

var pastSpeechVerbs = map[string]struct{}{
	"said": {}, "says": {}, "wrote": {}, "mentioned": {}, "promised": {}, "asked": {}, "told": {}, "sent": {}, "replied": {}, "suggested": {}, "proposed": {},
}

// detectSpeaker finds the person in "what did <Name> say/write/send...",
// "when did <Name> ..." and "<Name> said ...". Only a capitalised name counts:
// a lower-case word could as easily be an ordinary noun. It returns the name
// and the neutral speech verb to drop from the search terms.
func detectSpeaker(question string, ws []word, consumed map[string]struct{}) (name string, verb string) {
	if startsWith(ws, "what", "did") || startsWith(ws, "when", "did") {
		for v := 3; v < len(ws) && v <= 5; v++ {
			if _, speech := speechVerbs[ws[v].s]; !speech {
				continue
			}
			if name := capitalizedName(question, ws, 2, v, consumed); name != "" {
				return name, ws[v].s
			}
			return "", ""
		}
		return "", ""
	}
	for v := 1; v < len(ws) && v <= 3; v++ {
		if _, speech := pastSpeechVerbs[ws[v].s]; !speech {
			continue
		}
		if name := capitalizedName(question, ws, 0, v, consumed); name != "" {
			return name, ""
		}
		return "", ""
	}
	return "", ""
}

// capitalizedName returns ws[from:to] as written when every word is a
// capitalised possible name.
func capitalizedName(question string, ws []word, from, to int, consumed map[string]struct{}) string {
	if from >= to {
		return ""
	}
	for _, w := range ws[from:to] {
		if !isCapitalized(question, w) || !possibleName(w, consumed) {
			return ""
		}
	}
	return strings.TrimSpace(question[ws[from].start:ws[to-1].end])
}

var storageVerbs = []string{"store", "stored", "save", "saved", "keep", "kept", "put", "leave", "left", "stash", "stashed"}

// storageGrammarWords never narrow the search for the stored thing itself.
var storageGrammarWords = []string{"store", "stored", "save", "saved", "keep", "kept", "put", "leave", "left", "stash", "stashed", "again"}

// seeksStoredLocation recognises "where is/was X stored/saved/kept/put" and
// "where is X again?": a question about where the user put something, which
// only their own memory can answer.
func seeksStoredLocation(ws []word) bool {
	if len(ws) < 3 || ws[0].s != "where" && ws[0].s != "where's" && ws[0].s != "where’s" {
		return false
	}
	return hasWord(ws, storageVerbs...) || ws[len(ws)-1].s == "again"
}
