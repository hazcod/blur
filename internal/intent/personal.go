package intent

import (
	"regexp"
	"strings"
)

const scheduleWeekday = `(?:monday|mon|tuesday|tues|tue|wednesday|weds|wed|thursday|thurs|thur|thu|friday|fri|saturday|sat|sunday|sun)\.?`
const scheduleMonth = `(?:january|jan|february|feb|march|mar|april|apr|may|june|jun|july|jul|august|aug|september|sept|sep|october|oct|november|nov|december|dec)\.?`
const scheduleMonthDate = `(?:` + scheduleMonth + ` [0-9]{1,2}(?:st|nd|rd|th)?|[0-9]{1,2}(?:st|nd|rd|th)? ` + scheduleMonth + `)(?:,? [0-9]{4})?`
const scheduleDay = `(?:(?:(?:on|for) )?(?:(?:next|this|every) )?(?:` + scheduleWeekday + `|weekend|week|month)|(?:this )?(?:morning|afternoon|evening)|day after tomorrow|today|tomorrow|tonight|(?:(?:on|for) )?(?:[0-9]{4}-[0-9]{2}-[0-9]{2}|` + scheduleMonthDate + `))`
const scheduleClock = `(?:[0-9]{1,2}(?::[0-9]{2})? ?(?:[ap]\.?m\.?)?|(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve) ?(?:[ap]\.?m\.?)|noon|midnight)`
const scheduleRelativeUnit = `(?:s|secs?|seconds?|m|mins?|minutes?|h|hrs?|hours?|d|days?|weeks?)`
const personalSchedule = `(?:` + scheduleDay + `(?: (?:morning|afternoon|evening|night))?(?: (?:at )?` + scheduleClock + `)?|in ?(?:[0-9]+ ?|[a-z]+(?:-[a-z]+)? )` + scheduleRelativeUnit + `|at ` + scheduleClock + `)(?: (?:[a-z_]+/[a-z_/]+|utc))?`

var personalTaskPrefix = regexp.MustCompile(`^i (?:(?:really|still|also|definitely|just) )*(?:(?:need|have|have got|ought|plan|intend|want|would like|will have|will need|am going|am planning|am intending) to,?|must|should|will) (.+)$`)
var personalScheduleFirst = regexp.MustCompile(`^(` + personalSchedule + `),? (i .+)$`)
var personalScheduleLast = regexp.MustCompile(`^(.+?) (` + personalSchedule + `)$`)
var shorthandScheduleFirst = regexp.MustCompile(`^(` + personalSchedule + `)(?:\s*[:,]\s*|\s+)(.+)$`)
var shorthandImperative = regexp.MustCompile(`^(?:add|archive|arrange|ask|attend|back\s+up|backup|book|bring|build|buy|call|cancel|check|clean|clear|collect|commit|compare|complete|compile|configure|confirm|contact|cook|copy|create|debug|delete|deploy|discuss|display|do|document|download|draft|drop|email|enable|evaluate|file|finish|fix|follow\s+up|gather|get|give|go|handle|install|investigate|join|leave|look|make|meet|merge|move|order|organize|pack|pay|pick|pickup|plan|post|prepare|print|publish|push|read|refactor|register|rename|renew|repair|reply|research|restart|return|review|rotate|run|save|schedule|send|set|ship|sign|sort|start|stop|submit|sync|take|test|text|track|try|turn|update|upgrade|upload|verify|visit|walk|wash|water|write)(?: |$)`)
var shorthandStatement = regexp.MustCompile(`^(?:i|you|he|she|it|we|they|the|a|an|there|this|that|these|those)(?: |$)|\b(?:is|are|was|were|has|had|owns|prefers|uses|exposes|contains|supports|returns|fails|works|opens|closes|said|says|happened|costs|means)\b`)
var calendarNounHead = regexp.MustCompile(`\b(?:appointment|collection|return|shopping|run|meeting|booking|check|payment|backup|update|review|demo|discussion|evaluation|draft|profiling|session|call|inspection|verification|triage|report|cleanup)$`)
var personalNonAction = regexp.MustCompile(`^(?:know|think|believe|understand|feel|prefer|hope|wish|expect|avoid|remember|forget)(?: |$)`)
var schedulingSpan = regexp.MustCompile(`^(?:on|for|in|at|next|this|every|today|tomorrow|tonight|before|after|by|around|sometime|monday|tuesday|wednesday|thursday|friday|saturday|sunday|[0-9]{4}-[0-9]{2}-[0-9]{2})(?: |$)`)
var personalSupportingClause = regexp.MustCompile(`(?:;|\b(?:because|since|although|though|whereas|whose|while|said|says|told|wrote|writes|reported|mentioned|mentions|replied|explained)\b)`)
var taskTimingCue = regexp.MustCompile(`\b(?:today|tomorrow|tonight|yesterday|monday|tuesday|wednesday|thursday|friday|saturday|sunday|morning|afternoon|evening|midnight|noon|next|before|after|until|sometime|soon|later|daily|weekly|monthly|at|by|in|on|every|last|ago|january|february|march|april|may|june|july|august|september|october|november|december)\b|[0-9]{1,2}:[0-9]{2}|[0-9]{4}-[0-9]{2}-[0-9]{2}`)
var scheduleSpans = regexp.MustCompile(`\b` + personalSchedule + `\b`)
var relativeDelayOnly = regexp.MustCompile(`^(?:in ?)?[0-9]{1,6} ?` + scheduleRelativeUnit + `$`)

// RelativeDelay recognizes a standalone delay. It contains no action to execute:
// callers retain the exact due time and ask for the missing reminder title.
func RelativeDelay(input string) bool {
	return clean(input, MaxInputBytes) && relativeDelayOnly.MatchString(normalized(input))
}

// ConflictingSchedule identifies a current scheduling request with more than
// one independent schedule, such as "in 5m ... tomorrow". A day with its clock
// is one span. Notes, questions and quoted/reported statements grant no such
// request authority. Callers can clarify before asking a model to choose one.
func ConflictingSchedule(input string) bool {
	q, ok := personalInput(input)
	if !ok || len(scheduleSpans.FindAllStringIndex(q, 2)) < 2 {
		return false
	}
	if authority(input, Reminder) {
		return true
	}
	clause, _, labelled, ok := scheduledParts(q)
	if !ok || personalSupportingClause.MatchString(q) {
		return false
	}
	if _, ok := personalActionBody(clause, ""); ok {
		return true
	}
	_, ok = shorthandActionBody(clause, labelled)
	return ok
}

// PersonalTask is the undated counterpart of PersonalReminder. Any timing
// cue stays on the schedule/model path rather than being silently discarded.
func PersonalTask(input string) (Action, bool) {
	q, ok := personalInput(input)
	if !ok || personalSupportingClause.MatchString(q) || taskTimingCue.MatchString(q) {
		return Action{}, false
	}
	title, ok := personalActionBody(q, "")
	if !ok {
		title = requestBody(q)
		if !shorthandImperative.MatchString(title) || strings.ContainsAny(title, ":;") {
			return Action{}, false
		}
		// These bare commands also request generated answers. Without the
		// first-person commitment their operation belongs to the model/read
		// router, rather than being silently persisted as a to-do.
		for _, prefix := range []string{"write ", "draft ", "explain ", "compare ", "debug "} {
			if strings.HasPrefix(title, prefix) {
				return Action{}, false
			}
		}
		for _, prefix := range []string{"add ", "create ", "save ", "schedule ", "set "} {
			if strings.HasPrefix(title, prefix) && (hasWord(title, "task") || hasWord(title, "tasks") || hasWord(title, "todo") || hasWord(title, "reminder") || hasWord(title, "list") || hasWord(title, "note")) {
				return Action{}, false
			}
		}
	}
	title, ok = canonicalSpan(input, title)
	return Action{Kind: Task, Text: title}, ok
}

// PersonalReminder recognizes a current first-person task commitment with an
// explicit scheduling span. It uses the task's grammatical form rather than a
// vocabulary of allowed action verbs. This local path also works without a
// model; less direct phrasing can still be interpreted by the model.
// A future date on an observation, quotation, or question grants no authority.
func PersonalReminder(input string) (Action, bool) {
	q, ok := personalInput(input)
	if !ok {
		return Action{}, false
	}
	// The final date in a multi-clause statement may describe why the task
	// matters or what someone said. The model must resolve the scheduling
	// boundary before any such input can become a reminder.
	if personalSupportingClause.MatchString(q) || ConflictingSchedule(input) {
		return Action{}, false
	}
	clause, when, labelled, ok := scheduledParts(q)
	if !ok {
		return Action{}, false
	}
	title, ok := personalActionBody(clause, "")
	if !ok {
		title, ok = shorthandActionBody(clause, labelled)
		if !ok {
			return Action{}, false
		}
	}
	if strings.HasSuffix(title, " last") || strings.HasSuffix(title, " previous") {
		return Action{}, false
	}
	// An event boundary needs semantic interpretation, not just the last day
	// name. Do not save "before lunch" or "after my meeting" as task content.
	for _, marker := range []string{" before ", " after ", " by ", " around ", " sometime", " that ", " which "} {
		if strings.Contains(title, marker) {
			return Action{}, false
		}
	}
	originalTitle, titleOK := canonicalSpan(input, title)
	originalWhen, whenOK := canonicalSpan(input, when)
	if !titleOK || !whenOK {
		return Action{}, false
	}
	return Action{Kind: Reminder, Text: originalTitle, When: originalWhen}, true
}

func scheduledParts(q string) (clause, when string, labelled, ok bool) {
	if match := personalScheduleFirst.FindStringSubmatch(q); match != nil {
		return match[2], match[1], false, true
	}
	if match := shorthandScheduleFirst.FindStringSubmatch(q); match != nil {
		when, clause = match[1], match[2]
		labelled = strings.Contains(q[len(when):len(q)-len(clause)], ":")
		return clause, when, labelled, true
	}
	if match := personalScheduleLast.FindStringSubmatch(q); match != nil {
		return match[1], match[2], false, true
	}
	return "", "", false, false
}

// Semantic authority checks the selected model's exact task/date split rather
// than requiring its verb to occur in the local acceleration vocabulary. Hard
// non-authority patterns remain excluded. This validates a classification; it
// does not infer an arbitrary unfamiliar phrase locally or supply any timing.
func semanticScheduledAuthority(input string, action Action) bool {
	q, ok := personalInput(input)
	if !ok || personalSupportingClause.MatchString(q) || ConflictingSchedule(input) {
		return false
	}
	clause, when, _, ok := scheduledParts(q)
	if !ok || unprefixedSchedule(action.When) != unprefixedSchedule(when) {
		return false
	}
	body := requestBody(clause)
	if body == "" || Question(body) || strings.ContainsAny(body, ":;") || strings.HasSuffix(body, " last") || strings.HasSuffix(body, " previous") || personalNonAction.MatchString(body) {
		return false
	}
	if !shorthandImperative.MatchString(body) && (shorthandStatement.MatchString(body) || finiteSubjectPredicate(body)) {
		return false
	}
	return normalized(action.Text) == body
}

func unprefixedSchedule(value string) string {
	value = normalized(value)
	return strings.TrimPrefix(strings.TrimPrefix(value, "on "), "for ")
}

// TaskMutationTiming distinguishes a request to act later from permission to
// mutate a saved task immediately. Deferred completion/cancellation is not a
// supported scheduled operation; callers must clarify instead of executing now.
func TaskMutationTiming(input string) bool {
	q := normalized(input)
	if _, _, _, scheduled := scheduledParts(q); scheduled {
		return true
	}
	return hasWord(q, "before") || hasWord(q, "after") || hasWord(q, "later")
}

// A calendar-style entry and an imperative have direct task intent without
// requiring "I need to". The small imperative vocabulary accelerates common
// requests; unfamiliar phrasing still belongs to the selected model. A single
// noun is also a useful calendar label (including misspelled names). Longer
// noun labels require the explicit calendar-header colon.
func shorthandActionBody(clause string, labelled bool) (string, bool) {
	q := requestBody(clause)
	if q == "" || Question(q) || strings.ContainsAny(q, ":;") || personalNonAction.MatchString(q) {
		return "", false
	}
	if shorthandImperative.MatchString(q) {
		return q, true
	}
	if shorthandStatement.MatchString(q) || finiteSubjectPredicate(q) || len(words(q)) == 0 || len(words(q)) != 1 && (!labelled || !calendarNounHead.MatchString(q)) {
		return "", false
	}
	// A clock or numeric expression isn't a task title. Keep such input on
	// the ordinary read/clarification path rather than scheduling a number.
	for _, r := range q {
		if r >= 'a' && r <= 'z' {
			return q, true
		}
	}
	return "", false
}

// Recognize a subject followed by a familiar third-person predicate, rather
// than treating a named subject's action as the user's request. Inflecting the
// existing action vocabulary also covers new objects without enumerating full
// utterances. Ambiguous unfamiliar morphology is still model-dependent.
func finiteSubjectPredicate(q string) bool {
	ws := words(q)
	if len(ws) < 2 {
		return false
	}
	predicate := ws[1]
	for _, suffix := range []string{"s", "es", "ies"} {
		if !strings.HasSuffix(predicate, suffix) {
			continue
		}
		root := strings.TrimSuffix(predicate, suffix)
		if suffix == "ies" {
			root += "y"
		}
		if shorthandImperative.MatchString(root) {
			return true
		}
	}
	return false
}

func personalInput(input string) (string, bool) {
	if !clean(input, MaxInputBytes) || Question(input) || strings.ContainsAny(input, "\n\r\"`“”") {
		return "", false
	}
	q := normalized(strings.TrimRight(strings.TrimSpace(input), ".!"))
	q = strings.NewReplacer("i'm ", "i am ", "i’m ", "i am ", "i'll ", "i will ", "i’ll ", "i will ", "i've ", "i have ", "i’ve ", "i have ", "i'd ", "i would ", "i’d ", "i would ").Replace(q)
	if Negative(q) || hasWord(q, "not") || hasWord(q, "never") || hasWord(q, "if") || hasWord(q, "unless") {
		return "", false
	}
	for _, tentative := range []string{"maybe", "perhaps", "probably", "possibly"} {
		if hasWord(q, tentative) {
			return "", false
		}
	}
	return q, true
}

func personalActionBody(clause, when string) (string, bool) {
	match := personalTaskPrefix.FindStringSubmatch(clause)
	if match == nil {
		return "", false
	}
	title := strings.TrimSpace(match[1])
	if when != "" {
		if strings.HasPrefix(title, when) {
			title = strings.TrimSpace(strings.TrimPrefix(title[len(when):], ","))
		} else if strings.HasSuffix(title, " "+when) {
			title = strings.TrimSpace(title[:len(title)-len(when)])
		}
	}
	action := strings.TrimPrefix(title, "remember to ")
	state := strings.HasPrefix(action, "be ") && !strings.HasPrefix(action, "be at ")
	if title == "" || personalNonAction.MatchString(action) || state || strings.HasPrefix(clause, "i will be ") || strings.HasSuffix(title, " last") || strings.HasSuffix(title, " previous") {
		return "", false
	}
	// A date attached to reported or past activity is not a future schedule.
	for _, marker := range []string{" i said ", " i wrote ", " i read ", " i saw ", " i did ", " yesterday", " ago"} {
		if strings.Contains(title, marker) {
			return "", false
		}
	}
	return title, true
}

func reminderAuthority(input string, action Action) bool {
	if ConflictingSchedule(input) {
		return false
	}
	if authority(input, Reminder) {
		return true
	}
	if local, ok := PersonalReminder(input); ok && normalized(action.When) == normalized(local.When) && normalized(action.Text) == normalized(local.Text) {
		return true
	}
	if semanticScheduledAuthority(input, action) {
		return true
	}
	q, ok := personalInput(input)
	when := normalized(action.When)
	if !ok || when == "" || !schedulingSpan.MatchString(when) {
		return false
	}
	// Grounding a date is not enough when it belongs to supporting speech or
	// a reason. Only a schedule before that clause can authorize the action.
	raw := normalized(input)
	if boundary := personalSupportingClause.FindStringIndex(raw); boundary != nil {
		at := strings.Index(raw, when)
		if at < 0 || at+len(when) > boundary[0] || strings.Contains(raw[boundary[0]:], when) {
			return false
		}
	}
	if strings.HasPrefix(q, when) {
		q = strings.TrimSpace(strings.TrimPrefix(q[len(when):], ","))
	}
	_, ok = personalActionBody(q, when)
	return ok && verbatim(q, action.Text) && normalized(action.Text) != when
}
