package assistant

import "strings"

// This is request grammar, not a translation of evidence or topic names.
// Its scope view reuses the existing calendar-window calculation while terms
// and byte offsets stay in the original question's language.
type dutchRecall struct {
	active, memory, event bool
	scope                 []word
}

// IsDutchReadRequest shares explicit question/search grammar with Interact.
// A read must reach the answer planner instead of falling through to note
// saving, including without a question mark. The planner separately decides
// whether a search refers to personal memory or an explicit web/procedural task.
func IsDutchReadRequest(question string) bool {
	q, err := normalizeQuestion(question)
	if err != nil {
		return false
	}
	ws := splitWords(q)
	if len(ws) == 0 || strings.HasPrefix(q, "\"") || strings.HasPrefix(q, "`") {
		return false
	}
	return dutchRecallGrammar(q, ws).active || startsWith(ws, "zoek") || startsWith(ws, "vind")
}

var dutchCalendarWords = map[string]string{
	"maandag": "monday", "dinsdag": "tuesday", "woensdag": "wednesday", "donderdag": "thursday", "vrijdag": "friday", "zaterdag": "saturday", "zondag": "sunday",
	"vandaag": "today", "gisteren": "yesterday", "morgen": "tomorrow", "vanavond": "tonight",
	"ochtend": "morning", "middag": "afternoon", "avond": "evening", "dagen": "days", "dag": "day", "weken": "weeks", "maand": "month", "maanden": "months", "jaar": "year", "jaren": "years", "uur": "hour", "uren": "hours", "minuten": "minutes", "minuut": "minute",
	"op": "on", "voor": "for", "om": "at", "van": "from", "uit": "from", "deze": "this", "volgende": "next", "vorige": "last", "afgelopen": "last",
	"januari": "january", "februari": "february", "maart": "march", "mei": "may", "juni": "june", "juli": "july", "augustus": "august", "oktober": "october",
}

var dutchRequestWords = []string{
	"hoe", "laat", "wanneer", "wat", "waar", "welke", "zoek", "vind", "toon", "mijn", "onze", "ons", "ik", "wij", "we", "jij", "je",
	"heb", "hebt", "heeft", "hebben", "had", "hadden", "ben", "bent", "zijn", "zal", "zullen", "ga", "gaan",
	"op", "van", "over", "uit", "de", "het", "een", "die", "dat", "en", "ook", "maar", "dan", "nog", "alsjeblieft",
	"gelezen", "las", "gezien", "zag", "bekeken", "opgeslagen", "besproken", "gezegd", "zei", "geopend", "bezocht", "gewerkt",
}

func dutchCalendarView(ws []word) []word {
	view := append([]word(nil), ws...)
	for i := range view {
		if replacement, ok := dutchCalendarWords[view[i].s]; ok {
			view[i].s = replacement
		}
	}
	return view
}

func dutchRecallGrammar(question string, ws []word) dutchRecall {
	if len(ws) == 0 || strings.HasPrefix(strings.TrimSpace(question), "\"") || strings.HasPrefix(strings.TrimSpace(question), "`") {
		return dutchRecall{}
	}
	command := startsWith(ws, "zoek") || startsWith(ws, "vind")
	if command && (hasSeq(ws, "op", "internet") || hasSeq(ws, "op", "het", "internet") || hasSeq(ws, "op", "het", "web") || hasWord(ws[1:], "online") || hasSeq(ws, "een", "manier", "om")) {
		return dutchRecall{}
	}
	continuation := len(ws) <= 8 && (startsWith(ws, "en") || startsWith(ws, "ook")) || dutchDateFollowup(ws)
	questionHead := hasWord(ws[:1], "wanneer", "hoe", "wat", "waar", "welke")
	personal := hasWord(ws, "mijn", "onze", "ons", "ik", "wij", "jij")
	if !command && !continuation && !(questionHead && personal) {
		return dutchRecall{}
	}
	out := dutchRecall{active: true, memory: command || personal, scope: dutchCalendarView(ws)}
	timeQuestion := startsWith(ws, "wanneer") || startsWith(ws, "hoe", "laat")
	if !timeQuestion {
		return out
	}
	activity := hasWord(ws, "gelezen", "las", "gezien", "zag", "bekeken", "opgeslagen", "besproken", "gezegd", "zei", "geopend", "bezocht", "gewerkt")
	// Dutch relative activity clauses place the verb after their date:
	// "de mail die ik donderdag heb gelezen". Keep that clause's window
	// independent of the event date in the main question.
	for i := 1; i+1 < len(ws); i++ {
		if activity && hasWord(ws[i:i+1], "die", "dat") && hasWord(ws[i+1:i+2], "ik", "wij", "we", "jij", "je") {
			out.event, out.scope = true, dutchCalendarView(ws[i+1:])
			return out
		}
	}
	if activity && hasWord(ws, "ik", "wij", "we", "jij", "je") || hasSeq(ws, "was", "ik", "in") || hasSeq(ws, "waren", "wij", "in") {
		return out
	}
	out.event, out.scope = true, nil
	return out
}

func dutchDateFollowup(ws []word) bool {
	if len(ws) == 0 || len(ws) > 3 {
		return false
	}
	hasDate := false
	for _, w := range ws {
		canonical, ok := dutchCalendarWords[w.s]
		if !ok {
			return false
		}
		if _, day := weekdayWords[canonical]; day || canonical == "today" || canonical == "tomorrow" || canonical == "yesterday" || canonical == "tonight" {
			hasDate = true
		} else if canonical != "on" && canonical != "for" && canonical != "this" && canonical != "next" && canonical != "last" {
			return false
		}
	}
	return hasDate
}

func dutchScopeTerms(ws []word, used []string, scope Scope) []string {
	var terms []string
	for _, w := range ws {
		mapped := w.s
		if replacement, ok := dutchCalendarWords[w.s]; ok {
			mapped = replacement
		}
		for _, u := range used {
			if u == mapped {
				terms = append(terms, w.s)
				break
			}
		}
		if !scope.IsZero() && hasWord([]word{{s: mapped}}, "today", "yesterday", "tonight", "morning", "afternoon", "evening", "last", "this", "week", "month", "hour", "hours", "minute", "minutes", "day", "days") {
			terms = append(terms, w.s)
		}
	}
	return terms
}
