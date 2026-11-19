package assistant

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/search"
)

// The planner is the first step of Ask and never calls a model. The product
// rule is "the model last step, never first": a deterministic local reading of
// the question picks the kind, time window, and apps; structured answers (time
// spent, capture listings) are produced from local data without any model; and
// when a model is used it only ever sees a small, byte-bounded, labelled
// selection of evidence, never the whole memory.

type word struct {
	s          string // search.FoldLower form: lower-case, diacritics folded
	start, end int    // byte offsets into the original question
}

func splitWords(q string) []word {
	var out []word
	start := -1
	flush := func(end int) {
		if start >= 0 {
			w := search.FoldLower(strings.TrimRight(q[start:end], "'’"))
			if w != "" {
				out = append(out, word{s: w, start: start, end: end})
			}
			start = -1
		}
	}
	for i, r := range q {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || (start >= 0 && (r == '\'' || r == '’')) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(q))
	return out
}

func hasWord(ws []word, targets ...string) bool {
	for _, w := range ws {
		for _, t := range targets {
			if w.s == t {
				return true
			}
		}
	}
	return false
}

func hasSeq(ws []word, seq ...string) bool {
	return indexSeq(ws, 0, seq...) >= 0
}

func indexSeq(ws []word, from int, seq ...string) int {
	for i := from; i+len(seq) <= len(ws); i++ {
		ok := true
		for j, s := range seq {
			if ws[i+j].s != s {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func startsWith(ws []word, seq ...string) bool {
	return len(ws) >= len(seq) && indexSeq(ws[:len(seq)], 0, seq...) == 0
}

// planStopWords are grammar and planner vocabulary that never make useful
// exact-search terms, on top of questionStopWords.
var planStopWords = map[string]struct{}{
	"with": {}, "this": {}, "that": {}, "these": {}, "those": {}, "been": {}, "be": {}, "am": {}, "are": {}, "got": {}, "get": {},
	"any": {}, "anything": {}, "something": {}, "nothing": {}, "yet": {}, "up": {}, "out": {}, "into": {}, "there": {}, "talking": {},
	"than": {}, "then": {}, "some": {}, "just": {}, "also": {}, "app": {}, "site": {}, "website": {}, "so": {}, "far": {},
	"today": {}, "yesterday": {}, "tonight": {}, "earlier": {}, "recently": {}, "lately": {}, "ago": {}, "now": {}, "currently": {},
	"latest": {}, "newest": {}, "recent": {}, "current": {},
	"moment": {}, "right": {}, "last": {}, "past": {}, "previous": {}, "morning": {}, "afternoon": {}, "evening": {},
	"week": {}, "month": {}, "hour": {}, "hours": {}, "minute": {}, "minutes": {}, "day": {}, "days": {},
	"time": {}, "spent": {}, "spend": {}, "long": {}, "much": {}, "many": {},
	"open": {}, "opened": {}, "capture": {}, "captured": {}, "captures": {}, "capturing": {}, "recap": {}, "summary": {}, "summarize": {}, "summarise": {},
	"summarized": {}, "summarised": {}, "doing": {}, "done": {}, "saw": {}, "see": {}, "seen": {}, "record": {}, "recorded": {},
	"save": {}, "saved": {}, "happened": {}, "remember": {}, "know": {}, "recall": {},
}

var weekdayWords = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
}

// ambiguousApp names are ordinary words as well as app names. They only name
// an app after a locative word ("in notes") or before "app" ("notes app").
var ambiguousApp = map[string]struct{}{
	"notes": {}, "mail": {}, "messages": {}, "word": {}, "preview": {}, "calendar": {}, "signal": {}, "teams": {},
	"cursor": {}, "arc": {}, "terminal": {}, "claude": {}, "finder": {}, "reminders": {},
}

var locativeWords = map[string]struct{}{
	"in": {}, "on": {}, "from": {}, "into": {}, "using": {}, "via": {}, "with": {}, "open": {}, "opened": {}, "inside": {}, "of": {}, "at": {},
}

type appEntry struct{ id, name string }

var builtinApps = map[string]appEntry{
	"whatsapp": {"net.whatsapp.WhatsApp", "WhatsApp"}, "slack": {"com.tinyspeck.slackmacgap", "Slack"},
	"safari": {"com.apple.Safari", "Safari"}, "chrome": {"com.google.Chrome", "Chrome"}, "google chrome": {"com.google.Chrome", "Chrome"},
	"orion": {"com.kagi.kagimacos", "Orion"},
	"mail":  {"com.apple.mail", "Mail"}, "apple mail": {"com.apple.mail", "Mail"},
	"notes": {"com.apple.Notes", "Notes"}, "apple notes": {"com.apple.Notes", "Notes"},
	"messages": {"com.apple.MobileSMS", "Messages"}, "imessage": {"com.apple.MobileSMS", "Messages"},
	"telegram": {"ru.keepcoder.Telegram", "Telegram"}, "signal": {"org.whispersystems.signal-desktop", "Signal"},
	"discord": {"com.hnc.Discord", "Discord"}, "teams": {"com.microsoft.teams2", "Teams"}, "microsoft teams": {"com.microsoft.teams2", "Teams"},
	"zoom": {"us.zoom.xos", "Zoom"}, "vscode": {"com.microsoft.VSCode", "VS Code"}, "vs code": {"com.microsoft.VSCode", "VS Code"},
	"visual studio code": {"com.microsoft.VSCode", "VS Code"}, "cursor": {"com.todesktop.230313mzl4w4u92", "Cursor"},
	"terminal": {"com.apple.Terminal", "Terminal"}, "iterm": {"com.googlecode.iterm2", "iTerm"}, "iterm2": {"com.googlecode.iterm2", "iTerm"},
	"finder": {"com.apple.finder", "Finder"}, "firefox": {"org.mozilla.firefox", "Firefox"}, "arc": {"company.thebrowser.Browser", "Arc"},
	"notion": {"notion.id", "Notion"}, "obsidian": {"md.obsidian", "Obsidian"}, "figma": {"com.figma.Desktop", "Figma"},
	"xcode": {"com.apple.dt.Xcode", "Xcode"}, "spotify": {"com.spotify.client", "Spotify"}, "calendar": {"com.apple.iCal", "Calendar"},
	"reminders": {"com.apple.reminders", "Reminders"}, "preview": {"com.apple.Preview", "Preview"},
	"outlook": {"com.microsoft.Outlook", "Outlook"}, "microsoft outlook": {"com.microsoft.Outlook", "Outlook"},
	"excel": {"com.microsoft.Excel", "Excel"}, "microsoft excel": {"com.microsoft.Excel", "Excel"},
	"word": {"com.microsoft.Word", "Word"}, "microsoft word": {"com.microsoft.Word", "Word"},
	"powerpoint": {"com.microsoft.Powerpoint", "PowerPoint"}, "1password": {"com.1password.1password", "1Password"},
	"claude": {"com.anthropic.claudefordesktop", "Claude"}, "chatgpt": {"com.openai.chat", "ChatGPT"},
}

type appMatch struct {
	id, name string
	domain   bool
}

// appKeys lists the lower-case phrases a KnownApp answers to.
func appKeys(app KnownApp) []string {
	keys := make([]string, 0, 3+len(app.Aliases))
	add := func(k string) {
		k = strings.Join(strings.Fields(search.FoldLower(k)), " ")
		if len(k) >= 2 {
			keys = append(keys, k)
		}
	}
	add(app.Name)
	for _, a := range app.Aliases {
		add(a)
	}
	id := search.FoldLower(app.ID)
	if app.Domain {
		parts := strings.Split(id, ".")
		if len(parts) >= 2 {
			add(parts[len(parts)-2])
		}
		add(id)
	} else if i := strings.LastIndexByte(id, '.'); i >= 0 {
		add(id[i+1:])
	} else {
		add(id)
	}
	return keys
}

// matchApps finds apps and sites named by single words, two- and three-word
// phrases. consumed collects the words used so they are not topic terms.
func matchApps(q string, ws []word, apps []KnownApp) (ids, names, domains []string, consumed map[string]struct{}) {
	var known map[string]appMatch
	if len(apps) > 0 {
		known = make(map[string]appMatch, len(apps)*3)
		for _, app := range apps {
			for _, key := range appKeys(app) {
				if _, dup := known[key]; !dup {
					name := app.Name
					if name == "" {
						name = app.ID
					}
					known[key] = appMatch{id: app.ID, name: name, domain: app.Domain}
				}
			}
		}
	}
	lookup := func(key string) (appMatch, bool) {
		if m, ok := known[key]; ok {
			return m, true
		}
		if m, ok := builtinApps[key]; ok {
			return appMatch{id: m.id, name: m.name}, true
		}
		return appMatch{}, false
	}
	for i := 0; i < len(ws); i++ {
		for n := 3; n >= 1; n-- {
			if i+n > len(ws) {
				continue
			}
			key := ws[i].s
			if n > 1 {
				parts := make([]string, n)
				for j := range parts {
					parts[j] = ws[i+j].s
				}
				key = strings.Join(parts, " ")
			} else {
				key = strings.TrimSuffix(strings.TrimSuffix(key, "'s"), "’s")
			}
			m, ok := lookup(key)
			if !ok {
				continue
			}
			if n == 1 {
				if _, amb := ambiguousApp[key]; amb {
					prevOK := false
					if i > 0 {
						_, prevOK = locativeWords[ws[i-1].s]
					}
					nextOK := i+1 < len(ws) && ws[i+1].s == "app"
					if !prevOK && !nextOK {
						continue
					}
				}
			}
			if m.domain {
				d, err := search.CanonicalDomain(m.id)
				if err != nil || d == "" || containsString(domains, d) {
					i += n - 1
					break
				}
				domains = append(domains, d)
			} else {
				id, err := search.CanonicalAppID(m.id)
				if err != nil || id == "" || containsString(ids, id) {
					i += n - 1
					break
				}
				ids = append(ids, id)
				names = append(names, m.name)
			}
			if consumed == nil {
				consumed = make(map[string]struct{})
			}
			for j := 0; j < n; j++ {
				consumed[ws[i+j].s] = struct{}{}
			}
			i += n - 1
			break
		}
	}
	return ids, names, domains, consumed
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

var fileExtensions = map[string]struct{}{
	"html": {}, "htm": {}, "go": {}, "md": {}, "txt": {}, "pdf": {}, "png": {}, "jpg": {}, "jpeg": {}, "gif": {}, "json": {},
	"js": {}, "ts": {}, "py": {}, "doc": {}, "docx": {}, "xls": {}, "xlsx": {}, "csv": {}, "zip": {}, "swift": {}, "rs": {},
	"yaml": {}, "yml": {}, "css": {}, "sh": {}, "log": {}, "pptx": {}, "key": {}, "rb": {}, "mod": {}, "sum": {},
}

// findDomains extracts web domains written in the question ("github.com",
// "https://news.ycombinator.com/item"). Tokens that look like file names or
// version numbers are ignored.
func findDomains(q string, consumed map[string]struct{}) ([]string, map[string]struct{}) {
	var domains []string
	for _, field := range strings.Fields(q) {
		field = strings.Trim(field, "!?.,:;()[]{}\"'<>")
		if i := strings.Index(field, "://"); i >= 0 {
			field = field[i+3:]
		}
		if i := strings.IndexAny(field, "/?#"); i >= 0 {
			field = field[:i]
		}
		if i := strings.LastIndexByte(field, '@'); i >= 0 {
			continue
		}
		if !strings.Contains(field, ".") {
			continue
		}
		field = strings.TrimPrefix(strings.ToLower(field), "www.")
		labels := strings.Split(field, ".")
		tld := labels[len(labels)-1]
		if len(labels) < 2 || len(tld) < 2 || len(tld) > 24 {
			continue
		}
		ok := true
		for _, r := range tld {
			if r < 'a' || r > 'z' {
				ok = false
			}
		}
		if _, file := fileExtensions[tld]; file || !ok {
			continue
		}
		domain, err := search.CanonicalDomain(field)
		if err != nil || domain == "" || containsString(domains, domain) {
			continue
		}
		domains = append(domains, domain)
		if consumed == nil {
			consumed = make(map[string]struct{})
		}
		for _, l := range labels {
			consumed[l] = struct{}{}
		}
	}
	return domains, consumed
}

func dayStart(t time.Time, offsetDays int) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day()+offsetDays, 0, 0, 0, 0, t.Location())
}

func atHour(t time.Time, hour int) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, t.Location())
}

func parseUnit(w string) byte {
	switch w {
	case "hour", "hours", "hr", "hrs":
		return 'h'
	case "minute", "minutes", "min", "mins":
		return 'm'
	case "day", "days":
		return 'd'
	}
	return 0
}

// detectScope finds the time window a question names, computed in
// now.Location() with Start inclusive and End exclusive. The returned words are
// those consumed by the window ("7" in "last 7 days", "november" in
// "8 november afternoon") so they do not become search terms.
func detectScope(ws []word, now time.Time) (Scope, []string) {
	mk := func(start, end time.Time, label string) Scope {
		return Scope{Start: start, End: end, Label: label, Explicit: true}
	}
	for i := 0; i+1 < len(ws); i++ {
		if ws[i].s != "last" && ws[i].s != "past" {
			continue
		}
		if unit := parseUnit(ws[i+1].s); unit == 'h' || unit == 'm' {
			if unit == 'h' {
				return mk(now.Add(-time.Hour), now, "last hour"), nil
			}
			return mk(now.Add(-time.Minute), now, "last minute"), nil
		}
		if i+2 < len(ws) {
			if n, err := strconv.Atoi(ws[i+1].s); err == nil && n >= 1 {
				switch unit := parseUnit(ws[i+2].s); {
				case unit == 'h' && n <= 720:
					return mk(now.Add(-time.Duration(n)*time.Hour), now, "last "+ws[i+1].s+" hours"), []string{ws[i+1].s}
				case unit == 'm' && n <= 10080:
					return mk(now.Add(-time.Duration(n)*time.Minute), now, "last "+ws[i+1].s+" minutes"), []string{ws[i+1].s}
				case unit == 'd' && n <= 366:
					return mk(now.AddDate(0, 0, -n), now, "last "+ws[i+1].s+" days"), []string{ws[i+1].s}
				}
			}
		}
	}
	if scope, used, ok := dayPartScope(ws, now); ok {
		return scope, used
	}
	sinceMonday := (int(now.Weekday()) + 6) % 7
	switch {
	case hasSeq(ws, "last", "week"):
		return mk(dayStart(now, -sinceMonday-7), dayStart(now, -sinceMonday), "last week"), nil
	case hasSeq(ws, "this", "week"):
		return mk(dayStart(now, -sinceMonday), now, "this week"), nil
	case hasSeq(ws, "this", "month"):
		return mk(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), now, "this month"), nil
	case hasSeq(ws, "this", "morning"):
		return mk(atHour(now, 6), atHour(now, 12), "this morning"), nil
	case hasSeq(ws, "this", "afternoon"):
		return mk(atHour(now, 12), atHour(now, 18), "this afternoon"), nil
	case hasSeq(ws, "this", "evening"):
		return mk(atHour(now, 18), dayStart(now, 1), "this evening"), nil
	case hasWord(ws, "tonight"):
		return mk(atHour(now, 18), dayStart(now, 1), "tonight"), nil
	case hasWord(ws, "yesterday"):
		return mk(dayStart(now, -1), dayStart(now, 0), "yesterday"), nil
	}
	for i, w := range ws {
		wd, ok := weekdayWords[w.s]
		if !ok {
			continue
		}
		back := (int(now.Weekday()) - int(wd) + 7) % 7
		if back == 0 && i > 0 && ws[i-1].s == "last" {
			back = 7
		}
		name := strings.ToUpper(w.s[:1]) + w.s[1:]
		return mk(dayStart(now, -back), dayStart(now, -back+1), "on "+name), nil
	}
	switch {
	case hasWord(ws, "today"):
		return mk(dayStart(now, 0), now, "today"), nil
	case hasSeq(ws, "right", "now"), hasWord(ws, "currently"), hasSeq(ws, "at", "the", "moment"), hasSeq(ws, "at", "moment"):
		return mk(now.Add(-30*time.Minute), now, "right now"), nil
	case hasWord(ws, "earlier"):
		return mk(dayStart(now, 0), now, "earlier today"), nil
	case hasWord(ws, "recently", "lately"):
		return mk(now.AddDate(0, 0, -3), now, "recently"), nil
	case detectRecency(ws):
		return mk(now.Add(-ScopeJustNowWindow), now, ScopeJustNow), nil
	}
	return Scope{}, nil
}

func detectTimeKind(ws []word) bool {
	switch {
	case hasSeq(ws, "how", "much", "time"), hasSeq(ws, "how", "long"), hasSeq(ws, "time", "spent"),
		hasSeq(ws, "how", "many", "hours"), hasSeq(ws, "how", "many", "minutes"), hasSeq(ws, "how", "many", "hrs"):
		return true
	}
	return hasWord(ws, "spent", "spend", "spending") && hasWord(ws, "time", "hours", "minutes", "hrs")
}

func detectCaptureKind(ws []word) bool {
	if hasWord(ws, "capture", "captured", "captures", "capturing") {
		return true
	}
	for i, w := range ws {
		if w.s != "you" {
			continue
		}
		for j := i + 1; j < len(ws) && j <= i+2; j++ {
			switch ws[j].s {
			case "see", "saw", "seen", "record", "recorded", "save", "saved", "store", "stored", "keep", "kept":
				return true
			}
		}
	}
	return false
}

// detectWholeDocumentRequest is deliberately explicit. Broadening ordinary
// questions such as "what does this page say?" would make routine recall slow
// and could join evidence from unrelated visits. Explicit summary requests and
// clear "what is this page about?" requests expand to the whole source; detail
// questions continue to retrieve matching passages.
func detectWholeDocumentRequest(ws []word) bool {
	summary := hasWord(ws,
		"summarize", "summarise", "summary", "résume", "resume", "résumer", "resumer",
		"overview", "recap", "gist", "takeaway", "takeaways", "breakdown", "walkthrough",
	) || hasSeq(ws, "key", "points") || hasSeq(ws, "main", "idea") || hasSeq(ws, "main", "takeaway")
	action := summary || hasWord(ws,
		"analyze", "analyse", "analyser", "review", "explain", "describe", "outline",
		"walk", "inspect", "digest",
	)
	complete := hasWord(ws, "whole", "entire", "full", "complete", "all", "everything", "entirety", "completely", "tout", "toute", "tous", "toutes", "entier", "entiere")
	document := hasWord(ws, "page", "webpage", "website", "site", "article", "document", "doc", "source", "file", "report", "transcript", "text", "content", "email", "thread", "conversation")
	aboutSubject := hasWord(ws, "page", "webpage", "website", "site", "article", "document", "doc", "source", "file", "report", "text", "content")
	aboutQuestion := aboutSubject && hasWord(ws, "what", "what's", "what’s") && hasWord(ws, "is", "was", "are", "were", "what's", "what’s") && hasWord(ws, "about")
	if hasSeq(ws, "full", "path") && !summary {
		return false // “full path” is a property request, not a whole-file request.
	}
	return document && (summary || action && (complete || hasWord(ws, "document", "doc", "page", "webpage", "article", "source", "file", "report", "transcript", "email", "thread", "conversation")) || aboutQuestion)
}

func isWholeDocumentGrammarWord(word string) bool {
	switch word {
	case "summarize", "summarise", "summary", "résume", "resume", "résumer", "resumer",
		"analyze", "analyse", "analyser", "review", "explain", "describe", "outline",
		"overview", "recap", "gist", "takeaway", "takeaways", "breakdown", "walkthrough", "key", "point", "points", "idea", "walk", "inspect", "digest", "captured",
		"whole", "entire", "full", "complete", "all", "everything", "entirety", "completely", "tout", "toute", "tous", "toutes", "entier", "entiere", "entière", "la", "le", "les", "sur",
		"page", "webpage", "website", "site", "article", "document", "doc", "source", "file", "report", "transcript", "text", "content", "email", "thread", "conversation":
		return true
	default:
		return false
	}
}

func detectRecapPattern(ws []word) bool {
	for _, who := range []string{"i", "we"} {
		switch {
		case hasSeq(ws, "what", "did", who, "do"), hasSeq(ws, "what", "have", who, "done"), hasSeq(ws, "what", "was", who, "doing"),
			hasSeq(ws, "what", "were", who, "doing"), hasSeq(ws, "what", "have", who, "been", "doing"), hasSeq(ws, "what", "did", who, "work", "on"),
			hasSeq(ws, "what", "have", who, "worked", "on"), hasSeq(ws, "what", "was", who, "working", "on"):
			return true
		}
	}
	return hasSeq(ws, "what", "happened") || (startsWith(ws, "how", "was", "my") && hasWord(ws, "day", "morning", "afternoon", "week", "evening"))
}

// detectOutstandingSubject recognizes project-scoped follow-up questions that
// ask for unresolved work, so they retrieve saved project knowledge and open
// tasks instead of treating the intent word as a search topic.
func detectOutstandingSubject(ws []word) string {
	prefixes := [][]string{
		{"what's", "outstanding", "for"}, {"what’s", "outstanding", "for"}, {"whats", "outstanding", "for"}, {"what", "is", "outstanding", "for"},
		{"what's", "outstanding", "with"}, {"what’s", "outstanding", "with"}, {"whats", "outstanding", "with"}, {"what", "is", "outstanding", "with"},
		{"what's", "outstanding", "on"}, {"what’s", "outstanding", "on"}, {"whats", "outstanding", "on"}, {"what", "is", "outstanding", "on"},
		{"what", "is", "outstanding", "at"}, {"what", "are", "outstanding", "for"}, {"what", "are", "outstanding", "with"},
		{"what", "are", "outstanding", "on"}, {"what", "are", "outstanding", "at"}, {"what", "remains", "outstanding", "for"},
		{"what", "are", "the", "outstanding", "items", "for"}, {"what", "is", "still", "pending", "with"},
		{"anything", "left", "to", "do", "for"}, {"anything", "still", "open", "for"},
		{"anything", "outstanding", "for"}, {"any", "outstanding", "items", "for"},
		{"what", "follow", "ups", "are", "still", "open", "for"}, {"what", "follow", "ups", "are", "open", "for"},
		{"what", "do", "i", "still", "owe"}, {"what", "do", "we", "still", "owe"},
		{"what", "do", "i", "owe"}, {"what", "do", "we", "owe"},
		{"what", "are", "we", "waiting", "for", "from"}, {"what", "are", "we", "waiting", "on", "from"}, {"what", "are", "we", "still", "waiting", "on", "from"},
		{"what", "are", "we", "still", "waiting", "for", "from"}, {"what", "am", "i", "waiting", "for", "from"},
		{"where", "did", "we", "leave", "things", "with"}, {"where", "did", "i", "leave", "things", "with"},
		{"what's", "still", "open", "for"}, {"what’s", "still", "open", "for"}, {"whats", "still", "open", "for"},
		{"what", "is", "still", "open", "for"}, {"what", "is", "left", "for"},
		{"what", "is", "left", "to", "do", "for"}, {"what", "remains", "for"},
		{"what", "remains", "to", "do", "for"}, {"what", "still", "needs", "doing", "for"},
		{"what", "do", "i", "need", "to", "do", "for"}, {"what", "should", "i", "do", "for"},
		{"what", "do", "i", "have", "to", "do", "for"}, {"what", "do", "i", "still", "need", "to", "do", "for"},
		{"what", "have", "i", "got", "to", "do", "for"},
		{"what", "should", "we", "do", "for"}, {"what", "do", "we", "need", "to", "do", "for"},
		{"what", "are", "the", "next", "steps", "for"},
		{"what's", "on", "my", "plate", "for"}, {"what’s", "on", "my", "plate", "for"}, {"whats", "on", "my", "plate", "for"},
		{"what", "is", "on", "my", "plate", "for"}, {"what", "tasks", "do", "i", "have", "for"},
		{"what", "are", "my", "tasks", "for"}, {"which", "tasks", "are", "still", "open", "for"},
		{"any", "pending", "work", "for"}, {"what's", "the", "next", "step", "for"}, {"what’s", "the", "next", "step", "for"},
		{"whats", "the", "next", "step", "for"}, {"what", "is", "the", "next", "step", "for"},
		{"what", "needs", "follow", "up", "for"}, {"what", "are", "the", "open", "items", "for"},
		{"what", "are", "we", "waiting", "on", "for"}, {"what", "is", "pending", "for"},
		{"any", "blockers", "for"}, {"what", "are", "the", "blockers", "for"},
		{"what", "still", "needs", "attention", "for"},
	}
	for _, prefix := range prefixes {
		if startsWith(ws, prefix...) && len(ws) > len(prefix) {
			return strings.TrimSpace(strings.Join(wordStrings(ws[len(prefix):]), " "))
		}
	}
	// Keep project open-work recognition tolerant of ordinary paraphrases.
	// Searching a fixed list of complete sentence prefixes sent phrases like
	// “open loops”, “what's on the table”, and “what are we blocked on” through
	// general capture search, where an unrelated sign-in page could win.
	for i := len(ws) - 1; i > 0; i-- {
		switch ws[i].s {
		case "for", "with", "on", "from", "at":
		default:
			continue
		}
		if i == len(ws)-1 {
			continue
		}
		prefix, subject := ws[:i], ws[i+1:]
		if hasOutstandingCue(prefix) && !hasSeq(ws, "open", "source") && !hasWord(ws, "balance", "balances", "invoice", "invoices", "payment", "payments", "bill", "bills", "debt", "amount") {
			return strings.TrimSpace(strings.Join(wordStrings(subject), " "))
		}
	}
	return ""
}

func hasOutstandingCue(ws []word) bool {
	return hasWord(ws, "open", "outstanding", "pending", "remaining", "remain", "left", "unresolved", "blocked", "blocker", "blockers") ||
		hasSeq(ws, "open", "loop") || hasSeq(ws, "open", "loops") ||
		hasSeq(ws, "next", "step") || hasSeq(ws, "next", "steps") ||
		hasSeq(ws, "follow", "up") || hasSeq(ws, "follow", "ups") ||
		hasSeq(ws, "on", "my", "plate") || hasSeq(ws, "on", "the", "table") ||
		hasSeq(ws, "have", "to", "do") || hasSeq(ws, "need", "to", "do") ||
		hasWord(ws, "owe") || hasSeq(ws, "waiting", "for") || hasSeq(ws, "waiting", "on")
}

func wordStrings(ws []word) []string {
	words := make([]string, len(ws))
	for i, w := range ws {
		words[i] = w.s
	}
	return words
}

func IsOutstandingQuestion(question string) bool {
	return detectOutstandingSubject(splitWords(question)) != ""
}

func hasSummaryWord(ws []word) bool {
	return hasWord(ws, "recap", "summary", "summarize", "summarise", "summarized", "summarised")
}

// entityPrefixes introduce a question about a person, project, or thing.
var entityPrefixes = [][]string{
	{"what", "or", "who", "is"}, {"who", "is"}, {"who", "s"}, {"who's"}, {"who", "are"}, {"what", "do", "you", "know", "about"},
	{"what", "do", "you", "remember", "about"}, {"tell", "me", "about"}, {"do", "you", "know"}, {"do", "you", "remember"},
}

func detectEntity(q string, ws []word) string {
	for _, prefix := range entityPrefixes {
		if !startsWith(ws, prefix...) || len(ws) == len(prefix) {
			continue
		}
		rest := q[ws[len(prefix)].start:ws[len(ws)-1].end]
		return truncateUTF8(strings.TrimSpace(rest), 128)
	}
	return ""
}

// factualPossessiveSubject keeps a named subject separate from the attribute
// being asked about. A saved fact about Mira can ground "Mira's deadline"
// without already containing that deadline. The probe still requires the
// subject to match local evidence and keeps the complete original question.
func factualPossessiveSubject(q string, ws []word) string {
	subject, _ := factualPossessiveSubjectAndAttribute(q, ws)
	return subject
}

func factualPossessiveSubjectAndAttribute(q string, ws []word) (string, string) {
	for _, prefix := range [][]string{
		{"what", "is"}, {"what", "are"}, {"what's"}, {"what’s"},
		{"who", "is"}, {"who", "are"}, {"who's"}, {"who’s"},
		{"when", "is"}, {"when", "are"}, {"when's"}, {"when’s"},
		{"where", "is"}, {"where", "are"}, {"where's"}, {"where’s"},
	} {
		if !startsWith(ws, prefix...) {
			continue
		}
		start := len(prefix)
		// Bound the subject to four words, and require an attribute after it.
		for i := start; i < len(ws)-1 && i < start+4; i++ {
			for _, suffix := range []string{"'s", "’s"} {
				if !strings.HasSuffix(ws[i].s, suffix) {
					continue
				}
				subject := strings.TrimSpace(q[ws[start].start : ws[i].end-len(suffix)])
				if len(subject) <= 128 && strings.ContainsFunc(subject, unicode.IsLetter) {
					attribute := strings.TrimSpace(q[ws[i].end:ws[len(ws)-1].end])
					return subject, attribute
				}
				return "", ""
			}
		}
		return "", ""
	}
	return "", ""
}

var namedAttributeSynonymGroups = [][]string{
	{"deployment", "deploy", "rollout", "release", "launch"},
	{"deadline", "due", "date", "target", "schedule", "scheduled", "timeline", "planned", "planning", "today", "tomorrow", "tonight", "week", "month", "year", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday", "january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"},
	{"owner", "own", "owns", "responsible", "responsibility", "lead", "leads", "leader", "manager", "handles"},
	{"job", "occupation", "role", "title", "position", "profession", "leader", "lead", "leads", "manager", "supervisor"},
	{"team", "group", "squad", "department", "organization", "org"},
}

func namedAttributeIsUnsupported(question string, evidence []Evidence) bool {
	subject, attribute := factualPossessiveSubjectAndAttribute(question, splitWords(question))
	if attribute == "" {
		return false
	}
	terms, err := planTerms(attribute, nil, false)
	if err != nil || len(terms) == 0 {
		return false
	}
	for _, item := range evidence {
		for _, statement := range namedAttributeStatements(item.Text) {
			statementTerms := make(map[string]struct{})
			for _, token := range splitWords(statement) {
				statementTerms[token.s] = struct{}{}
			}
			if !evidenceContainsSubject(statementTerms, subject) {
				continue
			}
			checkedGroups := make(map[string]struct{})
			allMatched := true
			for _, term := range terms {
				group := namedAttributeSynonyms(term)
				key := group[0]
				if _, checked := checkedGroups[key]; checked {
					continue
				}
				checkedGroups[key] = struct{}{}
				matched := false
				for _, synonym := range group {
					if _, ok := statementTerms[synonym]; ok {
						matched = true
						break
					}
				}
				if !matched {
					allMatched = false
					break
				}
			}
			if allMatched {
				return false
			}
		}
	}
	return true
}

func namedAttributeMissingCopy(question string) string {
	subject, attribute := factualPossessiveSubjectAndAttribute(question, splitWords(question))
	if subject == "" || attribute == "" {
		return InsufficientEvidenceText
	}
	return "I found related information about " + subject + ", but not the " + attribute + "."
}

func namedAttributeSynonyms(term string) []string {
	for _, synonyms := range namedAttributeSynonymGroups {
		if slices.Contains(synonyms, term) {
			return synonyms
		}
	}
	return []string{term}
}

func evidenceContainsSubject(terms map[string]struct{}, subject string) bool {
	for _, subjectTerm := range splitWords(subject) {
		if _, ok := terms[subjectTerm.s]; ok {
			continue
		}
		if _, ok := terms[subjectTerm.s+"'s"]; !ok {
			if _, ok := terms[subjectTerm.s+"’s"]; !ok {
				return false
			}
		}
	}
	return true
}

func namedAttributeStatements(text string) []string {
	var statements []string
	start := 0
	for offset, r := range text {
		width := utf8.RuneLen(r)
		boundary := r == '?' || r == '!' || r == ';' || r == '\n' || r == '\r'
		if r == '.' {
			next := offset + width
			if next == len(text) {
				boundary = true
			} else {
				nextRune, _ := utf8.DecodeRuneInString(text[next:])
				boundary = unicode.IsSpace(nextRune)
			}
		}
		if boundary {
			if statement := strings.TrimSpace(text[start:offset]); statement != "" {
				statements = append(statements, statement)
			}
			start = offset + width
		}
	}
	if statement := strings.TrimSpace(text[start:]); statement != "" {
		statements = append(statements, statement)
	}
	return statements
}

// memoryLookupCandidate does not change the general plan unless local
// retrieval actually finds evidence. It never turns a request to write or
// explain something into a memory lookup.
func memoryLookupCandidate(plan Plan, apps []KnownApp) (Plan, bool) {
	if IsCreationRequest(plan.Question) {
		return Plan{}, false
	}
	ws := splitWords(plan.Question)
	subject := factualPossessiveSubject(plan.Question, ws)
	if subject == "" {
		subject = detectEntity(plan.Question, ws)
	}
	if subject == "" {
		for _, prefix := range [][]string{{"what", "or", "who", "is"}, {"who", "or", "what", "is"}, {"what", "is"}, {"what", "s"}, {"what's"}, {"what", "are"}} {
			if startsWith(ws, prefix...) && len(ws) > len(prefix) {
				subject = plan.Question[ws[len(prefix)].start:ws[len(ws)-1].end]
				break
			}
		}
	}
	kind := KindEntity
	if subject == "" && len(ws) > 0 && len(ws) <= 4 {
		if !hasWord(ws, "what", "who", "how", "why", "when", "where", "can", "could", "should", "do", "does", "did", "is", "are") {
			subject = plan.Question
			// A bare topic phrase such as "latest security news" asks for
			// matching history; it is not a Library entity name.
			kind = KindLookup
		}
	}
	if subject == "" && len(ws) > 0 {
		// Factual questions can name a project without saying "my". Probe
		// their topic locally; finding no evidence preserves the general route.
		switch ws[0].s {
		case "what", "who", "which", "where", "when", "why", "how":
			subject, kind = plan.Question, KindLookup
		}
	}
	if !strings.ContainsFunc(subject, unicode.IsLetter) {
		return Plan{}, false
	}
	_, _, _, consumed := matchApps(plan.Question, ws, apps)
	terms, err := planTerms(subject, consumed, !plan.Scope.IsZero())
	if err != nil || len(terms) == 0 {
		return Plan{}, false
	}
	meaningful := false
	for _, term := range terms {
		meaningful = meaningful || strings.ContainsFunc(term, unicode.IsLetter)
	}
	if !meaningful {
		return Plan{}, false
	}
	plan.Kind, plan.Terms, plan.MatchOnly = kind, terms, true
	if kind == KindEntity {
		plan.Entity = truncateUTF8(strings.TrimSpace(subject), 128)
	}
	return plan, true
}

// IsFollowUp reports whether a question reads as a short continuation of the
// previous one ("and in Slack?", "what about yesterday?", "who said that?")
// rather than a fresh question, so the service may inherit scope and apps
// from the previous turn.
func IsFollowUp(question string) bool {
	if IsGroundedFollowUp(question) {
		return true
	}
	ws := splitWords(strings.TrimSpace(question))
	if len(ws) == 0 || len(ws) > 8 {
		return false
	}
	switch ws[0].s {
	case "and", "also", "then", "but", "plus", "same", "more", "else", "ok", "okay", "now":
		return true
	case "in", "on", "from", "at", "during", "for", "only", "just":
		if len(ws) <= 4 {
			return true
		}
	}
	for _, p := range [][]string{{"what", "about"}, {"how", "about"}, {"what", "else"}, {"which", "one"}, {"any", "other"}, {"anything", "else"}} {
		if startsWith(ws, p...) {
			return true
		}
	}
	if len(ws) <= 3 && (hasWord(ws, "today", "yesterday", "tonight", "morning", "afternoon", "evening", "week", "month", "hour") || func() bool {
		for _, w := range ws {
			if _, ok := weekdayWords[w.s]; ok {
				return true
			}
		}
		return false
	}()) {
		return true
	}
	return len(ws) <= 6 && hasWord(ws, "it", "that", "those", "them", "they", "he", "she", "there")
}

// IsGroundedFollowUp recognises bounded references to the preceding answer.
// Explicit new text or a new named subject must keep its own request route.
func IsGroundedFollowUp(question string) bool {
	return classifyGroundedFollowup(question) != followupNone
}

type groundedFollowupKind uint8

const (
	followupNone groundedFollowupKind = iota
	followupAnswer
	followupSubject
)

func classifyGroundedFollowup(question string) groundedFollowupKind {
	q := strings.TrimRight(strings.TrimSpace(question), ".!?")
	if strings.ContainsAny(q, ":\n\r;\"?!") {
		return followupNone
	}
	// Dots inside domains, decimal numbers, or abbreviations are topic
	// content. A dot separating words starts new supplied content instead.
	runes := []rune(q)
	for i, r := range runes {
		if r == '.' && (i == 0 || i+1 == len(runes) || (!unicode.IsLetter(runes[i-1]) && !unicode.IsDigit(runes[i-1])) || (!unicode.IsLetter(runes[i+1]) && !unicode.IsDigit(runes[i+1]))) {
			return followupNone
		}
	}
	q = strings.ToLower(strings.Join(strings.Fields(q), " "))
	for _, prefix := range []string{"and ", "also ", "then ", "please ", "can you ", "could you ", "would you ", "please "} {
		q = strings.TrimSpace(strings.TrimPrefix(q, prefix))
	}
	ws := splitWords(q)
	if len(ws) == 0 || len(ws) > 12 {
		return followupNone
	}
	// A short attribute continuation can omit the question words: "and his
	// email" still refers to the previously established person or project.
	if len(ws) >= 2 && len(ws) <= 5 && (ws[0].s == "his" || ws[0].s == "her" || ws[0].s == "their" || ws[0].s == "its") &&
		!hasWord(ws, "save", "delete", "remove", "send", "schedule", "create", "cancel", "complete", "run", "open", "launch", "publish", "buy", "pay") {
		return followupSubject
	}
	// A transformation verb plus an answer reference carries arbitrary style
	// vocabulary. Bounded predicates avoid an ever-growing adjective list.
	if startsWith(ws, "make") {
		objectEnd := 0
		for _, object := range [][]string{{"make", "it"}, {"make", "that"}, {"make", "this"}, {"make", "your", "answer"}, {"make", "the", "answer"}} {
			if startsWith(ws, object...) {
				objectEnd = len(object)
				break
			}
		}
		if objectEnd > 0 && len(ws) > objectEnd && len(ws)-objectEnd <= 6 && !hasWord(ws[objectEnd:], "save", "store", "note", "notes", "task", "tasks", "reminder", "reminders", "schedule", "send", "delete", "remove", "forget", "remember", "capture", "click", "open", "launch", "quit", "restart", "upload", "publish", "execute", "run", "call", "buy", "pay", "happen", "here") {
			return followupAnswer
		}
		return followupNone
	}
	for _, prefix := range [][]string{
		{"summarize", "that"}, {"summarise", "that"}, {"summarize", "it"}, {"summarise", "it"}, {"summarize", "this"}, {"summarise", "this"},
		{"explain", "that"}, {"explain", "it"}, {"explain", "this"}, {"explain", "your", "answer"},
		{"rewrite", "that"}, {"rewrite", "it"}, {"rephrase", "that"}, {"rephrase", "it"},
		{"what", "does", "that", "mean"}, {"what", "does", "it", "mean"},
		{"who", "said", "that"}, {"where", "did", "that", "come", "from"},
		{"what", "are", "the", "sources"}, {"show", "the", "sources"},
	} {
		if startsWith(ws, prefix...) {
			// A new noun after a demonstrative is not an answer reference:
			// "Explain that compiler error" is its own request.
			if (prefix[0] == "explain" || prefix[0] == "summarize" || prefix[0] == "summarise" || prefix[0] == "rewrite" || prefix[0] == "rephrase") && len(prefix) == 2 && len(ws) > 2 {
				switch ws[2].s {
				case "in", "with", "more", "again", "briefly", "simply", "as", "for", "using":
				default:
					return followupNone
				}
			}
			return followupAnswer
		}
	}
	// Personal pronouns are references only in a factual question. This does
	// not capture independent writing requests such as "write her a poem".
	if startsWith(ws, "what") || startsWith(ws, "who") || startsWith(ws, "when") || startsWith(ws, "where") || startsWith(ws, "why") || startsWith(ws, "how") {
		if hasWord(ws, "she", "he", "her", "his", "their", "they", "them") {
			return followupSubject
		}
	}
	return followupNone
}

func planTerms(q string, consumed map[string]struct{}, scopeFound bool) ([]string, error) {
	terms, err := search.NormalizeTokens(q, MaxQuestionBytes)
	if err != nil {
		return nil, ErrContext
	}
	// consumed holds planner words (folded, possessives intact); terms are in
	// NormalizeTokens space, so compare through the one shared fold.
	used := make(map[string]struct{}, 2*len(consumed))
	for word := range consumed {
		used[word] = struct{}{}
		used[search.FoldWord(word)] = struct{}{}
	}
	consumed = used
	var out []string
	var sourceTypeTerms []string
	for _, term := range terms {
		if _, stop := questionStopWords[term]; stop {
			continue
		}
		if _, stop := planStopWords[term]; stop {
			continue
		}
		if _, used := consumed[term]; used {
			continue
		}
		// A source-type noun such as "email" is a useful fallback for a
		// source-only question, but it should not become a required exact
		// token when the user also supplied the real topic ("integrity emails").
		if term == "email" || term == "emails" {
			sourceTypeTerms = append(sourceTypeTerms, term)
			continue
		}
		if _, day := weekdayWords[term]; day && scopeFound {
			continue
		}
		out = append(out, term)
		if len(out) == search.MaxTerms {
			break
		}
	}
	if len(out) == 0 {
		out = sourceTypeTerms
	}
	return out, nil
}
