package client

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/tasks"
)

// EntryState is transient UI-owned action context. It is cleared on memory
// invalidation and never contains captured text or model-created identifiers.
type EntryState struct {
	Epoch         uint64
	Targets       []assistant.TargetRef
	Pending       *reminders.Draft
	PendingTarget *assistant.TargetRef
	HasAnswer     bool
	AnswerGeneral bool
}
type EntryResolution struct {
	SearchQuery  string
	Action       intent.Action
	Epoch        uint64
	Draft        *reminders.Draft
	Target       *assistant.TargetRef
	Message      string
	ClearPending bool
	Answer       *assistant.Result
}

func (c *Client) entryTargets(ctx context.Context, refs []assistant.TargetRef) ([]intent.Target, []assistant.TargetRef, error) {
	if len(refs) > 8 {
		return nil, nil, ErrInvalidRequest
	}
	modelTargets := []intent.Target{}
	bound := []assistant.TargetRef{}
	for _, ref := range refs {
		if ref.Kind != "task" {
			continue
		}
		item, err := c.Task(ctx, ref.ID)
		if err != nil {
			return nil, nil, err
		}
		// Reading a current title does not renew the user's authority to edit
		// a snapshot they previously saw. Keep the original revision bound to
		// mutations; ApplyTaskEntry checks it immediately before writing.
		ref.Label = "T" + string(rune('1'+len(bound)))
		modelTargets = append(modelTargets, intent.Target{Ref: ref.Label, Title: item.Title})
		bound = append(bound, ref)
	}
	return modelTargets, bound, nil
}
func entryCommand(input string) (kind intent.Kind, body string) {
	q := strings.TrimSpace(input)
	for _, prefix := range []string{"please ", "can you ", "could you ", "would you ", "actually, "} {
		if strings.HasPrefix(strings.ToLower(q), prefix) {
			q = strings.TrimSpace(q[len(prefix):])
		}
	}
	lower := strings.ToLower(q)
	for _, pair := range []struct {
		prefix string
		kind   intent.Kind
	}{{"cancel ", intent.TaskCancel}, {"stop ", intent.TaskCancel}, {"complete ", intent.TaskComplete}, {"finish ", intent.TaskComplete}, {"mark ", intent.TaskComplete}, {"move ", intent.TaskEdit}, {"reschedule ", intent.TaskEdit}, {"postpone ", intent.TaskEdit}, {"rename ", intent.TaskEdit}} {
		if strings.HasPrefix(lower, pair.prefix) {
			return pair.kind, q
		}
	}
	if lower == "done" {
		return intent.TaskComplete, q
	}
	return "", ""
}

func entryTaskReference(input string) bool {
	q := strings.ToLower(input)
	for _, word := range strings.FieldsFunc(q, func(r rune) bool { return r < 'a' || r > 'z' }) {
		switch word {
		case "it", "that", "this", "task", "tasks", "reminder", "reminders":
			return true
		}
	}
	for _, prefix := range []string{"reschedule ", "postpone "} {
		if strings.HasPrefix(q, prefix) {
			return true
		}
	}
	return false
}

func (c *Client) ResolveEntry(parent context.Context, input string, state EntryState, zone string) (EntryResolution, error) {
	return c.resolveEntry(parent, input, state, zone, false)
}

// Dashboard topic phrases are read-only searches; explicit commands and answer
// continuations share the popup's authority, scheduling, and local fast paths.
func (c *Client) resolveMemoryEntry(parent context.Context, input string, state EntryState, zone string) (EntryResolution, error) {
	return c.resolveEntry(parent, input, state, zone, true)
}

func (c *Client) resolveEntry(parent context.Context, input string, state EntryState, zone string, browseTopics bool) (EntryResolution, error) {
	started, stage := time.Now(), "local"
	defer func() {
		slog.Debug("entry routing completed", "stage", stage, "elapsed_ms", time.Since(started).Milliseconds())
	}()
	epoch, stable := c.BeginMemoryRead()
	if !stable || (len(state.Targets) > 0 || state.Pending != nil || state.HasAnswer) && state.Epoch != epoch {
		return EntryResolution{}, ErrMemoryChanging
	}
	result := EntryResolution{Epoch: epoch}
	rawInput := input
	input = strings.TrimSpace(input)
	if input == "" {
		return result, ErrInvalidRequest
	}
	ctx, done, err := c.beginOperation(parent, 60*time.Second)
	if err != nil {
		return result, err
	}
	defer done()
	ctx, cancel := context.WithCancel(ctx)
	id, ok := c.registerAskEpoch(cancel, &epoch)
	if !ok {
		cancel()
		return result, ErrMemoryChanging
	}
	defer func() { cancel(); c.unregisterAsk(id) }()
	if message := entryEmptyPrefixClarification(input); message != "" {
		result.Action.Kind, result.Message = intent.Clarify, message
		return result, nil
	}
	if intent.NoteSavingDenied(input) {
		result.Action.Kind = intent.Clarify
		result.Message = "I won’t save this. Would you like to discuss it without creating a note?"
		return result, nil
	}
	if action, ok := pastedNoteAction(rawInput); ok {
		result.Action = action
		if action.Kind == intent.Clarify {
			result.Message = action.Text
		}
		return result, nil
	}
	if assistant.IsCalculation(input) || entryNumericFunction.MatchString(input) {
		result.Action = intent.Action{Kind: intent.Ask, Text: input, Mode: "general"}
		return result, nil
	}
	if entryQuotedQuestion(input) {
		result.Action = intent.Action{Kind: intent.Ask, Text: input}
		return result, nil
	}
	if message := entryDataClarification(input); message != "" {
		result.Action.Kind, result.Message = intent.Clarify, message
		return result, nil
	}
	// Collection reads are independent of capture, selected AI and any pending
	// reminder draft. Main Interact and the popout share this exact local path.
	if read, ok := intent.TaskListRequest(input); ok && read.Generic {
		stage = "local_task_list"
		if read.Conflicting {
			result.Action.Kind = intent.Clarify
			result.Message = "Which task status and calendar period should I use?"
			return result, nil
		}
		if _, configured := c.taskRepository(); configured {
			query, _ := parseTaskQuery(input, c.askClock())
			answer, err := c.readTaskQuery(ctx, query)
			if err != nil {
				return result, err
			}
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if !c.MemoryReadCurrent(epoch) {
				return result, ErrMemoryChanging
			}
			result.Action = intent.Action{Kind: intent.TaskInspect}
			result.ClearPending = state.Pending != nil
			result.Answer = &assistant.Result{Answer: answer.Answer, Sources: answer.Sources, Targets: answer.Targets, Direct: true, Plan: assistant.Plan{Kind: assistant.KindLookup, Question: input}}
			return result, nil
		}
		result.Action = intent.Action{Kind: intent.Ask, Text: input, Mode: "memory"}
		return result, nil
	}
	// A source lookup has no task or scheduling authority, including while a
	// reminder draft is waiting for a title or a date.
	if browseTopics {
		if query, ok := entryFindQuery(input); ok {
			stage = "local_source_lookup"
			result.SearchQuery = query
			return result, nil
		}
	}
	// Project-scoped outstanding-work questions have a dedicated structured
	// retriever. Keep explicit source commands above this topical heuristic.
	if creation, _ := classifyQuickInput(input); creation != quickInputTask && assistant.IsOutstandingQuestion(input) {
		stage = "outstanding_work"
		result.Action = intent.Action{Kind: intent.Ask, Text: input, Mode: "memory"}
		return result, nil
	}
	if intent.ConflictingSchedule(input) {
		result.Action.Kind = intent.Clarify
		result.Message = "Which reminder time should I use? Give one date/time or one relative delay."
		return result, nil
	}
	if state.Pending != nil {
		q := strings.ToLower(input)
		if q == "cancel" || q == "cancel that" || q == "never mind" || q == "nevermind" {
			result.Action.Kind = intent.Clarify
			result.Message = "Reminder draft canceled."
			result.ClearPending = true
			return result, nil
		}
		kind, _ := classifyQuickInput(input)
		_, newCommitment := intent.PersonalReminder(input)
		newTaskAction, newTask := intent.PersonalTask(input)
		_, noteRequest := intent.NoteRequest(input)
		// A bare action is the expected reply when only the title is missing.
		// A new explicit first-person commitment still starts its own request.
		titleReply := state.Pending.Missing == "title" && newTask && input == newTaskAction.Text
		if kind == quickInputNote && !noteRequest && !newCommitment && (!newTask || titleReply) {
			draft, err := reminders.Continue(*state.Pending, input, c.askClock(), zone)
			result.Draft = &draft
			result.Action = intent.Action{Kind: intent.Reminder, Text: draft.Title, When: draft.When}
			result.Target = state.PendingTarget
			if result.Target != nil {
				result.Action.Kind = intent.TaskEdit
				result.Action.Target = result.Target.Label
			}
			return result, err
		}
	}
	if state.HasAnswer && assistant.IsGroundedFollowUp(input) {
		mode := "memory"
		if state.AnswerGeneral {
			mode = "general"
		}
		result.Action = intent.Action{Kind: intent.Ask, Text: input, Mode: mode}
		return result, nil
	}
	modelTargets, bound, err := c.entryTargets(ctx, state.Targets)
	if err != nil {
		return result, err
	}
	if len(bound) > 0 && intent.TaskInspection(input) {
		ref := intent.ResolveTarget(input, modelTargets)
		if ref != "" {
			for n, target := range modelTargets {
				if target.Ref == ref {
					result.Action = intent.Action{Kind: intent.TaskInspect, Target: ref}
					return c.resolveTaskInspection(ctx, result, bound[n].ID, input)
				}
			}
		} else if strings.Contains(strings.ToLower(input), " it") || strings.Contains(strings.ToLower(input), " that") || strings.Contains(strings.ToLower(input), " task") || strings.Contains(strings.ToLower(input), " reminder") {
			result.Action.Kind = intent.Clarify
			result.Message = "Which task? Name or number the item you want to inspect."
			return result, nil
		}
	}
	mutationRequest := intent.Request{Input: input, Targets: modelTargets}
	if intent.TaskMutationNeedsClarification(mutationRequest) {
		result.Action.Kind = intent.Clarify
		result.Message = "Should I change that task now, or change its reminder time?"
		return result, nil
	}
	if command, body := entryCommand(input); command != "" {
		ref := intent.ResolveTarget(input, modelTargets)
		_, freshReminder := intent.PersonalReminder(input)
		_, freshTask := intent.PersonalTask(input)
		freshAction := ref == "" && !entryTaskReference(body) && (freshReminder || freshTask)
		if ref == "" && !freshAction {
			result.Action.Kind = intent.Clarify
			result.Message = "Which task? Ask for your task list, then name or number the item."
			return result, nil
		}
		if !freshAction {
			if !intent.TaskMutationAllowed(mutationRequest, command) {
				result.Action.Kind = intent.Clarify
				result.Message = "Should I change that task now, or change its reminder time?"
				return result, nil
			}
			for n, target := range modelTargets {
				if target.Ref == ref {
					copy := bound[n]
					result.Target = &copy
				}
			}
			result.Action = intent.Action{Kind: command, Target: ref}
			if command == intent.TaskEdit {
				index := strings.Index(strings.ToLower(body), " to ")
				if index < 0 {
					result.Action.Kind = intent.Clarify
					result.Message = "What title or reminder time should I use?"
					return result, nil
				}
				value := strings.TrimSpace(body[index+4:])
				if strings.HasPrefix(strings.ToLower(body), "rename ") {
					result.Action.Text = value
				} else {
					result.Action.When = value
				}
			}
			encoded, _ := json.Marshal(result.Action)
			if _, err := intent.Decode(string(encoded), intent.Request{Input: input, Targets: modelTargets}); err != nil {
				return result, err
			}
			return result, nil
		}
	}
	if intent.RelativeDelay(input) {
		draft, err := reminders.Parse("remind me "+input, c.askClock(), zone)
		result.Action = intent.Action{Kind: intent.Reminder, When: input}
		result.Draft = &draft
		return result, err
	}
	kind, text := classifyQuickInput(input)
	explicit := false
	lower := strings.ToLower(input)
	for _, prefix := range []string{"ask:", "note:", "task:", "todo:", "reminder:", "remember:", "preference:", "rule:", "writing rule:", "remember that "} {
		if strings.HasPrefix(lower, prefix) {
			explicit = true
		}
	}
	if kind == quickInputReminder || kind == quickInputFact || kind == quickInputRule || kind == quickInputTask {
		explicit = true
	}
	if !explicit && intent.Question(input) && entryReadOnlyQuestion(input) {
		kind, text = quickInputAsk, input
	}
	if kind == quickInputAsk && assistant.IsCalculation(text) {
		result.Action = intent.Action{Kind: intent.Ask, Text: text, Mode: "general"}
		return result, nil
	}
	if !explicit {
		if action, ok := intent.PersonalReminder(input); ok {
			result.Action = action
			draft, err := reminders.Parse("remind me "+action.When+" to "+action.Text, c.askClock(), zone)
			result.Draft = &draft
			return result, err
		}
		if action, ok := intent.PersonalTask(input); ok {
			result.Action = action
			return result, nil
		}
	}
	if _, ok := parseTaskQuery(input, c.askClock()); ok && kind != quickInputTask {
		explicit = true
		kind = quickInputAsk
	}
	// Clear questions already have a read-only route. The answer planner owns
	// general-versus-memory retrieval, including ambiguous lookup questions;
	// intent inference would repeat that work before the real answer starts.
	if kind == quickInputAsk && (intent.QuestionPrefix(input) || intent.Question(input) && entryReadOnlyQuestion(input)) {
		explicit = true
		stage = "local_question"
	}
	if (browseTopics || kind == quickInputAsk && entryReadOnlyQuestion(input)) && !explicit {
		if kind == quickInputAsk && entryReadOnlyQuestion(input) && !intent.Negative(input) {
			result.Action = intent.Action{Kind: intent.Ask, Text: text}
			return result, nil
		}
		if browseTopics && kind == quickInputNote {
			result.SearchQuery = input
			return result, nil
		}
	}
	if !explicit && len(input) <= intent.MaxInputBytes {
		generator, parseCtx, finish, err := c.beginEntryParser(ctx, epoch)
		if err != nil {
			return result, err
		}
		defer finish()
		if generator != nil {
			stage = "selected_model_intent"
			action, err := generator.Parse(parseCtx, intent.Request{Input: input, Targets: modelTargets})
			if parseCtx.Err() != nil {
				return result, parseCtx.Err()
			}
			if !c.MemoryReadCurrent(epoch) {
				return result, ErrMemoryChanging
			}
			// A rejected interpretation grants no write authority. Keep the
			// original draft and offer useful choices without retrying inference.
			if err == intent.ErrInvalid || err == intent.ErrActionAuthority {
				result.Action.Kind = intent.Clarify
				result.Message = "Would you like an answer, a saved note, or a task or reminder? You can start with “ask:”, “note:”, “task:”, or “remind me”."
				return result, nil
			}
			if err != nil {
				return result, err
			}
			result.Action = action
			if action.Target != "" {
				for n, target := range modelTargets {
					if target.Ref == action.Target {
						copy := bound[n]
						result.Target = &copy
					}
				}
			}
			if action.Kind == intent.Reminder {
				draft, err := reminders.Parse("remind me "+action.When+" to "+action.Text, c.askClock(), zone)
				result.Draft = &draft
				return result, err
			}
			if action.Kind == intent.TaskInspect && result.Target != nil {
				return c.resolveTaskInspection(ctx, result, result.Target.ID, input)
			}
			if action.Kind == intent.Clarify {
				result.Message = action.Text
			}
			return result, nil
		}
	}
	switch kind {
	case quickInputAsk:
		result.Action = intent.Action{Kind: intent.Ask, Text: text}
	case quickInputFact:
		result.Action = intent.Action{Kind: intent.Fact, Text: text}
	case quickInputRule:
		result.Action = intent.Action{Kind: intent.Rule, Text: text}
	case quickInputTask:
		result.Action = intent.Action{Kind: intent.Task, Text: text}
	case quickInputReminder:
		draft, err := reminders.Parse(text, c.askClock(), zone)
		result.Action = intent.Action{Kind: intent.Reminder, Text: draft.Title, When: draft.When}
		result.Draft = &draft
		return result, err
	default:
		result.Action = intent.Action{Kind: intent.Note, Text: text}
	}
	return result, nil
}

func (c *Client) resolveTaskInspection(ctx context.Context, result EntryResolution, id, question string) (EntryResolution, error) {
	answer, err := c.readTaskQuery(ctx, taskQuery{ID: id})
	if err != nil {
		return result, err
	}
	result.Answer = &assistant.Result{Answer: answer.Answer, Sources: answer.Sources, Targets: answer.Targets, Direct: true, Plan: assistant.Plan{Kind: assistant.KindLookup, Question: question}}
	return result, nil
}

type TaskEntryResult struct {
	Result reminders.Result
	Draft  *reminders.Draft
}

func (c *Client) ApplyTaskEntry(ctx context.Context, resolved EntryResolution, zone string) (TaskEntryResult, error) {
	if resolved.Target == nil || resolved.Target.Kind != "task" || !c.MemoryReadCurrent(resolved.Epoch) {
		return TaskEntryResult{}, ErrMemoryChanging
	}
	item, err := c.Task(ctx, resolved.Target.ID)
	if err != nil {
		return TaskEntryResult{}, err
	}
	if item.Revision != resolved.Target.Revision {
		return TaskEntryResult{}, tasks.ErrConflict
	}
	switch resolved.Action.Kind {
	case intent.TaskComplete:
		item.State = tasks.Completed
	case intent.TaskCancel:
		item.State = tasks.Cancelled
	case intent.TaskEdit:
		if resolved.Action.Text != "" {
			item.Title = resolved.Action.Text
		}
		if resolved.Action.When != "" || resolved.Draft != nil {
			var draft reminders.Draft
			if resolved.Draft != nil {
				draft = *resolved.Draft
			} else {
				spec := reminders.NormalizeSchedule(resolved.Action.When)
				timeOnly := strings.HasPrefix(strings.ToLower(spec), "at ")
				if item.Reminder != nil {
					zone = item.Reminder.Timezone
					if timeOnly {
						old := taskWhenText(item)
						at := strings.LastIndex(old, " at ")
						if at >= 0 {
							spec = old[:at] + " " + spec
						}
					}
				}
				draft, err = reminders.Parse("remind me "+spec+" to "+item.Title, c.askClock(), zone)
				if err != nil {
					return TaskEntryResult{}, err
				}
				if draft.Missing == "time" && item.Reminder != nil {
					old := taskWhenText(item)
					if at := strings.LastIndex(old, " at "); at >= 0 {
						draft, err = reminders.Parse("remind me "+spec+old[at:]+" to "+item.Title, c.askClock(), zone)
					}
				}
				if item.Reminder != nil && item.Reminder.Repeat != tasks.Once && !timeOnly && draft.Reminder != nil && draft.Reminder.Repeat == tasks.Once {
					draft.Missing = "recurrence"
				}
			}
			if err != nil {
				return TaskEntryResult{}, err
			}
			if draft.Missing != "" {
				return TaskEntryResult{Draft: &draft}, nil
			}
			item.Reminder = draft.Reminder
		}
	default:
		return TaskEntryResult{}, ErrInvalidRequest
	}
	result, err := c.updateTaskWithEpoch(ctx, item, &resolved.Epoch)
	return TaskEntryResult{Result: result}, err
}
