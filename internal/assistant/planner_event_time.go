package assistant

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/search"
)

// timeQuestionScope separates the time of something described in evidence
// from the time the user observed that evidence. It uses question grammar, not
// a list of calendar event names. Relative activity clauses can still supply
// an observation window: an appointment on Monday in an email read Thursday.
func timeQuestionScope(ws []word) ([]word, bool) {
	aux := 0
	switch {
	case startsWith(ws, "when"):
		aux = 1
	case startsWith(ws, "what", "time"):
		aux = 2
	case startsWith(ws, "when's"), startsWith(ws, "when’s"):
		aux = 0 // the copula is part of the first word
	default:
		return ws, false
	}
	if len(ws) <= aux+1 {
		return ws, false
	}
	if aux != 0 && !hasWord(ws[aux:aux+1], "am", "is", "are", "was", "were", "will", "do", "does", "did", "have", "has", "had") {
		return ws, false
	}
	subject := aux + 1
	pastActivity := hasWord(ws[aux:aux+1], "did", "was", "were", "have", "has", "had")
	// Questions about the user's own past activity keep their ordinary
	// window, including locative forms such as "when was I in Slack".
	if pastActivity && subject+1 < len(ws) && hasWord(ws[subject:subject+1], "i", "we", "you") && hasWord(ws[subject+1:subject+2], "in", "on", "at", "using") {
		return ws, false
	}
	for i := subject; i < len(ws); i++ {
		switch ws[i].s {
		case "read", "reading", "saw", "seen", "see", "captured", "capturing", "opened", "opening", "browsed", "browsing", "viewed", "viewing", "worked", "working", "used", "using", "saved", "discussed", "said", "mentioned":
			// A verb alone is not a capture clause: "the book reading"
			// names an event. Require an explicit activity subject.
			for j := i - 1; j >= subject && j >= i-3; j-- {
				if hasWord(ws[j:j+1], "i", "we", "you", "he", "she", "they") {
					if j == subject && !pastActivity {
						break // scheduled activity cannot date an existing capture
					}
					return ws[i:], j != subject
				}
				if !hasWord(ws[j:j+1], "was", "were", "am", "is", "are", "have", "has", "had", "been", "just", "last") {
					break
				}
			}
		}
	}
	return nil, true
}

// Date replacement may remove a date's numbers, but never an arbitrary numeric
// subject such as Route 66. Recognise date spans, retaining unrelated names
// that also spell a month. This classifies content cues; it does not invent
// an event instant or resolve ambiguous numeric date conventions.
func eventDateWords(question string) ([]word, []bool) {
	ws := splitWords(question)
	original := ws
	if dutchRecallGrammar(question, ws).active {
		ws = dutchCalendarView(ws)
	}
	marked := make([]bool, len(ws))
	mark := func(i int) {
		marked[i] = true
		if i > 0 && hasWord(ws[i-1:i], "this", "next", "last") {
			marked[i-1] = true
		}
	}
	number := func(i, low, high int) bool {
		if i < 0 || i >= len(ws) {
			return false
		}
		value := ws[i].s
		for _, suffix := range []string{"st", "nd", "rd", "th"} {
			value = strings.TrimSuffix(value, suffix)
		}
		n, err := strconv.Atoi(value)
		return err == nil && n >= low && n <= high
	}
	for i, w := range ws {
		introduced := i > 0 && hasWord(ws[i-1:i], "on", "in", "at", "for", "this", "next", "last")
		shortFollowup := len(ws) <= 3 && IsFollowUp(question)
		if _, day := weekdayWords[w.s]; day && (introduced || shortFollowup) {
			mark(i)
		}
		switch w.s {
		case "today", "tomorrow", "yesterday", "tonight":
			if introduced || i == len(ws)-1 || shortFollowup || i+1 < len(ws) && hasWord(ws[i+1:i+2], "morning", "afternoon", "evening") {
				mark(i)
			}
		case "morning", "afternoon", "evening", "day", "days", "week", "weeks", "month", "months", "year", "years":
			if introduced || shortFollowup || i > 0 && marked[i-1] {
				mark(i)
			}
			if number(i-1, 1, 9999) && i > 1 && hasWord(ws[i-2:i-1], "in", "after", "for", "next") {
				mark(i - 1)
				mark(i)
			}
		}
		if _, month := monthWords[w.s]; month {
			before, after := number(i-1, 1, 31), number(i+1, 1, 31)
			if introduced || before || after || shortFollowup {
				mark(i)
				if introduced && number(i+1, 1000, 9999) {
					mark(i + 1)
				}
				if before {
					mark(i - 1)
					if number(i+1, 1000, 9999) {
						mark(i + 1)
					}
				}
				if after {
					mark(i + 1)
					if number(i+2, 1000, 9999) {
						mark(i + 2)
					}
				}
			}
		}
		if i > 0 && ws[i-1].s == "year" && number(i, 1000, 9999) {
			mark(i)
		}
		if introduced && i+2 < len(ws) {
			span := question[w.start:ws[i+2].end]
			for _, layout := range []string{"2006-01-02", "2/1/2006", "1/2/2006", "2.1.2006"} {
				if _, err := time.Parse(layout, span); err == nil {
					mark(i)
					mark(i + 1)
					mark(i + 2)
					break
				}
			}
		}
	}
	return original, marked
}

func eventDateTerms(question string) map[string]struct{} {
	ws, marked := eventDateWords(question)
	used := map[string]struct{}{}
	for i, w := range ws {
		if marked[i] {
			used[w.s] = struct{}{}
		}
	}
	// One date occurrence cannot erase the same word in the event's subject:
	// Route 23 on 23 November, or the May project meeting in May.
	for i, w := range ws {
		if !marked[i] {
			delete(used, w.s)
		}
	}
	return used
}

func eventSubjectTerms(p Plan) []string {
	if p.EventSubjectTerms != nil {
		return slices.Clone(p.EventSubjectTerms)
	}
	subject := make([]string, 0, len(p.Terms))
	dates := eventDateTerms(p.Question)
	for _, term := range p.Terms {
		if _, date := dates[strings.ToLower(term)]; !date {
			subject = append(subject, term)
		}
	}
	return subject
}

// A short date-only continuation refines the event named in the preceding
// question. Replace its previous date cues rather than filtering observations
// on the new date or keeping the previous weekday as a mandatory topic.
func eventDateContinuation(p, prev Plan) (Plan, bool) {
	if p.EventTime || !prev.EventTime || !IsFollowUp(p.Question) {
		return p, false
	}
	ws := splitWords(p.Question)
	appWords := map[string]struct{}{}
	for _, name := range append(slices.Clone(p.AppNames), p.Domains...) {
		for _, w := range splitWords(name) {
			appWords[w.s] = struct{}{}
		}
	}
	hasDate := false
	newDates := eventDateTerms(p.Question)
	for _, w := range ws {
		if _, actualDate := newDates[w.s]; actualDate {
			hasDate = true
			continue
		}
		if _, app := appWords[w.s]; app {
			continue
		}
		switch w.s {
		case "en", "ook", "op", "om", "voor", "van", "uit", "de", "het", "deze", "volgende", "vorige", "alsjeblieft":
			if dutchRecallGrammar(p.Question, ws).active {
				continue
			}
		case "and", "also", "then", "but", "what", "how", "about", "on", "at", "for", "in", "from", "the", "this", "next", "last", "same", "instead", "please":
			continue
		}
		return p, false // another subject or an explicit activity request
	}
	if !hasDate {
		return p, false
	}
	terms, err := planTerms(p.Question, appWords, false)
	if err != nil {
		return p, false
	}
	subject := eventSubjectTerms(prev)
	for _, term := range subject {
		if len(terms) >= search.MaxTerms {
			break
		}
		if !slices.Contains(terms, term) {
			terms = append(terms, term)
		}
	}
	slices.Sort(terms)
	p.Terms = slices.Compact(terms)
	p.EventSubjectTerms = slices.Clone(subject)
	p.NumberedSubjects = slices.Clone(prev.NumberedSubjects)
	p.Kind, p.Entity, p.EventTime, p.MatchOnly, p.Scope = prev.Kind, prev.Entity, true, true, Scope{}
	return p, true
}
