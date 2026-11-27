package client

import (
	"context"
	"errors"
	"strings"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/tasks"
)

func entryProgressCopy(input string) string {
	if read, ok := intent.TaskListRequest(input); ok && read.Generic {
		return "Loading your tasks…"
	}
	return "Understanding your request…"
}

func (d *desktop) cancelEntryInterpretation() {
	if d == nil {
		return
	}
	d.quickEntryRequest++
	if d.quickEntryCancel != nil {
		d.quickEntryCancel()
		d.quickEntryCancel = nil
	}
	if d.quickEntryLoading {
		d.loading = false
	}
	d.quickEntryLoading = false
}

func (d *desktop) submitQuickPanelInput() {
	if d == nil || strings.TrimSpace(d.quickInput) == "" {
		return
	}
	if d.client == nil {
		d.dispatchLegacyQuickPanelInput()
		return
	}
	if d.loading || d.askLoading {
		return
	}
	d.cancelVoiceRecapForNavigation()
	if submission := d.quickTaskSubmission; submission != nil && submission.Input == d.quickInput && submission.DraftRevision == d.quickUI.draftRevision {
		kind := quickInputTask
		if submission.Reminder != nil {
			kind = quickInputReminder
		}
		d.submitQuickTask(kind, submission.Title)
		return
	}
	d.cancelEntryInterpretation()
	request := d.quickEntryRequest
	input, state := d.quickInput, d.quickEntry
	draftRevision := d.quickUI.draftRevision
	if len(d.quickConversation) > 0 && state.Pending == nil {
		last := d.quickConversation[len(d.quickConversation)-1]
		state.HasAnswer, state.AnswerGeneral = !last.action, last.general
	}
	zone := reminderZoneName()
	ctx, cancel := context.WithCancel(context.Background())
	d.quickEntryCancel = cancel
	d.loading, d.quickEntryLoading, d.status = true, true, entryProgressCopy(input)
	d.resizeQuickPanel(false)
	d.app.RequestUpdate()
	go func() {
		resolved, err := d.client.ResolveEntry(ctx, input, state, zone)
		cancel()
		d.app.Post(func() {
			if request != d.quickEntryRequest {
				return
			}
			d.quickEntryCancel = nil
			d.quickEntryLoading, d.loading = false, false
			if err != nil {
				d.status = entryInterpretationErrorMessage(err, effectiveProvider(d.client.LocalAISettings()))
				d.resizeQuickPanel(false)
				d.app.RequestUpdate()
				return
			}
			if !d.client.MemoryReadCurrent(resolved.Epoch) {
				d.status = "Memory changed. Review your request before trying again."
				d.app.RequestUpdate()
				return
			}
			d.dispatchEntryResolution(input, resolved, zone, draftRevision)
		})
	}()
}

func entryInterpretationErrorMessage(err error, provider string) string {
	message := ""
	switch {
	case errors.Is(err, tasks.ErrConflict):
		message = "That task changed. Ask for the task list again before editing it."
	case errors.Is(err, intent.ErrInvalid):
		message = "I couldn’t interpret this request. Rephrase it as a question, note, task, or reminder."
	case errors.Is(err, context.Canceled):
		message = "This request was canceled."
	default:
		message = askErrorMessage(err, provider)
	}
	return message + " Your text is still here."
}

func (d *desktop) dispatchEntryResolution(input string, resolved EntryResolution, zone string, submittedRevision ...uint64) {
	a := resolved.Action
	draftRevision := d.quickUI.draftRevision
	if len(submittedRevision) > 0 {
		draftRevision = submittedRevision[0]
	}
	if resolved.ClearPending && a.Kind != intent.TaskInspect {
		d.quickEntry = EntryState{Epoch: resolved.Epoch}
	}
	if resolved.Draft != nil && resolved.Draft.Missing != "" {
		d.quickEntry.Pending, d.quickEntry.PendingTarget, d.quickEntry.Epoch = resolved.Draft, resolved.Target, resolved.Epoch
		d.status = reminderMissingCopy(resolved.Draft.Missing)
		d.resizeQuickPanel(false)
		d.app.RequestUpdate()
		return
	}
	switch a.Kind {
	case intent.Clarify:
		d.status = resolved.Message
	case intent.Ask:
		d.quickAskMode = a.Mode
		d.quickAskPending = true
		d.askQuestion = a.Text
		d.quickHistory.Record(a.Text)
		d.askMemoryWithDraftRevision(draftRevision)
		if d.askLoading {
			if d.quickUI.draftRevision == draftRevision && d.quickInput == input {
				d.quickInput = ""
			}
		} else {
			d.quickAskPending = false
		}
	case intent.Note:
		d.note = a.Text
		d.quickNotePending = true
		d.addNoteWithDraftRevision(draftRevision)
	case intent.Fact:
		d.submitQuickProfileWithDraft(quickInputFact, a.Text, input, draftRevision)
	case intent.Rule:
		d.submitQuickProfileWithDraft(quickInputRule, a.Text, input, draftRevision)
	case intent.Task:
		d.submitQuickTaskDraftWithDraft(quickInputTask, a.Text, nil, input, draftRevision)
	case intent.Reminder:
		d.submitQuickTaskDraftWithDraft(quickInputReminder, a.Text, resolved.Draft, input, draftRevision)
	case intent.TaskInspect:
		if resolved.Answer != nil {
			publish := func() {
				d.clearRecapSources()
				d.askQuestion, d.askResult = input, resolved.Answer
				d.quickConversation = appendQuickConversation(d.quickConversation, input, *resolved.Answer)
				d.quickEntry = EntryState{Epoch: resolved.Epoch, Targets: resolved.Answer.Targets}
				d.quickHistory.Record(input)
				if d.quickUI.draftRevision == draftRevision && d.quickInput == input {
					d.quickInput = ""
				}
				d.status = ""
				d.showQuickCurrentAnswer()
				d.expandedSourceID, d.expandedAnswer = "", false
			}
			if d.client == nil {
				publish()
			} else if !d.client.PublishMemoryRead(resolved.Epoch, publish) {
				d.status = "Memory changed. Review your request before trying again."
			}
		}
	case intent.TaskEdit, intent.TaskComplete, intent.TaskCancel:
		d.applyQuickTaskEntry(input, resolved, zone, draftRevision)
	}
	d.resizeQuickPanel(false)
	d.app.RequestUpdate()
}

func (d *desktop) applyQuickTaskEntry(input string, resolved EntryResolution, zone string, submittedRevision ...uint64) {
	request := d.quickEntryRequest
	draftRevision := d.quickUI.draftRevision
	if len(submittedRevision) > 0 {
		draftRevision = submittedRevision[0]
	}
	d.loading, d.status = true, "Updating task…"
	d.app.RequestUpdate()
	go func() {
		applied, err := d.client.ApplyTaskEntry(context.Background(), resolved, zone)
		d.app.Post(func() {
			if request != d.quickEntryRequest {
				return
			}
			if !d.client.PublishMemoryRead(resolved.Epoch, func() {
				d.loading = false
				if err != nil {
					d.status = "That task could not be changed. Your text is still here."
					if d.quickUI.draftRevision != draftRevision {
						d.status = "The earlier task could not be changed."
					}
					return
				}
				if applied.Draft != nil {
					d.quickEntry.Pending, d.quickEntry.PendingTarget, d.quickEntry.Epoch = applied.Draft, resolved.Target, resolved.Epoch
					d.status = reminderMissingCopy(applied.Draft.Missing)
					return
				}
				item := applied.Result.Item
				d.quickEntry = EntryState{Epoch: resolved.Epoch, Targets: []assistant.TargetRef{{Kind: "task", ID: item.ID, Revision: item.Revision, Label: "T1"}}}
				if d.quickUI.draftRevision == draftRevision && d.quickInput == input {
					d.quickInput = ""
				}
				d.status = taskResultCopy(applied.Result) + " " + item.Title
				d.quickConversation = appendQuickAction(d.quickConversation, input, d.status)
				if d.quickUI.draftRevision != draftRevision {
					d.status = ""
				}
				d.showQuickCurrentAnswer()
				d.tasksLoaded = false
			}) {
				d.loading = false
			}
			d.resizeQuickPanel(false)
			d.app.RequestUpdate()
		})
	}()
}
