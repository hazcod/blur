package assistant

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hazcod/blur/internal/search"
)

// The planner is the first step of Ask and never calls a model. The product
// rule is "the model last step, never first": a deterministic local reading of
// the question picks the kind, time window, and apps; structured answers (time
// spent, capture listings) are produced from local data without any model; and
// when a model is used it only ever sees a small, byte-bounded, labelled
// selection of evidence, never the whole memory.

type word struct {
	s          string // lower-case
	start, end int    // byte offsets into the original question
}

func splitWords(q string) []word {
	var out []word
	start := -1
	flush := func(end int) {
		if start >= 0 {
			w := strings.ToLower(q[start:end])
			w = strings.TrimRight(w, "'’")
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
	"any": {}, "anything": {}, "something": {}, "nothing": {}, "yet": {}, "up": {}, "out": {}, "into": {}, "there": {},
	"than": {}, "then": {}, "some": {}, "just": {}, "also": {}, "app": {}, "site": {}, "website": {}, "so": {}, "far": {},
	"today": {}, "yesterday": {}, "tonight": {}, "earlier": {}, "recently": {}, "lately": {}, "ago": {}, "now": {}, "currently": {},
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
	"mail": {"com.apple.mail", "Mail"}, "apple mail": {"com.apple.mail", "Mail"},
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
		k = strings.Join(strings.Fields(strings.ToLower(k)), " ")
		if len(k) >= 2 {
			keys = append(keys, k)
		}
	}
	add(app.Name)
	for _, a := range app.Aliases {
		add(a)
	}
	id := strings.ToLower(app.ID)
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
// now.Location() with Start inclusive and End exclusive. The returned string
// is a numeral consumed by the window ("7" in "last 7 days").
func detectScope(ws []word, now time.Time) (Scope, string) {
	mk := func(start, end time.Time, label string) Scope {
		return Scope{Start: start, End: end, Label: label, Explicit: true}
	}
	for i := 0; i+1 < len(ws); i++ {
		if ws[i].s != "last" && ws[i].s != "past" {
			continue
		}
		if unit := parseUnit(ws[i+1].s); unit == 'h' || unit == 'm' {
			if unit == 'h' {
				return mk(now.Add(-time.Hour), now, "last hour"), ""
			}
			return mk(now.Add(-time.Minute), now, "last minute"), ""
		}
		if i+2 < len(ws) {
			if n, err := strconv.Atoi(ws[i+1].s); err == nil && n >= 1 {
				switch unit := parseUnit(ws[i+2].s); {
				case unit == 'h' && n <= 720:
					return mk(now.Add(-time.Duration(n)*time.Hour), now, "last "+ws[i+1].s+" hours"), ws[i+1].s
				case unit == 'm' && n <= 10080:
					return mk(now.Add(-time.Duration(n)*time.Minute), now, "last "+ws[i+1].s+" minutes"), ws[i+1].s
				case unit == 'd' && n <= 366:
					return mk(now.AddDate(0, 0, -n), now, "last "+ws[i+1].s+" days"), ws[i+1].s
				}
			}
		}
	}
	sinceMonday := (int(now.Weekday()) + 6) % 7
	switch {
	case hasSeq(ws, "last", "week"):
		return mk(dayStart(now, -sinceMonday-7), dayStart(now, -sinceMonday), "last week"), ""
	case hasSeq(ws, "this", "week"):
		return mk(dayStart(now, -sinceMonday), now, "this week"), ""
	case hasSeq(ws, "this", "month"):
		return mk(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), now, "this month"), ""
	case hasSeq(ws, "this", "morning"):
		return mk(atHour(now, 6), atHour(now, 12), "this morning"), ""
	case hasSeq(ws, "this", "afternoon"):
		return mk(atHour(now, 12), atHour(now, 18), "this afternoon"), ""
	case hasSeq(ws, "this", "evening"):
		return mk(atHour(now, 18), dayStart(now, 1), "this evening"), ""
	case hasWord(ws, "tonight"):
		return mk(atHour(now, 18), dayStart(now, 1), "tonight"), ""
	case hasWord(ws, "yesterday"):
		return mk(dayStart(now, -1), dayStart(now, 0), "yesterday"), ""
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
		return mk(dayStart(now, -back), dayStart(now, -back+1), "on "+name), ""
	}
	switch {
	case hasWord(ws, "today"):
		return mk(dayStart(now, 0), now, "today"), ""
	case hasSeq(ws, "right", "now"), hasWord(ws, "currently"), hasSeq(ws, "at", "the", "moment"), hasSeq(ws, "at", "moment"):
		return mk(now.Add(-30*time.Minute), now, "right now"), ""
	case hasWord(ws, "earlier"):
		return mk(dayStart(now, 0), now, "earlier today"), ""
	case hasWord(ws, "recently", "lately"):
		return mk(now.AddDate(0, 0, -3), now, "recently"), ""
	}
	return Scope{}, ""
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

func hasSummaryWord(ws []word) bool {
	return hasWord(ws, "recap", "summary", "summarize", "summarise", "summarized", "summarised")
}

// entityPrefixes introduce a question about a person, project, or thing.
var entityPrefixes = [][]string{
	{"who", "is"}, {"who", "s"}, {"who's"}, {"who", "are"}, {"what", "do", "you", "know", "about"},
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

// IsFollowUp reports whether a question reads as a short continuation of the
// previous one ("and in Slack?", "what about yesterday?", "who said that?")
// rather than a fresh question, so the service may inherit scope and apps
// from the previous turn.
func IsFollowUp(question string) bool {
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

func planTerms(q string, consumed map[string]struct{}, scopeFound bool) ([]string, error) {
	terms, err := search.NormalizeTokens(q, MaxQuestionBytes)
	if err != nil {
		return nil, ErrContext
	}
	var out []string
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
		if _, day := weekdayWords[term]; day && scopeFound {
			continue
		}
		out = append(out, term)
		if len(out) == search.MaxTerms {
			break
		}
	}
	return out, nil
}
