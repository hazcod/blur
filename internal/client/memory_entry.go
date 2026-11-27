package client

import (
	"context"
	"strings"

	"github.com/golang-gui/goui/platform/events"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/profile"
	"github.com/hazcod/blur/internal/reminders"
	"github.com/hazcod/blur/internal/tasks"
)

func (d *desktop) cancelMemoryEntry() {
	u := &d.memoryUI
	u.entryRequest++
	if u.entryCancel != nil {
		u.entryCancel()
		u.entryCancel = nil
	}
	if u.entryLoading {
		d.loading = false
	}
	u.entryLoading = false
}

func (d *desktop) submitMemoryQuery() {
	if d == nil || d.client == nil || d.askLoading || d.loading {
		return
	}
	d.cancelVoiceRecapForNavigation()
	d.clearRecapSources()
	u := &d.memoryUI
	u.meetingDetail, u.answerInput = false, ""
	d.query = d.askQuestion
	d.searchHits, d.searchSummaries, d.searchComplete, d.searchFailed = nil, nil, false, false
	d.askResult = nil
	d.askSourcesExpanded = false
	if strings.TrimSpace(d.askQuestion) == "" {
		u.previous = nil
		d.loadMemoryBrowse(memoryBrowseCursor{})
		return
	}
	d.cancelMemoryEntry()
	request, input, state := u.entryRequest, d.askQuestion, u.entry
	if len(d.askConversation) > 0 && state.Pending == nil {
		last := d.askConversation[len(d.askConversation)-1]
		state.HasAnswer, state.AnswerGeneral = !last.action, last.general
	}
	zone := reminderZoneName()
	ctx, cancel := context.WithCancel(context.Background())
	u.entryCancel, u.entryLoading, d.loading, d.status = cancel, true, true, entryProgressCopy(input)
	d.app.RequestUpdate()
	go func() {
		resolved, err := d.client.resolveMemoryEntry(ctx, input, state, zone)
		cancel()
		d.app.Post(func() {
			if request != u.entryRequest {
				return
			}
			u.entryCancel, u.entryLoading, d.loading = nil, false, false
			if err != nil {
				d.status = entryInterpretationErrorMessage(err, effectiveProvider(d.client.LocalAISettings()))
			} else if !d.client.MemoryReadCurrent(resolved.Epoch) {
				d.status = "Memory changed. Review your request before trying again."
			} else {
				d.dispatchMemoryEntry(input, resolved, zone)
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) dispatchMemoryEntry(input string, resolved EntryResolution, zone string) {
	u, action := &d.memoryUI, resolved.Action
	if resolved.ClearPending {
		u.entry = EntryState{Epoch: resolved.Epoch}
	}
	if resolved.Draft != nil && resolved.Draft.Missing != "" {
		u.entry = EntryState{Epoch: resolved.Epoch, Pending: resolved.Draft, PendingTarget: resolved.Target}
		d.status = reminderMissingCopy(resolved.Draft.Missing)
		return
	}
	if resolved.SearchQuery != "" {
		u.entry = EntryState{Epoch: resolved.Epoch}
		d.askConversation = nil
		u.searchInput = resolved.SearchQuery
		d.query = resolved.SearchQuery
		d.runSearch()
		return
	}
	switch action.Kind {
	case intent.Clarify:
		d.status = resolved.Message
	case intent.Ask:
		u.askMode, u.submittedInput = action.Mode, input
		d.askQuestion = action.Text
		d.askMemory()
	case intent.TaskInspect:
		if resolved.Answer != nil {
			d.client.PublishMemoryRead(resolved.Epoch, func() {
				d.askResult = resolved.Answer
				u.answerInput = input
				d.askConversation = appendQuickConversation(d.askConversation, input, *resolved.Answer)
				u.entry = EntryState{Epoch: resolved.Epoch, Targets: resolved.Answer.Targets, HasAnswer: true, AnswerGeneral: resolved.Answer.General}
				if d.askQuestion == input {
					d.askQuestion, d.query = "", ""
				}
				d.status = ""
			})
		}
	case intent.Note:
		d.saveMemoryNote(input, action.Text, resolved.Epoch)
	case intent.Task, intent.Reminder:
		title := strings.TrimSpace(action.Text)
		var reminder *tasks.Reminder
		if resolved.Draft != nil {
			title, reminder = resolved.Draft.Title, resolved.Draft.Reminder
		}
		if !tasks.ValidTitle(title) {
			d.status = "Enter a task title of up to 512 bytes. Your text is still here."
			return
		}
		if u.taskSubmission == nil || u.taskSubmission.Input != input {
			id, err := newTaskRequestID()
			if err != nil {
				d.status = "The task could not be prepared. Your text is still here."
				return
			}
			u.taskSubmission = &taskSubmission{ID: id, Input: input, Title: title, Reminder: reminder, Epoch: resolved.Epoch}
		}
		submission := *u.taskSubmission
		d.applyMemoryEntry(input, resolved.Epoch, func(ctx context.Context) (memoryEntryReceipt, error) {
			result, err := d.client.createTaskAtEpoch(ctx, submission.Title, submission.Reminder, submission.ID, submission.Epoch)
			return memoryEntryReceipt{epoch: resolved.Epoch, status: taskResultCopy(result), targets: []assistant.TargetRef{{Kind: "task", ID: result.Item.ID, Revision: result.Item.Revision, Label: "T1"}}}, err
		})
	case intent.TaskEdit, intent.TaskComplete, intent.TaskCancel:
		d.applyMemoryEntry(input, resolved.Epoch, func(ctx context.Context) (memoryEntryReceipt, error) {
			applied, err := d.client.ApplyTaskEntry(ctx, resolved, zone)
			item := applied.Result.Item
			return memoryEntryReceipt{epoch: resolved.Epoch, status: taskResultCopy(applied.Result), targets: []assistant.TargetRef{{Kind: "task", ID: item.ID, Revision: item.Revision, Label: "T1"}}, pending: applied.Draft, target: resolved.Target}, err
		})
	case intent.Fact, intent.Rule:
		kind := profile.Fact
		if action.Kind == intent.Rule {
			kind = profile.WritingRule
		}
		d.applyMemoryEntry(input, resolved.Epoch, func(ctx context.Context) (memoryEntryReceipt, error) {
			result, err := d.client.createProfileAtEpoch(ctx, kind, action.Text, profile.Scope{}, resolved.Epoch)
			return memoryEntryReceipt{epoch: result.Epoch, status: "Remembered. Review it in Preferences."}, err
		})
	}
}

type memoryEntryReceipt struct {
	epoch            uint64
	status, sourceID string
	targets          []assistant.TargetRef
	pending          *reminders.Draft
	target           *assistant.TargetRef
}

func (d *desktop) applyMemoryEntry(input string, epoch uint64, apply func(context.Context) (memoryEntryReceipt, error)) {
	u := &d.memoryUI
	request := u.entryRequest
	u.entryLoading = true
	d.loading, d.status = true, "Saving locally…"
	d.app.RequestUpdate()
	go func() {
		receipt, err := apply(context.Background())
		if err != nil {
			receipt.epoch = epoch
		}
		d.app.Post(func() {
			if request != u.entryRequest && receipt.epoch == epoch {
				return
			}
			u.entryLoading = false
			if !d.client.PublishMemoryRead(receipt.epoch, func() {
				d.loading = false
				if err != nil {
					d.status = friendlyError(err) + " Your text is still here."
					return
				}
				if receipt.pending != nil {
					u.entry = EntryState{Epoch: receipt.epoch, Pending: receipt.pending, PendingTarget: receipt.target}
					d.status = reminderMissingCopy(receipt.pending.Missing)
					return
				}
				u.entry, u.taskSubmission = EntryState{Epoch: receipt.epoch, Targets: receipt.targets}, nil
				if d.askQuestion == input {
					d.askQuestion, d.query = "", ""
				}
				d.askConversation = appendQuickAction(d.askConversation, input, receipt.status)
				d.status = receipt.status
				d.tasksLoaded = false
			}) {
				d.loading = false
				d.app.RequestUpdate()
				return
			}
			if err == nil && receipt.sourceID != "" {
				d.revealSavedMemorySource(receipt.sourceID)
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) saveMemoryNote(input, text string, epoch uint64) {
	d.applyMemoryEntry(input, epoch, func(ctx context.Context) (memoryEntryReceipt, error) {
		result, err := d.client.addNoteAtEpoch(ctx, text, epoch)
		return memoryEntryReceipt{epoch: epoch, status: "Saved to memory.", sourceID: result.SourceID}, err
	})
}

func (d *desktop) memoryCopyText() string {
	if d == nil || d.page != "Ask" || d.noteUI.open || d.loading || d.askLoading || strings.TrimSpace(d.askQuestion) != "" || d.askResult == nil {
		return ""
	}
	return d.askResult.Answer.Text
}

func (d *desktop) copyMemoryAnswer() {
	text := d.memoryCopyText()
	if text == "" {
		return
	}
	copyText := d.memoryUI.copyText
	if copyText == nil {
		copyText = copyToPasteboard
	}
	if err := copyText(text); err != nil {
		d.status = "The answer could not be copied."
	} else {
		d.status = "Copied answer."
	}
	d.app.RequestUpdate()
}

func (d *desktop) memoryNoteSaveText() string {
	if d == nil || d.page != "Ask" || d.noteUI.open || d.loading || d.askLoading {
		return ""
	}
	if strings.TrimSpace(d.askQuestion) != "" {
		return quickPanelNoteText(d.askQuestion)
	}
	if d.askResult != nil {
		return strings.TrimSpace(d.memoryUI.answerInput + "\n\n" + d.askResult.Answer.Text)
	}
	return ""
}

func (d *desktop) saveMemorySelectionAsNote() {
	text := d.memoryNoteSaveText()
	if text == "" || d.client == nil {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.saveMemoryNote(d.askQuestion, text, epoch)
}

func (d *desktop) handleMemoryKey(event events.KeyEvent) bool {
	if d != nil && d.noteUI.open {
		return d.handleNoteDocumentKey(event)
	}
	if d == nil || d.page != "Ask" || event.Modifiers != events.ModifierCommand {
		return false
	}
	if event.Key == events.KeyN {
		d.newNoteDocument()
		return true
	}
	if event.Key == events.KeyC && d.memoryCopyText() != "" {
		d.copyMemoryAnswer()
		return true
	}
	if event.Key == events.KeyS && d.memoryNoteSaveText() != "" {
		d.saveMemorySelectionAsNote()
		return true
	}
	return false
}

func (d *desktop) memoryShortcuts() []*ui.ShortcutView {
	return []*ui.ShortcutView{
		ui.Shortcut(ui.KeyN).Modifiers(ui.ModCommand).Enabled(d.page == "Ask" && !d.noteUI.saving).OnActivate(d.newNoteDocument),
		ui.Shortcut(ui.KeyC).Modifiers(ui.ModCommand).Enabled(d.memoryCopyText() != "").OnActivate(d.copyMemoryAnswer),
		ui.Shortcut(ui.KeyS).Modifiers(ui.ModCommand).Enabled(d.memoryNoteSaveText() != "").OnActivate(d.saveMemorySelectionAsNote),
		ui.Shortcut(ui.KeyS).Modifiers(ui.ModCommand).Enabled(d.page == "Ask" && d.noteUI.open && !d.noteUI.loading && !d.noteUI.saving).OnActivate(d.saveNoteDocument),
	}
}
