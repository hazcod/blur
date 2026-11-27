package intent

import "strings"

// Only a closed collection-read grammar is adapted. Unknown topical content,
// quotations, reported text and creation commands never become read authority
// by translating arbitrary words. Returned filters use the shared local path.
func dutchTaskListRequest(q string) (TaskListRead, bool) {
	fields := strings.Fields(q)
	if len(fields) == 0 {
		return TaskListRead{}, false
	}
	switch fields[0] {
	case "welke", "toon", "hoeveel":
	case "wat":
		if !strings.HasPrefix(q, "wat moet ik nog doen") {
			return TaskListRead{}, false
		}
		fields = strings.Fields("what do i still need to do" + strings.TrimPrefix(q, "wat moet ik nog doen"))
	default:
		return TaskListRead{}, false
	}
	aliases := map[string]string{
		"welke": "which", "toon": "show", "hoeveel": "how many", "wat": "what", "moet": "should", "ik": "i", "nog": "still", "doen": "do",
		"taken": "tasks", "taak": "task", "herinneringen": "reminders", "herinnering": "reminder", "takenlijst": "tasks",
		"mijn": "my", "onze": "our", "staan": "are", "staat": "is", "heb": "have", "hebben": "have", "alle": "all", "en": "and",
		"afgeronde": "completed", "afgerond": "completed", "voltooide": "completed", "voltooid": "completed", "geannuleerde": "cancelled", "geannuleerd": "cancelled", "onvoltooide": "unfinished",
		"van": "from", "voor": "for", "gisteren": "yesterday", "vandaag": "today", "morgen": "tomorrow", "deze": "this", "volgende": "next", "vorige": "last", "achterstallige": "overdue", "komende": "upcoming",
		"geen": "not", "niet": "not", "artikel": "article", "alsjeblieft": "please",
	}
	for i, field := range fields {
		if translated, ok := aliases[field]; ok {
			fields[i] = translated
		}
	}
	read, ok := TaskListRequest(strings.Join(fields, " "))
	if !ok || !read.Generic {
		return TaskListRead{}, false
	}
	read.Question = q
	return read, true
}
