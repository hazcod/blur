package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/tasks"
)

const maxTaskQueryScan = 4096

type taskQuery struct {
	ID                         string // one app-bound task; bypasses list scanning
	State                      tasks.State
	RemindersOnly, Next, Count bool
	Overdue, Upcoming          bool
	HistoryState               tasks.State
	Start, End                 time.Time
	Term                       string
}

func parseTaskQuery(question string, now time.Time) (taskQuery, bool) {
	read, ok := intent.TaskListRequest(question)
	if !ok {
		return taskQuery{}, false
	}
	if read.Conflicting {
		return taskQuery{}, false
	}
	query := taskQuery{State: tasks.State(read.State), RemindersOnly: read.RemindersOnly, Count: read.Count, Next: read.Next, Overdue: read.Overdue, Upcoming: read.Upcoming}
	if read.State == "all" {
		query.State = ""
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch read.Window {
	case "today":
		query.Start, query.End = day, day.AddDate(0, 0, 1)
	case "tomorrow":
		query.Start, query.End = day.AddDate(0, 0, 1), day.AddDate(0, 0, 2)
	case "yesterday":
		query.Start, query.End = day.AddDate(0, 0, -1), day
	case "this week", "next week", "last week":
		delta := (int(day.Weekday()) + 6) % 7
		query.Start = day.AddDate(0, 0, -delta)
		switch read.Window {
		case "next week":
			query.Start = query.Start.AddDate(0, 0, 7)
		case "last week":
			query.Start = query.Start.AddDate(0, 0, -7)
		}
		query.End = query.Start.AddDate(0, 0, 7)
	}
	if !query.Start.IsZero() && (query.State == tasks.Completed || query.State == tasks.Cancelled) {
		query.HistoryState, query.State = query.State, ""
	}
	return query, true
}

type taskQueryMatch struct {
	item tasks.Item
	at   time.Time
}

func taskDue(item tasks.Item, now time.Time) time.Time {
	if item.Reminder == nil {
		return time.Time{}
	}
	if item.Delivery.NextAt.After(now) {
		return item.Delivery.NextAt
	}
	if item.Reminder.Repeat != tasks.Once {
		due, err := tasks.NextOccurrence(*item.Reminder, now)
		if err == nil {
			return due
		}
	}
	return item.Reminder.DueAt
}

// matchTaskQuery applies record-owned constraints before title relevance. A
// completion or cancellation period refers to transition history; a reminder period refers to
// its due time, never the date that its title happened to be edited.
func matchTaskQuery(item tasks.Item, query taskQuery, now time.Time) (at time.Time, matched, incomplete bool) {
	if query.RemindersOnly && item.Reminder == nil || query.State != "" && item.State != query.State || query.Term != "" && !strings.Contains(strings.ToLower(item.Title), strings.ToLower(query.Term)) {
		return time.Time{}, false, false
	}
	at = item.UpdatedAt
	if query.HistoryState != "" {
		incomplete = len(item.History) == 0 || item.HistoryOmitted > 0 && item.History[0].At.After(query.Start)
		for _, event := range item.History {
			if event.State == query.HistoryState && !event.At.Before(query.Start) && (query.End.IsZero() || event.At.Before(query.End)) {
				at, matched = event.At, true
			}
		}
		if !matched {
			return at, false, incomplete
		}
	} else if item.Reminder != nil && (!query.Start.IsZero() || !query.End.IsZero()) {
		at = taskDue(item, now)
		if !query.Start.IsZero() && at.Before(query.Start) || !query.End.IsZero() && !at.Before(query.End) {
			return at, false, incomplete
		}
	}
	if query.Next {
		at = taskDue(item, now)
		if at.IsZero() || !at.After(now) {
			return at, false, incomplete
		}
	}
	if query.Overdue || query.Upcoming {
		at = taskDue(item, now)
		if at.IsZero() || query.Overdue && !at.Before(now) || query.Upcoming && !at.After(now) {
			return at, false, incomplete
		}
	}
	return at, true, incomplete
}

func (c *Client) readTaskQuery(ctx context.Context, query taskQuery) (assistant.DirectAnswer, error) {
	repository, ok := c.taskRepository()
	if !ok {
		return assistant.DirectAnswer{}, ErrNotConfigured
	}
	now := c.askClock()
	var matches []taskQueryMatch
	after, scanned := "", 0
	incomplete := false
	for {
		var page tasks.Page
		var err error
		if query.ID != "" {
			var item tasks.Item
			var found bool
			item, found, err = repository.GetTask(ctx, query.ID)
			if err == nil && !found {
				return assistant.DirectAnswer{}, ErrInvalidRequest
			}
			page.Items = []tasks.Item{item}
		} else {
			page, err = repository.ListTasks(ctx, tasks.PageRequest{After: after, Limit: tasks.MaxPageSize})
		}
		if err != nil {
			return assistant.DirectAnswer{}, err
		}
		scanned += len(page.Items)
		if scanned > maxTaskQueryScan {
			return assistant.DirectAnswer{}, tasks.ErrCapacity
		}
		for _, item := range page.Items {
			at, matched, partial := matchTaskQuery(item, query, now)
			incomplete = incomplete || partial
			if matched {
				matches = append(matches, taskQueryMatch{item: item, at: at})
			}
		}
		if !page.HasMore {
			break
		}
		if page.Next == "" || page.Next == after {
			return assistant.DirectAnswer{}, ErrInvalidRequest
		}
		after = page.Next
	}
	return taskQueryAnswer(query, matches, now, incomplete), nil
}

func taskQueryAnswer(query taskQuery, matches []taskQueryMatch, now time.Time, incomplete bool) assistant.DirectAnswer {
	sort.SliceStable(matches, func(i, j int) bool {
		if !matches[i].at.Equal(matches[j].at) {
			if query.Next {
				return matches[i].at.Before(matches[j].at)
			}
			return matches[i].at.After(matches[j].at)
		}
		return matches[i].item.ID < matches[j].item.ID
	})
	answer := assistant.DirectAnswer{}
	label := "task"
	if query.RemindersOnly {
		label = "reminder"
	}
	state := string(query.State)
	if query.HistoryState != "" {
		state = string(query.HistoryState) + " in that period"
	}
	if state != "" {
		label = state + " " + label
	}
	if len(matches) != 1 {
		label += "s"
	}
	lines := []string{fmt.Sprintf("%d %s.", len(matches), label)}
	if query.HistoryState != "" {
		noun := "tasks"
		if len(matches) == 1 {
			noun = "task"
		}
		lines = []string{fmt.Sprintf("%d %s %s in that period.", len(matches), noun, query.HistoryState)}
	}
	if query.Next {
		lines = []string{"Your next reminder:"}
		if len(matches) == 0 {
			lines = []string{"No upcoming reminders are saved."}
		}
	}
	limit := assistant.MaxEvidenceSources
	if query.Next {
		limit = 1
	}
	if !query.Count {
		for n, match := range matches[:min(len(matches), limit)] {
			item := match.item
			line := strconv.Itoa(n+1) + ". " + item.Title + " (" + taskDeliveryCopy(item) + ")"
			if query.HistoryState != "" {
				event := "completion"
				if query.HistoryState == tasks.Cancelled {
					event = "cancellation"
				}
				line += "; " + event + " recorded " + match.at.In(now.Location()).Format("2 Jan 2006, 15:04 -07:00") + "; current title shown"
			}
			if item.Reminder != nil {
				r := *item.Reminder
				if r.Repeat == tasks.Once && !item.Delivery.NextAt.IsZero() {
					r.DueAt = item.Delivery.NextAt
				}
				line += "; " + reminderScheduleCopy(r)
			}
			lines = append(lines, line)
			evidence, target := taskEvidence(item, match.at, query.HistoryState)
			target.Label = "T" + strconv.Itoa(n+1)
			answer.Sources = append(answer.Sources, evidence)
			answer.SourceIDs = append(answer.SourceIDs, evidence.SourceID)
			answer.Targets = append(answer.Targets, target)
		}
		if !query.Next && len(matches) > limit {
			lines = append(lines, fmt.Sprintf("%d more are available in Tasks.", len(matches)-limit))
		}
	}
	if incomplete {
		lines = append(lines, "Task history is incomplete: older transitions or legacy completion dates are unavailable.")
	}
	answer.Text = strings.Join(lines, "\n")
	return answer
}

func (c *Client) directTasks(ctx context.Context, plan assistant.Plan) (assistant.DirectAnswer, bool, error) {
	query, ok := resolvedTaskQuery(plan, c.askClock())
	if !ok {
		return assistant.DirectAnswer{}, false, nil
	}
	if _, ok := c.taskRepository(); !ok {
		return assistant.DirectAnswer{}, false, nil
	}
	terms := taskInspectionTerms(plan)
	if len(terms) > 0 {
		page, err := c.searchTaskCandidates(ctx, plan.Question, terms, semantic.Query{AppIDs: plan.AppIDs, Domains: plan.Domains}, maxTaskQueryScan, false, &query, false, nil)
		if err != nil {
			return assistant.DirectAnswer{}, true, err
		}
		matches := make([]taskQueryMatch, len(page.matches))
		for i, match := range page.matches {
			matches[i] = taskQueryMatch{item: match.item, at: match.at}
		}
		answer := taskQueryAnswer(query, matches, c.askClock(), page.historyIncomplete)
		if page.hasMore || page.semanticIssue != "" {
			if query.Next {
				if len(matches) == 0 {
					answer.Text = "No upcoming reminder matches were found in the searched tasks."
				} else {
					answer.Text = strings.Replace(answer.Text, "Your next reminder:", "Earliest matching reminder found:", 1)
				}
			} else {
				answer.Text = "At least " + answer.Text
			}
			if page.hasMore {
				answer.Text += "\nTask search reached its limit; more matching tasks may exist."
			}
			if page.semanticIssue != "" {
				answer.Text += "\nSearch by meaning is unavailable; only exact title matches are included."
			}
		}
		return answer, true, nil
	}
	answer, err := c.readTaskQuery(ctx, query)
	return answer, true, err
}

func taskInspectionTerms(plan assistant.Plan) []string {
	if read, ok := intent.TaskListRequest(plan.Question); ok && read.Generic {
		return nil
	}
	terms := plan.Terms
	if len(terms) == 0 {
		// Imperative inspections such as "Show all laundry tasks" can be
		// classified as general. Retain their topic instead of listing all tasks.
		terms, _ = search.NormalizeTokens(plan.Question, assistant.MaxQuestionBytes)
	}
	var topic []string
	for _, term := range taskTopicTerms(terms) {
		switch term {
		case "show", "display", "view", "see", "pull", "up", "bring", "read", "check", "let", "me", "checklist", "checklists", "action", "items", "item", "personal", "what", "which", "when", "is", "are", "on", "in", "my", "our", "the", "a", "an", "any", "how", "many", "do", "did", "i", "have", "to", "of", "please", "open", "due", "scheduled", "today", "tomorrow", "yesterday", "this", "last", "week", "s":
			continue
		}
		topic = append(topic, term)
	}
	return topic
}

// liveTasks reads a bounded current task collection. Callers select relevant
// records before producing evidence; no copied note participates in recall.
func (c *Client) liveTasks(ctx context.Context) ([]tasks.Item, error) {
	repository, ok := c.taskRepository()
	if !ok {
		return nil, nil
	}
	var items []tasks.Item
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := repository.ListTasks(ctx, tasks.PageRequest{After: after, Limit: tasks.MaxPageSize})
		if err != nil {
			return nil, err
		}
		if len(items)+len(page.Items) > maxTaskQueryScan {
			return nil, tasks.ErrCapacity
		}
		items = append(items, page.Items...)
		if !page.HasMore {
			break
		}
		if page.Next == "" || page.Next == after {
			return nil, ErrInvalidRequest
		}
		after = page.Next
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

// taskEvidence binds an inspectable citation to the current record, revision,
// and native scheduling receipt without saving a second copy of the task.
func taskEvidence(item tasks.Item, at time.Time, historyState tasks.State) (assistant.Evidence, assistant.TargetRef) {
	text := "Saved task: " + item.Title + "\nCurrent state: " + string(item.State) + "\n" + taskDeliveryCopy(item) + "\nSaved: " + item.CreatedAt.Format(time.RFC3339) + "\nUpdated: " + item.UpdatedAt.Format(time.RFC3339)
	matchEnd := 0
	if historyState != "" {
		summary := "Task state event: " + string(historyState) + " at " + at.Format(time.RFC3339) + ". Current task title: " + item.Title + ". Current state: " + string(item.State) + ".\n"
		text = summary + text
		matchEnd = len(summary)
	}
	if item.Reminder != nil {
		text += "\nRequested schedule: " + reminderEvidenceTimeCopy(*item.Reminder)
		if !item.Delivery.NextAt.IsZero() {
			next := *item.Reminder
			next.Repeat = tasks.Once
			next.DueAt = item.Delivery.NextAt
			label := "Unconfirmed scheduling target: "
			if item.Delivery.State == tasks.Scheduled {
				label = "Observed next queued trigger: "
			}
			text += "\n" + label + reminderEvidenceTimeCopy(next)
		}
	}
	if matchEnd == 0 {
		matchEnd = len(text)
	}
	for _, event := range item.History {
		text += "\n" + string(event.State) + ": " + event.At.Format(time.RFC3339)
	}
	if item.HistoryOmitted > 0 {
		text += "\nOlder state changes omitted: " + strconv.FormatUint(item.HistoryOmitted, 10)
	}
	sum := sha256.Sum256([]byte("blur-task-v1:" + item.ID + ":" + strconv.FormatUint(item.Revision, 10) + ":" + text))
	id := hex.EncodeToString(sum[:])
	return assistant.Evidence{SourceID: id, AppID: "blur.tasks", AppName: "Saved task", Text: text, Origin: assistant.OriginTask, CapturedAt: at, MatchEnd: matchEnd}, assistant.TargetRef{Kind: "task", ID: item.ID, Revision: item.Revision, SourceID: id}
}

// Structural task vocabulary is removed only for title matching. The original
// full question goes to the semantic provider; no synonym dictionary is used.
func taskTopicTerms(terms []string) []string {
	var result []string
	for _, term := range terms {
		switch term {
		case "task", "tasks", "todo", "todos", "to-do", "list", "reminder", "reminders", "next", "completed", "complete", "finished", "cancelled", "canceled", "all", "what's", "whats", "outstanding", "remain", "remains", "remaining", "open", "left", "unresolved", "pending", "still", "needs", "doing", "anything":
			continue
		}
		result = append(result, term)
	}
	return result
}

func taskContextQuestion(plan assistant.Plan) bool {
	if _, ok := parseTaskQuery(plan.Question, time.Now()); ok {
		return len(taskTopicTerms(plan.Terms)) == 0
	}
	q := strings.Trim(strings.ToLower(strings.Join(strings.Fields(plan.Question), " ")), "?!. ")
	switch q {
	case "what should i work on next", "what should i do next", "what do i need to do", "where did i leave off", "where did we leave off", "where was i", "what was i working on":
		return true
	}
	return false
}

func resolvedTaskQuery(plan assistant.Plan, now time.Time) (taskQuery, bool) {
	query, ok := parseTaskQuery(plan.Question, now)
	if !ok {
		return query, false
	}
	// Calendar periods include reminders due later today or this week; more
	// precise capture-hour scopes still override the task calendar period.
	calendarWindow := !query.Start.IsZero() && (plan.Scope.Label == "today" || plan.Scope.Label == "yesterday" || plan.Scope.Label == "this week")
	if !plan.Scope.IsZero() && !calendarWindow {
		query.Start, query.End = plan.Scope.Start, plan.Scope.End
	}
	if !query.Start.IsZero() && (query.State == tasks.Completed || query.State == tasks.Cancelled) {
		query.HistoryState, query.State = query.State, ""
	}
	return query, true
}
